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
	pccndao "workflow_qoder/internal/top/pccn/dao"
	"workflow_qoder/internal/top/registry"
	topdao "workflow_qoder/internal/top/vpc/dao"

	"github.com/google/uuid"
	"github.com/paic/nsp-common/pkg/logger"
	"github.com/paic/nsp-common/pkg/saga"
	"github.com/paic/nsp-common/pkg/trace"
)

type Orchestrator struct {
	ctx        context.Context // 长生命周期 context，用于后台 goroutine
	registry   *registry.Registry
	azClient   *client.AZNSPClient // 保留，用于健康检查和状态查询
	topDAO     *topdao.TopVPCDAO
	pccnDAO    *pccndao.TopPCCNDAO // PCCN DAO
	sagaEngine *saga.Engine
	tracedHTTP *trace.TracedClient
	wg         sync.WaitGroup
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

	// Initialize PCCN DAO
	var pccnDAO *pccndao.TopPCCNDAO
	if topDB != nil {
		pccnDAO = pccndao.NewTopPCCNDAO(topDB)
	}

	return &Orchestrator{
		ctx:        ctx,
		registry:   registry,
		azClient:   azClient,
		topDAO:     dao,
		pccnDAO:    pccnDAO,
		sagaEngine: sagaEngine,
		tracedHTTP: tracedHTTP,
	}
}

