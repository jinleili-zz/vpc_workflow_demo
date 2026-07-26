package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jinleili-zz/nsp-platform/logger"
	"github.com/jinleili-zz/nsp-platform/taskqueue"
	"github.com/jinleili-zz/nsp-platform/trace"

	"workflow_qoder/internal/db/dao"
	"workflow_qoder/internal/models"
	"workflow_qoder/internal/operation"
	"workflow_qoder/internal/orchestration"
	"workflow_qoder/internal/queue"
)

const staleWorkflowTimeout = 5 * time.Minute

type AZOrchestrator struct {
	db           *sql.DB
	vpcDAO       *dao.VPCDAO
	subnetDAO    *dao.SubnetDAO
	pccnDAO      *dao.PCCNDAO
	taskDAO      *dao.TaskDAO
	workflowMgr  *orchestration.Manager
	broker       taskqueue.Broker
	inspector    taskqueue.Inspector
	tracedHTTP   *trace.TracedClient
	region       string
	az           string
	operationSvc *operation.Service
}

func NewAZOrchestrator(db *sql.DB, broker taskqueue.Broker, inspector taskqueue.Inspector, tracedHTTP *trace.TracedClient, region, az string) *AZOrchestrator {
	taskDAO := dao.NewTaskDAO(db)
	workflowMgr := orchestration.NewDurableManager(
		db,
		fmt.Sprintf("az-nsp-vpc-%s", az),
		broker,
		taskDAO,
		func(deviceType string, priority int) string {
			return queue.GetPriorityQueueName(region, az, queue.DeviceType(deviceType), queue.TaskPriority(priority))
		},
		queue.GetReplyQueueName(region, az, "vpc"),
	)
	workflowMgr.RegisterResourceStore(models.ResourceTypeVPC, dao.NewVPCDAO(db))
	workflowMgr.RegisterResourceStore(models.ResourceTypeSubnet, dao.NewSubnetDAO(db))
	workflowMgr.RegisterResourceStore(models.ResourceTypePCCN, dao.NewPCCNDAO(db))

	return &AZOrchestrator{
		db:           db,
		vpcDAO:       dao.NewVPCDAO(db),
		subnetDAO:    dao.NewSubnetDAO(db),
		pccnDAO:      dao.NewPCCNDAO(db),
		taskDAO:      taskDAO,
		workflowMgr:  workflowMgr,
		broker:       broker,
		inspector:    inspector,
		tracedHTTP:   tracedHTTP,
		region:       region,
		az:           az,
		operationSvc: operation.NewService(operation.NewRepository(db)),
	}
}

func (o *AZOrchestrator) HandleReplyTask(ctx context.Context, task *taskqueue.Task) error {
	return o.workflowMgr.HandleReply(ctx, task)
}

func (o *AZOrchestrator) ReplyQueueName() string {
	return o.workflowMgr.ReplyQueueName()
}

func (o *AZOrchestrator) StartOutboxDispatcher(ctx context.Context, interval time.Duration) {
	o.workflowMgr.StartOutboxDispatcher(ctx, interval)
}

func (o *AZOrchestrator) GetOperation(ctx context.Context, operationID string) (*operation.Operation, error) {
	return o.operationSvc.Get(ctx, operationID)
}

func (o *AZOrchestrator) CreateVPC(ctx context.Context, req *models.VPCRequest) (*models.VPCResponse, error) {
	logger.InfoContext(ctx, "开始创建VPC", "az", o.az, "vpcName", req.VPCName)

	vpcID := req.VPCID
	if vpcID == "" {
		vpcID = uuid.New().String()
	}

	vpcResource := &models.VPCResource{
		ID:             vpcID,
		VPCName:        req.VPCName,
		Region:         req.Region,
		AZ:             o.az,
		VRFName:        req.VRFName,
		VLANId:         req.VLANId,
		FirewallZone:   req.FirewallZone,
		Status:         models.ResourceStatusPending,
		TotalTasks:     0,
		CompletedTasks: 0,
		FailedTasks:    0,
	}

	params, err := o.buildVPCTaskParams(req)
	if err != nil {
		return &models.VPCResponse{
			Success: false,
			Message: fmt.Sprintf("序列化VPC任务参数失败: %v", err),
		}, nil
	}

	workflowID, persistedVPCID, err := o.workflowMgr.SubmitPreparedWorkflow(ctx, func(ctx context.Context, tx *sql.Tx) (orchestration.WorkflowDef, error) {
		op, decision, err := o.beginCreateOperationTx(ctx, tx, "POST /api/v1/vpc", "create_vpc", fmt.Sprintf("%s/%s/%s", o.region, o.az, req.VPCName), req, "vpc", vpcID)
		if err != nil {
			return orchestration.WorkflowDef{}, err
		}
		vpcID = op.ResourceID
		vpcResource.ID = vpcID
		if decision == operation.DecisionNew {
			if err := o.vpcDAO.CreateTx(ctx, tx, vpcResource); err != nil {
				return orchestration.WorkflowDef{}, err
			}
		}
		return orchestration.WorkflowDef{
			OperationID:       op.OperationID,
			RootOperationID:   op.RootOperationID,
			WorkflowID:        op.OperationID,
			Generation:        op.Generation,
			OperationRequired: true,
			ReplayExisting:    decision == operation.DecisionReplay,
			ResourceType:      models.ResourceTypeVPC,
			ResourceID:        vpcID,
			AZ:                o.az,
			Steps: []orchestration.WorkflowStep{
				{TaskType: "create_vrf_on_switch", TaskName: "创建VRF", DeviceType: string(queue.DeviceTypeSwitch), Priority: int(taskqueue.PriorityNormal), Payload: params},
				{TaskType: "create_vlan_subinterface", TaskName: "创建VLAN子接口", DeviceType: string(queue.DeviceTypeSwitch), Priority: int(taskqueue.PriorityNormal), Payload: params},
				{TaskType: "create_firewall_zone", TaskName: "创建防火墙安全区域", DeviceType: string(queue.DeviceTypeFirewall), Priority: int(taskqueue.PriorityNormal), Payload: params},
			},
		}, nil
	})
	if err != nil {
		return nil, fmt.Errorf("提交VPC工作流: %w", err)
	}
	vpcID = persistedVPCID

	logger.InfoContext(ctx, "VPC创建流程启动成功", "az", o.az, "vpcName", req.VPCName, "vpcID", vpcID, "workflowID", workflowID)

	return &models.VPCResponse{
		Success:     true,
		Message:     "VPC创建工作流已启动",
		ResourceID:  vpcID,
		Status:      "accepted",
		VPCID:       vpcID,
		WorkflowID:  workflowID,
		OperationID: workflowID,
	}, nil
}

