package sagaonce

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jinleili-zz/nsp-platform/saga"
)

var ErrDefinitionConflict = errors.New("SAGA_EXTERNAL_KEY_REUSED")

type Submitter interface {
	Submit(context.Context, *saga.SagaDefinition) (string, error)
}

type Service struct {
	db        *sql.DB
	submitter Submitter
}

type Submission struct {
	TransactionID string
	Definition    *saga.SagaDefinition
}

func NewService(db *sql.DB, submitter Submitter) *Service {
	return &Service{db: db, submitter: submitter}
}

func (s *Service) ResolveExisting(ctx context.Context, externalKey, operationID string) (*Submission, bool, error) {
	if s.db == nil || externalKey == "" || operationID == "" {
		return nil, false, fmt.Errorf("saga once database, external key and operation ID are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("begin saga resolution transaction: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, externalKey); err != nil {
		return nil, false, fmt.Errorf("lock saga external key: %w", err)
	}

	var storedOperationID string
	var txID sql.NullString
	var definitionPayload []byte
	err = tx.QueryRowContext(ctx, `
		SELECT operation_id, saga_transaction_id, COALESCE(definition_payload, 'null'::jsonb)::text
		FROM top_saga_submissions WHERE external_key = $1
	`, externalKey).Scan(&storedOperationID, &txID, &definitionPayload)
	submissionMissing := err == sql.ErrNoRows
	if err != nil && err != sql.ErrNoRows {
		return nil, false, fmt.Errorf("resolve saga submission: %w", err)
	}
	if err == nil && storedOperationID != operationID {
		return nil, false, fmt.Errorf("%w: external key belongs to another operation", ErrDefinitionConflict)
	}
	if err == nil && txID.Valid && txID.String != "" && string(definitionPayload) != "null" {
		definition, err := decodeDefinition(definitionPayload, "resolved")
		if err != nil {
			return nil, false, err
		}
		if err := tx.Commit(); err != nil {
			return nil, false, fmt.Errorf("commit saga resolution: %w", err)
		}
		return &Submission{TransactionID: txID.String, Definition: definition}, true, nil
	}

	recoveredTxID, recoveredHash, recoveredDefinition, recoveredOperationID, found, err := findPersistedSaga(ctx, tx, externalKey)
	if err != nil {
		return nil, false, err
	}
	// Older Saga rows without the durable operation identity or immutable
	// definition cannot safely bypass the current request's validation path.
	if !found || recoveredOperationID == "" || len(recoveredDefinition) == 0 {
		return nil, false, nil
	}
	if recoveredOperationID != operationID {
		return nil, false, fmt.Errorf("%w: persisted saga belongs to another operation", ErrDefinitionConflict)
	}
	definition, err := decodeDefinition(recoveredDefinition, "recovered")
	if err != nil {
		return nil, false, err
	}
	actualHash, err := DefinitionHash(definition)
	if err != nil {
		return nil, false, err
	}
	if recoveredHash == "" || recoveredHash != actualHash {
		return nil, false, fmt.Errorf("%w: persisted saga definition hash does not match its snapshot", ErrDefinitionConflict)
	}
	if submissionMissing {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO top_saga_submissions (
				external_key, operation_id, definition_hash, definition_payload, saga_transaction_id
			) VALUES ($1, $2, $3, $4::jsonb, $5)
		`, externalKey, operationID, recoveredHash, recoveredDefinition, recoveredTxID); err != nil {
			return nil, false, fmt.Errorf("restore saga submission: %w", err)
		}
	} else {
		if _, err := tx.ExecContext(ctx, `
			UPDATE top_saga_submissions
			SET definition_hash = $1, definition_payload = $2::jsonb,
				saga_transaction_id = $3, updated_at = NOW()
			WHERE external_key = $4 AND operation_id = $5
		`, recoveredHash, recoveredDefinition, recoveredTxID, externalKey, operationID); err != nil {
			return nil, false, fmt.Errorf("restore incomplete saga submission: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("commit restored saga submission: %w", err)
	}
	return &Submission{TransactionID: recoveredTxID, Definition: definition}, true, nil
}

func (s *Service) SubmitOnce(ctx context.Context, externalKey, operationID string, definition *saga.SagaDefinition) (string, error) {
	submission, err := s.SubmitOnceResolved(ctx, externalKey, operationID, definition)
	if err != nil {
		return "", err
	}
	return submission.TransactionID, nil
}

// SubmitOnceResolved returns the immutable definition that won the external
// key. Recovery therefore continues the first submitted AZ snapshot even when
// live registry membership or addresses have changed.
func (s *Service) SubmitOnceResolved(ctx context.Context, externalKey, operationID string, definition *saga.SagaDefinition) (*Submission, error) {
	if s.db == nil || s.submitter == nil {
		return nil, fmt.Errorf("saga once database and submitter are required")
	}
	if externalKey == "" || len(externalKey) > 256 || operationID == "" || definition == nil {
		return nil, fmt.Errorf("external key, operation ID and definition are required")
	}
	definitionHash, err := DefinitionHash(definition)
	if err != nil {
		return nil, err
	}
	definitionPayload, err := json.Marshal(definition)
	if err != nil {
		return nil, fmt.Errorf("marshal saga definition: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin saga submission transaction: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, externalKey); err != nil {
		return nil, fmt.Errorf("lock saga external key: %w", err)
	}

	var storedOperationID, storedHash string
	var storedTxID sql.NullString
	var storedDefinition []byte
	err = tx.QueryRowContext(ctx, `
		SELECT operation_id, definition_hash, saga_transaction_id, COALESCE(definition_payload, 'null'::jsonb)::text
		FROM top_saga_submissions WHERE external_key = $1
	`, externalKey).Scan(&storedOperationID, &storedHash, &storedTxID, &storedDefinition)
	switch {
	case err == nil:
		if storedOperationID != operationID {
			return nil, fmt.Errorf("%w: external key belongs to another operation", ErrDefinitionConflict)
		}
		if string(storedDefinition) == "null" {
			storedDefinition = nil
		}
		if len(storedDefinition) > 0 {
			var first saga.SagaDefinition
			if err := json.Unmarshal(storedDefinition, &first); err != nil {
				return nil, fmt.Errorf("decode stored saga definition: %w", err)
			}
			definition = &first
			definitionHash = storedHash
			definitionPayload = storedDefinition
		}
		if storedTxID.Valid && storedTxID.String != "" {
			if err := tx.Commit(); err != nil {
				return nil, fmt.Errorf("commit replayed saga submission: %w", err)
			}
			return &Submission{TransactionID: storedTxID.String, Definition: cloneDefinition(definition)}, nil
		}
	case err == sql.ErrNoRows:
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO top_saga_submissions (external_key, operation_id, definition_hash, definition_payload)
			VALUES ($1, $2, $3, $4::jsonb)
		`, externalKey, operationID, definitionHash, definitionPayload); err != nil {
			return nil, fmt.Errorf("record saga external key: %w", err)
		}
	case err != nil:
		return nil, fmt.Errorf("load saga submission: %w", err)
	}

	if recoveredTxID, recoveredHash, recoveredDefinition, recoveredOperationID, found, err := findPersistedSaga(ctx, tx, externalKey); err != nil {
		return nil, err
	} else if found {
		if recoveredOperationID != "" && recoveredOperationID != operationID {
			return nil, fmt.Errorf("%w: persisted saga belongs to another operation", ErrDefinitionConflict)
		}
		if len(recoveredDefinition) > 0 {
			var first saga.SagaDefinition
			if err := json.Unmarshal(recoveredDefinition, &first); err != nil {
				return nil, fmt.Errorf("decode recovered saga definition: %w", err)
			}
			definition, definitionHash, definitionPayload = &first, recoveredHash, recoveredDefinition
		} else if recoveredHash != definitionHash {
			return nil, fmt.Errorf("%w: persisted saga has a different definition without a recoverable snapshot", ErrDefinitionConflict)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE top_saga_submissions SET definition_hash = $1, definition_payload = $2::jsonb, updated_at = NOW()
			WHERE external_key = $3 AND operation_id = $4
		`, definitionHash, definitionPayload, externalKey, operationID); err != nil {
			return nil, fmt.Errorf("restore first saga definition: %w", err)
		}
		if err := attachTransaction(ctx, tx, externalKey, recoveredTxID); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit recovered saga submission: %w", err)
		}
		return &Submission{TransactionID: recoveredTxID, Definition: cloneDefinition(definition)}, nil
	}

	submitDefinition := cloneDefinition(definition)
	if submitDefinition.Payload == nil {
		submitDefinition.Payload = make(map[string]any)
	}
	submitDefinition.Payload["_external_key"] = externalKey
	submitDefinition.Payload["_definition_hash"] = definitionHash
	submitDefinition.Payload["_operation_id"] = operationID
	submitDefinition.Payload["_definition"] = json.RawMessage(definitionPayload)
	txID, err := s.submitter.Submit(ctx, submitDefinition)
	if err != nil {
		return nil, fmt.Errorf("submit saga: %w", err)
	}
	if txID == "" {
		return nil, fmt.Errorf("submit saga returned an empty transaction ID")
	}
	if err := attachTransaction(ctx, tx, externalKey, txID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit saga submission: %w", err)
	}
	return &Submission{TransactionID: txID, Definition: cloneDefinition(definition)}, nil
}

func findPersistedSaga(ctx context.Context, tx *sql.Tx, externalKey string) (txID, definitionHash string, definition []byte, operationID string, found bool, err error) {
	err = tx.QueryRowContext(ctx, `
		SELECT id, COALESCE(payload->>'_definition_hash', ''),
			COALESCE(payload->'_definition', 'null'::jsonb)::text,
			COALESCE(payload->>'_operation_id', '')
		FROM saga_transactions
		WHERE payload->>'_external_key' = $1
	`, externalKey).Scan(&txID, &definitionHash, &definition, &operationID)
	if err == sql.ErrNoRows {
		return "", "", nil, "", false, nil
	}
	if err != nil {
		return "", "", nil, "", false, fmt.Errorf("recover persisted saga: %w", err)
	}
	if string(definition) == "null" {
		definition = nil
	}
	return txID, definitionHash, definition, operationID, true, nil
}

func decodeDefinition(payload []byte, source string) (*saga.SagaDefinition, error) {
	var definition saga.SagaDefinition
	if err := json.Unmarshal(payload, &definition); err != nil {
		return nil, fmt.Errorf("decode %s saga definition: %w", source, err)
	}
	return &definition, nil
}

func attachTransaction(ctx context.Context, tx *sql.Tx, externalKey, txID string) error {
	result, err := tx.ExecContext(ctx, `
		UPDATE top_saga_submissions
		SET saga_transaction_id = $1, updated_at = NOW()
		WHERE external_key = $2 AND (saga_transaction_id IS NULL OR saga_transaction_id = $1)
	`, txID, externalKey)
	if err != nil {
		return fmt.Errorf("attach saga transaction: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return fmt.Errorf("attach saga transaction affected %d rows: %w", rows, err)
	}
	return nil
}

func DefinitionHash(definition *saga.SagaDefinition) (string, error) {
	if definition == nil {
		return "", fmt.Errorf("saga definition is required")
	}
	type stepDocument struct {
		Name              string
		Type              saga.StepType
		ActionMethod      string
		ActionURL         string
		ActionPayload     map[string]any
		AuthAK            string
		CompensateMethod  string
		CompensateURL     string
		CompensatePayload map[string]any
		PollURL           string
		PollMethod        string
		PollIntervalSec   int
		PollMaxTimes      int
		PollSuccessPath   string
		PollSuccessValue  string
		PollFailurePath   string
		PollFailureValue  string
		MaxRetry          int
	}
	document := struct {
		Name       string
		TimeoutSec int
		Payload    map[string]any
		Steps      []stepDocument
	}{Name: definition.Name, TimeoutSec: definition.TimeoutSec, Payload: definition.Payload}
	for _, step := range definition.Steps {
		document.Steps = append(document.Steps, stepDocument{
			Name: step.Name, Type: step.Type, ActionMethod: step.ActionMethod, ActionURL: step.ActionURL,
			ActionPayload: step.ActionPayload, AuthAK: step.AuthAK,
			CompensateMethod: step.CompensateMethod, CompensateURL: step.CompensateURL, CompensatePayload: step.CompensatePayload,
			PollURL: step.PollURL, PollMethod: step.PollMethod, PollIntervalSec: step.PollIntervalSec, PollMaxTimes: step.PollMaxTimes,
			PollSuccessPath: step.PollSuccessPath, PollSuccessValue: step.PollSuccessValue,
			PollFailurePath: step.PollFailurePath, PollFailureValue: step.PollFailureValue, MaxRetry: step.MaxRetry,
		})
	}
	payload, err := json.Marshal(document)
	if err != nil {
		return "", fmt.Errorf("marshal saga definition: %w", err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func cloneDefinition(definition *saga.SagaDefinition) *saga.SagaDefinition {
	cloned := *definition
	cloned.Payload = cloneMap(definition.Payload)
	cloned.Steps = make([]saga.Step, len(definition.Steps))
	copy(cloned.Steps, definition.Steps)
	for index := range cloned.Steps {
		cloned.Steps[index].ActionPayload = cloneMap(definition.Steps[index].ActionPayload)
		cloned.Steps[index].CompensatePayload = cloneMap(definition.Steps[index].CompensatePayload)
	}
	return &cloned
}

func cloneMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}
