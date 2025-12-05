package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/hibiken/asynq"
)

type TaskPayload struct {
	TaskID     string `json:"task_id"`
	ResourceID string `json:"resource_id"`
	TaskParams string `json:"task_params"`
}

type TaskCallbackPayload struct {
	TaskID       string      `json:"task_id"`
	Status       string      `json:"status"`
	Result       interface{} `json:"result"`
	ErrorMessage string      `json:"error_message"`
}

func notifyTaskCompletion(asynqClient *asynq.Client, callbackQueue, taskID, status string, result interface{}, errorMsg string) error {
	payload := TaskCallbackPayload{
		TaskID:       taskID,
		Status:       status,
		Result:       result,
		ErrorMessage: errorMsg,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("序列化回调载荷失败: %v", err)
	}

	task := asynq.NewTask("task_callback", payloadBytes)
	info, err := asynqClient.Enqueue(task, asynq.Queue(callbackQueue))
	if err != nil {
		return fmt.Errorf("发送回调任务失败: %v", err)
	}

	log.Printf("[Worker] 任务回调已入队: taskID=%s, status=%s, queue=%s, asynq_id=%s", taskID, status, callbackQueue, info.ID)
	return nil
}

func CreateVRFOnSwitchHandler(asynqClient *asynq.Client, callbackQueue string) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var payload TaskPayload
		if err := json.Unmarshal(t.Payload(), &payload); err != nil {
			return fmt.Errorf("解析任务载荷失败: %v", err)
		}

		var params map[string]interface{}
		if err := json.Unmarshal([]byte(payload.TaskParams), &params); err != nil {
			return fmt.Errorf("解析任务参数失败: %v", err)
		}

		vpcName := params["vpc_name"].(string)
		vrfName := params["vrf_name"].(string)

		log.Printf("[Worker] [VRF任务] 开始创建VRF: %s (VPC: %s, TaskID: %s)", vrfName, vpcName, payload.TaskID)

		time.Sleep(2 * time.Second)

		result := map[string]interface{}{
			"message": fmt.Sprintf("交换机上成功创建VRF: %s, 配置命令: ip vrf %s", vrfName, vrfName),
			"vrf_name": vrfName,
			"timestamp": time.Now().Unix(),
		}

		log.Printf("[Worker] [VRF任务] ✓ VRF创建完成: %s", vrfName)

		if err := notifyTaskCompletion(asynqClient, callbackQueue, payload.TaskID, "completed", result, ""); err != nil {
			log.Printf("[Worker] [VRF任务] 回调失败: %v", err)
			return err
		}

		return nil
	}
}

func CreateVLANSubInterfaceHandler(asynqClient *asynq.Client, callbackQueue string) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var payload TaskPayload
		if err := json.Unmarshal(t.Payload(), &payload); err != nil {
			return fmt.Errorf("解析任务载荷失败: %v", err)
		}

		var params map[string]interface{}
		if err := json.Unmarshal([]byte(payload.TaskParams), &params); err != nil {
			return fmt.Errorf("解析任务参数失败: %v", err)
		}

		vpcName := params["vpc_name"].(string)
		vrfName := params["vrf_name"].(string)
		vlanID := int(params["vlan_id"].(float64))

		log.Printf("[Worker] [VLAN任务] 开始创建VLAN子接口: VLAN %d (VPC: %s, TaskID: %s)", vlanID, vpcName, payload.TaskID)

		time.Sleep(2 * time.Second)

		result := map[string]interface{}{
			"message": fmt.Sprintf("交换机上成功创建VLAN子接口: VLAN %d, 接口配置: interface Vlan%d, ip vrf forwarding %s", vlanID, vlanID, vrfName),
			"vlan_id": vlanID,
			"vrf_name": vrfName,
			"timestamp": time.Now().Unix(),
		}

		log.Printf("[Worker] [VLAN任务] ✓ VLAN子接口创建完成: VLAN %d", vlanID)

		if err := notifyTaskCompletion(asynqClient, callbackQueue, payload.TaskID, "completed", result, ""); err != nil {
			log.Printf("[Worker] [VLAN任务] 回调失败: %v", err)
			return err
		}

		return nil
	}
}

