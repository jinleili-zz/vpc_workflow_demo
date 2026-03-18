package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/jinleili-zz/nsp-platform/logger"
	"github.com/jinleili-zz/nsp-platform/taskqueue"
	"github.com/jinleili-zz/nsp-platform/trace"

	"workflow_qoder/internal/db/dao"
	"workflow_qoder/internal/models"
	"workflow_qoder/internal/queue"

	"github.com/google/uuid"
)

type AZOrchestrator struct {
	vpcDAO     *dao.VPCDAO
	subnetDAO  *dao.SubnetDAO
	pccnDAO    *dao.PCCNDAO
	engine     *taskqueue.Engine
	tracedHTTP *trace.TracedClient
	region     string
	az         string
}

func NewAZOrchestrator(db *sql.DB, engine *taskqueue.Engine, tracedHTTP *trace.TracedClient, region, az string) *AZOrchestrator {
	return &AZOrchestrator{
		vpcDAO:     dao.NewVPCDAO(db),
		subnetDAO:  dao.NewSubnetDAO(db),
		pccnDAO:    dao.NewPCCNDAO(db),
		engine:     engine,
		tracedHTTP: tracedHTTP,
		region:     region,
		az:         az,
	}
}

// BuildWorkflowHooks returns the WorkflowHooks that synchronise business resource tables
// (vpc_resources, subnet_resources) with tq_workflows/tq_steps state transitions.
// NOTE: Hooks are non-blocking - they log warnings on errors but return nil to avoid
// stalling workflow execution. Compensation tasks handle eventual consistency.
func (o *AZOrchestrator) BuildWorkflowHooks() *taskqueue.WorkflowHooks {
	return &taskqueue.WorkflowHooks{
		OnStepComplete: func(ctx context.Context, wf *taskqueue.Workflow, step *taskqueue.StepTask) error {
			var err error
			switch wf.ResourceType {
			case string(models.ResourceTypeVPC):
				err = o.vpcDAO.IncrementCompletedTasks(ctx, wf.ResourceID)
			case string(models.ResourceTypeSubnet):
				err = o.subnetDAO.IncrementCompletedTasks(ctx, wf.ResourceID)
			case string(models.ResourceTypePCCN):
				err = o.pccnDAO.IncrementCompletedTasks(ctx, wf.ResourceID)
			}
			if err != nil {
				logger.WarnContext(ctx, "OnStepComplete hook failed (non-blocking)", "resourceType", wf.ResourceType, "resourceID", wf.ResourceID, "error", err)
			}
			return nil
		},
		OnStepFailed: func(ctx context.Context, wf *taskqueue.Workflow, step *taskqueue.StepTask, errMsg string) error {
			var err error
			switch wf.ResourceType {
			case string(models.ResourceTypeVPC):
				err = o.vpcDAO.IncrementFailedTasks(ctx, wf.ResourceID)
			case string(models.ResourceTypeSubnet):
				err = o.subnetDAO.IncrementFailedTasks(ctx, wf.ResourceID)
			case string(models.ResourceTypePCCN):
				err = o.pccnDAO.IncrementFailedTasks(ctx, wf.ResourceID)
			}
			if err != nil {
				logger.WarnContext(ctx, "OnStepFailed hook failed (non-blocking)", "resourceType", wf.ResourceType, "resourceID", wf.ResourceID, "error", err)
			}
			return nil
		},
		OnWorkflowComplete: func(ctx context.Context, wf *taskqueue.Workflow) error {
			var err error
			switch wf.ResourceType {
			case string(models.ResourceTypeVPC):
				logger.InfoContext(ctx, "VPC创建完成", "az", o.az, "resourceID", wf.ResourceID)
				err = o.vpcDAO.UpdateStatus(ctx, wf.ResourceID, models.ResourceStatusRunning, "")
			case string(models.ResourceTypeSubnet):
				logger.InfoContext(ctx, "子网创建完成", "az", o.az, "resourceID", wf.ResourceID)
				err = o.subnetDAO.UpdateStatus(ctx, wf.ResourceID, models.ResourceStatusRunning, "")
			case string(models.ResourceTypePCCN):
				logger.InfoContext(ctx, "PCCN创建完成", "az", o.az, "resourceID", wf.ResourceID)
				err = o.pccnDAO.UpdateStatus(ctx, wf.ResourceID, models.ResourceStatusRunning, "")
			}
			if err != nil {
				logger.WarnContext(ctx, "OnWorkflowComplete hook failed (non-blocking)", "resourceType", wf.ResourceType, "resourceID", wf.ResourceID, "error", err)
			}
			return nil
		},
		OnWorkflowFailed: func(ctx context.Context, wf *taskqueue.Workflow, errMsg string) error {
			var err error
			switch wf.ResourceType {
			case string(models.ResourceTypeVPC):
				err = o.vpcDAO.UpdateStatus(ctx, wf.ResourceID, models.ResourceStatusFailed, errMsg)
			case string(models.ResourceTypeSubnet):
				err = o.subnetDAO.UpdateStatus(ctx, wf.ResourceID, models.ResourceStatusFailed, errMsg)
			case string(models.ResourceTypePCCN):
				err = o.pccnDAO.UpdateStatus(ctx, wf.ResourceID, models.ResourceStatusFailed, errMsg)
			}
			if err != nil {
				logger.WarnContext(ctx, "OnWorkflowFailed hook failed (non-blocking)", "resourceType", wf.ResourceType, "resourceID", wf.ResourceID, "error", err)
			}
			return nil
		},
	}
}

