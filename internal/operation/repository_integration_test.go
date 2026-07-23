package operation

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"reflect"
	"sync"
	"testing"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

func TestRepositoryBeginSequentialReplayAndConflict(t *testing.T) {
	db := openOperationTestDB(t)
	repo := NewRepository(db)
	owner := "operation-test-" + uuid.NewString()
	t.Cleanup(func() { deleteTestOperations(t, db, owner) })

	cmd := beginTestCommand(t, owner, "key-sequential", "region-a/vpc-a", map[string]any{"vpc_name": "vpc-a", "vlan_id": 101})
	first, decision, err := repo.Begin(context.Background(), cmd)
	if err != nil {
		t.Fatalf("begin first: %v", err)
	}
	if decision != DecisionNew || first.OperationID == "" || first.RootOperationID != first.OperationID {
		t.Fatalf("first operation/decision = %#v/%q", first, decision)
	}

	replayed, decision, err := repo.Begin(context.Background(), cmd)
	if err != nil {
		t.Fatalf("begin replay: %v", err)
	}
	if decision != DecisionReplay || replayed.OperationID != first.OperationID {
		t.Fatalf("replay operation/decision = %#v/%q, want ID %s", replayed, decision, first.OperationID)
	}

	conflicting := beginTestCommand(t, owner, cmd.IdempotencyKey, cmd.TargetScope, map[string]any{"vpc_name": "vpc-a", "vlan_id": 202})
	conflictOp, decision, err := repo.Begin(context.Background(), conflicting)
	if err != nil {
		t.Fatalf("begin conflict: %v", err)
	}
	if decision != DecisionConflict || conflictOp.OperationID != first.OperationID {
		t.Fatalf("conflict operation/decision = %#v/%q, want existing ID %s", conflictOp, decision, first.OperationID)
	}
	if got := countTestOperations(t, db, owner); got != 1 {
		t.Fatalf("operation rows = %d, want 1", got)
	}
}

func TestRepositoryBeginConcurrentReplayHasOneWinner(t *testing.T) {
	db := openOperationTestDB(t)
	repo := NewRepository(db)
	owner := "operation-concurrent-" + uuid.NewString()
	t.Cleanup(func() { deleteTestOperations(t, db, owner) })
	cmd := beginTestCommand(t, owner, "key-concurrent", "region-a/vpc-a", map[string]any{"vpc_name": "vpc-a"})

	const contenders = 100
	start := make(chan struct{})
	type result struct {
		op       *Operation
		decision Decision
		err      error
	}
	results := make(chan result, contenders)
	var wg sync.WaitGroup
	var ready sync.WaitGroup
	ready.Add(contenders)
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ready.Done()
			<-start
			op, decision, err := repo.Begin(context.Background(), cmd)
			results <- result{op: op, decision: decision, err: err}
		}()
	}
	ready.Wait()
	close(start)
	wg.Wait()
	close(results)

	var operationID string
	newCount := 0
	replayCount := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent Begin: %v", result.err)
		}
		if operationID == "" {
			operationID = result.op.OperationID
		}
		if result.op.OperationID != operationID {
			t.Fatalf("operation ID = %s, want %s", result.op.OperationID, operationID)
		}
		switch result.decision {
		case DecisionNew:
			newCount++
		case DecisionReplay:
			replayCount++
		default:
			t.Fatalf("unexpected decision: %q", result.decision)
		}
	}
	if newCount != 1 || replayCount != contenders-1 {
		t.Fatalf("new/replay counts = %d/%d, want 1/%d", newCount, replayCount, contenders-1)
	}
	if got := countTestOperations(t, db, owner); got != 1 {
		t.Fatalf("operation rows = %d, want 1", got)
	}
}

func TestRepositoryDifferentKeysDoNotBypassResourceArbitrationBoundary(t *testing.T) {
	db := openOperationTestDB(t)
	repo := NewRepository(db)
	owner := "operation-target-" + uuid.NewString()
	t.Cleanup(func() { deleteTestOperations(t, db, owner) })

	first, firstDecision, err := repo.Begin(context.Background(), beginTestCommand(t, owner, "key-a", "region-a/vpc-a", map[string]any{"vpc_name": "vpc-a"}))
	if err != nil {
		t.Fatalf("begin first: %v", err)
	}
	second, secondDecision, err := repo.Begin(context.Background(), beginTestCommand(t, owner, "key-b", "region-a/vpc-a", map[string]any{"vpc_name": "vpc-a"}))
	if err != nil {
		t.Fatalf("begin second: %v", err)
	}
	if firstDecision != DecisionNew || secondDecision != DecisionNew || first.OperationID == second.OperationID {
		t.Fatalf("different-key operations = %s/%s decisions=%s/%s", first.OperationID, second.OperationID, firstDecision, secondDecision)
	}
	if got := countTestOperations(t, db, owner); got != 2 {
		t.Fatalf("operation rows = %d, want 2 before resource-layer arbitration", got)
	}
}

