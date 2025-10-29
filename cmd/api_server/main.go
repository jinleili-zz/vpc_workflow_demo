package main

import (
	"log"

	"workflow_qoder/api"
	"workflow_qoder/config"
)

func main() {
	// 创建machinery服务器
	machineryServer, err := config.NewMachineryServer(config.DefaultConfig)
	if err != nil {
		log.Fatalf("创建machinery服务器失败: %v", err)
	}

	// 创建API服务器
	apiServer := api.NewServer(machineryServer)

	// 启动API服务器
	if err := apiServer.Run(":8080"); err != nil {
		log.Fatalf("API服务器启动失败: %v", err)
	}
}
