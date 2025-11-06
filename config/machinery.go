package config

import (
	"github.com/hibiken/asynq"
)

// AsynqConfig asynq配置
type AsynqConfig struct {
	RedisAddr   string
	RedisDB     int
	Concurrency int
	RetryCount  int
	Queues      map[string]int // 队列名称到优先级的映射
}

// NewAsynqClient 创建asynq客户端
func NewAsynqClient(redisAddr string, redisDB int) *asynq.Client {
	return asynq.NewClient(asynq.RedisClientOpt{
		Addr: redisAddr,
		DB:   redisDB,
	})
}

// NewAsynqServer 创建asynq服务器
func NewAsynqServer(redisAddr string, redisDB int, queues map[string]int, concurrency int) *asynq.Server {
	return asynq.NewServer(
		asynq.RedisClientOpt{
			Addr: redisAddr,
			DB:   redisDB,
		},
		asynq.Config{
			Concurrency: concurrency,
			Queues:      queues,
		},
	)
}
