package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"workflow_qoder/internal/mq"
	"workflow_qoder/internal/queue"
	"workflow_qoder/tasks"
)

func main() {
	log.Println("========================================")
	log.Println("Worker 启动中...")
	log.Println("========================================")

	region := os.Getenv("REGION")
	az := os.Getenv("AZ")
	workerType := os.Getenv("WORKER_TYPE")

	if region == "" || az == "" || workerType == "" {
		log.Fatal("必须设置环境变量 REGION, AZ 和 WORKER_TYPE")
	}

	log.Printf("[Worker] Region=%s, AZ=%s, Type=%s", region, az, workerType)

	namesrvAddr := os.Getenv("ROCKETMQ_NAMESRV")
	if namesrvAddr == "" {
		namesrvAddr = "namesrv:9876"
	}

	var deviceType queue.DeviceType
	switch workerType {
	case "switch":
		deviceType = queue.DeviceTypeSwitch
	case "loadbalancer":
		deviceType = queue.DeviceTypeLoadBalancer
	case "firewall":
		deviceType = queue.DeviceTypeFirewall
	default:
		log.Fatalf("不支持的 WORKER_TYPE: %s (支持: switch, loadbalancer, firewall)", workerType)
	}

	taskTopic := queue.GetTopicName(region, az, deviceType)
	callbackTopic := queue.GetCallbackTopicName(region, az)
	consumerGroup := queue.GetConsumerGroup(region, az, deviceType)

	producer, err := mq.NewProducer(namesrvAddr)
	if err != nil {
		log.Fatalf("[Worker] 创建Producer失败: %v", err)
	}
	defer producer.Close()

	if err := producer.EnsureTopicExists(taskTopic); err != nil {
		log.Printf("[Worker] 创建任务topic失败(可能已存在): %v", err)
	}

	consumer, err := mq.NewConsumer(namesrvAddr, consumerGroup)
	if err != nil {
		log.Fatalf("[Worker] 创建Consumer失败: %v", err)
	}
	defer consumer.Close()

	switch deviceType {
	case queue.DeviceTypeSwitch:
		consumer.RegisterHandler("create_vrf_on_switch", tasks.CreateVRFOnSwitchHandler(producer, callbackTopic))
		consumer.RegisterHandler("create_vlan_subinterface", tasks.CreateVLANSubInterfaceHandler(producer, callbackTopic))
		consumer.RegisterHandler("create_subnet_on_switch", tasks.CreateSubnetOnSwitchHandler(producer, callbackTopic))
		consumer.RegisterHandler("configure_subnet_routing", tasks.ConfigureSubnetRoutingHandler(producer, callbackTopic))
	case queue.DeviceTypeFirewall:
		consumer.RegisterHandler("create_firewall_zone", tasks.CreateFirewallZoneHandler(producer, callbackTopic))
	case queue.DeviceTypeLoadBalancer:
		consumer.RegisterHandler("create_lb_pool", tasks.CreateLBPoolHandler(producer, callbackTopic))
		consumer.RegisterHandler("configure_lb_listener", tasks.ConfigureLBListenerHandler(producer, callbackTopic))
	}

	if err := consumer.Subscribe(taskTopic); err != nil {
		log.Fatalf("[Worker] 订阅topic失败: %v", err)
	}

	if err := consumer.Start(); err != nil {
		log.Fatalf("[Worker] 启动Consumer失败: %v", err)
	}

	log.Printf("[Worker %s-%s-%s] 启动成功, 任务topic=%s, 回调topic=%s", region, az, workerType, taskTopic, callbackTopic)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Printf("[Worker %s-%s-%s] 收到退出信号，正在关闭...", region, az, workerType)
	log.Printf("[Worker %s-%s-%s] 已关闭", region, az, workerType)
}