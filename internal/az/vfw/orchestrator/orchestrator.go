package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jinleili-zz/nsp-platform/logger"
	"github.com/jinleili-zz/nsp-platform/taskqueue"
	"github.com/jinleili-zz/nsp-platform/trace"

	vfwdao "workflow_qoder/internal/az/vfw/dao"
	dbdao "workflow_qoder/internal/db/dao"
	"workflow_qoder/internal/models"
	"workflow_qoder/internal/orchestration"
	"workflow_qoder/internal/queue"
)

const stalePolicyTimeout = 5 * time.Minute

type VFWOrchestrator struct {
	policyDAO   *vfwdao.FirewallPolicyDAO
	taskDAO     *dbdao.TaskDAO
	workflowMgr *orchestration.Manager
	inspector   taskqueue.Inspector
	tracedHTTP  *trace.TracedClient
	region      string
	az          string
}

func NewVFWOrchestrator(db *sql.DB, broker taskqueue.Broker, inspector taskqueue.Inspector, tracedHTTP *trace.TracedClient, region, az string) *VFWOrchestrator {
	taskDAO := dbdao.NewTaskDAO(db)
	workflowMgr := orchestration.NewManager(
		broker,
		taskDAO,
		func(deviceType string, priority int) string {
			return queue.GetPriorityQueueName(region, az, queue.DeviceType(deviceType), queue.TaskPriority(priority))
		},
		queue.GetReplyQueueName(region, az, "vfw"),
	)
	workflowMgr.RegisterResourceStore(models.ResourceTypeFirewallPolicy, vfwdao.NewFirewallPolicyDAO(db))

	return &VFWOrchestrator{
		policyDAO:   vfwdao.NewFirewallPolicyDAO(db),
		taskDAO:     taskDAO,
		workflowMgr: workflowMgr,
		inspector:   inspector,
		tracedHTTP:  tracedHTTP,
		region:      region,
		az:          az,
	}
}

func (o *VFWOrchestrator) HandleReplyTask(ctx context.Context, task *taskqueue.Task) error {
	return o.workflowMgr.HandleReply(ctx, task)
}

func (o *VFWOrchestrator) ReplyQueueName() string {
	return o.workflowMgr.ReplyQueueName()
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
		return &models.AZFirewallPolicyResponse{Success: false, Message: fmt.Sprintf("创建策略记录失败: %v", err)}, nil
	}

	params, err := o.buildPolicyTaskParams(req)
	if err != nil {
		return &models.AZFirewallPolicyResponse{Success: false, Message: fmt.Sprintf("序列化策略参数失败: %v", err)}, nil
	}

	workflowID, err := o.workflowMgr.SubmitWorkflow(ctx, orchestration.WorkflowDef{
		ResourceType: models.ResourceTypeFirewallPolicy,
		ResourceID:   policyID,
		AZ:           o.az,
		Steps: []orchestration.WorkflowStep{
			{
				TaskType:   "create_firewall_policy",
				TaskName:   "创建防火墙策略",
				DeviceType: string(queue.DeviceTypeFirewall),
				Priority:   int(taskqueue.PriorityNormal),
				MaxRetries: 3,
				Payload:    params,
			},
		},
	})
	if err != nil {
		return &models.AZFirewallPolicyResponse{Success: false, Message: fmt.Sprintf("提交工作流失败: %v", err)}, nil
	}

	logger.InfoContext(ctx, "防火墙策略创建流程启动成功", "az", o.az, "policy_name", req.PolicyName, "policy_id", policyID, "workflowID", workflowID)

	return &models.AZFirewallPolicyResponse{
		Success:    true,
		Message:    "防火墙策略创建工作流已启动",
		PolicyID:   policyID,
		WorkflowID: workflowID,
	}, nil
}

func (o *VFWOrchestrator) buildPolicyTaskParams(req *models.AZFirewallPolicyRequest) ([]byte, error) {
	return json.Marshal(map[string]any{
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
	})
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
			Pending:   maxInt(policy.TotalTasks-policy.CompletedTasks-policy.FailedTasks, 0),
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
	task, err := o.taskDAO.GetByID(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("查询任务失败: %v", err)
	}
	return o.enrichTaskStatus(ctx, task), nil
}

func (o *VFWOrchestrator) StartCompensationTask(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		logger.Platform().Info("[VFW补偿任务] 启动", "az", o.az, "interval", interval.String())

		for {
			select {
			case <-ctx.Done():
				logger.Platform().Info("[VFW补偿任务] 停止", "az", o.az)
				return
			case <-ticker.C:
				o.runCompensation(ctx)
			}
		}
	}()
}

func (o *VFWOrchestrator) runCompensation(ctx context.Context) {
	policies, err := o.policyDAO.ListAll(ctx)
	if err != nil {
		logger.Platform().Error("[VFW补偿任务] 查询策略列表失败", "az", o.az, "error", err)
		return
	}

	for _, policy := range policies {
		if policy.Status == models.ResourceStatusRunning || policy.Status == models.ResourceStatusFailed || policy.Status == models.ResourceStatusDeleted {
			continue
		}

		total, completed, failed, err := o.taskDAO.GetTaskStats(ctx, policy.ID)
		if err != nil {
			logger.Platform().Error("[VFW补偿任务] 查询任务统计失败", "az", o.az, "policyID", policy.ID, "error", err)
			continue
		}
		if total > 0 && completed == total {
			_ = o.policyDAO.UpdateStatus(ctx, policy.ID, models.ResourceStatusRunning, "")
			continue
		}
		if failed > 0 {
			_ = o.policyDAO.UpdateStatus(ctx, policy.ID, models.ResourceStatusFailed, "workflow step failed")
			continue
		}
		if o.resourceHasInFlightBrokerTask(ctx, policy.ID) {
			continue
		}
		if time.Since(policy.UpdatedAt) > stalePolicyTimeout {
			_ = o.policyDAO.UpdateStatus(ctx, policy.ID, models.ResourceStatusFailed, "workflow reply timeout")
		}
	}
}

func (o *VFWOrchestrator) enrichTaskStatus(ctx context.Context, task *models.Task) *models.Task {
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

func (o *VFWOrchestrator) resolveQueueName(deviceType string, priority int) string {
	return queue.GetPriorityQueueName(o.region, o.az, queue.DeviceType(deviceType), queue.TaskPriority(priority))
}

func (o *VFWOrchestrator) resourceHasInFlightBrokerTask(ctx context.Context, resourceID string) bool {
	tasks, err := o.taskDAO.GetByResourceID(ctx, resourceID)
	if err != nil {
		logger.Platform().Error("[VFW补偿任务] 查询任务列表失败", "az", o.az, "resourceID", resourceID, "error", err)
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
				logger.Platform().Warn("[VFW补偿任务] 查询broker任务状态失败", "az", o.az, "taskID", task.ID, "asynqTaskID", task.AsynqTaskID, "error", err)
			}
			continue
		}

		if task.Status == models.TaskStatusQueued || task.Status == models.TaskStatusRunning {
			return true
		}
	}
	return false
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

func taskIsInFlight(state taskqueue.TaskState) bool {
	switch state {
	case taskqueue.TaskStatePending, taskqueue.TaskStateScheduled, taskqueue.TaskStateActive, taskqueue.TaskStateRetry:
		return true
	default:
		return false
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
