package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"workflow_qoder/internal/config"
	"workflow_qoder/tasks"

	"github.com/go-redis/redis/v8"
	"github.com/hibiken/asynq"
)

func main() {
	log.Println("========================================")
	log.Println("Switch Worker (交换机任务执行器)")
	log.Println("========================================")

	// 1. 加载配置
	cfg := config.LoadConfig()
	log.Printf("配置加载完成: Region=%s, AZ=%s", cfg.Region, cfg.AZ)

	// 2. 创建Redis客户端（用于数据存储）
	redisClient := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DataDB,
	})
	defer redisClient.Close()

	ctx := context.Background()
	if err := redisClient.Ping(ctx).Err(); err != nil {
		log.Fatalf("Redis连接失败: %v", err)
	}
	log.Println("✓ Redis连接成功")

	// 3. 创建Asynq客户端（用于发送后续任务）
	asynqClient := asynq.NewClient(asynq.RedisClientOpt{
		Addr: cfg.GetRedisAddr(),
		DB:   cfg.GetRedisBrokerDB(),
	})
	defer asynqClient.Close()
	log.Println("✓ Asynq客户端已创建")

	// 4. 构造队列名
	queueName := fmt.Sprintf("vpc_tasks_%s_%s", cfg.Region, cfg.AZ)
	log.Printf("✓ 监听队列: %s", queueName)

	// 5. 创建Asynq Server
	queues := map[string]int{
		queueName: 10, // 队列优先级
	}
	asynqServer := asynq.NewServer(
		asynq.RedisClientOpt{
			Addr: cfg.GetRedisAddr(),
			DB:   cfg.GetRedisBrokerDB(),
		},
		asynq.Config{
			Concurrency: 3, // 3个并发协程
			Queues:      queues,
		},
	)

	// 6. 注册任务处理器（仅注册交换机相关任务）
	mux := asynq.NewServeMux()
	mux.HandleFunc("create_vrf_on_switch", tasks.CreateVRFOnSwitchHandler(asynqClient, queueName, redisClient))
	mux.HandleFunc("create_vlan_subinterface", tasks.CreateVLANSubInterfaceHandler(asynqClient, queueName, redisClient))
	log.Println("✓ 已注册任务: create_vrf_on_switch, create_vlan_subinterface")

	// 7. 处理退出信号
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Printf("[交换机Worker %s] 收到退出信号，正在关闭...", cfg.AZ)
		asynqServer.Shutdown()
	}()

	log.Printf("========================================")
	log.Printf("交换机Worker已启动: Region=%s, AZ=%s", cfg.Region, cfg.AZ)
	log.Printf("处理队列: %s", queueName)
	log.Printf("处理任务: 创廽VRF, 创廽VLAN子接口")
	log.Printf("========================================")

	// 8. 启动Worker
	if err := asynqServer.Run(mux); err != nil {
		log.Fatalf("Worker启动失败: %v", err)
	}
}