func (o *AZOrchestrator) buildVPCTaskParams(req *models.VPCRequest) ([]byte, error) {
	return json.Marshal(map[string]any{
		"vpc_name":      req.VPCName,
		"vrf_name":      req.VRFName,
		"vlan_id":       req.VLANId,
		"firewall_zone": req.FirewallZone,
		"region":        req.Region,
	})
}

func (o *AZOrchestrator) CreateSubnet(ctx context.Context, req *models.SubnetRequest) (*models.SubnetResponse, error) {
	logger.InfoContext(ctx, "开始创建子网", "az", o.az, "subnetName", req.SubnetName)

	subnetID := uuid.New().String()
	subnetResource := &models.SubnetResource{
		ID:             subnetID,
		SubnetName:     req.SubnetName,
		VPCName:        req.VPCName,
		Region:         req.Region,
		AZ:             o.az,
		CIDR:           req.CIDR,
		Status:         models.ResourceStatusPending,
		TotalTasks:     0,
		CompletedTasks: 0,
		FailedTasks:    0,
	}

	params, err := o.buildSubnetTaskParams(req)
	if err != nil {
		return &models.SubnetResponse{
			Success: false,
			Message: fmt.Sprintf("序列化子网任务参数失败: %v", err),
		}, nil
	}

	workflowID, persistedSubnetID, err := o.workflowMgr.SubmitPreparedWorkflow(ctx, func(ctx context.Context, tx *sql.Tx) (orchestration.WorkflowDef, error) {
		var parentStatus models.ResourceStatus
		if err := tx.QueryRowContext(ctx, `
			SELECT status FROM vpc_resources
			WHERE vpc_name = $1 AND az = $2
			FOR UPDATE
		`, req.VPCName, o.az).Scan(&parentStatus); err != nil {
			if err == sql.ErrNoRows {
				return orchestration.WorkflowDef{}, fmt.Errorf("parent VPC %s does not exist in AZ %s", req.VPCName, o.az)
			}
			return orchestration.WorkflowDef{}, err
		}
		if parentStatus != models.ResourceStatusRunning {
			return orchestration.WorkflowDef{}, fmt.Errorf("parent VPC %s is %s, want running", req.VPCName, parentStatus)
		}
		op, decision, err := o.beginCreateOperationTx(ctx, tx, "POST /api/v1/subnet", "create_subnet", fmt.Sprintf("%s/%s/%s", o.region, o.az, req.SubnetName), req, "subnet", subnetID)
		if err != nil {
			return orchestration.WorkflowDef{}, err
		}
		subnetID = op.ResourceID
		subnetResource.ID = subnetID
		if decision == operation.DecisionNew {
			if err := o.subnetDAO.CreateTx(ctx, tx, subnetResource); err != nil {
				return orchestration.WorkflowDef{}, err
			}
		}
		return orchestration.WorkflowDef{
			OperationID:       op.OperationID,
			RootOperationID:   op.RootOperationID,
			WorkflowID:        op.OperationID,
			Generation:        op.Generation,
			OperationRequired: true,
			ReplayExisting:    decision == operation.DecisionReplay,
			ResourceType:      models.ResourceTypeSubnet,
			ResourceID:        subnetID,
			AZ:                o.az,
			Steps: []orchestration.WorkflowStep{
				{TaskType: "create_subnet_on_switch", TaskName: "创建子网", DeviceType: string(queue.DeviceTypeSwitch), Priority: int(taskqueue.PriorityNormal), Payload: params},
				{TaskType: "configure_subnet_routing", TaskName: "配置子网路由", DeviceType: string(queue.DeviceTypeSwitch), Priority: int(taskqueue.PriorityNormal), Payload: params},
			},
		}, nil
	})
	if err != nil {
		return nil, fmt.Errorf("提交子网工作流: %w", err)
	}
	subnetID = persistedSubnetID

	logger.InfoContext(ctx, "子网创建流程启动成功", "az", o.az, "subnetName", req.SubnetName, "subnetID", subnetID, "workflowID", workflowID)

	return &models.SubnetResponse{
		Success:     true,
		Message:     "子网创建工作流已启动",
		ResourceID:  subnetID,
		Status:      "accepted",
		SubnetID:    subnetID,
		WorkflowID:  workflowID,
		OperationID: workflowID,
	}, nil
}

