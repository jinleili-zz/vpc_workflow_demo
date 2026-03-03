package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/yourorg/nsp-common/pkg/logger"
	"github.com/yourorg/nsp-common/pkg/taskqueue"
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

func CreateVRFOnSwitchHandler(cbSender *taskqueue.CallbackSender) taskqueue.HandlerFunc {
	return func(ctx context.Context, tp *taskqueue.TaskPayload) (*taskqueue.TaskResult, error) {
		var params VPCParams
		if err := json.Unmarshal(tp.Params, &params); err != nil {
			return nil, fmt.Errorf("解析任务参数失败: %v", err)
		}

		logger.InfoContext(ctx, "开始创建VRF", "vrfName", params.VRFName, "vpcName", params.VPCName, "taskID", tp.TaskID)

		time.Sleep(2 * time.Second)

		result := map[string]interface{}{
			"message":   fmt.Sprintf("交换机上成功创建VRF: %s, 配置命令: ip vrf %s", params.VRFName, params.VRFName),
			"vrf_name":  params.VRFName,
			"timestamp": time.Now().Unix(),
		}

		logger.InfoContext(ctx, "VRF创建完成", "vrfName", params.VRFName)

		if err := cbSender.Success(ctx, tp.TaskID, result); err != nil {
			logger.InfoContext(ctx, "VRF任务回调失败", "error", err)
			return nil, err
		}

		return &taskqueue.TaskResult{Data: result, Message: "VRF created"}, nil
	}
}

func CreateVLANSubInterfaceHandler(cbSender *taskqueue.CallbackSender) taskqueue.HandlerFunc {
	return func(ctx context.Context, tp *taskqueue.TaskPayload) (*taskqueue.TaskResult, error) {
		var params VPCParams
		if err := json.Unmarshal(tp.Params, &params); err != nil {
			return nil, fmt.Errorf("解析任务参数失败: %v", err)
		}

		logger.InfoContext(ctx, "开始创建VLAN子接口", "vlanID", params.VLANId, "vpcName", params.VPCName, "taskID", tp.TaskID)

		time.Sleep(2 * time.Second)

		result := map[string]interface{}{
			"message":   fmt.Sprintf("交换机上成功创建VLAN子接口: VLAN %d, 接口配置: interface Vlan%d, ip vrf forwarding %s", params.VLANId, params.VLANId, params.VRFName),
			"vlan_id":   params.VLANId,
			"vrf_name":  params.VRFName,
			"timestamp": time.Now().Unix(),
		}

		logger.InfoContext(ctx, "VLAN子接口创建完成", "vlanID", params.VLANId)

		if err := cbSender.Success(ctx, tp.TaskID, result); err != nil {
			logger.InfoContext(ctx, "VLAN任务回调失败", "error", err)
			return nil, err
		}

		return &taskqueue.TaskResult{Data: result, Message: "VLAN sub-interface created"}, nil
	}
}

func CreateFirewallZoneHandler(cbSender *taskqueue.CallbackSender) taskqueue.HandlerFunc {
	return func(ctx context.Context, tp *taskqueue.TaskPayload) (*taskqueue.TaskResult, error) {
		var params VPCParams
		if err := json.Unmarshal(tp.Params, &params); err != nil {
			return nil, fmt.Errorf("解析任务参数失败: %v", err)
		}

		logger.InfoContext(ctx, "开始创建安全区域", "firewallZone", params.FirewallZone, "vpcName", params.VPCName, "taskID", tp.TaskID)

		time.Sleep(2 * time.Second)

		result := map[string]interface{}{
			"message":       fmt.Sprintf("防火墙上成功创建安全区域: %s, 配置命令: security-zone name %s, set priority 100", params.FirewallZone, params.FirewallZone),
			"firewall_zone": params.FirewallZone,
			"timestamp":     time.Now().Unix(),
		}

		logger.InfoContext(ctx, "防火墙安全区域创建完成", "firewallZone", params.FirewallZone)
		logger.InfoContext(ctx, "VPC所有任务执行完成", "vpcName", params.VPCName)

		if err := cbSender.Success(ctx, tp.TaskID, result); err != nil {
			logger.InfoContext(ctx, "防火墙任务回调失败", "error", err)
			return nil, err
		}

		return &taskqueue.TaskResult{Data: result, Message: "Firewall zone created"}, nil
	}
}

func CreateSubnetOnSwitchHandler(cbSender *taskqueue.CallbackSender) taskqueue.HandlerFunc {
	return func(ctx context.Context, tp *taskqueue.TaskPayload) (*taskqueue.TaskResult, error) {
		var params SubnetParams
		if err := json.Unmarshal(tp.Params, &params); err != nil {
			return nil, fmt.Errorf("解析任务参数失败: %v", err)
		}

		logger.InfoContext(ctx, "开始创建子网", "subnetName", params.SubnetName, "cidr", params.CIDR, "vpcName", params.VPCName, "taskID", tp.TaskID)

		time.Sleep(2 * time.Second)

		if params.CIDR == "10.0.99.0/24" {
			errorMsg := fmt.Sprintf("CIDR冲突: %s 在VPC %s 中已存在", params.CIDR, params.VPCName)
			logger.InfoContext(ctx, "子网创建失败", "subnetName", params.SubnetName, "error", errorMsg)

			if err := cbSender.Fail(ctx, tp.TaskID, errorMsg); err != nil {
				logger.InfoContext(ctx, "子网任务回调失败", "error", err)
				return nil, err
			}
			return nil, fmt.Errorf("%s", errorMsg)
		}

		result := map[string]interface{}{
			"message":     fmt.Sprintf("交换机上成功创建子网: %s, CIDR: %s", params.SubnetName, params.CIDR),
			"subnet_name": params.SubnetName,
			"cidr":        params.CIDR,
			"timestamp":   time.Now().Unix(),
		}

		logger.InfoContext(ctx, "子网创建完成", "subnetName", params.SubnetName)

		if err := cbSender.Success(ctx, tp.TaskID, result); err != nil {
			logger.InfoContext(ctx, "子网任务回调失败", "error", err)
			return nil, err
		}

		return &taskqueue.TaskResult{Data: result, Message: "Subnet created"}, nil
	}
}