func (o *AZOrchestrator) CreateVPC(ctx context.Context, req *models.VPCRequest) (*models.VPCResponse, error) {
	logger.InfoContext(ctx, "开始创建VPC", "az", o.az, "vpcName", req.VPCName)

	// 使用 Top 层传入的统一 VPC ID，若未提供则自行生成（向后兼容）
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

	if err := o.vpcDAO.Create(ctx, vpcResource); err != nil {
		return &models.VPCResponse{
			Success: false,
			Message: fmt.Sprintf("创建VPC资源记录失败: %v", err),
		}, nil
	}

	params := o.buildVPCTaskParams(ctx, req)
	def := &taskqueue.WorkflowDefinition{
		Name:         "create_vpc",
		ResourceType: string(models.ResourceTypeVPC),
		ResourceID:   vpcID,
		Metadata:     map[string]string{"az": o.az},
		Steps: []taskqueue.StepDefinition{
			{TaskType: "create_vrf_on_switch", TaskName: "创建VRF", QueueTag: string(queue.DeviceTypeSwitch), Priority: taskqueue.PriorityNormal, Params: params},
			{TaskType: "create_vlan_subinterface", TaskName: "创建VLAN子接口", QueueTag: string(queue.DeviceTypeSwitch), Priority: taskqueue.PriorityNormal, Params: params},
			{TaskType: "create_firewall_zone", TaskName: "创建防火墙安全区域", QueueTag: string(queue.DeviceTypeFirewall), Priority: taskqueue.PriorityNormal, Params: params},
		},
	}

	if err := o.vpcDAO.UpdateTotalTasks(ctx, vpcID, len(def.Steps)); err != nil {
		return &models.VPCResponse{
			Success: false,
			Message: fmt.Sprintf("更新任务总数失败: %v", err),
		}, nil
	}

	if err := o.vpcDAO.UpdateStatus(ctx, vpcID, models.ResourceStatusCreating, ""); err != nil {
		return &models.VPCResponse{
			Success: false,
			Message: fmt.Sprintf("更新VPC状态失败: %v", err),
		}, nil
	}

	workflowID, err := o.engine.SubmitWorkflow(ctx, def)
	if err != nil {
		return &models.VPCResponse{
			Success: false,
			Message: fmt.Sprintf("提交工作流失败: %v", err),
		}, nil
	}

	logger.InfoContext(ctx, "VPC创建流程启动成功", "az", o.az, "vpcName", req.VPCName, "vpcID", vpcID, "workflowID", workflowID)

	return &models.VPCResponse{
		Success:    true,
		Message:    "VPC创建工作流已启动",
		VPCID:      vpcID,
		WorkflowID: workflowID,
	}, nil
}

