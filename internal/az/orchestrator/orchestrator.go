package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/yourorg/nsp-common/pkg/logger"
	"github.com/yourorg/nsp-common/pkg/taskqueue"

	"workflow_qoder/internal/db/dao"
	"workflow_qoder/internal/models"
	"workflow_qoder/internal/queue"

	"github.com/google/uuid"
)

type AZOrchestrator struct {
	vpcDAO    *dao.VPCDAO
	subnetDAO *dao.SubnetDAO
	taskDAO   *dao.TaskDAO
	broker    taskqueue.Broker
	region    string
	az        string
}

func NewAZOrchestrator(db *sql.DB, broker taskqueue.Broker, region, az string) *AZOrchestrator {
	return &AZOrchestrator{
		vpcDAO:    dao.NewVPCDAO(db),
		subnetDAO: dao.NewSubnetDAO(db),
		taskDAO:   dao.NewTaskDAO(db),
		broker:    broker,
		region:    region,
		az:        az,
	}
}

func (o *AZOrchestrator) CreateVPC(ctx context.Context, req *models.VPCRequest) (*models.VPCResponse, error) {
	logger.InfoContext(ctx, "开始创建VPC", "az", o.az, "vpcName", req.VPCName)

	vpcID := uuid.New().String()
	
	vpcResource := &models.VPCResource{
		ID:           vpcID,
		VPCName:      req.VPCName,
		Region:       req.Region,
		AZ:           o.az,
		VRFName:      req.VRFName,
		VLANId:       req.VLANId,
		FirewallZone: req.FirewallZone,
		Status:       models.ResourceStatusPending,
		TotalTasks:   0,
		CompletedTasks: 0,
		FailedTasks:  0,
	}

	if err := o.vpcDAO.Create(ctx, vpcResource); err != nil {
		return &models.VPCResponse{
			Success: false,
			Message: fmt.Sprintf("创建VPC资源记录失败: %v", err),
		}, nil
	}

	tasks := o.buildVPCTasks(vpcID, req)
	
	if err := o.taskDAO.BatchCreate(ctx, tasks); err != nil {
		return &models.VPCResponse{
			Success: false,
			Message: fmt.Sprintf("创建任务记录失败: %v", err),
		}, nil
	}

	if err := o.vpcDAO.UpdateTotalTasks(ctx, vpcID, len(tasks)); err != nil {
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

	if err := o.enqueueFirstTask(ctx, vpcID); err != nil {
		return &models.VPCResponse{
			Success: false,
			Message: fmt.Sprintf("任务入队失败: %v", err),
		}, nil
	}

	logger.InfoContext(ctx, "VPC创建流程启动成功", "az", o.az, "vpcName", req.VPCName, "vpcID", vpcID)

	return &models.VPCResponse{
		Success:    true,
		Message:    "VPC创建工作流已启动",
		VPCID:      vpcID,
		WorkflowID: vpcID,
	}, nil
}

func (o *AZOrchestrator) buildVPCTasks(vpcID string, req *models.VPCRequest) []*models.Task {
	tasks := []*models.Task{
		{
			ID:           uuid.New().String(),
			ResourceType: models.ResourceTypeVPC,
			ResourceID:   vpcID,
			TaskType:     "create_vrf_on_switch",
			TaskName:     "创建VRF",
			TaskOrder:    1,
			TaskParams:   o.buildVPCTaskParams(req),
			Status:       models.TaskStatusPending,
			Priority:     int(queue.PriorityNormal),
			DeviceType:   string(queue.DeviceTypeSwitch),
			RetryCount:   0,
			MaxRetries:   3,
			AZ:           o.az,
		},
		{
			ID:           uuid.New().String(),
			ResourceType: models.ResourceTypeVPC,
			ResourceID:   vpcID,
			TaskType:     "create_vlan_subinterface",
			TaskName:     "创建VLAN子接口",
			TaskOrder:    2,
			TaskParams:   o.buildVPCTaskParams(req),
			Status:       models.TaskStatusPending,
			Priority:     int(queue.PriorityNormal),
			DeviceType:   string(queue.DeviceTypeSwitch),
			RetryCount:   0,
			MaxRetries:   3,
			AZ:           o.az,
		},
		{
			ID:           uuid.New().String(),
			ResourceType: models.ResourceTypeVPC,
			ResourceID:   vpcID,
			TaskType:     "create_firewall_zone",
			TaskName:     "创建防火墙安全区域",
			TaskOrder:    3,
			TaskParams:   o.buildVPCTaskParams(req),
			Status:       models.TaskStatusPending,
			Priority:     int(queue.PriorityNormal),
			DeviceType:   string(queue.DeviceTypeFirewall),
			RetryCount:   0,
			MaxRetries:   3,
			AZ:           o.az,
		},
	}
	return tasks
}

func (o *AZOrchestrator) buildVPCTaskParams(req *models.VPCRequest) string {
	params := map[string]interface{}{
		"vpc_name":      req.VPCName,
		"vrf_name":      req.VRFName,
		"vlan_id":       req.VLANId,
		"firewall_zone": req.FirewallZone,
		"region":        req.Region,
	}
	data, _ := json.Marshal(params)
	return string(data)
}

func (o *AZOrchestrator) CreateSubnet(ctx context.Context, req *models.SubnetRequest) (*models.SubnetResponse, error) {
	logger.InfoContext(ctx, "开始创建子网", "az", o.az, "subnetName", req.SubnetName)

	subnetID := uuid.New().String()
	
	subnetResource := &models.SubnetResource{
		ID:         subnetID,
		SubnetName: req.SubnetName,
		VPCName:    req.VPCName,
		Region:     req.Region,
		AZ:         o.az,
		CIDR:       req.CIDR,
		Status:     models.ResourceStatusPending,
		TotalTasks: 0,
		CompletedTasks: 0,
		FailedTasks: 0,
	}

	if err := o.subnetDAO.Create(ctx, subnetResource); err != nil {
		return &models.SubnetResponse{
			Success: false,
			Message: fmt.Sprintf("创建子网资源记录失败: %v", err),
		}, nil
	}

	tasks := o.buildSubnetTasks(subnetID, req)
	
	if err := o.taskDAO.BatchCreate(ctx, tasks); err != nil {
		return &models.SubnetResponse{
			Success: false,
			Message: fmt.Sprintf("创建任务记录失败: %v", err),
		}, nil
	}

	if err := o.subnetDAO.UpdateTotalTasks(ctx, subnetID, len(tasks)); err != nil {
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

	if err := o.enqueueFirstTask(ctx, subnetID); err != nil {
		return &models.SubnetResponse{
			Success: false,
			Message: fmt.Sprintf("任务入队失败: %v", err),
		}, nil
	}

	logger.InfoContext(ctx, "子网创建流程启动成功", "az", o.az, "subnetName", req.SubnetName, "subnetID", subnetID)

	return &models.SubnetResponse{
		Success:    true,
		Message:    "子网创建工作流已启动",
		SubnetID:   subnetID,
		WorkflowID: subnetID,
	}, nil
}

func (o *AZOrchestrator) buildSubnetTasks(subnetID string, req *models.SubnetRequest) []*models.Task {
	tasks := []*models.Task{
		{
			ID:           uuid.New().String(),
			ResourceType: models.ResourceTypeSubnet,
			ResourceID:   subnetID,
			TaskType:     "create_subnet_on_switch",
			TaskName:     "创建子网",
			TaskOrder:    1,
			TaskParams:   o.buildSubnetTaskParams(req),
			Status:       models.TaskStatusPending,
			Priority:     int(queue.PriorityNormal),
			DeviceType:   string(queue.DeviceTypeSwitch),
			RetryCount:   0,
			MaxRetries:   3,
			AZ:           o.az,
		},
		{
			ID:           uuid.New().String(),
			ResourceType: models.ResourceTypeSubnet,
			ResourceID:   subnetID,
			TaskType:     "configure_subnet_routing",
			TaskName:     "配置子网路由",
			TaskOrder:    2,
			TaskParams:   o.buildSubnetTaskParams(req),
			Status:       models.TaskStatusPending,
			Priority:     int(queue.PriorityNormal),
			DeviceType:   string(queue.DeviceTypeSwitch),
			RetryCount:   0,
			MaxRetries:   3,
			AZ:           o.az,
		},
	}
	return tasks
}

func (o *AZOrchestrator) buildSubnetTaskParams(req *models.SubnetRequest) string {
	params := map[string]interface{}{
		"subnet_name": req.SubnetName,
		"vpc_name":    req.VPCName,
		"region":      req.Region,
		"az":          req.AZ,
		"cidr":        req.CIDR,
	}
	data, _ := json.Marshal(params)
	return string(data)
}

func (o *AZOrchestrator) enqueueFirstTask(ctx context.Context, resourceID string) error {
	task, err := o.taskDAO.GetNextPendingTask(ctx, resourceID)
	if err != nil {
		return fmt.Errorf("获取首个待执行任务失败: %v", err)
	}
	if task == nil {
		return fmt.Errorf("没有找到待执行任务")
	}

	return o.enqueueTask(ctx, task)
}

func (o *AZOrchestrator) enqueueTask(ctx context.Context, task *models.Task) error {
	payload := map[string]interface{}{
		"task_id":     task.ID,
		"resource_id": task.ResourceID,
		"task_params": task.TaskParams,
	}
	payloadData, _ := json.Marshal(payload)

	deviceType := queue.DeviceType(task.DeviceType)
	if deviceType == "" {
		deviceType = queue.GetDeviceTypeForTaskType(task.TaskType)
	}

	priority := queue.TaskPriority(task.Priority)
	if priority == 0 {
		priority = queue.PriorityNormal
	}

	queueName := queue.GetPriorityQueueName(o.region, o.az, deviceType, priority)

	tqTask := &taskqueue.Task{
		Type:    task.TaskType,
		Payload: payloadData,
		Queue:   queueName,
	}
	info, err := o.broker.Publish(ctx, tqTask)
	if err != nil {
		return fmt.Errorf("入队失败: %v", err)
	}

	if err := o.taskDAO.UpdateAsynqTaskID(ctx, task.ID, info.BrokerTaskID); err != nil {
		return fmt.Errorf("更新Broker任务ID失败: %v", err)
	}

	if err := o.taskDAO.UpdateStatus(ctx, task.ID, models.TaskStatusQueued); err != nil {
		return fmt.Errorf("更新任务状态失败: %v", err)
	}

	logger.InfoContext(ctx, "任务已入队", "az", o.az, "taskName", task.TaskName, "brokerID", info.BrokerTaskID, "queue", queueName, "priority", priority)
	return nil
}

func (o *AZOrchestrator) HandleTaskCallback(ctx context.Context, taskID string, status models.TaskStatus, result interface{}, errorMsg string) error {
	logger.InfoContext(ctx, "接收到任务回调", "az", o.az, "taskID", taskID, "status", status)

	task, err := o.taskDAO.GetByID(ctx, taskID)
	if err != nil {
		return fmt.Errorf("获取任务失败: %v", err)
	}

	if err := o.taskDAO.UpdateResult(ctx, taskID, status, result, errorMsg); err != nil {
		return fmt.Errorf("更新任务结果失败: %v", err)
	}

	if status == models.TaskStatusCompleted {
		if err := o.handleTaskSuccess(ctx, task); err != nil {
			return err
		}
	} else if status == models.TaskStatusFailed {
		if err := o.handleTaskFailure(ctx, task, errorMsg); err != nil {
			return err
		}
	}

	return nil
}

func (o *AZOrchestrator) handleTaskSuccess(ctx context.Context, task *models.Task) error {
	if task.ResourceType == models.ResourceTypeVPC {
		if err := o.vpcDAO.IncrementCompletedTasks(ctx, task.ResourceID); err != nil {
			return fmt.Errorf("更新VPC完成任务数失败: %v", err)
		}
	} else if task.ResourceType == models.ResourceTypeSubnet {
		if err := o.subnetDAO.IncrementCompletedTasks(ctx, task.ResourceID); err != nil {
			return fmt.Errorf("更新子网完成任务数失败: %v", err)
		}
	}

	nextTask, err := o.taskDAO.GetNextPendingTask(ctx, task.ResourceID)
	if err != nil {
		return fmt.Errorf("获取下一个任务失败: %v", err)
	}

	if nextTask != nil {
		if err := o.enqueueTask(ctx, nextTask); err != nil {
			return fmt.Errorf("入队下一个任务失败: %v", err)
		}
	} else {
		if err := o.checkAndCompleteResource(ctx, task.ResourceID, task.ResourceType); err != nil {
			return fmt.Errorf("检查资源完成状态失败: %v", err)
		}
	}

	return nil
}

func (o *AZOrchestrator) handleTaskFailure(ctx context.Context, task *models.Task, errorMsg string) error {
	if task.ResourceType == models.ResourceTypeVPC {
		if err := o.vpcDAO.IncrementFailedTasks(ctx, task.ResourceID); err != nil {
			return fmt.Errorf("更新VPC失败任务数失败: %v", err)
		}
		if err := o.vpcDAO.UpdateStatus(ctx, task.ResourceID, models.ResourceStatusFailed, errorMsg); err != nil {
			return fmt.Errorf("更新VPC状态失败: %v", err)
		}
	} else if task.ResourceType == models.ResourceTypeSubnet {
		if err := o.subnetDAO.IncrementFailedTasks(ctx, task.ResourceID); err != nil {
			return fmt.Errorf("更新子网失败任务数失败: %v", err)
		}
		if err := o.subnetDAO.UpdateStatus(ctx, task.ResourceID, models.ResourceStatusFailed, errorMsg); err != nil {
			return fmt.Errorf("更新子网状态失败: %v", err)
		}
	}

	logger.InfoContext(ctx, "任务失败，停止后续任务", "az", o.az, "resourceID", task.ResourceID)
	return nil
}

func (o *AZOrchestrator) checkAndCompleteResource(ctx context.Context, resourceID string, resourceType models.ResourceType) error {
	total, completed, failed, err := o.taskDAO.GetTaskStats(ctx, resourceID)
	if err != nil {
		return fmt.Errorf("获取任务统计失败: %v", err)
	}

	logger.InfoContext(ctx, "任务统计", "az", o.az, "total", total, "completed", completed, "failed", failed)

	if completed == total && failed == 0 {
		if resourceType == models.ResourceTypeVPC {
			if err := o.vpcDAO.UpdateStatus(ctx, resourceID, models.ResourceStatusRunning, ""); err != nil {
				return fmt.Errorf("更新VPC状态为running失败: %v", err)
			}
			logger.InfoContext(ctx, "VPC创建完成", "az", o.az, "resourceID", resourceID)
		} else if resourceType == models.ResourceTypeSubnet {
			if err := o.subnetDAO.UpdateStatus(ctx, resourceID, models.ResourceStatusRunning, ""); err != nil {
				return fmt.Errorf("更新子网状态为running失败: %v", err)
			}
			logger.InfoContext(ctx, "子网创建完成", "az", o.az, "resourceID", resourceID)
		}
	}

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

	tasks, err := o.taskDAO.GetByResourceID(ctx, vpc.ID)
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

	tasks, err := o.taskDAO.GetByResourceID(ctx, subnet.ID)
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

	policyCount, err := o.checkZonePolicies(vpc.FirewallZone)
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

	policyCount, err := o.checkZonePolicies(vpc.FirewallZone)
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
	return o.taskDAO.GetByID(ctx, taskID)
}

func (o *AZOrchestrator) ReplayTask(ctx context.Context, taskID string) error {
	task, err := o.taskDAO.GetByID(ctx, taskID)
	if err != nil {
		return fmt.Errorf("获取任务失败: %v", err)
	}

	if task.Status != models.TaskStatusFailed {
		return fmt.Errorf("任务状态不是failed，无法重做 (当前状态: %s)", task.Status)
	}

	if err := o.taskDAO.UpdateStatus(ctx, taskID, models.TaskStatusPending); err != nil {
		return fmt.Errorf("更新任务状态为pending失败: %v", err)
	}

	if err := o.enqueueTask(ctx, task); err != nil {
		return fmt.Errorf("重新入队任务失败: %v", err)
	}

	logger.InfoContext(ctx, "任务重做成功", "az", o.az, "taskID", taskID)
	return nil
}

func (o *AZOrchestrator) checkZonePolicies(zone string) (int, error) {
	vfwAddr := os.Getenv("AZ_NSP_VFW_ADDR")
	if vfwAddr == "" {
		vfwAddr = fmt.Sprintf("http://az-nsp-vfw-%s:8080", o.az)
	}

	url := fmt.Sprintf("%s/api/v1/firewall/zone/%s/policy-count", vfwAddr, zone)
	resp, err := http.Get(url)
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