func CreateFirewallZoneHandler(asynqClient *asynq.Client, callbackQueue string) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var payload TaskPayload
		if err := json.Unmarshal(t.Payload(), &payload); err != nil {
			return fmt.Errorf("解析任务载荷失败: %v", err)
		}

		var params map[string]interface{}
		if err := json.Unmarshal([]byte(payload.TaskParams), &params); err != nil {
			return fmt.Errorf("解析任务参数失败: %v", err)
		}

		vpcName := params["vpc_name"].(string)
		firewallZone := params["firewall_zone"].(string)

		log.Printf("[Worker] [防火墙任务] 开始创建安全区域: %s (VPC: %s, TaskID: %s)", firewallZone, vpcName, payload.TaskID)

		time.Sleep(2 * time.Second)

		result := map[string]interface{}{
			"message": fmt.Sprintf("防火墙上成功创建安全区域: %s, 配置命令: security-zone name %s, set priority 100", firewallZone, firewallZone),
			"firewall_zone": firewallZone,
			"timestamp": time.Now().Unix(),
		}

		log.Printf("[Worker] [防火墙任务] ✓ 防火墙安全区域创建完成: %s", firewallZone)
		log.Printf("[Worker] ✓✓✓ VPC %s 所有任务执行完成 ✓✓✓", vpcName)

		if err := notifyTaskCompletion(asynqClient, callbackQueue, payload.TaskID, "completed", result, ""); err != nil {
			log.Printf("[Worker] [防火墙任务] 回调失败: %v", err)
			return err
		}

		return nil
	}
}

func CreateSubnetOnSwitchHandler(asynqClient *asynq.Client, callbackQueue string) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var payload TaskPayload
		if err := json.Unmarshal(t.Payload(), &payload); err != nil {
			return fmt.Errorf("解析任务载荷失败: %v", err)
		}

		var params map[string]interface{}
		if err := json.Unmarshal([]byte(payload.TaskParams), &params); err != nil {
			return fmt.Errorf("解析任务参数失败: %v", err)
		}

		subnetName := params["subnet_name"].(string)
		vpcName := params["vpc_name"].(string)
		cidr := params["cidr"].(string)

		log.Printf("[Worker] [子网任务] 开始创建子网: %s (CIDR: %s, VPC: %s, TaskID: %s)", subnetName, cidr, vpcName, payload.TaskID)

		time.Sleep(2 * time.Second)

		result := map[string]interface{}{
			"message": fmt.Sprintf("交换机上成功创建子网: %s, CIDR: %s", subnetName, cidr),
			"subnet_name": subnetName,
			"cidr": cidr,
			"timestamp": time.Now().Unix(),
		}

		log.Printf("[Worker] [子网任务] ✓ 子网创建完成: %s", subnetName)

		if err := notifyTaskCompletion(asynqClient, callbackQueue, payload.TaskID, "completed", result, ""); err != nil {
			log.Printf("[Worker] [子网任务] 回调失败: %v", err)
			return err
		}

		return nil
	}
}

func ConfigureSubnetRoutingHandler(asynqClient *asynq.Client, callbackQueue string) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var payload TaskPayload
		if err := json.Unmarshal(t.Payload(), &payload); err != nil {
			return fmt.Errorf("解析任务载荷失败: %v", err)
		}

		var params map[string]interface{}
		if err := json.Unmarshal([]byte(payload.TaskParams), &params); err != nil {
			return fmt.Errorf("解析任务参数失败: %v", err)
		}

		subnetName := params["subnet_name"].(string)

		log.Printf("[Worker] [路由任务] 开始配置子网路由: %s (TaskID: %s)", subnetName, payload.TaskID)

		time.Sleep(2 * time.Second)

		result := map[string]interface{}{
			"message": fmt.Sprintf("成功配置子网路由: %s", subnetName),
			"subnet_name": subnetName,
			"timestamp": time.Now().Unix(),
		}

		log.Printf("[Worker] [路由任务] ✓ 子网路由配置完成: %s", subnetName)
		log.Printf("[Worker] ✓✓✓ 子网 %s 所有任务执行完成 ✓✓✓", subnetName)

		if err := notifyTaskCompletion(asynqClient, callbackQueue, payload.TaskID, "completed", result, ""); err != nil {
			log.Printf("[Worker] [路由任务] 回调失败: %v", err)
			return err
		}

		return nil
	}
}