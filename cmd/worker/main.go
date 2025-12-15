package main

import (
	"context"
	"log"
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
)

func main() {
	log.Println("========================================")
	log.Println("Worker 启动中...")
	log.Println("========================================")

	cfg := config.LoadConfig()

	region := os.Getenv("REGION")
	az := os.Getenv("AZ")
	workerType := os.Getenv("WORKER_TYPE")

	if region == "" || az == "" || workerType == "" {
		log.Fatal("必须设置环境变量 REGION, AZ 和 WORKER_TYPE")
	}

	log.Printf("[Worker] Region=%s, AZ=%s, Type=%s", region, az, workerType)

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
		log.Fatalf("不支持的 WORKER_TYPE: %s (支持: switch, loadbalancer, firewall)", workerType)
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
	case queue.DeviceTypeLoadBalancer:
		mux.HandleFunc("create_lb_pool", tasks.CreateLBPoolHandler(asynqClient, callbackQueueName))
		mux.HandleFunc("configure_lb_listener", tasks.ConfigureLBListenerHandler(asynqClient, callbackQueueName))
	}

	go func() {
		log.Printf("[Worker %s-%s-%s] 启动, 并发数=%d, 任务队列=%s, 回调队列=%s", region, az, workerType, workerCount, taskQueueName, callbackQueueName)
		if err := asynqServer.Run(mux); err != nil {
			log.Fatalf("[Worker %s-%s-%s] 启动失败: %v", region, az, workerType, err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Printf("[Worker %s-%s-%s] 收到退出信号，正在关闭...", region, az, workerType)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	asynqServer.Shutdown()

	<-ctx.Done()
	log.Printf("[Worker %s-%s-%s] 已关闭", region, az, workerType)
}