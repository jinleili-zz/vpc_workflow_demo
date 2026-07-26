package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"workflow_qoder/internal/client"
	"workflow_qoder/internal/models"
	"workflow_qoder/internal/operation"
	pccndao "workflow_qoder/internal/top/pccn/dao"
	topreconciler "workflow_qoder/internal/top/reconciler"
	"workflow_qoder/internal/top/registry"
	"workflow_qoder/internal/top/sagaonce"
	topdao "workflow_qoder/internal/top/vpc/dao"

	"github.com/google/uuid"
	"github.com/jinleili-zz/nsp-platform/auth"
	"github.com/jinleili-zz/nsp-platform/logger"
	"github.com/jinleili-zz/nsp-platform/saga"
	"github.com/jinleili-zz/nsp-platform/trace"
)

const topNSPAccessKey = "top-nsp"

const topDispatchLease = 30 * time.Second

type Orchestrator struct {
	ctx              context.Context // 长生命周期 context，用于后台 goroutine
	registry         *registry.Registry
	azClient         *client.AZNSPClient // 保留，用于健康检查和状态查询
	topDAO           *topdao.TopVPCDAO
	pccnDAO          *pccndao.TopPCCNDAO // PCCN DAO
	sagaEngine       *saga.Engine
	operationService *operation.Service
	sagaOnce         *sagaonce.Service
	tracedHTTP       *trace.TracedClient
	wg               sync.WaitGroup
	activeWatchers   sync.Map
	reconcilerOnce   sync.Once
	executionRepo    *topreconciler.Repository
	executionRunner  *topreconciler.Reconciler
}

func NewOrchestrator(ctx context.Context, registry *registry.Registry, topDB *sql.DB, sagaEngine *saga.Engine, tracedHTTP *trace.TracedClient, signer *auth.Signer) *Orchestrator {
	var dao *topdao.TopVPCDAO
	if topDB != nil {
		dao = topdao.NewTopVPCDAO(topDB)
	}

	// Create AZ client with trace support
	var azClient *client.AZNSPClient
	if tracedHTTP != nil {
		azClient = client.NewAZNSPClientWithTrace(tracedHTTP, tracedHTTP.Client(), signer)
	} else {
		azClient = client.NewAZNSPClient(nil, signer)
	}

	// Initialize PCCN DAO
	var pccnDAO *pccndao.TopPCCNDAO
	var operationService *operation.Service
	var sagaOnce *sagaonce.Service
	var executionRepo *topreconciler.Repository
	if topDB != nil {
		pccnDAO = pccndao.NewTopPCCNDAO(topDB)
		operationService = operation.NewService(operation.NewRepository(topDB))
		executionRepo = topreconciler.NewRepository(topDB, "top-nsp-vpc")
		if sagaEngine != nil {
			sagaOnce = sagaonce.NewService(topDB, sagaEngine)
		}
	}

	orchestrator := &Orchestrator{
		ctx:              ctx,
		registry:         registry,
		azClient:         azClient,
		topDAO:           dao,
		pccnDAO:          pccnDAO,
		sagaEngine:       sagaEngine,
		operationService: operationService,
		sagaOnce:         sagaOnce,
		tracedHTTP:       tracedHTTP,
		executionRepo:    executionRepo,
	}
	if executionRepo != nil {
		orchestrator.executionRunner = topreconciler.New(executionRepo, "top-nsp-vpc-"+uuid.NewString(), orchestrator.pollAZOperation, nil)
	}
	return orchestrator
}

func (o *Orchestrator) beginTopOperation(ctx context.Context, routeScope, operationType, targetScope, resourceType string, payload any) (*operation.Operation, operation.Decision, error) {
	if o.operationService == nil {
		return nil, "", fmt.Errorf("Top operation service is not initialized")
	}
	identity, ok := operation.IdentityFromContext(ctx)
	if !ok || identity.IdempotencyKey == "" {
		return nil, "", fmt.Errorf("%w: Idempotency-Key is required", operation.ErrInvalidIdempotencyKey)
	}
	op, decision, err := o.operationService.BeginTarget(ctx, operation.BeginRequest{
		OwnerService:   "top-nsp-vpc",
		CallerScope:    "northbound:compat",
		RouteScope:     routeScope,
		OperationType:  operationType,
		TargetScope:    targetScope,
		IdempotencyKey: identity.IdempotencyKey,
		Payload:        payload,
		ResourceType:   resourceType,
		ResourceID:     uuid.NewString(),
		Generation:     1,
	})
	if err != nil {
		return nil, "", err
	}
	if decision == operation.DecisionConflict {
		return op, decision, fmt.Errorf("%w: key is already associated with another request", operation.ErrIdempotencyKeyReused)
	}
	if decision == operation.DecisionResourceConflict {
		return op, decision, fmt.Errorf("%w: target resource already exists with another specification", operation.ErrResourceSpecConflict)
	}
	if decision == operation.DecisionResourceBusy {
		return op, decision, fmt.Errorf("%w: target resource deletion is in progress", operation.ErrResourceOperationInProgress)
	}
	return op, decision, nil
}

func topOperationContext(ctx context.Context, op *operation.Operation) context.Context {
	return operation.ContextWithIdentity(ctx, operation.RequestIdentity{
		IdempotencyKey:     op.IdempotencyKey,
		RootOperationID:    op.RootOperationID,
		ParentOperationID:  op.OperationID,
		ResourceGeneration: op.Generation,
	})
}

func replayVPCResponse(op *operation.Operation) *models.VPCResponse {
	if len(op.ResponsePayload) > 0 {
		var response models.VPCResponse
		if json.Unmarshal(op.ResponsePayload, &response) == nil {
			return &response
		}
	}
	return &models.VPCResponse{
		Code:        "0",
		Success:     true,
		Message:     "VPC operation already accepted",
		OperationID: op.OperationID,
		ResourceID:  op.ResourceID,
		VPCID:       op.ResourceID,
		Status:      string(op.Status),
	}
}

func (o *Orchestrator) failVPCOperation(ctx context.Context, op *operation.Operation, message string) (*models.VPCResponse, error) {
	response := &models.VPCResponse{
		Code:        "OPERATION_FAILED",
		Success:     false,
		Message:     message,
		OperationID: op.OperationID,
		ResourceID:  op.ResourceID,
		VPCID:       op.ResourceID,
		Status:      string(operation.StatusFailed),
	}
	stored, err := o.operationService.StoreResponseAndReleaseTarget(ctx, op.OperationID, operation.StatusFailed, response.Code, response)
	if err != nil {
		return nil, fmt.Errorf("store failed VPC operation: %w", err)
	}
	if !stored {
		return nil, fmt.Errorf("failed VPC operation was concurrently changed")
	}
	return response, nil
}

// =====================================================
// VPC Methods
// =====================================================

