// File: store.go
// Package saga - Database layer for SAGA transactions

package saga

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lib/pq"
)

// Store defines the interface for SAGA transaction persistence.
type Store interface {
	// Transaction operations
	CreateTransaction(ctx context.Context, tx *Transaction) error
	CreateTransactionWithSteps(ctx context.Context, tx *Transaction, steps []*Step) error
	GetTransaction(ctx context.Context, id string) (*Transaction, error)
	UpdateTransactionStatus(ctx context.Context, id string, status TxStatus, lastError string) error
	UpdateTransactionStep(ctx context.Context, id string, currentStep int) error

	// Step operations
	CreateSteps(ctx context.Context, steps []*Step) error
	GetSteps(ctx context.Context, txID string) ([]*Step, error)
	GetStep(ctx context.Context, stepID string) (*Step, error)
	UpdateStepStatus(ctx context.Context, stepID string, status StepStatus, lastError string) error
	UpdateStepResponse(ctx context.Context, stepID string, response map[string]any) error
	IncrementStepRetry(ctx context.Context, stepID string) error
	IncrementStepPollCount(ctx context.Context, stepID string, nextPollAt time.Time) error

	// Poll task operations
	CreatePollTask(ctx context.Context, task *PollTask) error
	DeletePollTask(ctx context.Context, stepID string) error

	// Recovery operations (multi-instance safe with FOR UPDATE SKIP LOCKED)
	ListRecoverableTransactions(ctx context.Context, instanceID string, batchSize int, leaseDuration time.Duration) ([]*Transaction, error)
	ListTimedOutTransactions(ctx context.Context, instanceID string, leaseDuration time.Duration) ([]*Transaction, error)

	// Distributed poll task operations
	AcquirePollTasks(ctx context.Context, instanceID string, batchSize int) ([]*PollTask, error)
	ReleasePollTask(ctx context.Context, stepID string) error

	// Distributed coordination operations
	ClaimTransaction(ctx context.Context, txID string, instanceID string, leaseDuration time.Duration) (bool, error)
	ReleaseTransaction(ctx context.Context, txID string, instanceID string) error
	UpdateTransactionStatusCAS(ctx context.Context, txID string, expectedStatus TxStatus, newStatus TxStatus, lastError string) (bool, error)
}

// PostgresStore implements Store interface using PostgreSQL.
type PostgresStore struct {
	db *sql.DB
}

// NewPostgresStore creates a new PostgresStore with the given database connection.
func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

