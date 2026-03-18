package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"workflow_qoder/internal/config"
	"workflow_qoder/internal/logging"
	"workflow_qoder/internal/queue"
	"workflow_qoder/tasks"

	"github.com/jinleili-zz/nsp-platform/logger"
	"github.com/jinleili-zz/nsp-platform/taskqueue"
	"github.com/jinleili-zz/nsp-platform/taskqueue/asynqbroker"
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

	callbackQueueName := queue.GetCallbackQueueName(region, az, "vpc")
	queuesConfig := queue.GetQueueConfig(region, az, deviceType)

	// 创建 Broker 和 CallbackSender
	broker := asynqbroker.NewBroker(redisOpt)
	defer broker.Close()

	cbSender := taskqueue.NewCallbackSenderFromBroker(broker, callbackQueueName)

	// 创建 Consumer
	consumer := asynqbroker.NewConsumer(redisOpt, asynqbroker.ConsumerConfig{
		Concurrency:    workerCount,
		Queues:         queuesConfig,
		StrictPriority: true,
		Logger:         logging.GetAsynqAdapter().GetAsynqLogger(),
	})

	// 注册 task handler
	switch deviceType {
	case queue.DeviceTypeSwitch:
		consumer.Handle("create_vrf_on_switch", tasks.CreateVRFOnSwitchHandler(cbSender))
		consumer.Handle("create_vlan_subinterface", tasks.CreateVLANSubInterfaceHandler(cbSender))
		consumer.Handle("create_subnet_on_switch", tasks.CreateSubnetOnSwitchHandler(cbSender))
		consumer.Handle("configure_subnet_routing", tasks.ConfigureSubnetRoutingHandler(cbSender))
		consumer.Handle("create_pccn_connection", tasks.CreatePCCNConnectionHandler(cbSender))
		consumer.Handle("configure_pccn_routing", tasks.ConfigurePCCNRoutingHandler(cbSender))
	case queue.DeviceTypeFirewall:
		cbSenderVFW := taskqueue.NewCallbackSenderFromBroker(broker, queue.GetCallbackQueueName(region, az, "vfw"))
		consumer.Handle("create_firewall_zone", tasks.CreateFirewallZoneHandler(cbSender))
		consumer.Handle("create_firewall_policy", tasks.CreateFirewallPolicyHandler(cbSenderVFW))
	case queue.DeviceTypeLoadBalancer:
		consumer.Handle("create_lb_pool", tasks.CreateLBPoolHandler(cbSender))
		consumer.Handle("configure_lb_listener", tasks.ConfigureLBListenerHandler(cbSender))
	}

	taskQueueName := queue.GetQueueName(region, az, deviceType)

	go func() {
		logger.Platform().Info("Worker 启动", "region", region, "az", az, "workerType", workerType, "concurrency", workerCount, "taskQueue", taskQueueName, "callbackQueue", callbackQueueName)
		if err := consumer.Start(context.Background()); err != nil {
			logger.Platform().Error("Worker 启动失败", "region", region, "az", az, "workerType", workerType, "error", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Platform().Info("Worker 收到退出信号，正在关闭...", "region", region, "az", az, "workerType", workerType)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	consumer.Stop()

	<-ctx.Done()
	logger.Platform().Info("Worker 已关闭", "region", region, "az", az, "workerType", workerType)
}
