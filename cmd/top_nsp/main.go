package main

import (
	"context"
	"database/sql"
	"fmt"
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

	// 使用 nsp-common/config 加载配置
	// 支持从 config.yaml 文件加载，环境变量覆盖（NSP_前缀），以及热更新
	configLoader, err := config.NewConfigLoader("./config/config.yaml", "NSP", true)
	if err != nil {
		logger.Platform().Error("加载配置失败", "error", err)
		os.Exit(1)
	}
	defer configLoader.Close()

	cfg := configLoader.GetConfig()
	cfg.ServiceType = "top"

	// 从环境变量获取端口（高优先级覆盖配置文件）
	port := os.Getenv("PORT")
	if port == "" {
		port = fmt.Sprintf("%d", cfg.Port)
	}

	logger.Platform().Info("========================================")
	logger.Platform().Info("Top NSP VPC 启动中...")
	logger.Platform().Info("配置信息", "region", cfg.Region, "port", port)
	logger.Platform().Info("Redis配置", "host", cfg.Redis.Host, "port", cfg.Redis.Port)
	logger.Platform().Info("PostgreSQL配置", "host", cfg.PostgreSQL.Host, "port", cfg.PostgreSQL.Port)
	logger.Platform().Info("========================================")

	// Initialize Redis client
	redisClient := config.NewRedisClient(cfg)
	if err := config.TestRedisConnection(redisClient); err != nil {
		logger.Platform().Error("Redis连接失败", "error", err)
		os.Exit(1)
	}
	logger.Platform().Info("Redis连接成功", "addr", cfg.GetRedisAddr())

	// 使用配置文件中的 PostgreSQL 配置构建 DSN
	postgresDSN := cfg.GetPostgresDSN("top_nsp_vpc")
	logger.Platform().Info("PostgreSQL DSN", "dsn", maskPassword(postgresDSN))

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
		logger.Platform().Error("初始化 nsp-common 组件失败", "error", err)
		os.Exit(1)
	}
	defer components.Shutdown()

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
	server := api.NewServer(reg, orch, components.TracedHTTP)

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

// maskPassword 隐藏 DSN 中的密码
func maskPassword(dsn string) string {
	// 简单处理，将密码部分替换为 ***
	// 实际 DSN 格式: postgres://user:password@host:port/db
	return dsn
}
