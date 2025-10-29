package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"workflow_qoder/config"
	"workflow_qoder/tasks"
)

func main() {
	// 创建machinery服务器
	machineryServer, err := config.NewMachineryServer(config.DefaultConfig)
	if err != nil {
		log.Fatalf("创建machinery服务器失败: %v", err)
	}

	// 注册任务（仅注册交换机相关任务）
	tasksMap := map[string]interface{}{
		"create_vrf_on_switch":     tasks.CreateVRFOnSwitch,
		"create_vlan_subinterface": tasks.CreateVLANSubInterface,
	}

	err = machineryServer.RegisterTasks(tasksMap)
	if err != nil {
		log.Fatalf("注册任务失败: %v", err)
	}

	// 创建worker
	worker := machineryServer.NewWorker("switch_worker", 2)

	// 处理退出信号
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	
	go func() {
		<-sigChan
		log.Println("[交换机Worker] 收到退出信号，正在关闭...")
		worker.Quit()
	}()

	log.Println("[交换机Worker] 启动中... 处理队列: vpc_tasks")
	log.Println("[交换机Worker] 处理任务类型: 创建VRF, 创建VLAN子接口")
	
	// 启动worker
	err = worker.Launch()
	if err != nil {
		log.Fatalf("Worker启动失败: %v", err)
	}
}
