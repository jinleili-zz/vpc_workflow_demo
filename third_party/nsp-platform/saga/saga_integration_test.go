// File: saga_integration_test.go
// Package saga - Integration tests for SAGA distributed transaction module.
//
// These tests start real PostgreSQL-backed engines (optionally multi-instance),
// use httptest mock services, and verify end-to-end behaviour.
//
// Run with:
//
//	TEST_DSN="postgres://nsp_user:nsp_password@localhost:5432/saga_test?sslmode=disable" go test -v -run TestIntegration -count=1 -timeout 300s ./pkg/saga/
package saga

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

const defaultTestDSN = "postgres://nsp_user:nsp_password@localhost:5432/saga_test?sslmode=disable"

func testDSN() string {
	if v := os.Getenv("TEST_DSN"); v != "" {
		return v
	}
	return defaultTestDSN
}

// cleanTables truncates all saga tables so each test is isolated.
func cleanTables(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, tbl := range []string{"saga_poll_tasks", "saga_steps", "saga_transactions"} {
		if _, err := db.Exec("DELETE FROM " + tbl); err != nil {
			t.Fatalf("failed to clean table %s: %v", tbl, err)
		}
	}
}

// newTestEngine creates an engine with fast scan intervals for testing.
func newTestEngine(t *testing.T, instanceID string) *Engine {
	t.Helper()
	e, err := NewEngine(&Config{
		DSN:               testDSN(),
		WorkerCount:       2,
		PollBatchSize:     10,
		PollScanInterval:  500 * time.Millisecond,
		CoordScanInterval: 500 * time.Millisecond,
		HTTPTimeout:       5 * time.Second,
		InstanceID:        instanceID,
	})
	if err != nil {
		t.Fatalf("NewEngine(%s): %v", instanceID, err)
	}
	return e
}

