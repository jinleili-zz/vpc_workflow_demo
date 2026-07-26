package operation

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

const operationColumns = `
    operation_id, root_operation_id, parent_operation_id,
    owner_service, caller_scope, route_scope, operation_type, target_scope,
    idempotency_key, request_hash_version, request_hash, request_payload::text,
    resource_type, resource_id, generation, status,
    response_code, response_payload::text, error_code, error_message,
    created_at, updated_at, completed_at, version`

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) Begin(ctx context.Context, cmd BeginCommand) (*Operation, Decision, error) {
	return r.begin(ctx, r.db, cmd)
}

func (r *Repository) BeginTx(ctx context.Context, tx *sql.Tx, cmd BeginCommand) (*Operation, Decision, error) {
	if tx == nil {
		return nil, "", fmt.Errorf("operation transaction is required")
	}
	return r.begin(ctx, tx, cmd)
}

func (r *Repository) BeginTarget(ctx context.Context, cmd BeginCommand) (*Operation, Decision, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", fmt.Errorf("begin target operation transaction: %w", err)
	}
	defer tx.Rollback()
	op, decision, err := r.BeginTargetTx(ctx, tx, cmd)
	if err != nil {
		return nil, "", err
	}
	if err := tx.Commit(); err != nil {
		return nil, "", fmt.Errorf("commit target operation: %w", err)
	}
	return op, decision, nil
}

