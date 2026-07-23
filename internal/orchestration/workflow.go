package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/google/uuid"
	"github.com/jinleili-zz/nsp-platform/logger"
	"github.com/jinleili-zz/nsp-platform/taskqueue"

	"workflow_qoder/internal/models"
)

type WorkflowStep struct {
	TaskType   string
	TaskName   string
	DeviceType string
	Priority   int
	MaxRetries int
	Payload    []byte
}

type WorkflowDef struct {
	ResourceType models.ResourceType
	ResourceID   string
	AZ           string
	Steps        []WorkflowStep
}

type ResourceStore interface {
	UpdateTotalTasks(ctx context.Context, id string, totalTasks int) error
	IncrementCompletedTasks(ctx context.Context, id string) error
	IncrementFailedTasks(ctx context.Context, id string) error
	UpdateStatus(ctx context.Context, id string, status models.ResourceStatus, errMsg string) error
}

type TaskStore interface {
	BatchCreate(ctx context.Context, tasks []*models.Task) error
	GetByResourceID(ctx context.Context, resourceID string) ([]*models.Task, error)
	GetByResourceAndOrder(ctx context.Context, resourceID string, taskOrder int) (*models.Task, error)
	GetNextPendingTask(ctx context.Context, resourceID string) (*models.Task, error)
	UpdateQueued(ctx context.Context, id, asynqTaskID string) error
	UpdateRetryProgress(ctx context.Context, id string, retryCount, maxRetries int, errMsg string) (bool, error)
	UpdateResult(ctx context.Context, id string, status models.TaskStatus, result any, errMsg string) (bool, error)
}

type QueueResolver func(deviceType string, priority int) string

type Manager struct {
	broker         taskqueue.Broker
	taskStore      TaskStore
	queueResolver  QueueResolver
	replyQueueName string
	resourceStores map[models.ResourceType]ResourceStore
}

func NewManager(broker taskqueue.Broker, taskStore TaskStore, queueResolver QueueResolver, replyQueueName string) *Manager {
	return &Manager{
		broker:         broker,
		taskStore:      taskStore,
		queueResolver:  queueResolver,
		replyQueueName: replyQueueName,
		resourceStores: make(map[models.ResourceType]ResourceStore),
	}
}

func (m *Manager) RegisterResourceStore(resourceType models.ResourceType, store ResourceStore) {
	m.resourceStores[resourceType] = store
}

func (m *Manager) ReplyQueueName() string {
	return m.replyQueueName
}

func (m *Manager) SubmitWorkflow(ctx context.Context, def WorkflowDef) (string, error) {
	if len(def.Steps) == 0 {
		return "", fmt.Errorf("workflow steps不能为空")
	}

	resourceStore, ok := m.resourceStores[def.ResourceType]
	if !ok {
		return "", fmt.Errorf("resource store未注册: %s", def.ResourceType)
	}

	tasks := make([]*models.Task, 0, len(def.Steps))
	for i, step := range def.Steps {
		maxRetries := step.MaxRetries
		if maxRetries == 0 {
			maxRetries = 3
		}

		tasks = append(tasks, &models.Task{
			ID:           uuid.New().String(),
			ResourceType: def.ResourceType,
			ResourceID:   def.ResourceID,
			TaskType:     step.TaskType,
			TaskName:     step.TaskName,
			TaskOrder:    i + 1,
			TaskParams:   string(step.Payload),
			Status:       models.TaskStatusPending,
			Priority:     step.Priority,
			DeviceType:   step.DeviceType,
			MaxRetries:   maxRetries,
			AZ:           def.AZ,
		})
	}

	if err := m.taskStore.BatchCreate(ctx, tasks); err != nil {
		return "", fmt.Errorf("创建任务记录失败: %w", err)
	}
	if err := resourceStore.UpdateTotalTasks(ctx, def.ResourceID, len(tasks)); err != nil {
		return "", fmt.Errorf("更新总任务数失败: %w", err)
	}
	if err := resourceStore.UpdateStatus(ctx, def.ResourceID, models.ResourceStatusCreating, ""); err != nil {
		return "", fmt.Errorf("更新资源状态失败: %w", err)
	}

	if err := m.publishTask(ctx, tasks[0], len(tasks)); err != nil {
		_, _ = m.taskStore.UpdateResult(ctx, tasks[0].ID, models.TaskStatusFailed, nil, err.Error())
		_ = resourceStore.IncrementFailedTasks(ctx, def.ResourceID)
		_ = resourceStore.UpdateStatus(ctx, def.ResourceID, models.ResourceStatusFailed, err.Error())
		return "", fmt.Errorf("发布首个step失败: %w", err)
	}

	return def.ResourceID, nil
}