// CreateRegionVPC 创建Region级VPC（使用SAGA模式实现分布式事务）
func (o *Orchestrator) CreateRegionVPC(ctx context.Context, req *models.VPCRequest) (*models.VPCResponse, error) {
	logger.InfoContext(ctx, "开始创建Region级VPC", "vpc_name", req.VPCName, "region", req.Region)
	op, decision, err := o.beginTopOperation(ctx, "POST /api/v1/vpc", "create_vpc", vpcTargetScope(req), "vpc", req)
	if err != nil {
		return nil, err
	}
	if decision != operation.DecisionNew && len(op.ResponsePayload) > 0 {
		return replayVPCResponse(op), nil
	}
	lease, claimed, err := o.operationService.ClaimDispatch(ctx, op.OperationID, topDispatchLease)
	if err != nil {
		return nil, err
	}
	if !claimed {
		return replayVPCResponse(op), nil
	}
	defer lease.Close()
	ctx = lease.Context(ctx)
	op.Status = operation.StatusDispatching
	op.Version++
	ctx = topOperationContext(ctx, op)
	if o.sagaOnce == nil {
		return o.failVPCOperation(ctx, op, "Saga幂等提交服务未初始化")
	}
	externalKey := "top-operation:" + op.OperationID + ":create-vpc"
	if existing, found, resolveErr := o.sagaOnce.ResolveExisting(ctx, externalKey, op.OperationID); resolveErr != nil {
		return nil, resolveErr
	} else if found {
		azs := o.refreshAZAddresses(ctx, vpcAZsFromDefinition(req.Region, existing.Definition))
		return o.finishSubmittedVPC(ctx, op, req, existing.TransactionID, azs)
	}

	// 1. 获取Region下的所有AZ
	azs, err := o.registry.GetRegionAZs(ctx, req.Region)
	if err != nil {
		return o.failVPCOperation(ctx, op, fmt.Sprintf("获取Region的AZ失败: %v", err))
	}

	// 2. 预检查阶段
	for _, az := range azs {
		if err := o.azClient.HealthCheck(ctx, az.NSPAddr); err != nil {
			return o.failVPCOperation(ctx, op, fmt.Sprintf("预检查失败: AZ %s 不健康", az.ID))
		}
	}

	// 3. 统一生成 VPC ID（Top 层和 AZ 层使用同一个 ID）
	vpcID := op.ResourceID

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
		payloadMap["_target_region"] = az.Region
		payloadMap["_target_az"] = az.ID
		addSagaOperationIdentity(payloadMap, op)
		builder.AddStep(saga.Step{
			Name:             fmt.Sprintf("创建VPC-%s", az.ID),
			Type:             saga.StepTypeSync,
			ActionMethod:     "POST",
			ActionURL:        fmt.Sprintf("%s/api/v1/vpc", az.NSPAddr),
			ActionPayload:    payloadMap,
			AuthAK:           topNSPAccessKey,
			CompensateMethod: "DELETE",
			CompensateURL:    fmt.Sprintf("%s/api/v1/vpc/%s", az.NSPAddr, req.VPCName),
		})
	}

	def, err := builder.Build()
	if err != nil {
		return o.failVPCOperation(ctx, op, fmt.Sprintf("构建SAGA定义失败: %v", err))
	}

	// 4. 提交 SAGA 事务
	submission, err := o.sagaOnce.SubmitOnceResolved(ctx, externalKey, op.OperationID, def)
	if err != nil {
		return nil, fmt.Errorf("submit recoverable VPC Saga: %w", err)
	}
	txID := submission.TransactionID
	azs = vpcAZsFromDefinition(req.Region, submission.Definition)
	azs = o.refreshAZAddresses(ctx, azs)

	logger.InfoContext(ctx, "SAGA事务已提交", "transaction_id", txID, "vpc_name", req.VPCName)
	return o.finishSubmittedVPC(ctx, op, req, txID, azs)
}

func (o *Orchestrator) finishSubmittedVPC(ctx context.Context, op *operation.Operation, req *models.VPCRequest, txID string, azs []*models.AZ) (*models.VPCResponse, error) {
	vpcID := op.ResourceID
	// 5. Persist topology after SubmitOnce. A failure leaves the operation
	// dispatching so the reconciler can retry both this idempotent upsert and
	// the submit-once lookup without launching another Saga.
	if o.topDAO == nil {
		return nil, fmt.Errorf("Top VPC DAO is not initialized")
	}
	azDetails := make(map[string]models.AZDetail)
	for _, az := range azs {
		azDetails[az.ID] = models.AZDetail{Status: "creating"}
	}
	vpcReg := &models.VPCRegistry{
		ID: vpcID, VPCName: req.VPCName, Region: req.Region,
		VRFName: req.VRFName, VLANId: req.VLANId, FirewallZone: req.FirewallZone,
		Status: "creating", SagaTxID: txID, AZDetails: azDetails,
	}
	if err := o.topDAO.RegisterVPC(ctx, vpcReg); err != nil {
		return nil, fmt.Errorf("persist submitted VPC topology for recovery: %w", err)
	}

	// 6. 启动后台 goroutine 监听 SAGA 事务状态
	response := &models.VPCResponse{
		Code:        "0",
		Success:     true,
		Message:     fmt.Sprintf("VPC创建任务已提交，事务ID: %s", txID),
		OperationID: op.OperationID,
		ResourceID:  vpcID,
		Status:      string(operation.StatusRunning),
		VPCID:       vpcID,
		WorkflowID:  txID,
	}
	stored, err := o.operationService.StoreResponse(ctx, op.OperationID, operation.StatusRunning, "0", response)
	if err != nil {
		return nil, fmt.Errorf("store VPC operation response: %w", err)
	}
	if !stored {
		return nil, fmt.Errorf("VPC operation response was concurrently changed")
	}
	o.startVPCWatcher(op.OperationID, txID, req.VPCName, azs)
	return response, nil
}

func addSagaOperationIdentity(payload map[string]any, op *operation.Operation) {
	if payload == nil || op == nil {
		return
	}
	payload["_root_operation_id"] = op.RootOperationID
	payload["_parent_operation_id"] = op.OperationID
	payload["_resource_generation"] = op.Generation
}

func vpcAZsFromDefinition(region string, definition *saga.SagaDefinition) []*models.AZ {
	if definition == nil {
		return nil
	}
	result := make([]*models.AZ, 0, len(definition.Steps))
	for _, step := range definition.Steps {
		azID, _ := step.ActionPayload["_target_az"].(string)
		stepRegion, _ := step.ActionPayload["_target_region"].(string)
		if stepRegion == "" {
			stepRegion = region
		}
		if azID == "" {
			azID = strings.TrimPrefix(step.Name, "创建VPC-")
		}
		if azID == step.Name || azID == "" {
			continue
		}
		result = append(result, &models.AZ{ID: azID, Region: stepRegion, NSPAddr: strings.TrimSuffix(step.ActionURL, "/api/v1/vpc")})
	}
	return uniqueSortedAZs(result)
}

