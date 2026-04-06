// inspector.go - asynq 实现的 Inspector 接口
package asynqbroker

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/hibiken/asynq"
	"github.com/jinleili-zz/nsp-platform/taskqueue"
)

// 编译期接口检查
var (
	_ taskqueue.Inspector       = (*Inspector)(nil)
	_ taskqueue.TaskReader      = (*Inspector)(nil)
	_ taskqueue.TaskController  = (*Inspector)(nil)
	_ taskqueue.QueueController = (*Inspector)(nil)
)

// Inspector 实现 taskqueue.Inspector 及其可选扩展接口。
// 内部持有 *asynq.Inspector，实现所有 4 层接口。
type Inspector struct {
	inner  *asynq.Inspector
	mu     sync.Mutex
	closed bool
}

// NewInspector 创建 asynq 实现的 Inspector。
func NewInspector(opt asynq.RedisConnOpt) *Inspector {
	return &Inspector{
		inner: asynq.NewInspector(opt),
	}
}

// -----------------------------------------------------------------------------
// Inspector 核心接口实现
// -----------------------------------------------------------------------------

// Queues 返回所有队列名称列表。
func (i *Inspector) Queues(ctx context.Context) ([]string, error) {
	return i.inner.Queues()
}

// GetQueueStats 返回指定队列的实时统计快照。
func (i *Inspector) GetQueueStats(ctx context.Context, queue string) (*taskqueue.QueueStats, error) {
	info, err := i.inner.GetQueueInfo(queue)
	if err != nil {
		if isQueueNotFoundErr(err) {
			return nil, taskqueue.ErrQueueNotFound
		}
		return nil, err
	}

	return &taskqueue.QueueStats{
		Queue:     info.Queue,
		Pending:   info.Pending,
		Scheduled: info.Scheduled,
		Active:    info.Active,
		Retry:     info.Retry,
		Failed:    info.Archived,
		Completed: info.Completed,
		Paused:    info.Paused,
		Timestamp: time.Now(),
	}, nil
}

// ListWorkers 返回当前在线的 worker 实例信息。
func (i *Inspector) ListWorkers(ctx context.Context) ([]*taskqueue.WorkerInfo, error) {
	servers, err := i.inner.Servers()
	if err != nil {
		return nil, err
	}

	workers := make([]*taskqueue.WorkerInfo, 0, len(servers))
	for _, s := range servers {
		// 提取队列名称列表
		queues := make([]string, 0, len(s.Queues))
		for q := range s.Queues {
			queues = append(queues, q)
		}

		workers = append(workers, &taskqueue.WorkerInfo{
			ID:          s.ID,
			Host:        s.Host,
			PID:         s.PID,
			Queues:      queues,
			StartedAt:   s.Started,
			ActiveTasks: len(s.ActiveWorkers),
		})
	}
	return workers, nil
}

// Close 释放 Inspector 持有的连接资源。
func (i *Inspector) Close() error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if i.closed {
		return nil
	}
	i.closed = true
	return i.inner.Close()
}

// -----------------------------------------------------------------------------
// TaskReader 可选接口实现
// -----------------------------------------------------------------------------

// GetTaskInfo 查询指定任务的详细信息。
func (i *Inspector) GetTaskInfo(ctx context.Context, queue, taskID string) (*taskqueue.TaskDetail, error) {
	info, err := i.inner.GetTaskInfo(queue, taskID)
	if err != nil {
		if isTaskNotFoundErr(err) {
			return nil, taskqueue.ErrTaskNotFound
		}
		if isQueueNotFoundErr(err) {
			return nil, taskqueue.ErrQueueNotFound
		}
		return nil, err
	}

	return convertTaskInfo(info), nil
}

