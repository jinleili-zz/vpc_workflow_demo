package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jinleili-zz/nsp-platform/logger"
	"github.com/jinleili-zz/nsp-platform/taskqueue"
)

type claimedReply struct {
	EventID string
	Payload []byte
}

func (r *Runtime) DispatchOnce(ctx context.Context) (int, error) {
	if r == nil || r.db == nil || r.broker == nil {
		return 0, fmt.Errorf("Worker dispatcher dependencies are required")
	}
	if _, err := r.db.ExecContext(ctx, `
		UPDATE outbox_events
		SET status = 'retry', locked_at = NULL, locked_by = NULL,
		    available_at = NOW(), updated_at = NOW()
		WHERE owner_service = $1 AND aggregate_type = 'worker_operation'
		  AND status = 'publishing' AND locked_at < NOW() - interval '30 seconds'
	`, r.ownerService); err != nil {
		return 0, err
	}
	workerID := uuidString()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `
		WITH candidates AS (
			SELECT event_id FROM outbox_events
			WHERE owner_service = $1 AND aggregate_type = 'worker_operation'
			  AND status IN ('pending','retry') AND available_at <= NOW()
			ORDER BY available_at, created_at
			FOR UPDATE SKIP LOCKED LIMIT $2
		)
		UPDATE outbox_events event
		SET status = 'publishing', locked_at = NOW(), locked_by = $3,
		    publish_attempts = publish_attempts + 1, updated_at = NOW()
		FROM candidates
		WHERE event.event_id = candidates.event_id
		RETURNING event.event_id, event.payload::text
	`, r.ownerService, r.batchSize, workerID)
	if err != nil {
		return 0, err
	}
	var claimed []claimedReply
	for rows.Next() {
		var item claimedReply
		var payload string
		if err := rows.Scan(&item.EventID, &payload); err != nil {
			rows.Close()
			return 0, err
		}
		item.Payload = []byte(payload)
		claimed = append(claimed, item)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}

	published := 0
	var dispatchErrors []error
	for _, item := range claimed {
		var envelope replyEnvelope
		if err := json.Unmarshal(item.Payload, &envelope); err != nil {
			dispatchErrors = append(dispatchErrors, err)
			_ = r.retryReply(ctx, item.EventID, workerID, err)
			continue
		}
		_, err := r.broker.Publish(ctx, &taskqueue.Task{
			Type: envelope.Type, Payload: envelope.Payload,
			Queue: envelope.Queue, Metadata: envelope.Metadata,
		})
		if err != nil {
			dispatchErrors = append(dispatchErrors, err)
			_ = r.retryReply(ctx, item.EventID, workerID, err)
			continue
		}
		result, err := r.db.ExecContext(ctx, `
			UPDATE outbox_events
			SET status = 'published', published_at = NOW(), locked_at = NULL,
			    locked_by = NULL, last_error = NULL, updated_at = NOW()
			WHERE event_id = $1 AND owner_service = $2
			  AND status = 'publishing' AND locked_by = $3
		`, item.EventID, r.ownerService, workerID)
		if err != nil {
			dispatchErrors = append(dispatchErrors, err)
			continue
		}
		if rows, _ := result.RowsAffected(); rows == 1 {
			published++
		}
	}
	return published, errors.Join(dispatchErrors...)
}

func (r *Runtime) retryReply(ctx context.Context, eventID, workerID string, cause error) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE outbox_events
		SET status = 'retry', available_at = NOW() + interval '1 second',
		    locked_at = NULL, locked_by = NULL, last_error = $1, updated_at = NOW()
		WHERE event_id = $2 AND owner_service = $3
		  AND status = 'publishing' AND locked_by = $4
	`, cause.Error(), eventID, r.ownerService, workerID)
	return err
}

func (r *Runtime) RunDispatcher(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if _, err := r.DispatchOnce(ctx); err != nil {
			logger.ErrorContext(ctx, "Worker Reply Outbox dispatch failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func uuidString() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}
