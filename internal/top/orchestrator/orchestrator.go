package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"workflow_qoder/internal/client"
	"workflow_qoder/internal/models"
	"workflow_qoder/internal/top/registry"
	topdao "workflow_qoder/internal/top/vpc/dao"

	"github.com/google/uuid"
	"github.com/yourorg/nsp-common/pkg/logger"
	"github.com/yourorg/nsp-common/pkg/saga"
	"github.com/yourorg/nsp-common/pkg/trace"
)

type Orchestrator struct {
	registry   *registry.Registry
	azClient   *client.AZNSPClient // 保留，用于健康检查
	topDAO     *topdao.TopVPCDAO
	sagaEngine *saga.Engine        // 新增
	tracedHTTP *trace.TracedClient // 新增
}

func NewOrchestrator(registry *registry.Registry, topDB *sql.DB, sagaEngine *saga.Engine, tracedHTTP *trace.TracedClient) *Orchestrator {
	var dao *topdao.TopVPCDAO
	if topDB != nil {
		dao = topdao.NewTopVPCDAO(topDB)
	}
	
	// Create AZ client with trace support
	var azClient *client.AZNSPClient
	if tracedHTTP != nil {
		azClient = client.NewAZNSPClientWithTrace(tracedHTTP)
	} else {
		azClient = client.NewAZNSPClient()
	}
	
	return &Orchestrator{
		registry:   registry,
		azClient:   azClient,
		topDAO:     dao,
		sagaEngine: sagaEngine,
		tracedHTTP: tracedHTTP,
	}
}

// CreateRegionVPC 创建Region级VPC（使用SAGA模式实现分布式事务）
func (o *Orchestrator) CreateRegionVPC(ctx context.Context, req *models.VPCRequest) (*models.VPCResponse, error) {
	logger.InfoContext(ctx, "开始创建Region级VPC", "vpc_name", req.VPCName, "region", req.Region)

	// 1. 获取Region下的所有AZ
	azs, err := o.registry.GetRegionAZs(ctx, req.Region)
	if err != nil {
		return &models.VPCResponse{Success: false, Message: fmt.Sprintf("获取Region的AZ失败: %v", err)}, nil
	}

	// 2. 预检查阶段
	for _, az := range azs {
		if err := o.azClient.HealthCheck(ctx, az.NSPAddr); err != nil {
			return &models.VPCResponse{Success: false, Message: fmt.Sprintf("预检查失败: AZ %s 不健康", az.ID)}, nil
		}
	}

	// 3. 构建 SAGA 事务定义
	builder := saga.NewSaga(fmt.Sprintf("region-vpc-create-%s", req.VPCName)).
		WithPayload(map[string]any{"vpc_name": req.VPCName, "region": req.Region}).
		WithTimeout(300) // 5 分钟超时

	// 为每个 AZ 添加一个步骤（串行执行）
	for _, az := range azs {
		payloadBytes, _ := json.Marshal(req)
		var payloadMap map[string]any
		json.Unmarshal(payloadBytes, &payloadMap)
		builder.AddStep(saga.Step{
			Name:             fmt.Sprintf("创建VPC-%s", az.ID),
			Type:             saga.StepTypeSync,
			ActionMethod:     "POST",
			ActionURL:        fmt.Sprintf("%s/api/v1/vpc", az.NSPAddr),
			ActionPayload:    payloadMap,
			CompensateMethod: "DELETE",
			CompensateURL:    fmt.Sprintf("%s/api/v1/vpc/%s", az.NSPAddr, req.VPCName),
		})
	}

	def, err := builder.Build()
	if err != nil {
		return &models.VPCResponse{Success: false, Message: fmt.Sprintf("构建SAGA定义失败: %v", err)}, nil
	}

	// 4. 提交 SAGA 事务
	txID, err := o.sagaEngine.Submit(ctx, def)
	if err != nil {
		return &models.VPCResponse{Success: false, Message: fmt.Sprintf("提交SAGA事务失败: %v", err)}, nil
	}

	logger.InfoContext(ctx, "SAGA事务已提交", "transaction_id", txID, "vpc_name", req.VPCName)

	// 5. 预注册 VPC 到 vpc_registry（供后续子网创建查询 firewall_zone）
	if o.topDAO != nil {
		for _, az := range azs {
			vpcReg := &models.VPCRegistry{
				ID:           uuid.New().String(),
				VPCName:      req.VPCName,
				Region:       req.Region,
				AZ:           az.ID,
				AZVpcID:      "", // SAGA 完成后由回调更新
				VRFName:      req.VRFName,
				VLANId:       req.VLANId,
				FirewallZone: req.FirewallZone,
				Status:       "creating",
			}
			if err := o.topDAO.RegisterVPC(ctx, vpcReg); err != nil {
				logger.InfoContext(ctx, "预注册VPC到Top层失败", "az", az.ID, "error", err)
			}
		}
	}

	return &models.VPCResponse{
		Success:    true,
		Message:    fmt.Sprintf("VPC创建任务已提交，事务ID: %s", txID),
		WorkflowID: txID,
	}, nil
}

