package dao

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"workflow_qoder/internal/models"
)

type TaskDAO struct {
	db *sql.DB
}

func NewTaskDAO(db *sql.DB) *TaskDAO {
	return &TaskDAO{db: db}
}

func (d *TaskDAO) BatchCreate(ctx context.Context, tasks []*models.Task) error {
	query := `
		INSERT INTO tasks (
			id, resource_type, resource_id, task_type, task_name, task_order,
			task_params, status, priority, device_type, asynq_task_id,
			result, error_message, retry_count, max_retries, az,
			created_at, queued_at, started_at, completed_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7::jsonb, $8, $9, $10, $11,
			$12::jsonb, $13, $14, $15, $16,
			$17, $18, $19, $20, $21
		)
	`

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now()
	for _, task := range tasks {
		resultJSON := nullableJSON(task.Result)
		if _, err := tx.ExecContext(ctx, query,
			task.ID, task.ResourceType, task.ResourceID, task.TaskType, task.TaskName, task.TaskOrder,
			task.TaskParams, task.Status, task.Priority, task.DeviceType, nullString(task.AsynqTaskID),
			resultJSON, nullString(task.ErrorMessage), task.RetryCount, task.MaxRetries, task.AZ,
			coalesceTime(task.CreatedAt, now), task.QueuedAt, task.StartedAt, task.CompletedAt, coalesceTime(task.UpdatedAt, now),
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (d *TaskDAO) GetByID(ctx context.Context, id string) (*models.Task, error) {
	query := `
		SELECT id, resource_type, resource_id, task_type, task_name, task_order,
		       task_params::text, status, priority, device_type, asynq_task_id,
		       result::text, error_message, retry_count, max_retries, az,
		       created_at, queued_at, started_at, completed_at, updated_at
		FROM tasks
		WHERE id = $1
	`
	return d.getOne(ctx, query, id)
}

func (d *TaskDAO) GetByResourceID(ctx context.Context, resourceID string) ([]*models.Task, error) {
	query := `
		SELECT id, resource_type, resource_id, task_type, task_name, task_order,
		       task_params::text, status, priority, device_type, asynq_task_id,
		       result::text, error_message, retry_count, max_retries, az,
		       created_at, queued_at, started_at, completed_at, updated_at
		FROM tasks
		WHERE resource_id = $1
		ORDER BY task_order ASC, created_at ASC
	`

	rows, err := d.db.QueryContext(ctx, query, resourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*models.Task
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (d *TaskDAO) GetByResourceAndOrder(ctx context.Context, resourceID string, taskOrder int) (*models.Task, error) {
	query := `
		SELECT id, resource_type, resource_id, task_type, task_name, task_order,
		       task_params::text, status, priority, device_type, asynq_task_id,
		       result::text, error_message, retry_count, max_retries, az,
		       created_at, queued_at, started_at, completed_at, updated_at
		FROM tasks
		WHERE resource_id = $1 AND task_order = $2
	`
	return d.getOne(ctx, query, resourceID, taskOrder)
}

func (d *TaskDAO) GetNextPendingTask(ctx context.Context, resourceID string) (*models.Task, error) {
	query := `
		SELECT id, resource_type, resource_id, task_type, task_name, task_order,
		       task_params::text, status, priority, device_type, asynq_task_id,
		       result::text, error_message, retry_count, max_retries, az,
		       created_at, queued_at, started_at, completed_at, updated_at
		FROM tasks
		WHERE resource_id = $1 AND status = 'pending'
		ORDER BY task_order ASC
		LIMIT 1
	`
	task, err := d.getOne(ctx, query, resourceID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return task, err
}

// DeleteByResourceID 删除资源关联的全部任务。
// 用于旧失败/已删除资源被重置复用时清理历史任务，避免与
// uq_tasks_resource_order 唯一约束和新工作流任务冲突（设计文档 7.8 节）。
func (d *TaskDAO) DeleteByResourceID(ctx context.Context, resourceID string) error {
	_, err := d.db.ExecContext(ctx, `DELETE FROM tasks WHERE resource_id = $1`, resourceID)
	return err
}

func (d *TaskDAO) GetTaskStats(ctx context.Context, resourceID string) (total, completed, failed int, err error) {
	query := `
		SELECT COUNT(*),
		       COUNT(*) FILTER (WHERE status = 'completed'),
		       COUNT(*) FILTER (WHERE status = 'failed')
		FROM tasks
		WHERE resource_id = $1
	`
	err = d.db.QueryRowContext(ctx, query, resourceID).Scan(&total, &completed, &failed)
	return
}

func (d *TaskDAO) UpdateStatus(ctx context.Context, id string, status models.TaskStatus) error {
	now := time.Now()

	query := `UPDATE tasks SET status = $1, updated_at = $2`
	args := []any{status, now}

	switch status {
	case models.TaskStatusQueued:
		query += `, queued_at = $3 WHERE id = $4`
		args = append(args, now, id)
	case models.TaskStatusRunning:
		query += `, started_at = $3 WHERE id = $4`
		args = append(args, now, id)
	case models.TaskStatusCompleted, models.TaskStatusFailed, models.TaskStatusCancelled:
		query += `, completed_at = $3 WHERE id = $4`
		args = append(args, now, id)
	default:
		query += ` WHERE id = $3`
		args = append(args, id)
	}

	_, err := d.db.ExecContext(ctx, query, args...)
	return err
}

func (d *TaskDAO) UpdateQueued(ctx context.Context, id, asynqTaskID string) error {
	query := `
		UPDATE tasks
		SET status = 'queued', asynq_task_id = $1, queued_at = $2, updated_at = $2
		WHERE id = $3
	`
	_, err := d.db.ExecContext(ctx, query, asynqTaskID, time.Now(), id)
	return err
}

func (d *TaskDAO) UpdateRetryProgress(ctx context.Context, id string, retryCount, maxRetries int, errMsg string) error {
	query := `
		UPDATE tasks
		SET status = 'queued',
		    retry_count = $1,
		    max_retries = $2,
		    error_message = $3,
		    updated_at = $4
		WHERE id = $5
	`
	_, err := d.db.ExecContext(ctx, query, retryCount, maxRetries, nullString(errMsg), time.Now(), id)
	return err
}

// UpdateResult 幂等改造（设计文档 7.8/11.5 节）：
// 终态更新使用 CAS——只有当前状态不是终态时才允许推进，
// 返回 applied=true 表示本次调用完成了唯一一次有效状态迁移；
// applied=false 表示任务已被并发/重复 Reply 推进到终态，
// 调用方必须跳过计数累加和下一步发布，防止 completed_tasks 重复 +1 与重复投递。
func (d *TaskDAO) UpdateResult(ctx context.Context, id string, status models.TaskStatus, result any, errMsg string) (bool, error) {
	var resultJSON any
	if result != nil {
		resultJSON = nullableJSON(mustJSONString(result))
	}

	query := `
		UPDATE tasks
		SET status = $1,
		    result = $2::jsonb,
		    error_message = $3,
		    completed_at = $4,
		    updated_at = $4
		WHERE id = $5
		  AND status NOT IN ('completed', 'failed', 'cancelled')
	`
	res, err := d.db.ExecContext(ctx, query, status, resultJSON, nullString(errMsg), time.Now(), id)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

func (d *TaskDAO) getOne(ctx context.Context, query string, args ...any) (*models.Task, error) {
	row := d.db.QueryRowContext(ctx, query, args...)
	return scanTask(row)
}

type scanner interface {
	Scan(dest ...any) error
}

func scanTask(s scanner) (*models.Task, error) {
	task := &models.Task{}
	var asynqTaskID, result, errMsg sql.NullString

	err := s.Scan(
		&task.ID, &task.ResourceType, &task.ResourceID, &task.TaskType, &task.TaskName, &task.TaskOrder,
		&task.TaskParams, &task.Status, &task.Priority, &task.DeviceType, &asynqTaskID,
		&result, &errMsg, &task.RetryCount, &task.MaxRetries, &task.AZ,
		&task.CreatedAt, &task.QueuedAt, &task.StartedAt, &task.CompletedAt, &task.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if asynqTaskID.Valid {
		task.AsynqTaskID = asynqTaskID.String
	}
	if result.Valid {
		task.Result = result.String
	}
	if errMsg.Valid {
		task.ErrorMessage = errMsg.String
	}
	return task, nil
}

func nullableJSON(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func mustJSONString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	default:
		data, _ := json.Marshal(value)
		return string(data)
	}
}

func coalesceTime(value, fallback time.Time) time.Time {
	if value.IsZero() {
		return fallback
	}
	return value
}
