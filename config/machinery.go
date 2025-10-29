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
}

// DefaultConfig 默认配置
var DefaultConfig = &MachineryConfig{
	Broker:          "redis://localhost:6379",
	DefaultQueue:    "vpc_tasks",
	ResultBackend:   "redis://localhost:6379",
	ResultsExpireIn: 3600,
}

// NewMachineryServer 创建machinery服务器
func NewMachineryServer(cfg *MachineryConfig) (*machinery.Server, error) {
	cnf := &machineryConfig.Config{
		Broker:          cfg.Broker,
		DefaultQueue:    cfg.DefaultQueue,
		ResultBackend:   cfg.ResultBackend,
		ResultsExpireIn: cfg.ResultsExpireIn,
	}

	server, err := machinery.NewServer(cnf)
	if err != nil {
		return nil, err
	}

	return server, nil
}
