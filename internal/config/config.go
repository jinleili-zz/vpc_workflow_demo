package config

import (
	"fmt"

	"github.com/paic/nsp-common/pkg/config"
)

// NSPConfig NSP服务配置
// 使用 mapstructure tag 配合 viper 的 Unmarshal 功能
type NSPConfig struct {
	// 服务基本信息
	ServiceType string `mapstructure:"-"` // 在代码中设置，不从配置文件读取
	Region      string `mapstructure:"region"`
	AZ          string `mapstructure:"az"`
	Port        int    `mapstructure:"port"`

	// Redis 配置
	Redis RedisConfig `mapstructure:"redis"`

	// PostgreSQL 配置
	PostgreSQL PostgreSQLConfig `mapstructure:"postgresql"`

	// Top NSP 特有配置
	TopNSP TopNSPConfig `mapstructure:"top_nsp"`

	// AZ NSP 特有配置
	AZNSP AZNSPConfig `mapstructure:"az_nsp"`
}

// RedisConfig Redis配置
type RedisConfig struct {
	Host        string `mapstructure:"host"`
	Port        int    `mapstructure:"port"`
	Password    string `mapstructure:"password"`
	DataDB      int    `mapstructure:"data_db"`
	BrokerDB    int    `mapstructure:"broker_db"`
	MaxIdle     int    `mapstructure:"max_idle"`
	MaxActive   int    `mapstructure:"max_active"`
	IdleTimeout int    `mapstructure:"idle_timeout"`
}

// PostgreSQLConfig PostgreSQL配置
type PostgreSQLConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	User     string `mapstructure:"user"`
	Password string `mapstructure:"password"`
}

// TopNSPConfig Top NSP配置
type TopNSPConfig struct {
	AZNSPPrefix string `mapstructure:"az_nsp_prefix"`
	AZNSPPort   int    `mapstructure:"az_nsp_port"`
}

// AZNSPConfig AZ NSP配置
type AZNSPConfig struct {
	TopNSPAddr  string `mapstructure:"top_nsp_addr"`
	WorkerCount int    `mapstructure:"worker_count"`
}

// ConfigLoader 配置加载器，支持热更新
type ConfigLoader struct {
	loader config.Loader
	cfg    *NSPConfig
}

