package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"workflow_qoder/internal/config"
	"workflow_qoder/tasks"

	"github.com/RichardKnop/machinery/v1"
	machineryConfig "github.com/RichardKnop/machinery/v1/config"
)

func main() {
	// 加载配置
	cfg := config.LoadConfig()

	if cfg.Region == "" || cfg.AZ == "" {
		log.Fatalf("[Firewall Worker] 必须设置 REGION 和 AZ 环境变量")
	}

	log.Printf("[Firewall Worker] 正在启动 Region=%s, AZ=%s", cfg.Region, cfg.AZ)
	log.Printf("[Firewall Worker] Redis地址: %s", cfg.Redis.Addr)

	// 创建 Machinery 服务器
	machineryServer, err := createMachineryServer(cfg)
	if err != nil {
		log.Fatalf("[Firewall Worker] 创建Machinery服务器失败: %v", err)
	}

	// 注册任务（仅注册防火墙相关任务）
	tasksMap := map[string]interface{}{
		"create_firewall_zone": tasks.CreateFirewallZone,
	}

	err = machineryServer.RegisterTasks(tasksMap)
	if err != nil {
		log.Fatalf("[Firewall Worker] 注册任务失败: %v", err)
	}

	// 创建 Worker，指定 Worker 名称
	workerName := fmt.Sprintf("firewall_worker_%s_%s", cfg.Region, cfg.AZ)
	workerCount := cfg.AZNSP.WorkerCount
	if workerCount <= 0 {
		workerCount = 2
	}
	worker := machineryServer.NewWorker(workerName, workerCount)

	// 处理退出信号
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Printf("[Firewall Worker %s] 收到退出信号，正在关闭...", cfg.AZ)
		worker.Quit()
	}()

	queueName := fmt.Sprintf("vpc_tasks_%s_%s", cfg.Region, cfg.AZ)
	log.Printf("[Firewall Worker %s] 启动中... 处理队列: %s", cfg.AZ, queueName)
	log.Printf("[Firewall Worker %s] 处理任务类型: 创建防火墙安全区域", cfg.AZ)
	log.Printf("[Firewall Worker %s] Worker数量: %d", cfg.AZ, workerCount)

	// 启动Worker
	err = worker.Launch()
	if err != nil {
		log.Fatalf("[Firewall Worker %s] Worker启动失败: %v", cfg.AZ, err)
	}
}

// createMachineryServer 创建Machinery服务器
func createMachineryServer(cfg *config.NSPConfig) (*machinery.Server, error) {
	// Worker 从特定 AZ 的队列消费任务
	queueName := fmt.Sprintf("vpc_tasks_%s_%s", cfg.Region, cfg.AZ)

	cnf := &machineryConfig.Config{
		Broker:          cfg.GetRedisBrokerAddr(),
		DefaultQueue:    queueName,
		ResultBackend:   cfg.GetRedisBrokerAddr(),
		ResultsExpireIn: 3600,
		Redis: &machineryConfig.RedisConfig{
			MaxIdle:                3,
			IdleTimeout:            240,
			ReadTimeout:            15,
			WriteTimeout:           15,
			ConnectTimeout:         15,
			NormalTasksPollPeriod:  1000,
			DelayedTasksPollPeriod: 500,
		},
	}

	return machinery.NewServer(cnf)
}
