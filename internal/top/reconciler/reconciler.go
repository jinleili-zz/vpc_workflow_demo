package reconciler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"workflow_qoder/internal/operation"

	"github.com/jinleili-zz/nsp-platform/logger"
)

type Execution struct {
	OperationID       string
	OperationType     string
	ResourceID        string
	Region            string
	AZ                string
	ChildOperationID  string
	SagaTransactionID string
	Status            operation.Status
	ErrorCode         string
	ErrorMessage      string
	Version           int64
}

type ChildResult struct {
	Status       operation.Status
	ErrorCode    string
	ErrorMessage string
}

type AggregateResult struct {
	OperationID string
	ResourceID  string
	Status      operation.Status
	Changed     bool
}

type PollFunc func(context.Context, Execution) (ChildResult, error)
type AggregateHook func(context.Context, AggregateResult) error

type Repository struct {
	db           *sql.DB
	ownerService string
}

func NewRepository(db *sql.DB, ownerService string) *Repository {
	return &Repository{db: db, ownerService: ownerService}
}

func (r *Repository) RecordExecution(ctx context.Context, execution Execution) error {
	if r == nil || r.db == nil || r.ownerService == "" {
		return fmt.Errorf("Top execution repository dependencies are required")
	}
	if execution.OperationID == "" || execution.Region == "" || execution.AZ == "" || execution.ChildOperationID == "" {
		return fmt.Errorf("Top execution identity is required")
	}
	result, err := r.db.ExecContext(ctx, `
		INSERT INTO operation_az_executions (
			operation_id, region, az, child_operation_id, saga_transaction_id, status
		)
		SELECT $1::varchar, $2::varchar, $3::varchar, $4::varchar, NULLIF($5::varchar, ''), 'running'
		FROM orchestration_operations
		WHERE operation_id = $1 AND owner_service = $6
		ON CONFLICT (operation_id, region, az) DO UPDATE
		SET child_operation_id = COALESCE(operation_az_executions.child_operation_id, EXCLUDED.child_operation_id),
			saga_transaction_id = COALESCE(operation_az_executions.saga_transaction_id, EXCLUDED.saga_transaction_id),
			updated_at = NOW()
		WHERE operation_az_executions.child_operation_id IS NULL
		   OR operation_az_executions.child_operation_id = EXCLUDED.child_operation_id
	`, execution.OperationID, execution.Region, execution.AZ, execution.ChildOperationID, execution.SagaTransactionID, r.ownerService)
	if err != nil {
		return fmt.Errorf("record Top AZ execution: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("Top AZ execution identity conflict: %s/%s/%s", execution.OperationID, execution.Region, execution.AZ)
	}
	return nil
}

func (r *Repository) ClaimBatch(ctx context.Context, owner string, limit int, lease time.Duration) ([]Execution, error) {
	if r == nil || r.db == nil || r.ownerService == "" || owner == "" || limit <= 0 {
		return nil, fmt.Errorf("Top execution claim dependencies are required")
	}
	if lease <= 0 {
		lease = 30 * time.Second
	}
	rows, err := r.db.QueryContext(ctx, `
		WITH candidates AS (
			SELECT execution.operation_id, execution.region, execution.az
			FROM operation_az_executions execution
			JOIN orchestration_operations operation USING (operation_id)
			WHERE operation.owner_service = $1 AND operation.status IN ('running', 'compensating')
			  AND execution.status IN ('accepted', 'dispatching', 'running', 'compensating')
			  AND (execution.lease_until IS NULL OR execution.lease_until < NOW())
			ORDER BY execution.updated_at, execution.operation_id, execution.region, execution.az
			FOR UPDATE OF execution SKIP LOCKED
			LIMIT $2
		)
		UPDATE operation_az_executions execution
		SET lease_owner = $3,
			lease_until = NOW() + ($4 * INTERVAL '1 millisecond'),
			updated_at = NOW()
		FROM candidates, orchestration_operations operation
		WHERE execution.operation_id = candidates.operation_id
		  AND execution.region = candidates.region AND execution.az = candidates.az
		  AND operation.operation_id = execution.operation_id
		RETURNING execution.operation_id, operation.operation_type, COALESCE(operation.resource_id, ''),
			execution.region, execution.az, COALESCE(execution.child_operation_id, ''),
			COALESCE(execution.saga_transaction_id, ''), execution.status,
			COALESCE(execution.error_code, ''), COALESCE(execution.error_message, ''), execution.version
	`, r.ownerService, limit, owner, lease.Milliseconds())
	if err != nil {
		return nil, fmt.Errorf("claim Top AZ executions: %w", err)
	}
	defer rows.Close()
	var executions []Execution
	for rows.Next() {
		var execution Execution
		if err := rows.Scan(&execution.OperationID, &execution.OperationType, &execution.ResourceID,
			&execution.Region, &execution.AZ, &execution.ChildOperationID, &execution.SagaTransactionID,
			&execution.Status, &execution.ErrorCode, &execution.ErrorMessage, &execution.Version); err != nil {
			return nil, err
		}
		executions = append(executions, execution)
	}
	return executions, rows.Err()
}

func (r *Repository) UpdateClaim(ctx context.Context, execution Execution, owner string, result ChildResult) (bool, error) {
	if result.Status != operation.StatusRunning && result.Status != operation.StatusSucceeded && result.Status != operation.StatusFailed && result.Status != operation.StatusCompensating && result.Status != operation.StatusCompensated && result.Status != operation.StatusCompensationFailed {
		return false, fmt.Errorf("invalid child operation status: %s", result.Status)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	updated, err := tx.ExecContext(ctx, `
		UPDATE operation_az_executions
		SET status = CASE
				WHEN status IN ('succeeded', 'failed', 'compensated', 'compensation_failed') THEN status
				ELSE $1
			END,
			error_code = NULLIF($2, ''), error_message = NULLIF($3, ''),
			lease_owner = NULL, lease_until = NULL, version = version + 1, updated_at = NOW()
		WHERE operation_id = $4 AND region = $5 AND az = $6
		  AND lease_owner = $7 AND lease_until > NOW() AND version = $8
	`, result.Status, result.ErrorCode, result.ErrorMessage, execution.OperationID, execution.Region, execution.AZ, owner, execution.Version)
	if err != nil {
		return false, fmt.Errorf("update claimed Top AZ execution: %w", err)
	}
	rows, err := updated.RowsAffected()
	if err != nil || rows != 1 {
		return false, err
	}
	if execution.OperationType == "apply_firewall_policy" {
		recordStatus := vfwAZRecordStatus(result.Status)
		recordResult, err := tx.ExecContext(ctx, `
			UPDATE policy_az_records
			SET status = CASE
					WHEN status IN ('failed', 'deleted') THEN status
					WHEN status = 'running' AND $1 = 'creating' THEN status
					ELSE $1
				END,
				error_message = NULLIF($2, ''), updated_at = NOW()
			WHERE policy_id = $3 AND region = $4 AND az = $5
		`, recordStatus, result.ErrorMessage, execution.ResourceID, execution.Region, execution.AZ)
		if err != nil {
			return false, fmt.Errorf("update fenced VFW AZ record: %w", err)
		}
		recordRows, err := recordResult.RowsAffected()
		if err != nil {
			return false, err
		}
		if recordRows != 1 {
			return false, fmt.Errorf("VFW AZ record is missing: %s/%s/%s", execution.ResourceID, execution.Region, execution.AZ)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func vfwAZRecordStatus(status operation.Status) string {
	switch status {
	case operation.StatusSucceeded:
		return "running"
	case operation.StatusFailed, operation.StatusCompensationFailed:
		return "failed"
	case operation.StatusCompensating:
		return "compensating"
	case operation.StatusCompensated:
		return "deleted"
	default:
		return "creating"
	}
}

func (r *Repository) DeferClaim(ctx context.Context, execution Execution, owner string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE operation_az_executions
		SET lease_owner = NULL, lease_until = NULL, updated_at = NOW()
		WHERE operation_id = $1 AND region = $2 AND az = $3 AND lease_owner = $4
	`, execution.OperationID, execution.Region, execution.AZ, owner)
	return err
}

func AggregateStatuses(statuses []operation.Status) operation.Status {
	if len(statuses) == 0 {
		return operation.StatusRunning
	}
	allTerminal := true
	hasSucceeded := false
	hasFailed := false
	hasCompensating := false
	hasCompensated := false
	hasCompensationFailed := false
	for _, status := range statuses {
		switch status {
		case operation.StatusSucceeded:
			hasSucceeded = true
		case operation.StatusFailed:
			hasFailed = true
		case operation.StatusCompensating:
			hasCompensating = true
			allTerminal = false
		case operation.StatusCompensated:
			hasCompensated = true
		case operation.StatusCompensationFailed:
			hasCompensationFailed = true
		default:
			allTerminal = false
		}
	}
	if !allTerminal {
		if hasCompensating || hasCompensated || hasCompensationFailed {
			return operation.StatusCompensating
		}
		return operation.StatusRunning
	}
	if hasCompensationFailed || (hasCompensated && (hasSucceeded || hasFailed)) {
		return operation.StatusCompensationFailed
	}
	if hasFailed {
		return operation.StatusFailed
	}
	if hasCompensated {
		return operation.StatusCompensated
	}
	return operation.StatusSucceeded
}

func (r *Repository) Aggregate(ctx context.Context, operationID string) (AggregateResult, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return AggregateResult{}, err
	}
	defer tx.Rollback()
	var operationType, resourceID string
	var current operation.Status
	if err := tx.QueryRowContext(ctx, `
		SELECT operation_type, COALESCE(resource_id, ''), status
		FROM orchestration_operations
		WHERE operation_id = $1 AND owner_service = $2
		FOR UPDATE
	`, operationID, r.ownerService).Scan(&operationType, &resourceID, &current); err != nil {
		return AggregateResult{}, err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT status, COALESCE(error_code, ''), COALESCE(error_message, '')
		FROM operation_az_executions WHERE operation_id = $1
		ORDER BY region, az
	`, operationID)
	if err != nil {
		return AggregateResult{}, err
	}
	var statuses []operation.Status
	var errorCode, errorMessage string
	for rows.Next() {
		var status operation.Status
		var childCode, childMessage string
		if err := rows.Scan(&status, &childCode, &childMessage); err != nil {
			rows.Close()
			return AggregateResult{}, err
		}
		statuses = append(statuses, status)
		if errorCode == "" && (status == operation.StatusFailed || status == operation.StatusCompensationFailed) {
			errorCode, errorMessage = childCode, childMessage
		}
	}
	if err := rows.Close(); err != nil {
		return AggregateResult{}, err
	}
	aggregated := AggregateStatuses(statuses)
	next := aggregated
	if current == operation.StatusRunning && (aggregated == operation.StatusCompensating || aggregated == operation.StatusCompensated || aggregated == operation.StatusCompensationFailed) {
		next = operation.StatusCompensating
	}
	result := AggregateResult{OperationID: operationID, ResourceID: resourceID, Status: next}
	if current.Terminal() || next == current {
		if !current.Terminal() {
			if _, err := tx.ExecContext(ctx, `UPDATE orchestration_operations SET updated_at = NOW() WHERE operation_id = $1`, operationID); err != nil {
				return AggregateResult{}, err
			}
		}
		return result, tx.Commit()
	}
	if !operation.CanTransition(operationType, current, next) {
		return AggregateResult{}, fmt.Errorf("invalid Top aggregate transition %s -> %s", current, next)
	}
	if !next.Terminal() {
		patch, _ := json.Marshal(map[string]any{"status": next})
		updated, err := tx.ExecContext(ctx, `
			UPDATE orchestration_operations
			SET status = $1,
				response_payload = COALESCE(response_payload, '{}'::jsonb) || $2::jsonb,
				version = version + 1, updated_at = NOW()
			WHERE operation_id = $3 AND status = $4
		`, next, patch, operationID, current)
		if err != nil {
			return AggregateResult{}, err
		}
		rows, err := updated.RowsAffected()
		if err != nil {
			return AggregateResult{}, err
		}
		result.Changed = rows == 1
		if err := tx.Commit(); err != nil {
			return AggregateResult{}, err
		}
		return result, nil
	}
	responseCode := "0"
	success := true
	message := "operation completed"
	if next == operation.StatusCompensated {
		success = false
		responseCode = "COMPENSATED"
		message = "operation was rolled back and the resource is absent"
	} else if next == operation.StatusFailed || next == operation.StatusCompensationFailed {
		success = false
		responseCode = errorCode
		if responseCode == "" {
			responseCode = "AZ_CHILD_FAILED"
		}
		if errorMessage == "" {
			errorMessage = "an AZ child operation failed"
		}
		message = errorMessage
	}
	patch, _ := json.Marshal(map[string]any{"code": responseCode, "success": success, "status": next, "message": message})
	if err := updateTopResourceAggregateTx(ctx, tx, operationType, resourceID, next, errorMessage); err != nil {
		return AggregateResult{}, err
	}
	if next == operation.StatusCompensated {
		if _, err := tx.ExecContext(ctx, `
			UPDATE orchestration_target_claims
			SET active = FALSE, updated_at = NOW()
			WHERE operation_id = $1 AND active = TRUE
		`, operationID); err != nil {
			return AggregateResult{}, fmt.Errorf("release compensated target claim: %w", err)
		}
	}
	updated, err := tx.ExecContext(ctx, `
		UPDATE orchestration_operations
		SET status = $1, response_code = $2,
			response_payload = COALESCE(response_payload, '{}'::jsonb) || $3::jsonb,
			error_code = NULLIF($4, ''), error_message = NULLIF($5, ''),
			completed_at = NOW(), version = version + 1, updated_at = NOW()
		WHERE operation_id = $6 AND status = $7
	`, next, responseCode, patch, errorCode, errorMessage, operationID, current)
	if err != nil {
		return AggregateResult{}, err
	}
	changed, err := updated.RowsAffected()
	if err != nil {
		return AggregateResult{}, err
	}
	result.Changed = changed == 1
	if err := tx.Commit(); err != nil {
		return AggregateResult{}, err
	}
	return result, nil
}

func updateTopResourceAggregateTx(ctx context.Context, tx *sql.Tx, operationType, resourceID string, status operation.Status, errorMessage string) error {
	resourceStatus := "running"
	if status == operation.StatusCompensated {
		resourceStatus = "deleted"
	} else if status == operation.StatusFailed || status == operation.StatusCompensationFailed {
		resourceStatus = "failed"
	}
	var result sql.Result
	var err error
	switch operationType {
	case "create_subnet":
		result, err = tx.ExecContext(ctx, `UPDATE subnet_registry SET status = $1, updated_at = NOW() WHERE id = $2`, resourceStatus, resourceID)
	case "apply_firewall_policy":
		result, err = tx.ExecContext(ctx, `UPDATE policy_registry SET status = $1, error_message = NULLIF($2, ''), updated_at = NOW() WHERE id = $3`, resourceStatus, errorMessage, resourceID)
	default:
		return nil
	}
	if err != nil {
		return fmt.Errorf("update Top resource aggregate: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("Top aggregate resource is missing: %s", resourceID)
	}
	return nil
}

func (r *Repository) ListAggregateCandidates(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("positive aggregate candidate limit is required")
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT operation.operation_id
		FROM orchestration_operations operation
		WHERE operation.owner_service = $1
		  AND operation.status IN ('running', 'compensating')
		  AND EXISTS (SELECT 1 FROM operation_az_executions execution WHERE execution.operation_id = operation.operation_id)
		ORDER BY operation.updated_at, operation.operation_id
		LIMIT $2
	`, r.ownerService, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var operationIDs []string
	for rows.Next() {
		var operationID string
		if err := rows.Scan(&operationID); err != nil {
			return nil, err
		}
		operationIDs = append(operationIDs, operationID)
	}
	return operationIDs, rows.Err()
}

func (r *Repository) DeferAggregate(ctx context.Context, operationID string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE orchestration_operations
		SET updated_at = NOW()
		WHERE operation_id = $1 AND owner_service = $2
		  AND status IN ('running', 'compensating')
	`, operationID, r.ownerService)
	return err
}

type Reconciler struct {
	repository *Repository
	workerID   string
	poll       PollFunc
	hook       AggregateHook
	batchSize  int
	lease      time.Duration
}

func New(repository *Repository, workerID string, poll PollFunc, hook AggregateHook) *Reconciler {
	return &Reconciler{repository: repository, workerID: workerID, poll: poll, hook: hook, batchSize: 100, lease: 30 * time.Second}
}

func (r *Reconciler) RunOnce(ctx context.Context) (int, error) {
	if r == nil || r.repository == nil || r.poll == nil || r.workerID == "" {
		return 0, fmt.Errorf("Top reconciler dependencies are required")
	}
	executions, err := r.repository.ClaimBatch(ctx, r.workerID, r.batchSize, r.lease)
	if err != nil {
		return 0, err
	}
	operationIDs := make(map[string]struct{})
	processed := 0
	for _, execution := range executions {
		result, pollErr := r.poll(ctx, execution)
		if pollErr != nil {
			_ = r.repository.DeferClaim(ctx, execution, r.workerID)
			logger.WarnContext(ctx, "Top child operation poll deferred", "operation_id", execution.OperationID, "region", execution.Region, "az", execution.AZ, "error", pollErr)
			continue
		}
		updated, updateErr := r.repository.UpdateClaim(ctx, execution, r.workerID, result)
		if updateErr != nil {
			_ = r.repository.DeferClaim(ctx, execution, r.workerID)
			logger.WarnContext(ctx, "Top child operation update deferred", "operation_id", execution.OperationID, "region", execution.Region, "az", execution.AZ, "error", updateErr)
			continue
		}
		if updated {
			processed++
			operationIDs[execution.OperationID] = struct{}{}
		}
	}
	candidates, err := r.repository.ListAggregateCandidates(ctx, r.batchSize)
	if err != nil {
		return processed, err
	}
	for _, operationID := range candidates {
		operationIDs[operationID] = struct{}{}
	}
	ordered := make([]string, 0, len(operationIDs))
	for operationID := range operationIDs {
		ordered = append(ordered, operationID)
	}
	sort.Strings(ordered)
	for _, operationID := range ordered {
		result, err := r.repository.Aggregate(ctx, operationID)
		if err != nil {
			_ = r.repository.DeferAggregate(ctx, operationID)
			logger.WarnContext(ctx, "Top operation aggregate deferred", "operation_id", operationID, "error", err)
			continue
		}
		if result.Changed && r.hook != nil {
			if err := r.hook(ctx, result); err != nil {
				logger.WarnContext(ctx, "Top operation aggregate hook failed", "operation_id", operationID, "error", err)
			}
		}
	}
	return processed, nil
}

func (r *Reconciler) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Second
	}
	_, _ = r.RunOnce(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = r.RunOnce(ctx)
		}
	}
}
