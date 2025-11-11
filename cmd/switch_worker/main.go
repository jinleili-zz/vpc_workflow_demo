package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"workflow_qoder/internal/config"
	"workflow_qoder/internal/rocketmq"
	"workflow_qoder/tasks"

	"github.com/go-redis/redis/v8"
)

func main() {
	// 加载配置
	cfg := config.LoadConfig()

	if cfg.Region == "" || cfg.AZ == "" {
		log.Fatalf("[Switch Worker] 必须设置 REGION 和 AZ 环境变量")
	}

	log.Printf("[Switch Worker] 正在启动 Region=%s, AZ=%s", cfg.Region, cfg.AZ)
	log.Printf("[Switch Worker] RocketMQ NameServer: %v", cfg.RocketMQ.NameServers)

	// 解析NameServer域名为IP地址
	resolvedNameServers := make([]string, 0, len(cfg.RocketMQ.NameServers))
	for _, addr := range cfg.RocketMQ.NameServers {
		parts := strings.Split(addr, ":")
		if len(parts) != 2 {
			continue
		}
		host, port := parts[0], parts[1]
		ips, err := net.LookupHost(host)
		if err != nil || len(ips) == 0 {
			resolvedNameServers = append(resolvedNameServers, addr)
		} else {
			resolvedAddr := fmt.Sprintf("%s:%s", ips[0], port)
			log.Printf("[Switch Worker] 解析 %s -> %s", addr, resolvedAddr)
			resolvedNameServers = append(resolvedNameServers, resolvedAddr)
		}
	}

	// 创建 Redis 客户端（用于状态存储）
	redisClient := redis.NewClient(&redis.Options{
		Addr: cfg.Redis.Addr,
		DB:   cfg.Redis.DataDB,
	})

	// 创建 RocketMQ 生产者（用于链式任务的下一个任务）
	rmqCfg := &rocketmq.Config{
		NameServerAddrs: resolvedNameServers,
		GroupName:       fmt.Sprintf("%s_switch_worker_%s_%s", cfg.RocketMQ.ProducerGroup, cfg.Region, cfg.AZ),
		InstanceName:    fmt.Sprintf("switch_worker_%s_%s", cfg.Region, cfg.AZ),
		RetryTimes:      cfg.RocketMQ.RetryTimes,
	}

	producer, err := rocketmq.NewProducer(rmqCfg, redisClient)
	if err != nil {
		log.Fatalf("[Switch Worker] 创建 RocketMQ 生产者失败: %v", err)
	}
	defer producer.Close()

	// 创建 RocketMQ 消费者
	topic := fmt.Sprintf("%s_%s_%s", cfg.RocketMQ.VPCTopic, cfg.Region, cfg.AZ)
	rmqCfg.GroupName = fmt.Sprintf("%s_switch_worker_%s_%s", cfg.RocketMQ.ConsumerGroup, cfg.Region, cfg.AZ)

	consumer, err := rocketmq.NewConsumer(rmqCfg, topic, redisClient, producer)
	if err != nil {
		log.Fatalf("[Switch Worker] 创建 RocketMQ 消费者失败: %v", err)
	}
	defer consumer.Close()

	// 注册任务处理器（仅注册交换机相关任务）
	consumer.RegisterTask("create_vrf_on_switch", wrapTaskHandler(tasks.CreateVRFOnSwitch))
	consumer.RegisterTask("create_vlan_subinterface", wrapTaskHandler(tasks.CreateVLANSubInterface))
	consumer.RegisterTask("create_subnet_on_switch", wrapTaskHandler(tasks.CreateSubnetOnSwitch))
	consumer.RegisterTask("configure_subnet_routing", wrapTaskHandler(tasks.ConfigureSubnetRouting))

	// 启动消费者
	if err := consumer.Start(); err != nil {
		log.Fatalf("[Switch Worker] 启动消费者失败: %v", err)
	}

	log.Printf("[Switch Worker %s] 启动成功... 处理Topic: %s", cfg.AZ, topic)
	log.Printf("[Switch Worker %s] 处理任务类型: 创建VRF, 创建VLAN子接口, 创建子网, 配置路由", cfg.AZ)

	// 处理退出信号
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	<-sigChan
	log.Printf("[Switch Worker %s] 收到退出信号，正在关闭...", cfg.AZ)
}

// wrapTaskHandler 封装任务处理器
func wrapTaskHandler(handler func(...string) (string, error)) rocketmq.TaskHandler {
	return func(args ...interface{}) (interface{}, error) {
		// 转换参数
		strArgs := make([]string, len(args))
		for i, arg := range args {
			if str, ok := arg.(string); ok {
				strArgs[i] = str
			}
		}
		return handler(strArgs...)
	}
}
