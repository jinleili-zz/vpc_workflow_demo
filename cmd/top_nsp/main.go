package main

import (
	"context"
	"database/sql"
	"os"
	"os/signal"
	"syscall"
	"time"

	"workflow_qoder/internal/bootstrap"
	"workflow_qoder/internal/config"
	"workflow_qoder/internal/top/api"
	"workflow_qoder/internal/top/orchestrator"
	"workflow_qoder/internal/top/registry"

	"github.com/yourorg/nsp-common/pkg/logger"

	_ "github.com/lib/pq"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Load configuration
	cfg := config.LoadConfig()
	cfg.ServiceType = "top"

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// Initialize Redis client
	redisClient := config.NewRedisClient(cfg)
	if err := config.TestRedisConnection(redisClient); err != nil {
		panic("Redis连接失败: " + err.Error())
	}

	// Build PostgreSQL DSN
	pgHost := getEnvOrDefault("POSTGRES_HOST", "postgres")
	pgPort := getEnvOrDefault("POSTGRES_PORT", "5432")
	pgUser := getEnvOrDefault("POSTGRES_USER", "nsp_user")
	pgPassword := getEnvOrDefault("POSTGRES_PASSWORD", "nsp_password")
	pgDB := getEnvOrDefault("POSTGRES_DB", "top_nsp_vpc")
	postgresDSN := buildPostgresDSN(pgHost, pgPort, pgUser, pgPassword, pgDB)

	// Initialize nsp-common components
	bootstrapCfg := bootstrap.DefaultConfig("top-nsp-vpc")
	bootstrapCfg.PostgresDSN = postgresDSN
	bootstrapCfg.EnableSaga = true
	bootstrapCfg.EnableAuth = false // Disable auth for testing
	bootstrapCfg.SkipAuthPaths = []string{
		"/api/v1/health",
		"/api/v1/register/az",
		"/api/v1/heartbeat",
	}

	components, err := bootstrap.Initialize(ctx, bootstrapCfg)
	if err != nil {
		panic("初始化 nsp-common 组件失败: " + err.Error())
	}
	defer components.Shutdown()

	logger.Platform().Info("========================================")
	logger.Platform().Info("Top NSP VPC 启动中...")
	logger.Platform().Info("========================================")

	// Wait for PostgreSQL with retry
	topDB := waitForPostgres(postgresDSN, 30)
	if topDB != nil {
		defer topDB.Close()
		logger.Platform().Info("PostgreSQL 连接成功")
	} else {
		logger.Platform().Warn("PostgreSQL 连接失败，将不同步拓扑")
	}

	// Initialize registry
	reg := registry.NewRegistry(redisClient)

	// Initialize orchestrator with SAGA engine
	orch := orchestrator.NewOrchestrator(reg, topDB, components.SagaEngine, components.TracedHTTP)

	// Initialize API server
	server := api.NewServer(reg, orch)

	// Setup middlewares (trace, auth, logger) BEFORE routes
	components.SetupGinMiddlewares(server.Engine())
	
	// Now setup routes AFTER middlewares
	server.SetupRoutes()

	// Start server
	addr := ":" + port
	logger.Platform().Info("启动服务", "port", port)

	// Graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		logger.Platform().Info("收到关闭信号，正在优雅关闭...")
		cancel()
	}()

	if err := server.Run(addr); err != nil {
		logger.Platform().Error("服务启动失败", "error", err)
		os.Exit(1)
	}
}

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func buildPostgresDSN(host, port, user, password, dbname string) string {
	return "postgres://" + user + ":" + password + "@" + host + ":" + port + "/" + dbname + "?sslmode=disable"
}

func waitForPostgres(dsn string, maxRetries int) *sql.DB {
	for i := 0; i < maxRetries; i++ {
		db, err := sql.Open("postgres", dsn)
		if err == nil {
			if err = db.Ping(); err == nil {
				return db
			}
			db.Close()
		}
		logger.Platform().Info("等待 PostgreSQL 就绪...", "attempt", i+1, "max", maxRetries)
		time.Sleep(2 * time.Second)
	}
	return nil
}
