package worker
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"workflow_qoder/internal/db"
	"workflow_qoder/tasks"
)

type WorkerConfig struct {
	WorkerType     string
	Region         string
	AZ             string
	PollInterval   int
	MaxConcurrency int
	MySQLHost      string
	MySQLPort      int
	MySQLUser      string
	MySQLPassword  string
	MySQLDatabase  string
}

func loadConfig() *WorkerConfig {
	pollInterval, _ := strconv.Atoi(getEnv("POLL_INTERVAL", "3"))
	maxConcurrency, _ := strconv.Atoi(getEnv("MAX_CONCURRENCY", "3"))
	mysqlPort, _ := strconv.Atoi(getEnv("MYSQL_PORT", "3306"))

	return &WorkerConfig{
		WorkerType:     getEnv("WORKER_TYPE", "switch"),
		Region:         getEnv("REGION", ""),
		AZ:             getEnv("AZ", ""),
		PollInterval:   pollInterval,
		MaxConcurrency: maxConcurrency,
		MySQLHost:      getEnv("MYSQL_HOST", "localhost"),
		MySQLPort:      mysqlPort,
		MySQLUser:      getEnv("MYSQL_USER", "nsp_user"),
		MySQLPassword:  getEnv("MYSQL_PASSWORD", "nsp_pass_2024"),
		MySQLDatabase:  getEnv("MYSQL_DATABASE", "nsp_workflow"),
	}
}

func getEnv(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}

func main() {
	config := loadConfig()
	workerID := fmt.Sprintf("%s-worker-%s-%s", config.WorkerType, config.Region, config.AZ)

	log.Printf("========================================")
	log.Printf("Worker启动: Type=%s, Region=%s, AZ=%s", config.WorkerType, config.Region, config.AZ)
	log.Printf("WorkerID: %s", workerID)
	log.Printf("最大并发: %d, 轮询间隔: %ds", config.MaxConcurrency, config.PollInterval)
	log.Printf("========================================")

	// 初始化MySQL连接
	mysqlConfig := &db.MySQLConfig{
		Host:     config.MySQLHost,
		Port:     config.MySQLPort,
		User:     config.MySQLUser,
		Password: config.MySQLPassword,
		Database: config.MySQLDatabase,
	}

	if err := db.InitMySQL(mysqlConfig); err != nil {
		log.Fatalf("MySQL初始化失败: %v", err)
	}
	defer db.CloseMySQL()

	// 创建Worker实例
	worker := NewWorker(workerID, config)

	// 处理退出信号
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Printf("[Worker %s] 收到退出信号，正在关闭...", workerID)
		cancel()
	}()

	// 启动Worker
	worker.Start(ctx)
}

type Worker struct {
	ID             string
	config         *WorkerConfig
	taskExecutor   TaskExecutor
	semaphore      chan struct{}
	wg             sync.WaitGroup
}

func NewWorker(id string, config *WorkerConfig) *Worker {
	return &Worker{
		ID:           id,
		config:       config,
		taskExecutor: NewTaskExecutor(config.WorkerType),
		semaphore:    make(chan struct{}, config.MaxConcurrency),
	}
}

func (w *Worker) Start(ctx context.Context) {
	log.Printf("[Worker %s] 启动成功，开始轮询任务...", w.ID)

	ticker := time.NewTicker(time.Duration(w.config.PollInterval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("[Worker %s] 等待所有任务完成...", w.ID)
			w.wg.Wait()
			log.Printf("[Worker %s] 已停止", w.ID)
			return
		case <-ticker.C:
			w.pollAndExecuteTasks(ctx)
		}
	}
}

func (w *Worker) pollAndExecuteTasks(ctx context.Context) {
	// 获取待处理任务（最多获取并发数量的任务）
	tasks, err := db.GetPendingTasksByType(w.config.WorkerType, w.config.MaxConcurrency)
	if err != nil {
		log.Printf("[Worker %s] 获取任务失败: %v", w.ID, err)
		return
	}

	if len(tasks) == 0 {
		return
	}

	log.Printf("[Worker %s] 获取到 %d 个待处理任务", w.ID, len(tasks))

	// 并发执行任务
	for _, task := range tasks {
		// 获取信号量，限制并发数
		select {
		case w.semaphore <- struct{}{}:
			w.wg.Add(1)
			go w.executeTask(ctx, task)
		case <-ctx.Done():
			return
		}
	}
}

