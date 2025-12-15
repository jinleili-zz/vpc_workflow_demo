package config

import (
	"context"
	"strings"

	"github.com/redis/go-redis/v9"
)

// NewRedisClient 创建Redis客户端（数据存储）
func NewRedisClient(cfg *NSPConfig) redis.UniversalClient {
	addrs := strings.Split(cfg.Redis.Addr, ",")
	
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
		Addr:         cfg.Redis.Addr,
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