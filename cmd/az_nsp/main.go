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

	"workflow_qoder/internal/az/api"
	"workflow_qoder/internal/config"
	"workflow_qoder/internal/db"
	"workflow_qoder/internal/queue"

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

	mysqlCfg := db.LoadMySQLConfig()
	if err := db.InitMySQL(mysqlCfg); err != nil {
		log.Fatalf("[AZ NSP] MySQL初始化失败: %v", err)
	}
	defer db.Close()

	mysqlDB := db.GetDB()

	migrationDir := "./internal/db/migrations"
	if err := db.RunMigrations(mysqlDB, migrationDir); err != nil {
		log.Printf("[AZ NSP] 数据库迁移失败: %v (可能已执行过)", err)
	}

	redisAddr := cfg.GetRedisAddr()
	redisBrokerDB := cfg.GetRedisBrokerDB()

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

	callbackQueueName := queue.GetCallbackQueueName(region, az)

	server := api.NewServer(cfg, asynqClient, mysqlDB)

	if err := server.RegisterToTopNSP(); err != nil {
		log.Printf("[AZ NSP %s] 注册到Top NSP失败: %v (将在后续心跳中重试)", az, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go server.StartHeartbeat(ctx)

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
			Concurrency: 2,
			Queues: map[string]int{
				callbackQueueName: 10,
			},
		},
	)

	mux := asynq.NewServeMux()
	mux.HandleFunc("task_callback", server.HandleTaskCallback())

	go func() {
		log.Printf("[AZ NSP %s] 回调处理器启动, 队列=%s", az, callbackQueueName)
		if err := asynqServer.Run(mux); err != nil {
			log.Fatalf("[AZ NSP %s] 回调处理器启动失败: %v", az, err)
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