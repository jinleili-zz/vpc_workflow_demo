package config

import (
	"github.com/RichardKnop/machinery/v1"
	machineryConfig "github.com/RichardKnop/machinery/v1/config"
)

// MachineryConfig machinery配置
type MachineryConfig struct {
	Broker          string
	DefaultQueue    string
	ResultBackend   string
	ResultsExpireIn int
	// Workflow相关配置
	MaxWorkers      int    // Worker最大并发数
	RetryCount      int    // 默认重试次数
	RetryTimeout    int    // 重试超时时间(秒)
	DelayedTasksKey string // 延迟任务键名
}

// DefaultConfig 默认配置
var DefaultConfig = &MachineryConfig{
	Broker:          "redis://localhost:6379",
	DefaultQueue:    "vpc_tasks",
	ResultBackend:   "redis://localhost:6379",
	ResultsExpireIn: 3600,
	// Workflow配置
	MaxWorkers:      10,
	RetryCount:      3,
	RetryTimeout:    60,
	DelayedTasksKey: "delayed_tasks",
}

// NewMachineryServer 创建machinery服务器
func NewMachineryServer(cfg *MachineryConfig) (*machinery.Server, error) {
	cnf := &machineryConfig.Config{
		Broker:          cfg.Broker,
		DefaultQueue:    cfg.DefaultQueue,
		ResultBackend:   cfg.ResultBackend,
		ResultsExpireIn: cfg.ResultsExpireIn,
		// 消息队列配置
		Redis: &machineryConfig.RedisConfig{
			MaxIdle:                3,
			IdleTimeout:            240,
			ReadTimeout:            15,
			WriteTimeout:           15,
			ConnectTimeout:         15,
			NormalTasksPollPeriod:  1000, // 轮询间隔(毫秒)
			DelayedTasksPollPeriod: 500,  // 延迟任务轮询间隔
		},
	}

	server, err := machinery.NewServer(cnf)
	if err != nil {
		return nil, err
	}

	return server, nil
}

// GetWorkflowConfig 获取workflow配置
func GetWorkflowConfig() map[string]interface{} {
	return map[string]interface{}{
		"max_workers":       DefaultConfig.MaxWorkers,
		"retry_count":       DefaultConfig.RetryCount,
		"retry_timeout":     DefaultConfig.RetryTimeout,
		"delayed_tasks_key": DefaultConfig.DelayedTasksKey,
	}
}
