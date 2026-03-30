package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jinleili-zz/nsp-platform/logger"
	"github.com/jinleili-zz/nsp-platform/taskqueue"
)

// PCCNParams PCCN任务参数结构体
type PCCNParams struct {
	PCCNID        string   `json:"pccn_id"`
	PCCNName      string   `json:"pccn_name"`
	VPCName       string   `json:"vpc_name"`
	VPCRegion     string   `json:"vpc_region"`
	PeerVPCName   string   `json:"peer_vpc_name"`
	PeerVPCRegion string   `json:"peer_vpc_region"`
	AZ            string   `json:"az"`
	Subnets       []string `json:"subnets"`
}

// CreatePCCNConnectionHandler 创建PCCN连接的Worker Handler
// 该Handler打印两个VPC的子网信息
func CreatePCCNConnectionHandler(broker taskqueue.Broker) taskqueue.HandlerFunc {
	return func(ctx context.Context, task *taskqueue.Task) error {
		var params PCCNParams
		if err := json.Unmarshal(task.Payload, &params); err != nil {
			return publishFailureReply(ctx, broker, task, fmt.Errorf("解析任务参数失败: %w", err))
		}

		logger.InfoContext(ctx, "开始创建PCCN连接",
			"pccn_name", params.PCCNName,
			"vpc_name", params.VPCName,
			"vpc_region", params.VPCRegion,
			"peer_vpc_name", params.PeerVPCName,
			"peer_vpc_region", params.PeerVPCRegion,
			"az", params.AZ,
			"taskType", task.Type,
		)

		time.Sleep(2 * time.Second)

		logger.InfoContext(ctx, "本地VPC子网信息",
			"vpc_name", params.VPCName,
			"vpc_region", params.VPCRegion,
			"subnets", params.Subnets,
		)

		logger.InfoContext(ctx, "对端VPC信息（用于路由配置）",
			"peer_vpc_name", params.PeerVPCName,
			"peer_vpc_region", params.PeerVPCRegion,
			"is_cross_region", params.VPCRegion != params.PeerVPCRegion,
		)

		configCmd := fmt.Sprintf("pccn connection create --local-vpc %s --peer-vpc %s --cross-region %v",
			params.VPCName, params.PeerVPCName, params.VPCRegion != params.PeerVPCRegion)

		result := map[string]any{
			"message":         fmt.Sprintf("PCCN连接创建成功: %s(%s) <-> %s(%s)", params.VPCName, params.VPCRegion, params.PeerVPCName, params.PeerVPCRegion),
			"pccn_id":         params.PCCNID,
			"pccn_name":       params.PCCNName,
			"vpc_name":        params.VPCName,
			"vpc_region":      params.VPCRegion,
			"peer_vpc_name":   params.PeerVPCName,
			"peer_vpc_region": params.PeerVPCRegion,
			"vpc_subnets":     params.Subnets,
			"is_cross_region": params.VPCRegion != params.PeerVPCRegion,
			"config_cmd":      configCmd,
			"timestamp":       time.Now().Unix(),
		}

		logger.InfoContext(ctx, "PCCN连接创建完成", "pccn_name", params.PCCNName)
		return publishSuccessReply(ctx, broker, task, result)
	}
}

// ConfigurePCCNRoutingHandler 配置PCCN路由的Worker Handler
func ConfigurePCCNRoutingHandler(broker taskqueue.Broker) taskqueue.HandlerFunc {
	return func(ctx context.Context, task *taskqueue.Task) error {
		var params PCCNParams
		if err := json.Unmarshal(task.Payload, &params); err != nil {
			return publishFailureReply(ctx, broker, task, fmt.Errorf("解析任务参数失败: %w", err))
		}

		logger.InfoContext(ctx, "开始配置PCCN路由",
			"pccn_name", params.PCCNName,
			"vpc_name", params.VPCName,
			"vpc_region", params.VPCRegion,
			"peer_vpc_region", params.PeerVPCRegion,
			"az", params.AZ,
			"taskType", task.Type,
		)

		time.Sleep(2 * time.Second)

		isCrossRegion := params.VPCRegion != params.PeerVPCRegion
		routingType := "intra-region"
		if isCrossRegion {
			routingType = "cross-region"
		}

		logger.InfoContext(ctx, "配置路由规则",
			"vpc_name", params.VPCName,
			"vpc_region", params.VPCRegion,
			"peer_vpc_name", params.PeerVPCName,
			"peer_vpc_region", params.PeerVPCRegion,
			"routing_type", routingType,
			"config_cmd", "ip route add <peer_cidr> via <pccn_gateway>",
		)

		var routingCmds []string
		for _, subnet := range params.Subnets {
			routingCmds = append(routingCmds, fmt.Sprintf("ip route add %s via pccn-gateway-%s", subnet, params.PCCNName))
		}

		result := map[string]any{
			"message":         fmt.Sprintf("PCCN路由配置成功: %s", params.PCCNName),
			"pccn_name":       params.PCCNName,
			"vpc_name":        params.VPCName,
			"vpc_region":      params.VPCRegion,
			"peer_vpc_name":   params.PeerVPCName,
			"peer_vpc_region": params.PeerVPCRegion,
			"routing_type":    routingType,
			"routing_cmds":    routingCmds,
			"timestamp":       time.Now().Unix(),
		}

		logger.InfoContext(ctx, "PCCN路由配置完成", "pccn_name", params.PCCNName, "routing_type", routingType)
		logger.InfoContext(ctx, "PCCN所有任务执行完成", "pccn_name", params.PCCNName)
		return publishSuccessReply(ctx, broker, task, result)
	}
}

// DeletePCCNConnectionHandler 删除PCCN连接的Worker Handler
func DeletePCCNConnectionHandler(broker taskqueue.Broker) taskqueue.HandlerFunc {
	return func(ctx context.Context, task *taskqueue.Task) error {
		var params PCCNParams
		if err := json.Unmarshal(task.Payload, &params); err != nil {
			return publishFailureReply(ctx, broker, task, fmt.Errorf("解析任务参数失败: %w", err))
		}

		logger.InfoContext(ctx, "开始删除PCCN连接",
			"pccn_name", params.PCCNName,
			"vpc_name", params.VPCName,
			"taskType", task.Type,
		)

		time.Sleep(1 * time.Second)

		result := map[string]any{
			"message":   fmt.Sprintf("PCCN连接删除成功: %s", params.PCCNName),
			"pccn_name": params.PCCNName,
			"timestamp": time.Now().Unix(),
		}

		logger.InfoContext(ctx, "PCCN连接删除完成", "pccn_name", params.PCCNName)
		return publishSuccessReply(ctx, broker, task, result)
	}
}
