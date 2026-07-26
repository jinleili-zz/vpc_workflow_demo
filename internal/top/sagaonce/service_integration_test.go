package sagaonce

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jinleili-zz/nsp-platform/saga"
	_ "github.com/lib/pq"
)

type countingSubmitter struct {
	calls atomic.Int64
}

func (s *countingSubmitter) Submit(_ context.Context, _ *saga.SagaDefinition) (string, error) {
	return fmt.Sprintf("tx-%d", s.calls.Add(1)), nil
}

func TestSubmitOnceConcurrentHasOneSaga(t *testing.T) {
	db := openSagaOnceTestDB(t)
	externalKey := "saga-once-" + uuid.NewString()
	t.Cleanup(func() { cleanupSagaOnce(t, db, externalKey) })
	submitter := &countingSubmitter{}
	service := NewService(db, submitter)

	const contenders = 100
	start := make(chan struct{})
	results := make(chan string, contenders)
	errs := make(chan error, contenders)
	var wg sync.WaitGroup
	for range contenders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			txID, err := service.SubmitOnce(context.Background(), externalKey, "operation-1", testDefinition())
			results <- txID
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("submit once: %v", err)
		}
	}
	for txID := range results {
		if txID != "tx-1" {
			t.Fatalf("transaction ID = %q, want tx-1", txID)
		}
	}
	if got := submitter.calls.Load(); got != 1 {
		t.Fatalf("submit calls = %d, want 1", got)
	}
}

func TestSubmitOnceResolvedReusesFirstCommittedDefinition(t *testing.T) {
	db := openSagaOnceTestDB(t)
	externalKey := "saga-conflict-" + uuid.NewString()
	t.Cleanup(func() { cleanupSagaOnce(t, db, externalKey) })
	service := NewService(db, &countingSubmitter{})
	firstTx, err := service.SubmitOnce(context.Background(), externalKey, "operation-1", testDefinition())
	if err != nil {
		t.Fatalf("first submit: %v", err)
	}
	changed := testDefinition()
	changed.Steps[0].ActionURL = "http://az-b/api/v1/vpc"
	submission, err := service.SubmitOnceResolved(context.Background(), externalKey, "operation-1", changed)
	if err != nil {
		t.Fatalf("resolve first definition: %v", err)
	}
	if submission.TransactionID != firstTx || submission.Definition.Steps[0].ActionURL != "http://az-a/api/v1/vpc" {
		t.Fatalf("resolved submission = %#v", submission)
	}
	existing, found, err := service.ResolveExisting(context.Background(), externalKey, "operation-1")
	if err != nil || !found || existing.TransactionID != firstTx || existing.Definition.Steps[0].ActionURL != "http://az-a/api/v1/vpc" {
		t.Fatalf("resolve existing submission = %#v/%v/%v", existing, found, err)
	}
}

func TestSubmitOnceRecoversCrashAfterSagaPersistence(t *testing.T) {
	db := openSagaOnceTestDB(t)
	externalKey := "saga-recover-" + uuid.NewString()
	t.Cleanup(func() { cleanupSagaOnce(t, db, externalKey) })
	definition := testDefinition()
	hash, err := DefinitionHash(definition)
	if err != nil {
		t.Fatalf("definition hash: %v", err)
	}
	const persistedTxID = "persisted-before-crash"
	_, err = db.Exec(`
		INSERT INTO saga_transactions (id, status, payload, current_step)
		VALUES ($1, 'pending', jsonb_build_object('_external_key', $2::text, '_definition_hash', $3::text), 0)
	`, persistedTxID, externalKey, hash)
	if err != nil {
		t.Fatalf("insert persisted saga: %v", err)
	}

	submitter := &countingSubmitter{}
	service := NewService(db, submitter)
	txID, err := service.SubmitOnce(context.Background(), externalKey, "operation-1", definition)
	if err != nil {
		t.Fatalf("recover submit: %v", err)
	}
	if txID != persistedTxID || submitter.calls.Load() != 0 {
		t.Fatalf("recovered tx/calls = %q/%d", txID, submitter.calls.Load())
	}
}

func TestSubmitOnceRecoveryUsesPersistedDefinitionSnapshot(t *testing.T) {
	db := openSagaOnceTestDB(t)
	externalKey := "saga-snapshot-recover-" + uuid.NewString()
	t.Cleanup(func() { cleanupSagaOnce(t, db, externalKey) })
	first := testDefinition()
	hash, err := DefinitionHash(first)
	if err != nil {
		t.Fatalf("definition hash: %v", err)
	}
	definitionJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal definition: %v", err)
	}
	const persistedTxID = "persisted-snapshot-before-crash"
	if _, err := db.Exec(`
		INSERT INTO saga_transactions (id, status, payload, current_step)
		VALUES ($1, 'pending', jsonb_build_object('_external_key', $2::text, '_definition_hash', $3::text, '_definition', $4::jsonb), 0)
	`, persistedTxID, externalKey, hash, definitionJSON); err != nil {
		t.Fatalf("insert persisted saga snapshot: %v", err)
	}
	changed := testDefinition()
	changed.Steps[0].ActionURL = "http://az-new/api/v1/vpc"
	submitter := &countingSubmitter{}
	submission, err := NewService(db, submitter).SubmitOnceResolved(context.Background(), externalKey, "operation-1", changed)
	if err != nil {
		t.Fatalf("recover snapshot: %v", err)
	}
	if submission.TransactionID != persistedTxID || submission.Definition.Steps[0].ActionURL != "http://az-a/api/v1/vpc" || submitter.calls.Load() != 0 {
		t.Fatalf("recovered submission/calls = %#v/%d", submission, submitter.calls.Load())
	}
}

