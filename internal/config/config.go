package config

import (
	"os"
	"strconv"
)

// NSPConfig NSP服务配置
type NSPConfig struct {
	// 服务基本信息
	ServiceType string // "top" 或 "az"
	Region      string
	AZ          string
	Port        int

	// MySQL 配置
	MySQL MySQLConfig

	// Top NSP 特有配置
	TopNSP TopNSPConfig

	// AZ NSP 特有配置
	AZNSP AZNSPConfig
}

// MySQLConfig MySQL配置
type MySQLConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	Database string
}

// TopNSPConfig Top NSP配置
type TopNSPConfig struct {
	// AZ NSP 服务发现
	AZNSPPrefix string // 容器名前缀，如 "az-nsp"
	AZNSPPort   int    // AZ NSP 统一端口
}

// AZNSPConfig AZ NSP配置
type AZNSPConfig struct {
	// Top NSP 地址（用于注册和心跳）
	TopNSPAddr string
}

// LoadConfig 从环境变量加载配置
func LoadConfig() *NSPConfig {
	return &NSPConfig{
		ServiceType: getEnv("SERVICE_TYPE", "az"),
		Region:      getEnv("REGION", ""),
		AZ:          getEnv("AZ", ""),
		Port:        getEnvInt("PORT", 8080),

		MySQL: MySQLConfig{
			Host:     getEnv("MYSQL_HOST", "localhost"),
			Port:     getEnvInt("MYSQL_PORT", 3306),
			User:     getEnv("MYSQL_USER", "nsp_user"),
			Password: getEnv("MYSQL_PASSWORD", "nsp_pass_2024"),
			Database: getEnv("MYSQL_DATABASE", "nsp_workflow"),
		},

		TopNSP: TopNSPConfig{
			AZNSPPrefix: getEnv("AZ_NSP_PREFIX", "az-nsp"),
			AZNSPPort:   getEnvInt("AZ_NSP_PORT", 8080),
		},

		AZNSP: AZNSPConfig{
			TopNSPAddr: getEnv("TOP_NSP_ADDR", "http://top-nsp:8080"),
		},
	}
}

// getEnv 获取环境变量，如果不存在则返回默认值
func getEnv(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}

// getEnvInt 获取整型环境变量
func getEnvInt(key string, defaultValue int) int {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	intValue, err := strconv.Atoi(value)
	if err != nil {
		return defaultValue
	}
	return intValue
}
