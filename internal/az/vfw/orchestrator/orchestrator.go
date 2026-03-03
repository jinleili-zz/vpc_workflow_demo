package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"workflow_qoder/internal/az/vfw/dao"
	"workflow_qoder/internal/models"
	"workflow_qoder/internal/queue"

	"github.com/google/uuid"
	"github.com/yourorg/nsp-common/pkg/logger"
	"github.com/yourorg/nsp-common/pkg/taskqueue"
	"github.com/yourorg/nsp-common/pkg/trace"
)

type VFWOrchestrator struct {
	policyDAO  *dao.FirewallPolicyDAO
	taskDAO    *dao.VFWTaskDAO
	broker     taskqueue.Broker
	tracedHTTP *trace.TracedClient
	region     string
	az         string
}

func NewVFWOrchestrator(db *sql.DB, broker taskqueue.Broker, tracedHTTP *trace.TracedClient, region, az string) *VFWOrchestrator {
	return &VFWOrchestrator{
		policyDAO:  dao.NewFirewallPolicyDAO(db),
		taskDAO:    dao.NewVFWTaskDAO(db),
		broker:     broker,
		tracedHTTP: tracedHTTP,
		region:     region,
		az:         az,
	}
}

func (o *VFWOrchestrator) CreatePolicy(ctx context.Context, req *models.AZFirewallPolicyRequest) (*models.AZFirewallPolicyResponse, error) {
	logger.InfoContext(ctx, "开始创建防火墙策略", "az", o.az, "policy_name", req.PolicyName)

	policyID := uuid.New().String()

	policy := &models.FirewallPolicy{
		ID:          policyID,
		PolicyName:  req.PolicyName,
		SourceZone:  req.SourceZone,
		DestZone:    req.DestZone,
		SourceIP:    req.SourceIP,
		DestIP:      req.DestIP,
		SourcePort:  req.SourcePort,
		DestPort:    req.DestPort,
		Protocol:    req.Protocol,
		Action:      req.Action,
		Description: req.Description,
		Status:      models.ResourceStatusPending,
		Region:      o.region,
		AZ:          o.az,
	}

	if err := o.policyDAO.Create(ctx, policy); err != nil {
		return &models.AZFirewallPolicyResponse{
			Success: false,
			Message: fmt.Sprintf("创建策略记录失败: %v", err),
		}, nil
	}

	tasks := o.buildPolicyTasks(policyID, req)

	if err := o.taskDAO.BatchCreate(ctx, tasks); err != nil {
		return &models.AZFirewallPolicyResponse{
			Success: false,
			Message: fmt.Sprintf("创建任务记录失败: %v", err),
		}, nil
	}

	if err := o.policyDAO.UpdateTotalTasks(ctx, policyID, len(tasks)); err != nil {
		return &models.AZFirewallPolicyResponse{
			Success: false,
			Message: fmt.Sprintf("更新任务总数失败: %v", err),
		}, nil
	}

	if err := o.policyDAO.UpdateStatus(ctx, policyID, models.ResourceStatusCreating, ""); err != nil {
		return &models.AZFirewallPolicyResponse{
			Success: false,
			Message: fmt.Sprintf("更新策略状态失败: %v", err),
		}, nil
	}

	if err := o.enqueueFirstTask(ctx, policyID); err != nil {
		return &models.AZFirewallPolicyResponse{
			Success: false,
			Message: fmt.Sprintf("任务入队失败: %v", err),
		}, nil
	}

	logger.InfoContext(ctx, "防火墙策略创建流程启动成功", "az", o.az, "policy_name", req.PolicyName, "policy_id", policyID)

	return &models.AZFirewallPolicyResponse{
		Success:    true,
		Message:    "防火墙策略创建工作流已启动",
		PolicyID:   policyID,
		WorkflowID: policyID,
	}, nil
}