func (o *AZOrchestrator) beginCreateOperationTx(ctx context.Context, tx *sql.Tx, route, operationType, target string, payload any, resourceType, candidateResourceID string) (*operation.Operation, operation.Decision, error) {
	identity, _ := operation.IdentityFromContext(ctx)
	if identity.IdempotencyKey == "" {
		return nil, "", fmt.Errorf("%w: X-Idempotency-Key is required", operation.ErrInvalidIdempotencyKey)
	}
	rootOperationID := identity.RootOperationID
	if rootOperationID == "" {
		rootOperationID = identity.SagaTransactionID
	}
	generation := identity.ResourceGeneration
	if generation == 0 {
		generation = 1
	}
	op, decision, err := o.operationSvc.BeginTargetTx(ctx, tx, operation.BeginRequest{
		RootOperationID:   rootOperationID,
		ParentOperationID: identity.ParentOperationID,
		OwnerService:      fmt.Sprintf("az-nsp-vpc-%s", o.az),
		CallerScope:       "top-nsp-vpc",
		RouteScope:        route,
		OperationType:     operationType,
		TargetScope:       target,
		IdempotencyKey:    identity.IdempotencyKey,
		Payload:           payload,
		ResourceType:      resourceType,
		ResourceID:        candidateResourceID,
		Generation:        generation,
	})
	if err != nil {
		return nil, "", err
	}
	if decision == operation.DecisionConflict {
		return nil, decision, operation.ErrIdempotencyKeyReused
	}
	if decision == operation.DecisionResourceConflict {
		return nil, decision, operation.ErrResourceSpecConflict
	}
	if decision == operation.DecisionResourceBusy {
		return nil, decision, operation.ErrResourceOperationInProgress
	}
	return op, decision, nil
}

func (o *AZOrchestrator) buildSubnetTaskParams(req *models.SubnetRequest) ([]byte, error) {
	return json.Marshal(map[string]any{
		"subnet_name": req.SubnetName,
		"vpc_name":    req.VPCName,
		"region":      req.Region,
		"az":          req.AZ,
		"cidr":        req.CIDR,
	})
}

func (o *AZOrchestrator) GetVPCStatus(ctx context.Context, vpcName string) (*models.VPCStatusResponse, error) {
	vpc, err := o.vpcDAO.GetByName(ctx, vpcName, o.az)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("VPC不存在: %s", vpcName)
	}
	if err != nil {
		return nil, fmt.Errorf("查询VPC失败: %v", err)
	}

	tasks, err := o.taskDAO.GetByResourceID(ctx, vpc.ID)
	if err != nil {
		return nil, fmt.Errorf("查询任务列表失败: %v", err)
	}

	return &models.VPCStatusResponse{
		VPCID:   vpc.ID,
		VPCName: vpc.VPCName,
		AZ:      vpc.AZ,
		Status:  vpc.Status,
		Progress: models.ResourceProgress{
			Total:     vpc.TotalTasks,
			Completed: vpc.CompletedTasks,
			Failed:    vpc.FailedTasks,
			Pending:   maxInt(vpc.TotalTasks-vpc.CompletedTasks-vpc.FailedTasks, 0),
		},
		Tasks:        tasks,
		ErrorMessage: vpc.ErrorMessage,
		CreatedAt:    vpc.CreatedAt,
		UpdatedAt:    vpc.UpdatedAt,
	}, nil
}

