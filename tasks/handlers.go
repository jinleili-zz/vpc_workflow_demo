package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/jinleili-zz/nsp-platform/logger"
	"github.com/jinleili-zz/nsp-platform/taskqueue"

	"workflow_qoder/internal/orchestration"
)

// VPCParams VPC 任务参数结构体
type VPCParams struct {
	VPCName      string `json:"vpc_name"`
	VRFName      string `json:"vrf_name"`
	VLANId       int    `json:"vlan_id"`
	FirewallZone string `json:"firewall_zone"`
	Region       string `json:"region"`
}

// SubnetParams Subnet 任务参数结构体
type SubnetParams struct {
	SubnetName string `json:"subnet_name"`
	VPCName    string `json:"vpc_name"`
	CIDR       string `json:"cidr"`
}

// FirewallPolicyParams 防火墙策略任务参数结构体
type FirewallPolicyParams struct {
	PolicyName string `json:"policy_name"`
	SourceZone string `json:"source_zone"`
	DestZone   string `json:"dest_zone"`
	SourceIP   string `json:"source_ip"`
	DestIP     string `json:"dest_ip"`
	DestPort   string `json:"dest_port"`
	Protocol   string `json:"protocol"`
	Action     string `json:"action"`
}

// LBParams 负载均衡任务参数结构体
type LBParams struct {
	PoolName     string `json:"pool_name"`
	ListenerName string `json:"listener_name"`
}

func CreateVRFOnSwitchHandler(broker taskqueue.Broker) taskqueue.HandlerFunc {
	return func(ctx context.Context, task *taskqueue.Task) error {
		var params VPCParams
		if err := json.Unmarshal(task.Payload, &params); err != nil {
			return publishFailureReply(ctx, broker, task, fmt.Errorf("解析任务参数失败: %w", err))
		}

		logger.InfoContext(ctx, "开始创建VRF", "vrfName", params.VRFName, "vpcName", params.VPCName, "taskType", task.Type)

		time.Sleep(2 * time.Second)

		result := map[string]any{
			"message":   fmt.Sprintf("交换机上成功创建VRF: %s, 配置命令: ip vrf %s", params.VRFName, params.VRFName),
			"vrf_name":  params.VRFName,
			"timestamp": time.Now().Unix(),
		}

		logger.InfoContext(ctx, "VRF创建完成", "vrfName", params.VRFName)
		return publishSuccessReply(ctx, broker, task, result)
	}
}

func CreateVLANSubInterfaceHandler(broker taskqueue.Broker) taskqueue.HandlerFunc {
	return func(ctx context.Context, task *taskqueue.Task) error {
		var params VPCParams
		if err := json.Unmarshal(task.Payload, &params); err != nil {
			return publishFailureReply(ctx, broker, task, fmt.Errorf("解析任务参数失败: %w", err))
		}

		logger.InfoContext(ctx, "开始创建VLAN子接口", "vlanID", params.VLANId, "vpcName", params.VPCName, "taskType", task.Type)

		time.Sleep(2 * time.Second)

		result := map[string]any{
			"message":   fmt.Sprintf("交换机上成功创建VLAN子接口: VLAN %d, 接口配置: interface Vlan%d, ip vrf forwarding %s", params.VLANId, params.VLANId, params.VRFName),
			"vlan_id":   params.VLANId,
			"vrf_name":  params.VRFName,
			"timestamp": time.Now().Unix(),
		}

		logger.InfoContext(ctx, "VLAN子接口创建完成", "vlanID", params.VLANId)
		return publishSuccessReply(ctx, broker, task, result)
	}
}

func CreateFirewallZoneHandler(broker taskqueue.Broker) taskqueue.HandlerFunc {
	return func(ctx context.Context, task *taskqueue.Task) error {
		var params VPCParams
		if err := json.Unmarshal(task.Payload, &params); err != nil {
			return publishFailureReply(ctx, broker, task, fmt.Errorf("解析任务参数失败: %w", err))
		}

		logger.InfoContext(ctx, "开始创建安全区域", "firewallZone", params.FirewallZone, "vpcName", params.VPCName, "taskType", task.Type)

		time.Sleep(2 * time.Second)

		result := map[string]any{
			"message":       fmt.Sprintf("防火墙上成功创建安全区域: %s, 配置命令: security-zone name %s, set priority 100", params.FirewallZone, params.FirewallZone),
			"firewall_zone": params.FirewallZone,
			"timestamp":     time.Now().Unix(),
		}

		logger.InfoContext(ctx, "防火墙安全区域创建完成", "firewallZone", params.FirewallZone)
		logger.InfoContext(ctx, "VPC所有任务执行完成", "vpcName", params.VPCName)
		return publishSuccessReply(ctx, broker, task, result)
	}
}