// ListTasks 按状态分页列出任务。
func (i *Inspector) ListTasks(ctx context.Context, queue string, state taskqueue.TaskState, opts *taskqueue.ListOptions) (*taskqueue.TaskListResult, error) {
	if opts == nil {
		opts = taskqueue.DefaultListOptions()
	}
	opts.Normalize()

	var tasks []*taskqueue.TaskDetail
	var total int

	// asynq 使用 PageSize/Page 的方式进行分页
	asynqOpts := []asynq.ListOption{
		asynq.PageSize(opts.PageSize),
		asynq.Page(opts.Page),
	}

	switch state {
	case taskqueue.TaskStatePending:
		// pending + aggregating
		pendingTasks, err := i.inner.ListPendingTasks(queue, asynqOpts...)
		if err != nil {
			return nil, wrapQueueErr(err)
		}

		// 遍历所有组获取 aggregating 任务
		var aggTasks []*asynq.TaskInfo
		groups, _ := i.inner.Groups(queue)
		for _, g := range groups {
			groupTasks, _ := i.inner.ListAggregatingTasks(queue, g.Group, asynqOpts...)
			aggTasks = append(aggTasks, groupTasks...)
		}

		tasks = make([]*taskqueue.TaskDetail, 0, len(pendingTasks)+len(aggTasks))
		for _, t := range pendingTasks {
			tasks = append(tasks, convertTaskInfo(t))
		}
		for _, t := range aggTasks {
			tasks = append(tasks, convertTaskInfo(t))
		}

		// 获取 pending + aggregating 计数
		info, err := i.inner.GetQueueInfo(queue)
		if err == nil {
			total = info.Pending + info.Aggregating
		}

	case taskqueue.TaskStateScheduled:
		scheduledTasks, err := i.inner.ListScheduledTasks(queue, asynqOpts...)
		if err != nil {
			return nil, wrapQueueErr(err)
		}
		tasks = make([]*taskqueue.TaskDetail, 0, len(scheduledTasks))
		for _, t := range scheduledTasks {
			tasks = append(tasks, convertTaskInfo(t))
		}
		info, err := i.inner.GetQueueInfo(queue)
		if err == nil {
			total = info.Scheduled
		}

	case taskqueue.TaskStateActive:
		activeTasks, err := i.inner.ListActiveTasks(queue, asynqOpts...)
		if err != nil {
			return nil, wrapQueueErr(err)
		}
		tasks = make([]*taskqueue.TaskDetail, 0, len(activeTasks))
		for _, t := range activeTasks {
			tasks = append(tasks, convertTaskInfo(t))
		}
		info, err := i.inner.GetQueueInfo(queue)
		if err == nil {
			total = info.Active
		}

	case taskqueue.TaskStateRetry:
		retryTasks, err := i.inner.ListRetryTasks(queue, asynqOpts...)
		if err != nil {
			return nil, wrapQueueErr(err)
		}
		tasks = make([]*taskqueue.TaskDetail, 0, len(retryTasks))
		for _, t := range retryTasks {
			tasks = append(tasks, convertTaskInfo(t))
		}
		info, err := i.inner.GetQueueInfo(queue)
		if err == nil {
			total = info.Retry
		}

	case taskqueue.TaskStateFailed:
		archivedTasks, err := i.inner.ListArchivedTasks(queue, asynqOpts...)
		if err != nil {
			return nil, wrapQueueErr(err)
		}
		tasks = make([]*taskqueue.TaskDetail, 0, len(archivedTasks))
		for _, t := range archivedTasks {
			tasks = append(tasks, convertTaskInfo(t))
		}
		info, err := i.inner.GetQueueInfo(queue)
		if err == nil {
			total = info.Archived
		}

	case taskqueue.TaskStateCompleted:
		completedTasks, err := i.inner.ListCompletedTasks(queue, asynqOpts...)
		if err != nil {
			return nil, wrapQueueErr(err)
		}
		tasks = make([]*taskqueue.TaskDetail, 0, len(completedTasks))
		for _, t := range completedTasks {
			tasks = append(tasks, convertTaskInfo(t))
		}
		info, err := i.inner.GetQueueInfo(queue)
		if err == nil {
			total = info.Completed
		}

	default:
		tasks = []*taskqueue.TaskDetail{}
	}

	return &taskqueue.TaskListResult{
		Tasks: tasks,
		Total: total,
	}, nil
}

