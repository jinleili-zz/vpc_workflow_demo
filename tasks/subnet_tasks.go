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

type SubnetRequest struct {
	SubnetName string `json:"subnet_name"`
	VPCName    string `json:"vpc_name"`
	Region     string `json:"region"`
	AZ         string `json:"az"`
	CIDR       string `json:"cidr"`
	WorkflowID string `json:"workflow_id"`
}

func CreateSubnetOnSwitchHandler(client *asynq.Client, queueName string, rdb *redis.Client) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var req SubnetRequest
		if err := json.Unmarshal(t.Payload(), &req); err != nil {
			return fmt.Errorf("解析请求失败: %v", err)
		}

		log.Printf("[Workflow-Subnet-Step1] [交换机任务] 开始创建子网: %s (CIDR: %s, VPC: %s)",
			req.SubnetName, req.CIDR, req.VPCName)
		updateWorkflowState(ctx, rdb, req.WorkflowID, "RUNNING:create_subnet_on_switch")

		time.Sleep(2 * time.Second)

		log.Printf("[Workflow-Subnet-Step1] ✓ 交换机上成功创建子网: %s, CIDR: %s", req.SubnetName, req.CIDR)
		updateWorkflowState(ctx, rdb, req.WorkflowID, "SUCCESS:create_subnet_on_switch")

		nextTask := asynq.NewTask("configure_subnet_routing", t.Payload())
		if _, err := client.Enqueue(nextTask, asynq.Queue(queueName)); err != nil {
			return fmt.Errorf("入队下一任务失败: %v", err)
		}
		return nil
	}
}

func ConfigureSubnetRoutingHandler(rdb *redis.Client) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var req SubnetRequest
		if err := json.Unmarshal(t.Payload(), &req); err != nil {
			return fmt.Errorf("解析请求失败: %v", err)
		}

		log.Printf("[Workflow-Subnet-Step2] [交换机任务] 开始配置子网路由: %s", req.SubnetName)
		updateWorkflowState(ctx, rdb, req.WorkflowID, "RUNNING:configure_subnet_routing")

		time.Sleep(2 * time.Second)

		log.Printf("[Workflow-Subnet-Step2] ✓ 成功配置子网路由: %s", req.SubnetName)
		log.Printf("[Workflow-Subnet-Complete] ✓✓✓ 子网 %s 创建工作流全部完成 ✓✓✓", req.SubnetName)
		updateWorkflowState(ctx, rdb, req.WorkflowID, "COMPLETED")

		return nil
	}
}