// QuerySagaTransaction 查询SAGA事务状态
func (o *Orchestrator) QuerySagaTransaction(ctx context.Context, txID string) (*saga.TransactionStatus, error) {
	return o.sagaEngine.Query(ctx, txID)
}

// CreateAZSubnet 创建AZ级子网（路由到指定AZ）
func (o *Orchestrator) CreateAZSubnet(ctx context.Context, req *models.SubnetRequest) (*models.SubnetResponse, error) {
	logger.InfoContext(ctx, "开始创建AZ级子网", "subnet_name", req.SubnetName, "region", req.Region, "az", req.AZ)

	az, err := o.registry.GetAZ(ctx, req.Region, req.AZ)
	if err != nil {
		return &models.SubnetResponse{
			Success: false,
			Message: fmt.Sprintf("获取AZ信息失败: %v", err),
		}, nil
	}

	healthy, err := o.registry.CheckAZHealth(ctx, req.Region, req.AZ)
	if err != nil || !healthy {
		return &models.SubnetResponse{
			Success: false,
			Message: fmt.Sprintf("AZ %s 不可用", req.AZ),
		}, nil
	}

	logger.InfoContext(ctx, "向AZ发送子网创建请求", "az_id", az.ID)
	resp, err := o.azClient.CreateSubnet(ctx, az.NSPAddr, req)
	if err != nil {
		return &models.SubnetResponse{
			Success: false,
			Message: fmt.Sprintf("创建子网失败: %v", err),
		}, nil
	}

	if resp.Success && o.topDAO != nil {
		var firewallZone string
		vpc, err := o.topDAO.GetVPCByNameAndAZ(ctx, req.VPCName, req.AZ)
		if err == nil && vpc != nil {
			firewallZone = vpc.FirewallZone
		}

		subnetReg := &models.SubnetRegistry{
			ID:           uuid.New().String(),
			SubnetName:   req.SubnetName,
			VPCName:      req.VPCName,
			Region:       req.Region,
			AZ:           req.AZ,
			AZSubnetID:   resp.SubnetID,
			CIDR:         req.CIDR,
			FirewallZone: firewallZone,
			Status:       "running",
		}
		if err := o.topDAO.RegisterSubnet(ctx, subnetReg); err != nil {
			logger.InfoContext(ctx, "同步子网拓扑到Top层失败", "error", err)
		} else {
			logger.InfoContext(ctx, "同步子网拓扑成功", "subnet_name", req.SubnetName, "az", req.AZ, "cidr", req.CIDR, "zone", firewallZone)
		}
	}

	return resp, nil
}

func (o *Orchestrator) CheckZonePolicies(ctx context.Context, zone string) (int, error) {
	vfwAddr := os.Getenv("TOP_NSP_VFW_ADDR")
	if vfwAddr == "" {
		vfwAddr = "http://top-nsp-vfw:8082"
	}
	url := fmt.Sprintf("%s/api/v1/firewall/zone/%s/policy-count", vfwAddr, zone)
	resp, err := http.Get(url)
	if err != nil {
		logger.InfoContext(ctx, "查询Zone策略数量失败", "zone", zone, "error", err)
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		logger.InfoContext(ctx, "查询Zone策略数量返回非200", "zone", zone, "status", resp.StatusCode)
		return 0, fmt.Errorf("查询Zone策略数量返回状态码: %d", resp.StatusCode)
	}

	var result struct {
		Count int `json:"count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		logger.InfoContext(ctx, "解析Zone策略数量响应失败", "zone", zone, "error", err)
		return 0, fmt.Errorf("解析响应失败: %v", err)
	}

	logger.InfoContext(ctx, "查询Zone策略数量成功", "zone", zone, "count", result.Count)
	return result.Count, nil
}