func (o *AZOrchestrator) buildVPCTaskParams(ctx context.Context, req *models.VPCRequest) string {
	params := map[string]interface{}{
		"vpc_name":      req.VPCName,
		"vrf_name":      req.VRFName,
		"vlan_id":       req.VLANId,
		"firewall_zone": req.FirewallZone,
		"region":        req.Region,
	}
	data, err := json.Marshal(params)
	if err != nil {
		logger.InfoContext(ctx, "序列化VPC任务参数失败", "error", err, "vpcName", req.VPCName)
		return ""
	}
	return string(data)
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

	if err := o.subnetDAO.Create(ctx, subnetResource); err != nil {
		return &models.SubnetResponse{
			Success: false,
			Message: fmt.Sprintf("创建子网资源记录失败: %v", err),
		}, nil
	}

	params := o.buildSubnetTaskParams(ctx, req)
	def := &taskqueue.WorkflowDefinition{
		Name:         "create_subnet",
		ResourceType: string(models.ResourceTypeSubnet),
		ResourceID:   subnetID,
		Metadata:     map[string]string{"az": o.az},
		Steps: []taskqueue.StepDefinition{
			{TaskType: "create_subnet_on_switch", TaskName: "创建子网", QueueTag: string(queue.DeviceTypeSwitch), Priority: taskqueue.PriorityNormal, Params: params},
			{TaskType: "configure_subnet_routing", TaskName: "配置子网路由", QueueTag: string(queue.DeviceTypeSwitch), Priority: taskqueue.PriorityNormal, Params: params},
		},
	}

	if err := o.subnetDAO.UpdateTotalTasks(ctx, subnetID, len(def.Steps)); err != nil {
		return &models.SubnetResponse{
			Success: false,
			Message: fmt.Sprintf("更新任务总数失败: %v", err),
		}, nil
	}

	if err := o.subnetDAO.UpdateStatus(ctx, subnetID, models.ResourceStatusCreating, ""); err != nil {
		return &models.SubnetResponse{
			Success: false,
			Message: fmt.Sprintf("更新子网状态失败: %v", err),
		}, nil
	}

	workflowID, err := o.engine.SubmitWorkflow(ctx, def)
	if err != nil {
		return &models.SubnetResponse{
			Success: false,
			Message: fmt.Sprintf("提交工作流失败: %v", err),
		}, nil
	}

	logger.InfoContext(ctx, "子网创建流程启动成功", "az", o.az, "subnetName", req.SubnetName, "subnetID", subnetID, "workflowID", workflowID)

	return &models.SubnetResponse{
		Success:    true,
		Message:    "子网创建工作流已启动",
		SubnetID:   subnetID,
		WorkflowID: workflowID,
	}, nil
}

func (o *AZOrchestrator) buildSubnetTaskParams(ctx context.Context, req *models.SubnetRequest) string {
	params := map[string]interface{}{
		"subnet_name": req.SubnetName,
		"vpc_name":    req.VPCName,
		"region":      req.Region,
		"az":          req.AZ,
		"cidr":        req.CIDR,
	}
	data, err := json.Marshal(params)
	if err != nil {
		logger.InfoContext(ctx, "序列化子网任务参数失败", "error", err, "subnetName", req.SubnetName)
		return ""
	}
	return string(data)
}

// HandleTaskCallback delegates callback processing to the Engine.
func (o *AZOrchestrator) HandleTaskCallback(ctx context.Context, payload []byte) error {
	var cb taskqueue.CallbackPayload
	if err := json.Unmarshal(payload, &cb); err != nil {
		return fmt.Errorf("解析回调载荷失败: %v", err)
	}

	logger.InfoContext(ctx, "收到任务回调", "az", o.az, "taskID", cb.TaskID, "status", cb.Status)

	if err := o.engine.HandleCallback(ctx, &cb); err != nil {
		logger.InfoContext(ctx, "任务回调处理失败", "az", o.az, "error", err)
		return err
	}

	logger.InfoContext(ctx, "任务回调处理成功", "az", o.az, "taskID", cb.TaskID)
	return nil
}