// CreateTransaction creates a new SAGA transaction record.
func (s *PostgresStore) CreateTransaction(ctx context.Context, tx *Transaction) error {
	payloadJSON, err := json.Marshal(tx.Payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	query := `
		INSERT INTO saga_transactions (id, status, payload, current_step, created_at, updated_at, timeout_at, retry_count, last_error)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`
	_, err = s.db.ExecContext(ctx, query,
		tx.ID,
		string(tx.Status),
		payloadJSON,
		tx.CurrentStep,
		tx.CreatedAt,
		tx.UpdatedAt,
		tx.TimeoutAt,
		tx.RetryCount,
		tx.LastError,
	)
	if err != nil {
		return fmt.Errorf("failed to create transaction: %w", err)
	}
	return nil
}

// CreateTransactionWithSteps creates a transaction and its steps atomically in a single database transaction.
// This ensures no orphan transactions exist if step creation fails.
func (s *PostgresStore) CreateTransactionWithSteps(ctx context.Context, tx *Transaction, steps []*Step) error {
	dbTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer dbTx.Rollback()

	// Create transaction
	payloadJSON, err := json.Marshal(tx.Payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	txQuery := `
		INSERT INTO saga_transactions (id, status, payload, current_step, created_at, updated_at, timeout_at, retry_count, last_error)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`
	_, err = dbTx.ExecContext(ctx, txQuery,
		tx.ID,
		string(tx.Status),
		payloadJSON,
		tx.CurrentStep,
		tx.CreatedAt,
		tx.UpdatedAt,
		tx.TimeoutAt,
		tx.RetryCount,
		tx.LastError,
	)
	if err != nil {
		return fmt.Errorf("failed to create transaction: %w", err)
	}

	// Create steps
	if len(steps) > 0 {
		stepQuery := `
			INSERT INTO saga_steps (
				id, transaction_id, step_index, name, step_type, status,
				action_method, action_url, action_payload, auth_ak,
				compensate_method, compensate_url, compensate_payload,
				poll_url, poll_method, poll_interval_sec, poll_max_times,
				poll_success_path, poll_success_value, poll_failure_path, poll_failure_value,
				max_retry
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22)
		`

		stmt, err := dbTx.PrepareContext(ctx, stepQuery)
		if err != nil {
			return fmt.Errorf("failed to prepare statement: %w", err)
		}
		defer stmt.Close()

		for _, step := range steps {
			actionPayloadJSON, err := json.Marshal(step.ActionPayload)
			if err != nil {
				return fmt.Errorf("failed to marshal action payload: %w", err)
			}

			compensatePayloadJSON, err := json.Marshal(step.CompensatePayload)
			if err != nil {
				return fmt.Errorf("failed to marshal compensate payload: %w", err)
			}

			_, err = stmt.ExecContext(ctx,
				step.ID,
				step.TransactionID,
				step.Index,
				step.Name,
				string(step.Type),
				string(step.Status),
				step.ActionMethod,
				step.ActionURL,
				actionPayloadJSON,
				step.AuthAK,
				step.CompensateMethod,
				step.CompensateURL,
				compensatePayloadJSON,
				step.PollURL,
				step.PollMethod,
				step.PollIntervalSec,
				step.PollMaxTimes,
				step.PollSuccessPath,
				step.PollSuccessValue,
				step.PollFailurePath,
				step.PollFailureValue,
				step.MaxRetry,
			)
			if err != nil {
				return fmt.Errorf("failed to create step %s: %w", step.ID, err)
			}
		}
	}

	if err := dbTx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

// GetTransaction retrieves a transaction by ID.
func (s *PostgresStore) GetTransaction(ctx context.Context, id string) (*Transaction, error) {
	query := `
		SELECT id, status, payload, current_step, created_at, updated_at, finished_at, timeout_at, retry_count, last_error
		FROM saga_transactions
		WHERE id = $1
	`
	row := s.db.QueryRowContext(ctx, query, id)

	var tx Transaction
	var payloadJSON []byte
	var status string
	var lastError sql.NullString
	err := row.Scan(
		&tx.ID,
		&status,
		&payloadJSON,
		&tx.CurrentStep,
		&tx.CreatedAt,
		&tx.UpdatedAt,
		&tx.FinishedAt,
		&tx.TimeoutAt,
		&tx.RetryCount,
		&lastError,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get transaction: %w", err)
	}

	tx.Status = TxStatus(status)
	tx.LastError = lastError.String
	if len(payloadJSON) > 0 {
		if err := json.Unmarshal(payloadJSON, &tx.Payload); err != nil {
			return nil, fmt.Errorf("failed to unmarshal payload: %w", err)
		}
	}

	return &tx, nil
}

// UpdateTransactionStatus updates the status and error message of a transaction.
func (s *PostgresStore) UpdateTransactionStatus(ctx context.Context, id string, status TxStatus, lastError string) error {
	var finishedAt interface{}
	if status == TxStatusSucceeded || status == TxStatusFailed {
		now := time.Now()
		finishedAt = now
	}

	query := `
		UPDATE saga_transactions
		SET status = $2, last_error = $3, updated_at = NOW(), finished_at = $4
		WHERE id = $1
	`
	result, err := s.db.ExecContext(ctx, query, id, string(status), lastError, finishedAt)
	if err != nil {
		return fmt.Errorf("failed to update transaction status: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("transaction not found: %s", id)
	}

	return nil
}

// UpdateTransactionStep updates the current step index of a transaction.
func (s *PostgresStore) UpdateTransactionStep(ctx context.Context, id string, currentStep int) error {
	query := `
		UPDATE saga_transactions
		SET current_step = $2, updated_at = NOW()
		WHERE id = $1
	`
	result, err := s.db.ExecContext(ctx, query, id, currentStep)
	if err != nil {
		return fmt.Errorf("failed to update transaction step: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("transaction not found: %s", id)
	}

	return nil
}

// CreateSteps creates multiple steps for a transaction.
func (s *PostgresStore) CreateSteps(ctx context.Context, steps []*Step) error {
	if len(steps) == 0 {
		return nil
	}

	dbTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer dbTx.Rollback()

	query := `
			INSERT INTO saga_steps (
				id, transaction_id, step_index, name, step_type, status,
				action_method, action_url, action_payload, auth_ak,
				compensate_method, compensate_url, compensate_payload,
				poll_url, poll_method, poll_interval_sec, poll_max_times,
				poll_success_path, poll_success_value, poll_failure_path, poll_failure_value,
				max_retry
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22)
	`

	stmt, err := dbTx.PrepareContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	for _, step := range steps {
		actionPayloadJSON, err := json.Marshal(step.ActionPayload)
		if err != nil {
			return fmt.Errorf("failed to marshal action payload: %w", err)
		}

		compensatePayloadJSON, err := json.Marshal(step.CompensatePayload)
		if err != nil {
			return fmt.Errorf("failed to marshal compensate payload: %w", err)
		}

		_, err = stmt.ExecContext(ctx,
			step.ID,
			step.TransactionID,
			step.Index,
			step.Name,
			string(step.Type),
			string(step.Status),
			step.ActionMethod,
			step.ActionURL,
			actionPayloadJSON,
			step.AuthAK,
			step.CompensateMethod,
			step.CompensateURL,
			compensatePayloadJSON,
			step.PollURL,
			step.PollMethod,
			step.PollIntervalSec,
			step.PollMaxTimes,
			step.PollSuccessPath,
			step.PollSuccessValue,
			step.PollFailurePath,
			step.PollFailureValue,
			step.MaxRetry,
		)
		if err != nil {
			return fmt.Errorf("failed to create step %s: %w", step.ID, err)
		}
	}

	if err := dbTx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

// GetSteps retrieves all steps for a transaction, ordered by step_index.
func (s *PostgresStore) GetSteps(ctx context.Context, txID string) ([]*Step, error) {
	query := `
		SELECT id, transaction_id, step_index, name, step_type, status,
			action_method, action_url, action_payload, action_response, auth_ak,
			compensate_method, compensate_url, compensate_payload,
			poll_url, poll_method, poll_interval_sec, poll_max_times, poll_count,
			poll_success_path, poll_success_value, poll_failure_path, poll_failure_value,
			next_poll_at, retry_count, max_retry, last_error, started_at, finished_at
		FROM saga_steps
		WHERE transaction_id = $1
		ORDER BY step_index
	`

	rows, err := s.db.QueryContext(ctx, query, txID)
	if err != nil {
		return nil, fmt.Errorf("failed to query steps: %w", err)
	}
	defer rows.Close()

	var steps []*Step
	for rows.Next() {
		step, err := scanStep(rows)
		if err != nil {
			return nil, err
		}
		steps = append(steps, step)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating steps: %w", err)
	}

	return steps, nil
}

// GetStep retrieves a single step by ID.
func (s *PostgresStore) GetStep(ctx context.Context, stepID string) (*Step, error) {
	query := `
		SELECT id, transaction_id, step_index, name, step_type, status,
			action_method, action_url, action_payload, action_response, auth_ak,
			compensate_method, compensate_url, compensate_payload,
			poll_url, poll_method, poll_interval_sec, poll_max_times, poll_count,
			poll_success_path, poll_success_value, poll_failure_path, poll_failure_value,
			next_poll_at, retry_count, max_retry, last_error, started_at, finished_at
		FROM saga_steps
		WHERE id = $1
	`

	row := s.db.QueryRowContext(ctx, query, stepID)
	step, err := scanStepRow(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return step, nil
}

// scanStep scans a step from a sql.Rows.
func scanStep(rows *sql.Rows) (*Step, error) {
	var step Step
	var stepType, status string
	var actionPayloadJSON, actionResponseJSON, compensatePayloadJSON []byte
	var authAK, pollURL, pollMethod, pollSuccessPath, pollSuccessValue, pollFailurePath, pollFailureValue, lastError sql.NullString

	err := rows.Scan(
		&step.ID,
		&step.TransactionID,
		&step.Index,
		&step.Name,
		&stepType,
		&status,
		&step.ActionMethod,
		&step.ActionURL,
		&actionPayloadJSON,
		&actionResponseJSON,
		&authAK,
		&step.CompensateMethod,
		&step.CompensateURL,
		&compensatePayloadJSON,
		&pollURL,
		&pollMethod,
		&step.PollIntervalSec,
		&step.PollMaxTimes,
		&step.PollCount,
		&pollSuccessPath,
		&pollSuccessValue,
		&pollFailurePath,
		&pollFailureValue,
		&step.NextPollAt,
		&step.RetryCount,
		&step.MaxRetry,
		&lastError,
		&step.StartedAt,
		&step.FinishedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to scan step: %w", err)
	}

	step.Type = StepType(stepType)
	step.Status = StepStatus(status)
	step.AuthAK = authAK.String
	step.PollURL = pollURL.String
	step.PollMethod = pollMethod.String
	step.PollSuccessPath = pollSuccessPath.String
	step.PollSuccessValue = pollSuccessValue.String
	step.PollFailurePath = pollFailurePath.String
	step.PollFailureValue = pollFailureValue.String
	step.LastError = lastError.String

	if len(actionPayloadJSON) > 0 {
		if err := json.Unmarshal(actionPayloadJSON, &step.ActionPayload); err != nil {
			return nil, fmt.Errorf("failed to unmarshal action payload: %w", err)
		}
	}
	if len(actionResponseJSON) > 0 {
		if err := json.Unmarshal(actionResponseJSON, &step.ActionResponse); err != nil {
			return nil, fmt.Errorf("failed to unmarshal action response: %w", err)
		}
	}
	if len(compensatePayloadJSON) > 0 {
		if err := json.Unmarshal(compensatePayloadJSON, &step.CompensatePayload); err != nil {
			return nil, fmt.Errorf("failed to unmarshal compensate payload: %w", err)
		}
	}

	return &step, nil
}

// scanStepRow scans a step from a sql.Row.
func scanStepRow(row *sql.Row) (*Step, error) {
	var step Step
	var stepType, status string
	var actionPayloadJSON, actionResponseJSON, compensatePayloadJSON []byte
	var authAK, pollURL, pollMethod, pollSuccessPath, pollSuccessValue, pollFailurePath, pollFailureValue, lastError sql.NullString

	err := row.Scan(
		&step.ID,
		&step.TransactionID,
		&step.Index,
		&step.Name,
		&stepType,
		&status,
		&step.ActionMethod,
		&step.ActionURL,
		&actionPayloadJSON,
		&actionResponseJSON,
		&authAK,
		&step.CompensateMethod,
		&step.CompensateURL,
		&compensatePayloadJSON,
		&pollURL,
		&pollMethod,
		&step.PollIntervalSec,
		&step.PollMaxTimes,
		&step.PollCount,
		&pollSuccessPath,
		&pollSuccessValue,
		&pollFailurePath,
		&pollFailureValue,
		&step.NextPollAt,
		&step.RetryCount,
		&step.MaxRetry,
		&lastError,
		&step.StartedAt,
		&step.FinishedAt,
	)
	if err != nil {
		return nil, err
	}

	step.Type = StepType(stepType)
	step.Status = StepStatus(status)
	step.AuthAK = authAK.String
	step.PollURL = pollURL.String
	step.PollMethod = pollMethod.String
	step.PollSuccessPath = pollSuccessPath.String
	step.PollSuccessValue = pollSuccessValue.String
	step.PollFailurePath = pollFailurePath.String
	step.PollFailureValue = pollFailureValue.String
	step.LastError = lastError.String

	if len(actionPayloadJSON) > 0 {
		if err := json.Unmarshal(actionPayloadJSON, &step.ActionPayload); err != nil {
			return nil, fmt.Errorf("failed to unmarshal action payload: %w", err)
		}
	}
	if len(actionResponseJSON) > 0 {
		if err := json.Unmarshal(actionResponseJSON, &step.ActionResponse); err != nil {
			return nil, fmt.Errorf("failed to unmarshal action response: %w", err)
		}
	}
	if len(compensatePayloadJSON) > 0 {
		if err := json.Unmarshal(compensatePayloadJSON, &step.CompensatePayload); err != nil {
			return nil, fmt.Errorf("failed to unmarshal compensate payload: %w", err)
		}
	}

	return &step, nil
}

// UpdateStepStatus updates the status and error message of a step.
func (s *PostgresStore) UpdateStepStatus(ctx context.Context, stepID string, status StepStatus, lastError string) error {
	var finishedAt interface{}
	var startedAt interface{}

	if status == StepStatusRunning {
		now := time.Now()
		startedAt = now
	}
	if status == StepStatusSucceeded || status == StepStatusFailed || status == StepStatusCompensated {
		now := time.Now()
		finishedAt = now
	}

	query := `
		UPDATE saga_steps
		SET status = $2, last_error = $3, started_at = COALESCE($4, started_at), finished_at = $5
		WHERE id = $1
	`
	result, err := s.db.ExecContext(ctx, query, stepID, string(status), lastError, startedAt, finishedAt)
	if err != nil {
		return fmt.Errorf("failed to update step status: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("step not found: %s", stepID)
	}

	return nil
}

// UpdateStepResponse updates the action response of a step.
func (s *PostgresStore) UpdateStepResponse(ctx context.Context, stepID string, response map[string]any) error {
	responseJSON, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("failed to marshal response: %w", err)
	}

	query := `
		UPDATE saga_steps
		SET action_response = $2
		WHERE id = $1
	`
	result, err := s.db.ExecContext(ctx, query, stepID, responseJSON)
	if err != nil {
		return fmt.Errorf("failed to update step response: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("step not found: %s", stepID)
	}

	return nil
}

// IncrementStepRetry increments the retry count of a step.
func (s *PostgresStore) IncrementStepRetry(ctx context.Context, stepID string) error {
	query := `
		UPDATE saga_steps
		SET retry_count = retry_count + 1
		WHERE id = $1
	`
	result, err := s.db.ExecContext(ctx, query, stepID)
	if err != nil {
		return fmt.Errorf("failed to increment step retry: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("step not found: %s", stepID)
	}

	return nil
}

// IncrementStepPollCount increments the poll count and updates the next poll time.
func (s *PostgresStore) IncrementStepPollCount(ctx context.Context, stepID string, nextPollAt time.Time) error {
	query := `
		UPDATE saga_steps
		SET poll_count = poll_count + 1, next_poll_at = $2
		WHERE id = $1
	`
	result, err := s.db.ExecContext(ctx, query, stepID, nextPollAt)
	if err != nil {
		return fmt.Errorf("failed to increment step poll count: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("step not found: %s", stepID)
	}

	return nil
}

// CreatePollTask creates a new poll task for an async step.
func (s *PostgresStore) CreatePollTask(ctx context.Context, task *PollTask) error {
	query := `
		INSERT INTO saga_poll_tasks (step_id, transaction_id, next_poll_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (step_id) DO UPDATE SET next_poll_at = $3
	`
	_, err := s.db.ExecContext(ctx, query, task.StepID, task.TransactionID, task.NextPollAt)
	if err != nil {
		return fmt.Errorf("failed to create poll task: %w", err)
	}
	return nil
}

// DeletePollTask removes a poll task by step ID.
func (s *PostgresStore) DeletePollTask(ctx context.Context, stepID string) error {
	query := `DELETE FROM saga_poll_tasks WHERE step_id = $1`
	_, err := s.db.ExecContext(ctx, query, stepID)
	if err != nil {
		return fmt.Errorf("failed to delete poll task: %w", err)
	}
	return nil
}

// ListRecoverableTransactions returns transactions that need recovery after crash.
// Uses FOR UPDATE SKIP LOCKED to ensure multi-instance safety.
// Transactions are claimed (locked_by/locked_until set) atomically within the same DB transaction.
func (s *PostgresStore) ListRecoverableTransactions(ctx context.Context, instanceID string, batchSize int, leaseDuration time.Duration) ([]*Transaction, error) {
	lockedUntil := time.Now().Add(leaseDuration)

	dbTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer dbTx.Rollback()

	selectQuery := `
		SELECT id, status, payload, current_step, created_at, updated_at, finished_at, timeout_at, retry_count, last_error
		FROM saga_transactions
		WHERE status IN ('pending', 'running', 'compensating')
		  AND (locked_by IS NULL OR locked_until < NOW())
		ORDER BY created_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	`

	rows, err := dbTx.QueryContext(ctx, selectQuery, batchSize)
	if err != nil {
		return nil, fmt.Errorf("failed to query recoverable transactions: %w", err)
	}

	var txs []*Transaction
	var txIDs []string
	for rows.Next() {
		var tx Transaction
		var payloadJSON []byte
		var status string
		var lastError sql.NullString
		err := rows.Scan(
			&tx.ID,
			&status,
			&payloadJSON,
			&tx.CurrentStep,
			&tx.CreatedAt,
			&tx.UpdatedAt,
			&tx.FinishedAt,
			&tx.TimeoutAt,
			&tx.RetryCount,
			&lastError,
		)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("failed to scan transaction: %w", err)
		}

		tx.Status = TxStatus(status)
		tx.LastError = lastError.String
		if len(payloadJSON) > 0 {
			if err := json.Unmarshal(payloadJSON, &tx.Payload); err != nil {
				rows.Close()
				return nil, fmt.Errorf("failed to unmarshal payload: %w", err)
			}
		}
		txs = append(txs, &tx)
		txIDs = append(txIDs, tx.ID)
	}
	rows.Close()

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating transactions: %w", err)
	}

	// Batch claim all selected transactions within the same DB transaction
	if len(txIDs) > 0 {
		updateQuery := `
			UPDATE saga_transactions
			SET locked_by = $1, locked_until = $2, updated_at = NOW()
			WHERE id = ANY($3)
		`
		_, err = dbTx.ExecContext(ctx, updateQuery, instanceID, lockedUntil, pq.Array(txIDs))
		if err != nil {
			return nil, fmt.Errorf("failed to claim transactions: %w", err)
		}
	}

	if err := dbTx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit: %w", err)
	}

	return txs, nil
}

// ListTimedOutTransactions returns transactions that have exceeded their timeout.
// Uses FOR UPDATE SKIP LOCKED to ensure multi-instance safety.
// Transactions are claimed atomically within the same DB transaction.
func (s *PostgresStore) ListTimedOutTransactions(ctx context.Context, instanceID string, leaseDuration time.Duration) ([]*Transaction, error) {
	lockedUntil := time.Now().Add(leaseDuration)

	dbTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer dbTx.Rollback()

	selectQuery := `
		SELECT id, status, payload, current_step, created_at, updated_at, finished_at, timeout_at, retry_count, last_error
		FROM saga_transactions
		WHERE status IN ('running', 'compensating')
		  AND timeout_at IS NOT NULL
		  AND timeout_at < NOW()
		  AND (locked_by IS NULL OR locked_until < NOW())
		ORDER BY timeout_at
		LIMIT 50
		FOR UPDATE SKIP LOCKED
	`

	rows, err := dbTx.QueryContext(ctx, selectQuery)
	if err != nil {
		return nil, fmt.Errorf("failed to query timed out transactions: %w", err)
	}

	var txs []*Transaction
	var txIDs []string
	for rows.Next() {
		var tx Transaction
		var payloadJSON []byte
		var status string
		var lastError sql.NullString
		err := rows.Scan(
			&tx.ID,
			&status,
			&payloadJSON,
			&tx.CurrentStep,
			&tx.CreatedAt,
			&tx.UpdatedAt,
			&tx.FinishedAt,
			&tx.TimeoutAt,
			&tx.RetryCount,
			&lastError,
		)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("failed to scan transaction: %w", err)
		}

		tx.Status = TxStatus(status)
		tx.LastError = lastError.String
		if len(payloadJSON) > 0 {
			if err := json.Unmarshal(payloadJSON, &tx.Payload); err != nil {
				rows.Close()
				return nil, fmt.Errorf("failed to unmarshal payload: %w", err)
			}
		}
		txs = append(txs, &tx)
		txIDs = append(txIDs, tx.ID)
	}
	rows.Close()

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating transactions: %w", err)
	}

	if len(txIDs) > 0 {
		updateQuery := `
			UPDATE saga_transactions
			SET locked_by = $1, locked_until = $2, updated_at = NOW()
			WHERE id = ANY($3)
		`
		_, err = dbTx.ExecContext(ctx, updateQuery, instanceID, lockedUntil, pq.Array(txIDs))
		if err != nil {
			return nil, fmt.Errorf("failed to claim timed out transactions: %w", err)
		}
	}

	if err := dbTx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit: %w", err)
	}

	return txs, nil
}

// AcquirePollTasks acquires poll tasks using FOR UPDATE SKIP LOCKED for distributed safety.
// It locks the tasks and returns them for processing.
func (s *PostgresStore) AcquirePollTasks(ctx context.Context, instanceID string, batchSize int) ([]*PollTask, error) {
	// Lock duration: 2 minutes
	lockDuration := 2 * time.Minute
	lockUntil := time.Now().Add(lockDuration)

	// Use a database transaction for atomicity
	dbTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer dbTx.Rollback()

	// Select and lock tasks
	selectQuery := `
		SELECT id, step_id, transaction_id, next_poll_at
		FROM saga_poll_tasks
		WHERE next_poll_at <= NOW()
		  AND (locked_until IS NULL OR locked_until < NOW())
		ORDER BY next_poll_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	`

	rows, err := dbTx.QueryContext(ctx, selectQuery, batchSize)
	if err != nil {
		return nil, fmt.Errorf("failed to query poll tasks: %w", err)
	}

	var tasks []*PollTask
	var taskIDs []int64
	for rows.Next() {
		var task PollTask
		err := rows.Scan(&task.ID, &task.StepID, &task.TransactionID, &task.NextPollAt)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("failed to scan poll task: %w", err)
		}
		tasks = append(tasks, &task)
		taskIDs = append(taskIDs, task.ID)
	}
	rows.Close()

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating poll tasks: %w", err)
	}

	// Update lock for acquired tasks
	if len(taskIDs) > 0 {
		updateQuery := `
			UPDATE saga_poll_tasks
			SET locked_until = $1, locked_by = $2
			WHERE id = ANY($3)
		`
		_, err = dbTx.ExecContext(ctx, updateQuery, lockUntil, instanceID, pq.Array(taskIDs))
		if err != nil {
			return nil, fmt.Errorf("failed to lock poll tasks: %w", err)
		}
	}

	if err := dbTx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return tasks, nil
}

// ReleasePollTask releases the lock on a poll task.
func (s *PostgresStore) ReleasePollTask(ctx context.Context, stepID string) error {
	query := `
		UPDATE saga_poll_tasks
		SET locked_until = NULL, locked_by = NULL
		WHERE step_id = $1
	`
	_, err := s.db.ExecContext(ctx, query, stepID)
	if err != nil {
		return fmt.Errorf("failed to release poll task: %w", err)
	}
	return nil
}

// ClaimTransaction attempts to acquire a distributed lock on a transaction.
// Only succeeds when the transaction is not locked or the lock has expired.
// The same instance can re-claim (reentrant) to support lease renewal.
// Returns (true, nil) when the lock is successfully acquired.
func (s *PostgresStore) ClaimTransaction(ctx context.Context, txID string, instanceID string, leaseDuration time.Duration) (bool, error) {
	lockedUntil := time.Now().Add(leaseDuration)

	query := `
		UPDATE saga_transactions
		SET locked_by = $2, locked_until = $3, updated_at = NOW()
		WHERE id = $1
		  AND status IN ('pending', 'running', 'compensating')
		  AND (locked_by IS NULL OR locked_until < NOW() OR locked_by = $2)
	`
	result, err := s.db.ExecContext(ctx, query, txID, instanceID, lockedUntil)
	if err != nil {
		return false, fmt.Errorf("failed to claim transaction: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("failed to get rows affected: %w", err)
	}

	return rowsAffected > 0, nil
}

// ReleaseTransaction releases the distributed lock on a transaction.
// Only releases the lock if it is held by the specified instance, preventing
// accidental release of another instance's lock.
func (s *PostgresStore) ReleaseTransaction(ctx context.Context, txID string, instanceID string) error {
	query := `
		UPDATE saga_transactions
		SET locked_by = NULL, locked_until = NULL, updated_at = NOW()
		WHERE id = $1 AND locked_by = $2
	`
	_, err := s.db.ExecContext(ctx, query, txID, instanceID)
	if err != nil {
		return fmt.Errorf("failed to release transaction: %w", err)
	}
	return nil
}

// UpdateTransactionStatusCAS updates a transaction status using Compare-And-Swap semantics.
// The update only succeeds when the current status matches expectedStatus, preventing
// concurrent overwrites by multiple instances.
// Returns (true, nil) when the update is successful.
func (s *PostgresStore) UpdateTransactionStatusCAS(ctx context.Context, txID string, expectedStatus TxStatus, newStatus TxStatus, lastError string) (bool, error) {
	var finishedAt interface{}
	if newStatus == TxStatusSucceeded || newStatus == TxStatusFailed {
		now := time.Now()
		finishedAt = now
	}

	query := `
		UPDATE saga_transactions
		SET status = $3, last_error = $4, updated_at = NOW(), finished_at = $5
		WHERE id = $1 AND status = $2
	`
	result, err := s.db.ExecContext(ctx, query, txID, string(expectedStatus), string(newStatus), lastError, finishedAt)
	if err != nil {
		return false, fmt.Errorf("failed to CAS update transaction status: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("failed to get rows affected: %w", err)
	}

	return rowsAffected > 0, nil
}

// DB returns the underlying database connection for use in transactions.
func (s *PostgresStore) DB() *sql.DB {
	return s.db
}