func CreateSubnetOnSwitchHandler(broker taskqueue.Broker) taskqueue.HandlerFunc {
	return func(ctx context.Context, task *taskqueue.Task) error {
		var params SubnetParams
		if err := json.Unmarshal(task.Payload, &params); err != nil {
			return publishFailureReply(ctx, broker, task, fmt.Errorf("解析任务参数失败: %w", err))
		}

		logger.InfoContext(ctx, "开始创建子网", "subnetName", params.SubnetName, "cidr", params.CIDR, "vpcName", params.VPCName, "taskType", task.Type)

		time.Sleep(2 * time.Second)

		if params.CIDR == "10.0.99.0/24" {
			return publishFailureReply(ctx, broker, task, fmt.Errorf("CIDR冲突: %s 在VPC %s 中已存在", params.CIDR, params.VPCName))
		}

		result := map[string]any{
			"message":     fmt.Sprintf("交换机上成功创建子网: %s, CIDR: %s", params.SubnetName, params.CIDR),
			"subnet_name": params.SubnetName,
			"cidr":        params.CIDR,
			"timestamp":   time.Now().Unix(),
		}

		logger.InfoContext(ctx, "子网创建完成", "subnetName", params.SubnetName)
		return publishSuccessReply(ctx, broker, task, result)
	}
}

func ConfigureSubnetRoutingHandler(broker taskqueue.Broker) taskqueue.HandlerFunc {
	return func(ctx context.Context, task *taskqueue.Task) error {
		var params SubnetParams
		if err := json.Unmarshal(task.Payload, &params); err != nil {
			return publishFailureReply(ctx, broker, task, fmt.Errorf("解析任务参数失败: %w", err))
		}

		logger.InfoContext(ctx, "开始配置子网路由", "subnetName", params.SubnetName, "taskType", task.Type)

		time.Sleep(2 * time.Second)

		result := map[string]any{
			"message":     fmt.Sprintf("成功配置子网路由: %s", params.SubnetName),
			"subnet_name": params.SubnetName,
			"timestamp":   time.Now().Unix(),
		}

		logger.InfoContext(ctx, "子网路由配置完成", "subnetName", params.SubnetName)
		logger.InfoContext(ctx, "子网所有任务执行完成", "subnetName", params.SubnetName)
		return publishSuccessReply(ctx, broker, task, result)
	}
}

func CreateLBPoolHandler(broker taskqueue.Broker) taskqueue.HandlerFunc {
	return func(ctx context.Context, task *taskqueue.Task) error {
		var params LBParams
		if err := json.Unmarshal(task.Payload, &params); err != nil {
			return publishFailureReply(ctx, broker, task, fmt.Errorf("解析任务参数失败: %w", err))
		}

		poolName := params.PoolName
		if poolName == "" {
			poolName = "default-pool"
		}

		logger.InfoContext(ctx, "开始创建LB Pool", "poolName", poolName, "taskType", task.Type)

		time.Sleep(2 * time.Second)

		result := map[string]any{
			"message":   fmt.Sprintf("负载均衡器上成功创建Pool: %s", poolName),
			"pool_name": poolName,
			"timestamp": time.Now().Unix(),
		}

		logger.InfoContext(ctx, "LB Pool创建完成", "poolName", poolName)
		return publishSuccessReply(ctx, broker, task, result)
	}
}

func ConfigureLBListenerHandler(broker taskqueue.Broker) taskqueue.HandlerFunc {
	return func(ctx context.Context, task *taskqueue.Task) error {
		var params LBParams
		if err := json.Unmarshal(task.Payload, &params); err != nil {
			return publishFailureReply(ctx, broker, task, fmt.Errorf("解析任务参数失败: %w", err))
		}

		listenerName := params.ListenerName
		if listenerName == "" {
			listenerName = "default-listener"
		}

		logger.InfoContext(ctx, "开始配置LB Listener", "listenerName", listenerName, "taskType", task.Type)

		time.Sleep(2 * time.Second)

		result := map[string]any{
			"message":       fmt.Sprintf("负载均衡器上成功配置Listener: %s", listenerName),
			"listener_name": listenerName,
			"timestamp":     time.Now().Unix(),
		}

		logger.InfoContext(ctx, "LB Listener配置完成", "listenerName", listenerName)
		return publishSuccessReply(ctx, broker, task, result)
	}
}

