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
	"workflow_qoder/internal/orchestration"
	"workflow_qoder/internal/queue"
)

const staleWorkflowTimeout = 5 * time.Minute

type AZOrchestrator struct {
	vpcDAO      *dao.VPCDAO
	subnetDAO   *dao.SubnetDAO
	pccnDAO     *dao.PCCNDAO
	taskDAO     *dao.TaskDAO
	workflowMgr *orchestration.Manager
	broker      taskqueue.Broker
	inspector   taskqueue.Inspector
	tracedHTTP  *trace.TracedClient
	region      string
	az          string
}

func NewAZOrchestrator(db *sql.DB, broker taskqueue.Broker, inspector taskqueue.Inspector, tracedHTTP *trace.TracedClient, region, az string) *AZOrchestrator {
	taskDAO := dao.NewTaskDAO(db)
	workflowMgr := orchestration.NewManager(
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
		vpcDAO:      dao.NewVPCDAO(db),
		subnetDAO:   dao.NewSubnetDAO(db),
		pccnDAO:     dao.NewPCCNDAO(db),
		taskDAO:     taskDAO,
		workflowMgr: workflowMgr,
		broker:      broker,
		inspector:   inspector,
		tracedHTTP:  tracedHTTP,
		region:      region,
		az:          az,
	}
}

func (o *AZOrchestrator) HandleReplyTask(ctx context.Context, task *taskqueue.Task) error {
	return o.workflowMgr.HandleReply(ctx, task)
}