func (o *AZOrchestrator) GetSubnetStatus(ctx context.Context, subnetName string) (*models.SubnetStatusResponse, error) {
	subnet, err := o.subnetDAO.GetByName(ctx, subnetName, o.az)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("子网不存在: %s", subnetName)
	}
	if err != nil {
		return nil, fmt.Errorf("查询子网失败: %v", err)
	}

	tasks, err := o.taskDAO.GetByResourceID(ctx, subnet.ID)
	if err != nil {
		return nil, fmt.Errorf("查询任务列表失败: %v", err)
	}

	return &models.SubnetStatusResponse{
		SubnetID:   subnet.ID,
		SubnetName: subnet.SubnetName,
		AZ:         subnet.AZ,
		Status:     subnet.Status,
		Progress: models.ResourceProgress{
			Total:     subnet.TotalTasks,
			Completed: subnet.CompletedTasks,
			Failed:    subnet.FailedTasks,
			Pending:   maxInt(subnet.TotalTasks-subnet.CompletedTasks-subnet.FailedTasks, 0),
		},
		Tasks:        tasks,
		ErrorMessage: subnet.ErrorMessage,
		CreatedAt:    subnet.CreatedAt,
		UpdatedAt:    subnet.UpdatedAt,
	}, nil
}

func (o *AZOrchestrator) DeleteVPC(ctx context.Context, vpcName string) error {
	targetScope := fmt.Sprintf("%s/%s/%s", o.region, o.az, vpcName)
	vpc, err := o.vpcDAO.GetByName(ctx, vpcName, o.az)
	if err == sql.ErrNoRows {
		return o.deleteResourceAndReleaseTarget(ctx, "vpc_resources", "vpc_name", vpcName, "vpc", targetScope)
	}
	if err != nil {
		return fmt.Errorf("查询VPC失败: %v", err)
	}
	subnetCount, err := o.vpcDAO.CountSubnets(ctx, vpcName, o.az)
	if err != nil {
		return fmt.Errorf("查询子网数量失败: %v", err)
	}
	if subnetCount > 0 {
		return fmt.Errorf("VPC下存在%d个子网，无法删除", subnetCount)
	}

	policyCount, err := o.checkZonePolicies(ctx, vpc.FirewallZone)
	if err != nil {
		return fmt.Errorf("检查Zone策略失败: %w", err)
	}
	if policyCount > 0 {
		return fmt.Errorf("Zone %s 中存在%d条防火墙策略，无法删除VPC", vpc.FirewallZone, policyCount)
	}

	if err := o.deleteResourceAndReleaseTarget(ctx, "vpc_resources", "vpc_name", vpcName, "vpc", targetScope); err != nil {
		return fmt.Errorf("原子删除VPC失败: %w", err)
	}

	logger.InfoContext(ctx, "VPC删除成功", "az", o.az, "vpcName", vpcName)
	return nil
}

func (o *AZOrchestrator) DeleteSubnet(ctx context.Context, subnetName string) error {
	targetScope := fmt.Sprintf("%s/%s/%s", o.region, o.az, subnetName)
	_, err := o.subnetDAO.GetByName(ctx, subnetName, o.az)
	if err == sql.ErrNoRows {
		return o.deleteResourceAndReleaseTarget(ctx, "subnet_resources", "subnet_name", subnetName, "subnet", targetScope)
	}
	if err != nil {
		return fmt.Errorf("查询子网失败: %v", err)
	}
	if err := o.deleteResourceAndReleaseTarget(ctx, "subnet_resources", "subnet_name", subnetName, "subnet", targetScope); err != nil {
		return fmt.Errorf("原子删除子网失败: %w", err)
	}

	logger.InfoContext(ctx, "子网删除成功", "az", o.az, "subnetName", subnetName)
	return nil
}

func (o *AZOrchestrator) ListVPCs(ctx context.Context) ([]*models.VPCResource, error) {
	return o.vpcDAO.ListAll(ctx)
}

func (o *AZOrchestrator) GetAZ() string {
	return o.az
}

func (o *AZOrchestrator) GetVPCByID(ctx context.Context, vpcID string) (*models.VPCResource, error) {
	return o.vpcDAO.GetByID(ctx, vpcID)
}

func (o *AZOrchestrator) DeleteVPCByID(ctx context.Context, vpcID string) error {
	vpc, err := o.vpcDAO.GetByID(ctx, vpcID)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("查询VPC失败: %v", err)
	}
	return o.DeleteVPC(ctx, vpc.VPCName)
}

func (o *AZOrchestrator) ListSubnetsByVPCID(ctx context.Context, vpcID string) ([]*models.SubnetResource, error) {
	return o.subnetDAO.ListByVPCID(ctx, vpcID)
}

func (o *AZOrchestrator) GetSubnetByID(ctx context.Context, subnetID string) (*models.SubnetResource, error) {
	return o.subnetDAO.GetByID(ctx, subnetID)
}

func (o *AZOrchestrator) DeleteSubnetByID(ctx context.Context, subnetID string) error {
	subnet, err := o.subnetDAO.GetByID(ctx, subnetID)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("查询子网失败: %v", err)
	}
	return o.DeleteSubnet(ctx, subnet.SubnetName)
}

func (o *AZOrchestrator) GetTaskByID(ctx context.Context, taskID string) (*models.Task, error) {
	task, err := o.taskDAO.GetByID(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("查询任务失败: %v", err)
	}
	return o.enrichTaskStatus(ctx, task), nil
}

