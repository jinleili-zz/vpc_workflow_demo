package operation

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"

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

type operationQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (r *Repository) begin(ctx context.Context, queryer operationQueryer, cmd BeginCommand) (*Operation, Decision, error) {
	if err := validateBeginCommand(cmd); err != nil {
		return nil, "", err
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
	return op, DecisionReplay, nil
}

func (r *Repository) Get(ctx context.Context, operationID string) (*Operation, error) {
	query := `SELECT ` + operationColumns + ` FROM orchestration_operations WHERE operation_id = $1`
	return scanOperation(r.db.QueryRowContext(ctx, query, operationID))
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
		    version = version + 1,
		    updated_at = NOW()
		WHERE operation_id = $5
		  AND version = $6
		  AND status = $7
		  AND operation_type = $8
	`
	result, err := r.db.ExecContext(ctx, query,
		next, nullString(responseCode), nullableJSON(payload), next.Terminal(), operationID, expectedVersion, expected, operationType,
	)
	if err != nil {
		return false, err
	}
	return changedOne(result)
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