func (o *AZOrchestrator) ReplyQueueName() string {
	return o.workflowMgr.ReplyQueueName()
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

	if err := o.vpcDAO.Create(ctx, vpcResource); err != nil {
		return &models.VPCResponse{
			Success: false,
			Message: fmt.Sprintf("创建VPC资源记录失败: %v", err),
		}, nil
	}

	params, err := o.buildVPCTaskParams(req)
	if err != nil {
		return &models.VPCResponse{
			Success: false,
			Message: fmt.Sprintf("序列化VPC任务参数失败: %v", err),
		}, nil
	}

	workflowID, err := o.workflowMgr.SubmitWorkflow(ctx, orchestration.WorkflowDef{
		ResourceType: models.ResourceTypeVPC,
		ResourceID:   vpcID,
		AZ:           o.az,
		Steps: []orchestration.WorkflowStep{
			{TaskType: "create_vrf_on_switch", TaskName: "创建VRF", DeviceType: string(queue.DeviceTypeSwitch), Priority: int(taskqueue.PriorityNormal), Payload: params},
			{TaskType: "create_vlan_subinterface", TaskName: "创建VLAN子接口", DeviceType: string(queue.DeviceTypeSwitch), Priority: int(taskqueue.PriorityNormal), Payload: params},
			{TaskType: "create_firewall_zone", TaskName: "创建防火墙安全区域", DeviceType: string(queue.DeviceTypeFirewall), Priority: int(taskqueue.PriorityNormal), Payload: params},
		},
	})
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

	if err := o.subnetDAO.Create(ctx, subnetResource); err != nil {
		return &models.SubnetResponse{
			Success: false,
			Message: fmt.Sprintf("创建子网资源记录失败: %v", err),
		}, nil
	}

	params, err := o.buildSubnetTaskParams(req)
	if err != nil {
		return &models.SubnetResponse{
			Success: false,
			Message: fmt.Sprintf("序列化子网任务参数失败: %v", err),
		}, nil
	}

	workflowID, err := o.workflowMgr.SubmitWorkflow(ctx, orchestration.WorkflowDef{
		ResourceType: models.ResourceTypeSubnet,
		ResourceID:   subnetID,
		AZ:           o.az,
		Steps: []orchestration.WorkflowStep{
			{TaskType: "create_subnet_on_switch", TaskName: "创建子网", DeviceType: string(queue.DeviceTypeSwitch), Priority: int(taskqueue.PriorityNormal), Payload: params},
			{TaskType: "configure_subnet_routing", TaskName: "配置子网路由", DeviceType: string(queue.DeviceTypeSwitch), Priority: int(taskqueue.PriorityNormal), Payload: params},
		},
	})
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
	if task.Status != models.TaskStatusFailed {
		return fmt.Errorf("任务状态不是failed，无法重做 (当前状态: %s)", task.Status)
	}

	if err := o.resetResourceStatus(ctx, task.ResourceType, task.ResourceID); err != nil {
		return err
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

func (o *AZOrchestrator) resetResourceStatus(ctx context.Context, resourceType models.ResourceType, resourceID string) error {
	switch resourceType {
	case models.ResourceTypeVPC:
		return o.vpcDAO.UpdateStatus(ctx, resourceID, models.ResourceStatusCreating, "")
	case models.ResourceTypeSubnet:
		return o.subnetDAO.UpdateStatus(ctx, resourceID, models.ResourceStatusCreating, "")
	case models.ResourceTypePCCN:
		return o.pccnDAO.UpdateStatus(ctx, resourceID, models.ResourceStatusCreating, "")
	default:
		return fmt.Errorf("不支持的资源类型: %s", resourceType)
	}
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
		o.compensateResource(ctx, vpc.ID, vpc.Status, vpc.UpdatedAt, vpc.TotalTasks, o.vpcDAO.UpdateStatus)
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
			o.compensateResource(ctx, subnet.ID, subnet.Status, subnet.UpdatedAt, subnet.TotalTasks, o.subnetDAO.UpdateStatus)
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
		o.compensateResource(ctx, pccn.ID, pccn.Status, pccn.UpdatedAt, pccn.TotalTasks, o.pccnDAO.UpdateStatus)
	}
}

func (o *AZOrchestrator) compensateResource(
	ctx context.Context,
	resourceID string,
	currentStatus models.ResourceStatus,
	updatedAt time.Time,
	totalTasks int,
	updateStatus func(ctx context.Context, id string, status models.ResourceStatus, errMsg string) error,
) {
	if currentStatus == models.ResourceStatusRunning || currentStatus == models.ResourceStatusFailed || currentStatus == models.ResourceStatusDeleted {
		return
	}

	total, completed, failed, err := o.taskDAO.GetTaskStats(ctx, resourceID)
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
	if o.resourceHasInFlightBrokerTask(ctx, resourceID) {
		return
	}
	if time.Since(updatedAt) > staleWorkflowTimeout {
		_ = updateStatus(ctx, resourceID, models.ResourceStatusFailed, "workflow reply timeout")
	}
}

func (o *AZOrchestrator) resourceHasInFlightBrokerTask(ctx context.Context, resourceID string) bool {
	tasks, err := o.taskDAO.GetByResourceID(ctx, resourceID)
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

	if err := o.pccnDAO.Create(ctx, pccnResource); err != nil {
		return &models.PCCNResponse{Success: false, Message: fmt.Sprintf("创建PCCN资源记录失败: %v", err)}, nil
	}

	params, err := o.buildPCCNTaskParams(pccnID, req, subnetCIDRs)
	if err != nil {
		return &models.PCCNResponse{Success: false, Message: fmt.Sprintf("序列化PCCN任务参数失败: %v", err)}, nil
	}

	workflowID, err := o.workflowMgr.SubmitWorkflow(ctx, orchestration.WorkflowDef{
		ResourceType: models.ResourceTypePCCN,
		ResourceID:   pccnID,
		AZ:           o.az,
		Steps: []orchestration.WorkflowStep{
			{TaskType: "create_pccn_connection", TaskName: "创建PCCN连接", DeviceType: string(queue.DeviceTypeSwitch), Priority: int(taskqueue.PriorityNormal), Payload: params},
			{TaskType: "configure_pccn_routing", TaskName: "配置PCCN路由", DeviceType: string(queue.DeviceTypeSwitch), Priority: int(taskqueue.PriorityNormal), Payload: params},
		},
	})
	if err != nil {
		return &models.PCCNResponse{Success: false, Message: fmt.Sprintf("提交工作流失败: %v", err)}, nil
	}

	logger.InfoContext(ctx, "PCCN创建流程启动成功",
		"az", o.az,
		"pccn_name", req.PCCNName,
		"pccn_id", pccnID,
		"workflow_id", workflowID,
	)

	return &models.PCCNResponse{
		Success: true,
		Message: "PCCN创建工作流已启动",
		PCCNID:  pccnID,
		TxID:    workflowID,
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
	if err := o.pccnDAO.DeleteByName(ctx, pccnName, o.az); err != nil {
		return fmt.Errorf("删除PCCN记录失败: %v", err)
	}

	logger.InfoContext(ctx, "PCCN删除成功", "az", o.az, "pccn_name", pccnName)
	return nil
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
