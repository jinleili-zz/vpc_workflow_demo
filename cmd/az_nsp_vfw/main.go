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
	"workflow_qoder/internal/az/vfw/orchestrator"
	"workflow_qoder/internal/config"
	"workflow_qoder/internal/queue"

	"github.com/hibiken/asynq"
	_ "github.com/lib/pq"
	"github.com/jinleili-zz/nsp-platform/logger"
	"github.com/jinleili-zz/nsp-platform/taskqueue"
	"github.com/jinleili-zz/nsp-platform/taskqueue/asynqbroker"
	"github.com/jinleili-zz/nsp-platform/trace"
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

	logger.Platform().Info("========================================")
	logger.Platform().Info("AZ NSP VFW 启动中...")
	logger.Platform().Info("========================================")

	port := 8080
	if portStr := os.Getenv("PORT"); portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil {
			port = p
		}
	}
	cfg.Port = port

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
	var err error
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

	callbackQueueName := queue.GetCallbackQueueName(region, az, "vfw")

	// 创建临时 orchestrator 实例以获取 WorkflowHooks
	tmpOrch := orchestrator.NewVFWOrchestrator(pgDB, nil, nil, region, az)
	hooks := tmpOrch.BuildWorkflowHooks()

	// 创建 Engine（使用 NewEngineWithStore 以复用 pgDB 连接）
	engineStore := taskqueue.NewPostgresStore(pgDB)
	engine := taskqueue.NewEngineWithStore(&taskqueue.Config{
		CallbackQueue: callbackQueueName,
		QueueRouter: func(queueTag string, priority taskqueue.Priority) string {
			deviceType := queue.DeviceType(queueTag)
			return queue.GetPriorityQueueName(region, az, deviceType, queue.TaskPriority(priority))
		},
		Hooks: hooks,
	}, broker, engineStore)

	// 运行数据库迁移（创建 tq_workflows + tq_steps 表）
	if err := engine.Migrate(context.Background()); err != nil {
		logger.Platform().Error("Engine 数据库迁移失败", "error", err)
		os.Exit(1)
	}
	logger.Platform().Info("[AZ NSP VFW] Engine 数据库迁移完成")

	// 创建 Traced HTTP Client
	tracedHTTP := trace.NewTracedClient(nil)

	server := api.NewServer(cfg, engine, tracedHTTP, pgDB)

	// 创建 Consumer 消费回调队列
	callbackConsumer := asynqbroker.NewConsumer(redisOpt, asynqbroker.ConsumerConfig{
		Concurrency: 10,
		Queues: map[string]int{
			callbackQueueName: 10,
		},
	})

	callbackConsumer.HandleRaw("task_callback", func(ctx context.Context, t *asynq.Task) error {
		return server.HandleTaskCallback(ctx, t.Payload())
	})

	go func() {
		logger.Platform().Info("[AZ NSP VFW] 回调处理器启动", "az", az, "queue", callbackQueueName)
		if err := callbackConsumer.Start(context.Background()); err != nil {
			logger.Platform().Error("[AZ NSP VFW] 回调消费者启动失败", "error", err)
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
	callbackConsumer.Stop()
	logger.Platform().Info("[AZ NSP VFW] 已关闭")
}
