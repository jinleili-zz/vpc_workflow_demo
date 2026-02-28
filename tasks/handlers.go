package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hibiken/asynq"
	"github.com/yourorg/nsp-common/pkg/logger"
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

	logger.Info("任务回调已入队", "taskID", taskID, "status", status, "queue", callbackQueue, "asynqID", info.ID)
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

		logger.InfoContext(ctx, "开始创建VRF", "vrfName", vrfName, "vpcName", vpcName, "taskID", payload.TaskID)

		time.Sleep(2 * time.Second)

		result := map[string]interface{}{
			"message": fmt.Sprintf("交换机上成功创建VRF: %s, 配置命令: ip vrf %s", vrfName, vrfName),
			"vrf_name": vrfName,
			"timestamp": time.Now().Unix(),
		}

		logger.InfoContext(ctx, "VRF创建完成", "vrfName", vrfName)

		if err := notifyTaskCompletion(asynqClient, callbackQueue, payload.TaskID, "completed", result, ""); err != nil {
			logger.InfoContext(ctx, "VRF任务回调失败", "error", err)
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

		logger.InfoContext(ctx, "开始创建VLAN子接口", "vlanID", vlanID, "vpcName", vpcName, "taskID", payload.TaskID)

		time.Sleep(2 * time.Second)

		result := map[string]interface{}{
			"message": fmt.Sprintf("交换机上成功创建VLAN子接口: VLAN %d, 接口配置: interface Vlan%d, ip vrf forwarding %s", vlanID, vlanID, vrfName),
			"vlan_id": vlanID,
			"vrf_name": vrfName,
			"timestamp": time.Now().Unix(),
		}

		logger.InfoContext(ctx, "VLAN子接口创建完成", "vlanID", vlanID)

		if err := notifyTaskCompletion(asynqClient, callbackQueue, payload.TaskID, "completed", result, ""); err != nil {
			logger.InfoContext(ctx, "VLAN任务回调失败", "error", err)
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

		logger.InfoContext(ctx, "开始创建安全区域", "firewallZone", firewallZone, "vpcName", vpcName, "taskID", payload.TaskID)

		time.Sleep(2 * time.Second)

		result := map[string]interface{}{
			"message": fmt.Sprintf("防火墙上成功创建安全区域: %s, 配置命令: security-zone name %s, set priority 100", firewallZone, firewallZone),
			"firewall_zone": firewallZone,
			"timestamp": time.Now().Unix(),
		}

		logger.InfoContext(ctx, "防火墙安全区域创建完成", "firewallZone", firewallZone)
		logger.InfoContext(ctx, "VPC所有任务执行完成", "vpcName", vpcName)

		if err := notifyTaskCompletion(asynqClient, callbackQueue, payload.TaskID, "completed", result, ""); err != nil {
			logger.InfoContext(ctx, "防火墙任务回调失败", "error", err)
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

		logger.InfoContext(ctx, "开始创建子网", "subnetName", subnetName, "cidr", cidr, "vpcName", vpcName, "taskID", payload.TaskID)

		time.Sleep(2 * time.Second)

		if cidr == "10.0.99.0/24" {
			errorMsg := fmt.Sprintf("CIDR冲突: %s 在VPC %s 中已存在", cidr, vpcName)
			logger.InfoContext(ctx, "子网创建失败", "subnetName", subnetName, "error", errorMsg)
			
			if err := notifyTaskCompletion(asynqClient, callbackQueue, payload.TaskID, "failed", nil, errorMsg); err != nil {
				logger.InfoContext(ctx, "子网任务回调失败", "error", err)
				return err
			}
			return fmt.Errorf("%s", errorMsg)
		}

		result := map[string]interface{}{
			"message": fmt.Sprintf("交换机上成功创建子网: %s, CIDR: %s", subnetName, cidr),
			"subnet_name": subnetName,
			"cidr": cidr,
			"timestamp": time.Now().Unix(),
		}

		logger.InfoContext(ctx, "子网创建完成", "subnetName", subnetName)

		if err := notifyTaskCompletion(asynqClient, callbackQueue, payload.TaskID, "completed", result, ""); err != nil {
			logger.InfoContext(ctx, "子网任务回调失败", "error", err)
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

		logger.InfoContext(ctx, "开始配置子网路由", "subnetName", subnetName, "taskID", payload.TaskID)

		time.Sleep(2 * time.Second)

		result := map[string]interface{}{
			"message": fmt.Sprintf("成功配置子网路由: %s", subnetName),
			"subnet_name": subnetName,
			"timestamp": time.Now().Unix(),
		}

		logger.InfoContext(ctx, "子网路由配置完成", "subnetName", subnetName)
		logger.InfoContext(ctx, "子网所有任务执行完成", "subnetName", subnetName)

		if err := notifyTaskCompletion(asynqClient, callbackQueue, payload.TaskID, "completed", result, ""); err != nil {
			logger.InfoContext(ctx, "路由任务回调失败", "error", err)
			return err
		}

		return nil
	}
}

func CreateLBPoolHandler(asynqClient *asynq.Client, callbackQueue string) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var payload TaskPayload
		if err := json.Unmarshal(t.Payload(), &payload); err != nil {
			return fmt.Errorf("解析任务载荷失败: %v", err)
		}

		var params map[string]interface{}
		if err := json.Unmarshal([]byte(payload.TaskParams), &params); err != nil {
			return fmt.Errorf("解析任务参数失败: %v", err)
		}

		poolName := "default-pool"
		if name, ok := params["pool_name"].(string); ok {
			poolName = name
		}

		logger.InfoContext(ctx, "开始创建LB Pool", "poolName", poolName, "taskID", payload.TaskID)

		time.Sleep(2 * time.Second)

		result := map[string]interface{}{
			"message":   fmt.Sprintf("负载均衡器上成功创建Pool: %s", poolName),
			"pool_name": poolName,
			"timestamp": time.Now().Unix(),
		}

		logger.InfoContext(ctx, "LB Pool创建完成", "poolName", poolName)

		if err := notifyTaskCompletion(asynqClient, callbackQueue, payload.TaskID, "completed", result, ""); err != nil {
			logger.InfoContext(ctx, "负载均衡任务回调失败", "error", err)
			return err
		}

		return nil
	}
}

