// inspector.go - Inspector 接口定义及数据模型
package taskqueue

import (
	"context"
	"errors"
	"time"
)

// -----------------------------------------------------------------------------
// 错误定义
// -----------------------------------------------------------------------------

var (
	// ErrQueueNotFound 队列不存在。
	ErrQueueNotFound = errors.New("taskqueue: queue not found")

	// ErrTaskNotFound 任务不存在。
	ErrTaskNotFound = errors.New("taskqueue: task not found")
)

// -----------------------------------------------------------------------------
// 第 1 层：Inspector（核心，所有后端必须完整实现）
// -----------------------------------------------------------------------------

// Inspector 提供对消息队列运行状态的只读查询能力。
// 核心接口只包含所有后端都能合理实现的方法。
// 所有方法都应正常返回结果，调用方无需处理 ErrNotSupported。
type Inspector interface {
	// Queues 返回所有队列名称列表。
	Queues(ctx context.Context) ([]string, error)

	// GetQueueStats 返回指定队列的实时统计快照。
	GetQueueStats(ctx context.Context, queue string) (*QueueStats, error)

	// ListWorkers 返回当前在线的 worker 实例信息。
	// 若后端返回的信息不完整（如缺少 PID），相应字段填零值。
	ListWorkers(ctx context.Context) ([]*WorkerInfo, error)

	// Close 释放 Inspector 持有的连接资源。
	// 实现必须保证幂等：多次调用不应 panic 或返回错误。
	Close() error
}

// -----------------------------------------------------------------------------
// 第 2 层：TaskReader（可选，支持任务级查询的后端实现）
// -----------------------------------------------------------------------------

// TaskReader 提供任务级别的查询能力。
// 仅支持此能力的后端（如 asynq）实现此接口。
// 调用方通过接口断言检测：
//
//	if tr, ok := inspector.(taskqueue.TaskReader); ok { ... }
type TaskReader interface {
	// GetTaskInfo 查询指定任务的详细信息。
	GetTaskInfo(ctx context.Context, queue, taskID string) (*TaskDetail, error)

	// ListTasks 按状态分页列出任务。
	// opts 为 nil 时使用默认分页（第 1 页，每页 30 条）。
	ListTasks(ctx context.Context, queue string, state TaskState, opts *ListOptions) (*TaskListResult, error)
}

// -----------------------------------------------------------------------------
// 第 3 层：TaskController（可选，支持任务级写操作的后端实现）
// -----------------------------------------------------------------------------

// TaskController 提供对单个或批量任务的状态变更操作。
// 仅支持此能力的后端（如 asynq）实现此接口。
// 调用方通过接口断言检测：
//
//	if tc, ok := inspector.(taskqueue.TaskController); ok { ... }
type TaskController interface {
	// DeleteTask 删除指定任务（不可删除 active 状态的任务）。
	DeleteTask(ctx context.Context, queue, taskID string) error

	// RunTask 将 scheduled/retry/failed 状态的任务立即提升为 pending。
	RunTask(ctx context.Context, queue, taskID string) error

	// ArchiveTask 将 pending/scheduled/retry 的任务移入死信（归档）。
	ArchiveTask(ctx context.Context, queue, taskID string) error

	// CancelTask 向正在执行的任务发送取消信号（尽力而为，不保证成功）。
	CancelTask(ctx context.Context, taskID string) error

	// BatchDeleteTasks 批量删除指定状态的所有任务，返回删除数量。
	// 注意：状态与 TaskState 枚举一一对应，不做合并（见 TaskState 说明）。
	BatchDeleteTasks(ctx context.Context, queue string, state TaskState) (int, error)

	// BatchRunTasks 批量将指定状态的所有任务提升为 pending，返回操作数量。
	BatchRunTasks(ctx context.Context, queue string, state TaskState) (int, error)

	// BatchArchiveTasks 批量将指定状态的所有任务归档，返回操作数量。
	BatchArchiveTasks(ctx context.Context, queue string, state TaskState) (int, error)
}

// -----------------------------------------------------------------------------
// 第 4 层：QueueController（可选，支持队列级操作的后端实现）
// -----------------------------------------------------------------------------