func (o *AZOrchestrator) ReplayTask(ctx context.Context, taskID string) error {
	task, err := o.taskDAO.GetByID(ctx, taskID)
	if err != nil {
		return fmt.Errorf("获取任务失败: %v", err)
	}
	if task.Status == models.TaskStatusCompleted || task.Status == models.TaskStatusFailed || task.Status == models.TaskStatusCancelled {
		return fmt.Errorf("终态任务不能原地Replay；请使用新的Idempotency-Key发起审计后的业务操作 (当前状态: %s)", task.Status)
	}

	queueName := o.resolveQueueName(task.DeviceType, task.Priority)
	if task.AsynqTaskID != "" {
		if tr, ok := o.inspector.(taskqueue.TaskReader); ok {
			if tc, ok := o.inspector.(taskqueue.TaskController); ok {
				if detail, err := tr.GetTaskInfo(ctx, queueName, task.AsynqTaskID); err == nil {
					if detail.State == taskqueue.TaskStateRetry || detail.State == taskqueue.TaskStateFailed || detail.State == taskqueue.TaskStateScheduled {
						if err := tc.RunTask(ctx, queueName, task.AsynqTaskID); err == nil {
							return o.taskDAO.UpdateQueued(ctx, task.ID, task.AsynqTaskID)
						}
					}
				}
			}
		}
	}

	if err := o.workflowMgr.RequeueTask(ctx, task); err != nil {
		return fmt.Errorf("重做任务失败: %v", err)
	}

	logger.InfoContext(ctx, "任务重做成功", "az", o.az, "taskID", taskID)
	return nil
}

func (o *AZOrchestrator) StartCompensationTask(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		logger.Platform().Info("[补偿任务] 启动", "az", o.az, "interval", interval.String())

		for {
			select {
			case <-ctx.Done():
				logger.Platform().Info("[补偿任务] 停止", "az", o.az)
				return
			case <-ticker.C:
				o.runCompensation(ctx)
			}
		}
	}()
}

func (o *AZOrchestrator) runCompensation(ctx context.Context) {
	o.compensateVPCs(ctx)
	o.compensateSubnets(ctx)
	o.compensatePCCNs(ctx)
}

func (o *AZOrchestrator) compensateVPCs(ctx context.Context) {
	vpcs, err := o.vpcDAO.ListAll(ctx)
	if err != nil {
		logger.Platform().Error("[补偿任务] 查询VPC列表失败", "az", o.az, "error", err)
		return
	}

	for _, vpc := range vpcs {
		o.compensateResource(ctx, vpc.ID, vpc.CurrentOperationID, vpc.Generation, vpc.Status, vpc.UpdatedAt, vpc.TotalTasks, o.vpcDAO.UpdateStatus)
	}
}

func (o *AZOrchestrator) compensateSubnets(ctx context.Context) {
	vpcs, err := o.vpcDAO.ListAll(ctx)
	if err != nil {
		return
	}

	for _, vpc := range vpcs {
		subnets, err := o.subnetDAO.ListByVPCID(ctx, vpc.ID)
		if err != nil {
			continue
		}
		for _, subnet := range subnets {
			o.compensateResource(ctx, subnet.ID, subnet.CurrentOperationID, subnet.Generation, subnet.Status, subnet.UpdatedAt, subnet.TotalTasks, o.subnetDAO.UpdateStatus)
		}
	}
}

func (o *AZOrchestrator) compensatePCCNs(ctx context.Context) {
	pccns, err := o.pccnDAO.ListAll(ctx)
	if err != nil {
		logger.Platform().Error("[补偿任务] 查询PCCN列表失败", "az", o.az, "error", err)
		return
	}

	for _, pccn := range pccns {
		o.compensateResource(ctx, pccn.ID, pccn.CurrentOperationID, pccn.Generation, pccn.Status, pccn.UpdatedAt, pccn.TotalTasks, o.pccnDAO.UpdateStatus)
	}
}

func (o *AZOrchestrator) compensateResource(
	ctx context.Context,
	resourceID string,
	operationID string,
	generation int64,
	currentStatus models.ResourceStatus,
	updatedAt time.Time,
	totalTasks int,
	updateStatus func(ctx context.Context, id string, status models.ResourceStatus, errMsg string) error,
) {
	if currentStatus == models.ResourceStatusRunning || currentStatus == models.ResourceStatusFailed || currentStatus == models.ResourceStatusDeleted {
		return
	}

	if operationID == "" || generation <= 0 {
		return
	}
	total, completed, failed, err := o.taskDAO.GetTaskStatsForOperationGeneration(ctx, resourceID, operationID, generation)
	if err != nil {
		logger.Platform().Error("[补偿任务] 查询任务统计失败", "az", o.az, "resourceID", resourceID, "error", err)
		return
	}

	if total == 0 && totalTasks == 0 {
		return
	}
	if completed > 0 && completed == maxInt(total, totalTasks) {
		_ = updateStatus(ctx, resourceID, models.ResourceStatusRunning, "")
		return
	}
	if failed > 0 {
		_ = updateStatus(ctx, resourceID, models.ResourceStatusFailed, "workflow step failed")
		return
	}
	if o.resourceHasInFlightBrokerTask(ctx, resourceID, operationID, generation) {
		return
	}
	if time.Since(updatedAt) > staleWorkflowTimeout {
		_ = updateStatus(ctx, resourceID, models.ResourceStatusFailed, "workflow reply timeout")
	}
}

