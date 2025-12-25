package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"workflow_qoder/internal/az/vfw/api"
	"workflow_qoder/internal/config"
	"workflow_qoder/internal/queue"

	_ "github.com/go-sql-driver/mysql"
	"github.com/hibiken/asynq"
)

func main() {
	log.Println("========================================")
	log.Println("AZ NSP VFW 启动中...")
	log.Println("========================================")

	cfg := config.LoadConfig()

	region := os.Getenv("REGION")
	az := os.Getenv("AZ")
	if region == "" || az == "" {
		log.Fatal("必须设置环境变量 REGION 和 AZ")
	}
	cfg.Region = region
	cfg.AZ = az
	cfg.ServiceType = "az"

	port := 8080
	if portStr := os.Getenv("PORT"); portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil {
			port = p
		}
	}
	cfg.Port = port

	log.Printf("[AZ NSP VFW] Region=%s, AZ=%s, Port=%d", region, az, port)

	mysqlHost := os.Getenv("MYSQL_HOST")
	if mysqlHost == "" {
		mysqlHost = "mysql"
	}
	mysqlPort := os.Getenv("MYSQL_PORT")
	if mysqlPort == "" {
		mysqlPort = "3306"
	}
	mysqlDatabase := os.Getenv("MYSQL_DATABASE")
	if mysqlDatabase == "" {
		mysqlDatabase = fmt.Sprintf("nsp_%s_vfw", strings.ReplaceAll(az, "-", "_"))
	}
	mysqlUser := os.Getenv("MYSQL_USER")
	if mysqlUser == "" {
		mysqlUser = "nsp_user"
	}
	mysqlPassword := os.Getenv("MYSQL_PASSWORD")
	if mysqlPassword == "" {
		mysqlPassword = "nsp_password"
	}

	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=True&loc=Local",
		mysqlUser, mysqlPassword, mysqlHost, mysqlPort, mysqlDatabase)

	var mysqlDB *sql.DB
	var err error
	for i := 0; i < 30; i++ {
		mysqlDB, err = sql.Open("mysql", dsn)
		if err == nil {
			if err = mysqlDB.Ping(); err == nil {
				break
			}
		}
		log.Printf("[AZ NSP VFW] 等待MySQL就绪... (%d/30)", i+1)
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		log.Fatalf("连接MySQL失败: %v", err)
	}
	defer mysqlDB.Close()
	log.Printf("[AZ NSP VFW] MySQL连接成功: %s", mysqlDatabase)

	redisAddr := cfg.GetRedisAddr()
	addrs := strings.Split(redisAddr, ",")

	var asynqClientOpt asynq.RedisConnOpt
	if len(addrs) > 1 {
		asynqClientOpt = asynq.RedisClusterClientOpt{
			Addrs: addrs,
		}
	} else {
		asynqClientOpt = asynq.RedisClientOpt{
			Addr: redisAddr,
			DB:   cfg.GetRedisBrokerDB(),
		}
	}

	asynqClient := asynq.NewClient(asynqClientOpt)
	defer asynqClient.Close()

	server := api.NewServer(cfg, asynqClient, mysqlDB)

	callbackQueueName := server.GetCallbackQueueName()

	var asynqServerOpt asynq.RedisConnOpt
	if len(addrs) > 1 {
		asynqServerOpt = asynq.RedisClusterClientOpt{
			Addrs: addrs,
		}
	} else {
		asynqServerOpt = asynq.RedisClientOpt{
			Addr: redisAddr,
			DB:   cfg.GetRedisBrokerDB(),
		}
	}

	queuesConfig := queue.GetAllQueuesConfig(region, az)
	queuesConfig[callbackQueueName] = 10

	asynqServer := asynq.NewServer(
		asynqServerOpt,
		asynq.Config{
			Concurrency: 10,
			Queues:      queuesConfig,
		},
	)

	mux := asynq.NewServeMux()
	mux.HandleFunc("task_callback", server.HandleTaskCallback())

	go func() {
		if err := asynqServer.Run(mux); err != nil {
			log.Printf("[AZ NSP VFW] Asynq服务器启动失败: %v", err)
		}
	}()

	time.Sleep(2 * time.Second)

	for i := 0; i < 10; i++ {
		if err := server.RegisterToTopNSP(); err != nil {
			log.Printf("[AZ NSP VFW] 注册失败，重试中... (%d/10): %v", i+1, err)
			time.Sleep(3 * time.Second)
			continue
		}
		break
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go server.StartHeartbeat(ctx)

	go func() {
		addr := fmt.Sprintf(":%d", port)
		if err := server.Run(addr); err != nil {
			log.Fatalf("[AZ NSP VFW] 服务启动失败: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("[AZ NSP VFW] 正在关闭...")
	cancel()
	asynqServer.Shutdown()
	log.Println("[AZ NSP VFW] 已关闭")
}