func CreateFirewallPolicyHandler(broker taskqueue.Broker) taskqueue.HandlerFunc {
	return func(ctx context.Context, task *taskqueue.Task) error {
		var params FirewallPolicyParams
		if err := json.Unmarshal(task.Payload, &params); err != nil {
			return publishFailureReply(ctx, broker, task, fmt.Errorf("解析任务参数失败: %w", err))
		}

		logger.InfoContext(ctx, "开始创建防火墙策略", "policyName", params.PolicyName, "taskType", task.Type)
		logger.InfoContext(ctx, "防火墙策略规则", "sourceZone", params.SourceZone, "sourceIP", params.SourceIP, "destZone", params.DestZone, "destIP", params.DestIP, "destPort", params.DestPort, "protocol", params.Protocol)

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
`, params.PolicyName, params.SourceZone, params.DestZone, params.SourceIP, params.DestIP, params.DestPort, params.Protocol, params.Action)

		result := map[string]any{
			"message":     fmt.Sprintf("防火墙策略创建成功: %s", params.PolicyName),
			"policy_name": params.PolicyName,
			"source_zone": params.SourceZone,
			"dest_zone":   params.DestZone,
			"config_cmd":  configCmd,
			"timestamp":   time.Now().Unix(),
		}

		logger.InfoContext(ctx, "防火墙策略创建完成", "policyName", params.PolicyName)
		return publishSuccessReply(ctx, broker, task, result)
	}
}

func publishSuccessReply(ctx context.Context, broker taskqueue.Broker, task *taskqueue.Task, result any) error {
	return publishReply(ctx, broker, task, orchestration.ReplyStatusSuccess, result, "")
}

func publishFailureReply(ctx context.Context, broker taskqueue.Broker, task *taskqueue.Task, cause error) error {
	if err := publishReply(ctx, broker, task, orchestration.ReplyStatusFailed, nil, cause.Error()); err != nil {
		return err
	}
	return cause
}

func publishReply(ctx context.Context, broker taskqueue.Broker, task *taskqueue.Task, status orchestration.ReplyStatus, result any, errMsg string) error {
	if broker == nil {
		return fmt.Errorf("broker is nil")
	}
	if task == nil {
		return fmt.Errorf("task is nil")
	}
	if task.Reply == nil || task.Reply.Queue == "" {
		return fmt.Errorf("reply queue is required")
	}

	replyPayload, err := buildReplyPayload(task, status, result, errMsg)
	if err != nil {
		return err
	}

	payload, err := json.Marshal(replyPayload)
	if err != nil {
		return fmt.Errorf("序列化reply payload失败: %w", err)
	}

	metadata := make(map[string]string, len(task.Metadata))
	for k, v := range task.Metadata {
		metadata[k] = v
	}

	if _, err := broker.Publish(ctx, &taskqueue.Task{
		Type:     orchestration.ReplyTaskType,
		Payload:  payload,
		Queue:    task.Reply.Queue,
		Metadata: metadata,
	}); err != nil {
		return fmt.Errorf("发布reply任务失败: %w", err)
	}

	return nil
}

func buildReplyPayload(task *taskqueue.Task, status orchestration.ReplyStatus, result any, errMsg string) (*orchestration.ReplyPayload, error) {
	stepIndex, err := metadataInt(task.Metadata, orchestration.MetadataKeyStepIndex)
	if err != nil {
		return nil, err
	}
	totalSteps, err := metadataInt(task.Metadata, orchestration.MetadataKeyTotalSteps)
	if err != nil {
		return nil, err
	}

	reply := &orchestration.ReplyPayload{
		TaskType:     task.Type,
		ResourceID:   task.Metadata[orchestration.MetadataKeyResourceID],
		ResourceType: task.Metadata[orchestration.MetadataKeyResourceType],
		StepIndex:    stepIndex,
		TotalSteps:   totalSteps,
		Status:       status,
		Error:        errMsg,
	}

	if reply.ResourceID == "" || reply.ResourceType == "" {
		return nil, fmt.Errorf("task metadata缺少resource上下文")
	}

	if result != nil {
		resultBytes, err := json.Marshal(result)
		if err != nil {
			return nil, fmt.Errorf("序列化reply result失败: %w", err)
		}
		reply.Result = resultBytes
	}

	return reply, nil
}

func metadataInt(metadata map[string]string, key string) (int, error) {
	value := metadata[key]
	if value == "" {
		return 0, fmt.Errorf("task metadata缺少%s", key)
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("task metadata %s 非法: %w", key, err)
	}
	return parsed, nil
}
