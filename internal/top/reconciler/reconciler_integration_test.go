package reconciler

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"workflow_qoder/internal/operation"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

func TestReconcilerCompletesAfterRestartWithoutDuplicateChildPoll(t *testing.T) {
	db := openReconcilerDB(t)
	operationID := seedRunningOperation(t, db, "top-reconcile-success")
	t.Cleanup(func() { cleanupOperation(t, db, operationID) })
	repository := NewRepository(db, "top-reconcile-success")
	for _, az := range []string{"az-a", "az-b"} {
		if err := repository.RecordExecution(t.Context(), Execution{OperationID: operationID, Region: "region-a", AZ: az, ChildOperationID: "child-" + az}); err != nil {
			t.Fatalf("record execution: %v", err)
		}
	}
	var polls atomic.Int64
	poll := func(context.Context, Execution) (ChildResult, error) {
		polls.Add(1)
		return ChildResult{Status: operation.StatusSucceeded}, nil
	}
	// A fresh instance represents recovery after the submitting process exits.
	restarted := New(NewRepository(db, "top-reconcile-success"), "restarted-instance", poll, nil)
	processed, err := restarted.RunOnce(t.Context())
	if err != nil {
		t.Fatalf("reconcile after restart: %v", err)
	}
	if processed != 2 || polls.Load() != 2 {
		t.Fatalf("processed/polls = %d/%d, want 2/2", processed, polls.Load())
	}
	assertOperationStatus(t, db, operationID, operation.StatusSucceeded)
	if processed, err := restarted.RunOnce(t.Context()); err != nil || processed != 0 || polls.Load() != 2 {
		t.Fatalf("terminal replay processed/polls/error = %d/%d/%v", processed, polls.Load(), err)
	}
}

func TestConcurrentReconcilersDoNotRegressFailedAggregate(t *testing.T) {
	db := openReconcilerDB(t)
	operationID := seedRunningOperation(t, db, "top-reconcile-failure")
	t.Cleanup(func() { cleanupOperation(t, db, operationID) })
	repository := NewRepository(db, "top-reconcile-failure")
	if err := repository.RecordExecution(t.Context(), Execution{OperationID: operationID, Region: "region-a", AZ: "az-a", ChildOperationID: "child-failed"}); err != nil {
		t.Fatalf("record execution: %v", err)
	}
	poll := func(context.Context, Execution) (ChildResult, error) {
		return ChildResult{Status: operation.StatusFailed, ErrorCode: "DEVICE_FAILED", ErrorMessage: "device rejected policy"}, nil
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	for index := range 2 {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			_, err := New(NewRepository(db, "top-reconcile-failure"), fmt.Sprintf("instance-%d", index), poll, nil).RunOnce(context.Background())
			errCh <- err
		}(index)
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent reconcile: %v", err)
		}
	}
	assertOperationStatus(t, db, operationID, operation.StatusFailed)
	var version int64
	if err := db.QueryRow(`SELECT version FROM orchestration_operations WHERE operation_id = $1`, operationID).Scan(&version); err != nil {
		t.Fatalf("load operation version: %v", err)
	}
	if version != 3 {
		t.Fatalf("operation version = %d, want one terminal transition from running version 2", version)
	}
}

func TestRestartAggregatesChildCommittedBeforeParentTransition(t *testing.T) {
	db := openReconcilerDB(t)
	operationID := seedRunningOperation(t, db, "top-reconcile-crash-window")
	t.Cleanup(func() { cleanupOperation(t, db, operationID) })
	repository := NewRepository(db, "top-reconcile-crash-window")
	if err := repository.RecordExecution(t.Context(), Execution{OperationID: operationID, Region: "region-a", AZ: "az-a", ChildOperationID: "child-before-crash"}); err != nil {
		t.Fatalf("record execution: %v", err)
	}
	claimed, err := repository.ClaimBatch(t.Context(), "crashed-instance", 1, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim before crash: executions=%#v err=%v", claimed, err)
	}
	if updated, err := repository.UpdateClaim(t.Context(), claimed[0], "crashed-instance", ChildResult{Status: operation.StatusSucceeded}); err != nil || !updated {
		t.Fatalf("commit child before crash: updated=%v err=%v", updated, err)
	}

	var polls atomic.Int64
	restarted := New(NewRepository(db, "top-reconcile-crash-window"), "restarted-instance", func(context.Context, Execution) (ChildResult, error) {
		polls.Add(1)
		return ChildResult{}, fmt.Errorf("terminal child must not be polled")
	}, nil)
	if processed, err := restarted.RunOnce(t.Context()); err != nil || processed != 0 {
		t.Fatalf("restart aggregate: processed=%d err=%v", processed, err)
	}
	if polls.Load() != 0 {
		t.Fatalf("terminal child polls = %d, want 0", polls.Load())
	}
	assertOperationStatus(t, db, operationID, operation.StatusSucceeded)
}

