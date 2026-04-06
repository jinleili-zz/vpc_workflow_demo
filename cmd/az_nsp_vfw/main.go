package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"workflow_qoder/internal/az/vfw/api"
	"workflow_qoder/internal/bootstrap"
	"workflow_qoder/internal/config"
	"workflow_qoder/internal/orchestration"

	"github.com/jinleili-zz/nsp-platform/logger"
	"github.com/jinleili-zz/nsp-platform/taskqueue/asynqbroker"
	_ "github.com/lib/pq"
)

func getEnvOrDefault(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

func main() {
	cfg := config.LoadConfig()

	region := os.Getenv("REGION")
	az := os.Getenv("AZ")
	if region == "" || az == "" {
		fmt.Println("必须设置环境变量 REGION 和 AZ")
		os.Exit(1)
	}
	cfg.Region = region
	cfg.AZ = az
	cfg.ServiceType = "az"

	port := 8080
	if portStr := os.Getenv("PORT"); portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil {
			port = p
		}
	}
	cfg.Port = port

	authCreds, err := cfg.AuthCredentials()
	if err != nil {
		fmt.Printf("解析认证配置失败: %v\n", err)
		os.Exit(1)
	}

	bootstrapCfg := bootstrap.DefaultConfig(fmt.Sprintf("az-nsp-vfw-%s", az))
	bootstrapCfg.EnableAuth = cfg.Auth.EnableAuth
	bootstrapCfg.EnableSaga = false
	bootstrapCfg.Credentials = authCreds
	bootstrapCfg.SkipAuthPaths = cfg.Auth.SkipAuthPaths

	components, err := bootstrap.Initialize(context.Background(), bootstrapCfg)
	if err != nil {
		fmt.Printf("初始化基础组件失败: %v\n", err)
		os.Exit(1)
	}
	defer components.Shutdown()

	logger.Platform().Info("========================================")
	logger.Platform().Info("AZ NSP VFW 启动中...")
	logger.Platform().Info("========================================")

	logger.Platform().Info("[AZ NSP VFW] 服务配置", "region", region, "az", az, "port", port)

	// Build PostgreSQL DSN
	pgHost := getEnvOrDefault("POSTGRES_HOST", "postgres")
	pgPort := getEnvOrDefault("POSTGRES_PORT", "5432")
	pgUser := getEnvOrDefault("POSTGRES_USER", "nsp_user")
	pgPassword := getEnvOrDefault("POSTGRES_PASSWORD", "nsp_password")
	dbName := fmt.Sprintf("nsp_%s_vfw", strings.ReplaceAll(az, "-", "_"))
	postgresDSN := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", pgUser, pgPassword, pgHost, pgPort, dbName)

	// Connect to PostgreSQL
	var pgDB *sql.DB
	for i := 0; i < 30; i++ {
		pgDB, err = sql.Open("postgres", postgresDSN)
		if err == nil {
			if err = pgDB.Ping(); err == nil {
				break
			}
			pgDB.Close()
		}
		logger.Platform().Info("[AZ NSP VFW] 等待 PostgreSQL 就绪...", "attempt", i+1)
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		logger.Platform().Error("PostgreSQL 连接失败", "error", err)
		os.Exit(1)
	}
	defer pgDB.Close()

	logger.Platform().Info("[AZ NSP VFW] PostgreSQL 连接成功", "database", dbName)

	redisAddr := cfg.GetRedisAddr()
	redisBrokerDB := cfg.GetRedisBrokerDB()

	// 从环境变量覆盖 Redis 地址（支持集群格式）
	if redisAddrEnv := os.Getenv("REDIS_ADDR"); redisAddrEnv != "" {
		redisAddr = redisAddrEnv
	}
	if brokerDB := os.Getenv("REDIS_BROKER_DB"); brokerDB != "" {
		if v, err := strconv.Atoi(brokerDB); err == nil {
			redisBrokerDB = v
		}
	}

	redisOpt := config.MakeAsynqRedisOpt(redisAddr, redisBrokerDB)

	// 创建 Broker
	broker := asynqbroker.NewBroker(redisOpt)
	defer broker.Close()

	inspector := asynqbroker.NewInspector(redisOpt)
	defer inspector.Close()

	server := api.NewServer(cfg, broker, inspector, components.TracedHTTP, pgDB, components.Verifier)

	// 创建 Consumer 消费 reply 队列
	replyConsumer := asynqbroker.NewConsumer(redisOpt, asynqbroker.ConsumerConfig{
		Concurrency: 10,
		Queues: map[string]int{
			server.ReplyQueueName(): 10,
		},
	})

	replyConsumer.Handle(orchestration.ReplyTaskType, server.HandleReplyTask)

	go func() {
		logger.Platform().Info("[AZ NSP VFW] Reply Consumer启动", "az", az, "queue", server.ReplyQueueName())
		if err := replyConsumer.Start(context.Background()); err != nil {
			logger.Platform().Error("[AZ NSP VFW] Reply Consumer启动失败", "error", err)
		}
	}()

	time.Sleep(2 * time.Second)

	for i := 0; i < 10; i++ {
		if err := server.RegisterToTopNSP(); err != nil {
			logger.Platform().Info("[AZ NSP VFW] 注册失败，重试中...", "attempt", i+1, "error", err)
			time.Sleep(3 * time.Second)
			continue
		}
		break
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 启动补偿任务（每30秒检查一次工作流与策略状态不一致的情况）
	server.StartCompensationTask(ctx, 30*time.Second)

	go server.StartHeartbeat(ctx)

	go func() {
		addr := fmt.Sprintf(":%d", port)
		logger.Platform().Info("[AZ NSP VFW] API服务启动", "az", az, "port", port)
		if err := server.Run(addr); err != nil {
			logger.Platform().Error("[AZ NSP VFW] 服务启动失败", "error", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Platform().Info("[AZ NSP VFW] 正在关闭...")
	cancel()
	replyConsumer.Stop()
	logger.Platform().Info("[AZ NSP VFW] 已关闭")
}
