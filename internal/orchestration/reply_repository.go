package orchestration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/jinleili-zz/nsp-platform/taskqueue"

	"workflow_qoder/internal/models"
)

type ReplyDecision string

const (
	ReplyDecisionApplied   ReplyDecision = "applied"
	ReplyDecisionDuplicate ReplyDecision = "duplicate"
	ReplyDecisionStale     ReplyDecision = "stale"
)

func (r *WorkflowRepository) HandleReplyTx(ctx context.Context, consumerName string, message *taskqueue.Task) (ReplyDecision, error) {
	if r == nil || r.db == nil || consumerName == "" || message == nil {
		return "", fmt.Errorf("reply repository, consumer, and message are required")
	}
	var reply ReplyPayload
	if err := json.Unmarshal(message.Payload, &reply); err != nil {
		return "", fmt.Errorf("decode reply payload: %w", err)
	}
	if err := validateV2Reply(reply, message.Metadata); err != nil {
		return "", err
	}
	payloadHash, err := replyMessageHash(message)
	if err != nil {
		return "", err
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	inserted, err := insertInboxEvent(ctx, tx, consumerName, reply.EventID, payloadHash)
	if err != nil {
		return "", err
	}
	if !inserted {
		var existingHash string
		if err := tx.QueryRowContext(ctx, `SELECT payload_hash FROM inbox_events WHERE consumer_name = $1 AND event_id = $2`, consumerName, reply.EventID).Scan(&existingHash); err != nil {
			return "", err
		}
		if existingHash != payloadHash {
			return "", fmt.Errorf("inbox event payload conflict: %s", reply.EventID)
		}
		if err := tx.Commit(); err != nil {
			return "", err
		}
		return ReplyDecisionDuplicate, nil
	}

	current, err := loadWorkflowTaskForUpdate(ctx, tx, reply.TaskID)
	if err != nil {
		return "", fmt.Errorf("load reply task: %w", err)
	}
	if reply.Generation < current.Generation || reply.Attempt < current.Attempt {
		if err := markInboxResult(ctx, tx, consumerName, reply.EventID, "stale"); err != nil {
			return "", err
		}
		if err := tx.Commit(); err != nil {
			return "", err
		}
		return ReplyDecisionStale, nil
	}
	if err := validateReplyIdentity(reply, current); err != nil {
		return "", err
	}
	staleResource, err := staleResourceGeneration(ctx, tx, current)
	if err != nil {
		return "", err
	}
	if staleResource {
		if err := markInboxResult(ctx, tx, consumerName, reply.EventID, "stale_resource_generation"); err != nil {
			return "", err
		}
		if err := tx.Commit(); err != nil {
			return "", err
		}
		return ReplyDecisionStale, nil
	}
	if current.Status == models.TaskStatusCompleted || current.Status == models.TaskStatusFailed || current.Status == models.TaskStatusCancelled {
		if err := markInboxResult(ctx, tx, consumerName, reply.EventID, "duplicate_terminal"); err != nil {
			return "", err
		}
		if err := tx.Commit(); err != nil {
			return "", err
		}
		return ReplyDecisionDuplicate, nil
	}

	if reply.Status == ReplyStatusFailed && !reply.FinalFailure {
		if _, err := tx.ExecContext(ctx, `
			UPDATE tasks
			SET status = 'retrying', retry_count = $1, max_retries = $2,
			    error_message = $3, attempt = GREATEST(attempt, $4),
			    last_event_id = $5, version = version + 1, updated_at = NOW()
			WHERE id = $6 AND version = $7
		`, reply.RetryCount, reply.MaxRetries, reply.Error, reply.Attempt+1, reply.EventID, current.ID, current.Version); err != nil {
			return "", err
		}
		if err := markInboxResult(ctx, tx, consumerName, reply.EventID, "retrying"); err != nil {
			return "", err
		}
		if err := tx.Commit(); err != nil {
			return "", err
		}
		return ReplyDecisionApplied, nil
	}

	nextStatus := models.TaskStatusCompleted
	if reply.Status == ReplyStatusFailed {
		nextStatus = models.TaskStatusFailed
	} else if reply.Status != ReplyStatusSuccess {
		return "", fmt.Errorf("unknown reply status: %s", reply.Status)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE tasks
		SET status = $1, result = $2::jsonb, error_message = NULLIF($3, ''),
		    completed_at = NOW(), last_event_id = $4,
		    version = version + 1, updated_at = NOW()
		WHERE id = $5 AND version = $6
		  AND status IN ('pending', 'queued', 'running', 'retrying')
	`, nextStatus, nullableReplyResult(reply.Result), reply.Error, reply.EventID, current.ID, current.Version)
	if err != nil {
		return "", err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return "", err
	}
	if rows != 1 {
		return "", fmt.Errorf("reply task CAS lost: %s", current.ID)
	}

	total, completed, failed, err := workflowTaskStats(ctx, tx, current.WorkflowID, current.Generation)
	if err != nil {
		return "", err
	}
	resourceStatus := models.ResourceStatusCreating
	resourceError := ""
	if failed > 0 {
		resourceStatus = models.ResourceStatusFailed
		resourceError = reply.Error
	} else if completed == total {
		resourceStatus = models.ResourceStatusRunning
	}
	if err := updateResourceAggregate(ctx, tx, current, total, completed, failed, resourceStatus, resourceError); err != nil {
		return "", err
	}
	if err := updateOperationFromAggregate(ctx, tx, current, completed, failed, total, reply.Error); err != nil {
		return "", err
	}

	if nextStatus == models.TaskStatusCompleted && completed < total {
		nextTask, err := loadNextWorkflowTask(ctx, tx, current.WorkflowID, current.Generation, current.TaskOrder+1)
		if err != nil {
			return "", err
		}
		if err := r.insertTaskOutbox(ctx, tx, nextTask, total, func(string, int) string { return nextTask.Destination }, nextTask.ReplyQueue); err != nil {
			return "", err
		}
	}
	if err := markInboxResult(ctx, tx, consumerName, reply.EventID, string(nextStatus)); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return ReplyDecisionApplied, nil
}

func validateV2Reply(reply ReplyPayload, metadata map[string]string) error {
	if reply.ProtocolVersion != TaskProtocolVersion {
		return fmt.Errorf("unsupported reply protocol version: %d", reply.ProtocolVersion)
	}
	if reply.EventID == "" || reply.OperationID == "" || reply.RootOperationID == "" ||
		reply.WorkflowID == "" || reply.TaskID == "" || reply.Generation <= 0 || reply.Attempt < 0 ||
		reply.StepName == "" || reply.StepOrdinal <= 0 || reply.OperationKey == "" || reply.DesiredHash == "" ||
		reply.ResourceID == "" || reply.ResourceType == "" || reply.TaskType == "" {
		return fmt.Errorf("reply v2 identity is incomplete")
	}
	if metadata[MetadataKeyEventID] != reply.EventID ||
		metadata[MetadataKeyOperationID] != reply.OperationID ||
		metadata[MetadataKeyRootOperationID] != reply.RootOperationID ||
		metadata[MetadataKeyWorkflowID] != reply.WorkflowID ||
		metadata[MetadataKeyTaskID] != reply.TaskID ||
		metadata[MetadataKeyGeneration] != strconv.FormatInt(reply.Generation, 10) ||
		metadata[MetadataKeyAttempt] != strconv.Itoa(reply.Attempt) ||
		metadata[MetadataKeyStepName] != reply.StepName ||
		metadata[MetadataKeyStepOrdinal] != strconv.Itoa(reply.StepOrdinal) ||
		metadata[MetadataKeyOperationKey] != reply.OperationKey ||
		metadata[MetadataKeyDesiredHash] != reply.DesiredHash ||
		metadata[MetadataKeyResourceID] != reply.ResourceID ||
		metadata[MetadataKeyResourceType] != reply.ResourceType {
		return fmt.Errorf("reply payload and metadata identity mismatch")
	}
	return nil
}

func validateReplyIdentity(reply ReplyPayload, task *models.Task) error {
	if reply.Generation > task.Generation || reply.Attempt > task.Attempt {
		return fmt.Errorf("reply identity is from a future generation or attempt")
	}
	if reply.OperationID != task.OperationID || reply.RootOperationID != task.RootOperationID ||
		reply.WorkflowID != task.WorkflowID || reply.ResourceID != task.ResourceID ||
		reply.ResourceType != string(task.ResourceType) || reply.TaskType != task.TaskType ||
		reply.StepName != task.StepName || reply.StepIndex != task.TaskOrder-1 || reply.StepOrdinal != task.TaskOrder {
		return fmt.Errorf("reply identity does not belong to task %s", task.ID)
	}
	wantDesiredHash, err := DesiredSpecHash([]byte(task.TaskParams))
	if err != nil {
		return err
	}
	wantOperationKey := fmt.Sprintf("%s:%s:%s:gen:%d", task.DeviceType, task.StepName, task.ResourceID, task.Generation)
	if reply.DesiredHash != wantDesiredHash || reply.OperationKey != wantOperationKey {
		return fmt.Errorf("reply command identity does not belong to task %s", task.ID)
	}
	return nil
}

func replyMessageHash(message *taskqueue.Task) (string, error) {
	encoded, err := json.Marshal(struct {
		Payload  json.RawMessage   `json:"payload"`
		Metadata map[string]string `json:"metadata"`
	}{Payload: message.Payload, Metadata: message.Metadata})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func insertInboxEvent(ctx context.Context, tx *sql.Tx, consumerName, eventID, payloadHash string) (bool, error) {
	result, err := tx.ExecContext(ctx, `
		INSERT INTO inbox_events (consumer_name, event_id, payload_hash)
		VALUES ($1, $2, $3)
		ON CONFLICT (consumer_name, event_id) DO NOTHING
	`, consumerName, eventID, payloadHash)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}

func markInboxResult(ctx context.Context, tx *sql.Tx, consumerName, eventID, resultCode string) error {
	_, err := tx.ExecContext(ctx, `UPDATE inbox_events SET result_code = $1, processed_at = NOW() WHERE consumer_name = $2 AND event_id = $3`, resultCode, consumerName, eventID)
	return err
}

func loadWorkflowTaskForUpdate(ctx context.Context, tx *sql.Tx, taskID string) (*models.Task, error) {
	return scanDurableTask(tx.QueryRowContext(ctx, durableTaskSelect+` WHERE id = $1 FOR UPDATE`, taskID))
}

func loadNextWorkflowTask(ctx context.Context, tx *sql.Tx, workflowID string, generation int64, taskOrder int) (*models.Task, error) {
	return scanDurableTask(tx.QueryRowContext(ctx, durableTaskSelect+` WHERE workflow_id = $1 AND generation = $2 AND task_order = $3 FOR UPDATE`, workflowID, generation, taskOrder))
}

const durableTaskSelect = `
	SELECT id, operation_id, root_operation_id, workflow_id, generation,
	       step_name, attempt, version, last_event_id, protocol_version, operation_required,
	       destination, reply_queue, resource_type::text, resource_id,
	       task_type, task_name, task_order, task_params::text, status,
	       priority, device_type, retry_count, max_retries, az
	FROM tasks`

type sqlRow interface {
	Scan(dest ...any) error
}

func scanDurableTask(row sqlRow) (*models.Task, error) {
	task := &models.Task{}
	var lastEventID, destination, replyQueue sql.NullString
	err := row.Scan(
		&task.ID, &task.OperationID, &task.RootOperationID, &task.WorkflowID, &task.Generation,
		&task.StepName, &task.Attempt, &task.Version, &lastEventID, &task.ProtocolVersion, &task.OperationRequired,
		&destination, &replyQueue, &task.ResourceType, &task.ResourceID,
		&task.TaskType, &task.TaskName, &task.TaskOrder, &task.TaskParams, &task.Status,
		&task.Priority, &task.DeviceType, &task.RetryCount, &task.MaxRetries, &task.AZ,
	)
	if err != nil {
		return nil, err
	}
	task.LastEventID = lastEventID.String
	task.Destination = destination.String
	task.ReplyQueue = replyQueue.String
	return task, nil
}

func workflowTaskStats(ctx context.Context, tx *sql.Tx, workflowID string, generation int64) (total, completed, failed int, err error) {
	err = tx.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COUNT(*) FILTER (WHERE status = 'completed'),
		       COUNT(*) FILTER (WHERE status = 'failed')
		FROM tasks WHERE workflow_id = $1 AND generation = $2
	`, workflowID, generation).Scan(&total, &completed, &failed)
	return
}

func updateResourceAggregate(ctx context.Context, tx *sql.Tx, task *models.Task, total, completed, failed int, status models.ResourceStatus, errorMessage string) error {
	table, ok := resourceTable(task.ResourceType)
	if !ok {
		return fmt.Errorf("unsupported resource type: %s", task.ResourceType)
	}
	query := fmt.Sprintf(`
		UPDATE %s
		SET total_tasks = $1, completed_tasks = $2, failed_tasks = $3,
		    status = $4, error_message = NULLIF($5, ''), updated_at = NOW()
		WHERE id = $6 AND generation = $7 AND current_operation_id = $8
	`, table)
	result, err := tx.ExecContext(ctx, query, total, completed, failed, status, errorMessage, task.ResourceID, task.Generation, task.OperationID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("reply resource not found: %s", task.ResourceID)
	}
	return nil
}

func staleResourceGeneration(ctx context.Context, tx *sql.Tx, task *models.Task) (bool, error) {
	table, ok := resourceTable(task.ResourceType)
	if !ok {
		return false, fmt.Errorf("unsupported resource type: %s", task.ResourceType)
	}
	query := fmt.Sprintf(`SELECT generation, current_operation_id FROM %s WHERE id = $1 FOR UPDATE`, table)
	var generation int64
	var currentOperationID sql.NullString
	if err := tx.QueryRowContext(ctx, query, task.ResourceID).Scan(&generation, &currentOperationID); err != nil {
		return false, err
	}
	if task.Generation > generation {
		return false, fmt.Errorf("task generation %d is ahead of resource generation %d", task.Generation, generation)
	}
	return task.Generation < generation || currentOperationID.String != task.OperationID, nil
}

func nullableReplyResult(result json.RawMessage) any {
	if len(result) == 0 {
		return nil
	}
	return string(result)
}

func updateOperationFromAggregate(ctx context.Context, tx *sql.Tx, task *models.Task, completed, failed, total int, errorMessage string) error {
	if failed > 0 {
		result, err := tx.ExecContext(ctx, `
			UPDATE orchestration_operations
			SET status = 'failed', error_code = 'WORKFLOW_STEP_FAILED', error_message = $1,
			    completed_at = NOW(), version = version + 1, updated_at = NOW()
			WHERE operation_id = $2 AND generation = $3
			  AND status IN ('accepted', 'dispatching', 'running')
		`, errorMessage, task.OperationID, task.Generation)
		if err != nil {
			return err
		}
		return requireOperationUpdate(ctx, tx, task, result, "failed")
	}
	if completed == total {
		result, err := tx.ExecContext(ctx, `
			UPDATE orchestration_operations
			SET status = 'succeeded', completed_at = NOW(), version = version + 1, updated_at = NOW()
			WHERE operation_id = $1 AND generation = $2 AND status = 'running'
		`, task.OperationID, task.Generation)
		if err != nil {
			return err
		}
		return requireOperationUpdate(ctx, tx, task, result, "succeeded")
	}
	return nil
}

func requireOperationUpdate(ctx context.Context, tx *sql.Tx, task *models.Task, result sql.Result, expectedStatus string) error {
	if !task.OperationRequired {
		return nil
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 1 {
		return nil
	}
	var status, resourceID string
	var generation int64
	if err := tx.QueryRowContext(ctx, `SELECT status, COALESCE(resource_id, ''), generation FROM orchestration_operations WHERE operation_id = $1 FOR UPDATE`, task.OperationID).Scan(&status, &resourceID, &generation); err != nil {
		return fmt.Errorf("required task operation is missing: %s: %w", task.OperationID, err)
	}
	if status == expectedStatus && resourceID == task.ResourceID && generation == task.Generation {
		return nil
	}
	return fmt.Errorf("required task operation update lost: %s status=%s resource=%s generation=%d", task.OperationID, status, resourceID, generation)
}
