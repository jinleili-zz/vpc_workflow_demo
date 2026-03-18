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
func CreatePCCNConnectionHandler(cbSender *taskqueue.CallbackSender) taskqueue.HandlerFunc {
	return func(ctx context.Context, tp *taskqueue.TaskPayload) (*taskqueue.TaskResult, error) {
		var params PCCNParams
		if err := json.Unmarshal(tp.Params, &params); err != nil {
			return nil, fmt.Errorf("解析任务参数失败: %v", err)
		}

		logger.InfoContext(ctx, "开始创建PCCN连接",
			"pccn_name", params.PCCNName,
			"vpc_name", params.VPCName,
			"vpc_region", params.VPCRegion,
			"peer_vpc_name", params.PeerVPCName,
			"peer_vpc_region", params.PeerVPCRegion,
			"az", params.AZ,
			"taskID", tp.TaskID,
		)

		// 模拟处理延迟
		time.Sleep(2 * time.Second)

		// 打印本地VPC的子网信息
		logger.InfoContext(ctx, "本地VPC子网信息",
			"vpc_name", params.VPCName,
			"vpc_region", params.VPCRegion,
			"subnets", params.Subnets,
		)

		// 打印对端VPC信息
		logger.InfoContext(ctx, "对端VPC信息（用于路由配置）",
			"peer_vpc_name", params.PeerVPCName,
			"peer_vpc_region", params.PeerVPCRegion,
			"is_cross_region", params.VPCRegion != params.PeerVPCRegion,
		)

		// 构建配置命令（模拟）
		configCmd := fmt.Sprintf("pccn connection create --local-vpc %s --peer-vpc %s --cross-region %v",
			params.VPCName, params.PeerVPCName, params.VPCRegion != params.PeerVPCRegion)

		result := map[string]interface{}{
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

		if err := cbSender.Success(ctx, tp.TaskID, result); err != nil {
			logger.InfoContext(ctx, "PCCN任务回调失败", "error", err)
			return nil, err
		}

		return &taskqueue.TaskResult{Data: result, Message: "PCCN connection created"}, nil
	}
}

// ConfigurePCCNRoutingHandler 配置PCCN路由的Worker Handler
func ConfigurePCCNRoutingHandler(cbSender *taskqueue.CallbackSender) taskqueue.HandlerFunc {
	return func(ctx context.Context, tp *taskqueue.TaskPayload) (*taskqueue.TaskResult, error) {
		var params PCCNParams
		if err := json.Unmarshal(tp.Params, &params); err != nil {
			return nil, fmt.Errorf("解析任务参数失败: %v", err)
		}

		logger.InfoContext(ctx, "开始配置PCCN路由",
			"pccn_name", params.PCCNName,
			"vpc_name", params.VPCName,
			"vpc_region", params.VPCRegion,
			"peer_vpc_region", params.PeerVPCRegion,
			"az", params.AZ,
			"taskID", tp.TaskID,
		)

		time.Sleep(2 * time.Second)

		// 跨Region路由需要特殊处理
		isCrossRegion := params.VPCRegion != params.PeerVPCRegion
		routingType := "intra-region"
		if isCrossRegion {
			routingType = "cross-region"
		}

		// 模拟配置路由
		logger.InfoContext(ctx, "配置路由规则",
			"vpc_name", params.VPCName,
			"vpc_region", params.VPCRegion,
			"peer_vpc_name", params.PeerVPCName,
			"peer_vpc_region", params.PeerVPCRegion,
			"routing_type", routingType,
			"config_cmd", "ip route add <peer_cidr> via <pccn_gateway>",
		)

		// 构建路由配置命令（模拟）
		var routingCmds []string
		for _, subnet := range params.Subnets {
			cmd := fmt.Sprintf("ip route add %s via pccn-gateway-%s", subnet, params.PCCNName)
			routingCmds = append(routingCmds, cmd)
		}

		result := map[string]interface{}{
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

		if err := cbSender.Success(ctx, tp.TaskID, result); err != nil {
			logger.InfoContext(ctx, "PCCN路由任务回调失败", "error", err)
			return nil, err
		}

		return &taskqueue.TaskResult{Data: result, Message: "PCCN routing configured"}, nil
	}
}

// DeletePCCNConnectionHandler 删除PCCN连接的Worker Handler
func DeletePCCNConnectionHandler(cbSender *taskqueue.CallbackSender) taskqueue.HandlerFunc {
	return func(ctx context.Context, tp *taskqueue.TaskPayload) (*taskqueue.TaskResult, error) {
		var params PCCNParams
		if err := json.Unmarshal(tp.Params, &params); err != nil {
			return nil, fmt.Errorf("解析任务参数失败: %v", err)
		}

		logger.InfoContext(ctx, "开始删除PCCN连接",
			"pccn_name", params.PCCNName,
			"vpc_name", params.VPCName,
			"taskID", tp.TaskID,
		)

		time.Sleep(1 * time.Second)

		result := map[string]interface{}{
			"message":   fmt.Sprintf("PCCN连接删除成功: %s", params.PCCNName),
			"pccn_name": params.PCCNName,
			"timestamp": time.Now().Unix(),
		}

		logger.InfoContext(ctx, "PCCN连接删除完成", "pccn_name", params.PCCNName)

		if err := cbSender.Success(ctx, tp.TaskID, result); err != nil {
			logger.InfoContext(ctx, "PCCN删除任务回调失败", "error", err)
			return nil, err
		}

		return &taskqueue.TaskResult{Data: result, Message: "PCCN connection deleted"}, nil
	}
}
