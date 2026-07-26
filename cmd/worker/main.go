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

	"workflow_qoder/internal/config"
	"workflow_qoder/internal/logging"
	"workflow_qoder/internal/queue"
	workerruntime "workflow_qoder/internal/worker"
	"workflow_qoder/tasks"

	"github.com/jinleili-zz/nsp-platform/logger"
	"github.com/jinleili-zz/nsp-platform/taskqueue"
	"github.com/jinleili-zz/nsp-platform/taskqueue/asynqbroker"
	_ "github.com/lib/pq"
)

func main() {
	cfg := config.LoadConfig()

	region := os.Getenv("REGION")
	az := os.Getenv("AZ")
	workerType := os.Getenv("WORKER_TYPE")

	if region == "" || az == "" || workerType == "" {
		fmt.Println("必须设置环境变量 REGION, AZ 和 WORKER_TYPE")
		os.Exit(1)
	}

	// 初始化 logger
	logCfg := logger.DefaultConfig(fmt.Sprintf("worker-%s", workerType))
	if os.Getenv("DEVELOPMENT") == "true" {
		logCfg = logger.DevelopmentConfig(fmt.Sprintf("worker-%s", workerType))
	}
	if err := logger.Init(logCfg); err != nil {
		panic("初始化日志失败: " + err.Error())
	}
	defer logger.Sync()

	logger.Platform().Info("========================================")
	logger.Platform().Info("Worker 启动中...")
	logger.Platform().Info("========================================")

	logger.Platform().Info("Worker 配置", "region", region, "az", az, "type", workerType)

	// 从环境变量覆盖 Redis 地址（支持集群格式）
	if redisAddrEnv := os.Getenv("REDIS_ADDR"); redisAddrEnv != "" {
		cfg.Redis.Host = redisAddrEnv
		cfg.Redis.Port = 0
	}
	if brokerDB := os.Getenv("REDIS_BROKER_DB"); brokerDB != "" {
		if v, err := strconv.Atoi(brokerDB); err == nil {
			cfg.Redis.BrokerDB = v
		}
	}
	if pgHost := os.Getenv("POSTGRES_HOST"); pgHost != "" {
		cfg.PostgreSQL.Host = pgHost
	}
	if pgPort := os.Getenv("POSTGRES_PORT"); pgPort != "" {
		if port, err := strconv.Atoi(pgPort); err == nil {
			cfg.PostgreSQL.Port = port
		}
	}
	if pgUser := os.Getenv("POSTGRES_USER"); pgUser != "" {
		cfg.PostgreSQL.User = pgUser
	}
	if pgPassword := os.Getenv("POSTGRES_PASSWORD"); pgPassword != "" {
		cfg.PostgreSQL.Password = pgPassword
	}

	redisAddr := cfg.GetRedisAddr()
	redisBrokerDB := cfg.GetRedisBrokerDB()
	redisOpt := config.MakeAsynqRedisOpt(redisAddr, redisBrokerDB)

	workerCount := 2
	if workerCountEnv := os.Getenv("WORKER_COUNT"); workerCountEnv != "" {
		if count, err := strconv.Atoi(workerCountEnv); err == nil {
			workerCount = count
		}
	}

	var deviceType queue.DeviceType
	switch workerType {
	case "switch":
		deviceType = queue.DeviceTypeSwitch
	case "loadbalancer":
		deviceType = queue.DeviceTypeLoadBalancer
	case "firewall":
		deviceType = queue.DeviceTypeFirewall
	default:
		logger.Platform().Error("不支持的 WORKER_TYPE", "workerType", workerType, "supported", "switch, loadbalancer, firewall")
		os.Exit(1)
	}

	queuesConfig := queue.GetQueueConfig(region, az, deviceType)

	// Worker Ledger is mandatory for v2 tasks. Switch/LB workers coordinate in
	// the AZ VPC database; firewall workers coordinate in the AZ VFW database.
	dbName := os.Getenv("POSTGRES_DB")
	if dbName == "" {
		suffix := "vpc"
		if deviceType == queue.DeviceTypeFirewall {
			suffix = "vfw"
		}
		dbName = fmt.Sprintf("nsp_%s_%s", strings.ReplaceAll(az, "-", "_"), suffix)
	}
	pgDB, err := sql.Open("postgres", cfg.GetPostgresDSN(dbName))
	if err != nil {
		logger.Platform().Error("Worker PostgreSQL连接失败", "error", err)
		os.Exit(1)
	}
	defer pgDB.Close()
	if err := pgDB.Ping(); err != nil {
		logger.Platform().Error("Worker PostgreSQL不可用", "database", dbName, "error", err)
		os.Exit(1)
	}

	// 创建原始 Broker；handler 使用 Runtime Broker 将 Reply 先写 Outbox。
	broker := asynqbroker.NewBroker(redisOpt)
	defer broker.Close()
	runtime := workerruntime.NewRuntime(pgDB, broker, fmt.Sprintf("worker-%s-%s-%s", workerType, region, az))
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	defer cancelWorker()
	go runtime.RunDispatcher(workerCtx, time.Second)

	// 创建 Consumer
	consumer := asynqbroker.NewConsumer(redisOpt, asynqbroker.ConsumerConfig{
		Concurrency:    workerCount,
		Queues:         queuesConfig,
		StrictPriority: true,
		Logger:         logging.GetAsynqAdapter().GetAsynqLogger(),
	})

	// 注册 task handler
	wrap := func(handler taskqueue.HandlerFunc) taskqueue.HandlerFunc {
		return tasks.ValidateTaskProtocol(runtime.Wrap(handler))
	}
	switch deviceType {
	case queue.DeviceTypeSwitch:
		consumer.Handle("create_vrf_on_switch", wrap(tasks.CreateVRFOnSwitchHandler(runtime)))
		consumer.Handle("create_vlan_subinterface", wrap(tasks.CreateVLANSubInterfaceHandler(runtime)))
		consumer.Handle("create_subnet_on_switch", wrap(tasks.CreateSubnetOnSwitchHandler(runtime)))
		consumer.Handle("configure_subnet_routing", wrap(tasks.ConfigureSubnetRoutingHandler(runtime)))
		consumer.Handle("create_pccn_connection", wrap(tasks.CreatePCCNConnectionHandler(runtime)))
		consumer.Handle("configure_pccn_routing", wrap(tasks.ConfigurePCCNRoutingHandler(runtime)))
		consumer.Handle("delete_pccn_connection", wrap(tasks.DeletePCCNConnectionHandler(runtime)))
	case queue.DeviceTypeFirewall:
		consumer.Handle("create_firewall_zone", wrap(tasks.CreateFirewallZoneHandler(runtime)))
		consumer.Handle("create_firewall_policy", wrap(tasks.CreateFirewallPolicyHandler(runtime)))
	case queue.DeviceTypeLoadBalancer:
		consumer.Handle("create_lb_pool", wrap(tasks.CreateLBPoolHandler(runtime)))
		consumer.Handle("configure_lb_listener", wrap(tasks.ConfigureLBListenerHandler(runtime)))
	}

	taskQueueName := queue.GetQueueName(region, az, deviceType)

	go func() {
		logger.Platform().Info("Worker 启动", "region", region, "az", az, "workerType", workerType, "concurrency", workerCount, "taskQueue", taskQueueName)
		if err := consumer.Start(workerCtx); err != nil {
			logger.Platform().Error("Worker 启动失败", "region", region, "az", az, "workerType", workerType, "error", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Platform().Info("Worker 收到退出信号，正在关闭...", "region", region, "az", az, "workerType", workerType)
	cancelWorker()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	consumer.Stop()

	<-ctx.Done()
	logger.Platform().Info("Worker 已关闭", "region", region, "az", az, "workerType", workerType)
}