// =====================================================
// VPC Methods
// =====================================================

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

	// 3. 统一生成 VPC ID（Top 层和 AZ 层使用同一个 ID）
	vpcID := uuid.New().String()

	// 4. 构建 SAGA 事务定义
	builder := saga.NewSaga(fmt.Sprintf("region-vpc-create-%s", req.VPCName)).
		WithPayload(map[string]any{"vpc_name": req.VPCName, "region": req.Region}).
		WithTimeout(60) // 1 分钟超时，Sync 步骤只等 API 调用

	// 为每个 AZ 添加一个步骤（Sync：POST 返回 200 即为成功）
	for _, az := range azs {
		// 将统一的 VPC ID 注入到请求中，确保 AZ 层使用相同 ID
		reqWithID := *req
		reqWithID.VPCID = vpcID
		payloadBytes, _ := json.Marshal(&reqWithID)
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

	// 5. 预注册 VPC 到 vpc_registry（使用统一的 VPC ID）
	if o.topDAO != nil {
		azDetails := make(map[string]models.AZDetail)
		for _, az := range azs {
			azDetails[az.ID] = models.AZDetail{Status: "creating"}
		}
		vpcReg := &models.VPCRegistry{
			ID:           vpcID,
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
		o.watchSagaAndPollAZs(txID, req.VPCName, azs)
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

// watchSagaAndPollAZs 分两阶段监听 VPC 创建：
//
//	阶段 1: 等待 Saga 事务完成（API 调用层）
//	阶段 2: 直接 Poll 各 AZ 接口收集 Worker 最终状态
func (o *Orchestrator) watchSagaAndPollAZs(txID, vpcName string, azs []*models.AZ) {
	if o.topDAO == nil || o.sagaEngine == nil {
		return
	}

	// ========== 阶段 1: 等待 Saga 完成 ==========
	sagaStatus := o.waitForSagaCompletion(txID, vpcName, azs)
	if sagaStatus != saga.TxStatusSucceeded {
		// Saga 失败 = API 调用失败，引擎已自动补偿，直接标记
		o.markVPCFromSagaFailure(txID, vpcName, azs)
		return
	}

	// ========== 阶段 2: Saga 成功后，Poll 各 AZ 的 Worker 状态 ==========
	o.pollAZWorkerStatuses(vpcName, azs)
}

// waitForSagaCompletion 轮询 Saga 引擎直到事务完结
// 返回最终的 TxStatus（succeeded / failed）
func (o *Orchestrator) waitForSagaCompletion(txID, vpcName string, azs []*models.AZ) saga.TxStatus {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	timeout := time.After(2 * time.Minute) // Sync 步骤不需要很长

	for {
		select {
		case <-o.ctx.Done():
			// 服务关闭时使用独立 context 标记状态，避免 DB 写入失败
			dbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			azDetails := make(map[string]models.AZDetail)
			for _, az := range azs {
				azDetails[az.ID] = models.AZDetail{Status: "interrupted", Error: "service shutdown"}
			}
			o.topDAO.UpdateVPCOverallStatus(dbCtx, vpcName, "interrupted", azDetails)
			cancel()
			return saga.TxStatusFailed
		case <-timeout:
			logger.Info("Saga等待超时", "tx_id", txID)
			dbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			azDetails := make(map[string]models.AZDetail)
			for _, az := range azs {
				azDetails[az.ID] = models.AZDetail{Status: "failed", Error: "saga timeout"}
			}
			o.topDAO.UpdateVPCOverallStatus(dbCtx, vpcName, "failed", azDetails)
			cancel()
			return saga.TxStatusFailed
		case <-ticker.C:
			status, err := o.sagaEngine.Query(o.ctx, txID)
			if err != nil || status == nil {
				continue
			}
			switch saga.TxStatus(status.Status) {
			case saga.TxStatusSucceeded:
				return saga.TxStatusSucceeded
			case saga.TxStatusFailed:
				return saga.TxStatusFailed
			}
			// pending / running / compensating → 继续等
		}
	}
}

// markVPCFromSagaFailure Saga 失败时，根据各 Step 状态标记 per-AZ 详情
// 注意：此方法可能在 o.ctx 已取消时被调用（waitForSagaCompletion 的 ctx.Done 分支），
// 因此使用独立的 context.Background() 执行 DB 操作和 Saga 查询。
func (o *Orchestrator) markVPCFromSagaFailure(txID, vpcName string, azs []*models.AZ) {
	dbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	status, err := o.sagaEngine.Query(dbCtx, txID)
	if err != nil || status == nil {
		return
	}
	azDetails := make(map[string]models.AZDetail)
	for i, step := range status.Steps {
		if i < len(azs) {
			d := models.AZDetail{}
			if saga.StepStatus(step.Status) == saga.StepStatusSucceeded {
				d.Status = "compensated" // API 成功但被补偿了
			} else {
				d.Status = "failed"
				d.Error = step.LastError
			}
			azDetails[azs[i].ID] = d
		}
	}
	// 注意：Steps 与 azs 的索引对应关系依赖于构建 Saga 时一个 AZ 一个 Step 按序添加，
	// 后续如果在 Steps 中插入非 AZ 相关步骤，需要改用 Step Name 中的 AZ ID 做显式映射。
	o.topDAO.UpdateVPCOverallStatus(dbCtx, vpcName, "failed", azDetails)
	logger.Info("VPC Saga失败，状态已更新", "tx_id", txID, "vpc_name", vpcName)
}

// pollAZWorkerStatuses Saga 成功后，直接 Poll 各 AZ 接口收集 Worker 最终状态
// TODO: 当前对各 AZ 的查询是串行的，AZ 数量较多时可改为并发 fan-out
func (o *Orchestrator) pollAZWorkerStatuses(vpcName string, azs []*models.AZ) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	timeout := time.After(15 * time.Minute) // Worker 执行可能较慢

	// 跟踪每个 AZ 是否已到达终态
	settled := make(map[string]bool)

	for {
		select {
		case <-o.ctx.Done():
			// 服务关闭时使用独立 context 标记状态
			dbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			vpc, err := o.topDAO.GetVPCByName(dbCtx, vpcName)
			if err == nil && vpc != nil {
				for _, az := range azs {
					if !settled[az.ID] {
						vpc.AZDetails[az.ID] = models.AZDetail{Status: "interrupted", Error: "service shutdown"}
					}
				}
				o.topDAO.UpdateVPCOverallStatus(dbCtx, vpcName, o.computeOverallStatus(vpc.AZDetails), vpc.AZDetails)
			}
			cancel()
			return
		case <-timeout:
			logger.Info("Worker状态轮询超时", "vpc_name", vpcName)
			dbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			azDetails := make(map[string]models.AZDetail)
			for _, az := range azs {
				if !settled[az.ID] {
					azDetails[az.ID] = models.AZDetail{Status: "failed", Error: "worker poll timeout"}
				}
			}
			// 仅更新未到达终态的 AZ（已到终态的保留原值）
			if len(azDetails) > 0 {
				// TODO: Read-Modify-Write 存在竞态窗口，后续可改用 SQL jsonb_set 做原子 merge
				vpc, err := o.topDAO.GetVPCByName(dbCtx, vpcName)
				if err == nil && vpc != nil {
					for azID, detail := range azDetails {
						vpc.AZDetails[azID] = detail
					}
					o.topDAO.UpdateVPCOverallStatus(dbCtx, vpcName, o.computeOverallStatus(vpc.AZDetails), vpc.AZDetails)
				}
			}
			cancel()
			return
		case <-ticker.C:
			allSettled := true
			azDetails := make(map[string]models.AZDetail)

			for _, az := range azs {
				if settled[az.ID] {
					continue
				}

				vpcStatus, err := o.azClient.GetVPCStatus(o.ctx, az.NSPAddr, vpcName)
				if err != nil {
					logger.Info("查询AZ Worker状态失败", "az", az.ID, "error", err)
					allSettled = false
					continue
				}

				switch vpcStatus.Status {
				case models.ResourceStatusRunning:
					azDetails[az.ID] = models.AZDetail{Status: "running"}
					settled[az.ID] = true
				case models.ResourceStatusFailed:
					azDetails[az.ID] = models.AZDetail{Status: "failed", Error: vpcStatus.ErrorMessage}
					settled[az.ID] = true
				default:
					// creating / pending → 还在执行，继续等
					allSettled = false
				}
			}

			// 有变化就增量更新 DB
			if len(azDetails) > 0 {
				// TODO: Read-Modify-Write 存在竞态窗口，后续可改用 SQL jsonb_set 做原子 merge
				vpc, err := o.topDAO.GetVPCByName(o.ctx, vpcName)
				if err == nil && vpc != nil {
					for azID, detail := range azDetails {
						vpc.AZDetails[azID] = detail
					}
					overall := o.computeOverallStatus(vpc.AZDetails)
					o.topDAO.UpdateVPCOverallStatus(o.ctx, vpcName, overall, vpc.AZDetails)
				}
			}

			if allSettled {
				logger.Info("所有AZ Worker已完成", "vpc_name", vpcName)
				return
			}
		}
	}
}

// computeOverallStatus 根据各 AZ 状态计算整体状态
func (o *Orchestrator) computeOverallStatus(azDetails map[string]models.AZDetail) string {
	hasFailed := false
	hasCreating := false
	hasInterrupted := false
	allRunning := true
	for _, d := range azDetails {
		switch d.Status {
		case "running":
			// ok
		case "failed":
			hasFailed = true
			allRunning = false
		case "creating":
			hasCreating = true
			allRunning = false
		case "interrupted":
			hasInterrupted = true
			allRunning = false
		default:
			// "compensated", "deleted" 等非常规状态
			allRunning = false
		}
	}
	if allRunning {
		return "running"
	}
	if hasFailed {
		hasRunning := false
		for _, d := range azDetails {
			if d.Status == "running" {
				hasRunning = true
				break
			}
		}
		if hasRunning {
			return "partial_running"
		}
		return "failed"
	}
	if hasInterrupted {
		return "interrupted"
	}
	if hasCreating {
		return "creating"
	}
	return "failed"
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

// =====================================================
// PCCN Methods (Private Cloud Connection Network)
// =====================================================

// CreatePCCN creates a PCCN connection between two VPCs (supports cross-Region)
// 设计参考 docs/pccn_design.md
// 实现参考 VPC 重构方案：Saga Sync + 业务层 Poll
func (o *Orchestrator) CreatePCCN(ctx context.Context, req *models.PCCNRequest) (*models.PCCNResponse, error) {
	if o.pccnDAO == nil {
		return &models.PCCNResponse{Success: false, Message: "PCCN DAO not initialized"}, nil
	}
	if o.topDAO == nil {
		return &models.PCCNResponse{Success: false, Message: "Top DAO not initialized"}, nil
	}

	logger.InfoContext(ctx, "开始创建PCCN连接",
		"pccn_name", req.PCCNName,
		"vpc1", fmt.Sprintf("%s/%s", req.VPC1.Region, req.VPC1.VPCName),
		"vpc2", fmt.Sprintf("%s/%s", req.VPC2.Region, req.VPC2.VPCName),
	)

	// 1. 预检查：验证两个VPC存在且状态正常
	vpc1, err := o.topDAO.GetVPCByName(ctx, req.VPC1.VPCName)
	if err != nil {
		return &models.PCCNResponse{Success: false, Message: fmt.Sprintf("VPC1不存在: %v", err)}, nil
	}
	if vpc1.Region != req.VPC1.Region {
		return &models.PCCNResponse{Success: false, Message: fmt.Sprintf("VPC1 Region不匹配: 请求=%s, 实际=%s", req.VPC1.Region, vpc1.Region)}, nil
	}

	vpc2, err := o.topDAO.GetVPCByName(ctx, req.VPC2.VPCName)
	if err != nil {
		return &models.PCCNResponse{Success: false, Message: fmt.Sprintf("VPC2不存在: %v", err)}, nil
	}
	if vpc2.Region != req.VPC2.Region {
		return &models.PCCNResponse{Success: false, Message: fmt.Sprintf("VPC2 Region不匹配: 请求=%s, 实际=%s", req.VPC2.Region, vpc2.Region)}, nil
	}

	// 2. 获取两个VPC涉及的AZ（可能跨Region）
	vpc1AZs := o.getAZsFromVPCDetails(ctx, vpc1)
	vpc2AZs := o.getAZsFromVPCDetails(ctx, vpc2)
	allAZs := append(vpc1AZs, vpc2AZs...)

	// 3. 健康检查所有AZ（跨Region）
	for _, az := range allAZs {
		if err := o.azClient.HealthCheck(ctx, az.NSPAddr); err != nil {
			return &models.PCCNResponse{Success: false, Message: fmt.Sprintf("AZ %s 不健康", az.ID)}, nil
		}
	}

	// 4. 生成统一的PCCN ID
	pccnID := uuid.New().String()

	// 5. 构建 Saga 事务（Sync Step：POST 返回 200 即为成功）
	// 参考 VPC 重构方案：Poll 在 Saga 之外，由业务层直接轮询
	builder := saga.NewSaga(fmt.Sprintf("pccn-create-%s", req.PCCNName)).
		WithPayload(map[string]any{
			"pccn_name":   req.PCCNName,
			"vpc1_name":   req.VPC1.VPCName,
			"vpc1_region": req.VPC1.Region,
			"vpc2_name":   req.VPC2.VPCName,
			"vpc2_region": req.VPC2.Region,
		}).
		WithTimeout(60) // 1 分钟超时，Sync 步骤只等 API 调用

	// 为VPC1的每个AZ添加 Sync Step
	for _, az := range vpc1AZs {
		azReq := &models.PCCNRequest{
			PCCNID:   pccnID,
			PCCNName: req.PCCNName,
			VPC1:     req.VPC1,
			VPC2:     req.VPC2,
		}
		payloadBytes, _ := json.Marshal(azReq)
		var payloadMap map[string]any
		json.Unmarshal(payloadBytes, &payloadMap)

		builder.AddStep(saga.Step{
			Name:             fmt.Sprintf("提交PCCN创建-VPC1-%s", az.ID),
			Type:             saga.StepTypeSync,
			ActionMethod:     "POST",
			ActionURL:        fmt.Sprintf("%s/api/v1/pccn", az.NSPAddr),
			ActionPayload:    payloadMap,
			CompensateMethod: "DELETE",
			CompensateURL:    fmt.Sprintf("%s/api/v1/pccn/%s", az.NSPAddr, req.PCCNName),
		})
	}

	// 为VPC2的每个AZ添加 Sync Step
	for _, az := range vpc2AZs {
		azReq := &models.PCCNRequest{
			PCCNID:   pccnID,
			PCCNName: req.PCCNName,
			VPC1:     req.VPC1,
			VPC2:     req.VPC2,
		}
		payloadBytes, _ := json.Marshal(azReq)
		var payloadMap map[string]any
		json.Unmarshal(payloadBytes, &payloadMap)

		builder.AddStep(saga.Step{
			Name:             fmt.Sprintf("提交PCCN创建-VPC2-%s", az.ID),
			Type:             saga.StepTypeSync,
			ActionMethod:     "POST",
			ActionURL:        fmt.Sprintf("%s/api/v1/pccn", az.NSPAddr),
			ActionPayload:    payloadMap,
			CompensateMethod: "DELETE",
			CompensateURL:    fmt.Sprintf("%s/api/v1/pccn/%s", az.NSPAddr, req.PCCNName),
		})
	}

	def, err := builder.Build()
	if err != nil {
		return &models.PCCNResponse{Success: false, Message: fmt.Sprintf("构建Saga定义失败: %v", err)}, nil
	}

	// 6. 提交Saga事务
	txID, err := o.sagaEngine.Submit(ctx, def)
	if err != nil {
		return &models.PCCNResponse{Success: false, Message: fmt.Sprintf("提交Saga事务失败: %v", err)}, nil
	}

	logger.InfoContext(ctx, "Saga事务已提交", "transaction_id", txID, "pccn_name", req.PCCNName)

	// 7. 预注册PCCN（包含跨Region信息）
	vpcDetails := make(map[string]models.VPCDetail)
	vpc1Key := fmt.Sprintf("%s/%s", req.VPC1.Region, req.VPC1.VPCName)
	vpc2Key := fmt.Sprintf("%s/%s", req.VPC2.Region, req.VPC2.VPCName)

	vpc1AZIDs := make([]string, len(vpc1AZs))
	for i, az := range vpc1AZs {
		vpc1AZIDs[i] = az.ID
	}
	vpc2AZIDs := make([]string, len(vpc2AZs))
	for i, az := range vpc2AZs {
		vpc2AZIDs[i] = az.ID
	}

	vpcDetails[vpc1Key] = models.VPCDetail{
		Region: req.VPC1.Region,
		AZs:    vpc1AZIDs,
		Status: "creating",
	}
	vpcDetails[vpc2Key] = models.VPCDetail{
		Region: req.VPC2.Region,
		AZs:    vpc2AZIDs,
		Status: "creating",
	}

	pccnReg := &models.PCCNRegistry{
		ID:         pccnID,
		PCCNName:   req.PCCNName,
		VPC1Name:   req.VPC1.VPCName,
		VPC1Region: req.VPC1.Region,
		VPC2Name:   req.VPC2.VPCName,
		VPC2Region: req.VPC2.Region,
		Status:     "creating",
		TxID:       txID,
		VPCDetails: vpcDetails,
	}
	if err := o.pccnDAO.RegisterPCCN(ctx, pccnReg); err != nil {
		logger.InfoContext(ctx, "预注册PCCN到Top层失败", "error", err)
	}

	// 8. 启动后台goroutine监听Saga状态并 Poll AZ Worker 状态
	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		o.watchPCCNSagaAndPollAZs(txID, req.PCCNName, vpc1AZs, vpc2AZs, req.VPC1, req.VPC2)
	}()

	return &models.PCCNResponse{
		Success: true,
		Message: fmt.Sprintf("PCCN创建任务已提交，事务ID: %s", txID),
		PCCNID:  pccnID,
		TxID:    txID,
	}, nil
}

// getAZsFromVPCDetails 从VPCRegistry的AZDetails中提取AZ列表
func (o *Orchestrator) getAZsFromVPCDetails(ctx context.Context, vpc *models.VPCRegistry) []*models.AZ {
	var azs []*models.AZ
	for azID := range vpc.AZDetails {
		az, err := o.registry.GetAZ(ctx, vpc.Region, azID)
		if err != nil {
			logger.InfoContext(ctx, "获取AZ信息失败", "az_id", azID, "error", err)
			continue
		}
		azs = append(azs, az)
	}
	return azs
}

// watchPCCNSagaAndPollAZs 分两阶段监听 PCCN 创建：
//
//	阶段 1: 等待 Saga 事务完成（API 调用层）
//	阶段 2: 直接 Poll 各 AZ 接口收集 Worker 最终状态
//
// 参考: watchSagaAndPollAZs (VPC)
func (o *Orchestrator) watchPCCNSagaAndPollAZs(txID, pccnName string, vpc1AZs, vpc2AZs []*models.AZ, vpc1, vpc2 models.VPCRef) {
	if o.pccnDAO == nil || o.sagaEngine == nil {
		return
	}

	allAZs := append(vpc1AZs, vpc2AZs...)

	// ========== 阶段 1: 等待 Saga 完成 ==========
	sagaStatus := o.waitForPCCNSagaCompletion(txID, pccnName)
	if sagaStatus != saga.TxStatusSucceeded {
		// Saga 失败 = API 调用失败，引擎已自动补偿
		o.markPCCNFromSagaFailure(txID, pccnName)
		return
	}

	// ========== 阶段 2: Saga 成功后，Poll 各 AZ 的 Worker 状态 ==========
	o.pollPCCNAZWorkerStatuses(pccnName, allAZs, vpc1, vpc2)
}

// waitForPCCNSagaCompletion 等待Saga事务完成
func (o *Orchestrator) waitForPCCNSagaCompletion(txID, pccnName string) saga.TxStatus {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	timeout := time.After(2 * time.Minute) // Sync 步骤不需要很长

	for {
		select {
		case <-o.ctx.Done():
			dbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			o.pccnDAO.UpdatePCCNStatus(dbCtx, pccnName, "interrupted", nil)
			cancel()
			return saga.TxStatusFailed
		case <-timeout:
			logger.Info("PCCN Saga等待超时", "tx_id", txID, "pccn_name", pccnName)
			dbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			o.pccnDAO.UpdatePCCNStatus(dbCtx, pccnName, "failed", nil)
			cancel()
			return saga.TxStatusFailed
		case <-ticker.C:
			status, err := o.sagaEngine.Query(o.ctx, txID)
			if err != nil || status == nil {
				continue
			}
			switch saga.TxStatus(status.Status) {
			case saga.TxStatusSucceeded:
				return saga.TxStatusSucceeded
			case saga.TxStatusFailed:
				return saga.TxStatusFailed
			}
			// pending / running / compensating → 继续等
		}
	}
}

// markPCCNFromSagaFailure Saga失败时更新状态
func (o *Orchestrator) markPCCNFromSagaFailure(txID, pccnName string) {
	dbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	o.pccnDAO.UpdatePCCNStatus(dbCtx, pccnName, "failed", nil)
	logger.Info("PCCN Saga失败，状态已更新", "tx_id", txID, "pccn_name", pccnName)
}

// pollPCCNAZWorkerStatuses Saga 成功后，直接 Poll 各 AZ 接口收集 Worker 最终状态
func (o *Orchestrator) pollPCCNAZWorkerStatuses(pccnName string, allAZs []*models.AZ, vpc1, vpc2 models.VPCRef) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	timeout := time.After(15 * time.Minute) // Worker 执行可能较慢

	// 跟踪每个 AZ 是否已到达终态
	settled := make(map[string]bool)

	for {
		select {
		case <-o.ctx.Done():
			// 服务关闭时标记状态
			dbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			o.pccnDAO.UpdatePCCNStatus(dbCtx, pccnName, "interrupted", nil)
			cancel()
			return
		case <-timeout:
			logger.Info("PCCN Worker状态轮询超时", "pccn_name", pccnName)
			dbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			o.pccnDAO.UpdatePCCNStatus(dbCtx, pccnName, "failed", nil)
			cancel()
			return
		case <-ticker.C:
			allSettled := true
			hasFailed := false

			for _, az := range allAZs {
				if settled[az.ID] {
					continue
				}

				pccnStatus, err := o.azClient.GetPCCNStatus(o.ctx, az.NSPAddr, pccnName)
				if err != nil {
					logger.Info("查询AZ PCCN Worker状态失败", "az", az.ID, "error", err)
					allSettled = false
					continue
				}

				switch pccnStatus.Status {
				case models.ResourceStatusRunning:
					settled[az.ID] = true
				case models.ResourceStatusFailed:
					settled[az.ID] = true
					hasFailed = true
				default:
					// creating / pending → 还在执行，继续等
					allSettled = false
				}
			}

			if allSettled {
				// 所有 AZ 都已到达终态
				dbCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()

				if hasFailed {
					o.pccnDAO.UpdatePCCNStatus(dbCtx, pccnName, "partial_running", nil)
					logger.Info("PCCN 部分成功", "pccn_name", pccnName)
				} else {
					// 全部成功，更新 VPC 详情
					vpcDetails := make(map[string]models.VPCDetail)
					vpc1Key := fmt.Sprintf("%s/%s", vpc1.Region, vpc1.VPCName)
					vpc2Key := fmt.Sprintf("%s/%s", vpc2.Region, vpc2.VPCName)

					vpcDetails[vpc1Key] = models.VPCDetail{
						Region: vpc1.Region,
						Status: "running",
					}
					vpcDetails[vpc2Key] = models.VPCDetail{
						Region: vpc2.Region,
						Status: "running",
					}

					o.pccnDAO.UpdatePCCNStatus(dbCtx, pccnName, "running", vpcDetails)
					logger.Info("PCCN 创建成功", "pccn_name", pccnName)
				}
				return
			}
		}
	}
}

// GetPCCNStatus 查询PCCN状态
func (o *Orchestrator) GetPCCNStatus(ctx context.Context, pccnName string) (*models.PCCNStatusQueryResponse, error) {
	if o.pccnDAO == nil {
		return nil, fmt.Errorf("PCCN DAO not initialized")
	}

	pccn, err := o.pccnDAO.GetPCCNByName(ctx, pccnName)
	if err != nil {
		return nil, fmt.Errorf("PCCN %s not found", pccnName)
	}

	return &models.PCCNStatusQueryResponse{
		PCCNName:      pccn.PCCNName,
		OverallStatus: pccn.Status,
		VPCDetails:    pccn.VPCDetails,
		Source:        "database",
	}, nil
}

// DeletePCCN 删除PCCN连接
func (o *Orchestrator) DeletePCCN(ctx context.Context, pccnName string) (*models.PCCNResponse, error) {
	if o.pccnDAO == nil {
		return &models.PCCNResponse{Success: false, Message: "PCCN DAO not initialized"}, nil
	}

	// 1. 查询PCCN信息
	pccn, err := o.pccnDAO.GetPCCNByName(ctx, pccnName)
	if err != nil {
		return &models.PCCNResponse{Success: false, Message: fmt.Sprintf("PCCN不存在: %v", err)}, nil
	}

	if pccn.Status != "running" && pccn.Status != "partial_running" {
		return &models.PCCNResponse{Success: false, Message: fmt.Sprintf("PCCN状态不是running，无法删除: %s", pccn.Status)}, nil
	}

	// 2. 获取涉及的AZ（跨Region）
	vpc1AZs := o.getAZsFromRegionAndVPC(ctx, pccn.VPC1Region, pccn.VPC1Name)
	vpc2AZs := o.getAZsFromRegionAndVPC(ctx, pccn.VPC2Region, pccn.VPC2Name)
	allAZs := append(vpc1AZs, vpc2AZs...)

	// 3. 构建Saga事务
	builder := saga.NewSaga(fmt.Sprintf("pccn-delete-%s", pccnName)).
		WithPayload(map[string]any{
			"pccn_name":   pccnName,
			"vpc1_region": pccn.VPC1Region,
			"vpc2_region": pccn.VPC2Region,
		}).
		WithTimeout(120)

	// 为每个AZ添加删除Step
	for _, az := range allAZs {
		builder.AddStep(saga.Step{
			Name:         fmt.Sprintf("删除PCCN-%s", az.ID),
			Type:         saga.StepTypeSync,
			ActionMethod: "DELETE",
			ActionURL:    fmt.Sprintf("%s/api/v1/pccn/%s", az.NSPAddr, pccnName),
		})
	}

	def, err := builder.Build()
	if err != nil {
		return &models.PCCNResponse{Success: false, Message: fmt.Sprintf("构建Saga定义失败: %v", err)}, nil
	}

	// 4. 提交Saga事务
	txID, err := o.sagaEngine.Submit(ctx, def)
	if err != nil {
		return &models.PCCNResponse{Success: false, Message: fmt.Sprintf("提交Saga事务失败: %v", err)}, nil
	}

	// 5. 更新状态
	o.pccnDAO.UpdatePCCNStatus(ctx, pccnName, "deleting", nil)

	// 6. 启动后台监听
	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		o.watchPCCNDeletion(txID, pccnName)
	}()

	return &models.PCCNResponse{
		Success: true,
		Message: fmt.Sprintf("PCCN删除任务已提交，事务ID: %s", txID),
		PCCNID:  pccn.ID,
		TxID:    txID,
	}, nil
}