func TestCompensationAggregateUsesRequiredIntermediateState(t *testing.T) {
	db := openReconcilerDB(t)
	operationID := seedRunningOperation(t, db, "top-reconcile-compensation")
	t.Cleanup(func() { cleanupOperation(t, db, operationID) })
	repository := NewRepository(db, "top-reconcile-compensation")
	if err := repository.RecordExecution(t.Context(), Execution{OperationID: operationID, Region: "region-a", AZ: "az-a", ChildOperationID: "child-compensated"}); err != nil {
		t.Fatalf("record execution: %v", err)
	}
	if _, err := db.Exec(`UPDATE operation_az_executions SET status = 'compensated' WHERE operation_id = $1`, operationID); err != nil {
		t.Fatalf("set child compensated: %v", err)
	}
	runner := New(repository, "compensation-instance", func(context.Context, Execution) (ChildResult, error) {
		return ChildResult{}, fmt.Errorf("terminal child must not be polled")
	}, nil)
	if _, err := runner.RunOnce(t.Context()); err != nil {
		t.Fatalf("enter compensating: %v", err)
	}
	assertOperationStatus(t, db, operationID, operation.StatusCompensating)
	if _, err := runner.RunOnce(t.Context()); err != nil {
		t.Fatalf("complete compensation: %v", err)
	}
	assertOperationStatus(t, db, operationID, operation.StatusCompensated)
	var responseCode string
	var activeClaims int
	if err := db.QueryRow(`SELECT response_code FROM orchestration_operations WHERE operation_id = $1`, operationID).Scan(&responseCode); err != nil {
		t.Fatalf("load compensated response code: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM orchestration_target_claims WHERE operation_id = $1 AND active = TRUE`, operationID).Scan(&activeClaims); err != nil {
		t.Fatalf("count compensated target claims: %v", err)
	}
	if responseCode != "COMPENSATED" || activeClaims != 0 {
		t.Fatalf("compensated response/active claims = %q/%d, want COMPENSATED/0", responseCode, activeClaims)
	}
}

func TestFailedSiblingThenCompensatedSiblingConvergesToCompensationFailed(t *testing.T) {
	db := openReconcilerDB(t)
	operationID := seedRunningOperation(t, db, "top-reconcile-mixed-compensation")
	t.Cleanup(func() { cleanupOperation(t, db, operationID) })
	repository := NewRepository(db, "top-reconcile-mixed-compensation")
	for _, item := range []struct {
		az     string
		status operation.Status
	}{{az: "az-failed", status: operation.StatusFailed}, {az: "az-compensating", status: operation.StatusCompensating}} {
		if err := repository.RecordExecution(t.Context(), Execution{OperationID: operationID, Region: "region-a", AZ: item.az, ChildOperationID: "child-" + item.az}); err != nil {
			t.Fatalf("record execution: %v", err)
		}
		if _, err := db.Exec(`UPDATE operation_az_executions SET status = $1 WHERE operation_id = $2 AND az = $3`, item.status, operationID, item.az); err != nil {
			t.Fatalf("set child status: %v", err)
		}
	}
	if _, err := repository.Aggregate(t.Context(), operationID); err != nil {
		t.Fatalf("enter compensating: %v", err)
	}
	assertOperationStatus(t, db, operationID, operation.StatusCompensating)
	if _, err := db.Exec(`UPDATE operation_az_executions SET status = 'compensated' WHERE operation_id = $1 AND az = 'az-compensating'`, operationID); err != nil {
		t.Fatalf("complete sibling compensation: %v", err)
	}
	if _, err := repository.Aggregate(t.Context(), operationID); err != nil {
		t.Fatalf("aggregate inconsistent terminal mix: %v", err)
	}
	assertOperationStatus(t, db, operationID, operation.StatusCompensationFailed)
}

func TestVFWAZRecordUpdateIsAtomicAndFencedWithExecutionLease(t *testing.T) {
	db := openReconcilerDB(t)
	operationID := seedRunningOperation(t, db, "top-nsp-vfw")
	t.Cleanup(func() { cleanupOperation(t, db, operationID) })
	var resourceID string
	if err := db.QueryRow(`SELECT resource_id FROM orchestration_operations WHERE operation_id = $1`, operationID).Scan(&resourceID); err != nil {
		t.Fatalf("load resource ID: %v", err)
	}
	if _, err := db.Exec(`UPDATE orchestration_operations SET operation_type = 'apply_firewall_policy' WHERE operation_id = $1`, operationID); err != nil {
		t.Fatalf("set VFW operation type: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO policy_registry (id, policy_name, source_ip, dest_ip, protocol, action, status)
		VALUES ($1, $2, '10.0.0.1', '10.0.0.2', 'tcp', 'allow', 'creating')
	`, resourceID, "fenced-policy-"+uuid.NewString()); err != nil {
		t.Fatalf("insert policy: %v", err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM policy_registry WHERE id = $1`, resourceID) })
	if _, err := db.Exec(`
		INSERT INTO policy_az_records (id, policy_id, region, az, status)
		VALUES ($1, $2, 'region-a', 'az-a', 'creating')
	`, uuid.NewString(), resourceID); err != nil {
		t.Fatalf("insert policy AZ record: %v", err)
	}
	repository := NewRepository(db, "top-nsp-vfw")
	if err := repository.RecordExecution(t.Context(), Execution{OperationID: operationID, Region: "region-a", AZ: "az-a", ChildOperationID: "child-vfw"}); err != nil {
		t.Fatalf("record execution: %v", err)
	}
	first, err := repository.ClaimBatch(t.Context(), "stale-instance", 1, 20*time.Millisecond)
	if err != nil || len(first) != 1 {
		t.Fatalf("first claim: %#v/%v", first, err)
	}
	time.Sleep(30 * time.Millisecond)
	second, err := repository.ClaimBatch(t.Context(), "winner-instance", 1, time.Minute)
	if err != nil || len(second) != 1 {
		t.Fatalf("takeover claim: %#v/%v", second, err)
	}
	if updated, err := repository.UpdateClaim(t.Context(), second[0], "winner-instance", ChildResult{Status: operation.StatusSucceeded}); err != nil || !updated {
		t.Fatalf("winner update: updated=%v err=%v", updated, err)
	}
	if updated, err := repository.UpdateClaim(t.Context(), first[0], "stale-instance", ChildResult{Status: operation.StatusRunning}); err != nil || updated {
		t.Fatalf("stale update: updated=%v err=%v", updated, err)
	}
	var executionStatus operation.Status
	var recordStatus string
	if err := db.QueryRow(`SELECT status FROM operation_az_executions WHERE operation_id = $1`, operationID).Scan(&executionStatus); err != nil {
		t.Fatalf("load execution status: %v", err)
	}
	if err := db.QueryRow(`SELECT status FROM policy_az_records WHERE policy_id = $1 AND region = 'region-a' AND az = 'az-a'`, resourceID).Scan(&recordStatus); err != nil {
		t.Fatalf("load AZ record status: %v", err)
	}
	if executionStatus != operation.StatusSucceeded || recordStatus != "running" {
		t.Fatalf("execution/record status = %s/%s, want succeeded/running", executionStatus, recordStatus)
	}
}

