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
	"workflow_qoder/internal/db"
	"workflow_qoder/internal/mq"
	"workflow_qoder/internal/queue"
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

	namesrvAddr := os.Getenv("ROCKETMQ_NAMESRV")
	if namesrvAddr == "" {
		namesrvAddr = "namesrv:9876"
	}

	producer, err := mq.NewProducer(namesrvAddr)
	if err != nil {
		log.Fatalf("[AZ NSP] 创建RocketMQ Producer失败: %v", err)
	}
	defer producer.Close()

	callbackTopic := queue.GetCallbackTopicName(region, az)
	callbackGroup := queue.GetCallbackConsumerGroup(region, az)

	if err := producer.EnsureTopicExists(callbackTopic); err != nil {
		log.Printf("[AZ NSP] 创建回调topic失败(可能已存在): %v", err)
	}

	server := api.NewServer(cfg, producer, mysqlDB)

	if err := server.RegisterToTopNSP(); err != nil {
		log.Printf("[AZ NSP %s] 注册到Top NSP失败: %v (将在后续心跳中重试)", az, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go server.StartHeartbeat(ctx)

	callbackConsumer, err := mq.NewConsumer(namesrvAddr, callbackGroup)
	if err != nil {
		log.Fatalf("[AZ NSP] 创建回调Consumer失败: %v", err)
	}
	defer callbackConsumer.Close()

	callbackConsumer.RegisterHandler("task_callback", server.HandleTaskCallback())

	if err := callbackConsumer.Subscribe(callbackTopic); err != nil {
		log.Fatalf("[AZ NSP] 订阅回调topic失败: %v", err)
	}

	if err := callbackConsumer.Start(); err != nil {
		log.Fatalf("[AZ NSP] 启动回调Consumer失败: %v", err)
	}

	log.Printf("[AZ NSP %s] 回调处理器启动, topic=%s", az, callbackTopic)

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

	<-shutdownCtx.Done()
	log.Printf("[AZ NSP %s] 服务已关闭", az)
}