func (o *AZOrchestrator) GetVPCStatus(ctx context.Context, vpcName string) (*models.VPCStatusResponse, error) {
	vpc, err := o.vpcDAO.GetByName(ctx, vpcName, o.az)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("VPC不存在: %s", vpcName)
	}
	if err != nil {
		return nil, fmt.Errorf("查询VPC失败: %v", err)
	}

	tasks, err := o.getTasksByResourceID(ctx, string(models.ResourceTypeVPC), vpc.ID)
	if err != nil {
		return nil, fmt.Errorf("查询任务列表失败: %v", err)
	}

	pending := vpc.TotalTasks - vpc.CompletedTasks - vpc.FailedTasks

	return &models.VPCStatusResponse{
		VPCID:   vpc.ID,
		VPCName: vpc.VPCName,
		AZ:      vpc.AZ,
		Status:  vpc.Status,
		Progress: models.ResourceProgress{
			Total:     vpc.TotalTasks,
			Completed: vpc.CompletedTasks,
			Failed:    vpc.FailedTasks,
			Pending:   pending,
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

	tasks, err := o.getTasksByResourceID(ctx, string(models.ResourceTypeSubnet), subnet.ID)
	if err != nil {
		return nil, fmt.Errorf("查询任务列表失败: %v", err)
	}

	pending := subnet.TotalTasks - subnet.CompletedTasks - subnet.FailedTasks

	return &models.SubnetStatusResponse{
		SubnetID:   subnet.ID,
		SubnetName: subnet.SubnetName,
		AZ:         subnet.AZ,
		Status:     subnet.Status,
		Progress: models.ResourceProgress{
			Total:     subnet.TotalTasks,
			Completed: subnet.CompletedTasks,
			Failed:    subnet.FailedTasks,
			Pending:   pending,
		},
		Tasks:        tasks,
		ErrorMessage: subnet.ErrorMessage,
		CreatedAt:    subnet.CreatedAt,
		UpdatedAt:    subnet.UpdatedAt,
	}, nil
}

// getTasksByResourceID queries Engine store for workflows by resource and converts steps to models.Task.
func (o *AZOrchestrator) getTasksByResourceID(ctx context.Context, resourceType, resourceID string) ([]*models.Task, error) {
	workflows, err := o.engine.Store().GetWorkflowsByResourceID(ctx, resourceType, resourceID)
	if err != nil {
		return nil, err
	}
	if len(workflows) == 0 {
		return nil, nil
	}

	// Use the most recent workflow (first in DESC order)
	wf := workflows[0]
	steps, err := o.engine.Store().GetStepsByWorkflow(ctx, wf.ID)
	if err != nil {
		return nil, err
	}

	tasks := make([]*models.Task, len(steps))
	for i, s := range steps {
		tasks[i] = stepToTask(s, wf)
	}
	return tasks, nil
}

// stepToTask converts a taskqueue.StepTask to models.Task for API response compatibility.
func stepToTask(s *taskqueue.StepTask, wf *taskqueue.Workflow) *models.Task {
	return &models.Task{
		ID:           s.ID,
		ResourceType: models.ResourceType(wf.ResourceType),
		ResourceID:   wf.ResourceID,
		TaskType:     s.TaskType,
		TaskName:     s.TaskName,
		TaskOrder:    s.StepOrder,
		TaskParams:   s.Params,
		Status:       models.TaskStatus(s.Status),
		Priority:     int(s.Priority),
		DeviceType:   s.QueueTag,
		AsynqTaskID:  s.BrokerTaskID,
		Result:       s.Result,
		ErrorMessage: s.ErrorMessage,
		RetryCount:   s.RetryCount,
		MaxRetries:   s.MaxRetries,
		AZ:           wf.Metadata["az"],
		CreatedAt:    s.CreatedAt,
		QueuedAt:     s.QueuedAt,
		StartedAt:    s.StartedAt,
		CompletedAt:  s.CompletedAt,
		UpdatedAt:    s.UpdatedAt,
	}
}

func (o *AZOrchestrator) DeleteVPC(ctx context.Context, vpcName string) error {
	vpc, err := o.vpcDAO.GetByName(ctx, vpcName, o.az)
	if err == sql.ErrNoRows {
		return fmt.Errorf("VPC不存在: %s", vpcName)
	}
	if err != nil {
		return fmt.Errorf("查询VPC失败: %v", err)
	}

	if vpc.Status != models.ResourceStatusRunning {
		return fmt.Errorf("VPC状态不是running，无法删除")
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
		logger.InfoContext(ctx, "检查Zone策略失败", "az", o.az, "error", err)
	}
	if policyCount > 0 {
		return fmt.Errorf("Zone %s 中存在%d条防火墙策略，无法删除VPC", vpc.FirewallZone, policyCount)
	}

	if err := o.vpcDAO.UpdateStatus(ctx, vpc.ID, models.ResourceStatusDeleting, ""); err != nil {
		return fmt.Errorf("更新VPC状态失败: %v", err)
	}

	logger.InfoContext(ctx, "VPC删除成功", "az", o.az, "vpcName", vpcName)
	return nil
}

func (o *AZOrchestrator) DeleteSubnet(ctx context.Context, subnetName string) error {
	subnet, err := o.subnetDAO.GetByName(ctx, subnetName, o.az)
	if err == sql.ErrNoRows {
		return fmt.Errorf("子网不存在: %s", subnetName)
	}
	if err != nil {
		return fmt.Errorf("查询子网失败: %v", err)
	}

	if subnet.Status != models.ResourceStatusRunning {
		return fmt.Errorf("子网状态不是running，无法删除")
	}

	if err := o.subnetDAO.UpdateStatus(ctx, subnet.ID, models.ResourceStatusDeleting, ""); err != nil {
		return fmt.Errorf("更新子网状态失败: %v", err)
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
		return fmt.Errorf("VPC不存在: %s", vpcID)
	}
	if err != nil {
		return fmt.Errorf("查询VPC失败: %v", err)
	}

	if vpc.Status != models.ResourceStatusRunning {
		return fmt.Errorf("VPC状态不是running，无法删除")
	}

	subnetCount, err := o.vpcDAO.CountSubnetsByVPCID(ctx, vpcID)
	if err != nil {
		return fmt.Errorf("查询子网数量失败: %v", err)
	}
	if subnetCount > 0 {
		return fmt.Errorf("VPC下存在%d个子网，无法删除", subnetCount)
	}

	policyCount, err := o.checkZonePolicies(ctx, vpc.FirewallZone)
	if err != nil {
		logger.InfoContext(ctx, "检查Zone策略失败", "az", o.az, "error", err)
	}
	if policyCount > 0 {
		return fmt.Errorf("Zone %s 中存在%d条防火墙策略，无法删除VPC", vpc.FirewallZone, policyCount)
	}

	if err := o.vpcDAO.UpdateStatus(ctx, vpcID, models.ResourceStatusDeleted, ""); err != nil {
		return fmt.Errorf("更新VPC状态失败: %v", err)
	}

	logger.InfoContext(ctx, "VPC删除成功", "az", o.az, "vpcID", vpcID)
	return nil
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
		return fmt.Errorf("子网不存在: %s", subnetID)
	}
	if err != nil {
		return fmt.Errorf("查询子网失败: %v", err)
	}

	if subnet.Status != models.ResourceStatusRunning {
		return fmt.Errorf("子网状态不是running，无法删除")
	}

	if err := o.subnetDAO.UpdateStatus(ctx, subnetID, models.ResourceStatusDeleted, ""); err != nil {
		return fmt.Errorf("更新子网状态失败: %v", err)
	}

	logger.InfoContext(ctx, "子网删除成功", "az", o.az, "subnetID", subnetID)
	return nil
}