func TestPoisonAggregateRotatesWithoutStarvingLaterOperation(t *testing.T) {
	db := openReconcilerDB(t)
	owner := "top-reconcile-poison"
	poisonID := seedRunningOperation(t, db, owner)
	validID := seedRunningOperation(t, db, owner)
	t.Cleanup(func() {
		cleanupOperation(t, db, poisonID)
		cleanupOperation(t, db, validID)
	})
	repository := NewRepository(db, owner)
	for _, item := range []struct{ operationID, childID string }{{poisonID, "poison-child"}, {validID, "valid-child"}} {
		if err := repository.RecordExecution(t.Context(), Execution{OperationID: item.operationID, Region: "region-a", AZ: "az-a", ChildOperationID: item.childID}); err != nil {
			t.Fatalf("record execution: %v", err)
		}
		if _, err := db.Exec(`UPDATE operation_az_executions SET status = 'succeeded' WHERE operation_id = $1`, item.operationID); err != nil {
			t.Fatalf("set terminal child: %v", err)
		}
	}
	if _, err := db.Exec(`UPDATE orchestration_operations SET operation_type = 'create_subnet', updated_at = NOW() - INTERVAL '1 hour' WHERE operation_id = $1`, poisonID); err != nil {
		t.Fatalf("make poison aggregate: %v", err)
	}
	runner := New(repository, "isolation-instance", func(context.Context, Execution) (ChildResult, error) {
		return ChildResult{}, fmt.Errorf("terminal child must not be polled")
	}, nil)
	runner.batchSize = 1
	if _, err := runner.RunOnce(t.Context()); err != nil {
		t.Fatalf("poison batch should be isolated: %v", err)
	}
	assertOperationStatus(t, db, poisonID, operation.StatusRunning)
	if _, err := runner.RunOnce(t.Context()); err != nil {
		t.Fatalf("later aggregate: %v", err)
	}
	assertOperationStatus(t, db, validID, operation.StatusSucceeded)
}

func openReconcilerDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("NSP_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("NSP_TEST_POSTGRES_DSN is required")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	db.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(t.Context()); err != nil {
		t.Fatalf("ping postgres: %v", err)
	}
	migration, err := os.ReadFile("../../db/migrations/005_create_operations.sql")
	if err != nil {
		t.Fatalf("read operation migration: %v", err)
	}
	if _, err := db.Exec(string(migration)); err != nil {
		t.Fatalf("apply operation migration: %v", err)
	}
	return db
}

func seedRunningOperation(t *testing.T, db *sql.DB, owner string) string {
	t.Helper()
	service := operation.NewService(operation.NewRepository(db))
	key := uuid.NewString()
	op, decision, err := service.BeginTarget(t.Context(), operation.BeginRequest{
		OwnerService: owner, CallerScope: "test", RouteScope: "POST /test", OperationType: "create_test",
		TargetScope: key, IdempotencyKey: key, Payload: map[string]any{"key": key}, ResourceType: "test", ResourceID: uuid.NewString(), Generation: 1,
	})
	if err != nil || decision != operation.DecisionNew {
		t.Fatalf("begin operation: op=%#v decision=%s err=%v", op, decision, err)
	}
	if changed, err := operation.NewRepository(db).UpdateStatusCAS(t.Context(), op.OperationID, 0, op.OperationType, operation.StatusAccepted, operation.StatusDispatching, "", ""); err != nil || !changed {
		t.Fatalf("advance dispatching: changed=%v err=%v", changed, err)
	}
	if changed, err := operation.NewRepository(db).StoreResponseCAS(t.Context(), op.OperationID, 1, op.OperationType, operation.StatusDispatching, operation.StatusRunning, "0", []byte(`{"code":"0","success":true,"status":"running"}`)); err != nil || !changed {
		t.Fatalf("advance running: changed=%v err=%v", changed, err)
	}
	return op.OperationID
}

func assertOperationStatus(t *testing.T, db *sql.DB, operationID string, want operation.Status) {
	t.Helper()
	var got operation.Status
	if err := db.QueryRow(`SELECT status FROM orchestration_operations WHERE operation_id = $1`, operationID).Scan(&got); err != nil {
		t.Fatalf("load operation status: %v", err)
	}
	if got != want {
		t.Fatalf("operation status = %s, want %s", got, want)
	}
}

func cleanupOperation(t *testing.T, db *sql.DB, operationID string) {
	t.Helper()
	_, _ = db.Exec(`DELETE FROM orchestration_target_claims WHERE operation_id = $1`, operationID)
	_, _ = db.Exec(`DELETE FROM orchestration_operations WHERE operation_id = $1`, operationID)
}