func TestRepositoryStoresReplayableResponseWithVersionCAS(t *testing.T) {
	db := openOperationTestDB(t)
	repo := NewRepository(db)
	owner := "operation-response-" + uuid.NewString()
	t.Cleanup(func() { deleteTestOperations(t, db, owner) })
	cmd := beginTestCommand(t, owner, "key-response", "region-a/vpc-a", map[string]any{"vpc_name": "vpc-a"})

	op, decision, err := repo.Begin(context.Background(), cmd)
	if err != nil || decision != DecisionNew {
		t.Fatalf("begin operation: decision=%q err=%v", decision, err)
	}
	updated, err := repo.UpdateStatusCAS(context.Background(), op.OperationID, 0, "create_vpc", StatusAccepted, StatusDispatching, "", "")
	if err != nil || !updated {
		t.Fatalf("dispatch operation: updated=%v err=%v", updated, err)
	}
	updated, err = repo.UpdateStatusCAS(context.Background(), op.OperationID, 1, "create_vpc", StatusDispatching, StatusRunning, "", "")
	if err != nil || !updated {
		t.Fatalf("run operation: updated=%v err=%v", updated, err)
	}
	response := json.RawMessage(`{"code":"0","operation_id":"` + op.OperationID + `","status":"succeeded"}`)
	updated, err = repo.StoreResponseCAS(context.Background(), op.OperationID, 2, "create_vpc", StatusRunning, StatusSucceeded, "0", response)
	if err != nil || !updated {
		t.Fatalf("store response: updated=%v err=%v", updated, err)
	}
	updated, err = repo.StoreResponseCAS(context.Background(), op.OperationID, 2, "create_vpc", StatusRunning, StatusFailed, "FAILED", json.RawMessage(`{"code":"FAILED"}`))
	if err != nil {
		t.Fatalf("stale store response: %v", err)
	}
	if updated {
		t.Fatal("stale version overwrote stored response")
	}

	replayed, decision, err := repo.Begin(context.Background(), cmd)
	if err != nil || decision != DecisionReplay {
		t.Fatalf("replay operation: decision=%q err=%v", decision, err)
	}
	if replayed.Status != StatusSucceeded || replayed.ResponseCode != "0" {
		t.Fatalf("replayed response = status:%q code:%q payload:%s", replayed.Status, replayed.ResponseCode, replayed.ResponsePayload)
	}
	assertJSONEqual(t, replayed.ResponsePayload, response)
}

func TestRepositoryStatusCASCannotRegressTerminalState(t *testing.T) {
	db := openOperationTestDB(t)
	repo := NewRepository(db)
	owner := "operation-status-" + uuid.NewString()
	t.Cleanup(func() { deleteTestOperations(t, db, owner) })
	op, _, err := repo.Begin(context.Background(), beginTestCommand(t, owner, "key-status", "region-a/vpc-a", map[string]any{"vpc_name": "vpc-a"}))
	if err != nil {
		t.Fatalf("begin operation: %v", err)
	}

	updated, err := repo.UpdateStatusCAS(context.Background(), op.OperationID, 0, "create_vpc", StatusAccepted, StatusDispatching, "", "")
	if err != nil || !updated {
		t.Fatalf("accepted -> dispatching: updated=%v err=%v", updated, err)
	}
	updated, err = repo.UpdateStatusCAS(context.Background(), op.OperationID, 1, "create_vpc", StatusDispatching, StatusRunning, "", "")
	if err != nil || !updated {
		t.Fatalf("dispatching -> running: updated=%v err=%v", updated, err)
	}
	updated, err = repo.UpdateStatusCAS(context.Background(), op.OperationID, 2, "create_vpc", StatusRunning, StatusSucceeded, "", "")
	if err != nil || !updated {
		t.Fatalf("running -> succeeded: updated=%v err=%v", updated, err)
	}
	updated, err = repo.UpdateStatusCAS(context.Background(), op.OperationID, 3, "create_vpc", StatusSucceeded, StatusRunning, "", "")
	if err == nil || updated {
		t.Fatalf("terminal regression updated=%v err=%v", updated, err)
	}
}

func TestRepositoryStatusCASRejectsBackwardUnknownAndArbitraryResponseTransitions(t *testing.T) {
	db := openOperationTestDB(t)
	repo := NewRepository(db)
	owner := "operation-invalid-status-" + uuid.NewString()
	t.Cleanup(func() { deleteTestOperations(t, db, owner) })
	op, _, err := repo.Begin(context.Background(), beginTestCommand(t, owner, "key-invalid-status", "region-a/vpc-a", map[string]any{"vpc_name": "vpc-a"}))
	if err != nil {
		t.Fatalf("begin operation: %v", err)
	}

	for _, test := range []struct {
		name     string
		expected Status
		next     Status
	}{
		{name: "skips dispatching", expected: StatusAccepted, next: StatusRunning},
		{name: "unknown next", expected: StatusAccepted, next: Status("unknown")},
	} {
		t.Run(test.name, func(t *testing.T) {
			updated, err := repo.UpdateStatusCAS(context.Background(), op.OperationID, 0, "create_vpc", test.expected, test.next, "", "")
			if err == nil || updated {
				t.Fatalf("invalid transition updated=%v err=%v", updated, err)
			}
		})
	}

	updated, err := repo.StoreResponseCAS(context.Background(), op.OperationID, 0, "create_vpc", StatusAccepted, StatusRunning, "0", json.RawMessage(`{"code":"0"}`))
	if err == nil || updated {
		t.Fatalf("arbitrary response transition updated=%v err=%v", updated, err)
	}
}