func (o *AZOrchestrator) GetTaskByID(ctx context.Context, taskID string) (*models.Task, error) {
	step, err := o.engine.Store().GetStep(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("查询任务失败: %v", err)
	}
	if step == nil {
		return nil, fmt.Errorf("任务不存在: %s", taskID)
	}

	wf, err := o.engine.Store().GetWorkflow(ctx, step.WorkflowID)
	if err != nil {
		return nil, fmt.Errorf("查询工作流失败: %v", err)
	}
	if wf == nil {
		return nil, fmt.Errorf("工作流不存在: %s", step.WorkflowID)
	}

	return stepToTask(step, wf), nil
}

func (o *AZOrchestrator) ReplayTask(ctx context.Context, taskID string) error {
	step, err := o.engine.Store().GetStep(ctx, taskID)
	if err != nil {
		return fmt.Errorf("获取任务失败: %v", err)
	}
	if step == nil {
		return fmt.Errorf("任务不存在: %s", taskID)
	}

	if models.TaskStatus(step.Status) != models.TaskStatusFailed {
		return fmt.Errorf("任务状态不是failed，无法重做 (当前状态: %s)", step.Status)
	}

	// Reset the resource status back to creating so the workflow can proceed
	wf, err := o.engine.Store().GetWorkflow(ctx, step.WorkflowID)
	if err != nil {
		return fmt.Errorf("查询工作流失败: %v", err)
	}
	if wf != nil {
		switch wf.ResourceType {
		case string(models.ResourceTypeVPC):
			_ = o.vpcDAO.UpdateStatus(ctx, wf.ResourceID, models.ResourceStatusCreating, "")
		case string(models.ResourceTypeSubnet):
			_ = o.subnetDAO.UpdateStatus(ctx, wf.ResourceID, models.ResourceStatusCreating, "")
		}
	}

	if err := o.engine.RetryStep(ctx, taskID); err != nil {
		return fmt.Errorf("重做任务失败: %v", err)
	}

	logger.InfoContext(ctx, "任务重做成功", "az", o.az, "taskID", taskID)
	return nil
}