func (o *Orchestrator) refreshAZAddresses(ctx context.Context, azs []*models.AZ) []*models.AZ {
	if o.registry == nil {
		return azs
	}
	for _, snapshot := range azs {
		if current, err := o.registry.GetAZ(ctx, snapshot.Region, snapshot.ID); err == nil && current.NSPAddr != "" {
			snapshot.NSPAddr = current.NSPAddr
		}
	}
	return azs
}

// QuerySagaTransaction 查询SAGA事务状态
func (o *Orchestrator) QuerySagaTransaction(ctx context.Context, txID string) (*saga.TransactionStatus, error) {
	return o.sagaEngine.Query(ctx, txID)
}

func (o *Orchestrator) GetOperation(ctx context.Context, operationID string) (*operation.Operation, error) {
	if o.operationService == nil {
		return nil, fmt.Errorf("Top operation service is not initialized")
	}
	return o.operationService.Get(ctx, operationID)
}

func (o *Orchestrator) StartReconciler(interval time.Duration) {
	if interval <= 0 || o.operationService == nil {
		return
	}
	o.reconcilerOnce.Do(func() {
		o.wg.Add(1)
		go func() {
			defer o.wg.Done()
			o.reconcileOnce()
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-o.ctx.Done():
					return
				case <-ticker.C:
					o.reconcileOnce()
				}
			}
		}()
	})
}

func (o *Orchestrator) reconcileOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dispatches, err := o.operationService.ListRecoverableDispatch(ctx, "top-nsp-vpc", 100)
	if err == nil {
		for _, op := range dispatches {
			o.resumeDispatch(ctx, op)
		}
	}
	if o.executionRunner != nil {
		_, _ = o.executionRunner.RunOnce(ctx)
	}
	running, err := o.operationService.ListByStatus(ctx, "top-nsp-vpc", operation.StatusRunning, 200)
	if err == nil {
		for _, op := range running {
			o.resumeWatcher(ctx, op)
		}
	}
}

func (o *Orchestrator) pollAZOperation(ctx context.Context, execution topreconciler.Execution) (topreconciler.ChildResult, error) {
	az, err := o.registry.GetAZ(ctx, execution.Region, execution.AZ)
	if err != nil {
		return topreconciler.ChildResult{}, err
	}
	child, err := o.azClient.GetOperation(ctx, az.NSPAddr, execution.ChildOperationID)
	if err != nil {
		return topreconciler.ChildResult{}, err
	}
	status := child.Status
	if status == operation.StatusAccepted || status == operation.StatusDispatching {
		status = operation.StatusRunning
	}
	return topreconciler.ChildResult{Status: status, ErrorCode: child.ErrorCode, ErrorMessage: child.ErrorMessage}, nil
}

func (o *Orchestrator) resumeDispatch(ctx context.Context, op *operation.Operation) {
	ctx = topOperationContext(ctx, op)
	switch op.OperationType {
	case "create_vpc":
		var request models.VPCRequest
		if json.Unmarshal(op.RequestPayload, &request) == nil {
			_, _ = o.CreateRegionVPC(ctx, &request)
			return
		}
	case "create_subnet":
		var request models.SubnetRequest
		if json.Unmarshal(op.RequestPayload, &request) == nil {
			if _, err := o.registry.GetAZ(ctx, request.Region, request.AZ); err == nil {
				_, _ = o.CreateAZSubnet(ctx, &request)
				return
			}
		}
	case "create_pccn":
		var request models.PCCNRequest
		if json.Unmarshal(op.RequestPayload, &request) == nil {
			_, _ = o.CreatePCCN(ctx, &request)
			return
		}
	}
	_ = o.operationService.DeferDispatch(ctx, op.OperationID)
}

func (o *Orchestrator) resumeWatcher(ctx context.Context, op *operation.Operation) {
	defer func() { _ = o.operationService.DeferStatus(ctx, op.OperationID, operation.StatusRunning) }()
	if _, active := o.activeWatchers.Load(op.OperationID); active {
		return
	}
	if o.sagaOnce == nil {
		return
	}
	switch op.OperationType {
	case "create_vpc":
		var request models.VPCRequest
		var response models.VPCResponse
		if json.Unmarshal(op.RequestPayload, &request) != nil || json.Unmarshal(op.ResponsePayload, &response) != nil || response.WorkflowID == "" {
			return
		}
		if existing, found, err := o.sagaOnce.ResolveExisting(ctx, "top-operation:"+op.OperationID+":create-vpc", op.OperationID); err == nil && found {
			azs := o.refreshAZAddresses(ctx, vpcAZsFromDefinition(request.Region, existing.Definition))
			o.startVPCWatcher(op.OperationID, response.WorkflowID, request.VPCName, azs)
		}
	case "create_pccn":
		var request models.PCCNRequest
		var response models.PCCNResponse
		if json.Unmarshal(op.RequestPayload, &request) != nil || json.Unmarshal(op.ResponsePayload, &response) != nil || response.TxID == "" {
			return
		}
		if existing, found, err := o.sagaOnce.ResolveExisting(ctx, "top-operation:"+op.OperationID+":create-pccn", op.OperationID); err == nil && found {
			allAZs := o.refreshAZAddresses(ctx, pccnAZsFromDefinition(existing.Definition, nil, nil))
			o.startPCCNWatcher(op.OperationID, response.TxID, request.PCCNName, allAZs, request.VPC1, request.VPC2)
		}
	}
}

func uniqueSortedAZs(input []*models.AZ) []*models.AZ {
	seen := make(map[string]bool)
	result := make([]*models.AZ, 0, len(input))
	for _, az := range input {
		key := az.Region + "/" + az.ID
		if !seen[key] {
			seen[key] = true
			result = append(result, az)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Region == result[j].Region {
			return result[i].ID < result[j].ID
		}
		return result[i].Region < result[j].Region
	})
	return result
}

