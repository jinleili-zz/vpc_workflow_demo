package config

import (
	"context"

	"github.com/go-redis/redis/v8"
)

// NewRedisClient 创建Redis客户端（数据存储）
func NewRedisClient(cfg *NSPConfig) *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:         cfg.Redis.Addr,
		Password:     cfg.Redis.Password,
		DB:           cfg.Redis.DataDB,
		MaxRetries:   3,
		PoolSize:     cfg.Redis.MaxActive,
		MinIdleConns: cfg.Redis.MaxIdle,
	})
}

// TestRedisConnection 测试Redis连接
func TestRedisConnection(client *redis.Client) error {
	ctx := context.Background()
	return client.Ping(ctx).Err()
}