func ConfigureSubnetRoutingHandler(cbSender *taskqueue.CallbackSender) taskqueue.HandlerFunc {
	return func(ctx context.Context, tp *taskqueue.TaskPayload) (*taskqueue.TaskResult, error) {
		var params SubnetParams
		if err := json.Unmarshal(tp.Params, &params); err != nil {
			return nil, fmt.Errorf("解析任务参数失败: %v", err)
		}

		logger.InfoContext(ctx, "开始配置子网路由", "subnetName", params.SubnetName, "taskID", tp.TaskID)

		time.Sleep(2 * time.Second)

		result := map[string]interface{}{
			"message":     fmt.Sprintf("成功配置子网路由: %s", params.SubnetName),
			"subnet_name": params.SubnetName,
			"timestamp":   time.Now().Unix(),
		}

		logger.InfoContext(ctx, "子网路由配置完成", "subnetName", params.SubnetName)
		logger.InfoContext(ctx, "子网所有任务执行完成", "subnetName", params.SubnetName)

		if err := cbSender.Success(ctx, tp.TaskID, result); err != nil {
			logger.InfoContext(ctx, "路由任务回调失败", "error", err)
			return nil, err
		}

		return &taskqueue.TaskResult{Data: result, Message: "Subnet routing configured"}, nil
	}
}

func CreateLBPoolHandler(cbSender *taskqueue.CallbackSender) taskqueue.HandlerFunc {
	return func(ctx context.Context, tp *taskqueue.TaskPayload) (*taskqueue.TaskResult, error) {
		var params LBParams
		if err := json.Unmarshal(tp.Params, &params); err != nil {
			return nil, fmt.Errorf("解析任务参数失败: %v", err)
		}

		poolName := params.PoolName
		if poolName == "" {
			poolName = "default-pool"
		}

		logger.InfoContext(ctx, "开始创建LB Pool", "poolName", poolName, "taskID", tp.TaskID)

		time.Sleep(2 * time.Second)

		result := map[string]interface{}{
			"message":   fmt.Sprintf("负载均衡器上成功创建Pool: %s", poolName),
			"pool_name": poolName,
			"timestamp": time.Now().Unix(),
		}

		logger.InfoContext(ctx, "LB Pool创建完成", "poolName", poolName)

		if err := cbSender.Success(ctx, tp.TaskID, result); err != nil {
			logger.InfoContext(ctx, "负载均衡任务回调失败", "error", err)
			return nil, err
		}

		return &taskqueue.TaskResult{Data: result, Message: "LB pool created"}, nil
	}
}

func ConfigureLBListenerHandler(cbSender *taskqueue.CallbackSender) taskqueue.HandlerFunc {
	return func(ctx context.Context, tp *taskqueue.TaskPayload) (*taskqueue.TaskResult, error) {
		var params LBParams
		if err := json.Unmarshal(tp.Params, &params); err != nil {
			return nil, fmt.Errorf("解析任务参数失败: %v", err)
		}

		listenerName := params.ListenerName
		if listenerName == "" {
			listenerName = "default-listener"
		}

		logger.InfoContext(ctx, "开始配置LB Listener", "listenerName", listenerName, "taskID", tp.TaskID)

		time.Sleep(2 * time.Second)

		result := map[string]interface{}{
			"message":       fmt.Sprintf("负载均衡器上成功配置Listener: %s", listenerName),
			"listener_name": listenerName,
			"timestamp":     time.Now().Unix(),
		}

		logger.InfoContext(ctx, "LB Listener配置完成", "listenerName", listenerName)

		if err := cbSender.Success(ctx, tp.TaskID, result); err != nil {
			logger.InfoContext(ctx, "负载均衡任务回调失败", "error", err)
			return nil, err
		}

		return &taskqueue.TaskResult{Data: result, Message: "LB listener configured"}, nil
	}
}

func CreateFirewallPolicyHandler(cbSender *taskqueue.CallbackSender) taskqueue.HandlerFunc {
	return func(ctx context.Context, tp *taskqueue.TaskPayload) (*taskqueue.TaskResult, error) {
		var params FirewallPolicyParams
		if err := json.Unmarshal(tp.Params, &params); err != nil {
			return nil, fmt.Errorf("解析任务参数失败: %v", err)
		}

		logger.InfoContext(ctx, "开始创建防火墙策略", "policyName", params.PolicyName, "taskID", tp.TaskID)
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

		result := map[string]interface{}{
			"message":     fmt.Sprintf("防火墙策略创建成功: %s", params.PolicyName),
			"policy_name": params.PolicyName,
			"source_zone": params.SourceZone,
			"dest_zone":   params.DestZone,
			"config_cmd":  configCmd,
			"timestamp":   time.Now().Unix(),
		}

		logger.InfoContext(ctx, "防火墙策略创建完成", "policyName", params.PolicyName)

		if err := cbSender.Success(ctx, tp.TaskID, result); err != nil {
			logger.InfoContext(ctx, "防火墙策略任务回调失败", "error", err)
			return nil, err
		}

		return &taskqueue.TaskResult{Data: result, Message: "Firewall policy created"}, nil
	}
}
