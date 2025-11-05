package orchestrator

import (
	"context"
	"fmt"
	"log"
	"sync"

	"workflow_qoder/internal/client"
	"workflow_qoder/internal/models"
	"workflow_qoder/internal/top/registry"

	"github.com/google/uuid"
)

// Orchestrator 服务编排器
type Orchestrator struct {
	registry *registry.Registry
	azClient *client.AZNSPClient
}

// NewOrchestrator 创建编排器
func NewOrchestrator(registry *registry.Registry) *Orchestrator {
	return &Orchestrator{
		registry: registry,
		azClient: client.NewAZNSPClient(),
	}
}

// CreateRegionVPC 创建Region级VPC（分发到所有AZ）
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

	// 2. 并行发送VPC创建请求到所有AZ
	var wg sync.WaitGroup
	results := make(map[string]string)
	var mu sync.Mutex
	var hasError bool

	for _, az := range azs {
		wg.Add(1)
		go func(az *models.AZ) {
			defer wg.Done()

			log.Printf("[Orchestrator] 向AZ %s 发送VPC创建请求", az.ID)
			resp, err := o.azClient.CreateVPC(ctx, az.NSPAddr, req)

			mu.Lock()
			defer mu.Unlock()

			if err != nil {
				log.Printf("[Orchestrator] AZ %s 创建失败: %v", az.ID, err)
				results[az.ID] = fmt.Sprintf("失败: %v", err)
				hasError = true
			} else if !resp.Success {
				log.Printf("[Orchestrator] AZ %s 创建失败: %s", az.ID, resp.Message)
				results[az.ID] = fmt.Sprintf("失败: %s", resp.Message)
				hasError = true
			} else {
				log.Printf("[Orchestrator] AZ %s 创建成功: workflow_id=%s", az.ID, resp.WorkflowID)
				results[az.ID] = resp.WorkflowID
			}
		}(az)
	}

	// 等待所有AZ完成
	wg.Wait()

	// 3. 构造响应
	vpcID := uuid.New().String()
	message := fmt.Sprintf("VPC已在%d个AZ中创建", len(azs))
	if hasError {
		message = fmt.Sprintf("VPC创建部分成功，%d个AZ中有失败", len(azs))
	}

	return &models.VPCResponse{
		Success:   !hasError,
		Message:   message,
		VPCID:     vpcID,
		AZResults: results,
	}, nil
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
