package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"strconv"

	"workflow_qoder/internal/top/vfw/api"
	"workflow_qoder/internal/top/vfw/service"

	_ "github.com/go-sql-driver/mysql"
)

func main() {
	log.Println("========================================")
	log.Println("Top NSP VFW 启动中...")
	log.Println("========================================")

	port := 8082
	if portStr := os.Getenv("PORT"); portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil {
			port = p
		}
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

	vpcDSN := fmt.Sprintf("%s:%s@tcp(%s:%s)/top_nsp_vpc?charset=utf8mb4&parseTime=True&loc=Local",
		mysqlUser, mysqlPassword, mysqlHost, mysqlPort)
	vfwDSN := fmt.Sprintf("%s:%s@tcp(%s:%s)/top_nsp_vfw?charset=utf8mb4&parseTime=True&loc=Local",
		mysqlUser, mysqlPassword, mysqlHost, mysqlPort)

	vpcDB, err := sql.Open("mysql", vpcDSN)
	if err != nil {
		log.Fatalf("连接VPC数据库失败: %v", err)
	}
	defer vpcDB.Close()

	if err := vpcDB.Ping(); err != nil {
		log.Fatalf("VPC数据库连接测试失败: %v", err)
	}
	log.Println("[Top NSP VFW] VPC数据库连接成功")

	vfwDB, err := sql.Open("mysql", vfwDSN)
	if err != nil {
		log.Fatalf("连接VFW数据库失败: %v", err)
	}
	defer vfwDB.Close()

	if err := vfwDB.Ping(); err != nil {
		log.Fatalf("VFW数据库连接测试失败: %v", err)
	}
	log.Println("[Top NSP VFW] VFW数据库连接成功")

	policyService := service.NewPolicyService(vpcDB, vfwDB)

	server := api.NewServer(policyService)

	addr := fmt.Sprintf(":%d", port)
	if err := server.Run(addr); err != nil {
		log.Fatalf("服务启动失败: %v", err)
	}
}