func (o *AZOrchestrator) resourceHasInFlightBrokerTask(ctx context.Context, resourceID, operationID string, generation int64) bool {
	if o.workflowMgr.ResourceOperationHasActiveOutbox(ctx, resourceID, operationID, generation) {
		return true
	}
	tasks, err := o.taskDAO.GetByResourceOperationGeneration(ctx, resourceID, operationID, generation)
	if err != nil {
		logger.Platform().Error("[补偿任务] 查询任务列表失败", "az", o.az, "resourceID", resourceID, "error", err)
		return false
	}

	reader, ok := o.inspector.(taskqueue.TaskReader)
	for _, task := range tasks {
		if task == nil {
			continue
		}
		if ok && task.AsynqTaskID != "" {
			detail, err := reader.GetTaskInfo(ctx, o.resolveQueueName(task.DeviceType, task.Priority), task.AsynqTaskID)
			if err == nil && taskIsInFlight(detail.State) {
				return true
			}
			if err != nil && !errors.Is(err, taskqueue.ErrTaskNotFound) && !errors.Is(err, taskqueue.ErrQueueNotFound) {
				logger.Platform().Warn("[补偿任务] 查询broker任务状态失败", "az", o.az, "taskID", task.ID, "asynqTaskID", task.AsynqTaskID, "error", err)
			}
			continue
		}

		if task.Status == models.TaskStatusQueued || task.Status == models.TaskStatusRunning {
			return true
		}
	}
	return false
}

func taskIsInFlight(state taskqueue.TaskState) bool {
	switch state {
	case taskqueue.TaskStatePending, taskqueue.TaskStateScheduled, taskqueue.TaskStateActive, taskqueue.TaskStateRetry:
		return true
	default:
		return false
	}
}