func (r *Repository) BeginTargetTx(ctx context.Context, tx *sql.Tx, cmd BeginCommand) (*Operation, Decision, error) {
	if tx == nil {
		return nil, "", fmt.Errorf("operation transaction is required")
	}
	lockKey := cmd.OwnerService + ":" + cmd.ResourceType + ":" + cmd.TargetScope
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return nil, "", fmt.Errorf("lock operation target: %w", err)
	}
	op, decision, err := r.begin(ctx, tx, cmd)
	if err != nil {
		return nil, "", err
	}
	if decision != DecisionNew {
		return op, decision, nil
	}

	var claimOperationID, claimResourceID, claimHash string
	var claimGeneration int64
	var claimActive, claimRetiring bool
	err = tx.QueryRowContext(ctx, `
		SELECT operation_id, resource_id, request_hash, generation, active, retiring
		FROM orchestration_target_claims
		WHERE owner_service = $1 AND resource_type = $2 AND target_scope = $3
		FOR UPDATE
	`, cmd.OwnerService, cmd.ResourceType, cmd.TargetScope).Scan(&claimOperationID, &claimResourceID, &claimHash, &claimGeneration, &claimActive, &claimRetiring)
	if err == sql.ErrNoRows {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO orchestration_target_claims (
				owner_service, resource_type, target_scope, request_hash,
				operation_id, resource_id, generation
			) VALUES ($1, $2, $3, $4, $5, $6, $7)
		`, cmd.OwnerService, cmd.ResourceType, cmd.TargetScope, cmd.RequestHash, op.OperationID, op.ResourceID, op.Generation); err != nil {
			return nil, "", fmt.Errorf("claim operation target: %w", err)
		}
		return op, DecisionNew, nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("load operation target claim: %w", err)
	}
	if !claimActive {
		generation := claimGeneration + 1
		if _, err := tx.ExecContext(ctx, `
			UPDATE orchestration_operations
			SET generation = $1, updated_at = NOW(), version = version + 1
			WHERE operation_id = $2
		`, generation, op.OperationID); err != nil {
			return nil, "", fmt.Errorf("advance operation generation: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE orchestration_target_claims
			SET request_hash = $1, operation_id = $2, resource_id = $3,
			    generation = $4, active = TRUE, retiring = FALSE, updated_at = NOW()
			WHERE owner_service = $5 AND resource_type = $6 AND target_scope = $7
		`, cmd.RequestHash, op.OperationID, op.ResourceID, generation, cmd.OwnerService, cmd.ResourceType, cmd.TargetScope); err != nil {
			return nil, "", fmt.Errorf("reactivate operation target: %w", err)
		}
		op.Generation = generation
		op.Version++
		return op, DecisionNew, nil
	}
	if claimRetiring {
		if _, err := tx.ExecContext(ctx, `
			UPDATE orchestration_operations
			SET resource_id = $1, generation = $2, status = $3,
			    error_code = $4, error_message = $5,
			    completed_at = NOW(), updated_at = NOW(), version = version + 1
			WHERE operation_id = $6
		`, claimResourceID, claimGeneration, StatusFailed, ErrResourceOperationInProgress.Error(), "target resource deletion is in progress", op.OperationID); err != nil {
			return nil, "", fmt.Errorf("record resource deletion conflict: %w", err)
		}
		op.ResourceID = claimResourceID
		op.Generation = claimGeneration
		op.Status = StatusFailed
		op.ErrorCode = ErrResourceOperationInProgress.Error()
		op.ErrorMessage = "target resource deletion is in progress"
		op.Version++
		return op, DecisionResourceBusy, nil
	}

	if claimHash == cmd.RequestHash {
		existing, err := getByID(ctx, tx, claimOperationID)
		if err != nil {
			return nil, "", fmt.Errorf("load target operation: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO orchestration_idempotency_aliases (
				owner_service, caller_scope, route_scope, idempotency_key,
				request_hash_version, request_hash, operation_id
			) VALUES ($1, $2, $3, $4, $5, $6, $7)
		`, cmd.OwnerService, cmd.CallerScope, cmd.RouteScope, cmd.IdempotencyKey, cmd.RequestHashVersion, cmd.RequestHash, existing.OperationID); err != nil {
			return nil, "", fmt.Errorf("alias idempotency key to target operation: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM orchestration_operations WHERE operation_id = $1`, op.OperationID); err != nil {
			return nil, "", fmt.Errorf("remove redundant target operation: %w", err)
		}
		return existing, DecisionReplay, nil
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE orchestration_operations
		SET resource_id = $1, generation = $2, status = $3,
		    error_code = $4, error_message = $5,
		    completed_at = NOW(), updated_at = NOW(), version = version + 1
		WHERE operation_id = $6
	`, claimResourceID, claimGeneration, StatusFailed, ErrResourceSpecConflict.Error(), "target resource already exists with a different specification", op.OperationID); err != nil {
		return nil, "", fmt.Errorf("record resource specification conflict: %w", err)
	}
	op.ResourceID = claimResourceID
	op.Generation = claimGeneration
	op.Status = StatusFailed
	op.ErrorCode = ErrResourceSpecConflict.Error()
	op.ErrorMessage = "target resource already exists with a different specification"
	op.Version++
	return op, DecisionResourceConflict, nil
}

type operationQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (r *Repository) begin(ctx context.Context, queryer operationQueryer, cmd BeginCommand) (*Operation, Decision, error) {
	if err := validateBeginCommand(cmd); err != nil {
		return nil, "", err
	}
	var aliasOperationID, aliasHash string
	var aliasHashVersion int16
	aliasErr := queryer.QueryRowContext(ctx, `
		SELECT operation_id, request_hash_version, request_hash
		FROM orchestration_idempotency_aliases
		WHERE owner_service = $1 AND caller_scope = $2 AND route_scope = $3 AND idempotency_key = $4
	`, cmd.OwnerService, cmd.CallerScope, cmd.RouteScope, cmd.IdempotencyKey).Scan(&aliasOperationID, &aliasHashVersion, &aliasHash)
	if aliasErr == nil {
		op, err := getByID(ctx, queryer, aliasOperationID)
		if err != nil {
			return nil, "", fmt.Errorf("load aliased operation: %w", err)
		}
		if aliasHashVersion != cmd.RequestHashVersion || aliasHash != cmd.RequestHash {
			return op, DecisionConflict, nil
		}
		if op.ErrorCode == ErrResourceSpecConflict.Error() {
			return op, DecisionResourceConflict, nil
		}
		return op, DecisionReplay, nil
	}
	if aliasErr != sql.ErrNoRows {
		return nil, "", fmt.Errorf("load idempotency alias: %w", aliasErr)
	}
	operationID := cmd.OperationID
	if operationID == "" {
		operationID = uuid.NewString()
	}
	rootOperationID := cmd.RootOperationID
	if rootOperationID == "" {
		rootOperationID = operationID
	}
	generation := cmd.Generation
	if generation == 0 {
		generation = 1
	}

	query := `
		INSERT INTO orchestration_operations (
			operation_id, root_operation_id, parent_operation_id,
			owner_service, caller_scope, route_scope, operation_type, target_scope,
			idempotency_key, request_hash_version, request_hash, request_payload,
			resource_type, resource_id, generation, status
		) VALUES (
			$1, $2, $3,
			$4, $5, $6, $7, $8,
			$9, $10, $11, $12::jsonb,
			$13, $14, $15, $16
		)
		ON CONFLICT (owner_service, caller_scope, route_scope, idempotency_key)
		DO NOTHING
		RETURNING ` + operationColumns

	op, err := scanOperation(queryer.QueryRowContext(ctx, query,
		operationID, rootOperationID, nullString(cmd.ParentOperationID),
		cmd.OwnerService, cmd.CallerScope, cmd.RouteScope, cmd.OperationType, cmd.TargetScope,
		cmd.IdempotencyKey, cmd.RequestHashVersion, cmd.RequestHash, cmd.RequestPayload,
		cmd.ResourceType, nullString(cmd.ResourceID), generation, StatusAccepted,
	))
	if err == nil {
		return op, DecisionNew, nil
	}
	if err != sql.ErrNoRows {
		return nil, "", fmt.Errorf("insert operation: %w", err)
	}

	op, err = getByIdempotency(ctx, queryer, cmd.OwnerService, cmd.CallerScope, cmd.RouteScope, cmd.IdempotencyKey)
	if err != nil {
		return nil, "", fmt.Errorf("load existing operation: %w", err)
	}
	if op.RequestHashVersion != cmd.RequestHashVersion || op.RequestHash != cmd.RequestHash {
		return op, DecisionConflict, nil
	}
	if op.ErrorCode == ErrResourceSpecConflict.Error() {
		return op, DecisionResourceConflict, nil
	}
	if op.ErrorCode == ErrResourceOperationInProgress.Error() {
		return op, DecisionResourceBusy, nil
	}
	return op, DecisionReplay, nil
}

func (r *Repository) Get(ctx context.Context, operationID string) (*Operation, error) {
	return getByID(ctx, r.db, operationID)
}

func (r *Repository) ListRecoverableDispatch(ctx context.Context, ownerService string, limit int) ([]*Operation, error) {
	if ownerService == "" || limit <= 0 {
		return nil, fmt.Errorf("owner service and positive limit are required")
	}
	query := `SELECT ` + operationColumns + `
		FROM orchestration_operations
		WHERE owner_service = $1
		  AND response_payload IS NULL
		  AND (status = $2 OR (status = $3 AND (lease_until IS NULL OR lease_until < NOW())))
		ORDER BY updated_at, operation_id
		LIMIT $4`
	return scanOperations(r.db.QueryContext(ctx, query, ownerService, StatusAccepted, StatusDispatching, limit))
}

func (r *Repository) ListByStatus(ctx context.Context, ownerService string, status Status, limit int) ([]*Operation, error) {
	if ownerService == "" || !status.Valid() || limit <= 0 {
		return nil, fmt.Errorf("owner service, valid status and positive limit are required")
	}
	query := `SELECT ` + operationColumns + `
		FROM orchestration_operations
		WHERE owner_service = $1 AND status = $2
		ORDER BY updated_at, operation_id
		LIMIT $3`
	return scanOperations(r.db.QueryContext(ctx, query, ownerService, status, limit))
}

func getByID(ctx context.Context, queryer operationQueryer, operationID string) (*Operation, error) {
	query := `SELECT ` + operationColumns + ` FROM orchestration_operations WHERE operation_id = $1`
	return scanOperation(queryer.QueryRowContext(ctx, query, operationID))
}

func (r *Repository) AcquireDispatch(ctx context.Context, operationID, leaseOwner string, leaseDuration time.Duration) (bool, error) {
	if leaseOwner == "" || leaseDuration <= 0 {
		return false, fmt.Errorf("dispatch lease owner and positive duration are required")
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE orchestration_operations
		SET status = $1, lease_owner = $2, lease_until = NOW() + ($3 * interval '1 millisecond'),
		    version = version + 1, updated_at = NOW()
		WHERE operation_id = $4
		  AND response_payload IS NULL
		  AND (status = $5 OR (status = $1 AND (lease_until IS NULL OR lease_until < NOW())))
	`, StatusDispatching, leaseOwner, leaseDuration.Milliseconds(), operationID, StatusAccepted)
	if err != nil {
		return false, fmt.Errorf("claim operation dispatch: %w", err)
	}
	return changedOne(result)
}