// CreateAZSubnet 创建AZ级子网（路由到指定AZ）
func (o *Orchestrator) CreateAZSubnet(ctx context.Context, req *models.SubnetRequest) (*models.SubnetResponse, error) {
	logger.InfoContext(ctx, "开始创建AZ级子网", "subnet_name", req.SubnetName, "region", req.Region, "az", req.AZ)
	op, decision, err := o.beginTopOperation(ctx, "POST /api/v1/subnet", "create_subnet", subnetTargetScope(req), "subnet", req)
	if err != nil {
		return nil, err
	}
	if decision != operation.DecisionNew && len(op.ResponsePayload) > 0 {
		return replaySubnetResponse(op), nil
	}
	lease, claimed, err := o.operationService.ClaimDispatch(ctx, op.OperationID, topDispatchLease)
	if err != nil {
		return nil, err
	}
	if !claimed {
		return replaySubnetResponse(op), nil
	}
	defer lease.Close()
	ctx = lease.Context(ctx)
	op.Status = operation.StatusDispatching
	op.Version++
	ctx = topOperationContext(ctx, op)
	parentVPC, err := o.topDAO.GetVPCByName(ctx, req.VPCName)
	if err != nil {
		return o.failSubnetOperationAndRelease(ctx, op, fmt.Sprintf("查询父VPC失败: %v", err))
	}
	if parentVPC.Region != req.Region || parentVPC.Status != "running" {
		return o.failSubnetOperationAndRelease(ctx, op, fmt.Sprintf("父VPC %s 不可用于创建子网: region=%s status=%s", req.VPCName, parentVPC.Region, parentVPC.Status))
	}

	az, err := o.registry.GetAZ(ctx, req.Region, req.AZ)
	if err != nil {
		return o.failSubnetOperationAndRelease(ctx, op, fmt.Sprintf("获取AZ信息失败: %v", err))
	}

	healthy, err := o.registry.CheckAZHealth(ctx, req.Region, req.AZ)
	if err != nil || !healthy {
		return o.failSubnetOperationAndRelease(ctx, op, fmt.Sprintf("AZ %s 不可用", req.AZ))
	}

	logger.InfoContext(ctx, "向AZ发送子网创建请求", "az_id", az.ID)
	childIdentity, err := operation.DeriveChildIdentity(ctx, "POST /api/v1/subnet", fmt.Sprintf("%s/%s/%s", req.Region, req.AZ, req.SubnetName), req)
	if err != nil {
		return nil, fmt.Errorf("derive subnet operation identity: %w", err)
	}
	childCtx := operation.ContextWithIdentity(ctx, childIdentity)
	resp, err := o.azClient.CreateSubnet(childCtx, az.NSPAddr, req)
	if err != nil {
		return nil, fmt.Errorf("submit recoverable subnet child operation: %w", err)
	}
	if !resp.Success {
		return o.failSubnetOperation(ctx, op, resp.Message)
	}
	if o.executionRepo == nil {
		return nil, fmt.Errorf("Top execution repository is not initialized")
	}
	if resp.OperationID == "" {
		return nil, fmt.Errorf("AZ subnet response omitted child operation ID")
	}
	if err := o.executionRepo.RecordExecution(ctx, topreconciler.Execution{
		OperationID: op.OperationID, Region: req.Region, AZ: req.AZ,
		ChildOperationID: resp.OperationID,
	}); err != nil {
		return nil, fmt.Errorf("record subnet child operation: %w", err)
	}

	if resp.Success && o.topDAO != nil {
		var firewallZone string
		vpc, err := o.topDAO.GetVPCByName(ctx, req.VPCName)
		if err == nil && vpc != nil {
			firewallZone = vpc.FirewallZone
		}

		subnetReg := &models.SubnetRegistry{
			ID:           op.ResourceID,
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
			// The AZ child is already durable. Keep the parent dispatching so a
			// retry can idempotently restore local topology before reconciliation.
			return nil, fmt.Errorf("persist recoverable subnet topology: %w", err)
		} else {
			logger.InfoContext(ctx, "同步子网拓扑成功", "subnet_name", req.SubnetName, "az", req.AZ, "cidr", req.CIDR, "zone", firewallZone)
		}
	}
	resp.Code = "0"
	resp.OperationID = op.OperationID
	resp.ResourceID = op.ResourceID
	resp.Status = string(operation.StatusRunning)
	stored, err := o.operationService.StoreResponse(ctx, op.OperationID, operation.StatusRunning, "0", resp)
	if err != nil {
		return nil, fmt.Errorf("store subnet operation response: %w", err)
	}
	if !stored {
		return nil, fmt.Errorf("subnet operation response was concurrently changed")
	}
	return resp, nil
}

func replaySubnetResponse(op *operation.Operation) *models.SubnetResponse {
	if len(op.ResponsePayload) > 0 {
		var response models.SubnetResponse
		if json.Unmarshal(op.ResponsePayload, &response) == nil {
			return &response
		}
	}
	return &models.SubnetResponse{Code: "0", Success: true, Message: "Subnet operation already accepted", OperationID: op.OperationID, ResourceID: op.ResourceID, Status: string(op.Status)}
}

func (o *Orchestrator) failSubnetOperation(ctx context.Context, op *operation.Operation, message string) (*models.SubnetResponse, error) {
	response := &models.SubnetResponse{Code: "OPERATION_FAILED", Success: false, Message: message, OperationID: op.OperationID, ResourceID: op.ResourceID, Status: string(operation.StatusFailed)}
	stored, err := o.operationService.StoreResponse(ctx, op.OperationID, operation.StatusFailed, response.Code, response)
	if err != nil {
		return nil, fmt.Errorf("store failed subnet operation: %w", err)
	}
	if !stored {
		return nil, fmt.Errorf("failed subnet operation was concurrently changed")
	}
	return response, nil
}

func (o *Orchestrator) failSubnetOperationAndRelease(ctx context.Context, op *operation.Operation, message string) (*models.SubnetResponse, error) {
	response := &models.SubnetResponse{Code: "OPERATION_FAILED", Success: false, Message: message, OperationID: op.OperationID, ResourceID: op.ResourceID, Status: string(operation.StatusFailed)}
	stored, err := o.operationService.StoreResponseAndReleaseTarget(ctx, op.OperationID, operation.StatusFailed, response.Code, response)
	if err != nil {
		return nil, fmt.Errorf("store pre-dispatch failed subnet operation: %w", err)
	}
	if !stored {
		return nil, fmt.Errorf("failed subnet operation was concurrently changed")
	}
	return response, nil
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
func (o *Orchestrator) startVPCWatcher(operationID, txID, vpcName string, azs []*models.AZ) {
	if _, loaded := o.activeWatchers.LoadOrStore(operationID, struct{}{}); loaded {
		return
	}
	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		defer o.activeWatchers.Delete(operationID)
		o.watchSagaAndPollAZs(operationID, txID, vpcName, azs)
	}()
}

func (o *Orchestrator) watchSagaAndPollAZs(operationID, txID, vpcName string, azs []*models.AZ) {
	if o.topDAO == nil || o.sagaEngine == nil {
		return
	}

	// ========== 阶段 1: 等待 Saga 完成 ==========
	sagaStatus := o.waitForSagaCompletion(txID, vpcName, azs)
	if sagaStatus != saga.TxStatusSucceeded {
		if o.ctx.Err() != nil {
			return
		}
		// Saga 失败 = API 调用失败，引擎已自动补偿，直接标记
		o.markVPCFromSagaFailure(txID, vpcName, azs)
		o.finishVPCOperation(operationID, txID, vpcName, operation.StatusFailed, "VPC Saga failed")
		return
	}

	// ========== 阶段 2: Saga 成功后，Poll 各 AZ 的 Worker 状态 ==========
	overall := o.pollAZWorkerStatuses(vpcName, azs)
	if overall == "running" {
		o.finishVPCOperation(operationID, txID, vpcName, operation.StatusSucceeded, "VPC creation succeeded")
	} else if overall != "" {
		o.finishVPCOperation(operationID, txID, vpcName, operation.StatusFailed, "VPC worker execution failed")
	}
}