// -----------------------------------------------------------------------------
// TaskController 可选接口实现
// -----------------------------------------------------------------------------

// DeleteTask 删除指定任务。
func (i *Inspector) DeleteTask(ctx context.Context, queue, taskID string) error {
	err := i.inner.DeleteTask(queue, taskID)
	if err != nil {
		if isTaskNotFoundErr(err) {
			return taskqueue.ErrTaskNotFound
		}
		if isQueueNotFoundErr(err) {
			return taskqueue.ErrQueueNotFound
		}
	}
	return err
}

// RunTask 将任务立即提升为 pending。
func (i *Inspector) RunTask(ctx context.Context, queue, taskID string) error {
	err := i.inner.RunTask(queue, taskID)
	if err != nil {
		if isTaskNotFoundErr(err) {
			return taskqueue.ErrTaskNotFound
		}
		if isQueueNotFoundErr(err) {
			return taskqueue.ErrQueueNotFound
		}
	}
	return err
}

// ArchiveTask 将任务移入死信。
func (i *Inspector) ArchiveTask(ctx context.Context, queue, taskID string) error {
	err := i.inner.ArchiveTask(queue, taskID)
	if err != nil {
		if isTaskNotFoundErr(err) {
			return taskqueue.ErrTaskNotFound
		}
		if isQueueNotFoundErr(err) {
			return taskqueue.ErrQueueNotFound
		}
	}
	return err
}

// CancelTask 向正在执行的任务发送取消信号。
func (i *Inspector) CancelTask(ctx context.Context, taskID string) error {
	return i.inner.CancelProcessing(taskID)
}

// BatchDeleteTasks 批量删除指定状态的所有任务。
func (i *Inspector) BatchDeleteTasks(ctx context.Context, queue string, state taskqueue.TaskState) (int, error) {
	var deleted int
	var err error

	switch state {
	case taskqueue.TaskStatePending:
		// pending + aggregating
		deleted, err = i.inner.DeleteAllPendingTasks(queue)
		if err != nil {
			return 0, wrapQueueErr(err)
		}
		// 尝试删除 aggregating 任务，忽略可能的组名错误
		groups, _ := i.inner.Groups(queue)
		for _, g := range groups {
			n, _ := i.inner.DeleteAllAggregatingTasks(queue, g.Group)
			deleted += n
		}
	case taskqueue.TaskStateScheduled:
		deleted, err = i.inner.DeleteAllScheduledTasks(queue)
	case taskqueue.TaskStateRetry:
		deleted, err = i.inner.DeleteAllRetryTasks(queue)
	case taskqueue.TaskStateFailed:
		deleted, err = i.inner.DeleteAllArchivedTasks(queue)
	case taskqueue.TaskStateCompleted:
		deleted, err = i.inner.DeleteAllCompletedTasks(queue)
	default:
		return 0, nil
	}

	if err != nil {
		return 0, wrapQueueErr(err)
	}
	return deleted, nil
}

// BatchRunTasks 批量将指定状态的所有任务提升为 pending。
func (i *Inspector) BatchRunTasks(ctx context.Context, queue string, state taskqueue.TaskState) (int, error) {
	var count int
	var err error

	switch state {
	case taskqueue.TaskStateScheduled:
		count, err = i.inner.RunAllScheduledTasks(queue)
	case taskqueue.TaskStateRetry:
		count, err = i.inner.RunAllRetryTasks(queue)
	case taskqueue.TaskStateFailed:
		count, err = i.inner.RunAllArchivedTasks(queue)
	default:
		return 0, nil
	}

	if err != nil {
		return 0, wrapQueueErr(err)
	}
	return count, nil
}