func (m *Manager) HandleReply(ctx context.Context, task *taskqueue.Task) error {
	if task == nil {
		return fmt.Errorf("reply task不能为空")
	}

	var reply ReplyPayload
	if err := json.Unmarshal(task.Payload, &reply); err != nil {
		return fmt.Errorf("解析reply payload失败: %w", err)
	}

	resourceType, resourceID, stepIndex, totalSteps, err := metadataContext(task.Metadata)
	if err != nil {
		return err
	}

	resourceStore, ok := m.resourceStores[resourceType]
	if !ok {
		return fmt.Errorf("resource store未注册: %s", resourceType)
	}

	currentTask, err := m.taskStore.GetByResourceAndOrder(ctx, resourceID, stepIndex+1)
	if err != nil {
		return fmt.Errorf("查询当前step失败: %w", err)
	}

	if currentTask.Status == models.TaskStatusCompleted || currentTask.Status == models.TaskStatusFailed {
		logger.InfoContext(ctx, "忽略重复reply", "resourceID", resourceID, "taskID", currentTask.ID, "status", currentTask.Status)
		return nil
	}

	switch reply.Status {
	case ReplyStatusSuccess:
		updated, err := m.taskStore.UpdateResult(ctx, currentTask.ID, models.TaskStatusCompleted, string(reply.Result), "")
		if err != nil {
			return fmt.Errorf("更新任务完成状态失败: %w", err)
		}
		if !updated {
			logger.InfoContext(ctx, "忽略并发重复reply", "resourceID", resourceID, "taskID", currentTask.ID)
			return nil
		}
		if err := resourceStore.IncrementCompletedTasks(ctx, resourceID); err != nil {
			return fmt.Errorf("更新完成任务计数失败: %w", err)
		}
		if stepIndex == totalSteps-1 {
			if err := resourceStore.UpdateStatus(ctx, resourceID, models.ResourceStatusRunning, ""); err != nil {
				return fmt.Errorf("更新资源running状态失败: %w", err)
			}
			return nil
		}

		nextTask, err := m.taskStore.GetNextPendingTask(ctx, resourceID)
		if err != nil {
			return fmt.Errorf("查询下一个step失败: %w", err)
		}
		if nextTask == nil {
			return fmt.Errorf("资源 %s 没有可推进的下一个step", resourceID)
		}
		if err := m.publishTask(ctx, nextTask, totalSteps); err != nil {
			return fmt.Errorf("发布下一个step失败: %w", err)
		}
		return nil

	case ReplyStatusFailed:
		if !reply.FinalFailure {
			updated, err := m.taskStore.UpdateRetryProgress(ctx, currentTask.ID, reply.RetryCount, reply.MaxRetries, reply.Error)
			if err != nil {
				return fmt.Errorf("更新任务重试进度失败: %w", err)
			}
			if !updated {
				logger.InfoContext(ctx, "忽略迟到的重试reply", "resourceID", resourceID, "taskID", currentTask.ID)
			}
			return nil
		}
		updated, err := m.taskStore.UpdateResult(ctx, currentTask.ID, models.TaskStatusFailed, nil, reply.Error)
		if err != nil {
			return fmt.Errorf("更新任务失败状态失败: %w", err)
		}
		if !updated {
			logger.InfoContext(ctx, "忽略并发重复reply", "resourceID", resourceID, "taskID", currentTask.ID)
			return nil
		}
		if err := resourceStore.IncrementFailedTasks(ctx, resourceID); err != nil {
			return fmt.Errorf("更新失败任务计数失败: %w", err)
		}
		if err := resourceStore.UpdateStatus(ctx, resourceID, models.ResourceStatusFailed, reply.Error); err != nil {
			return fmt.Errorf("更新资源failed状态失败: %w", err)
		}
		return nil

	default:
		return fmt.Errorf("未知reply状态: %s", reply.Status)
	}
}

func (m *Manager) RequeueTask(ctx context.Context, task *models.Task) error {
	tasks, err := m.taskStore.GetByResourceID(ctx, task.ResourceID)
	if err != nil {
		return fmt.Errorf("查询任务列表失败: %w", err)
	}
	if len(tasks) == 0 {
		return fmt.Errorf("资源 %s 没有关联任务", task.ResourceID)
	}
	return m.publishTask(ctx, task, len(tasks))
}

func (m *Manager) publishTask(ctx context.Context, task *models.Task, totalSteps int) error {
	queueName := m.queueResolver(task.DeviceType, task.Priority)
	if queueName == "" {
		return fmt.Errorf("queue resolver返回空队列: deviceType=%s priority=%d", task.DeviceType, task.Priority)
	}

	info, err := m.broker.Publish(ctx, &taskqueue.Task{
		Type:     task.TaskType,
		Payload:  []byte(task.TaskParams),
		Queue:    queueName,
		MaxRetry: &task.MaxRetries,
		Reply: &taskqueue.ReplySpec{
			Queue: m.replyQueueName,
		},
		Metadata: map[string]string{
			MetadataKeyResourceID:   task.ResourceID,
			MetadataKeyResourceType: string(task.ResourceType),
			MetadataKeyStepIndex:    strconv.Itoa(task.TaskOrder - 1),
			MetadataKeyTotalSteps:   strconv.Itoa(totalSteps),
		},
	})
	if err != nil {
		return err
	}

	return m.taskStore.UpdateQueued(ctx, task.ID, info.BrokerTaskID)
}

func metadataContext(metadata map[string]string) (models.ResourceType, string, int, int, error) {
	resourceID := metadata[MetadataKeyResourceID]
	resourceTypeRaw := metadata[MetadataKeyResourceType]
	if resourceID == "" || resourceTypeRaw == "" {
		return "", "", 0, 0, fmt.Errorf("reply metadata缺少resource上下文")
	}

	stepIndex, err := strconv.Atoi(metadata[MetadataKeyStepIndex])
	if err != nil {
		return "", "", 0, 0, fmt.Errorf("reply metadata step_index非法: %w", err)
	}
	totalSteps, err := strconv.Atoi(metadata[MetadataKeyTotalSteps])
	if err != nil {
		return "", "", 0, 0, fmt.Errorf("reply metadata total_steps非法: %w", err)
	}

	return models.ResourceType(resourceTypeRaw), resourceID, stepIndex, totalSteps, nil
}
