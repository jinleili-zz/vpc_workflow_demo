package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"workflow_qoder/internal/orchestration"

	"github.com/google/uuid"
	"github.com/jinleili-zz/nsp-platform/taskqueue"
)

var (
	ErrExecutionBusy   = errors.New("WORKER_OPERATION_BUSY")
	ErrDesiredConflict = errors.New("WORKER_DESIRED_SPEC_CONFLICT")
	ErrLeaseLost       = errors.New("WORKER_OPERATION_LEASE_LOST")
)

type acquireDecision string

const (
	acquireExecute acquireDecision = "execute"
	acquireReplay  acquireDecision = "replay"
)

type execution struct {
	OperationKey string
	DesiredHash  string
	LeaseOwner   string
}

type executionContextKey struct{}

type replyEnvelope struct {
	Type     string            `json:"type"`
	Payload  json.RawMessage   `json:"payload"`
	Queue    string            `json:"queue"`
	Metadata map[string]string `json:"metadata"`
}

// Runtime coordinates device handlers through a durable Worker Operation
// Ledger. It also implements Broker: handler replies are persisted to an
// Outbox in the same transaction as the ledger/device ensure result.
type Runtime struct {
	db           *sql.DB
	broker       taskqueue.Broker
	ownerService string
	lease        time.Duration
	batchSize    int
	driver       DeviceDriver
	afterEnsure  func(context.Context, DeviceTarget) error
}

func NewRuntime(db *sql.DB, broker taskqueue.Broker, ownerService string) *Runtime {
	return &Runtime{
		db: db, broker: broker, ownerService: ownerService,
		lease: 30 * time.Second, batchSize: 32, driver: newSQLDeviceDriver(db),
	}
}

func (r *Runtime) Close() error { return nil }

func (r *Runtime) Wrap(next taskqueue.HandlerFunc) taskqueue.HandlerFunc {
	return func(ctx context.Context, task *taskqueue.Task) error {
		if r == nil || r.db == nil || next == nil || task == nil {
			return fmt.Errorf("worker runtime dependencies are required")
		}
		if task.Metadata[orchestration.MetadataKeyOperationKey] == "" {
			return next(ctx, task)
		}
		exec, decision, err := r.acquire(ctx, task)
		if err != nil {
			return err
		}
		if decision == acquireReplay {
			return nil
		}
		return r.runWithLease(ctx, exec, func(leaseCtx context.Context) error {
			return next(context.WithValue(leaseCtx, executionContextKey{}, exec), task)
		})
	}
}

func (r *Runtime) runWithLease(ctx context.Context, exec execution, run func(context.Context) error) error {
	leaseCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	lost := make(chan error, 1)
	renewEvery := r.lease / 3
	if renewEvery < 10*time.Millisecond {
		renewEvery = 10 * time.Millisecond
	}
	go func() {
		ticker := time.NewTicker(renewEvery)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-leaseCtx.Done():
				return
			case <-ticker.C:
				result, err := r.db.ExecContext(leaseCtx, `
					UPDATE worker_operations
					SET lease_expires_at = NOW() + ($1 * interval '1 millisecond'),
					    updated_at = NOW()
					WHERE operation_key = $2 AND desired_hash = $3
					  AND status = 'running' AND lease_owner = $4
				`, r.lease.Milliseconds(), exec.OperationKey, exec.DesiredHash, exec.LeaseOwner)
				if err == nil {
					var rows int64
					rows, err = result.RowsAffected()
					if rows != 1 {
						err = ErrLeaseLost
					}
				}
				if err != nil {
					select {
					case lost <- err:
					default:
					}
					cancel()
					return
				}
			}
		}
	}()
	err := run(leaseCtx)
	close(done)
	if err != nil {
		return err
	}
	select {
	case leaseErr := <-lost:
		return fmt.Errorf("%w: %v", ErrLeaseLost, leaseErr)
	default:
		return nil
	}
}