// StartCompensationTask starts a background goroutine that periodically scans for
// inconsistencies between workflow state (tq_workflows.status) and resource state
// (vpc_resources.status / subnet_resources.status), then repairs them.
// This provides eventual consistency when the WorkflowHooks fail due to transient errors.
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

// runCompensation scans and repairs inconsistencies between workflow and resource states.
func (o *AZOrchestrator) runCompensation(ctx context.Context) {
	// Repair VPC resources
	o.compensateVPCs(ctx)
	// Repair Subnet resources
	o.compensateSubnets(ctx)
}

func (o *AZOrchestrator) compensateVPCs(ctx context.Context) {
	vpcs, err := o.vpcDAO.ListAll(ctx)
	if err != nil {
		logger.Platform().Error("[补偿任务] 查询VPC列表失败", "az", o.az, "error", err)
		return
	}

	for _, vpc := range vpcs {
		// Skip already terminal states (running, failed, deleted)
		if vpc.Status == models.ResourceStatusRunning || vpc.Status == models.ResourceStatusFailed || vpc.Status == models.ResourceStatusDeleted {
			continue
		}

		// Check if there's a workflow for this resource
		workflows, err := o.engine.Store().GetWorkflowsByResourceID(ctx, string(models.ResourceTypeVPC), vpc.ID)
		if err != nil {
			logger.Platform().Error("[补偿任务] 查询VPC工作流失败", "az", o.az, "vpcID", vpc.ID, "error", err)
			continue
		}

		if len(workflows) == 0 {
			continue
		}

		wf := workflows[0] // Most recent workflow
		o.compensateResource(ctx, wf, "VPC", vpc.ID, vpc.Status, o.vpcDAO.UpdateStatus)
	}
}

func (o *AZOrchestrator) compensateSubnets(ctx context.Context) {
	// Query all subnets (we need a ListAll method if not present; for now use workaround)
	// Since we don't have ListAllSubnets, we'll skip detailed subnet compensation
	// In production, add SubnetDAO.ListAll() method and implement similar logic

	// For now, compensate by scanning subnets via VPCs
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
			if subnet.Status == models.ResourceStatusRunning || subnet.Status == models.ResourceStatusFailed || subnet.Status == models.ResourceStatusDeleted {
				continue
			}

			workflows, err := o.engine.Store().GetWorkflowsByResourceID(ctx, string(models.ResourceTypeSubnet), subnet.ID)
			if err != nil {
				logger.Platform().Error("[补偿任务] 查询子网工作流失败", "az", o.az, "subnetID", subnet.ID, "error", err)
				continue
			}

			if len(workflows) == 0 {
				continue
			}

			wf := workflows[0]
			o.compensateResource(ctx, wf, "Subnet", subnet.ID, subnet.Status, o.subnetDAO.UpdateStatus)
		}
	}
}