func (r *Repository) RenewDispatch(ctx context.Context, operationID, leaseOwner string, leaseDuration time.Duration) (bool, error) {
	if leaseOwner == "" || leaseDuration <= 0 {
		return false, fmt.Errorf("dispatch lease owner and positive duration are required")
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE orchestration_operations
		SET lease_until = NOW() + ($1 * interval '1 millisecond'), updated_at = NOW()
		WHERE operation_id = $2 AND status = $3 AND response_payload IS NULL AND lease_owner = $4
	`, leaseDuration.Milliseconds(), operationID, StatusDispatching, leaseOwner)
	if err != nil {
		return false, fmt.Errorf("renew operation dispatch: %w", err)
	}
	return changedOne(result)
}

func (r *Repository) ReleaseDispatch(ctx context.Context, operationID, leaseOwner string) (bool, error) {
	result, err := r.db.ExecContext(ctx, `
		UPDATE orchestration_operations
		SET lease_until = NOW(), updated_at = NOW()
		WHERE operation_id = $1 AND status = $2 AND response_payload IS NULL AND lease_owner = $3
	`, operationID, StatusDispatching, leaseOwner)
	if err != nil {
		return false, fmt.Errorf("release operation dispatch: %w", err)
	}
	return changedOne(result)
}

func (r *Repository) DeferDispatch(ctx context.Context, operationID string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE orchestration_operations
		SET updated_at = NOW()
		WHERE operation_id = $1 AND response_payload IS NULL
		  AND (status = $2 OR (status = $3 AND (lease_until IS NULL OR lease_until < NOW())))
	`, operationID, StatusAccepted, StatusDispatching)
	if err != nil {
		return fmt.Errorf("defer operation dispatch: %w", err)
	}
	return nil
}