func TestRepositoryStatusCASMatchesPersistedOperationType(t *testing.T) {
	db := openOperationTestDB(t)
	repo := NewRepository(db)
	owner := "operation-type-status-" + uuid.NewString()
	t.Cleanup(func() { deleteTestOperations(t, db, owner) })
	op, _, err := repo.Begin(context.Background(), beginTestCommand(t, owner, "key-operation-type", "region-a/vpc-a", map[string]any{"vpc_name": "vpc-a"}))
	if err != nil {
		t.Fatalf("begin operation: %v", err)
	}

	updated, err := repo.UpdateStatusCAS(context.Background(), op.OperationID, 0, "delete_vpc", StatusAccepted, StatusDeleted, "", "")
	if err != nil {
		t.Fatalf("mismatched operation type: %v", err)
	}
	if updated {
		t.Fatal("delete state machine updated a persisted create operation")
	}
}

func TestValidateBeginCommandRejectsNonHexadecimalHash(t *testing.T) {
	cmd := BeginCommand{
		OwnerService:       "owner",
		CallerScope:        "caller",
		RouteScope:         "POST /api/v1/vpc",
		OperationType:      "create_vpc",
		TargetScope:        "region-a/vpc-a",
		IdempotencyKey:     "key",
		RequestHashVersion: CanonicalHashVersion,
		RequestHash:        "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",
		RequestPayload:     json.RawMessage(`{"vpc_name":"vpc-a"}`),
		ResourceType:       "vpc",
	}
	if err := validateBeginCommand(cmd); err == nil {
		t.Fatal("non-hexadecimal request hash accepted")
	}
}

func TestValidateBeginCommandRejectsUnknownOperationType(t *testing.T) {
	cmd := beginTestCommand(t, "owner", "key", "region-a/vpc-a", map[string]any{"vpc_name": "vpc-a"})
	cmd.OperationType = "crate_vpc"
	if err := validateBeginCommand(cmd); err == nil {
		t.Fatal("unknown operation type accepted")
	}
}

func TestStoreResponseCASRejectsUnknownOperationTypeForSameStatus(t *testing.T) {
	repo := NewRepository(nil)
	updated, err := repo.StoreResponseCAS(
		context.Background(),
		"operation-id",
		0,
		"crate_vpc",
		StatusAccepted,
		StatusAccepted,
		"0",
		json.RawMessage(`{"code":"0"}`),
	)
	if err == nil || updated {
		t.Fatalf("unknown operation type same-status response updated=%v err=%v", updated, err)
	}
}

func beginTestCommand(t *testing.T, owner, key, target string, payload any) BeginCommand {
	t.Helper()
	hash, canonical, err := CanonicalHash(CanonicalHashVersion, target, payload)
	if err != nil {
		t.Fatalf("canonical hash: %v", err)
	}
	return BeginCommand{
		OwnerService:       owner,
		CallerScope:        "caller-a",
		RouteScope:         "POST /api/v1/vpc",
		OperationType:      "create_vpc",
		TargetScope:        target,
		IdempotencyKey:     key,
		RequestHashVersion: CanonicalHashVersion,
		RequestHash:        hash,
		RequestPayload:     canonical,
		ResourceType:       "vpc",
		Generation:         1,
	}
}

func openOperationTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("NSP_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("NSP_TEST_POSTGRES_DSN is required for PostgreSQL integration tests")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	db.SetMaxOpenConns(32)
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Ping(); err != nil {
		t.Fatalf("ping PostgreSQL: %v", err)
	}
	migration, err := os.ReadFile("../db/migrations/005_create_operations.sql")
	if err != nil {
		t.Fatalf("read operation migration: %v", err)
	}
	if _, err := db.Exec(string(migration)); err != nil {
		t.Fatalf("apply operation migration: %v", err)
	}
	return db
}

func countTestOperations(t *testing.T, db *sql.DB, owner string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM orchestration_operations WHERE owner_service = $1`, owner).Scan(&count); err != nil {
		t.Fatalf("count operations: %v", err)
	}
	return count
}

func deleteTestOperations(t *testing.T, db *sql.DB, owner string) {
	t.Helper()
	if _, err := db.Exec(`DELETE FROM orchestration_operations WHERE owner_service = $1`, owner); err != nil {
		t.Errorf("delete operations: %v", err)
	}
}

func assertJSONEqual(t *testing.T, got, want json.RawMessage) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("decode got JSON: %v; payload=%s", err, got)
	}
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("decode want JSON: %v; payload=%s", err, want)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("JSON mismatch: got=%s want=%s", got, want)
	}
}