// BatchArchiveTasks 批量将指定状态的所有任务归档。
func (i *Inspector) BatchArchiveTasks(ctx context.Context, queue string, state taskqueue.TaskState) (int, error) {
	var count int
	var err error

	switch state {
	case taskqueue.TaskStatePending:
		count, err = i.inner.ArchiveAllPendingTasks(queue)
	case taskqueue.TaskStateScheduled:
		count, err = i.inner.ArchiveAllScheduledTasks(queue)
	case taskqueue.TaskStateRetry:
		count, err = i.inner.ArchiveAllRetryTasks(queue)
	default:
		return 0, nil
	}

	if err != nil {
		return 0, wrapQueueErr(err)
	}
	return count, nil
}

// -----------------------------------------------------------------------------
// QueueController 可选接口实现
// -----------------------------------------------------------------------------

// PauseQueue 暂停队列。
func (i *Inspector) PauseQueue(ctx context.Context, queue string) error {
	err := i.inner.PauseQueue(queue)
	if err != nil {
		return wrapQueueErr(err)
	}
	return nil
}

// UnpauseQueue 恢复队列。
func (i *Inspector) UnpauseQueue(ctx context.Context, queue string) error {
	err := i.inner.UnpauseQueue(queue)
	if err != nil {
		return wrapQueueErr(err)
	}
	return nil
}

// DeleteQueue 删除队列。
func (i *Inspector) DeleteQueue(ctx context.Context, queue string, force bool) error {
	var err error
	if force {
		err = i.inner.DeleteQueue(queue, true)
	} else {
		err = i.inner.DeleteQueue(queue, false)
	}
	if err != nil {
		return wrapQueueErr(err)
	}
	return nil
}

// -----------------------------------------------------------------------------
// 辅助函数
// -----------------------------------------------------------------------------

// convertTaskInfo 将 asynq.TaskInfo 转换为 taskqueue.TaskDetail。
func convertTaskInfo(info *asynq.TaskInfo) *taskqueue.TaskDetail {
	detail := &taskqueue.TaskDetail{
		ID:        info.ID,
		Queue:     info.Queue,
		Type:      info.Type,
		State:     convertState(info.State),
		MaxRetry:  info.MaxRetry,
		Retried:   info.Retried,
		LastError: info.LastErr,
	}

	// NextProcessAt
	if !info.NextProcessAt.IsZero() {
		t := info.NextProcessAt
		detail.NextProcessAt = &t
	}

	// CompletedAt
	if !info.CompletedAt.IsZero() {
		t := info.CompletedAt
		detail.CompletedAt = &t
	}

	// Payload: 去除 trace envelope，返回纯业务数据
	payload, _, _, _ := unwrapEnvelope(info.Payload)
	detail.Payload = payload

	return detail
}

// convertState 将 asynq.TaskState 转换为 taskqueue.TaskState。
func convertState(s asynq.TaskState) taskqueue.TaskState {
	switch s {
	case asynq.TaskStatePending:
		return taskqueue.TaskStatePending
	case asynq.TaskStateScheduled:
		return taskqueue.TaskStateScheduled
	case asynq.TaskStateActive:
		return taskqueue.TaskStateActive
	case asynq.TaskStateRetry:
		return taskqueue.TaskStateRetry
	case asynq.TaskStateArchived:
		return taskqueue.TaskStateFailed
	case asynq.TaskStateCompleted:
		return taskqueue.TaskStateCompleted
	case asynq.TaskStateAggregating:
		// aggregating 从业务视角属于待处理
		return taskqueue.TaskStatePending
	default:
		return taskqueue.TaskStatePending
	}
}

// isQueueNotFoundErr 检查是否为队列不存在错误。
func isQueueNotFoundErr(err error) bool {
	return errors.Is(err, asynq.ErrQueueNotFound)
}

// isTaskNotFoundErr 检查是否为任务不存在错误。
func isTaskNotFoundErr(err error) bool {
	return errors.Is(err, asynq.ErrTaskNotFound)
}

// wrapQueueErr 包装队列相关错误。
func wrapQueueErr(err error) error {
	if isQueueNotFoundErr(err) {
		return taskqueue.ErrQueueNotFound
	}
	return err
}
