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

// VPCRequest VPC创建请求
type VPCRequest struct {
	VPCName      string `json:"vpc_name"`
	VPCID        string `json:"vpc_id"`
	VRFName      string `json:"vrf_name"`
	VLANId       int    `json:"vlan_id"`
	FirewallZone string `json:"firewall_zone"`
	WorkflowID   string `json:"workflow_id"` // 添加WorkflowID字段
}

// TaskResult 任务执行结果
type TaskResult struct {
	Success   bool   `json:"success"`
	Message   string `json:"message"`
	TaskName  string `json:"task_name"`
	Timestamp int64  `json:"timestamp"`
}

// 更新工作流状态
func updateWorkflowState(ctx context.Context, rdb *redis.Client, workflowID, state string) {
	if workflowID == "" || rdb == nil {
		return
	}
	stateKey := fmt.Sprintf("workflow:%s:state", workflowID)
	if err := rdb.Set(ctx, stateKey, state, 24*time.Hour).Err(); err != nil {
		log.Printf("[更新状态失败] workflowID=%s, state=%s, error=%v", workflowID, state, err)
	}
}

// CreateVRFOnSwitchHandler 在交换机上创建VRF (Asynq Handler)
func CreateVRFOnSwitchHandler(client *asynq.Client, queueName string, rdb *redis.Client) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var req VPCRequest
		if err := json.Unmarshal(t.Payload(), &req); err != nil {
			return fmt.Errorf("解析请求失败: %v", err)
		}

		log.Printf("[Workflow-Step1] [交换机任务] 开始创建VRF: %s (VPC: %s)", req.VRFName, req.VPCName)
		updateWorkflowState(ctx, rdb, req.WorkflowID, "RUNNING:create_vrf_on_switch")

		// 模拟执行任务
		time.Sleep(2 * time.Second)

		log.Printf("[Workflow-Step1] ✓ 交换机上成功创建VRF: %s, 配置命令: ip vrf %s", req.VRFName, req.VRFName)
		updateWorkflowState(ctx, rdb, req.WorkflowID, "SUCCESS:create_vrf_on_switch")

		// 入队下一个任务
		nextTask := asynq.NewTask("create_vlan_subinterface", t.Payload())
		if _, err := client.Enqueue(nextTask, asynq.Queue(queueName)); err != nil {
			return fmt.Errorf("入队下一任务失败: %v", err)
		}
		return nil
	}
}

// CreateVLANSubInterfaceHandler 创建VLAN子接口 (Asynq Handler)
func CreateVLANSubInterfaceHandler(client *asynq.Client, queueName string, rdb *redis.Client) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var req VPCRequest
		if err := json.Unmarshal(t.Payload(), &req); err != nil {
			return fmt.Errorf("解析请求失败: %v", err)
		}

		log.Printf("[Workflow-Step2] [交换机任务] 开始创建VLAN子接口: VLAN %d (VPC: %s)", req.VLANId, req.VPCName)
		updateWorkflowState(ctx, rdb, req.WorkflowID, "RUNNING:create_vlan_subinterface")

		// 模拟执行任务
		time.Sleep(2 * time.Second)

		log.Printf("[Workflow-Step2] ✓ 交换机上成功创建VLAN子接口: VLAN %d, 接口配置: interface Vlan%d, ip vrf forwarding %s",
			req.VLANId, req.VLANId, req.VRFName)
		updateWorkflowState(ctx, rdb, req.WorkflowID, "SUCCESS:create_vlan_subinterface")

		// 入队下一个任务
		nextTask := asynq.NewTask("create_firewall_zone", t.Payload())
		if _, err := client.Enqueue(nextTask, asynq.Queue(queueName)); err != nil {
			return fmt.Errorf("入队下一任务失败: %v", err)
		}
		return nil
	}
}

