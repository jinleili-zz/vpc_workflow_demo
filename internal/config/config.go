package config

import (
	"fmt"
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

	// Redis 配置
	Redis RedisConfig

	// Top NSP 特有配置
	TopNSP TopNSPConfig

	// AZ NSP 特有配置
	AZNSP AZNSPConfig
}

// RedisConfig Redis配置
type RedisConfig struct {
	// Redis 地址
	Addr     string
	Password string

	// 数据存储 DB（任务状态、VPC映射、Region/AZ信息等）
	DataDB int

	// 消息队列 DB（go-machinery broker）
	BrokerDB int

	// 连接池配置
	MaxIdle     int
	MaxActive   int
	IdleTimeout int
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

	// Worker 配置
	WorkerCount int
}

// LoadConfig 从环境变量加载配置
func LoadConfig() *NSPConfig {
	return &NSPConfig{
		ServiceType: getEnv("SERVICE_TYPE", "az"),
		Region:      getEnv("REGION", ""),
		AZ:          getEnv("AZ", ""),
		Port:        getEnvInt("PORT", 8080),

		Redis: RedisConfig{
			Addr:        getEnv("REDIS_ADDR", "localhost:6379"),
			Password:    getEnv("REDIS_PASSWORD", ""),
			DataDB:      getEnvInt("REDIS_DATA_DB", 0),
			BrokerDB:    getEnvInt("REDIS_BROKER_DB", 1),
			MaxIdle:     getEnvInt("REDIS_MAX_IDLE", 3),
			MaxActive:   getEnvInt("REDIS_MAX_ACTIVE", 10),
			IdleTimeout: getEnvInt("REDIS_IDLE_TIMEOUT", 240),
		},

		TopNSP: TopNSPConfig{
			AZNSPPrefix: getEnv("AZ_NSP_PREFIX", "az-nsp"),
			AZNSPPort:   getEnvInt("AZ_NSP_PORT", 8080),
		},

		AZNSP: AZNSPConfig{
			TopNSPAddr:  getEnv("TOP_NSP_ADDR", "http://top-nsp:8080"),
			WorkerCount: getEnvInt("WORKER_COUNT", 2),
		},
	}
}

// GetRedisDataAddr 获取数据存储Redis地址
func (c *NSPConfig) GetRedisDataAddr() string {
	if c.Redis.Password != "" {
		return fmt.Sprintf("redis://:%s@%s/%d", c.Redis.Password, c.Redis.Addr, c.Redis.DataDB)
	}
	return fmt.Sprintf("redis://%s/%d", c.Redis.Addr, c.Redis.DataDB)
}

// GetRedisBrokerAddr 获取消息队列Redis地址
func (c *NSPConfig) GetRedisBrokerAddr() string {
	if c.Redis.Password != "" {
		return fmt.Sprintf("redis://:%s@%s/%d", c.Redis.Password, c.Redis.Addr, c.Redis.BrokerDB)
	}
	return fmt.Sprintf("redis://%s/%d", c.Redis.Addr, c.Redis.BrokerDB)
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