func ConfigureLBListenerHandler(asynqClient *asynq.Client, callbackQueue string) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var payload TaskPayload
		if err := json.Unmarshal(t.Payload(), &payload); err != nil {
			return fmt.Errorf("解析任务载荷失败: %v", err)
		}

		var params map[string]interface{}
		if err := json.Unmarshal([]byte(payload.TaskParams), &params); err != nil {
			return fmt.Errorf("解析任务参数失败: %v", err)
		}

		listenerName := "default-listener"
		if name, ok := params["listener_name"].(string); ok {
			listenerName = name
		}

		logger.InfoContext(ctx, "开始配置LB Listener", "listenerName", listenerName, "taskID", payload.TaskID)

		time.Sleep(2 * time.Second)

		result := map[string]interface{}{
			"message":       fmt.Sprintf("负载均衡器上成功配置Listener: %s", listenerName),
			"listener_name": listenerName,
			"timestamp":     time.Now().Unix(),
		}

		logger.InfoContext(ctx, "LB Listener配置完成", "listenerName", listenerName)

		if err := notifyTaskCompletion(asynqClient, callbackQueue, payload.TaskID, "completed", result, ""); err != nil {
			logger.InfoContext(ctx, "负载均衡任务回调失败", "error", err)
			return err
		}

		return nil
	}
}

func CreateFirewallPolicyHandler(asynqClient *asynq.Client, callbackQueue string) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var payload TaskPayload
		if err := json.Unmarshal(t.Payload(), &payload); err != nil {
			return fmt.Errorf("解析任务载荷失败: %v", err)
		}

		var params map[string]interface{}
		if err := json.Unmarshal([]byte(payload.TaskParams), &params); err != nil {
			return fmt.Errorf("解析任务参数失败: %v", err)
		}

		policyName := params["policy_name"].(string)
		sourceZone := params["source_zone"].(string)
		destZone := params["dest_zone"].(string)
		sourceIP := params["source_ip"].(string)
		destIP := params["dest_ip"].(string)
		destPort := params["dest_port"].(string)
		protocol := params["protocol"].(string)
		action := params["action"].(string)

		logger.InfoContext(ctx, "开始创建防火墙策略", "policyName", policyName, "taskID", payload.TaskID)
		logger.InfoContext(ctx, "防火墙策略规则", "sourceZone", sourceZone, "sourceIP", sourceIP, "destZone", destZone, "destIP", destIP, "destPort", destPort, "protocol", protocol)

		time.Sleep(2 * time.Second)

		configCmd := fmt.Sprintf(`
security-policy
 rule name %s
  source-zone %s
  destination-zone %s
  source-address %s
  destination-address %s
  destination-port %s
  protocol %s
  action %s
`, policyName, sourceZone, destZone, sourceIP, destIP, destPort, protocol, action)

		result := map[string]interface{}{
			"message":     fmt.Sprintf("防火墙策略创建成功: %s", policyName),
			"policy_name": policyName,
			"source_zone": sourceZone,
			"dest_zone":   destZone,
			"config_cmd":  configCmd,
			"timestamp":   time.Now().Unix(),
		}

		logger.InfoContext(ctx, "防火墙策略创建完成", "policyName", policyName)

		if err := notifyTaskCompletion(asynqClient, callbackQueue, payload.TaskID, "completed", result, ""); err != nil {
			logger.InfoContext(ctx, "防火墙策略任务回调失败", "error", err)
			return err
		}

		return nil
	}
}