package main

import (
	"context"
	"log"
	"os"

	"workflow_qoder/internal/config"
	"workflow_qoder/internal/top/api"
	"workflow_qoder/internal/top/orchestrator"
	"workflow_qoder/internal/top/registry"
)

func main() {
	log.Println("========================================")
	log.Println("Top NSP 启动中...")
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

	reg := registry.NewRegistry(redisClient)

	orch := orchestrator.NewOrchestrator(reg)

	server := api.NewServer(reg, orch)

	addr := ":" + port
	log.Printf("[Top NSP] 启动服务在端口 %s", port)

	if err := server.Run(addr); err != nil {
		log.Fatalf("[Top NSP] 服务启动失败: %v", err)
	}

	<-ctx.Done()
}