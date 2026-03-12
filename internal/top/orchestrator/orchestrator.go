package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"workflow_qoder/internal/client"
	"workflow_qoder/internal/models"
	"workflow_qoder/internal/top/registry"
	topdao "workflow_qoder/internal/top/vpc/dao"

	"github.com/google/uuid"
	"github.com/paic/nsp-common/pkg/logger"
	"github.com/paic/nsp-common/pkg/saga"
	"github.com/paic/nsp-common/pkg/trace"
)

type Orchestrator struct {
	ctx        context.Context     // 长生命周期 context，用于后台 goroutine
	registry   *registry.Registry
	azClient   *client.AZNSPClient // 保留，用于健康检查
	topDAO     *topdao.TopVPCDAO
	sagaEngine *saga.Engine        // 新增
	tracedHTTP *trace.TracedClient // 新增
	wg         sync.WaitGroup      // 用于跟踪后台 goroutine 生命周期
}

func NewOrchestrator(ctx context.Context, registry *registry.Registry, topDB *sql.DB, sagaEngine *saga.Engine, tracedHTTP *trace.TracedClient) *Orchestrator {
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
		ctx:        ctx,
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
		WithTimeout(900) // 15 分钟超时，给 worker 足够时间

	// 为每个 AZ 添加一个步骤（串行执行，使用 Async + Poll 等待 worker 完成）
	for _, az := range azs {
		payloadBytes, _ := json.Marshal(req)
		var payloadMap map[string]any
		json.Unmarshal(payloadBytes, &payloadMap)
		builder.AddStep(saga.Step{
			Name:             fmt.Sprintf("创建VPC-%s", az.ID),
			Type:             saga.StepTypeAsync,
			ActionMethod:     "POST",
			ActionURL:        fmt.Sprintf("%s/api/v1/vpc", az.NSPAddr),
			ActionPayload:    payloadMap,
			CompensateMethod: "DELETE",
			CompensateURL:    fmt.Sprintf("%s/api/v1/vpc/%s", az.NSPAddr, req.VPCName),
			PollMethod:       "GET",
			PollURL:          fmt.Sprintf("%s/api/v1/vpc/%s/status", az.NSPAddr, req.VPCName),
			PollIntervalSec:  5,
			PollMaxTimes:     120,
			PollSuccessPath:  "$.status",
			PollSuccessValue: "running",
			PollFailurePath:  "$.status",
			PollFailureValue: "failed",
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

	// 5. 预注册 VPC 到 vpc_registry（一个 VPC 一条记录）
	if o.topDAO != nil {
		azDetails := make(map[string]models.AZDetail)
		for _, az := range azs {
			azDetails[az.ID] = models.AZDetail{Status: "creating"}
		}
		vpcReg := &models.VPCRegistry{
			ID:           uuid.New().String(),
			VPCName:      req.VPCName,
			Region:       req.Region,
			VRFName:      req.VRFName,
			VLANId:       req.VLANId,
			FirewallZone: req.FirewallZone,
			Status:       "creating",
			SagaTxID:     txID,
			AZDetails:    azDetails,
		}
		if err := o.topDAO.RegisterVPC(ctx, vpcReg); err != nil {
			logger.InfoContext(ctx, "预注册VPC到Top层失败", "error", err)
		}
	}

	// 6. 启动后台 goroutine 监听 SAGA 事务状态
	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		o.watchSagaTransaction(txID, req.VPCName, azs)
	}()

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
		vpc, err := o.topDAO.GetVPCByName(ctx, req.VPCName)
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

// watchSagaTransaction 后台监听 SAGA 事务状态，完成后回写 vpc_registry
func (o *Orchestrator) watchSagaTransaction(txID, vpcName string, azs []*models.AZ) {
	if o.topDAO == nil || o.sagaEngine == nil {
		logger.Info("watchSagaTransaction退出：topDAO或sagaEngine为nil", "tx_id", txID, "vpc_name", vpcName)
		return
	}

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	timeout := time.After(15 * time.Minute)

	for {
		select {
		case <-o.ctx.Done():
			logger.Info("SAGA监听因context取消而退出", "tx_id", txID, "vpc_name", vpcName)
			return
		case <-timeout:
			logger.Info("SAGA监听超时，标记VPC失败", "tx_id", txID, "vpc_name", vpcName)
			// 使用独立 context 确保 DB 操作能完成，避免因主 context 取消导致 DB 写入失败
			dbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			azDetails := make(map[string]models.AZDetail)
			for _, az := range azs {
				azDetails[az.ID] = models.AZDetail{Status: "failed", Error: "timeout"}
			}
			if err := o.topDAO.UpdateVPCOverallStatus(dbCtx, vpcName, "failed", azDetails); err != nil {
				logger.Info("超时标记VPC失败时DB更新失败", "vpc_name", vpcName, "error", err)
			}
			cancel()
			return
		case <-ticker.C:
			status, err := o.sagaEngine.Query(o.ctx, txID)
			if err != nil {
				logger.Info("SAGA事务查询失败", "tx_id", txID, "error", err)
				continue
			}
			if status == nil {
				logger.Info("SAGA事务查询返回nil", "tx_id", txID)
				continue
			}

			switch saga.TxStatus(status.Status) {
			case saga.TxStatusSucceeded:
				azDetails := make(map[string]models.AZDetail)
				for _, az := range azs {
					azDetails[az.ID] = models.AZDetail{Status: "running"}
				}
				if err := o.topDAO.UpdateVPCOverallStatus(o.ctx, vpcName, "running", azDetails); err != nil {
					logger.Info("更新VPC状态失败", "vpc_name", vpcName, "error", err)
				}
				logger.Info("VPC创建完成，状态已更新", "tx_id", txID, "vpc_name", vpcName)
				return

			case saga.TxStatusFailed:
				azDetails := make(map[string]models.AZDetail)
				for i, step := range status.Steps {
					if i < len(azs) {
						d := models.AZDetail{}
						if saga.StepStatus(step.Status) == saga.StepStatusSucceeded {
							d.Status = "running"
						} else {
							d.Status = "failed"
							d.Error = step.LastError
						}
						azDetails[azs[i].ID] = d
					}
				}
				if err := o.topDAO.UpdateVPCOverallStatus(o.ctx, vpcName, "failed", azDetails); err != nil {
					logger.Info("更新VPC状态失败", "vpc_name", vpcName, "error", err)
				}
				logger.Info("VPC创建失败，状态已更新", "tx_id", txID, "vpc_name", vpcName, "error", status.LastError)
				return
				// pending / running / compensating → 继续轮询
			}
		}
	}
}

// GetVPCByName 从 Top 层数据库查询单个 VPC 记录
func (o *Orchestrator) GetVPCByName(ctx context.Context, vpcName string) (*models.VPCRegistry, error) {
	if o.topDAO == nil {
		return nil, fmt.Errorf("topDAO is nil")
	}
	return o.topDAO.GetVPCByName(ctx, vpcName)
}

// ListAllVPCs 从 Top 层数据库查询所有 VPC
func (o *Orchestrator) ListAllVPCs(ctx context.Context) ([]*models.VPCRegistry, error) {
	if o.topDAO == nil {
		return nil, fmt.Errorf("topDAO is nil")
	}
	return o.topDAO.ListAllVPCs(ctx)
}

// UpdateVPCStatus 更新 Top 层 vpc_registry 的整体状态和 per-AZ 详情
func (o *Orchestrator) UpdateVPCStatus(ctx context.Context, vpcName, status string, azDetails map[string]models.AZDetail) error {
	if o.topDAO == nil {
		return fmt.Errorf("topDAO is nil")
	}
	return o.topDAO.UpdateVPCOverallStatus(ctx, vpcName, status, azDetails)
}

// HasTopDAO 检查 topDAO 是否可用
func (o *Orchestrator) HasTopDAO() bool {
	return o.topDAO != nil
}

// Shutdown 优雅关闭，等待所有后台 goroutine 完成
func (o *Orchestrator) Shutdown() {
	o.wg.Wait()
}
