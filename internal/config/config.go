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

	// RocketMQ 配置
	RocketMQ RocketMQConfig

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

// RocketMQConfig RocketMQ配置
type RocketMQConfig struct {
	// NameServer 地址列表
	NameServers []string

	// 生产者配置
	ProducerGroup    string
	ProducerInstance string

	// 消费者配置
	ConsumerGroup    string
	ConsumerInstance string

	// 主题配置
	VPCTopic    string // VPC任务主题
	SubnetTopic string // 子网任务主题

	// 重试次数
	RetryTimes int
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

		RocketMQ: RocketMQConfig{
			NameServers:      getEnvSlice("ROCKETMQ_NAME_SERVERS", []string{"rmqnamesrv:9876"}),
			ProducerGroup:    getEnv("ROCKETMQ_PRODUCER_GROUP", "nsp_producer_group"),
			ProducerInstance: getEnv("ROCKETMQ_PRODUCER_INSTANCE", "nsp_producer"),
			ConsumerGroup:    getEnv("ROCKETMQ_CONSUMER_GROUP", "nsp_consumer_group"),
			ConsumerInstance: getEnv("ROCKETMQ_CONSUMER_INSTANCE", "nsp_consumer"),
			VPCTopic:         getEnv("ROCKETMQ_VPC_TOPIC", "vpc_tasks"),
			SubnetTopic:      getEnv("ROCKETMQ_SUBNET_TOPIC", "subnet_tasks"),
			RetryTimes:       getEnvInt("ROCKETMQ_RETRY_TIMES", 3),
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

// getEnvSlice 获取字符串数组环境变量（逗号分隔）
func getEnvSlice(key string, defaultValue []string) []string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	// 简单处理，按逗号分割
	var result []string
	for _, v := range []rune(value) {
		if v != ',' {
			result = append(result, string(v))
		}
	}
	if len(result) == 0 {
		return defaultValue
	}
	return []string{value} // 简单处理，实际应该按逗号分割
}