func (r *Runtime) acquire(ctx context.Context, task *taskqueue.Task) (execution, acquireDecision, error) {
	metadata := task.Metadata
	operationKey := metadata[orchestration.MetadataKeyOperationKey]
	desiredHash := metadata[orchestration.MetadataKeyDesiredHash]
	generation, err := strconv.ParseInt(metadata[orchestration.MetadataKeyGeneration], 10, 64)
	if err != nil || generation <= 0 || operationKey == "" || desiredHash == "" {
		return execution{}, "", fmt.Errorf("invalid Worker operation identity")
	}
	leaseOwner := uuid.NewString()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return execution{}, "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, operationKey); err != nil {
		return execution{}, "", err
	}

	var storedHash, status string
	var leaseExpires sql.NullTime
	err = tx.QueryRowContext(ctx, `
		SELECT desired_hash, status, lease_expires_at
		FROM worker_operations WHERE operation_key = $1 FOR UPDATE
	`, operationKey).Scan(&storedHash, &status, &leaseExpires)
	if err == sql.ErrNoRows {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO worker_operations (
				operation_key, root_operation_id, operation_id, workflow_id,
				task_id, resource_id, generation, device_type, target_key,
				action, desired_hash, status, lease_owner, lease_expires_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'running',$12,NOW()+($13 * interval '1 millisecond'))
		`, operationKey,
			metadata[orchestration.MetadataKeyRootOperationID],
			metadata[orchestration.MetadataKeyOperationID],
			metadata[orchestration.MetadataKeyWorkflowID],
			metadata[orchestration.MetadataKeyTaskID],
			metadata[orchestration.MetadataKeyResourceID],
			generation,
			metadata[orchestration.MetadataKeyDeviceType],
			metadata[orchestration.MetadataKeyResourceID],
			task.Type, desiredHash, leaseOwner, r.lease.Milliseconds())
		if err != nil {
			return execution{}, "", err
		}
		if err := tx.Commit(); err != nil {
			return execution{}, "", err
		}
		return execution{OperationKey: operationKey, DesiredHash: desiredHash, LeaseOwner: leaseOwner}, acquireExecute, nil
	}
	if err != nil {
		return execution{}, "", err
	}
	if storedHash != desiredHash {
		return execution{}, "", fmt.Errorf("%w: operation_key=%s", ErrDesiredConflict, operationKey)
	}
	if status == "succeeded" {
		if err := tx.Commit(); err != nil {
			return execution{}, "", err
		}
		return execution{OperationKey: operationKey, DesiredHash: desiredHash}, acquireReplay, nil
	}
	if status == "running" && leaseExpires.Valid && leaseExpires.Time.After(time.Now()) {
		return execution{}, "", fmt.Errorf("%w: operation_key=%s", ErrExecutionBusy, operationKey)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE worker_operations
		SET status = 'running', lease_owner = $1,
		    lease_expires_at = NOW() + ($2 * interval '1 millisecond'),
		    error_code = NULL, error_message = NULL, updated_at = NOW()
		WHERE operation_key = $3 AND desired_hash = $4
	`, leaseOwner, r.lease.Milliseconds(), operationKey, desiredHash)
	if err != nil {
		return execution{}, "", err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return execution{}, "", ErrLeaseLost
	}
	if err := tx.Commit(); err != nil {
		return execution{}, "", err
	}
	return execution{OperationKey: operationKey, DesiredHash: desiredHash, LeaseOwner: leaseOwner}, acquireExecute, nil
}

