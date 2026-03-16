package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"workflow_qoder/internal/az/vfw/dao"
	"workflow_qoder/internal/models"
	"workflow_qoder/internal/queue"

	"github.com/google/uuid"
	"github.com/paic/nsp-common/pkg/logger"
	"github.com/paic/nsp-common/pkg/taskqueue"
	"github.com/paic/nsp-common/pkg/trace"
)

type VFWOrchestrator struct {
	policyDAO  *dao.FirewallPolicyDAO
	engine     *taskqueue.Engine
	tracedHTTP *trace.TracedClient
	region     string
	az         string
}

func NewVFWOrchestrator(db *sql.DB, engine *taskqueue.Engine, tracedHTTP *trace.TracedClient, region, az string) *VFWOrchestrator {
	return &VFWOrchestrator{
		policyDAO:  dao.NewFirewallPolicyDAO(db),
		engine:     engine,
		tracedHTTP: tracedHTTP,
		region:     region,
		az:         az,
	}
}

// BuildWorkflowHooks returns the WorkflowHooks that synchronize firewall_policies table
// with tq_workflows/tq_steps state transitions.
// NOTE: Hooks are non-blocking - they log warnings on errors but return nil to avoid
// stalling workflow execution. Compensation tasks handle eventual consistency.
func (o *VFWOrchestrator) BuildWorkflowHooks() *taskqueue.WorkflowHooks {
	return &taskqueue.WorkflowHooks{
		OnStepComplete: func(ctx context.Context, wf *taskqueue.Workflow, step *taskqueue.StepTask) error {
			if wf.ResourceType == string(models.ResourceTypeFirewallPolicy) {
				if err := o.policyDAO.IncrementCompletedTasks(ctx, wf.ResourceID); err != nil {
					logger.WarnContext(ctx, "OnStepComplete hook failed (non-blocking)", "resourceType", wf.ResourceType, "resourceID", wf.ResourceID, "error", err)
				}
			}
			return nil
		},
		OnStepFailed: func(ctx context.Context, wf *taskqueue.Workflow, step *taskqueue.StepTask, errMsg string) error {
			if wf.ResourceType == string(models.ResourceTypeFirewallPolicy) {
				if err := o.policyDAO.IncrementFailedTasks(ctx, wf.ResourceID); err != nil {
					logger.WarnContext(ctx, "OnStepFailed hook failed (non-blocking)", "resourceType", wf.ResourceType, "resourceID", wf.ResourceID, "error", err)
				}
			}
			return nil
		},
		OnWorkflowComplete: func(ctx context.Context, wf *taskqueue.Workflow) error {
			if wf.ResourceType == string(models.ResourceTypeFirewallPolicy) {
				logger.InfoContext(ctx, "防火墙策略创建完成", "az", o.az, "resourceID", wf.ResourceID)
				if err := o.policyDAO.UpdateStatus(ctx, wf.ResourceID, models.ResourceStatusRunning, ""); err != nil {
					logger.WarnContext(ctx, "OnWorkflowComplete hook failed (non-blocking)", "resourceType", wf.ResourceType, "resourceID", wf.ResourceID, "error", err)
				}
			}
			return nil
		},
		OnWorkflowFailed: func(ctx context.Context, wf *taskqueue.Workflow, errMsg string) error {
			if wf.ResourceType == string(models.ResourceTypeFirewallPolicy) {
				if err := o.policyDAO.UpdateStatus(ctx, wf.ResourceID, models.ResourceStatusFailed, errMsg); err != nil {
					logger.WarnContext(ctx, "OnWorkflowFailed hook failed (non-blocking)", "resourceType", wf.ResourceType, "resourceID", wf.ResourceID, "error", err)
				}
			}
			return nil
		},
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

	params := o.buildPolicyTaskParams(req)
	def := &taskqueue.WorkflowDefinition{
		Name:         "create_firewall_policy",
		ResourceType: string(models.ResourceTypeFirewallPolicy),
		ResourceID:   policyID,
		Metadata:     map[string]string{"az": o.az},
		Steps: []taskqueue.StepDefinition{
			{
				TaskType:   "create_firewall_policy",
				TaskName:   "创建防火墙策略",
				QueueTag:   string(queue.DeviceTypeFirewall),
				Priority:   taskqueue.PriorityNormal,
				Params:     params,
				MaxRetries: 3,
			},
		},
	}

	if err := o.policyDAO.UpdateTotalTasks(ctx, policyID, len(def.Steps)); err != nil {
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

	workflowID, err := o.engine.SubmitWorkflow(ctx, def)
	if err != nil {
		return &models.AZFirewallPolicyResponse{
			Success: false,
			Message: fmt.Sprintf("提交工作流失败: %v", err),
		}, nil
	}

	logger.InfoContext(ctx, "防火墙策略创建流程启动成功", "az", o.az, "policy_name", req.PolicyName, "policy_id", policyID, "workflowID", workflowID)

	return &models.AZFirewallPolicyResponse{
		Success:    true,
		Message:    "防火墙策略创建工作流已启动",
		PolicyID:   policyID,
		WorkflowID: workflowID,
	}, nil
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

// HandleTaskCallback delegates callback processing to the Engine.
func (o *VFWOrchestrator) HandleTaskCallback(ctx context.Context, payload []byte) error {
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

func (o *VFWOrchestrator) GetPolicyStatus(ctx context.Context, policyName string) (*models.FirewallPolicyStatusResponse, error) {
	policy, err := o.policyDAO.GetByName(ctx, policyName, o.az)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("策略不存在: %s", policyName)
	}
	if err != nil {
		return nil, fmt.Errorf("查询策略失败: %v", err)
	}

	tasks, err := o.getTasksByResourceID(ctx, string(models.ResourceTypeFirewallPolicy), policy.ID)
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

// getTasksByResourceID queries Engine store for workflows by resource and converts steps to models.Task.
func (o *VFWOrchestrator) getTasksByResourceID(ctx context.Context, resourceType, resourceID string) ([]*models.Task, error) {
	workflows, err := o.engine.Store().GetWorkflowsByResourceID(ctx, resourceType, resourceID)
	if err != nil {
		return nil, err
	}
	if len(workflows) == 0 {
		return nil, nil
	}

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

// StartCompensationTask starts a background goroutine that periodically scans for
// inconsistencies between workflow state and policy state, then repairs them.
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

		workflows, err := o.engine.Store().GetWorkflowsByResourceID(ctx, string(models.ResourceTypeFirewallPolicy), policy.ID)
		if err != nil {
			logger.Platform().Error("[VFW补偿任务] 查询策略工作流失败", "az", o.az, "policyID", policy.ID, "error", err)
			continue
		}

		if len(workflows) == 0 {
			continue
		}

		wf := workflows[0]
		switch wf.Status {
		case taskqueue.WorkflowStatusSucceeded:
			if policy.Status != models.ResourceStatusRunning {
				logger.Platform().Info("[VFW补偿任务] 修复策略状态: succeeded -> running",
					"az", o.az, "policyID", policy.ID, "currentStatus", policy.Status)
				if err := o.policyDAO.UpdateStatus(ctx, policy.ID, models.ResourceStatusRunning, ""); err != nil {
					logger.Platform().Error("[VFW补偿任务] 更新策略状态失败", "az", o.az, "error", err)
				}
			}
		case taskqueue.WorkflowStatusFailed:
			if policy.Status != models.ResourceStatusFailed {
				logger.Platform().Info("[VFW补偿任务] 修复策略状态: failed",
					"az", o.az, "policyID", policy.ID, "currentStatus", policy.Status)
				if err := o.policyDAO.UpdateStatus(ctx, policy.ID, models.ResourceStatusFailed, wf.ErrorMessage); err != nil {
					logger.Platform().Error("[VFW补偿任务] 更新策略状态失败", "az", o.az, "error", err)
				}
			}
		}
	}
}
