package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/hibiken/asynq"
)

// SubnetRequest 子网请求
type SubnetRequest struct {
	SubnetName string `json:"subnet_name"`
	VPCName    string `json:"vpc_name"`
	Region     string `json:"region"`
	AZ         string `json:"az"`
	CIDR       string `json:"cidr"`
	WorkflowID string `json:"workflow_id"` // 添加WorkflowID字段
}

// CreateSubnetOnSwitchHandler 在交换机上创建子网 (Asynq Handler)
func CreateSubnetOnSwitchHandler(client *asynq.Client, queueName string, rdb *redis.Client) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var req SubnetRequest
		if err := json.Unmarshal(t.Payload(), &req); err != nil {
			return fmt.Errorf("解析请求失败: %v", err)
		}

		log.Printf("[Workflow-Subnet-Step1] [交换机任务] 开始创建子网: %s (CIDR: %s, VPC: %s)",
			req.SubnetName, req.CIDR, req.VPCName)
		updateWorkflowState(ctx, rdb, req.WorkflowID, "RUNNING:create_subnet_on_switch")

		// 模拟执行任务
		time.Sleep(2 * time.Second)

		log.Printf("[Workflow-Subnet-Step1] ✓ 交换机上成功创建子网: %s, CIDR: %s", req.SubnetName, req.CIDR)
		updateWorkflowState(ctx, rdb, req.WorkflowID, "SUCCESS:create_subnet_on_switch")

		// 入队下一个任务
		nextTask := asynq.NewTask("configure_subnet_routing", t.Payload())
		if _, err := client.Enqueue(nextTask, asynq.Queue(queueName)); err != nil {
			return fmt.Errorf("入队下一任务失败: %v", err)
		}
		return nil
	}
}

// ConfigureSubnetRoutingHandler 配置子网路由 (Asynq Handler)
func ConfigureSubnetRoutingHandler(rdb *redis.Client) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var req SubnetRequest
		if err := json.Unmarshal(t.Payload(), &req); err != nil {
			return fmt.Errorf("解析请求失败: %v", err)
		}

		log.Printf("[Workflow-Subnet-Step2] [交换机任务] 开始配置子网路由: %s", req.SubnetName)
		updateWorkflowState(ctx, rdb, req.WorkflowID, "RUNNING:configure_subnet_routing")

		// 模拟执行任务
		time.Sleep(2 * time.Second)

		log.Printf("[Workflow-Subnet-Step2] ✓ 成功配置子网路由: %s", req.SubnetName)
		log.Printf("[Workflow-Subnet-Complete] ✓✓✓ 子网 %s 创建工作流全部完成 ✓✓✓", req.SubnetName)
		updateWorkflowState(ctx, rdb, req.WorkflowID, "COMPLETED")

		return nil
	}
}

// ===== 以下是兼容旧的machinery接口的函数，保留但不使用 =====

// CreateSubnetOnSwitch 在交换机上创建子网
func CreateSubnetOnSwitch(args ...string) (string, error) {
	// 使用最后一个参数（最新的输入）
	requestJSON := args[len(args)-1]

	var req SubnetRequest
	if err := json.Unmarshal([]byte(requestJSON), &req); err != nil {
		return "", fmt.Errorf("解析请求失败: %v", err)
	}

	log.Printf("[Workflow-Subnet-Step1] [交换机任务] 开始创建子网: %s (CIDR: %s, VPC: %s)",
		req.SubnetName, req.CIDR, req.VPCName)

	// 模拟执行任务
	time.Sleep(2 * time.Second)

	result := TaskResult{
		Success:   true,
		Message:   fmt.Sprintf("交换机上成功创建子网: %s, CIDR: %s", req.SubnetName, req.CIDR),
		TaskName:  "create_subnet_on_switch",
		Timestamp: time.Now().Unix(),
	}
	log.Printf("[Workflow-Subnet-Step1] ✓ %s", result.Message)

	// 将结果传递给下一个任务
	return requestJSON, nil
}

// ConfigureSubnetRouting 配置子网路由
func ConfigureSubnetRouting(args ...string) (string, error) {
	// 使用最后一个参数（最新的输入）
	requestJSON := args[len(args)-1]

	var req SubnetRequest
	if err := json.Unmarshal([]byte(requestJSON), &req); err != nil {
		return "", fmt.Errorf("解析请求失败: %v", err)
	}

	log.Printf("[Workflow-Subnet-Step2] [交换机任务] 开始配置子网路由: %s", req.SubnetName)

	// 模拟执行任务
	time.Sleep(2 * time.Second)

	result := TaskResult{
		Success:   true,
		Message:   fmt.Sprintf("成功配置子网路由: %s", req.SubnetName),
		TaskName:  "configure_subnet_routing",
		Timestamp: time.Now().Unix(),
	}
	log.Printf("[Workflow-Subnet-Step2] ✓ %s", result.Message)
	log.Printf("[Workflow-Subnet-Complete] ✓✓✓ 子网 %s 创建工作流全部完成 ✓✓✓", req.SubnetName)

	// 返回最终结果
	resultJSON, _ := json.Marshal(result)
	return string(resultJSON), nil
}