// Publish persists a reply rather than sending it directly. The dispatcher
// performs the external publish after the device/ledger transaction commits.
func (r *Runtime) Publish(ctx context.Context, task *taskqueue.Task) (*taskqueue.TaskInfo, error) {
	exec, ok := ctx.Value(executionContextKey{}).(execution)
	if !ok {
		if r.broker == nil || task == nil {
			return nil, fmt.Errorf("legacy Worker reply broker is unavailable")
		}
		return r.broker.Publish(ctx, task)
	}
	if task == nil || task.Queue == "" {
		return nil, fmt.Errorf("Worker reply is outside a leased execution")
	}
	var reply orchestration.ReplyPayload
	if err := json.Unmarshal(task.Payload, &reply); err != nil {
		return nil, fmt.Errorf("decode Worker reply: %w", err)
	}
	if reply.OperationKey != exec.OperationKey || reply.DesiredHash != exec.DesiredHash || reply.EventID == "" {
		return nil, fmt.Errorf("Worker reply identity does not match leased execution")
	}
	envelopeBytes, err := json.Marshal(replyEnvelope{
		Type: task.Type, Payload: task.Payload, Queue: task.Queue, Metadata: task.Metadata,
	})
	if err != nil {
		return nil, err
	}
	status := "failed"
	if reply.Status == orchestration.ReplyStatusSuccess {
		status = "succeeded"
	}

	target, err := r.loadDeviceTarget(ctx, exec)
	if err != nil {
		return nil, err
	}
	if status == "succeeded" {
		if err := r.ensureDeviceState(ctx, target, task.Payload); err != nil {
			return nil, err
		}
		if r.afterEnsure != nil {
			if err := r.afterEnsure(ctx, target); err != nil {
				return nil, err
			}
		}
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, `
		SELECT operation_key FROM worker_operations
		WHERE operation_key = $1 AND desired_hash = $2
		  AND status = 'running' AND lease_owner = $3
		FOR UPDATE
	`, exec.OperationKey, exec.DesiredHash, exec.LeaseOwner).Scan(&target.OperationKey); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrLeaseLost
		}
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE worker_operations
		SET status = $1::varchar, result_payload = $2::jsonb, error_message = $3,
		    lease_owner = NULL, lease_expires_at = NULL,
		    completed_at = CASE WHEN $1::varchar = 'succeeded' THEN NOW() ELSE NULL END,
		    updated_at = NOW()
		WHERE operation_key = $4 AND lease_owner = $5
	`, status, task.Payload, reply.Error, exec.OperationKey, exec.LeaseOwner)
	if err != nil {
		return nil, err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return nil, ErrLeaseLost
	}
	eventKey := "worker-reply:" + reply.EventID
	_, err = tx.ExecContext(ctx, `
		INSERT INTO outbox_events (
			event_id, event_key, owner_service, aggregate_type, aggregate_id,
			event_type, destination, payload, headers
		) VALUES ($1,$2,$3,'worker_operation',$4,'worker.reply.v2',$5,$6::jsonb,$7::jsonb)
		ON CONFLICT (event_key) DO NOTHING
	`, reply.EventID, eventKey, r.ownerService, exec.OperationKey, task.Queue, envelopeBytes, mustJSON(task.Metadata))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &taskqueue.TaskInfo{BrokerTaskID: "outbox-" + reply.EventID, Queue: task.Queue}, nil
}

func (r *Runtime) loadDeviceTarget(ctx context.Context, exec execution) (DeviceTarget, error) {
	var target DeviceTarget
	err := r.db.QueryRowContext(ctx, `
		SELECT operation_key, device_type, target_key, action, desired_hash,
		       resource_id, generation
		FROM worker_operations
		WHERE operation_key = $1 AND desired_hash = $2
		  AND status = 'running' AND lease_owner = $3
	`, exec.OperationKey, exec.DesiredHash, exec.LeaseOwner).Scan(
		&target.OperationKey, &target.DeviceType, &target.TargetKey,
		&target.Action, &target.DesiredHash, &target.ResourceID,
		&target.Generation,
	)
	if err == sql.ErrNoRows {
		return DeviceTarget{}, ErrLeaseLost
	}
	if err == nil {
		err = r.db.QueryRowContext(ctx, `
			SELECT COALESCE(resource_type::text, '') FROM tasks WHERE id = (
				SELECT task_id FROM worker_operations WHERE operation_key = $1
			)
		`, exec.OperationKey).Scan(&target.ResourceType)
		if err == sql.ErrNoRows {
			// Legacy v1 tasks did not persist workflow identity in tasks. They
			// remain supported during the documented compatibility window.
			err = nil
		}
	}
	return target, err
}

func (r *Runtime) ensureDeviceState(ctx context.Context, target DeviceTarget, result json.RawMessage) error {
	if r.driver == nil {
		return fmt.Errorf("device driver is required")
	}
	if !isEnsureAbsent(target.Action) {
		current, err := r.resourceGenerationCurrent(ctx, target)
		if err != nil {
			return fmt.Errorf("verify resource generation: %w", err)
		}
		if !current {
			absentTarget := target
			absentTarget.Action = "delete_stale_generation"
			if err := r.driver.EnsureAbsent(ctx, absentTarget); err != nil {
				return fmt.Errorf("remove stale device state: %w", err)
			}
			actual, err := r.driver.Get(ctx, absentTarget)
			if err != nil {
				return fmt.Errorf("verify stale device state removal: %w", err)
			}
			if !r.driver.Compare(actual, absentTarget) {
				return fmt.Errorf("stale device state remains present")
			}
			return nil
		}
	}
	actual, err := r.driver.Get(ctx, target)
	if err != nil {
		return fmt.Errorf("read device state: %w", err)
	}
	if !r.driver.Compare(actual, target) {
		if isEnsureAbsent(target.Action) {
			err = r.driver.EnsureAbsent(ctx, target)
		} else {
			err = r.driver.EnsurePresent(ctx, target, result)
		}
		if err != nil {
			return fmt.Errorf("ensure device state: %w", err)
		}
	}
	actual, err = r.driver.Get(ctx, target)
	if err != nil {
		return fmt.Errorf("verify device state: %w", err)
	}
	if !r.driver.Compare(actual, target) {
		return fmt.Errorf("device state does not match desired state")
	}
	return nil
}

func (r *Runtime) resourceGenerationCurrent(ctx context.Context, target DeviceTarget) (bool, error) {
	if target.ResourceType == "" {
		return true, nil
	}
	tables := map[string]string{
		"vpc":             "vpc_resources",
		"subnet":          "subnet_resources",
		"pccn":            "pccn_resources",
		"firewall_policy": "firewall_policies",
	}
	table := tables[target.ResourceType]
	if table == "" {
		return false, fmt.Errorf("unsupported resource type %q", target.ResourceType)
	}
	var generation int64
	query := fmt.Sprintf(`SELECT generation FROM %s WHERE id = $1`, table)
	err := r.db.QueryRowContext(ctx, query, target.ResourceID).Scan(&generation)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return generation == target.Generation, nil
}

func mustJSON(value any) []byte {
	data, _ := json.Marshal(value)
	return data
}
