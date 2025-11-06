package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Task 任务模型
type Task struct {
	ID            int64
	TaskID        string
	WorkflowID    string
	TaskName      string
	TaskType      string
	SequenceOrder int
	Status        string
	Payload       json.RawMessage
	Result        sql.NullString
	ErrorMessage  sql.NullString
	RetryCount    int
	MaxRetries    int
	WorkerID      sql.NullString
	StartedAt     sql.NullTime
	CompletedAt   sql.NullTime
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// CreateTask 创建任务
func CreateTask(task *Task) error {
	query := `INSERT INTO tasks (task_id, workflow_id, task_name, task_type, sequence_order, 
	          status, payload, max_retries) 
	          VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
	
	result, err := db.Exec(query, task.TaskID, task.WorkflowID, task.TaskName, task.TaskType,
		task.SequenceOrder, task.Status, task.Payload, task.MaxRetries)
	if err != nil {
		return fmt.Errorf("创建任务失败: %v", err)
	}
	
	task.ID, _ = result.LastInsertId()
	return nil
}

// GetPendingTasksByType 获取指定类型的待执行任务（按创建时间排序）
func GetPendingTasksByType(taskType string, limit int) ([]*Task, error) {
	query := `SELECT id, task_id, workflow_id, task_name, task_type, sequence_order, 
	          status, payload, result, error_message, retry_count, max_retries, worker_id, 
	          started_at, completed_at, created_at, updated_at 
	          FROM tasks 
	          WHERE task_type = ? AND status = 'pending' 
	          ORDER BY sequence_order ASC, created_at ASC
	          LIMIT ?`
	
	rows, err := db.Query(query, taskType, limit)
	if err != nil {
		return nil, fmt.Errorf("查询待执行任务失败: %v", err)
	}
	defer rows.Close()
	
	var tasks []*Task
	for rows.Next() {
		task := &Task{}
		err := rows.Scan(
			&task.ID, &task.TaskID, &task.WorkflowID, &task.TaskName, &task.TaskType,
			&task.SequenceOrder, &task.Status, &task.Payload, &task.Result, &task.ErrorMessage,
			&task.RetryCount, &task.MaxRetries, &task.WorkerID, &task.StartedAt, &task.CompletedAt,
			&task.CreatedAt, &task.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("扫描任务数据失败: %v", err)
		}
		tasks = append(tasks, task)
	}
	
	return tasks, nil
}

// ClaimTask 认领任务（使用悲观锁）
func ClaimTask(taskID, workerID string) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("开启事务失败: %v", err)
	}
	defer tx.Rollback()
	
	// 使用 FOR UPDATE 锁定行
	query := `SELECT status FROM tasks WHERE task_id = ? FOR UPDATE`
	var status string
	err = tx.QueryRow(query, taskID).Scan(&status)
	if err != nil {
		return fmt.Errorf("查询任务失败: %v", err)
	}
	
	if status != "pending" {
		return fmt.Errorf("任务状态不是pending: %s", status)
	}
	
	// 更新状态为running
	updateQuery := `UPDATE tasks SET status = 'running', worker_id = ?, started_at = NOW(), 
	                updated_at = NOW() WHERE task_id = ?`
	_, err = tx.Exec(updateQuery, workerID, taskID)
	if err != nil {
		return fmt.Errorf("更新任务状态失败: %v", err)
	}
	
	return tx.Commit()
}

// UpdateTaskStatus 更新任务状态
func UpdateTaskStatus(taskID, status string, result *string, errorMsg *string) error {
	var query string
	var args []interface{}
	
	if status == "completed" {
		query = `UPDATE tasks SET status = ?, result = ?, completed_at = NOW(), updated_at = NOW() 
		         WHERE task_id = ?`
		args = []interface{}{status, result, taskID}
	} else if status == "failed" {
		query = `UPDATE tasks SET status = ?, error_message = ?, updated_at = NOW() 
		         WHERE task_id = ?`
		args = []interface{}{status, errorMsg, taskID}
	} else {
		query = `UPDATE tasks SET status = ?, updated_at = NOW() WHERE task_id = ?`
		args = []interface{}{status, taskID}
	}
	
	_, err := db.Exec(query, args...)
	if err != nil {
		return fmt.Errorf("更新任务状态失败: %v", err)
	}
	
	return nil
}

// IncrementTaskRetry 增加任务重试次数
func IncrementTaskRetry(taskID string) error {
	query := `UPDATE tasks SET retry_count = retry_count + 1, status = 'pending', 
	          worker_id = NULL, updated_at = NOW() WHERE task_id = ?`
	
	_, err := db.Exec(query, taskID)
	if err != nil {
		return fmt.Errorf("增加任务重试次数失败: %v", err)
	}
	
	return nil
}

// GetTasksByWorkflowID 获取工作流的所有任务
func GetTasksByWorkflowID(workflowID string) ([]*Task, error) {
	query := `SELECT id, task_id, workflow_id, task_name, task_type, sequence_order, 
	          status, payload, result, error_message, retry_count, max_retries, worker_id, 
	          started_at, completed_at, created_at, updated_at 
	          FROM tasks 
	          WHERE workflow_id = ? 
	          ORDER BY sequence_order ASC`
	
	rows, err := db.Query(query, workflowID)
	if err != nil {
		return nil, fmt.Errorf("查询工作流任务失败: %v", err)
	}
	defer rows.Close()
	
	var tasks []*Task
	for rows.Next() {
		task := &Task{}
		err := rows.Scan(
			&task.ID, &task.TaskID, &task.WorkflowID, &task.TaskName, &task.TaskType,
			&task.SequenceOrder, &task.Status, &task.Payload, &task.Result, &task.ErrorMessage,
			&task.RetryCount, &task.MaxRetries, &task.WorkerID, &task.StartedAt, &task.CompletedAt,
			&task.CreatedAt, &task.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("扫描任务数据失败: %v", err)
		}
		tasks = append(tasks, task)
	}
	
	return tasks, nil
}
