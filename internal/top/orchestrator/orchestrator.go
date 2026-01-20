package orchestrator

import (
	"context"
	"fmt"
	"log"

	"workflow_qoder/internal/client"
	"workflow_qoder/internal/models"
	"workflow_qoder/internal/top/registry"

	"github.com/dtm-labs/client/dtmcli"
	"github.com/google/uuid"
)

// Orchestrator 服务编排器
type Orchestrator struct {
	registry      *registry.Registry
	azClient      *client.AZNSPClient
	dtmServerAddr string
}

// NewOrchestrator 创建编排器
func NewOrchestrator(registry *registry.Registry, dtmServerAddr string) *Orchestrator {
	return &Orchestrator{
		registry:      registry,
		azClient:      client.NewAZNSPClient(),
		dtmServerAddr: dtmServerAddr,
	}
}

// CreateRegionVPC 创建Region级VPC（使用DTM Saga分布式事务）
func (o *Orchestrator) CreateRegionVPC(ctx context.Context, req *models.VPCRequest) (*models.VPCResponse, error) {
	log.Printf("[Orchestrator] 开始创建Region级VPC: %s (Region: %s)", req.VPCName, req.Region)

	// 1. 获取Region下的所有AZ
	azs, err := o.registry.GetRegionAZs(ctx, req.Region)
	if err != nil {
		return &models.VPCResponse{
			Success: false,
			Message: fmt.Sprintf("获取Region的AZ失败: %v", err),
		}, nil
	}

	if len(azs) == 0 {
		return &models.VPCResponse{
			Success: false,
			Message: fmt.Sprintf("Region %s 没有可用的AZ", req.Region),
		}, nil
	}

	log.Printf("[Orchestrator] 找到 %d 个AZ: %v", len(azs), func() []string {
		ids := make([]string, len(azs))
		for i, az := range azs {
			ids[i] = az.ID
		}
		return ids
	}())

	// 2. 预检查阶段：检查所有AZ是否健康
	log.Printf("[Orchestrator] 预检查阶段：检查所有AZ健康状态")
	unhealthyAZs := []string{}
	for _, az := range azs {
		if err := o.azClient.HealthCheck(ctx, az.NSPAddr); err != nil {
			log.Printf("[Orchestrator] AZ %s 健康检查失败: %v", az.ID, err)
			unhealthyAZs = append(unhealthyAZs, az.ID)
		}
	}

	if len(unhealthyAZs) > 0 {
		return &models.VPCResponse{
			Success: false,
			Message: fmt.Sprintf("预检查失败: 以下AZ不健康: %v", unhealthyAZs),
		}, nil
	}

	// 3. 使用DTM Saga编排分布式事务
	log.Printf("[Orchestrator] 开始DTM Saga事务：创建VPC")
	gid := dtmcli.MustGenGid(o.dtmServerAddr)
	saga := dtmcli.NewSaga(o.dtmServerAddr, gid).
		SetConcurrent() // 设置并发模式（所有AZ并行创建）

	// 为每个AZ注册正向操作和补偿操作
	for _, az := range azs {
		actionURL := fmt.Sprintf("%s/api/v1/dtm/vpc", az.NSPAddr)
		compensateURL := fmt.Sprintf("%s/api/v1/dtm/vpc/compensate", az.NSPAddr)

		saga.Add(actionURL, compensateURL, req)
		log.Printf("[Orchestrator] 注册Saga步骤: AZ=%s, Action=%s, Compensate=%s", az.ID, actionURL, compensateURL)
	}

	// 提交Saga事务
	log.Printf("[Orchestrator] 提交DTM Saga事务 (GID: %s)", gid)
	err = saga.Submit()
	if err != nil {
		log.Printf("[Orchestrator] DTM Saga事务失败: %v", err)
		return &models.VPCResponse{
			Success: false,
			Message: fmt.Sprintf("VPC创建失败: %v (DTM已自动回滚)", err),
		}, nil
	}

	// 全部成功
	vpcID := uuid.New().String()
	log.Printf("[Orchestrator] VPC创建成功: %s, 在%d个AZ中创建完成 (DTM GID: %s)", req.VPCName, len(azs), gid)

	return &models.VPCResponse{
		Success: true,
		Message: fmt.Sprintf("VPC已在%d个AZ中成功创建", len(azs)),
		VPCID:   vpcID,
	}, nil
}

// rollbackVPC 已废弃：DTM自动处理补偿，无需手动回滚
// 保留此方法以兼容旧代码，但不再使用
func (o *Orchestrator) rollbackVPC(ctx context.Context, vpcName string, azs []*models.AZ) {
	log.Printf("[Orchestrator] ⚠️  rollbackVPC已废弃：DTM Saga自动处理补偿，无需手动调用")
}

// CreateAZSubnet 创建AZ级子网（路由到指定AZ）
func (o *Orchestrator) CreateAZSubnet(ctx context.Context, req *models.SubnetRequest) (*models.SubnetResponse, error) {
	log.Printf("[Orchestrator] 开始创建AZ级子网: %s (Region: %s, AZ: %s)", req.SubnetName, req.Region, req.AZ)

	// 1. 获取指定AZ的信息
	az, err := o.registry.GetAZ(ctx, req.Region, req.AZ)
	if err != nil {
		return &models.SubnetResponse{
			Success: false,
			Message: fmt.Sprintf("获取AZ信息失败: %v", err),
		}, nil
	}

	// 2. 检查AZ健康状态
	healthy, err := o.registry.CheckAZHealth(ctx, req.Region, req.AZ)
	if err != nil || !healthy {
		return &models.SubnetResponse{
			Success: false,
			Message: fmt.Sprintf("AZ %s 不可用", req.AZ),
		}, nil
	}

	// 3. 发送子网创建请求到目标AZ
	log.Printf("[Orchestrator] 向AZ %s 发送子网创建请求", az.ID)
	resp, err := o.azClient.CreateSubnet(ctx, az.NSPAddr, req)
	if err != nil {
		return &models.SubnetResponse{
			Success: false,
			Message: fmt.Sprintf("创建子网失败: %v", err),
		}, nil
	}

	return resp, nil
}