func (o *AZOrchestrator) checkZonePolicies(ctx context.Context, zone string) (int, error) {
	vfwAddr := os.Getenv("AZ_NSP_VFW_ADDR")
	if vfwAddr == "" {
		vfwAddr = fmt.Sprintf("http://az-nsp-vfw-%s:8080", o.az)
	}

	url := fmt.Sprintf("%s/api/v1/firewall/zone/%s/policy-count", vfwAddr, zone)
	resp, err := o.tracedHTTP.Get(ctx, url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, nil
	}

	var result struct {
		Success bool `json:"success"`
		Count   int  `json:"count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, err
	}
	return result.Count, nil
}

func (o *AZOrchestrator) CreatePCCN(ctx context.Context, req *models.PCCNRequest) (*models.PCCNResponse, error) {
	logger.InfoContext(ctx, "开始创建PCCN连接",
		"az", o.az,
		"pccn_name", req.PCCNName,
		"vpc_name", req.VPC1.VPCName,
		"vpc_region", req.VPC1.Region,
		"peer_vpc_name", req.VPC2.VPCName,
		"peer_vpc_region", req.VPC2.Region,
	)

	pccnID := req.PCCNID
	if pccnID == "" {
		pccnID = uuid.New().String()
	}

	vpc, err := o.vpcDAO.GetByName(ctx, req.VPC1.VPCName, o.az)
	if err == sql.ErrNoRows {
		return &models.PCCNResponse{Success: false, Message: fmt.Sprintf("VPC不存在: %s", req.VPC1.VPCName)}, nil
	}
	if err != nil {
		return &models.PCCNResponse{Success: false, Message: fmt.Sprintf("查询VPC失败: %v", err)}, nil
	}

	subnets, err := o.subnetDAO.ListByVPCID(ctx, vpc.ID)
	if err != nil {
		return &models.PCCNResponse{Success: false, Message: fmt.Sprintf("获取子网列表失败: %v", err)}, nil
	}

	var subnetCIDRs []string
	for _, subnet := range subnets {
		subnetCIDRs = append(subnetCIDRs, subnet.CIDR)
	}

	pccnResource := &models.PCCNResource{
		ID:            pccnID,
		PCCNName:      req.PCCNName,
		VPCName:       req.VPC1.VPCName,
		VPCRegion:     req.VPC1.Region,
		PeerVPCName:   req.VPC2.VPCName,
		PeerVPCRegion: req.VPC2.Region,
		AZ:            o.az,
		Status:        models.ResourceStatusPending,
		Subnets:       subnetCIDRs,
		TotalTasks:    0,
	}

	workflowID, persistedPCCNID, err := o.workflowMgr.SubmitPreparedWorkflow(ctx, func(ctx context.Context, tx *sql.Tx) (orchestration.WorkflowDef, error) {
		operationPayload := struct {
			PCCNName string        `json:"pccn_name"`
			VPC1     models.VPCRef `json:"vpc1"`
			VPC2     models.VPCRef `json:"vpc2"`
		}{
			PCCNName: req.PCCNName,
			VPC1:     req.VPC1,
			VPC2:     req.VPC2,
		}
		op, decision, err := o.beginCreateOperationTx(ctx, tx, "POST /api/v1/pccn", "create_pccn", fmt.Sprintf("%s/%s", o.az, req.PCCNName), operationPayload, "pccn", pccnID)
		if err != nil {
			return orchestration.WorkflowDef{}, err
		}
		pccnID = op.ResourceID
		pccnResource.ID = pccnID
		if decision == operation.DecisionReplay {
			return orchestration.WorkflowDef{
				OperationID: op.OperationID, RootOperationID: op.RootOperationID,
				WorkflowID: op.OperationID, Generation: op.Generation,
				OperationRequired: true, ReplayExisting: true,
				ResourceType: models.ResourceTypePCCN, ResourceID: pccnID, AZ: o.az,
			}, nil
		}
		if decision == operation.DecisionNew {
			persistedPCCN, err := o.pccnDAO.CreateTx(ctx, tx, pccnResource)
			if err != nil {
				return orchestration.WorkflowDef{}, err
			}
			pccnID = persistedPCCN.ID
			if pccnID != op.ResourceID {
				if _, err := tx.ExecContext(ctx, `UPDATE orchestration_operations SET resource_id = $1, updated_at = NOW() WHERE operation_id = $2 AND status = 'accepted'`, pccnID, op.OperationID); err != nil {
					return orchestration.WorkflowDef{}, err
				}
			}
		}
		params, err := o.buildPCCNTaskParams(pccnID, req, subnetCIDRs)
		if err != nil {
			return orchestration.WorkflowDef{}, err
		}
		return orchestration.WorkflowDef{
			OperationID:       op.OperationID,
			RootOperationID:   op.RootOperationID,
			WorkflowID:        op.OperationID,
			Generation:        op.Generation,
			OperationRequired: true,
			ResourceType:      models.ResourceTypePCCN,
			ResourceID:        pccnID,
			AZ:                o.az,
			Steps: []orchestration.WorkflowStep{
				{TaskType: "create_pccn_connection", TaskName: "创建PCCN连接", DeviceType: string(queue.DeviceTypeSwitch), Priority: int(taskqueue.PriorityNormal), Payload: params},
				{TaskType: "configure_pccn_routing", TaskName: "配置PCCN路由", DeviceType: string(queue.DeviceTypeSwitch), Priority: int(taskqueue.PriorityNormal), Payload: params},
			},
		}, nil
	})
	if err != nil {
		return nil, fmt.Errorf("提交PCCN工作流: %w", err)
	}
	pccnID = persistedPCCNID

	logger.InfoContext(ctx, "PCCN创建流程启动成功",
		"az", o.az,
		"pccn_name", req.PCCNName,
		"pccn_id", pccnID,
		"workflow_id", workflowID,
	)

	return &models.PCCNResponse{
		Success:     true,
		Message:     "PCCN创建工作流已启动",
		ResourceID:  pccnID,
		Status:      "accepted",
		PCCNID:      pccnID,
		TxID:        workflowID,
		OperationID: workflowID,
	}, nil
}

func (o *AZOrchestrator) buildPCCNTaskParams(pccnID string, req *models.PCCNRequest, subnets []string) ([]byte, error) {
	return json.Marshal(map[string]any{
		"pccn_id":         pccnID,
		"pccn_name":       req.PCCNName,
		"vpc_name":        req.VPC1.VPCName,
		"vpc_region":      req.VPC1.Region,
		"peer_vpc_name":   req.VPC2.VPCName,
		"peer_vpc_region": req.VPC2.Region,
		"az":              o.az,
		"subnets":         subnets,
	})
}

func (o *AZOrchestrator) GetPCCNStatus(ctx context.Context, pccnName string) (*models.PCCNStatusResponse, error) {
	pccn, err := o.pccnDAO.GetByName(ctx, pccnName, o.az)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("PCCN不存在: %s", pccnName)
	}
	if err != nil {
		return nil, fmt.Errorf("查询PCCN失败: %v", err)
	}

	tasks, err := o.taskDAO.GetByResourceID(ctx, pccn.ID)
	if err != nil {
		return nil, fmt.Errorf("查询任务列表失败: %v", err)
	}

	return &models.PCCNStatusResponse{
		PCCNID:        pccn.ID,
		PCCNName:      pccn.PCCNName,
		VPCName:       pccn.VPCName,
		VPCRegion:     pccn.VPCRegion,
		PeerVPCName:   pccn.PeerVPCName,
		PeerVPCRegion: pccn.PeerVPCRegion,
		AZ:            pccn.AZ,
		Status:        pccn.Status,
		Subnets:       pccn.Subnets,
		Progress: models.ResourceProgress{
			Total:     pccn.TotalTasks,
			Completed: pccn.CompletedTasks,
			Failed:    pccn.FailedTasks,
			Pending:   maxInt(pccn.TotalTasks-pccn.CompletedTasks-pccn.FailedTasks, 0),
		},
		Tasks:        tasks,
		ErrorMessage: pccn.ErrorMessage,
		CreatedAt:    pccn.CreatedAt,
		UpdatedAt:    pccn.UpdatedAt,
	}, nil
}

func (o *AZOrchestrator) DeletePCCN(ctx context.Context, pccnName string) error {
	targetScope := fmt.Sprintf("%s/%s", o.az, pccnName)
	_, err := o.pccnDAO.GetByName(ctx, pccnName, o.az)
	if err == sql.ErrNoRows {
		return o.deleteResourceAndReleaseTarget(ctx, "pccn_resources", "pccn_name", pccnName, "pccn", targetScope)
	}
	if err != nil {
		return fmt.Errorf("查询PCCN失败: %v", err)
	}
	if err := o.deleteResourceAndReleaseTarget(ctx, "pccn_resources", "pccn_name", pccnName, "pccn", targetScope); err != nil {
		return fmt.Errorf("原子删除PCCN失败: %w", err)
	}

	logger.InfoContext(ctx, "PCCN删除成功", "az", o.az, "pccn_name", pccnName)
	return nil
}

func (o *AZOrchestrator) deleteResourceAndReleaseTarget(ctx context.Context, table, nameColumn, name, resourceType, targetScope string) error {
	allowed := map[string]string{"vpc_resources": "vpc_name", "subnet_resources": "subnet_name", "pccn_resources": "pccn_name"}
	if allowed[table] != nameColumn {
		return fmt.Errorf("unsupported resource table %q", table)
	}
	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ownerService := fmt.Sprintf("az-nsp-vpc-%s", o.az)
	if err := o.operationSvc.MarkTargetRetiringTx(ctx, tx, ownerService, resourceType, targetScope); err != nil {
		return err
	}
	query := fmt.Sprintf(`SELECT id FROM %s WHERE %s = $1 AND az = $2 FOR UPDATE`, table, nameColumn)
	var resourceID string
	err = tx.QueryRowContext(ctx, query, name, o.az).Scan(&resourceID)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if table == "vpc_resources" && resourceID != "" {
		var subnetCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM subnet_resources WHERE vpc_name = $1 AND az = $2 AND status != 'deleted'`, name, o.az).Scan(&subnetCount); err != nil {
			return err
		}
		if subnetCount > 0 {
			return fmt.Errorf("VPC下存在%d个子网，无法删除", subnetCount)
		}
	}
	if err := o.operationSvc.ReleaseTargetTx(ctx, tx, ownerService, resourceType, targetScope); err != nil {
		return err
	}
	if resourceID != "" {
		// The demo DeviceDriver persists its queryable device state in the AZ
		// database. Remove it before the resource identity is retired. A late
		// create task is fenced by Runtime.resourceGenerationCurrent and will
		// reconcile the same target back to absent.
		if _, err := tx.ExecContext(ctx, `DELETE FROM worker_device_state WHERE target_key = $1`, resourceID); err != nil {
			return err
		}
		deleteQuery := fmt.Sprintf(`DELETE FROM %s WHERE id = $1`, table)
		if _, err := tx.ExecContext(ctx, deleteQuery, resourceID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (o *AZOrchestrator) ListPCCNs(ctx context.Context) ([]*models.PCCNResource, error) {
	return o.pccnDAO.ListAll(ctx)
}

func (o *AZOrchestrator) enrichTaskStatus(ctx context.Context, task *models.Task) *models.Task {
	if task == nil || o.inspector == nil || task.AsynqTaskID == "" {
		return task
	}

	reader, ok := o.inspector.(taskqueue.TaskReader)
	if !ok {
		return task
	}

	detail, err := reader.GetTaskInfo(ctx, o.resolveQueueName(task.DeviceType, task.Priority), task.AsynqTaskID)
	if err != nil {
		if errors.Is(err, taskqueue.ErrTaskNotFound) || errors.Is(err, taskqueue.ErrQueueNotFound) {
			return task
		}
		return task
	}

	task.Status = mapTaskState(detail.State, task.Status)
	task.RetryCount = detail.Retried
	task.MaxRetries = detail.MaxRetry
	if detail.LastError != "" {
		task.ErrorMessage = detail.LastError
	}
	return task
}

func (o *AZOrchestrator) resolveQueueName(deviceType string, priority int) string {
	return queue.GetPriorityQueueName(o.region, o.az, queue.DeviceType(deviceType), queue.TaskPriority(priority))
}

func mapTaskState(state taskqueue.TaskState, fallback models.TaskStatus) models.TaskStatus {
	switch state {
	case taskqueue.TaskStatePending, taskqueue.TaskStateScheduled, taskqueue.TaskStateRetry:
		return models.TaskStatusQueued
	case taskqueue.TaskStateActive:
		return models.TaskStatusRunning
	case taskqueue.TaskStateCompleted:
		return models.TaskStatusCompleted
	case taskqueue.TaskStateFailed:
		return models.TaskStatusFailed
	default:
		return fallback
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