func TestResolveExistingRecoversUnattachedPersistedSaga(t *testing.T) {
	db := openSagaOnceTestDB(t)
	externalKey := "saga-resolve-unattached-" + uuid.NewString()
	t.Cleanup(func() { cleanupSagaOnce(t, db, externalKey) })
	first := testDefinition()
	hash, err := DefinitionHash(first)
	if err != nil {
		t.Fatalf("definition hash: %v", err)
	}
	definitionJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal definition: %v", err)
	}
	const persistedTxID = "persisted-unattached-before-crash"
	if _, err := db.Exec(`
		INSERT INTO saga_transactions (id, status, payload, current_step)
		VALUES ($1, 'pending', jsonb_build_object(
			'_external_key', $2::text,
			'_definition_hash', $3::text,
			'_definition', $4::jsonb,
			'_operation_id', 'operation-1'::text
		), 0)
	`, persistedTxID, externalKey, hash, definitionJSON); err != nil {
		t.Fatalf("insert unattached saga: %v", err)
	}

	service := NewService(db, &countingSubmitter{})
	submission, found, err := service.ResolveExisting(context.Background(), externalKey, "operation-1")
	if err != nil || !found {
		t.Fatalf("resolve unattached saga = %#v/%v/%v", submission, found, err)
	}
	if submission.TransactionID != persistedTxID || submission.Definition.Steps[0].ActionURL != "http://az-a/api/v1/vpc" {
		t.Fatalf("resolved unattached submission = %#v", submission)
	}
	var attachedTxID string
	if err := db.QueryRow(`SELECT saga_transaction_id FROM top_saga_submissions WHERE external_key = $1`, externalKey).Scan(&attachedTxID); err != nil {
		t.Fatalf("load restored submission: %v", err)
	}
	if attachedTxID != persistedTxID {
		t.Fatalf("attached transaction ID = %q, want %q", attachedTxID, persistedTxID)
	}
}

func testDefinition() *saga.SagaDefinition {
	return &saga.SagaDefinition{
		Name:       "create-vpc",
		TimeoutSec: 60,
		Payload:    map[string]any{"vpc_name": "vpc-a"},
		Steps: []saga.Step{{
			Name:             "create-az-a",
			Type:             saga.StepTypeSync,
			ActionMethod:     "POST",
			ActionURL:        "http://az-a/api/v1/vpc",
			ActionPayload:    map[string]any{"vpc_name": "vpc-a"},
			CompensateMethod: "DELETE",
			CompensateURL:    "http://az-a/api/v1/vpc/vpc-a",
		}},
	}
}

func openSagaOnceTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("NSP_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("NSP_TEST_POSTGRES_DSN is not set")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	db.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Ping(); err != nil {
		t.Fatalf("ping postgres: %v", err)
	}
	sagaMigration, err := os.ReadFile("../../../deployments/docker/saga-migration.sql")
	if err != nil {
		t.Fatalf("read saga migration: %v", err)
	}
	if _, err := db.Exec(string(sagaMigration)); err != nil {
		t.Fatalf("apply saga migration: %v", err)
	}
	migration, err := os.ReadFile("../../db/migrations/007_create_top_saga_submissions.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := db.Exec(string(migration)); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO orchestration_operations (
			operation_id, root_operation_id, owner_service, caller_scope, route_scope,
			operation_type, target_scope, idempotency_key, request_hash_version,
			request_hash, request_payload, resource_type, generation, status
		) VALUES (
			'operation-1', 'operation-1', 'saga-once-test', 'test', 'POST /test',
			'create_vpc', 'test/vpc', 'test-key', 1,
			repeat('0', 64), '{}'::jsonb, 'vpc', 1, 'accepted'
		) ON CONFLICT (operation_id) DO NOTHING
	`); err != nil {
		t.Fatalf("ensure test operation: %v", err)
	}
	return db
}

func cleanupSagaOnce(t *testing.T, db *sql.DB, externalKey string) {
	t.Helper()
	_, _ = db.Exec(`DELETE FROM top_saga_submissions WHERE external_key = $1`, externalKey)
	_, _ = db.Exec(`DELETE FROM saga_transactions WHERE payload->>'_external_key' = $1`, externalKey)
}
