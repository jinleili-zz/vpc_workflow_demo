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