// compensateResource checks a single resource and repairs if workflow state diverges.
func (o *AZOrchestrator) compensateResource(
	ctx context.Context,
	wf *taskqueue.Workflow,
	resourceName string,
	resourceID string,
	currentStatus models.ResourceStatus,
	updateStatus func(ctx context.Context, id string, status models.ResourceStatus, errMsg string) error,
) {
	switch wf.Status {
	case taskqueue.WorkflowStatusSucceeded:
		if currentStatus != models.ResourceStatusRunning {
			logger.Platform().Info("[补偿任务] 修复资源状态: succeeded -> running",
				"az", o.az, "resource", resourceName, "resourceID", resourceID, "currentStatus", currentStatus)
			if err := updateStatus(ctx, resourceID, models.ResourceStatusRunning, ""); err != nil {
				logger.Platform().Error("[补偿任务] 更新资源状态失败", "az", o.az, "error", err)
			}
		}
	case taskqueue.WorkflowStatusFailed:
		if currentStatus != models.ResourceStatusFailed {
			logger.Platform().Info("[补偿任务] 修复资源状态: failed",
				"az", o.az, "resource", resourceName, "resourceID", resourceID, "currentStatus", currentStatus)
			if err := updateStatus(ctx, resourceID, models.ResourceStatusFailed, wf.ErrorMessage); err != nil {
				logger.Platform().Error("[补偿任务] 更新资源状态失败", "az", o.az, "error", err)
			}
		}
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

// =====================================================
// PCCN Methods
// =====================================================

// CreatePCCN creates a PCCN connection workflow in the AZ layer
func (o *AZOrchestrator) CreatePCCN(ctx context.Context, req *models.PCCNRequest) (*models.PCCNResponse, error) {
	logger.InfoContext(ctx, "开始创建PCCN连接",
		"az", o.az,
		"pccn_name", req.PCCNName,
		"vpc_name", req.VPC1.VPCName,
		"vpc_region", req.VPC1.Region,
		"peer_vpc_name", req.VPC2.VPCName,
		"peer_vpc_region", req.VPC2.Region,
	)

	// Use the PCCN ID from Top layer or generate one
	pccnID := req.PCCNID
	if pccnID == "" {
		pccnID = uuid.New().String()
	}

	// Get VPC info to obtain subnet list
	vpc, err := o.vpcDAO.GetByName(ctx, req.VPC1.VPCName, o.az)
	if err == sql.ErrNoRows {
		return &models.PCCNResponse{
			Success: false,
			Message: fmt.Sprintf("VPC不存在: %s", req.VPC1.VPCName),
		}, nil
	}
	if err != nil {
		return &models.PCCNResponse{
			Success: false,
			Message: fmt.Sprintf("查询VPC失败: %v", err),
		}, nil
	}

	// Get VPC's subnet list
	subnets, err := o.subnetDAO.ListByVPCID(ctx, vpc.ID)
	if err != nil {
		return &models.PCCNResponse{
			Success: false,
			Message: fmt.Sprintf("获取子网列表失败: %v", err),
		}, nil
	}

	// Build subnet CIDR list
	var subnetCIDRs []string
	for _, subnet := range subnets {
		subnetCIDRs = append(subnetCIDRs, subnet.CIDR)
	}

	// Create PCCN resource record
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

	if err := o.pccnDAO.Create(ctx, pccnResource); err != nil {
		return &models.PCCNResponse{
			Success: false,
			Message: fmt.Sprintf("创建PCCN资源记录失败: %v", err),
		}, nil
	}

	// Build task params
	params := o.buildPCCNTaskParams(req, subnetCIDRs)

	// Define workflow
	def := &taskqueue.WorkflowDefinition{
		Name:         "create_pccn",
		ResourceType: string(models.ResourceTypePCCN),
		ResourceID:   pccnID,
		Metadata:     map[string]string{"az": o.az, "vpc_region": req.VPC1.Region, "peer_vpc_region": req.VPC2.Region},
		Steps: []taskqueue.StepDefinition{
			{
				TaskType:   "create_pccn_connection",
				TaskName:   "创建PCCN连接",
				QueueTag:   string(queue.DeviceTypeSwitch),
				Priority:   taskqueue.PriorityNormal,
				Params:     params,
			},
			{
				TaskType:   "configure_pccn_routing",
				TaskName:   "配置PCCN路由",
				QueueTag:   string(queue.DeviceTypeSwitch),
				Priority:   taskqueue.PriorityNormal,
				Params:     params,
			},
		},
	}

	if err := o.pccnDAO.UpdateTotalTasks(ctx, pccnID, len(def.Steps)); err != nil {
		return &models.PCCNResponse{
			Success: false,
			Message: fmt.Sprintf("更新任务总数失败: %v", err),
		}, nil
	}

	if err := o.pccnDAO.UpdateStatus(ctx, pccnID, models.ResourceStatusCreating, ""); err != nil {
		return &models.PCCNResponse{
			Success: false,
			Message: fmt.Sprintf("更新PCCN状态失败: %v", err),
		}, nil
	}

	workflowID, err := o.engine.SubmitWorkflow(ctx, def)
	if err != nil {
		return &models.PCCNResponse{
			Success: false,
			Message: fmt.Sprintf("提交工作流失败: %v", err),
		}, nil
	}

	logger.InfoContext(ctx, "PCCN创建流程启动成功",
		"az", o.az,
		"pccn_name", req.PCCNName,
		"pccn_id", pccnID,
		"workflow_id", workflowID,
		"is_cross_region", req.VPC1.Region != req.VPC2.Region,
	)

	return &models.PCCNResponse{
		Success:  true,
		Message:  "PCCN创建工作流已启动",
		PCCNID:   pccnID,
		TxID:     workflowID,
	}, nil
}

func (o *AZOrchestrator) buildPCCNTaskParams(req *models.PCCNRequest, subnets []string) string {
	params := map[string]interface{}{
		"pccn_id":         req.PCCNID,
		"pccn_name":       req.PCCNName,
		"vpc_name":        req.VPC1.VPCName,
		"vpc_region":      req.VPC1.Region,
		"peer_vpc_name":   req.VPC2.VPCName,
		"peer_vpc_region": req.VPC2.Region,
		"az":              o.az,
		"subnets":         subnets,
	}
	data, _ := json.Marshal(params)
	return string(data)
}

// GetPCCNStatus returns the status of a PCCN connection
func (o *AZOrchestrator) GetPCCNStatus(ctx context.Context, pccnName string) (*models.PCCNStatusResponse, error) {
	pccn, err := o.pccnDAO.GetByName(ctx, pccnName, o.az)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("PCCN不存在: %s", pccnName)
	}
	if err != nil {
		return nil, fmt.Errorf("查询PCCN失败: %v", err)
	}

	tasks, err := o.getTasksByResourceID(ctx, string(models.ResourceTypePCCN), pccn.ID)
	if err != nil {
		return nil, fmt.Errorf("查询任务列表失败: %v", err)
	}

	pending := pccn.TotalTasks - pccn.CompletedTasks - pccn.FailedTasks

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
			Pending:   pending,
		},
		Tasks:        tasks,
		ErrorMessage: pccn.ErrorMessage,
		CreatedAt:    pccn.CreatedAt,
		UpdatedAt:    pccn.UpdatedAt,
	}, nil
}

