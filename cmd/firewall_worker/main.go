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
	log.Println("Firewall Worker (防火墙任务执行器)")
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

	// 3. 构造队列名
	queueName := fmt.Sprintf("vpc_tasks_%s_%s", cfg.Region, cfg.AZ)
	log.Printf("✓ 监听队列: %s", queueName)

	// 4. 创建Asynq Server
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

	// 5. 注册任务处理器（仅注册防火墙相关任务）
	mux := asynq.NewServeMux()
	mux.HandleFunc("create_firewall_zone", tasks.CreateFirewallZoneHandler(redisClient))
	log.Println("✓ 已注册任务: create_firewall_zone")

	// 6. 处理退出信号
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Printf("[防火墙Worker %s] 收到退出信号，正在关闭...", cfg.AZ)
		asynqServer.Shutdown()
	}()

	log.Printf("========================================")
	log.Printf("防火墙Worker已启动: Region=%s, AZ=%s", cfg.Region, cfg.AZ)
	log.Printf("处理队列: %s", queueName)
	log.Printf("处理任务: 创廽防火墙安全区域")
	log.Printf("========================================")

	// 7. 启动Worker
	if err := asynqServer.Run(mux); err != nil {
		log.Fatalf("Worker启动失败: %v", err)
	}
}