func (o *Orchestrator) finishVPCOperation(operationID, txID, vpcName string, status operation.Status, message string) {
	if o.operationService == nil || operationID == "" {
		return
	}
	code := "0"
	success := status == operation.StatusSucceeded
	if !success {
		code = "OPERATION_FAILED"
	}
	op, err := o.operationService.Get(context.Background(), operationID)
	if err != nil {
		return
	}
	response := &models.VPCResponse{Code: code, Success: success, Message: message, OperationID: operationID, ResourceID: op.ResourceID, VPCID: op.ResourceID, WorkflowID: txID, Status: string(status)}
	_, _ = o.operationService.StoreResponse(context.Background(), operationID, status, code, response)
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
func (o *Orchestrator) pollAZWorkerStatuses(vpcName string, azs []*models.AZ) string {
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
			return ""
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
			return "failed"
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
				vpc, err := o.topDAO.GetVPCByName(o.ctx, vpcName)
				if err != nil {
					return "failed"
				}
				return o.computeOverallStatus(vpc.AZDetails)
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

// GetVPCByID 从 Top 层数据库根据 ID 查询单个 VPC 记录
func (o *Orchestrator) GetVPCByID(ctx context.Context, id string) (*models.VPCRegistry, error) {
	if o.topDAO == nil {
		return nil, fmt.Errorf("topDAO is nil")
	}
	return o.topDAO.GetVPCByID(ctx, id)
}

// ListSubnetsByVPCID 从 Top 层数据库查询某 VPC 下的所有子网
func (o *Orchestrator) ListSubnetsByVPCID(ctx context.Context, vpcID string) ([]*models.SubnetRegistry, error) {
	if o.topDAO == nil {
		return nil, fmt.Errorf("topDAO is nil")
	}
	return o.topDAO.ListSubnetsByVPCID(ctx, vpcID)
}

// GetSubnetByID 从 Top 层数据库根据 ID 查询子网
func (o *Orchestrator) GetSubnetByID(ctx context.Context, id string) (*models.SubnetRegistry, error) {
	if o.topDAO == nil {
		return nil, fmt.Errorf("topDAO is nil")
	}
	return o.topDAO.GetSubnetByID(ctx, id)
}

// GetVPCByName 从 Top 层数据库查询单个 VPC 记录
func (o *Orchestrator) GetVPCByName(ctx context.Context, vpcName string) (*models.VPCRegistry, error) {
	if o.topDAO == nil {
		return nil, fmt.Errorf("topDAO is nil")
	}
	return o.topDAO.GetVPCByName(ctx, vpcName)
}

// DeleteRegionVPC ensures the VPC is absent from every participating AZ before
// retiring the Top topology and its create target claim. A retry repeats only
// ensure-absent calls, so an interrupted fanout converges safely.
func (o *Orchestrator) DeleteRegionVPC(ctx context.Context, vpc *models.VPCRegistry) error {
	if o.topDAO == nil || o.operationService == nil {
		return fmt.Errorf("Top VPC persistence is not initialized")
	}
	if vpc == nil {
		return fmt.Errorf("VPC is required")
	}
	if err := o.operationService.MarkTargetRetiring(ctx, "top-nsp-vpc", "vpc", vpc.VPCName); err != nil {
		return err
	}
	if err := o.topDAO.MarkVPCDeleting(ctx, vpc.VPCName); err != nil {
		return fmt.Errorf("mark VPC deleting: %w", err)
	}
	azIDs := make([]string, 0, len(vpc.AZDetails))
	for azID := range vpc.AZDetails {
		azIDs = append(azIDs, azID)
	}
	sort.Strings(azIDs)
	deletedDetails := make(map[string]models.AZDetail, len(vpc.AZDetails))
	for azID, detail := range vpc.AZDetails {
		deletedDetails[azID] = detail
	}
	for _, azID := range azIDs {
		az, err := o.registry.GetAZ(ctx, vpc.Region, azID)
		if err != nil {
			return fmt.Errorf("resolve AZ %s/%s for VPC deletion: %w", vpc.Region, azID, err)
		}
		if err := o.azClient.DeleteVPC(ctx, az.NSPAddr, vpc.VPCName); err != nil {
			return fmt.Errorf("ensure VPC absent in AZ %s/%s: %w", vpc.Region, azID, err)
		}
		detail := deletedDetails[azID]
		detail.Status = "deleted"
		detail.Error = ""
		deletedDetails[azID] = detail
	}
	return o.topDAO.MarkVPCDeletedAndReleaseTarget(ctx, vpc.VPCName, deletedDetails)
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
	if status == "deleted" {
		return o.topDAO.MarkVPCDeletedAndReleaseTarget(ctx, vpcName, azDetails)
	}
	return o.topDAO.UpdateVPCOverallStatus(ctx, vpcName, status, azDetails)
}

func (o *Orchestrator) ReleaseSubnetTarget(ctx context.Context, region, az, subnetName string) error {
	return o.operationService.ReleaseTarget(ctx, "top-nsp-vpc", "subnet", fmt.Sprintf("%s/%s", az, subnetName))
}

func (o *Orchestrator) DeleteSubnetTopologyAndReleaseTarget(ctx context.Context, az, subnetName string) error {
	if o.topDAO == nil {
		return fmt.Errorf("Top VPC DAO is not initialized")
	}
	return o.topDAO.DeleteSubnetAndReleaseTarget(ctx, subnetName, az)
}

func (o *Orchestrator) MarkSubnetDeleting(ctx context.Context, az, subnetName string) error {
	return o.operationService.MarkTargetRetiring(ctx, "top-nsp-vpc", "subnet", fmt.Sprintf("%s/%s", az, subnetName))
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
	op, decision, err := o.beginTopOperation(ctx, "POST /api/v1/pccn", "create_pccn", pccnTargetScope(req), "pccn", req)
	if err != nil {
		return nil, err
	}
	if decision != operation.DecisionNew && len(op.ResponsePayload) > 0 {
		return replayPCCNResponse(op), nil
	}
	lease, claimed, err := o.operationService.ClaimDispatch(ctx, op.OperationID, topDispatchLease)
	if err != nil {
		return nil, err
	}
	if !claimed {
		return replayPCCNResponse(op), nil
	}
	defer lease.Close()
	ctx = lease.Context(ctx)
	op.Status = operation.StatusDispatching
	op.Version++
	ctx = topOperationContext(ctx, op)
	if o.pccnDAO == nil {
		return o.failPCCNOperation(ctx, op, "PCCN DAO not initialized")
	}
	if o.topDAO == nil {
		return o.failPCCNOperation(ctx, op, "Top DAO not initialized")
	}
	if o.sagaOnce == nil {
		return o.failPCCNOperation(ctx, op, "Saga幂等提交服务未初始化")
	}
	externalKey := "top-operation:" + op.OperationID + ":create-pccn"
	if existing, found, resolveErr := o.sagaOnce.ResolveExisting(ctx, externalKey, op.OperationID); resolveErr != nil {
		return nil, resolveErr
	} else if found {
		vpc1AZIDs, vpc2AZIDs := pccnAZMembershipFromDefinition(existing.Definition)
		allAZs := o.refreshAZAddresses(ctx, pccnAZsFromDefinition(existing.Definition, nil, nil))
		return o.finishSubmittedPCCN(ctx, op, req, existing.TransactionID, allAZs, vpc1AZIDs, vpc2AZIDs)
	}

	logger.InfoContext(ctx, "开始创建PCCN连接",
		"pccn_name", req.PCCNName,
		"vpc1", fmt.Sprintf("%s/%s", req.VPC1.Region, req.VPC1.VPCName),
		"vpc2", fmt.Sprintf("%s/%s", req.VPC2.Region, req.VPC2.VPCName),
	)

	// 1. 预检查：验证两个VPC存在且状态正常
	vpc1, err := o.topDAO.GetVPCByName(ctx, req.VPC1.VPCName)
	if err != nil {
		return o.failPCCNOperation(ctx, op, fmt.Sprintf("VPC1不存在: %v", err))
	}
	if vpc1.Region != req.VPC1.Region {
		return o.failPCCNOperation(ctx, op, fmt.Sprintf("VPC1 Region不匹配: 请求=%s, 实际=%s", req.VPC1.Region, vpc1.Region))
	}
	if vpc1.Status != "running" {
		return o.failPCCNOperation(ctx, op, fmt.Sprintf("VPC1状态不是running: %s", vpc1.Status))
	}

	vpc2, err := o.topDAO.GetVPCByName(ctx, req.VPC2.VPCName)
	if err != nil {
		return o.failPCCNOperation(ctx, op, fmt.Sprintf("VPC2不存在: %v", err))
	}
	if vpc2.Region != req.VPC2.Region {
		return o.failPCCNOperation(ctx, op, fmt.Sprintf("VPC2 Region不匹配: 请求=%s, 实际=%s", req.VPC2.Region, vpc2.Region))
	}
	if vpc2.Status != "running" {
		return o.failPCCNOperation(ctx, op, fmt.Sprintf("VPC2状态不是running: %s", vpc2.Status))
	}

	// 2. 获取两个VPC涉及的AZ（可能跨Region），对相同AZ去重
	vpc1AZs := o.getAZsFromVPCDetails(ctx, vpc1)
	vpc2AZs := o.getAZsFromVPCDetails(ctx, vpc2)

	// 去重：vpc1 和 vpc2 可能共享同一批 AZ（同 Region），避免对同一 AZ 提交两次
	allAZsSeen := make(map[string]bool)
	allAZs := make([]*models.AZ, 0, len(vpc1AZs)+len(vpc2AZs))
	for _, az := range vpc1AZs {
		key := az.Region + "/" + az.ID
		if !allAZsSeen[key] {
			allAZsSeen[key] = true
			allAZs = append(allAZs, az)
		}
	}
	for _, az := range vpc2AZs {
		key := az.Region + "/" + az.ID
		if !allAZsSeen[key] {
			allAZsSeen[key] = true
			allAZs = append(allAZs, az)
		}
	}
	sort.Slice(allAZs, func(i, j int) bool {
		if allAZs[i].Region == allAZs[j].Region {
			return allAZs[i].ID < allAZs[j].ID
		}
		return allAZs[i].Region < allAZs[j].Region
	})

	// 3. 健康检查所有AZ（跨Region）
	for _, az := range allAZs {
		if err := o.azClient.HealthCheck(ctx, az.NSPAddr); err != nil {
			return o.failPCCNOperation(ctx, op, fmt.Sprintf("AZ %s 不健康", az.ID))
		}
	}

	// 4. 生成统一的PCCN ID
	pccnID := op.ResourceID

	// 5. 构建 Saga 事务（Sync Step：POST 返回 200 即为成功）
	// 参考 VPC 重构方案：Poll 在 Saga 之外，由业务层直接轮询
	builder := saga.NewSaga(fmt.Sprintf("pccn-create-%s", req.PCCNName)).
		WithPayload(map[string]any{
			"pccn_name":   req.PCCNName,
			"vpc1_name":   req.VPC1.VPCName,
			"vpc1_region": req.VPC1.Region,
			"vpc2_name":   req.VPC2.VPCName,
			"vpc2_region": req.VPC2.Region,
			"vpc1_azs":    sortedAZDetailIDs(vpc1),
			"vpc2_azs":    sortedAZDetailIDs(vpc2),
		}).
		WithTimeout(60) // 1 分钟超时，Sync 步骤只等 API 调用

	// 为每个去重后的AZ添加 Sync Step（包含 vpc1 和 vpc2 信息，AZ层自行判断角色）
	for _, az := range allAZs {
		azReq := &models.PCCNRequest{
			PCCNID:   pccnID,
			PCCNName: req.PCCNName,
			VPC1:     req.VPC1,
			VPC2:     req.VPC2,
		}
		payloadBytes, _ := json.Marshal(azReq)
		var payloadMap map[string]any
		json.Unmarshal(payloadBytes, &payloadMap)
		payloadMap["_target_region"] = az.Region
		payloadMap["_target_az"] = az.ID
		addSagaOperationIdentity(payloadMap, op)

		builder.AddStep(saga.Step{
			Name:             fmt.Sprintf("提交PCCN创建-%s", az.ID),
			Type:             saga.StepTypeSync,
			ActionMethod:     "POST",
			ActionURL:        fmt.Sprintf("%s/api/v1/pccn", az.NSPAddr),
			ActionPayload:    payloadMap,
			AuthAK:           topNSPAccessKey,
			CompensateMethod: "DELETE",
			CompensateURL:    fmt.Sprintf("%s/api/v1/pccn/%s", az.NSPAddr, req.PCCNName),
		})
	}

	def, err := builder.Build()
	if err != nil {
		return o.failPCCNOperation(ctx, op, fmt.Sprintf("构建Saga定义失败: %v", err))
	}

	// 6. 提交Saga事务
	submission, err := o.sagaOnce.SubmitOnceResolved(ctx, externalKey, op.OperationID, def)
	if err != nil {
		return nil, fmt.Errorf("submit recoverable PCCN Saga: %w", err)
	}
	txID := submission.TransactionID
	allAZs = pccnAZsFromDefinition(submission.Definition, vpc1, vpc2)
	allAZs = o.refreshAZAddresses(ctx, allAZs)

	logger.InfoContext(ctx, "Saga事务已提交", "transaction_id", txID, "pccn_name", req.PCCNName)
	return o.finishSubmittedPCCN(ctx, op, req, txID, allAZs, sortedAZDetailIDs(vpc1), sortedAZDetailIDs(vpc2))
}

func (o *Orchestrator) finishSubmittedPCCN(ctx context.Context, op *operation.Operation, req *models.PCCNRequest, txID string, allAZs []*models.AZ, vpc1AZIDs, vpc2AZIDs []string) (*models.PCCNResponse, error) {
	pccnID := op.ResourceID
	// 7. 预注册PCCN（包含跨Region信息）
	vpcDetails := make(map[string]models.VPCDetail)
	vpc1Key := fmt.Sprintf("%s/%s", req.VPC1.Region, req.VPC1.VPCName)
	vpc2Key := fmt.Sprintf("%s/%s", req.VPC2.Region, req.VPC2.VPCName)

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
		return nil, fmt.Errorf("persist submitted PCCN topology for recovery: %w", err)
	}

	response := &models.PCCNResponse{Code: "0", Success: true, Message: fmt.Sprintf("PCCN创建任务已提交，事务ID: %s", txID), OperationID: op.OperationID, ResourceID: pccnID, Status: string(operation.StatusRunning), PCCNID: pccnID, TxID: txID}
	stored, err := o.operationService.StoreResponse(ctx, op.OperationID, operation.StatusRunning, "0", response)
	if err != nil {
		return nil, fmt.Errorf("store PCCN operation response: %w", err)
	}
	if !stored {
		return nil, fmt.Errorf("PCCN operation response was concurrently changed")
	}
	o.startPCCNWatcher(op.OperationID, txID, req.PCCNName, allAZs, req.VPC1, req.VPC2)
	return response, nil
}

func pccnAZsFromDefinition(definition *saga.SagaDefinition, vpc1, vpc2 *models.VPCRegistry) []*models.AZ {
	if definition == nil {
		return nil
	}
	regionByAZ := make(map[string]string)
	if vpc1 != nil {
		for azID := range vpc1.AZDetails {
			regionByAZ[azID] = vpc1.Region
		}
	}
	if vpc2 != nil {
		for azID := range vpc2.AZDetails {
			if _, exists := regionByAZ[azID]; !exists {
				regionByAZ[azID] = vpc2.Region
			}
		}
	}
	result := make([]*models.AZ, 0, len(definition.Steps))
	for _, step := range definition.Steps {
		azID, _ := step.ActionPayload["_target_az"].(string)
		region, _ := step.ActionPayload["_target_region"].(string)
		if azID == "" {
			azID = strings.TrimPrefix(step.Name, "提交PCCN创建-")
		}
		if azID == step.Name || azID == "" {
			continue
		}
		if region == "" {
			region = regionByAZ[azID]
		}
		result = append(result, &models.AZ{ID: azID, Region: region, NSPAddr: strings.TrimSuffix(step.ActionURL, "/api/v1/pccn")})
	}
	return uniqueSortedAZs(result)
}

func pccnAZMembershipFromDefinition(definition *saga.SagaDefinition) ([]string, []string) {
	if definition == nil {
		return nil, nil
	}
	return stringSlice(definition.Payload["vpc1_azs"]), stringSlice(definition.Payload["vpc2_azs"])
}

func stringSlice(value any) []string {
	var result []string
	switch values := value.(type) {
	case []string:
		result = append(result, values...)
	case []any:
		for _, value := range values {
			if item, ok := value.(string); ok {
				result = append(result, item)
			}
		}
	}
	sort.Strings(result)
	return result
}

func sortedAZDetailIDs(vpc *models.VPCRegistry) []string {
	result := make([]string, 0, len(vpc.AZDetails))
	for azID := range vpc.AZDetails {
		result = append(result, azID)
	}
	sort.Strings(result)
	return result
}

func replayPCCNResponse(op *operation.Operation) *models.PCCNResponse {
	if len(op.ResponsePayload) > 0 {
		var response models.PCCNResponse
		if json.Unmarshal(op.ResponsePayload, &response) == nil {
			return &response
		}
	}
	return &models.PCCNResponse{Code: "0", Success: true, Message: "PCCN operation already accepted", OperationID: op.OperationID, ResourceID: op.ResourceID, PCCNID: op.ResourceID, Status: string(op.Status)}
}

func (o *Orchestrator) failPCCNOperation(ctx context.Context, op *operation.Operation, message string) (*models.PCCNResponse, error) {
	response := &models.PCCNResponse{Code: "OPERATION_FAILED", Success: false, Message: message, OperationID: op.OperationID, ResourceID: op.ResourceID, PCCNID: op.ResourceID, Status: string(operation.StatusFailed)}
	stored, err := o.operationService.StoreResponseAndReleaseTarget(ctx, op.OperationID, operation.StatusFailed, response.Code, response)
	if err != nil {
		return nil, fmt.Errorf("store failed PCCN operation: %w", err)
	}
	if !stored {
		return nil, fmt.Errorf("failed PCCN operation was concurrently changed")
	}
	return response, nil
}

// getAZsFromVPCDetails 从VPCRegistry的AZDetails中提取AZ列表
func (o *Orchestrator) getAZsFromVPCDetails(ctx context.Context, vpc *models.VPCRegistry) []*models.AZ {
	var azs []*models.AZ
	azIDs := make([]string, 0, len(vpc.AZDetails))
	for azID := range vpc.AZDetails {
		azIDs = append(azIDs, azID)
	}
	sort.Strings(azIDs)
	for _, azID := range azIDs {
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
func (o *Orchestrator) startPCCNWatcher(operationID, txID, pccnName string, allAZs []*models.AZ, vpc1, vpc2 models.VPCRef) {
	if _, loaded := o.activeWatchers.LoadOrStore(operationID, struct{}{}); loaded {
		return
	}
	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		defer o.activeWatchers.Delete(operationID)
		o.watchPCCNSagaAndPollAZs(operationID, txID, pccnName, allAZs, vpc1, vpc2)
	}()
}

func (o *Orchestrator) watchPCCNSagaAndPollAZs(operationID, txID, pccnName string, allAZs []*models.AZ, vpc1, vpc2 models.VPCRef) {
	if o.pccnDAO == nil || o.sagaEngine == nil {
		return
	}

	// ========== 阶段 1: 等待 Saga 完成 ==========
	sagaStatus := o.waitForPCCNSagaCompletion(txID, pccnName)
	if sagaStatus != saga.TxStatusSucceeded {
		if o.ctx.Err() != nil {
			return
		}
		// Saga 失败 = API 调用失败，引擎已自动补偿
		o.markPCCNFromSagaFailure(txID, pccnName)
		o.finishPCCNOperation(operationID, txID, operation.StatusFailed, "PCCN Saga failed")
		return
	}

	// ========== 阶段 2: Saga 成功后，Poll 各 AZ 的 Worker 状态 ==========
	overall := o.pollPCCNAZWorkerStatuses(pccnName, allAZs, vpc1, vpc2)
	if overall == "running" {
		o.finishPCCNOperation(operationID, txID, operation.StatusSucceeded, "PCCN creation succeeded")
	} else if overall != "" {
		o.finishPCCNOperation(operationID, txID, operation.StatusFailed, "PCCN worker execution failed")
	}
}

func (o *Orchestrator) finishPCCNOperation(operationID, txID string, status operation.Status, message string) {
	if o.operationService == nil || operationID == "" {
		return
	}
	code := "0"
	success := status == operation.StatusSucceeded
	if !success {
		code = "OPERATION_FAILED"
	}
	op, err := o.operationService.Get(context.Background(), operationID)
	if err != nil {
		return
	}
	response := &models.PCCNResponse{Code: code, Success: success, Message: message, OperationID: operationID, ResourceID: op.ResourceID, PCCNID: op.ResourceID, TxID: txID, Status: string(status)}
	_, _ = o.operationService.StoreResponse(context.Background(), operationID, status, code, response)
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
func (o *Orchestrator) pollPCCNAZWorkerStatuses(pccnName string, allAZs []*models.AZ, vpc1, vpc2 models.VPCRef) string {
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
			return ""
		case <-timeout:
			logger.Info("PCCN Worker状态轮询超时", "pccn_name", pccnName)
			dbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			o.pccnDAO.UpdatePCCNStatus(dbCtx, pccnName, "failed", nil)
			cancel()
			return "failed"
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
					// Preserve the immutable AZ target snapshot. Rebuilding
					// these values from only VPCRef used to erase AZs and made
					// a later fail-closed delete impossible.
					current, loadErr := o.pccnDAO.GetPCCNByName(dbCtx, pccnName)
					if loadErr != nil {
						logger.Error("加载PCCN目标快照失败", "pccn_name", pccnName, "error", loadErr)
						return ""
					}
					vpcDetails := runningPCCNDetails(current.VPCDetails, vpc1, vpc2, allAZs)
					o.pccnDAO.UpdatePCCNStatus(dbCtx, pccnName, "running", vpcDetails)
					logger.Info("PCCN 创建成功", "pccn_name", pccnName)
				}
				if hasFailed {
					return "partial_running"
				}
				return "running"
			}
		}
	}
}

