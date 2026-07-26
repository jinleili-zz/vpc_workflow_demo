package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"time"

	"workflow_qoder/internal/bootstrap"
	"workflow_qoder/internal/config"
	"workflow_qoder/internal/top/vfw/api"
	"workflow_qoder/internal/top/vfw/service"

	"github.com/jinleili-zz/nsp-platform/logger"
	_ "github.com/lib/pq"
)

func main() {
	ctx := context.Background()

	configLoader, err := config.NewConfigLoader("./config/config.yaml", "NSP", true)
	if err != nil {
		fmt.Printf("加载配置失败: %v\n", err)
		os.Exit(1)
	}
	defer configLoader.Close()

	cfg := configLoader.GetConfig()
	cfg.ServiceType = "top"

	authCreds, err := cfg.AuthCredentials()
	if err != nil {
		fmt.Printf("解析认证配置失败: %v\n", err)
		os.Exit(1)
	}
	signerCred, err := cfg.ResolveSignerCredential("top-nsp")
	if err != nil {
		fmt.Printf("解析签名凭证失败: %v\n", err)
		os.Exit(1)
	}

	bootstrapCfg := bootstrap.DefaultConfig("top-nsp-vfw")
	bootstrapCfg.EnableAuth = false
	bootstrapCfg.EnableSaga = false
	bootstrapCfg.Credentials = authCreds
	bootstrapCfg.ServiceAccessKey = signerCred.AccessKey

	components, err := bootstrap.Initialize(ctx, bootstrapCfg)
	if err != nil {
		fmt.Printf("初始化基础组件失败: %v\n", err)
		os.Exit(1)
	}
	defer components.Shutdown()

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
	pgHost := getEnvOrDefault("POSTGRES_HOST", cfg.PostgreSQL.Host)
	pgPort := getEnvOrDefault("POSTGRES_PORT", fmt.Sprintf("%d", cfg.PostgreSQL.Port))
	pgUser := getEnvOrDefault("POSTGRES_USER", cfg.PostgreSQL.User)
	pgPassword := getEnvOrDefault("POSTGRES_PASSWORD", cfg.PostgreSQL.Password)

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

	policyService := service.NewPolicyService(vpcDB, vfwDB, components.Signer)
	policyService.StartReconciler(ctx, 5*time.Second)

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
