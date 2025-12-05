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

type VPCRequest struct {
	VPCName      string `json:"vpc_name"`
	VPCID        string `json:"vpc_id"`
	VRFName      string `json:"vrf_name"`
	VLANId       int    `json:"vlan_id"`
	FirewallZone string `json:"firewall_zone"`
	WorkflowID   string `json:"workflow_id"`
}

type TaskResult struct {
	Success   bool   `json:"success"`
	Message   string `json:"message"`
	TaskName  string `json:"task_name"`
	Timestamp int64  `json:"timestamp"`
}

func updateWorkflowState(ctx context.Context, rdb *redis.Client, workflowID, state string) {
	if workflowID == "" || rdb == nil {
		return
	}
	stateKey := fmt.Sprintf("workflow:%s:state", workflowID)
	if err := rdb.Set(ctx, stateKey, state, 24*time.Hour).Err(); err != nil {
		log.Printf("[更新状态失败] workflowID=%s, state=%s, error=%v", workflowID, state, err)
	}
}

func CreateVRFOnSwitchHandler(client *asynq.Client, queueName string, rdb *redis.Client) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var req VPCRequest
		if err := json.Unmarshal(t.Payload(), &req); err != nil {
			return fmt.Errorf("解析请求失败: %v", err)
		}

		log.Printf("[Workflow-Step1] [交换机任务] 开始创建VRF: %s (VPC: %s)", req.VRFName, req.VPCName)
		updateWorkflowState(ctx, rdb, req.WorkflowID, "RUNNING:create_vrf_on_switch")

		time.Sleep(2 * time.Second)

		log.Printf("[Workflow-Step1] ✓ 交换机上成功创建VRF: %s, 配置命令: ip vrf %s", req.VRFName, req.VRFName)
		updateWorkflowState(ctx, rdb, req.WorkflowID, "SUCCESS:create_vrf_on_switch")

		nextTask := asynq.NewTask("create_vlan_subinterface", t.Payload())
		if _, err := client.Enqueue(nextTask, asynq.Queue(queueName)); err != nil {
			return fmt.Errorf("入队下一任务失败: %v", err)
		}
		return nil
	}
}

func CreateVLANSubInterfaceHandler(client *asynq.Client, queueName string, rdb *redis.Client) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var req VPCRequest
		if err := json.Unmarshal(t.Payload(), &req); err != nil {
			return fmt.Errorf("解析请求失败: %v", err)
		}

		log.Printf("[Workflow-Step2] [交换机任务] 开始创建VLAN子接口: VLAN %d (VPC: %s)", req.VLANId, req.VPCName)
		updateWorkflowState(ctx, rdb, req.WorkflowID, "RUNNING:create_vlan_subinterface")

		time.Sleep(2 * time.Second)

		log.Printf("[Workflow-Step2] ✓ 交换机上成功创建VLAN子接口: VLAN %d, 接口配置: interface Vlan%d, ip vrf forwarding %s",
			req.VLANId, req.VLANId, req.VRFName)
		updateWorkflowState(ctx, rdb, req.WorkflowID, "SUCCESS:create_vlan_subinterface")

		nextTask := asynq.NewTask("create_firewall_zone", t.Payload())
		if _, err := client.Enqueue(nextTask, asynq.Queue(queueName)); err != nil {
			return fmt.Errorf("入队下一任务失败: %v", err)
		}
		return nil
	}
}

func CreateFirewallZoneHandler(rdb *redis.Client) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var req VPCRequest
		if err := json.Unmarshal(t.Payload(), &req); err != nil {
			return fmt.Errorf("解析请求失败: %v", err)
		}

		log.Printf("[Workflow-Step3] [防火墙任务] 开始创建安全区域: %s (VPC: %s)", req.FirewallZone, req.VPCName)
		updateWorkflowState(ctx, rdb, req.WorkflowID, "RUNNING:create_firewall_zone")

		time.Sleep(2 * time.Second)

		log.Printf("[Workflow-Step3] ✓ 防火墙上成功创建安全区域: %s, 配置命令: security-zone name %s, set priority 100",
			req.FirewallZone, req.FirewallZone)
		log.Printf("[Workflow-Complete] ✓✓✓ VPC %s 创建工作流全部完成 ✓✓✓", req.VPCName)
		updateWorkflowState(ctx, rdb, req.WorkflowID, "COMPLETED")

		return nil
	}
}