func runningPCCNDetails(current map[string]models.VPCDetail, vpc1, vpc2 models.VPCRef, allAZs []*models.AZ) map[string]models.VPCDetail {
	result := make(map[string]models.VPCDetail, len(current)+2)
	for key, detail := range current {
		detail.AZs = append([]string(nil), detail.AZs...)
		detail.Subnets = append([]string(nil), detail.Subnets...)
		result[key] = detail
	}
	for _, ref := range []models.VPCRef{vpc1, vpc2} {
		key := fmt.Sprintf("%s/%s", ref.Region, ref.VPCName)
		detail := result[key]
		detail.Region = ref.Region
		detail.Status = "running"
		if len(detail.AZs) == 0 {
			for _, az := range allAZs {
				if az != nil && az.Region == ref.Region {
					detail.AZs = append(detail.AZs, az.ID)
				}
			}
			sort.Strings(detail.AZs)
		}
		result[key] = detail
	}
	return result
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
		if err == sql.ErrNoRows {
			return &models.PCCNResponse{Success: true, Message: "PCCN已不存在"}, nil
		}
		return nil, err
	}
	if pccn.Status != "running" && pccn.Status != "partial_running" && pccn.Status != "failed" && pccn.Status != "deleting" {
		return &models.PCCNResponse{Success: false, Message: fmt.Sprintf("PCCN状态不允许删除: %s", pccn.Status)}, nil
	}
	if err := o.operationService.MarkTargetRetiring(ctx, "top-nsp-vpc", "pccn", pccnName); err != nil {
		return nil, err
	}

	// 2. 获取涉及的AZ（跨Region），去重。任何目标解析失败都必须
	// fail closed，不能在遗漏 AZ 的情况下释放 Top claim。
	seenAZs := make(map[string]bool)
	allAZs := make([]*models.AZ, 0)
	for _, detail := range pccn.VPCDetails {
		for _, azID := range detail.AZs {
			key := detail.Region + "/" + azID
			if seenAZs[key] {
				continue
			}
			az, err := o.registry.GetAZ(ctx, detail.Region, azID)
			if err != nil {
				return nil, fmt.Errorf("resolve PCCN deletion AZ %s: %w", key, err)
			}
			seenAZs[key] = true
			allAZs = append(allAZs, az)
		}
	}
	sort.Slice(allAZs, func(i, j int) bool {
		if allAZs[i].Region == allAZs[j].Region {
			return allAZs[i].ID < allAZs[j].ID
		}
		return allAZs[i].Region < allAZs[j].Region
	})
	if len(allAZs) == 0 {
		return nil, fmt.Errorf("PCCN %s has no resolvable AZ targets", pccnName)
	}
	if err := o.pccnDAO.UpdatePCCNStatus(ctx, pccnName, "deleting", pccn.VPCDetails); err != nil {
		return nil, err
	}
	for _, az := range allAZs {
		if err := o.azClient.DeletePCCN(ctx, az.NSPAddr, pccnName); err != nil {
			return nil, fmt.Errorf("ensure PCCN absent in AZ %s/%s: %w", az.Region, az.ID, err)
		}
	}
	if err := o.pccnDAO.DeletePCCNAndReleaseTarget(ctx, pccnName); err != nil {
		return nil, err
	}
	return &models.PCCNResponse{Success: true, Message: "PCCN已删除", PCCNID: pccn.ID}, nil
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
				deleteErr := o.pccnDAO.DeletePCCNAndReleaseTarget(dbCtx, pccnName)
				cancel()
				if deleteErr == nil {
					logger.Info("PCCN删除成功", "pccn_name", pccnName)
					return
				}
				logger.Info("PCCN删除本地提交失败，将继续重试", "pccn_name", pccnName, "error", deleteErr)
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

// GetPCCNsByVPCName 获取与指定VPC相关的所有PCCN连接
func (o *Orchestrator) GetPCCNsByVPC(ctx context.Context, vpcName string) ([]*models.PCCNRegistry, error) {
	if o.pccnDAO == nil {
		return nil, fmt.Errorf("PCCN DAO not initialized")
	}
	return o.pccnDAO.GetPCCNsByVPCName(ctx, vpcName)
}