func (r *Repository) DeferStatus(ctx context.Context, operationID string, status Status) error {
	if !status.Valid() || status.Terminal() {
		return fmt.Errorf("a valid non-terminal status is required")
	}
	_, err := r.db.ExecContext(ctx, `
		UPDATE orchestration_operations SET updated_at = NOW()
		WHERE operation_id = $1 AND status = $2
	`, operationID, status)
	if err != nil {
		return fmt.Errorf("defer operation status reconciliation: %w", err)
	}
	return nil
}

func (r *Repository) ReleaseTarget(ctx context.Context, ownerService, resourceType, targetScope string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := r.ReleaseTargetTx(ctx, tx, ownerService, resourceType, targetScope); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) ReleaseTargetTx(ctx context.Context, tx *sql.Tx, ownerService, resourceType, targetScope string) error {
	if tx == nil {
		return fmt.Errorf("operation transaction is required")
	}
	claimFound, err := assertTargetReleasable(ctx, tx, ownerService, resourceType, targetScope, true)
	if err != nil {
		return err
	}
	if !claimFound {
		return nil
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE orchestration_target_claims
		SET active = FALSE, retiring = FALSE, updated_at = NOW()
		WHERE owner_service = $1 AND resource_type = $2 AND target_scope = $3 AND active = TRUE
	`, ownerService, resourceType, targetScope)
	if err != nil {
		return fmt.Errorf("release operation target: %w", err)
	}
	return nil
}

func (r *Repository) MarkTargetRetiring(ctx context.Context, ownerService, resourceType, targetScope string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := r.MarkTargetRetiringTx(ctx, tx, ownerService, resourceType, targetScope); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) MarkTargetRetiringTx(ctx context.Context, tx *sql.Tx, ownerService, resourceType, targetScope string) error {
	if tx == nil {
		return fmt.Errorf("operation transaction is required")
	}
	var operationID string
	var status Status
	err := tx.QueryRowContext(ctx, `
		SELECT operation.operation_id, operation.status
		FROM orchestration_target_claims claim
		JOIN orchestration_operations operation ON operation.operation_id = claim.operation_id
		WHERE claim.owner_service = $1 AND claim.resource_type = $2
		  AND claim.target_scope = $3 AND claim.active = TRUE
		FOR UPDATE OF claim, operation
	`, ownerService, resourceType, targetScope).Scan(&operationID, &status)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if !status.Terminal() {
		result, err := tx.ExecContext(ctx, `
			UPDATE orchestration_operations
			SET status = $1, error_code = 'DELETE_WON_RACE',
			    error_message = 'resource deletion superseded the create operation',
			    completed_at = NOW(), lease_until = NULL,
			    version = version + 1, updated_at = NOW()
			WHERE operation_id = $2
			  AND status IN ($3,$4,$5,$6)
		`, StatusCancelled, operationID, StatusAccepted, StatusDispatching, StatusRunning, StatusCompensating)
		if err != nil {
			return err
		}
		if rows, _ := result.RowsAffected(); rows != 1 {
			return fmt.Errorf("cancel target create operation: %s", operationID)
		}
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE orchestration_target_claims SET retiring = TRUE, updated_at = NOW()
		WHERE owner_service = $1 AND resource_type = $2 AND target_scope = $3 AND active = TRUE
	`, ownerService, resourceType, targetScope)
	return err
}