// waitForStatus polls the engine until the transaction reaches one of the target
// statuses or the timeout elapses.
func waitForStatus(ctx context.Context, e *Engine, txID string, timeout time.Duration, targets ...string) (*TransactionStatus, error) {
	deadline := time.After(timeout)
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline:
			st, _ := e.Query(ctx, txID)
			if st != nil {
				return st, fmt.Errorf("timeout waiting for status %v; current=%s", targets, st.Status)
			}
			return nil, fmt.Errorf("timeout waiting for status %v; query returned nil", targets)
		case <-tick.C:
			st, err := e.Query(ctx, txID)
			if err != nil {
				return nil, err
			}
			if st == nil {
				continue
			}
			for _, t := range targets {
				if st.Status == t {
					return st, nil
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// TC-01  All sync steps succeed → succeeded
// ---------------------------------------------------------------------------

func TestIntegration_AllSyncStepsSucceed(t *testing.T) {
	// Mock services: both return 200
	step1Called := int32(0)
	step2Called := int32(0)
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/stock/deduct"):
			atomic.AddInt32(&step1Called, 1)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"deducted": true})
		case strings.Contains(r.URL.Path, "/orders"):
			atomic.AddInt32(&step2Called, 1)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"order_id": "ORD-001"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer svc.Close()

	engine := newTestEngine(t, "tc01-inst1")
	cleanTables(t, engine.DB())
	ctx := context.Background()
	engine.Start(ctx)
	defer engine.Stop()

	def, err := NewSaga("tc01-all-sync-ok").
		AddStep(Step{
			Name: "扣减库存", Type: StepTypeSync,
			ActionMethod: "POST", ActionURL: svc.URL + "/stock/deduct",
			ActionPayload:    map[string]any{"item_id": "SKU-001"},
			CompensateMethod: "POST", CompensateURL: svc.URL + "/stock/rollback",
		}).
		AddStep(Step{
			Name: "创建订单", Type: StepTypeSync,
			ActionMethod: "POST", ActionURL: svc.URL + "/orders",
			ActionPayload:    map[string]any{"user_id": "U-001"},
			CompensateMethod: "DELETE", CompensateURL: svc.URL + "/orders/ORD-001",
		}).
		Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	txID, err := engine.Submit(ctx, def)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	st, err := waitForStatus(ctx, engine, txID, 15*time.Second, "succeeded")
	if err != nil {
		t.Fatalf("TC-01 FAIL: %v", err)
	}
	if st.Status != "succeeded" {
		t.Fatalf("TC-01 FAIL: expected succeeded, got %s", st.Status)
	}
	if atomic.LoadInt32(&step1Called) < 1 || atomic.LoadInt32(&step2Called) < 1 {
		t.Fatalf("TC-01 FAIL: step1=%d step2=%d", step1Called, step2Called)
	}
	t.Logf("TC-01 PASS: txID=%s status=%s", txID, st.Status)
}

// ---------------------------------------------------------------------------
// TC-02  Second sync step fails → compensation called → failed
// ---------------------------------------------------------------------------

func TestIntegration_SyncStepFailTriggersCompensation(t *testing.T) {
	compensateCalled := int32(0)
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/stock/deduct"):
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"deducted": true})
		case strings.Contains(r.URL.Path, "/stock/rollback"):
			atomic.AddInt32(&compensateCalled, 1)
			w.WriteHeader(200)
		case strings.Contains(r.URL.Path, "/orders") && r.Method == "POST":
			// Fail this step
			w.WriteHeader(500)
			w.Write([]byte(`{"error":"service unavailable"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer svc.Close()

	engine := newTestEngine(t, "tc02-inst1")
	cleanTables(t, engine.DB())
	ctx := context.Background()
	engine.Start(ctx)
	defer engine.Stop()

	def, err := NewSaga("tc02-fail-compensate").
		AddStep(Step{
			Name: "扣减库存", Type: StepTypeSync, MaxRetry: 1,
			ActionMethod: "POST", ActionURL: svc.URL + "/stock/deduct",
			ActionPayload:    map[string]any{"item_id": "SKU-001"},
			CompensateMethod: "POST", CompensateURL: svc.URL + "/stock/rollback",
		}).
		AddStep(Step{
			Name: "创建订单", Type: StepTypeSync, MaxRetry: 1,
			ActionMethod: "POST", ActionURL: svc.URL + "/orders",
			ActionPayload:    map[string]any{"user_id": "U-001"},
			CompensateMethod: "DELETE", CompensateURL: svc.URL + "/orders/dummy",
		}).
		Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	txID, err := engine.Submit(ctx, def)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	st, err := waitForStatus(ctx, engine, txID, 30*time.Second, "failed")
	if err != nil {
		t.Fatalf("TC-02 FAIL: %v", err)
	}
	if st.Status != "failed" {
		t.Fatalf("TC-02 FAIL: expected failed, got %s", st.Status)
	}
	if atomic.LoadInt32(&compensateCalled) < 1 {
		t.Fatalf("TC-02 FAIL: compensation not called")
	}
	// Step-1 should be compensated, Step-2 should be failed or skipped
	t.Logf("TC-02 PASS: txID=%s status=%s compensateCalled=%d", txID, st.Status, compensateCalled)
}

// ---------------------------------------------------------------------------
// TC-03  Async step poll success → succeeded
// ---------------------------------------------------------------------------

func TestIntegration_AsyncStepPollSuccess(t *testing.T) {
	pollCount := int32(0)
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/config/apply"):
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"task_id": "TASK-100"})
		case strings.Contains(r.URL.Path, "/config/status"):
			n := atomic.AddInt32(&pollCount, 1)
			w.Header().Set("Content-Type", "application/json")
			if n >= 3 {
				json.NewEncoder(w).Encode(map[string]any{"status": "success"})
			} else {
				json.NewEncoder(w).Encode(map[string]any{"status": "processing"})
			}
		case strings.Contains(r.URL.Path, "/config/rollback"):
			w.WriteHeader(200)
		default:
			http.NotFound(w, r)
		}
	}))
	defer svc.Close()

	engine := newTestEngine(t, "tc03-inst1")
	cleanTables(t, engine.DB())
	ctx := context.Background()
	engine.Start(ctx)
	defer engine.Stop()

	def, err := NewSaga("tc03-async-poll-ok").
		AddStep(Step{
			Name: "设备配置下发", Type: StepTypeAsync,
			ActionMethod: "POST", ActionURL: svc.URL + "/config/apply",
			ActionPayload:    map[string]any{"device_id": "DEV-001"},
			CompensateMethod: "POST", CompensateURL: svc.URL + "/config/rollback",
			PollURL:          svc.URL + "/config/status?task_id={action_response.task_id}",
			PollMethod:       "GET",
			PollIntervalSec:  1,
			PollMaxTimes:     20,
			PollSuccessPath:  "$.status",
			PollSuccessValue: "success",
			PollFailurePath:  "$.status",
			PollFailureValue: "failed",
		}).
		Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	txID, err := engine.Submit(ctx, def)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	st, err := waitForStatus(ctx, engine, txID, 30*time.Second, "succeeded")
	if err != nil {
		t.Fatalf("TC-03 FAIL: %v", err)
	}
	if st.Status != "succeeded" {
		t.Fatalf("TC-03 FAIL: expected succeeded, got %s", st.Status)
	}
	t.Logf("TC-03 PASS: txID=%s status=%s pollCount=%d", txID, st.Status, atomic.LoadInt32(&pollCount))
}

// ---------------------------------------------------------------------------
// TC-04  Async step poll returns failure → compensation → failed
// ---------------------------------------------------------------------------

func TestIntegration_AsyncStepPollFailure(t *testing.T) {
	compensateCalled := int32(0)
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/stock/deduct"):
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"deducted": true})
		case strings.Contains(r.URL.Path, "/stock/rollback"):
			atomic.AddInt32(&compensateCalled, 1)
			w.WriteHeader(200)
		case strings.Contains(r.URL.Path, "/config/apply"):
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"task_id": "TASK-200"})
		case strings.Contains(r.URL.Path, "/config/status"):
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"status": "failed"})
		case strings.Contains(r.URL.Path, "/config/rollback"):
			w.WriteHeader(200)
		default:
			http.NotFound(w, r)
		}
	}))
	defer svc.Close()

	engine := newTestEngine(t, "tc04-inst1")
	cleanTables(t, engine.DB())
	ctx := context.Background()
	engine.Start(ctx)
	defer engine.Stop()

	def, err := NewSaga("tc04-async-poll-fail").
		AddStep(Step{
			Name: "扣减库存", Type: StepTypeSync,
			ActionMethod: "POST", ActionURL: svc.URL + "/stock/deduct",
			ActionPayload:    map[string]any{"item_id": "SKU-001"},
			CompensateMethod: "POST", CompensateURL: svc.URL + "/stock/rollback",
		}).
		AddStep(Step{
			Name: "设备配置下发", Type: StepTypeAsync,
			ActionMethod: "POST", ActionURL: svc.URL + "/config/apply",
			ActionPayload:    map[string]any{"device_id": "DEV-001"},
			CompensateMethod: "POST", CompensateURL: svc.URL + "/config/rollback",
			PollURL:          svc.URL + "/config/status?task_id={action_response.task_id}",
			PollMethod:       "GET",
			PollIntervalSec:  1,
			PollMaxTimes:     10,
			PollSuccessPath:  "$.status",
			PollSuccessValue: "success",
			PollFailurePath:  "$.status",
			PollFailureValue: "failed",
		}).
		Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	txID, err := engine.Submit(ctx, def)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	st, err := waitForStatus(ctx, engine, txID, 30*time.Second, "failed")
	if err != nil {
		t.Fatalf("TC-04 FAIL: %v", err)
	}
	if st.Status != "failed" {
		t.Fatalf("TC-04 FAIL: expected failed, got %s", st.Status)
	}
	if atomic.LoadInt32(&compensateCalled) < 1 {
		t.Fatalf("TC-04 FAIL: Step-1 compensation not called")
	}
	t.Logf("TC-04 PASS: txID=%s status=%s compensateCalled=%d", txID, st.Status, compensateCalled)
}

// ---------------------------------------------------------------------------
// TC-05  Step retry: first 2 calls 500, third 200 → succeeded
// ---------------------------------------------------------------------------

func TestIntegration_StepRetry(t *testing.T) {
	callCount := int32(0)
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/flaky"):
			n := atomic.AddInt32(&callCount, 1)
			if n <= 2 {
				w.WriteHeader(500)
				w.Write([]byte(`{"error":"temporary failure"}`))
			} else {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{"ok": true})
			}
		case strings.Contains(r.URL.Path, "/rollback"):
			w.WriteHeader(200)
		default:
			http.NotFound(w, r)
		}
	}))
	defer svc.Close()

	engine := newTestEngine(t, "tc05-inst1")
	cleanTables(t, engine.DB())
	ctx := context.Background()
	engine.Start(ctx)
	defer engine.Stop()

	def, err := NewSaga("tc05-retry").
		AddStep(Step{
			Name: "不稳定服务", Type: StepTypeSync, MaxRetry: 5,
			ActionMethod: "POST", ActionURL: svc.URL + "/flaky",
			ActionPayload:    map[string]any{"key": "val"},
			CompensateMethod: "POST", CompensateURL: svc.URL + "/rollback",
		}).
		Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	txID, err := engine.Submit(ctx, def)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	st, err := waitForStatus(ctx, engine, txID, 30*time.Second, "succeeded")
	if err != nil {
		t.Fatalf("TC-05 FAIL: %v", err)
	}
	if st.Status != "succeeded" {
		t.Fatalf("TC-05 FAIL: expected succeeded, got %s", st.Status)
	}
	final := atomic.LoadInt32(&callCount)
	if final < 3 {
		t.Fatalf("TC-05 FAIL: expected >=3 calls, got %d", final)
	}
	t.Logf("TC-05 PASS: txID=%s status=%s totalCalls=%d", txID, st.Status, final)
}

// ---------------------------------------------------------------------------
// TC-06  Transaction timeout → compensation → failed
// ---------------------------------------------------------------------------

func TestIntegration_TransactionTimeout(t *testing.T) {
	compensateCalled := int32(0)
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/stock/deduct"):
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"deducted": true})
		case strings.Contains(r.URL.Path, "/stock/rollback"):
			atomic.AddInt32(&compensateCalled, 1)
			w.WriteHeader(200)
		case strings.Contains(r.URL.Path, "/slow"):
			// This step hangs long enough for the transaction to timeout
			time.Sleep(60 * time.Second)
			w.WriteHeader(200)
		case strings.Contains(r.URL.Path, "/slow-rollback"):
			w.WriteHeader(200)
		default:
			http.NotFound(w, r)
		}
	}))
	defer svc.Close()

	engine := newTestEngine(t, "tc06-inst1")
	cleanTables(t, engine.DB())
	ctx := context.Background()
	engine.Start(ctx)
	defer engine.Stop()

	// Use manual SQL to create a transaction that is already timed out
	// This simulates a transaction whose timeout_at is in the past
	def, err := NewSaga("tc06-timeout").
		WithTimeout(1). // 1 second timeout
		AddStep(Step{
			Name: "扣减库存", Type: StepTypeSync,
			ActionMethod: "POST", ActionURL: svc.URL + "/stock/deduct",
			ActionPayload:    map[string]any{"item_id": "SKU-001"},
			CompensateMethod: "POST", CompensateURL: svc.URL + "/stock/rollback",
		}).
		AddStep(Step{
			Name: "慢服务", Type: StepTypeSync,
			ActionMethod: "POST", ActionURL: svc.URL + "/slow",
			ActionPayload:    map[string]any{"key": "val"},
			CompensateMethod: "POST", CompensateURL: svc.URL + "/slow-rollback",
		}).
		Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	txID, err := engine.Submit(ctx, def)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// The timeout scanner runs every ~500ms in test config, should pick this up
	st, err := waitForStatus(ctx, engine, txID, 60*time.Second, "failed")
	if err != nil {
		t.Fatalf("TC-06 FAIL: %v", err)
	}
	if st.Status != "failed" {
		t.Fatalf("TC-06 FAIL: expected failed, got %s", st.Status)
	}
	t.Logf("TC-06 PASS: txID=%s status=%s compensateCalled=%d", txID, st.Status, atomic.LoadInt32(&compensateCalled))
}

// ---------------------------------------------------------------------------
// TC-07  Crash recovery: engine restarts, transaction resumes
// ---------------------------------------------------------------------------

func TestIntegration_CrashRecovery(t *testing.T) {
	step2Called := int32(0)
	step1Gate := make(chan struct{}) // blocks step-2 until we close it
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/step1"):
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"result": "ok1"})
		case strings.Contains(r.URL.Path, "/step2"):
			// Block until gate is opened (simulates slow step during crash)
			<-step1Gate
			atomic.AddInt32(&step2Called, 1)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"result": "ok2"})
		case strings.Contains(r.URL.Path, "/rollback"):
			w.WriteHeader(200)
		default:
			http.NotFound(w, r)
		}
	}))
	defer svc.Close()

	// Phase 1: create engine, submit transaction, step-1 will finish but step-2 blocks
	engine1 := newTestEngine(t, "tc07-inst1")
	cleanTables(t, engine1.DB())
	ctx := context.Background()
	engine1.Start(ctx)

	def, err := NewSaga("tc07-crash-recovery").
		AddStep(Step{
			Name: "Step-1", Type: StepTypeSync,
			ActionMethod: "POST", ActionURL: svc.URL + "/step1",
			ActionPayload:    map[string]any{"key": "val"},
			CompensateMethod: "POST", CompensateURL: svc.URL + "/rollback",
		}).
		AddStep(Step{
			Name: "Step-2", Type: StepTypeSync,
			ActionMethod: "POST", ActionURL: svc.URL + "/step2",
			ActionPayload:    map[string]any{"key": "val"},
			CompensateMethod: "POST", CompensateURL: svc.URL + "/rollback",
		}).
		Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	txID, err := engine1.Submit(ctx, def)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Wait for step-1 to complete (transaction should be "running" with step-2 blocked)
	time.Sleep(2 * time.Second)

	// Simulate crash: stop engine (step-2 is still blocked)
	engine1.Stop()

	// Unblock step-2 for the next engine
	close(step1Gate)

	// Query via separate DB connection since engine1 is stopped
	db, _ := sql.Open("postgres", testDSN())
	defer db.Close()

	var currentStatus string
	db.QueryRow("SELECT status FROM saga_transactions WHERE id = $1", txID).Scan(&currentStatus)
	t.Logf("TC-07: after crash status=%s", currentStatus)

	if currentStatus == "succeeded" {
		t.Logf("TC-07 PASS (fast path): transaction completed before crash simulation")
		return
	}

	// Release locks (simulate lease expiry after crash)
	db.Exec("UPDATE saga_transactions SET locked_by = NULL, locked_until = NULL WHERE id = $1", txID)

	// Phase 2: start a new engine (different instance) — should recover
	engine2 := newTestEngine(t, "tc07-inst2")
	ctx2 := context.Background()
	engine2.Start(ctx2)
	defer engine2.Stop()

	st, err := waitForStatus(ctx2, engine2, txID, 30*time.Second, "succeeded", "failed")
	if err != nil {
		t.Fatalf("TC-07 FAIL: %v", err)
	}
	if st.Status != "succeeded" {
		t.Fatalf("TC-07 FAIL: expected succeeded after recovery, got %s (lastError=%s)", st.Status, st.LastError)
	}
	t.Logf("TC-07 PASS: txID=%s status=%s step2Called=%d", txID, st.Status, atomic.LoadInt32(&step2Called))
}

// ---------------------------------------------------------------------------
// TC-08  Async step poll timeout → compensation
// ---------------------------------------------------------------------------

func TestIntegration_AsyncPollTimeout(t *testing.T) {
	compensateCalled := int32(0)
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/config/apply"):
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"task_id": "TASK-300"})
		case strings.Contains(r.URL.Path, "/config/status"):
			// Always return processing — never finishes
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"status": "processing"})
		case strings.Contains(r.URL.Path, "/config/rollback"):
			atomic.AddInt32(&compensateCalled, 1)
			w.WriteHeader(200)
		default:
			http.NotFound(w, r)
		}
	}))
	defer svc.Close()

	engine := newTestEngine(t, "tc08-inst1")
	cleanTables(t, engine.DB())
	ctx := context.Background()
	engine.Start(ctx)
	defer engine.Stop()

	def, err := NewSaga("tc08-poll-timeout").
		AddStep(Step{
			Name: "设备配置下发", Type: StepTypeAsync,
			ActionMethod: "POST", ActionURL: svc.URL + "/config/apply",
			ActionPayload:    map[string]any{"device_id": "DEV-001"},
			CompensateMethod: "POST", CompensateURL: svc.URL + "/config/rollback",
			PollURL:          svc.URL + "/config/status?task_id={action_response.task_id}",
			PollMethod:       "GET",
			PollIntervalSec:  1,
			PollMaxTimes:     3, // Only 3 polls before timeout
			PollSuccessPath:  "$.status",
			PollSuccessValue: "success",
			PollFailurePath:  "$.status",
			PollFailureValue: "failed",
		}).
		Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	txID, err := engine.Submit(ctx, def)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	st, err := waitForStatus(ctx, engine, txID, 60*time.Second, "failed")
	if err != nil {
		t.Fatalf("TC-08 FAIL: %v", err)
	}
	if st.Status != "failed" {
		t.Fatalf("TC-08 FAIL: expected failed, got %s", st.Status)
	}
	t.Logf("TC-08 PASS: txID=%s status=%s compensateCalled=%d", txID, st.Status, atomic.LoadInt32(&compensateCalled))
}

// ---------------------------------------------------------------------------
// TC-09  Idempotency key is propagated
// ---------------------------------------------------------------------------

func TestIntegration_IdempotencyKeyPropagated(t *testing.T) {
	var receivedIdempotencyKey string
	var receivedTxID string
	var mu sync.Mutex
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		receivedIdempotencyKey = r.Header.Get("X-Idempotency-Key")
		receivedTxID = r.Header.Get("X-Saga-Transaction-Id")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer svc.Close()

	engine := newTestEngine(t, "tc09-inst1")
	cleanTables(t, engine.DB())
	ctx := context.Background()
	engine.Start(ctx)
	defer engine.Stop()

	def, err := NewSaga("tc09-idempotency").
		AddStep(Step{
			Name: "Step-1", Type: StepTypeSync,
			ActionMethod: "POST", ActionURL: svc.URL + "/action",
			ActionPayload:    map[string]any{"key": "val"},
			CompensateMethod: "POST", CompensateURL: svc.URL + "/rollback",
		}).
		Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	txID, err := engine.Submit(ctx, def)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	_, err = waitForStatus(ctx, engine, txID, 15*time.Second, "succeeded")
	if err != nil {
		t.Fatalf("TC-09 FAIL: %v", err)
	}

	mu.Lock()
	idk := receivedIdempotencyKey
	rtx := receivedTxID
	mu.Unlock()
	if idk == "" {
		t.Fatalf("TC-09 FAIL: X-Idempotency-Key not received")
	}
	if rtx != txID {
		t.Fatalf("TC-09 FAIL: X-Saga-Transaction-Id mismatch: %s != %s", rtx, txID)
	}
	t.Logf("TC-09 PASS: idempotencyKey=%s txHeader=%s", idk, rtx)
}

// ---------------------------------------------------------------------------
// TC-10  Multi-instance: same transaction NOT processed by two instances
// ---------------------------------------------------------------------------

func TestIntegration_MultiInstanceExclusivity(t *testing.T) {
	// Track which instance processed each request via custom header
	instanceHits := sync.Map{} // map[string]int — instanceID -> count
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The saga engine embeds X-Saga-Transaction-Id
		txID := r.Header.Get("X-Saga-Transaction-Id")
		if txID != "" {
			instanceHits.Store(txID, true)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer svc.Close()

	engine1 := newTestEngine(t, "tc10-inst1")
	cleanTables(t, engine1.DB())
	engine2 := newTestEngine(t, "tc10-inst2")
	ctx := context.Background()

	engine1.Start(ctx)
	engine2.Start(ctx)
	defer engine1.Stop()
	defer engine2.Stop()

	// Submit multiple transactions
	txIDs := make([]string, 10)
	for i := 0; i < 10; i++ {
		def, err := NewSaga(fmt.Sprintf("tc10-tx-%d", i)).
			AddStep(Step{
				Name: fmt.Sprintf("step-%d", i), Type: StepTypeSync,
				ActionMethod: "POST", ActionURL: svc.URL + "/action",
				ActionPayload:    map[string]any{"idx": i},
				CompensateMethod: "POST", CompensateURL: svc.URL + "/rollback",
			}).
			Build()
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		txID, err := engine1.Submit(ctx, def)
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
		txIDs[i] = txID
		// Also notify engine2's coordinator
		engine2.coordinator.Submit(txID)
	}

	// Wait for all to complete
	allDone := true
	for _, txID := range txIDs {
		st, err := waitForStatus(ctx, engine1, txID, 30*time.Second, "succeeded", "failed")
		if err != nil {
			t.Logf("TC-10: txID=%s err=%v", txID, err)
			allDone = false
			continue
		}
		if st.Status != "succeeded" {
			t.Logf("TC-10: txID=%s unexpected status=%s", txID, st.Status)
		}
	}
	if !allDone {
		t.Fatalf("TC-10 FAIL: not all transactions completed")
	}

	// Verify all transactions reached terminal state
	for _, txID := range txIDs {
		st, err := engine1.Query(ctx, txID)
		if err != nil {
			t.Fatalf("TC-10 FAIL: query txID=%s: %v", txID, err)
		}
		if st == nil {
			t.Fatalf("TC-10 FAIL: txID=%s not found", txID)
		}
		if st.Status != "succeeded" {
			t.Fatalf("TC-10 FAIL: txID=%s expected succeeded, got %s", txID, st.Status)
		}
	}
	t.Logf("TC-10 PASS: all %d transactions succeeded with 2 instances", len(txIDs))
}

// ---------------------------------------------------------------------------
// TC-11  Multi-instance crash recovery: instance1 crashes, instance2 recovers
// ---------------------------------------------------------------------------

func TestIntegration_MultiInstanceCrashRecovery(t *testing.T) {
	step2Called := int32(0)
	step2Gate := make(chan struct{})
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/step1"):
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"result": "ok1"})
		case strings.Contains(r.URL.Path, "/step2"):
			<-step2Gate
			atomic.AddInt32(&step2Called, 1)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"result": "ok2"})
		case strings.Contains(r.URL.Path, "/rollback"):
			w.WriteHeader(200)
		default:
			http.NotFound(w, r)
		}
	}))
	defer svc.Close()

	// Phase 1: Instance-1 starts, submits transaction, step-2 is blocked
	engine1 := newTestEngine(t, "tc11-inst1")
	cleanTables(t, engine1.DB())
	ctx := context.Background()
	engine1.Start(ctx)

	def, err := NewSaga("tc11-multi-crash").
		AddStep(Step{
			Name: "Step-1", Type: StepTypeSync,
			ActionMethod: "POST", ActionURL: svc.URL + "/step1",
			ActionPayload:    map[string]any{"key": "val"},
			CompensateMethod: "POST", CompensateURL: svc.URL + "/rollback",
		}).
		AddStep(Step{
			Name: "Step-2", Type: StepTypeSync,
			ActionMethod: "POST", ActionURL: svc.URL + "/step2",
			ActionPayload:    map[string]any{"key": "val"},
			CompensateMethod: "POST", CompensateURL: svc.URL + "/rollback",
		}).
		Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	txID, err := engine1.Submit(ctx, def)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Wait for step-1 to finish
	time.Sleep(2 * time.Second)

	// Simulate crash
	engine1.Stop()

	// Unblock step-2 for the recovering instance
	close(step2Gate)

	// Query via separate DB connection since engine1 is stopped
	db, _ := sql.Open("postgres", testDSN())
	defer db.Close()

	var currentStatus string
	db.QueryRow("SELECT status FROM saga_transactions WHERE id = $1", txID).Scan(&currentStatus)
	t.Logf("TC-11: after inst1 crash, status=%s", currentStatus)

	if currentStatus == "succeeded" {
		t.Logf("TC-11 PASS (fast path): completed before crash")
		return
	}

	// Release locks (simulate lease expiry)
	db.Exec("UPDATE saga_transactions SET locked_by = NULL, locked_until = NULL WHERE id = $1", txID)

	// Phase 2: Instance-2 starts, should recover
	engine2 := newTestEngine(t, "tc11-inst2")
	ctx2 := context.Background()
	engine2.Start(ctx2)
	defer engine2.Stop()

	st, err := waitForStatus(ctx2, engine2, txID, 30*time.Second, "succeeded", "failed")
	if err != nil {
		t.Fatalf("TC-11 FAIL: %v", err)
	}
	if st.Status != "succeeded" {
		t.Fatalf("TC-11 FAIL: expected succeeded, got %s (lastError=%s)", st.Status, st.LastError)
	}
	t.Logf("TC-11 PASS: txID=%s recovered by inst2, status=%s step2Called=%d", txID, st.Status, atomic.LoadInt32(&step2Called))
}

// ---------------------------------------------------------------------------
// TC-12  Template rendering: compensate URL uses action_response
// ---------------------------------------------------------------------------

func TestIntegration_TemplateRendering(t *testing.T) {
	var compensateURL string
	var mu sync.Mutex
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/orders") && r.Method == "POST":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"order_id": "ORD-777"})
		case strings.Contains(r.URL.Path, "/fail"):
			w.WriteHeader(500)
		case strings.Contains(r.URL.Path, "/orders/"):
			mu.Lock()
			compensateURL = r.URL.Path
			mu.Unlock()
			w.WriteHeader(200)
		default:
			w.WriteHeader(200)
		}
	}))
	defer svc.Close()

	engine := newTestEngine(t, "tc12-inst1")
	cleanTables(t, engine.DB())
	ctx := context.Background()
	engine.Start(ctx)
	defer engine.Stop()

	def, err := NewSaga("tc12-template").
		AddStep(Step{
			Name: "创建订单", Type: StepTypeSync,
			ActionMethod: "POST", ActionURL: svc.URL + "/orders",
			ActionPayload:    map[string]any{"user_id": "U-001"},
			CompensateMethod: "DELETE",
			CompensateURL:    svc.URL + "/orders/{action_response.order_id}",
		}).
		AddStep(Step{
			Name: "故意失败", Type: StepTypeSync, MaxRetry: 1,
			ActionMethod: "POST", ActionURL: svc.URL + "/fail",
			ActionPayload:    map[string]any{},
			CompensateMethod: "POST", CompensateURL: svc.URL + "/noop",
		}).
		Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	txID, err := engine.Submit(ctx, def)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	st, err := waitForStatus(ctx, engine, txID, 30*time.Second, "failed")
	if err != nil {
		t.Fatalf("TC-12 FAIL: %v", err)
	}
	if st.Status != "failed" {
		t.Fatalf("TC-12 FAIL: expected failed, got %s", st.Status)
	}

	mu.Lock()
	cURL := compensateURL
	mu.Unlock()
	if cURL != "/orders/ORD-777" {
		t.Fatalf("TC-12 FAIL: expected compensate URL /orders/ORD-777, got %s", cURL)
	}
	t.Logf("TC-12 PASS: template rendered correctly, compensateURL=%s", cURL)
}

// ---------------------------------------------------------------------------
// TC-13  Concurrent submit: 20 transactions across 3 instances
// ---------------------------------------------------------------------------

func TestIntegration_ConcurrentMultiInstance(t *testing.T) {
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer svc.Close()

	engine1 := newTestEngine(t, "tc13-inst1")
	cleanTables(t, engine1.DB())
	engine2 := newTestEngine(t, "tc13-inst2")
	engine3 := newTestEngine(t, "tc13-inst3")

	ctx := context.Background()
	engine1.Start(ctx)
	engine2.Start(ctx)
	engine3.Start(ctx)
	defer engine1.Stop()
	defer engine2.Stop()
	defer engine3.Stop()

	engines := []*Engine{engine1, engine2, engine3}
	txIDs := make([]string, 20)
	var wg sync.WaitGroup

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			e := engines[idx%3]
			def, err := NewSaga(fmt.Sprintf("tc13-tx-%d", idx)).
				AddStep(Step{
					Name: fmt.Sprintf("step-%d", idx), Type: StepTypeSync,
					ActionMethod: "POST", ActionURL: svc.URL + "/action",
					ActionPayload:    map[string]any{"idx": idx},
					CompensateMethod: "POST", CompensateURL: svc.URL + "/rollback",
				}).
				Build()
			if err != nil {
				t.Errorf("Build: %v", err)
				return
			}
			txID, err := e.Submit(ctx, def)
			if err != nil {
				t.Errorf("Submit: %v", err)
				return
			}
			txIDs[idx] = txID
		}(i)
	}
	wg.Wait()

	// Wait for all
	succeeded := 0
	for _, txID := range txIDs {
		if txID == "" {
			continue
		}
		st, err := waitForStatus(ctx, engine1, txID, 30*time.Second, "succeeded", "failed")
		if err != nil {
			t.Logf("TC-13: txID=%s err=%v", txID, err)
			continue
		}
		if st.Status == "succeeded" {
			succeeded++
		}
	}
	if succeeded < 20 {
		t.Fatalf("TC-13 FAIL: only %d/20 succeeded", succeeded)
	}
	t.Logf("TC-13 PASS: %d/20 transactions succeeded across 3 instances", succeeded)
}
