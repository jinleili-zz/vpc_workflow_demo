package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"workflow_qoder/internal/config"
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

	taskQueueName := "vpc_tasks_" + region + "_" + az
	callbackQueueName := "vpc_callbacks_" + region + "_" + az

	asynqClient := asynq.NewClient(asynq.RedisClientOpt{
		Addr: redisAddr,
		DB:   redisBrokerDB,
	})
	defer asynqClient.Close()

	asynqServer := asynq.NewServer(
		asynq.RedisClientOpt{
			Addr: redisAddr,
			DB:   redisBrokerDB,
		},
		asynq.Config{
			Concurrency: workerCount,
			Queues: map[string]int{
				taskQueueName: 10,
			},
		},
	)

	mux := asynq.NewServeMux()
	mux.HandleFunc("create_vrf_on_switch", tasks.CreateVRFOnSwitchHandler(asynqClient, callbackQueueName))
	mux.HandleFunc("create_vlan_subinterface", tasks.CreateVLANSubInterfaceHandler(asynqClient, callbackQueueName))
	mux.HandleFunc("create_firewall_zone", tasks.CreateFirewallZoneHandler(asynqClient, callbackQueueName))
	mux.HandleFunc("create_subnet_on_switch", tasks.CreateSubnetOnSwitchHandler(asynqClient, callbackQueueName))
	mux.HandleFunc("configure_subnet_routing", tasks.ConfigureSubnetRoutingHandler(asynqClient, callbackQueueName))

	go func() {
		log.Printf("[Worker %s-%s] 启动, 并发数=%d, 任务队列=%s, 回调队列=%s", region, az, workerCount, taskQueueName, callbackQueueName)
		if err := asynqServer.Run(mux); err != nil {
			log.Fatalf("[Worker %s-%s] 启动失败: %v", region, az, err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Printf("[Worker %s-%s] 收到退出信号，正在关闭...", region, az)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	asynqServer.Shutdown()

	<-ctx.Done()
	log.Printf("[Worker %s-%s] 已关闭", region, az)
}