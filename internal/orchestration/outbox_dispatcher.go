package orchestration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/jinleili-zz/nsp-platform/logger"
	"github.com/jinleili-zz/nsp-platform/taskqueue"

	"workflow_qoder/internal/models"
)

const (
	defaultOutboxMaxAttempts = 10
	defaultOutboxLease       = time.Minute
)

func (r *WorkflowRepository) ClaimOutboxBatch(ctx context.Context, workerID string, limit int) ([]OutboxEvent, error) {
	if workerID == "" || limit <= 0 {
		return nil, fmt.Errorf("outbox worker ID and positive limit are required")
	}
	rows, err := r.db.QueryContext(ctx, `
		WITH candidates AS (
			SELECT event_id
			FROM outbox_events
			WHERE owner_service = $1
			  AND status IN ('pending', 'retry')
			  AND available_at <= NOW()
			ORDER BY available_at, created_at
			FOR UPDATE SKIP LOCKED
			LIMIT $2
		)
		UPDATE outbox_events AS event
		SET status = 'publishing',
		    locked_by = $3,
		    locked_at = NOW(),
		    publish_attempts = publish_attempts + 1,
		    updated_at = NOW()
		FROM candidates
		WHERE event.event_id = candidates.event_id
		RETURNING event.event_id, event.event_key, event.owner_service,
		          event.aggregate_type, event.aggregate_id, event.event_type,
		          event.destination, event.payload::text, event.status,
		          event.publish_attempts, event.available_at
	`, r.ownerService, limit, workerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []OutboxEvent
	for rows.Next() {
		var event OutboxEvent
		var payload string
		if err := rows.Scan(
			&event.EventID, &event.EventKey, &event.OwnerService,
			&event.AggregateType, &event.AggregateID, &event.EventType,
			&event.Destination, &payload, &event.Status,
			&event.PublishAttempts, &event.AvailableAt,
		); err != nil {
			return nil, err
		}
		event.Payload = json.RawMessage(payload)
		events = append(events, event)
	}
	return events, rows.Err()
}

func (r *WorkflowRepository) MarkOutboxPublished(ctx context.Context, event OutboxEvent, workerID, brokerTaskID string) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE outbox_events
		SET status = 'published', published_at = NOW(), locked_at = NULL,
		    locked_by = NULL, last_error = NULL, updated_at = NOW()
		WHERE event_id = $1 AND owner_service = $2
		  AND status = 'publishing' AND locked_by = $3
	`, event.EventID, r.ownerService, workerID)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return false, err
	}
	if event.AggregateType == "task" {
		task, err := loadWorkflowTaskForUpdate(ctx, tx, event.AggregateID)
		if err != nil {
			return false, err
		}
		taskResult, err := tx.ExecContext(ctx, `
			UPDATE tasks
			SET status = 'queued', asynq_task_id = $1, queued_at = COALESCE(queued_at, NOW()),
			    last_event_id = $2, version = version + 1, updated_at = NOW()
			WHERE id = $3 AND status IN ('pending', 'queued', 'retrying')
		`, brokerTaskID, event.EventID, event.AggregateID)
		if err != nil {
			return false, err
		}
		taskRows, err := taskResult.RowsAffected()
		if err != nil {
			return false, err
		}
		if taskRows != 1 {
			return false, fmt.Errorf("outbox task is missing or terminal: %s", event.AggregateID)
		}
		operationResult, err := tx.ExecContext(ctx, `
			UPDATE orchestration_operations AS operation
			SET status = 'running', version = operation.version + 1, updated_at = NOW()
			FROM tasks AS task
			WHERE task.id = $1 AND operation.operation_id = task.operation_id
			  AND operation.status = 'dispatching'
		`, event.AggregateID)
		if err != nil {
			return false, err
		}
		if err := requireOperationUpdate(ctx, tx, task, operationResult, "running"); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (r *WorkflowRepository) MarkOutboxFailed(ctx context.Context, event OutboxEvent, workerID string, cause error) error {
	baseDelay := time.Second << minInt(event.PublishAttempts-1, 8)
	jitterWindow := baseDelay / 5
	delay := baseDelay - jitterWindow + time.Duration(rand.Int64N(int64(2*jitterWindow)+1))
	status := "retry"
	if event.PublishAttempts >= defaultOutboxMaxAttempts {
		status = "dead"
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE outbox_events
		SET status = $1, available_at = $2, locked_at = NULL, locked_by = NULL,
		    last_error = $3, updated_at = NOW()
		WHERE event_id = $4 AND owner_service = $5
		  AND status = 'publishing' AND locked_by = $6
	`, status, time.Now().Add(delay), cause.Error(), event.EventID, r.ownerService, workerID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("outbox failure ownership lost: %s", event.EventID)
	}
	if status == "dead" && event.AggregateType == "task" {
		task, err := loadWorkflowTaskForUpdate(ctx, tx, event.AggregateID)
		if err != nil {
			return err
		}
		taskResult, err := tx.ExecContext(ctx, `
			UPDATE tasks
			SET status = 'failed', error_message = $1, completed_at = NOW(),
			    last_event_id = $2, version = version + 1, updated_at = NOW()
			WHERE id = $3 AND status IN ('pending', 'queued', 'running', 'retrying')
		`, cause.Error(), event.EventID, task.ID)
		if err != nil {
			return err
		}
		taskRows, err := taskResult.RowsAffected()
		if err != nil {
			return err
		}
		if taskRows == 1 {
			total, completed, failed, err := workflowTaskStats(ctx, tx, task.WorkflowID, task.Generation)
			if err != nil {
				return err
			}
			if err := updateResourceAggregate(ctx, tx, task, total, completed, failed, models.ResourceStatusFailed, cause.Error()); err != nil {
				return err
			}
			operationResult, err := tx.ExecContext(ctx, `
				UPDATE orchestration_operations
				SET status = 'failed', error_code = 'OUTBOX_DEAD', error_message = $1,
				    completed_at = NOW(), version = version + 1, updated_at = NOW()
				WHERE operation_id = $2 AND generation = $3
				  AND status IN ('accepted', 'dispatching', 'running')
			`, cause.Error(), task.OperationID, task.Generation)
			if err != nil {
				return err
			}
			if err := requireOperationUpdate(ctx, tx, task, operationResult, "failed"); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (r *WorkflowRepository) RecoverExpiredOutboxLeases(ctx context.Context, lease time.Duration) (int64, error) {
	if lease <= 0 {
		lease = defaultOutboxLease
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE outbox_events
		SET status = 'retry', locked_at = NULL, locked_by = NULL,
		    available_at = NOW(), last_error = 'publisher lease expired', updated_at = NOW()
		WHERE owner_service = $1 AND status = 'publishing'
		  AND locked_at < NOW() - $2::interval
	`, r.ownerService, intervalLiteral(lease))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

type OutboxDispatcher struct {
	repository *WorkflowRepository
	broker     taskqueue.Broker
	workerID   string
	batchSize  int
}

func NewOutboxDispatcher(repository *WorkflowRepository, broker taskqueue.Broker, workerID string, batchSize int) *OutboxDispatcher {
	return &OutboxDispatcher{repository: repository, broker: broker, workerID: workerID, batchSize: batchSize}
}

func (d *OutboxDispatcher) DispatchOnce(ctx context.Context) (int, error) {
	if d == nil || d.repository == nil || d.broker == nil {
		return 0, fmt.Errorf("outbox dispatcher dependencies are required")
	}
	events, err := d.repository.ClaimOutboxBatch(ctx, d.workerID, d.batchSize)
	if err != nil {
		return 0, err
	}
	published := 0
	var dispatchErrors []error
	for _, event := range events {
		if event.AggregateType == "task" {
			publishedEvent, err := d.repository.PublishClaimedTaskEvent(ctx, event, d.workerID, func(payload TaskDispatchPayload) (string, error) {
				maxRetries := payload.MaxRetries
				info, err := d.broker.Publish(ctx, &taskqueue.Task{
					Type:     payload.TaskType,
					Payload:  payload.Payload,
					Queue:    payload.Queue,
					Priority: taskqueue.Priority(payload.Priority),
					MaxRetry: &maxRetries,
					Reply:    &taskqueue.ReplySpec{Queue: payload.ReplyQueue},
					Metadata: payload.Metadata,
				})
				if err != nil {
					return "", err
				}
				return info.BrokerTaskID, nil
			})
			if err != nil {
				dispatchErrors = append(dispatchErrors, err)
				if markErr := d.repository.MarkOutboxFailed(ctx, event, d.workerID, err); markErr != nil {
					dispatchErrors = append(dispatchErrors, markErr)
				}
				continue
			}
			if publishedEvent {
				published++
			}
			continue
		}
		current, err := d.repository.FenceOutboxEvent(ctx, event, d.workerID)
		if err != nil {
			dispatchErrors = append(dispatchErrors, err)
			if markErr := d.repository.MarkOutboxFailed(ctx, event, d.workerID, err); markErr != nil {
				dispatchErrors = append(dispatchErrors, markErr)
			}
			continue
		}
		if !current {
			continue
		}
		var payload TaskDispatchPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			dispatchErrors = append(dispatchErrors, err)
			_ = d.repository.MarkOutboxFailed(ctx, event, d.workerID, err)
			continue
		}
		maxRetries := payload.MaxRetries
		info, err := d.broker.Publish(ctx, &taskqueue.Task{
			Type:     payload.TaskType,
			Payload:  payload.Payload,
			Queue:    payload.Queue,
			Priority: taskqueue.Priority(payload.Priority),
			MaxRetry: &maxRetries,
			Reply:    &taskqueue.ReplySpec{Queue: payload.ReplyQueue},
			Metadata: payload.Metadata,
		})
		if err != nil {
			dispatchErrors = append(dispatchErrors, err)
			if markErr := d.repository.MarkOutboxFailed(ctx, event, d.workerID, err); markErr != nil {
				dispatchErrors = append(dispatchErrors, markErr)
			}
			continue
		}
		marked, err := d.repository.MarkOutboxPublished(ctx, event, d.workerID, info.BrokerTaskID)
		if err != nil || !marked {
			if err == nil {
				err = fmt.Errorf("outbox publish ownership lost: %s", event.EventID)
			}
			dispatchErrors = append(dispatchErrors, err)
			continue
		}
		published++
	}
	return published, errors.Join(dispatchErrors...)
}

func (r *WorkflowRepository) PublishClaimedTaskEvent(ctx context.Context, event OutboxEvent, workerID string, publish func(TaskDispatchPayload) (string, error)) (bool, error) {
	if event.AggregateType != "task" || publish == nil {
		return false, fmt.Errorf("claimed task event and publisher are required")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var payloadJSON string
	if err := tx.QueryRowContext(ctx, `
		SELECT payload::text
		FROM outbox_events
		WHERE event_id = $1 AND owner_service = $2
		  AND status = 'publishing' AND locked_by = $3
		FOR UPDATE
	`, event.EventID, r.ownerService, workerID).Scan(&payloadJSON); err != nil {
		return false, fmt.Errorf("lock claimed task outbox: %w", err)
	}
	task, err := loadWorkflowTaskForUpdate(ctx, tx, event.AggregateID)
	if err != nil {
		return false, err
	}
	stale, err := staleResourceGeneration(ctx, tx, task)
	if err != nil {
		return false, err
	}
	if stale {
		if err := cancelStaleClaimedTask(ctx, tx, r.ownerService, event, workerID, task); err != nil {
			return false, err
		}
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}

	var payload TaskDispatchPayload
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return false, fmt.Errorf("decode claimed task outbox: %w", err)
	}
	brokerTaskID, err := publish(payload)
	if err != nil {
		return false, err
	}
	outboxResult, err := tx.ExecContext(ctx, `
		UPDATE outbox_events
		SET status = 'published', published_at = NOW(), locked_at = NULL,
		    locked_by = NULL, last_error = NULL, updated_at = NOW()
		WHERE event_id = $1 AND owner_service = $2
		  AND status = 'publishing' AND locked_by = $3
	`, event.EventID, r.ownerService, workerID)
	if err != nil {
		return false, err
	}
	if rows, err := outboxResult.RowsAffected(); err != nil || rows != 1 {
		return false, fmt.Errorf("claimed task outbox ownership lost: %s", event.EventID)
	}
	taskResult, err := tx.ExecContext(ctx, `
		UPDATE tasks
		SET status = 'queued', asynq_task_id = $1, queued_at = COALESCE(queued_at, NOW()),
		    last_event_id = $2, version = version + 1, updated_at = NOW()
		WHERE id = $3 AND status IN ('pending', 'queued', 'retrying')
	`, brokerTaskID, event.EventID, task.ID)
	if err != nil {
		return false, err
	}
	if rows, err := taskResult.RowsAffected(); err != nil || rows != 1 {
		return false, fmt.Errorf("outbox task is missing or terminal: %s", task.ID)
	}
	operationResult, err := tx.ExecContext(ctx, `
		UPDATE orchestration_operations
		SET status = 'running', version = version + 1, updated_at = NOW()
		WHERE operation_id = $1 AND status = 'dispatching'
	`, task.OperationID)
	if err != nil {
		return false, err
	}
	if err := requireOperationUpdate(ctx, tx, task, operationResult, "running"); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func cancelStaleClaimedTask(ctx context.Context, tx *sql.Tx, ownerService string, event OutboxEvent, workerID string, task *models.Task) error {
	result, err := tx.ExecContext(ctx, `
		UPDATE outbox_events
		SET status = 'cancelled', locked_at = NULL, locked_by = NULL,
		    last_error = 'stale resource generation', updated_at = NOW()
		WHERE event_id = $1 AND owner_service = $2
		  AND status = 'publishing' AND locked_by = $3
	`, event.EventID, ownerService, workerID)
	if err != nil {
		return err
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		return fmt.Errorf("stale outbox ownership lost: %s", event.EventID)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE tasks
		SET status = 'cancelled', completed_at = NOW(), error_message = 'stale resource generation',
		    version = version + 1, updated_at = NOW()
		WHERE id = $1 AND status IN ('pending', 'queued', 'running', 'retrying')
	`, task.ID); err != nil {
		return err
	}
	operationResult, err := tx.ExecContext(ctx, `
		UPDATE orchestration_operations
		SET status = 'failed', error_code = 'STALE_GENERATION', error_message = 'superseded before task dispatch',
		    completed_at = NOW(), version = version + 1, updated_at = NOW()
		WHERE operation_id = $1 AND generation = $2
		  AND status IN ('accepted', 'dispatching', 'running')
	`, task.OperationID, task.Generation)
	if err != nil {
		return err
	}
	return requireOperationUpdate(ctx, tx, task, operationResult, "failed")
}

func (r *WorkflowRepository) FenceOutboxEvent(ctx context.Context, event OutboxEvent, workerID string) (bool, error) {
	if event.AggregateType != "task" {
		return true, nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	task, err := loadWorkflowTaskForUpdate(ctx, tx, event.AggregateID)
	if err != nil {
		return false, err
	}
	stale, err := staleResourceGeneration(ctx, tx, task)
	if err != nil {
		return false, err
	}
	if !stale {
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return true, nil
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE outbox_events
		SET status = 'cancelled', locked_at = NULL, locked_by = NULL,
		    last_error = 'stale resource generation', updated_at = NOW()
		WHERE event_id = $1 AND owner_service = $2
		  AND status = 'publishing' AND locked_by = $3
	`, event.EventID, r.ownerService, workerID)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if rows != 1 {
		return false, fmt.Errorf("stale outbox ownership lost: %s", event.EventID)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE tasks
		SET status = 'cancelled', completed_at = NOW(), error_message = 'stale resource generation',
		    version = version + 1, updated_at = NOW()
		WHERE id = $1 AND status IN ('pending', 'queued', 'running', 'retrying')
	`, task.ID); err != nil {
		return false, err
	}
	operationResult, err := tx.ExecContext(ctx, `
		UPDATE orchestration_operations
		SET status = 'failed', error_code = 'STALE_GENERATION', error_message = 'superseded before task dispatch',
		    completed_at = NOW(), version = version + 1, updated_at = NOW()
		WHERE operation_id = $1 AND generation = $2
		  AND status IN ('accepted', 'dispatching', 'running')
	`, task.OperationID, task.Generation)
	if err != nil {
		return false, err
	}
	if err := requireOperationUpdate(ctx, tx, task, operationResult, "failed"); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return false, nil
}

func (d *OutboxDispatcher) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if _, err := d.repository.RecoverExpiredOutboxLeases(ctx, defaultOutboxLease); err != nil {
			logger.ErrorContext(ctx, "recover outbox leases failed", "error", err)
		}
		if _, err := d.DispatchOnce(ctx); err != nil {
			logger.ErrorContext(ctx, "dispatch outbox batch failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func intervalLiteral(duration time.Duration) string {
	return fmt.Sprintf("%f seconds", duration.Seconds())
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
