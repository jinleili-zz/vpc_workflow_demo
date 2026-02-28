package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"workflow_qoder/internal/config"
	"workflow_qoder/internal/queue"
	"workflow_qoder/tasks"

	"github.com/hibiken/asynq"
	"github.com/yourorg/nsp-common/pkg/logger"
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

	logger.Info("========================================")
	logger.Info("Worker 启动中...")
	logger.Info("========================================")

	logger.Info("Worker 配置", "region", region, "az", az, "type", workerType)

	redisAddr := cfg.GetRedisAddr()
	redisBrokerDB := cfg.GetRedisBrokerDB()

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
		logger.Error("不支持的 WORKER_TYPE", "workerType", workerType, "supported", "switch, loadbalancer, firewall")
		os.Exit(1)
	}

	taskQueueName := queue.GetQueueName(region, az, deviceType)
	callbackQueueName := queue.GetCallbackQueueName(region, az)

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

	queuesConfig := queue.GetQueueConfig(region, az, deviceType)

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
			Concurrency:    workerCount,
			Queues:         queuesConfig,
			StrictPriority: true,
		},
	)

	mux := asynq.NewServeMux()

	switch deviceType {
	case queue.DeviceTypeSwitch:
		mux.HandleFunc("create_vrf_on_switch", tasks.CreateVRFOnSwitchHandler(asynqClient, callbackQueueName))
		mux.HandleFunc("create_vlan_subinterface", tasks.CreateVLANSubInterfaceHandler(asynqClient, callbackQueueName))
		mux.HandleFunc("create_subnet_on_switch", tasks.CreateSubnetOnSwitchHandler(asynqClient, callbackQueueName))
		mux.HandleFunc("configure_subnet_routing", tasks.ConfigureSubnetRoutingHandler(asynqClient, callbackQueueName))
	case queue.DeviceTypeFirewall:
		mux.HandleFunc("create_firewall_zone", tasks.CreateFirewallZoneHandler(asynqClient, callbackQueueName))
		mux.HandleFunc("create_firewall_policy", tasks.CreateFirewallPolicyHandler(asynqClient, callbackQueueName+"_vfw"))
	case queue.DeviceTypeLoadBalancer:
		mux.HandleFunc("create_lb_pool", tasks.CreateLBPoolHandler(asynqClient, callbackQueueName))
		mux.HandleFunc("configure_lb_listener", tasks.ConfigureLBListenerHandler(asynqClient, callbackQueueName))
	}

	go func() {
		logger.Info("Worker 启动", "region", region, "az", az, "workerType", workerType, "concurrency", workerCount, "taskQueue", taskQueueName, "callbackQueue", callbackQueueName)
		if err := asynqServer.Run(mux); err != nil {
			logger.Error("Worker 启动失败", "region", region, "az", az, "workerType", workerType, "error", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("Worker 收到退出信号，正在关闭...", "region", region, "az", az, "workerType", workerType)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	asynqServer.Shutdown()

	<-ctx.Done()
	logger.Info("Worker 已关闭", "region", region, "az", az, "workerType", workerType)
}