func (w *Worker) executeTask(ctx context.Context, task *db.Task) {
	defer func() {
		<-w.semaphore
		w.wg.Done()
	}()

	// 认领任务
	if err := db.ClaimTask(task.TaskID, w.ID); err != nil {
		log.Printf("[Worker %s] 认领任务失败 %s: %v", w.ID, task.TaskID, err)
		return
	}

	log.Printf("[Worker %s] 开始执行任务: %s (%s)", w.ID, task.TaskName, task.TaskID)

	// 执行任务
	result, err := w.taskExecutor.Execute(task)
	
	if err != nil {
		log.Printf("[Worker %s] 任务执行失败 %s: %v", w.ID, task.TaskID, err)
		
		// 检查是否需要重试
		if task.RetryCount < task.MaxRetries {
			log.Printf("[Worker %s] 任务 %s 将重试 (当前重试次数: %d/%d)", 
				w.ID, task.TaskID, task.RetryCount+1, task.MaxRetries)
			if err := db.IncrementTaskRetry(task.TaskID); err != nil {
				log.Printf("[Worker %s] 更新重试次数失败: %v", w.ID, err)
			}
		} else {
			// 超过最大重试次数，标记为失败
			errMsg := err.Error()
			if err := db.UpdateTaskStatus(task.TaskID, "failed", nil, &errMsg); err != nil {
				log.Printf("[Worker %s] 更新任务状态为失败失败: %v", w.ID, err)
			}
			
			// 更新工作流状态为失败
			if err := db.UpdateWorkflowStatus(task.WorkflowID, "failed", &errMsg); err != nil {
				log.Printf("[Worker %s] 更新工作流状态失败: %v", w.ID, err)
			}
		}
		return
	}

	// 任务成功完成
	log.Printf("[Worker %s] 任务执行成功: %s", w.ID, task.TaskID)
	
	if err := db.UpdateTaskStatus(task.TaskID, "completed", &result, nil); err != nil {
		log.Printf("[Worker %s] 更新任务状态失败: %v", w.ID, err)
		return
	}

	// 检查是否有后续任务需要创建
	w.checkAndCreateNextTask(task)
}

func (w *Worker) checkAndCreateNextTask(completedTask *db.Task) {
	// 解析payload获取任务链信息
	var payload map[string]interface{}
	if err := json.Unmarshal(completedTask.Payload, &payload); err != nil {
		log.Printf("[Worker %s] 解析任务payload失败: %v", w.ID, err)
		return
	}

	// 根据当前任务确定下一个任务
	nextTaskName := getNextTaskName(completedTask.TaskName)
	if nextTaskName == "" {
		// 没有后续任务，工作流完成
		log.Printf("[Worker %s] 工作流 %s 所有任务已完成", w.ID, completedTask.WorkflowID)
		if err := db.UpdateWorkflowStatus(completedTask.WorkflowID, "completed", nil); err != nil {
			log.Printf("[Worker %s] 更新工作流状态失败: %v", w.ID, err)
		}
		return
	}

	// 创建下一个任务
	nextTaskType := getTaskType(nextTaskName)
	nextTaskID := fmt.Sprintf("%s-%d", completedTask.WorkflowID, time.Now().UnixNano())
	
	nextTask := &db.Task{
		TaskID:        nextTaskID,
		WorkflowID:    completedTask.WorkflowID,
		TaskName:      nextTaskName,
		TaskType:      nextTaskType,
		SequenceOrder: completedTask.SequenceOrder + 1,
		Status:        "pending",
		Payload:       completedTask.Payload,
		MaxRetries:    3,
	}

	if err := db.CreateTask(nextTask); err != nil {
		log.Printf("[Worker %s] 创建下一个任务失败: %v", w.ID, err)
		return
	}

	log.Printf("[Worker %s] 已创建下一个任务: %s (%s)", w.ID, nextTaskName, nextTaskID)
}

// getNextTaskName 获取下一个任务名称
func getNextTaskName(currentTaskName string) string {
	taskChain := map[string]string{
		"create_vrf_on_switch":     "create_vlan_subinterface",
		"create_vlan_subinterface": "create_firewall_zone",
		"create_firewall_zone":     "", // VPC工作流结束
		"create_subnet_on_switch":  "configure_subnet_routing",
		"configure_subnet_routing": "", // 子网工作流结束
	}
	return taskChain[currentTaskName]
}

// getTaskType 根据任务名称获取任务类型
func getTaskType(taskName string) string {
	switchTasks := map[string]bool{
		"create_vrf_on_switch":     true,
		"create_vlan_subinterface": true,
		"create_subnet_on_switch":  true,
		"configure_subnet_routing": true,
	}
	
	if switchTasks[taskName] {
		return "switch"
	}
	return "firewall"
}

// TaskExecutor 任务执行器接口
type TaskExecutor interface {
	Execute(task *db.Task) (string, error)
}

type taskExecutor struct {
	workerType string
}

func NewTaskExecutor(workerType string) TaskExecutor {
	return &taskExecutor{workerType: workerType}
}

func (e *taskExecutor) Execute(task *db.Task) (string, error) {
	// 解析payload
	var req map[string]interface{}
	if err := json.Unmarshal(task.Payload, &req); err != nil {
		return "", fmt.Errorf("解析任务参数失败: %v", err)
	}

	reqJSON, _ := json.Marshal(req)
	requestJSON := string(reqJSON)

	// 根据任务名称调用对应的处理函数
	switch task.TaskName {
	case "create_vrf_on_switch":
		result, err := tasks.CreateVRFOnSwitch(requestJSON)
		return result, err
		
	case "create_vlan_subinterface":
		result, err := tasks.CreateVLANSubInterface(requestJSON)
		return result, err
		
	case "create_firewall_zone":
		result, err := tasks.CreateFirewallZone(requestJSON)
		return result, err
		
	case "create_subnet_on_switch":
		result, err := tasks.CreateSubnetOnSwitch(requestJSON)
		return result, err
		
	case "configure_subnet_routing":
		result, err := tasks.ConfigureSubnetRouting(requestJSON)
		return result, err
		
	default:
		return "", fmt.Errorf("未知的任务类型: %s", task.TaskName)
	}
}