func (r *Repository) AssertTargetReleasable(ctx context.Context, ownerService, resourceType, targetScope string) error {
	_, err := assertTargetReleasable(ctx, r.db, ownerService, resourceType, targetScope, false)
	return err
}

type targetStatusQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func assertTargetReleasable(ctx context.Context, queryer targetStatusQueryer, ownerService, resourceType, targetScope string, lock bool) (bool, error) {
	query := `
		SELECT operation.status
		FROM orchestration_target_claims claim
		JOIN orchestration_operations operation ON operation.operation_id = claim.operation_id
		WHERE claim.owner_service = $1 AND claim.resource_type = $2
		  AND claim.target_scope = $3 AND claim.active = TRUE
	`
	if lock {
		query += ` FOR UPDATE OF claim, operation`
	}
	var status Status
	err := queryer.QueryRowContext(ctx, query, ownerService, resourceType, targetScope).Scan(&status)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load operation target for release: %w", err)
	}
	if !status.Terminal() {
		return true, fmt.Errorf("%w: target create operation is %s", ErrResourceOperationInProgress, status)
	}
	return true, nil
}

func (r *Repository) UpdateStatusCAS(ctx context.Context, operationID string, expectedVersion int64, operationType string, expected, next Status, errorCode, errorMessage string) (bool, error) {
	if !CanTransition(operationType, expected, next) {
		return false, fmt.Errorf("invalid operation status transition: %s -> %s", expected, next)
	}
	query := `
		UPDATE orchestration_operations
		SET status = $1,
		    error_code = $2,
		    error_message = $3,
		    completed_at = CASE WHEN $4 THEN NOW() ELSE completed_at END,
		    lease_owner = NULL,
		    lease_until = NULL,
		    version = version + 1,
		    updated_at = NOW()
		WHERE operation_id = $5
		  AND version = $6
		  AND status = $7
		  AND operation_type = $8
	`
	result, err := r.db.ExecContext(ctx, query,
		next, nullString(errorCode), nullString(errorMessage), next.Terminal(),
		operationID, expectedVersion, expected, operationType,
	)
	if err != nil {
		return false, err
	}
	return changedOne(result)
}

