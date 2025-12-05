package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"workflow_qoder/internal/az/api"
	"workflow_qoder/internal/config"
	"workflow_qoder/tasks"

	"github.com/hibiken/asynq"
)

func main() {
	log.Println("========================================")
	log.Println("AZ NSP 启动中...")
	log.Println("========================================")

	cfg := config.LoadConfig()
	cfg.ServiceType = "az"

	region := os.Getenv("REGION")
	az := os.Getenv("AZ")
	port := os.Getenv("PORT")
	topNSPAddr := os.Getenv("TOP_NSP_ADDR")

	if region == "" || az == "" {
		log.Fatal("必须设置环境变量 REGION 和 AZ")
	}

	if topNSPAddr == "" {
		topNSPAddr = "http://top-nsp:8080"
	}

	if port == "" {
		port = "8080"
	}

	portInt, _ := strconv.Atoi(port)

	cfg.Region = region
	cfg.AZ = az
	cfg.Port = portInt
	cfg.AZNSP.TopNSPAddr = topNSPAddr

	log.Printf("[AZ NSP] Region=%s, AZ=%s, Port=%d", region, az, portInt)
	log.Printf("[AZ NSP] Top NSP地址: %s", topNSPAddr)

	redisAddr := cfg.GetRedisAddr()
	redisBrokerDB := cfg.GetRedisBrokerDB()

	asynqClient := asynq.NewClient(asynq.RedisClientOpt{
		Addr: redisAddr,
		DB:   redisBrokerDB,
	})
	defer asynqClient.Close()

	redisClient := config.NewRedisClient(cfg)
	if err := config.TestRedisConnection(redisClient); err != nil {
		log.Fatalf("Redis连接失败: %v", err)
	}

	queueName := "vpc_tasks_" + region + "_" + az

	server := api.NewServer(cfg, asynqClient, redisClient, queueName)

	if err := server.RegisterToTopNSP(); err != nil {
		log.Printf("[AZ NSP %s] 注册到Top NSP失败: %v (将在后续心跳中重试)", az, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go server.StartHeartbeat(ctx)

	workerCount := 2
	if workerCountEnv := os.Getenv("WORKER_COUNT"); workerCountEnv != "" {
		if count, err := strconv.Atoi(workerCountEnv); err == nil {
			workerCount = count
		}
	}

	asynqServer := asynq.NewServer(
		asynq.RedisClientOpt{
			Addr: redisAddr,
			DB:   redisBrokerDB,
		},
		asynq.Config{
			Concurrency: workerCount,
			Queues: map[string]int{
				queueName: 10,
			},
		},
	)

	mux := asynq.NewServeMux()
	mux.HandleFunc("create_vrf_on_switch", tasks.CreateVRFOnSwitchHandler(asynqClient, queueName, redisClient))
	mux.HandleFunc("create_vlan_subinterface", tasks.CreateVLANSubInterfaceHandler(asynqClient, queueName, redisClient))
	mux.HandleFunc("create_firewall_zone", tasks.CreateFirewallZoneHandler(redisClient))
	mux.HandleFunc("create_subnet_on_switch", tasks.CreateSubnetOnSwitchHandler(asynqClient, queueName, redisClient))
	mux.HandleFunc("configure_subnet_routing", tasks.ConfigureSubnetRoutingHandler(redisClient))

	go func() {
		log.Printf("[AZ NSP %s] Worker启动, 并发数=%d, 队列=%s", az, workerCount, queueName)
		if err := asynqServer.Run(mux); err != nil {
			log.Fatalf("[AZ NSP %s] Worker启动失败: %v", az, err)
		}
	}()

	go func() {
		addr := ":" + port
		log.Printf("[AZ NSP %s] API服务启动在端口 %s", az, port)
		if err := server.Run(addr); err != nil {
			log.Fatalf("[AZ NSP %s] API服务启动失败: %v", az, err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Printf("[AZ NSP %s] 收到退出信号，正在关闭...", az)

	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	asynqServer.Shutdown()

	<-shutdownCtx.Done()
	log.Printf("[AZ NSP %s] 服务已关闭", az)
}