func (o *VFWOrchestrator) buildPolicyTasks(policyID string, req *models.AZFirewallPolicyRequest) []*models.Task {
	params := o.buildPolicyTaskParams(req)

	tasks := []*models.Task{
		{
			ID:           uuid.New().String(),
			ResourceType: models.ResourceTypeFirewallPolicy,
			ResourceID:   policyID,
			TaskType:     "create_firewall_policy",
			TaskName:     "创建防火墙策略",
			TaskOrder:    1,
			TaskParams:   params,
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

func (o *VFWOrchestrator) buildPolicyTaskParams(req *models.AZFirewallPolicyRequest) string {
	params := map[string]interface{}{
		"policy_name": req.PolicyName,
		"source_zone": req.SourceZone,
		"dest_zone":   req.DestZone,
		"source_ip":   req.SourceIP,
		"dest_ip":     req.DestIP,
		"source_port": req.SourcePort,
		"dest_port":   req.DestPort,
		"protocol":    req.Protocol,
		"action":      req.Action,
		"description": req.Description,
		"region":      req.Region,
		"az":          req.AZ,
	}
	data, _ := json.Marshal(params)
	return string(data)
}

func (o *VFWOrchestrator) enqueueFirstTask(ctx context.Context, resourceID string) error {
	task, err := o.taskDAO.GetNextPendingTask(ctx, resourceID)
	if err != nil {
		return fmt.Errorf("获取首个待执行任务失败: %v", err)
	}
	if task == nil {
		return fmt.Errorf("没有找到待执行任务")
	}

	return o.enqueueTask(ctx, task)
}

func (o *VFWOrchestrator) enqueueTask(ctx context.Context, task *models.Task) error {
	payload := map[string]interface{}{
		"task_id":     task.ID,
		"resource_id": task.ResourceID,
		"task_params": task.TaskParams,
	}
	payloadData, _ := json.Marshal(payload)

	deviceType := queue.DeviceType(task.DeviceType)
	if deviceType == "" {
		deviceType = queue.DeviceTypeFirewall
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

	logger.InfoContext(ctx, "任务已入队", "az", o.az, "task_name", task.TaskName, "broker_id", info.BrokerTaskID, "queue", queueName)
	return nil
}

func (o *VFWOrchestrator) HandleTaskCallback(ctx context.Context, taskID string, status models.TaskStatus, result interface{}, errorMsg string) error {
	logger.InfoContext(ctx, "接收到任务回调", "az", o.az, "task_id", taskID, "status", status)

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

func (o *VFWOrchestrator) handleTaskSuccess(ctx context.Context, task *models.Task) error {
	if err := o.policyDAO.IncrementCompletedTasks(ctx, task.ResourceID); err != nil {
		return fmt.Errorf("更新策略完成任务数失败: %v", err)
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
		if err := o.checkAndCompletePolicy(ctx, task.ResourceID); err != nil {
			return fmt.Errorf("检查策略完成状态失败: %v", err)
		}
	}

	return nil
}

func (o *VFWOrchestrator) handleTaskFailure(ctx context.Context, task *models.Task, errorMsg string) error {
	if err := o.policyDAO.IncrementFailedTasks(ctx, task.ResourceID); err != nil {
		return fmt.Errorf("更新策略失败任务数失败: %v", err)
	}
	if err := o.policyDAO.UpdateStatus(ctx, task.ResourceID, models.ResourceStatusFailed, errorMsg); err != nil {
		return fmt.Errorf("更新策略状态失败: %v", err)
	}

	logger.WarnContext(ctx, "任务失败，停止后续任务", "az", o.az, "resource_id", task.ResourceID)
	return nil
}

func (o *VFWOrchestrator) checkAndCompletePolicy(ctx context.Context, resourceID string) error {
	total, completed, failed, err := o.taskDAO.GetTaskStats(ctx, resourceID)
	if err != nil {
		return fmt.Errorf("获取任务统计失败: %v", err)
	}

	logger.InfoContext(ctx, "任务统计", "az", o.az, "total", total, "completed", completed, "failed", failed)

	if completed == total && failed == 0 {
		if err := o.policyDAO.UpdateStatus(ctx, resourceID, models.ResourceStatusRunning, ""); err != nil {
			return fmt.Errorf("更新策略状态为running失败: %v", err)
		}
		logger.InfoContext(ctx, "防火墙策略创建完成", "az", o.az, "resource_id", resourceID)
	}

	return nil
}

func (o *VFWOrchestrator) GetPolicyStatus(ctx context.Context, policyName string) (*models.FirewallPolicyStatusResponse, error) {
	policy, err := o.policyDAO.GetByName(ctx, policyName, o.az)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("策略不存在: %s", policyName)
	}
	if err != nil {
		return nil, fmt.Errorf("查询策略失败: %v", err)
	}

	tasks, err := o.taskDAO.GetByResourceID(ctx, policy.ID)
	if err != nil {
		return nil, fmt.Errorf("查询任务列表失败: %v", err)
	}

	pending := policy.TotalTasks - policy.CompletedTasks - policy.FailedTasks

	return &models.FirewallPolicyStatusResponse{
		PolicyID:   policy.ID,
		PolicyName: policy.PolicyName,
		SourceZone: policy.SourceZone,
		DestZone:   policy.DestZone,
		Status:     policy.Status,
		Progress: models.ResourceProgress{
			Total:     policy.TotalTasks,
			Completed: policy.CompletedTasks,
			Failed:    policy.FailedTasks,
			Pending:   pending,
		},
		Tasks:        tasks,
		ErrorMessage: policy.ErrorMessage,
		CreatedAt:    policy.CreatedAt,
		UpdatedAt:    policy.UpdatedAt,
	}, nil
}

func (o *VFWOrchestrator) DeletePolicy(ctx context.Context, policyName string) error {
	policy, err := o.policyDAO.GetByName(ctx, policyName, o.az)
	if err == sql.ErrNoRows {
		return fmt.Errorf("策略不存在: %s", policyName)
	}
	if err != nil {
		return fmt.Errorf("查询策略失败: %v", err)
	}

	if err := o.policyDAO.UpdateStatus(ctx, policy.ID, models.ResourceStatusDeleted, ""); err != nil {
		return fmt.Errorf("更新策略状态失败: %v", err)
	}

	logger.InfoContext(ctx, "策略删除成功", "az", o.az, "policy_name", policyName)
	return nil
}

func (o *VFWOrchestrator) ListPolicies(ctx context.Context) ([]*models.FirewallPolicy, error) {
	return o.policyDAO.ListAll(ctx)
}

func (o *VFWOrchestrator) GetPolicyByID(ctx context.Context, id string) (*models.FirewallPolicy, error) {
	return o.policyDAO.GetByID(ctx, id)
}

func (o *VFWOrchestrator) CountPoliciesByZone(ctx context.Context, zone string) (int, error) {
	return o.policyDAO.CountByZone(ctx, zone)
}

func (o *VFWOrchestrator) GetTaskByID(ctx context.Context, taskID string) (*models.Task, error) {
	return o.taskDAO.GetByID(ctx, taskID)
}
