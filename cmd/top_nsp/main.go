package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"time"

	"workflow_qoder/internal/config"
	"workflow_qoder/internal/top/api"
	"workflow_qoder/internal/top/orchestrator"
	"workflow_qoder/internal/top/registry"

	_ "github.com/go-sql-driver/mysql"
)

func main() {
	log.Println("========================================")
	log.Println("Top NSP VPC 启动中...")
	log.Println("========================================")

	ctx := context.Background()

	cfg := config.LoadConfig()
	cfg.ServiceType = "top"

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	redisClient := config.NewRedisClient(cfg)
	if err := config.TestRedisConnection(redisClient); err != nil {
		log.Fatalf("Redis连接失败: %v", err)
	}

	mysqlHost := os.Getenv("MYSQL_HOST")
	if mysqlHost == "" {
		mysqlHost = "mysql"
	}
	mysqlPort := os.Getenv("MYSQL_PORT")
	if mysqlPort == "" {
		mysqlPort = "3306"
	}
	mysqlUser := os.Getenv("MYSQL_USER")
	if mysqlUser == "" {
		mysqlUser = "nsp_user"
	}
	mysqlPassword := os.Getenv("MYSQL_PASSWORD")
	if mysqlPassword == "" {
		mysqlPassword = "nsp_password"
	}

	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/top_nsp_vpc?charset=utf8mb4&parseTime=True&loc=Local",
		mysqlUser, mysqlPassword, mysqlHost, mysqlPort)

	var topDB *sql.DB
	var err error
	for i := 0; i < 30; i++ {
		topDB, err = sql.Open("mysql", dsn)
		if err == nil {
			if err = topDB.Ping(); err == nil {
				break
			}
		}
		log.Printf("[Top NSP VPC] 等待MySQL就绪... (%d/30)", i+1)
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		log.Printf("[Top NSP VPC] MySQL连接失败，将不同步拓扑: %v", err)
		topDB = nil
	} else {
		defer topDB.Close()
		log.Println("[Top NSP VPC] MySQL连接成功")
	}

	reg := registry.NewRegistry(redisClient)

	orch := orchestrator.NewOrchestrator(reg, topDB)

	server := api.NewServer(reg, orch)

	addr := ":" + port
	log.Printf("[Top NSP VPC] 启动服务在端口 %s", port)

	if err := server.Run(addr); err != nil {
		log.Fatalf("[Top NSP VPC] 服务启动失败: %v", err)
	}

	<-ctx.Done()
}