func (r *Repository) StoreResponseCAS(ctx context.Context, operationID string, expectedVersion int64, operationType string, expected, next Status, responseCode string, payload json.RawMessage) (bool, error) {
	return r.storeResponseCAS(ctx, operationID, expectedVersion, operationType, expected, next, responseCode, payload, false)
}

func (r *Repository) storeResponseCAS(ctx context.Context, operationID string, expectedVersion int64, operationType string, expected, next Status, responseCode string, payload json.RawMessage, releaseTarget bool) (bool, error) {
	if !validOperationType(operationType) {
		return false, fmt.Errorf("unsupported operation type: %s", operationType)
	}
	if expected != next && !CanTransition(operationType, expected, next) {
		return false, fmt.Errorf("invalid operation response status transition: %s -> %s", expected, next)
	}
	if !expected.Valid() || !next.Valid() || expected.Terminal() {
		return false, fmt.Errorf("cannot store response for operation status: %s", expected)
	}
	query := `
		UPDATE orchestration_operations
		SET status = $1,
		    response_code = $2,
		    response_payload = $3::jsonb,
		    completed_at = CASE WHEN $4 THEN NOW() ELSE completed_at END,
		    lease_owner = NULL,
		    lease_until = NULL,
		    version = version + 1,
		    updated_at = NOW()
		WHERE operation_id = $5
		  AND version = $6
		  AND status = $7
		  AND operation_type = $8
	`
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, query,
		next, nullString(responseCode), nullableJSON(payload), next.Terminal(), operationID, expectedVersion, expected, operationType,
	)
	if err != nil {
		return false, err
	}
	changed, err := changedOne(result)
	if err != nil || !changed {
		return changed, err
	}
	if releaseTarget {
		if err := releaseTargetClaimTx(ctx, tx, operationID); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (r *Repository) StoreResponseLease(ctx context.Context, operationID, leaseOwner, operationType string, next Status, responseCode string, payload json.RawMessage) (bool, error) {
	return r.storeResponseLease(ctx, operationID, leaseOwner, operationType, next, responseCode, payload, false)
}

func (r *Repository) storeResponseLease(ctx context.Context, operationID, leaseOwner, operationType string, next Status, responseCode string, payload json.RawMessage, releaseTarget bool) (bool, error) {
	if !CanTransition(operationType, StatusDispatching, next) {
		return false, fmt.Errorf("invalid leased response status transition: %s -> %s", StatusDispatching, next)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE orchestration_operations
		SET status = $1, response_code = $2, response_payload = $3::jsonb,
		    completed_at = CASE WHEN $4 THEN NOW() ELSE completed_at END,
		    lease_owner = NULL, lease_until = NULL, version = version + 1, updated_at = NOW()
		WHERE operation_id = $5 AND operation_type = $6 AND status = $7
		  AND response_payload IS NULL AND lease_owner = $8 AND lease_until > NOW()
	`, next, nullString(responseCode), nullableJSON(payload), next.Terminal(), operationID, operationType, StatusDispatching, leaseOwner)
	if err != nil {
		return false, err
	}
	changed, err := changedOne(result)
	if err != nil || !changed {
		return changed, err
	}
	if releaseTarget {
		if err := releaseTargetClaimTx(ctx, tx, operationID); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func releaseTargetClaimTx(ctx context.Context, tx *sql.Tx, operationID string) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE orchestration_target_claims
		SET active = FALSE, retiring = FALSE, updated_at = NOW()
		WHERE operation_id = $1 AND active = TRUE
	`, operationID)
	return err
}

func (r *Repository) getByIdempotency(ctx context.Context, owner, caller, route, key string) (*Operation, error) {
	return getByIdempotency(ctx, r.db, owner, caller, route, key)
}

func getByIdempotency(ctx context.Context, queryer operationQueryer, owner, caller, route, key string) (*Operation, error) {
	query := `SELECT ` + operationColumns + `
		FROM orchestration_operations
		WHERE owner_service = $1 AND caller_scope = $2 AND route_scope = $3 AND idempotency_key = $4`
	return scanOperation(queryer.QueryRowContext(ctx, query, owner, caller, route, key))
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanOperations(rows *sql.Rows, err error) ([]*Operation, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var operations []*Operation
	for rows.Next() {
		op, err := scanOperation(rows)
		if err != nil {
			return nil, err
		}
		operations = append(operations, op)
	}
	return operations, rows.Err()
}

func scanOperation(row rowScanner) (*Operation, error) {
	op := &Operation{}
	var parentID, resourceID, responseCode, responsePayload, errorCode, errorMessage sql.NullString
	var requestPayload string
	var completedAt sql.NullTime
	err := row.Scan(
		&op.OperationID, &op.RootOperationID, &parentID,
		&op.OwnerService, &op.CallerScope, &op.RouteScope, &op.OperationType, &op.TargetScope,
		&op.IdempotencyKey, &op.RequestHashVersion, &op.RequestHash, &requestPayload,
		&op.ResourceType, &resourceID, &op.Generation, &op.Status,
		&responseCode, &responsePayload, &errorCode, &errorMessage,
		&op.CreatedAt, &op.UpdatedAt, &completedAt, &op.Version,
	)
	if err != nil {
		return nil, err
	}
	op.ParentOperationID = parentID.String
	op.ResourceID = resourceID.String
	op.RequestPayload = json.RawMessage(requestPayload)
	op.ResponseCode = responseCode.String
	if responsePayload.Valid {
		op.ResponsePayload = json.RawMessage(responsePayload.String)
	}
	op.ErrorCode = errorCode.String
	op.ErrorMessage = errorMessage.String
	if completedAt.Valid {
		completed := completedAt.Time
		op.CompletedAt = &completed
	}
	return op, nil
}

func validateBeginCommand(cmd BeginCommand) error {
	switch {
	case cmd.OwnerService == "":
		return fmt.Errorf("owner service is required")
	case cmd.CallerScope == "":
		return fmt.Errorf("caller scope is required")
	case cmd.RouteScope == "":
		return fmt.Errorf("route scope is required")
	case cmd.OperationType == "":
		return fmt.Errorf("operation type is required")
	case !validOperationType(cmd.OperationType):
		return fmt.Errorf("unsupported operation type: %s", cmd.OperationType)
	case cmd.TargetScope == "":
		return fmt.Errorf("target scope is required")
	case cmd.IdempotencyKey == "":
		return fmt.Errorf("idempotency key is required")
	case len(cmd.IdempotencyKey) > 256:
		return fmt.Errorf("idempotency key exceeds 256 characters")
	case cmd.RequestHashVersion <= 0:
		return fmt.Errorf("request hash version is required")
	case len(cmd.RequestHash) != 64:
		return fmt.Errorf("request hash must be 64 hexadecimal characters")
	case !validRequestHash(cmd.RequestHash):
		return fmt.Errorf("request hash must be 64 hexadecimal characters")
	case len(cmd.RequestPayload) == 0:
		return fmt.Errorf("request payload is required")
	case cmd.ResourceType == "":
		return fmt.Errorf("resource type is required")
	case cmd.Generation < 0:
		return fmt.Errorf("generation must be positive")
	}
	return nil
}

func validOperationType(operationType string) bool {
	_, valid := operationStateMachine(operationType)
	return valid
}

func validRequestHash(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256Size
}

const sha256Size = 32

func changedOne(result sql.Result) (bool, error) {
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows == 1, nil
}

func nullableJSON(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return string(value)
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
