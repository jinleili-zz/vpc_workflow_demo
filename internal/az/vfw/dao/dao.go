package dao

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"workflow_qoder/internal/models"
)

type FirewallPolicyDAO struct {
	db *sql.DB
}

func NewFirewallPolicyDAO(db *sql.DB) *FirewallPolicyDAO {
	return &FirewallPolicyDAO{db: db}
}

func (d *FirewallPolicyDAO) Create(ctx context.Context, policy *models.FirewallPolicy) error {
	query := `
		INSERT INTO firewall_policies (
			id, policy_name, source_zone, dest_zone, source_ip, dest_ip,
			source_port, dest_port, protocol, action, description,
			status, total_tasks, completed_tasks, failed_tasks, region, az
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
	`
	_, err := d.db.ExecContext(ctx, query,
		policy.ID, policy.PolicyName, policy.SourceZone, policy.DestZone,
		policy.SourceIP, policy.DestIP, policy.SourcePort, policy.DestPort,
		policy.Protocol, policy.Action, policy.Description,
		policy.Status, policy.TotalTasks, policy.CompletedTasks, policy.FailedTasks,
		policy.Region, policy.AZ,
	)
	return err
}

func (d *FirewallPolicyDAO) GetByID(ctx context.Context, id string) (*models.FirewallPolicy, error) {
	query := `
		SELECT id, policy_name, source_zone, dest_zone, source_ip, dest_ip,
			   source_port, dest_port, protocol, action, description,
			   status, error_message, total_tasks, completed_tasks, failed_tasks,
			   region, az, created_at, updated_at
		FROM firewall_policies WHERE id = $1
	`
	policy := &models.FirewallPolicy{}
	var desc, errMsg sql.NullString

	err := d.db.QueryRowContext(ctx, query, id).Scan(
		&policy.ID, &policy.PolicyName, &policy.SourceZone, &policy.DestZone,
		&policy.SourceIP, &policy.DestIP, &policy.SourcePort, &policy.DestPort,
		&policy.Protocol, &policy.Action, &desc,
		&policy.Status, &errMsg, &policy.TotalTasks, &policy.CompletedTasks, &policy.FailedTasks,
		&policy.Region, &policy.AZ, &policy.CreatedAt, &policy.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	if desc.Valid {
		policy.Description = desc.String
	}
	if errMsg.Valid {
		policy.ErrorMessage = errMsg.String
	}

	return policy, nil
}

func (d *FirewallPolicyDAO) GetByName(ctx context.Context, name, az string) (*models.FirewallPolicy, error) {
	query := `
		SELECT id, policy_name, source_zone, dest_zone, source_ip, dest_ip,
			   source_port, dest_port, protocol, action, description,
			   status, error_message, total_tasks, completed_tasks, failed_tasks,
			   region, az, created_at, updated_at
		FROM firewall_policies WHERE policy_name = $1 AND az = $2
	`
	policy := &models.FirewallPolicy{}
	var desc, errMsg sql.NullString

	err := d.db.QueryRowContext(ctx, query, name, az).Scan(
		&policy.ID, &policy.PolicyName, &policy.SourceZone, &policy.DestZone,
		&policy.SourceIP, &policy.DestIP, &policy.SourcePort, &policy.DestPort,
		&policy.Protocol, &policy.Action, &desc,
		&policy.Status, &errMsg, &policy.TotalTasks, &policy.CompletedTasks, &policy.FailedTasks,
		&policy.Region, &policy.AZ, &policy.CreatedAt, &policy.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	if desc.Valid {
		policy.Description = desc.String
	}
	if errMsg.Valid {
		policy.ErrorMessage = errMsg.String
	}

	return policy, nil
}

func (d *FirewallPolicyDAO) UpdateStatus(ctx context.Context, id string, status models.ResourceStatus, errorMsg string) error {
	query := `UPDATE firewall_policies SET status = $1, error_message = $2, updated_at = $3 WHERE id = $4`
	_, err := d.db.ExecContext(ctx, query, status, errorMsg, time.Now(), id)
	return err
}

func (d *FirewallPolicyDAO) UpdateTotalTasks(ctx context.Context, id string, totalTasks int) error {
	query := `UPDATE firewall_policies SET total_tasks = $1, updated_at = $2 WHERE id = $3`
	_, err := d.db.ExecContext(ctx, query, totalTasks, time.Now(), id)
	return err
}

func (d *FirewallPolicyDAO) IncrementCompletedTasks(ctx context.Context, id string) error {
	query := `UPDATE firewall_policies SET completed_tasks = completed_tasks + 1, updated_at = $1 WHERE id = $2`
	_, err := d.db.ExecContext(ctx, query, time.Now(), id)
	return err
}

func (d *FirewallPolicyDAO) IncrementFailedTasks(ctx context.Context, id string) error {
	query := `UPDATE firewall_policies SET failed_tasks = failed_tasks + 1, updated_at = $1 WHERE id = $2`
	_, err := d.db.ExecContext(ctx, query, time.Now(), id)
	return err
}

func (d *FirewallPolicyDAO) Delete(ctx context.Context, id string) error {
	query := `DELETE FROM firewall_policies WHERE id = $1`
	_, err := d.db.ExecContext(ctx, query, id)
	return err
}

func (d *FirewallPolicyDAO) ListAll(ctx context.Context) ([]*models.FirewallPolicy, error) {
	query := `
		SELECT id, policy_name, source_zone, dest_zone, source_ip, dest_ip,
			   source_port, dest_port, protocol, action, description,
			   status, error_message, total_tasks, completed_tasks, failed_tasks,
			   region, az, created_at, updated_at
		FROM firewall_policies
		WHERE status != 'deleted'
		ORDER BY created_at DESC
	`
	rows, err := d.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var policies []*models.FirewallPolicy
	for rows.Next() {
		policy := &models.FirewallPolicy{}
		var desc, errMsg sql.NullString

		err := rows.Scan(
			&policy.ID, &policy.PolicyName, &policy.SourceZone, &policy.DestZone,
			&policy.SourceIP, &policy.DestIP, &policy.SourcePort, &policy.DestPort,
			&policy.Protocol, &policy.Action, &desc,
			&policy.Status, &errMsg, &policy.TotalTasks, &policy.CompletedTasks, &policy.FailedTasks,
			&policy.Region, &policy.AZ, &policy.CreatedAt, &policy.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}

		if desc.Valid {
			policy.Description = desc.String
		}
		if errMsg.Valid {
			policy.ErrorMessage = errMsg.String
		}

		policies = append(policies, policy)
	}
	return policies, rows.Err()
}

func (d *FirewallPolicyDAO) CountByZone(ctx context.Context, zone string) (int, error) {
	query := `
		SELECT COUNT(*) FROM firewall_policies 
		WHERE (source_zone = $1 OR dest_zone = $2) AND status NOT IN ('deleted', 'failed')
	`
	var count int
	err := d.db.QueryRowContext(ctx, query, zone, zone).Scan(&count)
	return count, err
}

type VFWTaskDAO struct {
	db *sql.DB
}

func NewVFWTaskDAO(db *sql.DB) *VFWTaskDAO {
	return &VFWTaskDAO{db: db}
}

func (d *VFWTaskDAO) Create(ctx context.Context, task *models.Task) error {
	query := `
		INSERT INTO tasks (
			id, resource_type, resource_id, task_type, task_name, task_order,
			task_params, status, priority, device_type, retry_count, max_retries, az
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`
	_, err := d.db.ExecContext(ctx, query,
		task.ID, task.ResourceType, task.ResourceID, task.TaskType, task.TaskName, task.TaskOrder,
		task.TaskParams, task.Status, task.Priority, task.DeviceType, task.RetryCount, task.MaxRetries, task.AZ,
	)
	return err
}

func (d *VFWTaskDAO) BatchCreate(ctx context.Context, tasks []*models.Task) error {
	if len(tasks) == 0 {
		return nil
	}

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	query := `
		INSERT INTO tasks (
			id, resource_type, resource_id, task_type, task_name, task_order,
			task_params, status, priority, device_type, retry_count, max_retries, az
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`

	stmt, err := tx.PrepareContext(ctx, query)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, task := range tasks {
		_, err := stmt.ExecContext(ctx,
			task.ID, task.ResourceType, task.ResourceID, task.TaskType, task.TaskName, task.TaskOrder,
			task.TaskParams, task.Status, task.Priority, task.DeviceType, task.RetryCount, task.MaxRetries, task.AZ,
		)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (d *VFWTaskDAO) GetByID(ctx context.Context, id string) (*models.Task, error) {
	query := `
		SELECT id, resource_type, resource_id, task_type, task_name, task_order,
		       task_params, status, priority, device_type, asynq_task_id, result, error_message,
		       retry_count, max_retries, az, created_at, queued_at, started_at, completed_at, updated_at
		FROM tasks WHERE id = $1
	`
	task := &models.Task{}
	var asynqTaskID, result, errorMessage, deviceType sql.NullString
	var priority sql.NullInt32
	var queuedAt, startedAt, completedAt sql.NullTime

	err := d.db.QueryRowContext(ctx, query, id).Scan(
		&task.ID, &task.ResourceType, &task.ResourceID, &task.TaskType, &task.TaskName, &task.TaskOrder,
		&task.TaskParams, &task.Status, &priority, &deviceType, &asynqTaskID, &result, &errorMessage,
		&task.RetryCount, &task.MaxRetries, &task.AZ, &task.CreatedAt, &queuedAt, &startedAt, &completedAt, &task.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	if priority.Valid {
		task.Priority = int(priority.Int32)
	}
	if deviceType.Valid {
		task.DeviceType = deviceType.String
	}
	if asynqTaskID.Valid {
		task.AsynqTaskID = asynqTaskID.String
	}
	if result.Valid {
		task.Result = result.String
	}
	if errorMessage.Valid {
		task.ErrorMessage = errorMessage.String
	}
	if queuedAt.Valid {
		task.QueuedAt = &queuedAt.Time
	}
	if startedAt.Valid {
		task.StartedAt = &startedAt.Time
	}
	if completedAt.Valid {
		task.CompletedAt = &completedAt.Time
	}

	return task, nil
}

func (d *VFWTaskDAO) GetByResourceID(ctx context.Context, resourceID string) ([]*models.Task, error) {
	query := `
		SELECT id, resource_type, resource_id, task_type, task_name, task_order,
		       task_params, status, priority, device_type, asynq_task_id, result, error_message,
		       retry_count, max_retries, az, created_at, queued_at, started_at, completed_at, updated_at
		FROM tasks WHERE resource_id = $1 ORDER BY task_order ASC
	`
	rows, err := d.db.QueryContext(ctx, query, resourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*models.Task
	for rows.Next() {
		task := &models.Task{}
		var asynqTaskID, result, errorMessage, deviceType sql.NullString
		var priority sql.NullInt32
		var queuedAt, startedAt, completedAt sql.NullTime

		err := rows.Scan(
			&task.ID, &task.ResourceType, &task.ResourceID, &task.TaskType, &task.TaskName, &task.TaskOrder,
			&task.TaskParams, &task.Status, &priority, &deviceType, &asynqTaskID, &result, &errorMessage,
			&task.RetryCount, &task.MaxRetries, &task.AZ, &task.CreatedAt, &queuedAt, &startedAt, &completedAt, &task.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}

		if priority.Valid {
			task.Priority = int(priority.Int32)
		}
		if deviceType.Valid {
			task.DeviceType = deviceType.String
		}
		if asynqTaskID.Valid {
			task.AsynqTaskID = asynqTaskID.String
		}
		if result.Valid {
			task.Result = result.String
		}
		if errorMessage.Valid {
			task.ErrorMessage = errorMessage.String
		}
		if queuedAt.Valid {
			task.QueuedAt = &queuedAt.Time
		}
		if startedAt.Valid {
			task.StartedAt = &startedAt.Time
		}
		if completedAt.Valid {
			task.CompletedAt = &completedAt.Time
		}

		tasks = append(tasks, task)
	}

	return tasks, rows.Err()
}

func (d *VFWTaskDAO) GetNextPendingTask(ctx context.Context, resourceID string) (*models.Task, error) {
	query := `
		SELECT id, resource_type, resource_id, task_type, task_name, task_order,
		       task_params, status, priority, device_type, asynq_task_id, result, error_message,
		       retry_count, max_retries, az, created_at, queued_at, started_at, completed_at, updated_at
		FROM tasks 
		WHERE resource_id = $1 AND status = 'pending'
		ORDER BY task_order ASC LIMIT 1
	`
	task := &models.Task{}
	var asynqTaskID, result, errorMessage, deviceType sql.NullString
	var priority sql.NullInt32
	var queuedAt, startedAt, completedAt sql.NullTime

	err := d.db.QueryRowContext(ctx, query, resourceID).Scan(
		&task.ID, &task.ResourceType, &task.ResourceID, &task.TaskType, &task.TaskName, &task.TaskOrder,
		&task.TaskParams, &task.Status, &priority, &deviceType, &asynqTaskID, &result, &errorMessage,
		&task.RetryCount, &task.MaxRetries, &task.AZ, &task.CreatedAt, &queuedAt, &startedAt, &completedAt, &task.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if priority.Valid {
		task.Priority = int(priority.Int32)
	}
	if deviceType.Valid {
		task.DeviceType = deviceType.String
	}
	if asynqTaskID.Valid {
		task.AsynqTaskID = asynqTaskID.String
	}
	if result.Valid {
		task.Result = result.String
	}
	if errorMessage.Valid {
		task.ErrorMessage = errorMessage.String
	}
	if queuedAt.Valid {
		task.QueuedAt = &queuedAt.Time
	}
	if startedAt.Valid {
		task.StartedAt = &startedAt.Time
	}
	if completedAt.Valid {
		task.CompletedAt = &completedAt.Time
	}

	return task, nil
}

func (d *VFWTaskDAO) UpdateStatus(ctx context.Context, id string, status models.TaskStatus) error {
	now := time.Now()
	var query string
	var args []interface{}

	if status == models.TaskStatusQueued {
		query = `UPDATE tasks SET status = $1, updated_at = $2, queued_at = $3 WHERE id = $4`
		args = []interface{}{status, now, now, id}
	} else if status == models.TaskStatusRunning {
		query = `UPDATE tasks SET status = $1, updated_at = $2, started_at = $3 WHERE id = $4`
		args = []interface{}{status, now, now, id}
	} else if status == models.TaskStatusCompleted || status == models.TaskStatusFailed {
		query = `UPDATE tasks SET status = $1, updated_at = $2, completed_at = $3 WHERE id = $4`
		args = []interface{}{status, now, now, id}
	} else {
		query = `UPDATE tasks SET status = $1, updated_at = $2 WHERE id = $3`
		args = []interface{}{status, now, id}
	}

	_, err := d.db.ExecContext(ctx, query, args...)
	return err
}

func (d *VFWTaskDAO) UpdateResult(ctx context.Context, id string, status models.TaskStatus, result interface{}, errorMsg string) error {
	now := time.Now()
	resultJSON, _ := json.Marshal(result)

	query := `
		UPDATE tasks 
		SET status = $1, result = $2, error_message = $3, completed_at = $4, updated_at = $5
		WHERE id = $6
	`
	_, err := d.db.ExecContext(ctx, query, status, string(resultJSON), errorMsg, now, now, id)
	return err
}

func (d *VFWTaskDAO) UpdateAsynqTaskID(ctx context.Context, id, asynqTaskID string) error {
	query := `UPDATE tasks SET asynq_task_id = $1, updated_at = $2 WHERE id = $3`
	_, err := d.db.ExecContext(ctx, query, asynqTaskID, time.Now(), id)
	return err
}

func (d *VFWTaskDAO) GetTaskStats(ctx context.Context, resourceID string) (total, completed, failed int, err error) {
	query := `
		SELECT 
			COUNT(*) as total,
			SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END) as completed,
			SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END) as failed
		FROM tasks WHERE resource_id = $1
	`
	err = d.db.QueryRowContext(ctx, query, resourceID).Scan(&total, &completed, &failed)
	return
}
