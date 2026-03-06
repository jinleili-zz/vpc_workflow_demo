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
	"github.com/paic/nsp-common/pkg/logger"
	"github.com/paic/nsp-common/pkg/taskqueue/asynqbroker"
	"github.com/paic/nsp-common/pkg/trace"
)

func main() {
	// Get region and az first for logger initialization
	region := os.Getenv("REGION")
	az := os.Getenv("AZ")

	// Initialize logger with service name
	serviceName := fmt.Sprintf("az-nsp-vpc-%s", az)
	logCfg := logger.DefaultConfig(serviceName)
	if err := logger.Init(logCfg); err != nil {
		fmt.Printf("Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	logger.Platform().Info("========================================")
	logger.Platform().Info("AZ NSP 启动中...")
	logger.Platform().Info("========================================")

	// 使用 nsp-common/config 加载配置
	// 支持从 config.yaml 文件加载，环境变量覆盖（NSP_前缀），以及热更新
	configLoader, err := config.NewConfigLoader("./config/config.yaml", "NSP", true)
	if err != nil {
		logger.Platform().Error("加载配置失败", "error", err)
		os.Exit(1)
	}
	defer configLoader.Close()

	cfg := configLoader.GetConfig()
	cfg.ServiceType = "az"

	// 从环境变量获取端口和必要配置（高优先级覆盖配置文件）
	port := os.Getenv("PORT")
	if port == "" {
		port = fmt.Sprintf("%d", cfg.Port)
	}

	topNSPAddr := os.Getenv("TOP_NSP_ADDR")
	if topNSPAddr == "" {
		topNSPAddr = cfg.AZNSP.TopNSPAddr
	}

	if region == "" {
		region = cfg.Region
	}
	if az == "" {
		az = cfg.AZ
	}

	if region == "" || az == "" {
		logger.Platform().Error("必须设置环境变量 REGION 和 AZ，或在配置文件中指定")
		os.Exit(1)
	}

	cfg.Region = region
	cfg.AZ = az
	cfg.AZNSP.TopNSPAddr = topNSPAddr

	portInt, _ := strconv.Atoi(port)
	cfg.Port = portInt

	logger.Platform().Info("[AZ NSP] 服务配置", "region", region, "az", az, "port", portInt)
	logger.Platform().Info("[AZ NSP] Top NSP地址", "addr", topNSPAddr)
	logger.Platform().Info("[AZ NSP] Redis配置", "host", cfg.Redis.Host, "port", cfg.Redis.Port)
	logger.Platform().Info("[AZ NSP] PostgreSQL配置", "host", cfg.PostgreSQL.Host, "port", cfg.PostgreSQL.Port)

	// 使用配置文件中的 PostgreSQL 配置构建 DSN
	dbName := fmt.Sprintf("nsp_%s_vpc", strings.ReplaceAll(az, "-", "_"))
	postgresDSN := cfg.GetPostgresDSN(dbName)
	logger.Platform().Info("[AZ NSP] PostgreSQL DSN", "database", dbName)

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
		logger.Platform().Info("等待 PostgreSQL 就绪...", "attempt", i+1)
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		logger.Platform().Error("PostgreSQL 连接失败", "error", err)
		os.Exit(1)
	}
	defer pgDB.Close()

	logger.Platform().Info("[AZ NSP] PostgreSQL 连接成功", "database", dbName)

	redisAddr := cfg.GetRedisAddr()
	redisBrokerDB := cfg.GetRedisBrokerDB()
	redisOpt := config.MakeAsynqRedisOpt(redisAddr, redisBrokerDB)

	// 创建 Broker（用于 orchestrator 入队任务）
	broker := asynqbroker.NewBroker(redisOpt)
	defer broker.Close()

	callbackQueueName := queue.GetCallbackQueueName(region, az)

	// 创建 Traced HTTP Client
	tracedHTTP := trace.NewTracedClient(nil)

	server := api.NewServer(cfg, broker, tracedHTTP, pgDB)

	if err := server.RegisterToTopNSP(); err != nil {
		logger.Platform().Info("[AZ NSP] 注册到Top NSP失败 (将在后续心跳中重试)", "az", az, "error", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go server.StartHeartbeat(ctx)

	// 创建 Consumer 消费回调队列
	callbackConsumer := asynqbroker.NewConsumer(redisOpt, asynqbroker.ConsumerConfig{
		Concurrency: 2,
		Queues: map[string]int{
			callbackQueueName: 10,
		},
	})

	callbackConsumer.HandleRaw("task_callback", func(ctx context.Context, t *asynq.Task) error {
		return server.HandleTaskCallback(ctx, t.Payload())
	})

	go func() {
		logger.Platform().Info("[AZ NSP] 回调处理器启动", "az", az, "queue", callbackQueueName)
		if err := callbackConsumer.Start(context.Background()); err != nil {
			logger.Platform().Error("[AZ NSP] 回调处理器启动失败", "az", az, "error", err)
			os.Exit(1)
		}
	}()

	go func() {
		addr := ":" + port
		logger.Platform().Info("[AZ NSP] API服务启动", "az", az, "port", port)
		if err := server.Run(addr); err != nil {
			logger.Platform().Error("[AZ NSP] API服务启动失败", "az", az, "error", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Platform().Info("[AZ NSP] 收到退出信号，正在关闭...", "az", az)

	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	callbackConsumer.Stop()

	<-shutdownCtx.Done()
	logger.Platform().Info("[AZ NSP] 服务已关闭", "az", az)
}
