package dao

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"workflow_qoder/internal/models"
)

type VPCDAO struct {
	db *sql.DB
}

func NewVPCDAO(db *sql.DB) *VPCDAO {
	return &VPCDAO{db: db}
}

func (d *VPCDAO) Create(ctx context.Context, vpc *models.VPCResource) error {
	query := `
		INSERT INTO vpc_resources (
			id, vpc_name, region, az, vrf_name, vlan_id, firewall_zone,
			status, total_tasks, completed_tasks, failed_tasks
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`
	_, err := d.db.ExecContext(ctx, query,
		vpc.ID, vpc.VPCName, vpc.Region, vpc.AZ, vpc.VRFName, vpc.VLANId, vpc.FirewallZone,
		vpc.Status, vpc.TotalTasks, vpc.CompletedTasks, vpc.FailedTasks,
	)
	return err
}

func (d *VPCDAO) GetByName(ctx context.Context, vpcName, az string) (*models.VPCResource, error) {
	query := `
		SELECT id, vpc_name, region, az, vrf_name, vlan_id, firewall_zone,
		       status, error_message, total_tasks, completed_tasks, failed_tasks,
		       created_at, updated_at
		FROM vpc_resources
		WHERE vpc_name = $1 AND az = $2
	`
	vpc := &models.VPCResource{}
	var errorMessage sql.NullString

	err := d.db.QueryRowContext(ctx, query, vpcName, az).Scan(
		&vpc.ID, &vpc.VPCName, &vpc.Region, &vpc.AZ, &vpc.VRFName, &vpc.VLANId, &vpc.FirewallZone,
		&vpc.Status, &errorMessage, &vpc.TotalTasks, &vpc.CompletedTasks, &vpc.FailedTasks,
		&vpc.CreatedAt, &vpc.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	if errorMessage.Valid {
		vpc.ErrorMessage = errorMessage.String
	}

	return vpc, nil
}

func (d *VPCDAO) GetByID(ctx context.Context, id string) (*models.VPCResource, error) {
	query := `
		SELECT id, vpc_name, region, az, vrf_name, vlan_id, firewall_zone,
		       status, error_message, total_tasks, completed_tasks, failed_tasks,
		       created_at, updated_at
		FROM vpc_resources
		WHERE id = $1
	`
	vpc := &models.VPCResource{}
	var errorMessage sql.NullString

	err := d.db.QueryRowContext(ctx, query, id).Scan(
		&vpc.ID, &vpc.VPCName, &vpc.Region, &vpc.AZ, &vpc.VRFName, &vpc.VLANId, &vpc.FirewallZone,
		&vpc.Status, &errorMessage, &vpc.TotalTasks, &vpc.CompletedTasks, &vpc.FailedTasks,
		&vpc.CreatedAt, &vpc.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	if errorMessage.Valid {
		vpc.ErrorMessage = errorMessage.String
	}

	return vpc, nil
}

func (d *VPCDAO) UpdateStatus(ctx context.Context, id string, status models.ResourceStatus, errorMsg string) error {
	query := `UPDATE vpc_resources SET status = $1, error_message = $2, updated_at = $3 WHERE id = $4`
	_, err := d.db.ExecContext(ctx, query, status, errorMsg, time.Now(), id)
	return err
}

func (d *VPCDAO) UpdateTotalTasks(ctx context.Context, id string, totalTasks int) error {
	query := `UPDATE vpc_resources SET total_tasks = $1, updated_at = $2 WHERE id = $3`
	_, err := d.db.ExecContext(ctx, query, totalTasks, time.Now(), id)
	return err
}

func (d *VPCDAO) IncrementCompletedTasks(ctx context.Context, id string) error {
	query := `UPDATE vpc_resources SET completed_tasks = completed_tasks + 1, updated_at = $1 WHERE id = $2`
	_, err := d.db.ExecContext(ctx, query, time.Now(), id)
	return err
}

func (d *VPCDAO) IncrementFailedTasks(ctx context.Context, id string) error {
	query := `UPDATE vpc_resources SET failed_tasks = failed_tasks + 1, updated_at = $1 WHERE id = $2`
	_, err := d.db.ExecContext(ctx, query, time.Now(), id)
	return err
}

func (d *VPCDAO) CountSubnets(ctx context.Context, vpcName, az string) (int, error) {
	query := `SELECT COUNT(*) FROM subnet_resources WHERE vpc_name = $1 AND az = $2 AND status != 'deleted'`
	var count int
	err := d.db.QueryRowContext(ctx, query, vpcName, az).Scan(&count)
	return count, err
}

func (d *VPCDAO) Delete(ctx context.Context, id string) error {
	query := `DELETE FROM vpc_resources WHERE id = $1`
	_, err := d.db.ExecContext(ctx, query, id)
	return err
}

func (d *VPCDAO) ListAll(ctx context.Context) ([]*models.VPCResource, error) {
	query := `
		SELECT id, vpc_name, region, az, vrf_name, vlan_id, firewall_zone,
		       status, error_message, total_tasks, completed_tasks, failed_tasks,
		       created_at, updated_at
		FROM vpc_resources
		WHERE status != 'deleted'
		ORDER BY created_at DESC
	`
	rows, err := d.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var vpcs []*models.VPCResource
	for rows.Next() {
		vpc := &models.VPCResource{}
		var errorMessage sql.NullString
		err := rows.Scan(
			&vpc.ID, &vpc.VPCName, &vpc.Region, &vpc.AZ, &vpc.VRFName, &vpc.VLANId, &vpc.FirewallZone,
			&vpc.Status, &errorMessage, &vpc.TotalTasks, &vpc.CompletedTasks, &vpc.FailedTasks,
			&vpc.CreatedAt, &vpc.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		if errorMessage.Valid {
			vpc.ErrorMessage = errorMessage.String
		}
		vpcs = append(vpcs, vpc)
	}
	return vpcs, rows.Err()
}

func (d *VPCDAO) CountSubnetsByVPCID(ctx context.Context, vpcID string) (int, error) {
	query := `
		SELECT COUNT(*) FROM subnet_resources s
		JOIN vpc_resources v ON s.vpc_name = v.vpc_name AND s.az = v.az
		WHERE v.id = $1 AND s.status != 'deleted'`
	var count int
	err := d.db.QueryRowContext(ctx, query, vpcID).Scan(&count)
	return count, err
}

type SubnetDAO struct {
	db *sql.DB
}

func NewSubnetDAO(db *sql.DB) *SubnetDAO {
	return &SubnetDAO{db: db}
}

func (d *SubnetDAO) Create(ctx context.Context, subnet *models.SubnetResource) error {
	query := `
		INSERT INTO subnet_resources (
			id, subnet_name, vpc_name, region, az, cidr,
			status, total_tasks, completed_tasks, failed_tasks
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`
	_, err := d.db.ExecContext(ctx, query,
		subnet.ID, subnet.SubnetName, subnet.VPCName, subnet.Region, subnet.AZ, subnet.CIDR,
		subnet.Status, subnet.TotalTasks, subnet.CompletedTasks, subnet.FailedTasks,
	)
	return err
}

func (d *SubnetDAO) GetByName(ctx context.Context, subnetName, az string) (*models.SubnetResource, error) {
	query := `
		SELECT id, subnet_name, vpc_name, region, az, cidr,
		       status, error_message, total_tasks, completed_tasks, failed_tasks,
		       created_at, updated_at
		FROM subnet_resources
		WHERE subnet_name = $1 AND az = $2
	`
	subnet := &models.SubnetResource{}
	var errorMessage sql.NullString

	err := d.db.QueryRowContext(ctx, query, subnetName, az).Scan(
		&subnet.ID, &subnet.SubnetName, &subnet.VPCName, &subnet.Region, &subnet.AZ, &subnet.CIDR,
		&subnet.Status, &errorMessage, &subnet.TotalTasks, &subnet.CompletedTasks, &subnet.FailedTasks,
		&subnet.CreatedAt, &subnet.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	if errorMessage.Valid {
		subnet.ErrorMessage = errorMessage.String
	}

	return subnet, nil
}

func (d *SubnetDAO) GetByID(ctx context.Context, id string) (*models.SubnetResource, error) {
	query := `
		SELECT id, subnet_name, vpc_name, region, az, cidr,
		       status, error_message, total_tasks, completed_tasks, failed_tasks,
		       created_at, updated_at
		FROM subnet_resources
		WHERE id = $1
	`
	subnet := &models.SubnetResource{}
	var errorMessage sql.NullString

	err := d.db.QueryRowContext(ctx, query, id).Scan(
		&subnet.ID, &subnet.SubnetName, &subnet.VPCName, &subnet.Region, &subnet.AZ, &subnet.CIDR,
		&subnet.Status, &errorMessage, &subnet.TotalTasks, &subnet.CompletedTasks, &subnet.FailedTasks,
		&subnet.CreatedAt, &subnet.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	if errorMessage.Valid {
		subnet.ErrorMessage = errorMessage.String
	}

	return subnet, nil
}

func (d *SubnetDAO) UpdateStatus(ctx context.Context, id string, status models.ResourceStatus, errorMsg string) error {
	query := `UPDATE subnet_resources SET status = $1, error_message = $2, updated_at = $3 WHERE id = $4`
	_, err := d.db.ExecContext(ctx, query, status, errorMsg, time.Now(), id)
	return err
}

func (d *SubnetDAO) UpdateTotalTasks(ctx context.Context, id string, totalTasks int) error {
	query := `UPDATE subnet_resources SET total_tasks = $1, updated_at = $2 WHERE id = $3`
	_, err := d.db.ExecContext(ctx, query, totalTasks, time.Now(), id)
	return err
}

func (d *SubnetDAO) IncrementCompletedTasks(ctx context.Context, id string) error {
	query := `UPDATE subnet_resources SET completed_tasks = completed_tasks + 1, updated_at = $1 WHERE id = $2`
	_, err := d.db.ExecContext(ctx, query, time.Now(), id)
	return err
}

func (d *SubnetDAO) IncrementFailedTasks(ctx context.Context, id string) error {
	query := `UPDATE subnet_resources SET failed_tasks = failed_tasks + 1, updated_at = $1 WHERE id = $2`
	_, err := d.db.ExecContext(ctx, query, time.Now(), id)
	return err
}

func (d *SubnetDAO) Delete(ctx context.Context, id string) error {
	query := `DELETE FROM subnet_resources WHERE id = $1`
	_, err := d.db.ExecContext(ctx, query, id)
	return err
}

func (d *SubnetDAO) ListByVPCName(ctx context.Context, vpcName, az string) ([]*models.SubnetResource, error) {
	query := `
		SELECT id, subnet_name, vpc_name, region, az, cidr,
		       status, error_message, total_tasks, completed_tasks, failed_tasks,
		       created_at, updated_at
		FROM subnet_resources
		WHERE vpc_name = $1 AND az = $2 AND status != 'deleted'
		ORDER BY created_at DESC
	`
	rows, err := d.db.QueryContext(ctx, query, vpcName, az)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subnets []*models.SubnetResource
	for rows.Next() {
		subnet := &models.SubnetResource{}
		var errorMessage sql.NullString
		err := rows.Scan(
			&subnet.ID, &subnet.SubnetName, &subnet.VPCName, &subnet.Region, &subnet.AZ, &subnet.CIDR,
			&subnet.Status, &errorMessage, &subnet.TotalTasks, &subnet.CompletedTasks, &subnet.FailedTasks,
			&subnet.CreatedAt, &subnet.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		if errorMessage.Valid {
			subnet.ErrorMessage = errorMessage.String
		}
		subnets = append(subnets, subnet)
	}
	return subnets, rows.Err()
}

func (d *SubnetDAO) ListByVPCID(ctx context.Context, vpcID string) ([]*models.SubnetResource, error) {
	query := `
		SELECT s.id, s.subnet_name, s.vpc_name, s.region, s.az, s.cidr,
		       s.status, s.error_message, s.total_tasks, s.completed_tasks, s.failed_tasks,
		       s.created_at, s.updated_at
		FROM subnet_resources s
		INNER JOIN vpc_resources v ON s.vpc_name = v.vpc_name AND s.az = v.az
		WHERE v.id = $1 AND s.status != 'deleted'
		ORDER BY s.created_at DESC
	`
	rows, err := d.db.QueryContext(ctx, query, vpcID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subnets []*models.SubnetResource
	for rows.Next() {
		subnet := &models.SubnetResource{}
		var errorMessage sql.NullString
		err := rows.Scan(
			&subnet.ID, &subnet.SubnetName, &subnet.VPCName, &subnet.Region, &subnet.AZ, &subnet.CIDR,
			&subnet.Status, &errorMessage, &subnet.TotalTasks, &subnet.CompletedTasks, &subnet.FailedTasks,
			&subnet.CreatedAt, &subnet.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		if errorMessage.Valid {
			subnet.ErrorMessage = errorMessage.String
		}
		subnets = append(subnets, subnet)
	}
	return subnets, rows.Err()
}

type TaskDAO struct {
	db *sql.DB
}

func NewTaskDAO(db *sql.DB) *TaskDAO {
	return &TaskDAO{db: db}
}

func (d *TaskDAO) Create(ctx context.Context, task *models.Task) error {
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

func (d *TaskDAO) BatchCreate(ctx context.Context, tasks []*models.Task) error {
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

func (d *TaskDAO) GetByID(ctx context.Context, id string) (*models.Task, error) {
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

func (d *TaskDAO) GetByResourceID(ctx context.Context, resourceID string) ([]*models.Task, error) {
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

func (d *TaskDAO) GetNextPendingTask(ctx context.Context, resourceID string) (*models.Task, error) {
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

func (d *TaskDAO) UpdateStatus(ctx context.Context, id string, status models.TaskStatus) error {
	now := time.Now()
	query := `UPDATE tasks SET status = $1, updated_at = $2`
	args := []interface{}{status, now}
	paramIndex := 3

	if status == models.TaskStatusQueued {
		query += fmt.Sprintf(`, queued_at = $%d`, paramIndex)
		args = append(args, now)
		paramIndex++
	} else if status == models.TaskStatusRunning {
		query += fmt.Sprintf(`, started_at = $%d`, paramIndex)
		args = append(args, now)
		paramIndex++
	} else if status == models.TaskStatusCompleted || status == models.TaskStatusFailed {
		query += fmt.Sprintf(`, completed_at = $%d`, paramIndex)
		args = append(args, now)
		paramIndex++
	}

	query += fmt.Sprintf(` WHERE id = $%d`, paramIndex)
	args = append(args, id)

	_, err := d.db.ExecContext(ctx, query, args...)
	return err
}

func (d *TaskDAO) UpdateStatusAndResetRetry(ctx context.Context, id string, status models.TaskStatus) error {
	now := time.Now()
	query := `UPDATE tasks SET status = $1, retry_count = 0, updated_at = $2 WHERE id = $3`
	_, err := d.db.ExecContext(ctx, query, status, now, id)
	return err
}

func (d *TaskDAO) UpdateResult(ctx context.Context, id string, status models.TaskStatus, result interface{}, errorMsg string) error {
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

func (d *TaskDAO) UpdateAsynqTaskID(ctx context.Context, id, asynqTaskID string) error {
	query := `UPDATE tasks SET asynq_task_id = $1, updated_at = $2 WHERE id = $3`
	_, err := d.db.ExecContext(ctx, query, asynqTaskID, time.Now(), id)
	return err
}

func (d *TaskDAO) IncrementRetryCount(ctx context.Context, id string) error {
	query := `UPDATE tasks SET retry_count = retry_count + 1, updated_at = $1 WHERE id = $2`
	_, err := d.db.ExecContext(ctx, query, time.Now(), id)
	return err
}

func (d *TaskDAO) GetTaskStats(ctx context.Context, resourceID string) (total, completed, failed int, err error) {
	query := `
		SELECT 
			COUNT(*) as total,
			COALESCE(SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END), 0) as completed,
			COALESCE(SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END), 0) as failed
		FROM tasks WHERE resource_id = $1
	`
	err = d.db.QueryRowContext(ctx, query, resourceID).Scan(&total, &completed, &failed)
	return
}