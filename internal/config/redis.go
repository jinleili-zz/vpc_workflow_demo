package config

import (
	"context"
	"strings"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

// NewRedisClient 创建Redis客户端（数据存储）
func NewRedisClient(cfg *NSPConfig) redis.UniversalClient {
	addr := cfg.GetRedisAddr()
	addrs := strings.Split(addr, ",")

	if len(addrs) > 1 {
		return redis.NewClusterClient(&redis.ClusterOptions{
			Addrs:        addrs,
			Password:     cfg.Redis.Password,
			MaxRetries:   3,
			PoolSize:     cfg.Redis.MaxActive,
			MinIdleConns: cfg.Redis.MaxIdle,
		})
	}

	return redis.NewClient(&redis.Options{
		Addr:         addr,
		Password:     cfg.Redis.Password,
		DB:           cfg.Redis.DataDB,
		MaxRetries:   3,
		PoolSize:     cfg.Redis.MaxActive,
		MinIdleConns: cfg.Redis.MaxIdle,
	})
}

// TestRedisConnection 测试Redis连接
func TestRedisConnection(client redis.UniversalClient) error {
	ctx := context.Background()
	return client.Ping(ctx).Err()
}

// MakeAsynqRedisOpt 创建 asynq Redis 连接选项，支持单节点和集群模式
func MakeAsynqRedisOpt(addr string, db int) asynq.RedisConnOpt {
	addrs := strings.Split(addr, ",")
	if len(addrs) > 1 {
		return asynq.RedisClusterClientOpt{
			Addrs: addrs,
		}
	}
	return asynq.RedisClientOpt{
		Addr: addr,
		DB:   db,
	}
}