// DeletePCCN deletes a PCCN connection
func (o *AZOrchestrator) DeletePCCN(ctx context.Context, pccnName string) error {
	pccn, err := o.pccnDAO.GetByName(ctx, pccnName, o.az)
	if err == sql.ErrNoRows {
		return fmt.Errorf("PCCN不存在: %s", pccnName)
	}
	if err != nil {
		return fmt.Errorf("查询PCCN失败: %v", err)
	}

	if pccn.Status != models.ResourceStatusRunning {
		return fmt.Errorf("PCCN状态不是running，无法删除")
	}

	if err := o.pccnDAO.UpdateStatus(ctx, pccn.ID, models.ResourceStatusDeleting, ""); err != nil {
		return fmt.Errorf("更新PCCN状态失败: %v", err)
	}

	// Delete the PCCN record
	if err := o.pccnDAO.DeleteByName(ctx, pccnName, o.az); err != nil {
		return fmt.Errorf("删除PCCN记录失败: %v", err)
	}

	logger.InfoContext(ctx, "PCCN删除成功", "az", o.az, "pccn_name", pccnName)
	return nil
}

// ListPCCNs lists all PCCN connections
func (o *AZOrchestrator) ListPCCNs(ctx context.Context) ([]*models.PCCNResource, error) {
	return o.pccnDAO.ListAll(ctx)
}
