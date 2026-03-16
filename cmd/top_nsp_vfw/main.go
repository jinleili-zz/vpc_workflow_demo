package main

import (
	"database/sql"
	"fmt"
	"os"
	"strconv"

	"workflow_qoder/internal/top/vfw/api"
	"workflow_qoder/internal/top/vfw/service"

	"github.com/paic/nsp-common/pkg/logger"

	_ "github.com/lib/pq"
)

func main() {
	// 初始化 logger
	logCfg := logger.DefaultConfig("top-nsp-vfw")
	if os.Getenv("DEVELOPMENT") == "true" {
		logCfg = logger.DevelopmentConfig("top-nsp-vfw")
	}
	if err := logger.Init(logCfg); err != nil {
		panic("初始化日志失败: " + err.Error())
	}
	defer logger.Sync()

	logger.Platform().Info("========================================")
	logger.Platform().Info("Top NSP VFW 启动中...")
	logger.Platform().Info("========================================")

	port := 8082
	if portStr := os.Getenv("PORT"); portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil {
			port = p
		}
	}

	// Build PostgreSQL DSN
	pgHost := getEnvOrDefault("POSTGRES_HOST", "postgres")
	pgPort := getEnvOrDefault("POSTGRES_PORT", "5432")
	pgUser := getEnvOrDefault("POSTGRES_USER", "nsp_user")
	pgPassword := getEnvOrDefault("POSTGRES_PASSWORD", "nsp_password")

	vpcDSN := buildPostgresDSN(pgHost, pgPort, pgUser, pgPassword, "top_nsp_vpc")
	vfwDSN := buildPostgresDSN(pgHost, pgPort, pgUser, pgPassword, "top_nsp_vfw")

	vpcDB, err := sql.Open("postgres", vpcDSN)
	if err != nil {
		logger.Platform().Error("连接VPC数据库失败", "error", err)
		os.Exit(1)
	}
	defer vpcDB.Close()

	if err := vpcDB.Ping(); err != nil {
		logger.Platform().Error("VPC数据库连接测试失败", "error", err)
		os.Exit(1)
	}
	logger.Platform().Info("[Top NSP VFW] VPC数据库连接成功")

	vfwDB, err := sql.Open("postgres", vfwDSN)
	if err != nil {
		logger.Platform().Error("连接VFW数据库失败", "error", err)
		os.Exit(1)
	}
	defer vfwDB.Close()

	if err := vfwDB.Ping(); err != nil {
		logger.Platform().Error("VFW数据库连接测试失败", "error", err)
		os.Exit(1)
	}
	logger.Platform().Info("[Top NSP VFW] VFW数据库连接成功")

	policyService := service.NewPolicyService(vpcDB, vfwDB)

	server := api.NewServer(policyService)

	addr := fmt.Sprintf(":%d", port)
	logger.Platform().Info("启动服务", "port", port)
	if err := server.Run(addr); err != nil {
		logger.Platform().Error("服务启动失败", "error", err)
		os.Exit(1)
	}
}

func getEnvOrDefault(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

func buildPostgresDSN(host, port, user, password, dbname string) string {
	return "postgres://" + user + ":" + password + "@" + host + ":" + port + "/" + dbname + "?sslmode=disable"
}