// QueueController 提供队列级别的管理操作。
// 仅支持此能力的后端（如 asynq）实现此接口。
// 调用方通过接口断言检测：
//
//	if qc, ok := inspector.(taskqueue.QueueController); ok { ... }
type QueueController interface {
	// PauseQueue 暂停队列，停止消费新任务。
	PauseQueue(ctx context.Context, queue string) error

	// UnpauseQueue 恢复队列消费。
	UnpauseQueue(ctx context.Context, queue string) error

	// DeleteQueue 删除队列。
	// force=false 时队列非空会返回错误；force=true 时强制删除。
	DeleteQueue(ctx context.Context, queue string, force bool) error
}

// -----------------------------------------------------------------------------
// 数据模型
// -----------------------------------------------------------------------------

// QueueStats 是某队列在某一时刻的统计快照。
type QueueStats struct {
	Queue     string    // 队列名称
	Pending   int       // 等待执行的任务数（不含 Scheduled）
	Scheduled int       // 延迟调度中的任务数
	Active    int       // 正在执行的任务数
	Retry     int       // 等待重试的任务数
	Failed    int       // 已失败的任务数（死信/归档）
	Completed int       // 已完成的任务数（需后端开启 Retention）

	// Paused 队列是否已暂停。
	// 仅部分后端（如 asynq）支持队列暂停功能，
	// 不支持的后端固定返回 false。
	Paused bool

	Timestamp time.Time // 统计时间点
}

// TaskState 是所有后端统一的任务状态枚举。
// 各状态语义明确，不做合并，保证 Batch 操作行为清晰。
type TaskState string

const (
	// TaskStatePending 任务等待立即执行。
	TaskStatePending TaskState = "pending"

	// TaskStateScheduled 任务延迟调度中（有 NextProcessAt）。
	TaskStateScheduled TaskState = "scheduled"

	// TaskStateActive 任务正在被 worker 执行。
	TaskStateActive TaskState = "active"

	// TaskStateRetry 任务执行失败，等待重试。
	TaskStateRetry TaskState = "retry"

	// TaskStateFailed 任务已进入死信/归档，不再自动重试。
	TaskStateFailed TaskState = "failed"

	// TaskStateCompleted 任务已成功完成（需后端开启结果保留）。
	TaskStateCompleted TaskState = "completed"
)

// TaskDetail 是单个任务的完整信息视图。
type TaskDetail struct {
	ID            string     // Broker 分配的任务 ID
	Queue         string     // 所在队列
	Type          string     // 任务类型
	State         TaskState  // 当前状态
	MaxRetry      int        // 最大重试次数
	Retried       int        // 已重试次数
	LastError     string     // 最近一次错误信息
	NextProcessAt *time.Time // 下次执行时间（scheduled/retry 时有值）
	CreatedAt     *time.Time // 任务创建时间
	CompletedAt   *time.Time // 任务完成时间（completed 时有值）

	// Payload 是业务原始数据（JSON），不含内部 wrapper。
	// 实现方应在返回前去除 trace envelope 等内部封装，
	// 保证调用方看到的是可读的业务 payload。
	Payload []byte
}

// ListOptions 任务列表查询的分页参数。
type ListOptions struct {
	Page     int // 页码，从 1 开始，默认 1
	PageSize int // 每页条数，默认 30，最大 100
}

// DefaultListOptions 返回默认分页选项。
func DefaultListOptions() *ListOptions {
	return &ListOptions{
		Page:     1,
		PageSize: 30,
	}
}

// Normalize 规范化分页参数，确保在合法范围内。
func (o *ListOptions) Normalize() {
	if o.Page < 1 {
		o.Page = 1
	}
	if o.PageSize < 1 {
		o.PageSize = 30
	}
	if o.PageSize > 100 {
		o.PageSize = 100
	}
}

// TaskListResult 分页查询结果。
type TaskListResult struct {
	Tasks []*TaskDetail
	Total int // 符合条件的总数
}

// WorkerInfo 描述一个在线的 worker 服务实例。
type WorkerInfo struct {
	ID          string    // 实例唯一标识
	Host        string    // 主机名或 Pod 名称
	PID         int       // 进程 ID（不支持时为 0）
	Queues      []string  // 监听的队列列表
	StartedAt   time.Time // 启动时间
	ActiveTasks int       // 当前正在处理的任务数
}
