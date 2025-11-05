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

// CreateRegionVPC 创建Region级VPC（分发到所有AZ，支持自动回滚）
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

	// 3. 执行阶段：并行发送VPC创建请求到所有AZ
	log.Printf("[Orchestrator] 执行阶段：并行创建VPC")
	type azResult struct {
		az          *models.AZ
		workflowID  string
		err         error
		success     bool
	}

	var wg sync.WaitGroup
	resultChan := make(chan *azResult, len(azs))

	for _, az := range azs {
		wg.Add(1)
		go func(az *models.AZ) {
			defer wg.Done()

			log.Printf("[Orchestrator] 向AZ %s 发送VPC创建请求", az.ID)
			resp, err := o.azClient.CreateVPC(ctx, az.NSPAddr, req)

			result := &azResult{az: az}
			if err != nil {
				log.Printf("[Orchestrator] AZ %s 创建失败: %v", az.ID, err)
				result.err = err
				result.success = false
			} else if !resp.Success {
				log.Printf("[Orchestrator] AZ %s 创建失败: %s", az.ID, resp.Message)
				result.err = fmt.Errorf("%s", resp.Message)
				result.success = false
			} else {
				log.Printf("[Orchestrator] AZ %s 创建成功: workflow_id=%s", az.ID, resp.WorkflowID)
				result.workflowID = resp.WorkflowID
				result.success = true
			}
			resultChan <- result
		}(az)
	}

	// 等待所有AZ完成
	wg.Wait()
	close(resultChan)

	// 4. 收集结果
	successAZs := make([]*models.AZ, 0)
	failedAZs := make([]*models.AZ, 0)
	results := make(map[string]string)

	for result := range resultChan {
		if result.success {
			successAZs = append(successAZs, result.az)
			results[result.az.ID] = result.workflowID
		} else {
			failedAZs = append(failedAZs, result.az)
			results[result.az.ID] = fmt.Sprintf("失败: %v", result.err)
		}
	}

	// 5. 判断是否需要回滚
	if len(failedAZs) > 0 {
		log.Printf("[Orchestrator] 检测到失败: %d个成功, %d个失败", len(successAZs), len(failedAZs))
		
		// 如果部分成功，触发回滚
		if len(successAZs) > 0 {
			log.Printf("[Orchestrator] 触发回滚：清理%d个已成功的AZ", len(successAZs))
			o.rollbackVPC(ctx, req.VPCName, successAZs)
		}

		return &models.VPCResponse{
			Success: false,
			Message: fmt.Sprintf("VPC创建失败: %d个AZ失败，已回滚成功的%d个AZ", len(failedAZs), len(successAZs)),
			AZResults: results,
		}, nil
	}

	// 6. 全部成功，构造响应
	vpcID := uuid.New().String()
	log.Printf("[Orchestrator] VPC创建成功: %s, 在%d个AZ中创建完成", req.VPCName, len(azs))

	return &models.VPCResponse{
		Success:   true,
		Message:   fmt.Sprintf("VPC已在%d个AZ中成功创建", len(azs)),
		VPCID:     vpcID,
		AZResults: results,
	}, nil
}

// rollbackVPC 回滚VPC创建（删除已成功创建的VPC）
func (o *Orchestrator) rollbackVPC(ctx context.Context, vpcName string, azs []*models.AZ) {
	log.Printf("[Orchestrator] 开始回滚VPC: %s, 涉及%d个AZ", vpcName, len(azs))

	var wg sync.WaitGroup
	for _, az := range azs {
		wg.Add(1)
		go func(az *models.AZ) {
			defer wg.Done()
			
			log.Printf("[Orchestrator] 回滚AZ %s 的VPC: %s", az.ID, vpcName)
			if err := o.azClient.DeleteVPC(ctx, az.NSPAddr, vpcName); err != nil {
				log.Printf("[Orchestrator] ⚠️  AZ %s 回滚失败: %v (需要人工介入)", az.ID, err)
			} else {
				log.Printf("[Orchestrator] ✓ AZ %s 回滚成功", az.ID)
			}
		}(az)
	}
	wg.Wait()
	log.Printf("[Orchestrator] VPC回滚完成: %s", vpcName)
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