// CreateFirewallZoneHandler 创建防火墙安全区域 (Asynq Handler)
func CreateFirewallZoneHandler(rdb *redis.Client) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var req VPCRequest
		if err := json.Unmarshal(t.Payload(), &req); err != nil {
			return fmt.Errorf("解析请求失败: %v", err)
		}

		log.Printf("[Workflow-Step3] [防火墙任务] 开始创建安全区域: %s (VPC: %s)", req.FirewallZone, req.VPCName)
		updateWorkflowState(ctx, rdb, req.WorkflowID, "RUNNING:create_firewall_zone")

		// 模拟执行任务
		time.Sleep(2 * time.Second)

		log.Printf("[Workflow-Step3] ✓ 防火墙上成功创建安全区域: %s, 配置命令: security-zone name %s, set priority 100",
			req.FirewallZone, req.FirewallZone)
		log.Printf("[Workflow-Complete] ✓✓✓ VPC %s 创建工作流全部完成 ✓✓✓", req.VPCName)
		updateWorkflowState(ctx, rdb, req.WorkflowID, "COMPLETED")

		return nil
	}
}

// ===== 以下是兼容旧的machinery接口的函数，保留但不使用 =====

// CreateVRFOnSwitch 在交换机上创廻VRF
func CreateVRFOnSwitch(args ...string) (string, error) {
	// 使用最后一个参数（最新的输入）
	requestJSON := args[len(args)-1]

	var req VPCRequest
	if err := json.Unmarshal([]byte(requestJSON), &req); err != nil {
		return "", fmt.Errorf("解析请求失败: %v", err)
	}

	log.Printf("[Workflow-Step1] [交换机任务] 开始创建VRF: %s (VPC: %s)", req.VRFName, req.VPCName)

	// 模拟执行任务
	time.Sleep(2 * time.Second)

	// 构造结果
	result := TaskResult{
		Success:   true,
		Message:   fmt.Sprintf("交换机上成功创建VRF: %s, 配置命令: ip vrf %s", req.VRFName, req.VRFName),
		TaskName:  "create_vrf_on_switch",
		Timestamp: time.Now().Unix(),
	}
	log.Printf("[Workflow-Step1] ✓ %s", result.Message)

	// 将结果传递给下一个任务
	return requestJSON, nil
}

// CreateVLANSubInterface 创建VLAN子接口
func CreateVLANSubInterface(args ...string) (string, error) {
	// 使用最后一个参数（最新的输入）
	requestJSON := args[len(args)-1]

	var req VPCRequest
	if err := json.Unmarshal([]byte(requestJSON), &req); err != nil {
		return "", fmt.Errorf("解析请求失败: %v", err)
	}

	log.Printf("[Workflow-Step2] [交换机任务] 开始创建VLAN子接口: VLAN %d (VPC: %s)", req.VLANId, req.VPCName)

	// 模拟执行任务
	time.Sleep(2 * time.Second)

	// 构造结果
	result := TaskResult{
		Success:   true,
		Message:   fmt.Sprintf("交换机上成功创建VLAN子接口: VLAN %d, 接口配置: interface Vlan%d, ip vrf forwarding %s", req.VLANId, req.VLANId, req.VRFName),
		TaskName:  "create_vlan_subinterface",
		Timestamp: time.Now().Unix(),
	}
	log.Printf("[Workflow-Step2] ✓ %s", result.Message)

	// 将结果传递给下一个任务
	return requestJSON, nil
}

// CreateFirewallZone 创建防火墙安全区域
func CreateFirewallZone(args ...string) (string, error) {
	// 使用最后一个参数（最新的输入）
	requestJSON := args[len(args)-1]

	var req VPCRequest
	if err := json.Unmarshal([]byte(requestJSON), &req); err != nil {
		return "", fmt.Errorf("解析请求失败: %v", err)
	}

	log.Printf("[Workflow-Step3] [防火墙任务] 开始创建安全区域: %s (VPC: %s)", req.FirewallZone, req.VPCName)

	// 模拟执行任务
	time.Sleep(2 * time.Second)

	// 构造结果
	result := TaskResult{
		Success:   true,
		Message:   fmt.Sprintf("防火墙上成功创建安全区域: %s, 配置命令: security-zone name %s, set priority 100", req.FirewallZone, req.FirewallZone),
		TaskName:  "create_firewall_zone",
		Timestamp: time.Now().Unix(),
	}
	log.Printf("[Workflow-Step3] ✓ %s", result.Message)
	log.Printf("[Workflow-Complete] ✓✓✓ VPC %s 创建工作流全部完成 ✓✓✓", req.VPCName)

	// 返回最终结果
	resultJSON, _ := json.Marshal(result)
	return string(resultJSON), nil
}
