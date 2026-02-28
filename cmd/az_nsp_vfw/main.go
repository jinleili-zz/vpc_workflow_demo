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
	"workflow_qoder/internal/config"

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

	// 初始化 logger
	logCfg := logger.DefaultConfig(fmt.Sprintf("az-nsp-vfw-%s", az))
	if os.Getenv("DEVELOPMENT") == "true" {
		logCfg = logger.DevelopmentConfig(fmt.Sprintf("az-nsp-vfw-%s", az))
	}
	if err := logger.Init(logCfg); err != nil {
		panic("初始化日志失败: " + err.Error())
	}
	defer logger.Sync()

	logger.Info("========================================")
	logger.Info("AZ NSP VFW 启动中...")
	logger.Info("========================================")

	port := 8080
	if portStr := os.Getenv("PORT"); portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil {
			port = p
		}
	}
	cfg.Port = port

	logger.Info("[AZ NSP VFW] 服务配置", "region", region, "az", az, "port", port)

	// Build PostgreSQL DSN
	pgHost := getEnvOrDefault("POSTGRES_HOST", "postgres")
	pgPort := getEnvOrDefault("POSTGRES_PORT", "5432")
	pgUser := getEnvOrDefault("POSTGRES_USER", "nsp_user")
	pgPassword := getEnvOrDefault("POSTGRES_PASSWORD", "nsp_password")
	dbName := fmt.Sprintf("nsp_%s_%s_vfw", strings.ReplaceAll(region, "-", "_"), strings.ReplaceAll(az, "-", "_"))
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
		logger.Info("[AZ NSP VFW] 等待 PostgreSQL 就绪...", "attempt", i+1)
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		logger.Error("PostgreSQL 连接失败", "error", err)
		os.Exit(1)
	}
	defer pgDB.Close()

	logger.Info("[AZ NSP VFW] PostgreSQL 连接成功", "database", dbName)

	redisAddr := cfg.GetRedisAddr()
	addrs := strings.Split(redisAddr, ",")

	var asynqClientOpt asynq.RedisConnOpt
	if len(addrs) > 1 {
		asynqClientOpt = asynq.RedisClusterClientOpt{
			Addrs: addrs,
		}
	} else {
		asynqClientOpt = asynq.RedisClientOpt{
			Addr: redisAddr,
			DB:   cfg.GetRedisBrokerDB(),
		}
	}

	asynqClient := asynq.NewClient(asynqClientOpt)
	defer asynqClient.Close()

	server := api.NewServer(cfg, asynqClient, pgDB)

	callbackQueueName := server.GetCallbackQueueName()

	var asynqServerOpt asynq.RedisConnOpt
	if len(addrs) > 1 {
		asynqServerOpt = asynq.RedisClusterClientOpt{
			Addrs: addrs,
		}
	} else {
		asynqServerOpt = asynq.RedisClientOpt{
			Addr: redisAddr,
			DB:   cfg.GetRedisBrokerDB(),
		}
	}

	asynqServer := asynq.NewServer(
		asynqServerOpt,
		asynq.Config{
			Concurrency: 10,
			Queues: map[string]int{
				callbackQueueName: 10,
			},
		},
	)

	mux := asynq.NewServeMux()
	mux.HandleFunc("task_callback", server.HandleTaskCallback())

	go func() {
		if err := asynqServer.Run(mux); err != nil {
			logger.Error("[AZ NSP VFW] Asynq服务器启动失败", "error", err)
		}
	}()

	time.Sleep(2 * time.Second)

	for i := 0; i < 10; i++ {
		if err := server.RegisterToTopNSP(); err != nil {
			logger.Info("[AZ NSP VFW] 注册失败，重试中...", "attempt", i+1, "error", err)
			time.Sleep(3 * time.Second)
			continue
		}
		break
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go server.StartHeartbeat(ctx)

	go func() {
		addr := fmt.Sprintf(":%d", port)
		if err := server.Run(addr); err != nil {
			logger.Error("[AZ NSP VFW] 服务启动失败", "error", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("[AZ NSP VFW] 正在关闭...")
	cancel()
	asynqServer.Shutdown()
	logger.Info("[AZ NSP VFW] 已关闭")
}