// NewConfigLoader 创建配置加载器
// configFile: 配置文件路径，如 "./config/config.yaml"
// envPrefix: 环境变量前缀，如 "NSP"
// watch: 是否开启热更新
func NewConfigLoader(configFile, envPrefix string, watch bool) (*ConfigLoader, error) {
	loader, err := config.New(config.Option{
		ConfigFile: configFile,
		EnvPrefix:  envPrefix,
		Watch:      watch,
		Defaults: map[string]any{
			"port":                  8080,
			"redis.host":           "localhost",
			"redis.port":           6379,
			"redis.data_db":        0,
			"redis.broker_db":      1,
			"redis.max_idle":       3,
			"redis.max_active":     10,
			"redis.idle_timeout":   240,
			"postgresql.host":      "localhost",
			"postgresql.port":      5432,
			"postgresql.user":      "nsp_user",
			"postgresql.password":  "nsp_password",
			"top_nsp.az_nsp_prefix": "az-nsp",
			"top_nsp.az_nsp_port":   8080,
			"az_nsp.top_nsp_addr":   "http://top-nsp:8080",
			"az_nsp.worker_count":   2,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create config loader: %w", err)
	}

	var cfg NSPConfig
	if err := loader.Load(&cfg); err != nil {
		loader.Close()
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	cl := &ConfigLoader{
		loader: loader,
		cfg:    &cfg,
	}

	// 如果开启热更新，注册回调函数
	if watch {
		loader.OnChange(func(apply func(target any) error) {
			var newCfg NSPConfig
			if err := apply(&newCfg); err != nil {
				// 新配置解析失败，记录日志，继续使用旧配置
				fmt.Printf("config reload failed: %v\n", err)
				return
			}
			// 保留 ServiceType 和 AZ（这些通常在代码中设置）
			newCfg.ServiceType = cl.cfg.ServiceType
			newCfg.AZ = cl.cfg.AZ
			cl.cfg = &newCfg
			fmt.Printf("config reloaded successfully\n")
		})
	}

	return cl, nil
}

// GetConfig 获取当前配置
func (cl *ConfigLoader) GetConfig() *NSPConfig {
	return cl.cfg
}

// Close 关闭配置加载器，释放资源
func (cl *ConfigLoader) Close() {
	if cl.loader != nil {
		cl.loader.Close()
	}
}

// GetRedisDataAddr 获取数据存储Redis地址
func (c *NSPConfig) GetRedisDataAddr() string {
	addr := fmt.Sprintf("%s:%d", c.Redis.Host, c.Redis.Port)
	if c.Redis.Password != "" {
		return fmt.Sprintf("redis://:%s@%s/%d", c.Redis.Password, addr, c.Redis.DataDB)
	}
	return fmt.Sprintf("redis://%s/%d", addr, c.Redis.DataDB)
}

// GetRedisBrokerAddr 获取消息队列Redis地址（Machinery 格式）
func (c *NSPConfig) GetRedisBrokerAddr() string {
	addr := fmt.Sprintf("%s:%d", c.Redis.Host, c.Redis.Port)
	if c.Redis.Password != "" {
		return fmt.Sprintf("redis://:%s@%s/%d", c.Redis.Password, addr, c.Redis.BrokerDB)
	}
	return fmt.Sprintf("redis://%s/%d", addr, c.Redis.BrokerDB)
}

// GetRedisAddr 获取简单的Redis地址（用于Asynq）
func (c *NSPConfig) GetRedisAddr() string {
	return fmt.Sprintf("%s:%d", c.Redis.Host, c.Redis.Port)
}

// GetRedisBrokerDB 获取Broker DB编号
func (c *NSPConfig) GetRedisBrokerDB() int {
	return c.Redis.BrokerDB
}

// GetPostgresDSN 获取 PostgreSQL DSN
// dbName: 数据库名称，top-nsp 使用 "top_nsp_vpc"，az-nsp 使用 "nsp_{az}_vpc"
func (c *NSPConfig) GetPostgresDSN(dbName string) string {
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable",
		c.PostgreSQL.User,
		c.PostgreSQL.Password,
		c.PostgreSQL.Host,
		c.PostgreSQL.Port,
		dbName,
	)
}

// LoadConfig 兼容旧版的配置加载函数
// 内部使用 NewConfigLoader 创建加载器，但不支持热更新
// 用于快速迁移现有代码
func LoadConfig() *NSPConfig {
	cl, err := NewConfigLoader("./config/config.yaml", "NSP", false)
	if err != nil {
		// 如果配置文件加载失败，使用默认配置
		fmt.Printf("Warning: failed to load config file, using defaults: %v\n", err)
		return &NSPConfig{
			ServiceType: "az",
			Region:      "cn-north-1",
			Port:        8080,
			Redis: RedisConfig{
				Host:        "localhost",
				Port:        6379,
				Password:    "",
				DataDB:      0,
				BrokerDB:    1,
				MaxIdle:     3,
				MaxActive:   10,
				IdleTimeout: 240,
			},
			PostgreSQL: PostgreSQLConfig{
				Host:     "localhost",
				Port:     5432,
				User:     "nsp_user",
				Password: "nsp_password",
			},
			TopNSP: TopNSPConfig{
				AZNSPPrefix: "az-nsp",
				AZNSPPort:   8080,
			},
			AZNSP: AZNSPConfig{
				TopNSPAddr:  "http://top-nsp:8080",
				WorkerCount: 2,
			},
		}
	}
	// 注意：使用 LoadConfig 时，调用方需要自行管理 ConfigLoader 的生命周期
	// 这里为了兼容旧接口，不返回 loader，调用方无法调用 Close()
	// 建议新代码直接使用 NewConfigLoader
	return cl.GetConfig()
}