// getAZsFromRegionAndVPC 从指定Region的VPC获取AZ列表
func (o *Orchestrator) getAZsFromRegionAndVPC(ctx context.Context, region, vpcName string) []*models.AZ {
	vpc, err := o.topDAO.GetVPCByName(ctx, vpcName)
	if err != nil {
		return nil
	}

	var azs []*models.AZ
	for azID := range vpc.AZDetails {
		az, err := o.registry.GetAZ(ctx, region, azID)
		if err != nil {
			continue
		}
		azs = append(azs, az)
	}
	return azs
}

// watchPCCNDeletion 监听PCCN删除完成
func (o *Orchestrator) watchPCCNDeletion(txID, pccnName string) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	timeout := time.After(2 * time.Minute)

	for {
		select {
		case <-o.ctx.Done():
			return
		case <-timeout:
			logger.Info("PCCN删除等待超时", "tx_id", txID)
			return
		case <-ticker.C:
			status, err := o.sagaEngine.Query(o.ctx, txID)
			if err != nil || status == nil {
				continue
			}
			switch saga.TxStatus(status.Status) {
			case saga.TxStatusSucceeded:
				dbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				o.pccnDAO.DeletePCCN(dbCtx, pccnName)
				cancel()
				logger.Info("PCCN删除成功", "pccn_name", pccnName)
				return
			case saga.TxStatusFailed:
				dbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				o.pccnDAO.UpdatePCCNStatus(dbCtx, pccnName, "failed", nil)
				cancel()
				return
			}
		}
	}
}

// HasPCCNDAO 检查PCCN DAO是否可用
func (o *Orchestrator) HasPCCNDAO() bool {
	return o.pccnDAO != nil
}

// ListPCCNs 列出所有PCCN
func (o *Orchestrator) ListPCCNs(ctx context.Context) ([]*models.PCCNRegistry, error) {
	if o.pccnDAO == nil {
		return nil, fmt.Errorf("PCCN DAO not initialized")
	}
	return o.pccnDAO.ListAllPCCNs(ctx)
}
