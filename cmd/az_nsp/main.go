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

	"workflow_qoder/internal/az/api"
	"workflow_qoder/internal/config"
	"workflow_qoder/internal/queue"

	"github.com/hibiken/asynq"
	_ "github.com/lib/pq"
	"github.com/yourorg/nsp-common/pkg/logger"
)

func getEnvOrDefault(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

func main() {
	logger.Info("========================================")
	logger.Info("AZ NSP 启动中...")
	logger.Info("========================================")

	cfg := config.LoadConfig()
	cfg.ServiceType = "az"

	region := os.Getenv("REGION")
	az := os.Getenv("AZ")
	port := os.Getenv("PORT")
	topNSPAddr := os.Getenv("TOP_NSP_ADDR")

	if region == "" || az == "" {
		logger.Error("必须设置环境变量 REGION 和 AZ")
		os.Exit(1)
	}

	if topNSPAddr == "" {
		topNSPAddr = "http://top-nsp:8080"
	}

	if port == "" {
		port = "8080"
	}

	portInt, _ := strconv.Atoi(port)

	cfg.Region = region
	cfg.AZ = az
	cfg.Port = portInt
	cfg.AZNSP.TopNSPAddr = topNSPAddr

	logger.Info("[AZ NSP] 服务配置", "region", region, "az", az, "port", portInt)
	logger.Info("[AZ NSP] Top NSP地址", "addr", topNSPAddr)

	// Build PostgreSQL DSN
	pgHost := getEnvOrDefault("POSTGRES_HOST", "postgres")
	pgPort := getEnvOrDefault("POSTGRES_PORT", "5432")
	pgUser := getEnvOrDefault("POSTGRES_USER", "nsp_user")
	pgPassword := getEnvOrDefault("POSTGRES_PASSWORD", "nsp_password")
	dbName := fmt.Sprintf("nsp_%s_%s_vpc", strings.ReplaceAll(region, "-", "_"), strings.ReplaceAll(az, "-", "_"))
	postgresDSN := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", pgUser, pgPassword, pgHost, pgPort, dbName)

	// Connect to PostgreSQL
	var pgDB *sql.DB
	var err error
	for i := 0; i < 30; i++ {
		pgDB, err = sql.Open("postgres", postgresDSN)
		if err == nil {
			if err = pgDB.Ping(); err == nil {
				break
			}
			pgDB.Close()
		}
		logger.Info("等待 PostgreSQL 就绪...", "attempt", i+1)
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		logger.Error("PostgreSQL 连接失败", "error", err)
		os.Exit(1)
	}
	defer pgDB.Close()

	logger.Info("[AZ NSP] PostgreSQL 连接成功", "database", dbName)

	redisAddr := cfg.GetRedisAddr()
	redisBrokerDB := cfg.GetRedisBrokerDB()

	addrs := strings.Split(redisAddr, ",")
	var asynqClientOpt asynq.RedisConnOpt
	
	if len(addrs) > 1 {
		asynqClientOpt = asynq.RedisClusterClientOpt{
			Addrs: addrs,
		}
	} else {
		asynqClientOpt = asynq.RedisClientOpt{
			Addr: redisAddr,
			DB:   redisBrokerDB,
		}
	}

	asynqClient := asynq.NewClient(asynqClientOpt)
	defer asynqClient.Close()

	var asynqInspectorOpt asynq.RedisConnOpt
	if len(addrs) > 1 {
		asynqInspectorOpt = asynq.RedisClusterClientOpt{
			Addrs: addrs,
		}
	} else {
		asynqInspectorOpt = asynq.RedisClientOpt{
			Addr: redisAddr,
			DB:   redisBrokerDB,
		}
	}

	asynqInspector := asynq.NewInspector(asynqInspectorOpt)
	defer asynqInspector.Close()

	callbackQueueName := queue.GetCallbackQueueName(region, az)

	server := api.NewServer(cfg, asynqClient, asynqInspector, pgDB)

	if err := server.RegisterToTopNSP(); err != nil {
		logger.Info("[AZ NSP] 注册到Top NSP失败 (将在后续心跳中重试)", "az", az, "error", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go server.StartHeartbeat(ctx)

	var asynqServerOpt asynq.RedisConnOpt
	if len(addrs) > 1 {
		asynqServerOpt = asynq.RedisClusterClientOpt{
			Addrs: addrs,
		}
	} else {
		asynqServerOpt = asynq.RedisClientOpt{
			Addr: redisAddr,
			DB:   redisBrokerDB,
		}
	}

	asynqServer := asynq.NewServer(
		asynqServerOpt,
		asynq.Config{
			Concurrency: 2,
			Queues: map[string]int{
				callbackQueueName: 10,
			},
		},
	)

	mux := asynq.NewServeMux()
	mux.HandleFunc("task_callback", server.HandleTaskCallback())

	go func() {
		logger.Info("[AZ NSP] 回调处理器启动", "az", az, "queue", callbackQueueName)
		if err := asynqServer.Run(mux); err != nil {
			logger.Error("[AZ NSP] 回调处理器启动失败", "az", az, "error", err)
			os.Exit(1)
		}
	}()

	go func() {
		addr := ":" + port
		logger.Info("[AZ NSP] API服务启动", "az", az, "port", port)
		if err := server.Run(addr); err != nil {
			logger.Error("[AZ NSP] API服务启动失败", "az", az, "error", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("[AZ NSP] 收到退出信号，正在关闭...", "az", az)

	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	asynqServer.Shutdown()

	<-shutdownCtx.Done()
	logger.Info("[AZ NSP] 服务已关闭", "az", az)
}
