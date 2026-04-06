// File: saga_test.go
// Package saga - Integration tests for SAGA transactions

package saga

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

// Test DSN from environment variable
func getTestDSN() string {
	dsn := os.Getenv("TEST_DSN")
	if dsn == "" {
		dsn = "postgres://postgres:postgres@localhost:5432/saga_test?sslmode=disable"
	}
	return dsn
}

// setupTestDB creates a test database connection and cleans up tables
func setupTestDB(t *testing.T) *sql.DB {
	dsn := getTestDSN()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Skipf("Failed to connect to test database: %v", err)
	}

	if err := db.Ping(); err != nil {
		t.Skipf("Failed to ping test database: %v", err)
	}

	// Clean up tables
	cleanupTables(t, db)

	return db
}

// cleanupTables removes all test data
func cleanupTables(t *testing.T, db *sql.DB) {
	tables := []string{"saga_poll_tasks", "saga_steps", "saga_transactions"}
	for _, table := range tables {
		_, err := db.Exec(fmt.Sprintf("DELETE FROM %s", table))
		if err != nil {
			// Table might not exist, ignore
			t.Logf("Warning: could not clean table %s: %v", table, err)
		}
	}
}

// TestTemplateRendering tests the template rendering functionality
func TestTemplateRendering(t *testing.T) {
	data := map[string]any{
		"action_response": map[string]any{
			"order_id": "ORD-123",
			"status":   "created",
		},
		"transaction": map[string]any{
			"payload": map[string]any{
				"user_id": "U-001",
				"amount":  100,
			},
		},
		"step": []any{
			map[string]any{
				"action_response": map[string]any{
					"item_id": "ITEM-001",
				},
			},
			map[string]any{
				"action_response": map[string]any{
					"item_id": "ITEM-002",
				},
			},
		},
	}

	tests := []struct {
		name     string
		template string
		expected string
		wantErr  bool
	}{
		{
			name:     "action_response field",
			template: "http://service/orders/{action_response.order_id}",
			expected: "http://service/orders/ORD-123",
		},
		{
			name:     "transaction payload field",
			template: "http://service/users/{transaction.payload.user_id}",
			expected: "http://service/users/U-001",
		},
		{
			name:     "step array access",
			template: "http://service/items/{step[0].action_response.item_id}",
			expected: "http://service/items/ITEM-001",
		},
		{
			name:     "step array second element",
			template: "http://service/items/{step[1].action_response.item_id}",
			expected: "http://service/items/ITEM-002",
		},
		{
			name:     "missing field",
			template: "http://service/{action_response.missing}",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := RenderTemplate(tt.template, data)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

// TestJSONPathExtraction tests the JSONPath extraction functionality
func TestJSONPathExtraction(t *testing.T) {
	data := map[string]any{
		"status": "success",
		"result": map[string]any{
			"code":    200,
			"message": "OK",
		},
		"items": []any{
			map[string]any{"status": "active"},
			map[string]any{"status": "inactive"},
		},
	}

	tests := []struct {
		name     string
		path     string
		expected string
		wantErr  bool
	}{
		{
			name:     "top-level field",
			path:     "$.status",
			expected: "success",
		},
		{
			name:     "nested field",
			path:     "$.result.code",
			expected: "200",
		},
		{
			name:     "nested string field",
			path:     "$.result.message",
			expected: "OK",
		},
		{
			name:     "array index",
			path:     "$.items[0].status",
			expected: "active",
		},
		{
			name:     "array index second element",
			path:     "$.items[1].status",
			expected: "inactive",
		},
		{
			name:    "missing field",
			path:    "$.missing",
			wantErr: true,
		},
		{
			name:    "invalid array index",
			path:    "$.items[99].status",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ExtractByPath(data, tt.path)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

// TestSagaBuilder tests the SAGA builder functionality
func TestSagaBuilder(t *testing.T) {
	t.Run("valid sync saga", func(t *testing.T) {
		def, err := NewSaga("test-saga").
			AddStep(Step{
				Name:             "Step 1",
				Type:             StepTypeSync,
				ActionMethod:     "POST",
				ActionURL:        "http://service/action",
				CompensateMethod: "POST",
				CompensateURL:    "http://service/compensate",
			}).
			Build()

		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if def.Name != "test-saga" {
			t.Errorf("expected name 'test-saga', got %q", def.Name)
		}
		if len(def.Steps) != 1 {
			t.Errorf("expected 1 step, got %d", len(def.Steps))
		}
	})

	t.Run("no steps", func(t *testing.T) {
		_, err := NewSaga("empty-saga").Build()
		if err != ErrNoSteps {
			t.Errorf("expected ErrNoSteps, got %v", err)
		}
	})

	t.Run("async step missing poll config", func(t *testing.T) {
		_, err := NewSaga("async-saga").
			AddStep(Step{
				Name:             "Async Step",
				Type:             StepTypeAsync,
				ActionMethod:     "POST",
				ActionURL:        "http://service/action",
				CompensateMethod: "POST",
				CompensateURL:    "http://service/compensate",
				// Missing PollURL, PollSuccessPath, PollSuccessValue
			}).
			Build()

		if err != ErrAsyncStepMissingPollConfig {
			t.Errorf("expected ErrAsyncStepMissingPollConfig, got %v", err)
		}
	})

	t.Run("valid async saga", func(t *testing.T) {
		def, err := NewSaga("async-saga").
			AddStep(Step{
				Name:             "Async Step",
				Type:             StepTypeAsync,
				ActionMethod:     "POST",
				ActionURL:        "http://service/action",
				CompensateMethod: "POST",
				CompensateURL:    "http://service/compensate",
				PollURL:          "http://service/status",
				PollSuccessPath:  "$.status",
				PollSuccessValue: "done",
			}).
			Build()

		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if def.Steps[0].PollMethod != "GET" {
			t.Errorf("expected default poll method 'GET', got %q", def.Steps[0].PollMethod)
		}
	})
}

// TestMatchPollResult tests the poll result matching functionality
func TestMatchPollResult(t *testing.T) {
	step := &Step{
		PollSuccessPath:  "$.status",
		PollSuccessValue: "success",
		PollFailurePath:  "$.status",
		PollFailureValue: "failed",
	}

	tests := []struct {
		name        string
		response    map[string]any
		wantSuccess bool
		wantFailure bool
	}{
		{
			name:        "success match",
			response:    map[string]any{"status": "success"},
			wantSuccess: true,
			wantFailure: false,
		},
		{
			name:        "failure match",
			response:    map[string]any{"status": "failed"},
			wantSuccess: false,
			wantFailure: true,
		},
		{
			name:        "pending",
			response:    map[string]any{"status": "pending"},
			wantSuccess: false,
			wantFailure: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			success, failure, err := MatchPollResult(tt.response, step)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if success != tt.wantSuccess {
				t.Errorf("expected success=%v, got %v", tt.wantSuccess, success)
			}
			if failure != tt.wantFailure {
				t.Errorf("expected failure=%v, got %v", tt.wantFailure, failure)
			}
		})
	}
}

// Integration tests require a PostgreSQL database

// TestFullSyncTransaction tests a complete sync transaction flow
func TestFullSyncTransaction(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Create mock server
	var step1Called, step2Called, comp1Called atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/step1/action":
			step1Called.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"step1_id": "S1-001"})
		case "/step2/action":
			step2Called.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"step2_id": "S2-001"})
		case "/step1/compensate":
			comp1Called.Add(1)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	// Create engine
	engine, err := NewEngine(&Config{
		DSN:              getTestDSN(),
		WorkerCount:      2,
		PollScanInterval: 100 * time.Millisecond,
		HTTPTimeout:      5 * time.Second,
	})
	if err != nil {
		t.Skipf("Failed to create engine: %v", err)
	}

	ctx := context.Background()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Failed to start engine: %v", err)
	}
	defer engine.Stop()

	// Build and submit saga
	def, err := NewSaga("test-sync").
		AddStep(Step{
			Name:             "Step 1",
			Type:             StepTypeSync,
			ActionMethod:     "POST",
			ActionURL:        server.URL + "/step1/action",
			CompensateMethod: "POST",
			CompensateURL:    server.URL + "/step1/compensate",
		}).
		AddStep(Step{
			Name:             "Step 2",
			Type:             StepTypeSync,
			ActionMethod:     "POST",
			ActionURL:        server.URL + "/step2/action",
			CompensateMethod: "POST",
			CompensateURL:    server.URL + "/step2/compensate",
		}).
		Build()

	if err != nil {
		t.Fatalf("Failed to build saga: %v", err)
	}

	txID, err := engine.Submit(ctx, def)
	if err != nil {
		t.Fatalf("Failed to submit saga: %v", err)
	}

	// Wait for completion
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		status, err := engine.Query(ctx, txID)
		if err != nil {
			t.Fatalf("Failed to query status: %v", err)
		}
		if status.Status == string(TxStatusSucceeded) {
			// Verify steps were called
			if step1Called.Load() < 1 {
				t.Error("step1 was not called")
			}
			if step2Called.Load() < 1 {
				t.Error("step2 was not called")
			}
			if comp1Called.Load() > 0 {
				t.Error("compensation should not have been called")
			}
			return
		}
		if status.Status == string(TxStatusFailed) {
			t.Fatalf("Transaction failed: %s", status.LastError)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("Transaction did not complete in time")
}

// TestSyncStepFailureCompensation tests compensation when a sync step fails
func TestSyncStepFailureCompensation(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	var step1Called, step2Called, comp1Called atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/step1/action":
			step1Called.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"step1_id": "S1-001"})
		case "/step2/action":
			step2Called.Add(1)
			// Return error
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("step2 failed"))
		case "/step1/compensate":
			comp1Called.Add(1)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	engine, err := NewEngine(&Config{
		DSN:              getTestDSN(),
		WorkerCount:      2,
		PollScanInterval: 100 * time.Millisecond,
		HTTPTimeout:      5 * time.Second,
	})
	if err != nil {
		t.Skipf("Failed to create engine: %v", err)
	}

	ctx := context.Background()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Failed to start engine: %v", err)
	}
	defer engine.Stop()

	def, err := NewSaga("test-compensation").
		AddStep(Step{
			Name:             "Step 1",
			Type:             StepTypeSync,
			ActionMethod:     "POST",
			ActionURL:        server.URL + "/step1/action",
			CompensateMethod: "POST",
			CompensateURL:    server.URL + "/step1/compensate",
			MaxRetry:         1, // Fail fast
		}).
		AddStep(Step{
			Name:             "Step 2",
			Type:             StepTypeSync,
			ActionMethod:     "POST",
			ActionURL:        server.URL + "/step2/action",
			CompensateMethod: "POST",
			CompensateURL:    server.URL + "/step2/compensate",
			MaxRetry:         1,
		}).
		Build()

	if err != nil {
		t.Fatalf("Failed to build saga: %v", err)
	}

	txID, err := engine.Submit(ctx, def)
	if err != nil {
		t.Fatalf("Failed to submit saga: %v", err)
	}

	// Wait for completion
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		status, err := engine.Query(ctx, txID)
		if err != nil {
			t.Fatalf("Failed to query status: %v", err)
		}
		if status.Status == string(TxStatusFailed) {
			// Verify compensation was called
			if step1Called.Load() < 1 {
				t.Error("step1 was not called")
			}
			if comp1Called.Load() < 1 {
				t.Error("step1 compensation was not called")
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("Transaction did not complete in time")
}

// TestAsyncStepPollingSuccess tests async step with polling
func TestAsyncStepPollingSuccess(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	var actionCalled, pollCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/async/action":
			actionCalled.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"task_id": "TASK-001"})
		case "/async/status":
			count := pollCount.Add(1)
			if count >= 3 {
				// Return success after 3 polls
				json.NewEncoder(w).Encode(map[string]any{"status": "success"})
			} else {
				json.NewEncoder(w).Encode(map[string]any{"status": "pending"})
			}
		case "/async/compensate":
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	engine, err := NewEngine(&Config{
		DSN:              getTestDSN(),
		WorkerCount:      2,
		PollScanInterval: 100 * time.Millisecond,
		HTTPTimeout:      5 * time.Second,
	})
	if err != nil {
		t.Skipf("Failed to create engine: %v", err)
	}

	ctx := context.Background()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Failed to start engine: %v", err)
	}
	defer engine.Stop()

	def, err := NewSaga("test-async").
		AddStep(Step{
			Name:             "Async Step",
			Type:             StepTypeAsync,
			ActionMethod:     "POST",
			ActionURL:        server.URL + "/async/action",
			CompensateMethod: "POST",
			CompensateURL:    server.URL + "/async/compensate",
			PollURL:          server.URL + "/async/status?task_id={action_response.task_id}",
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
		t.Fatalf("Failed to build saga: %v", err)
	}

	txID, err := engine.Submit(ctx, def)
	if err != nil {
		t.Fatalf("Failed to submit saga: %v", err)
	}

	// Wait for completion
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		status, err := engine.Query(ctx, txID)
		if err != nil {
			t.Fatalf("Failed to query status: %v", err)
		}
		if status == nil {
			t.Log("Query returned nil status")
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if status.Status == string(TxStatusSucceeded) {
			if actionCalled.Load() < 1 {
				t.Error("action was not called")
			}
			if pollCount.Load() < 3 {
				t.Errorf("expected at least 3 polls, got %d", pollCount.Load())
			}
			return
		}
		if status.Status == string(TxStatusFailed) {
			t.Fatalf("Transaction failed: %s", status.LastError)
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("Transaction did not complete in time")
}

// TestAsyncStepPollingFailure tests async step polling failure
func TestAsyncStepPollingFailure(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	var actionCalled, compCalled, pollCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/async/action":
			actionCalled.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"task_id": "TASK-001"})
		case "/async/status":
			count := pollCount.Add(1)
			if count >= 2 {
				// Return failure
				json.NewEncoder(w).Encode(map[string]any{"status": "failed"})
			} else {
				json.NewEncoder(w).Encode(map[string]any{"status": "pending"})
			}
		case "/async/compensate":
			compCalled.Add(1)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	engine, err := NewEngine(&Config{
		DSN:              getTestDSN(),
		WorkerCount:      2,
		PollScanInterval: 100 * time.Millisecond,
		HTTPTimeout:      5 * time.Second,
	})
	if err != nil {
		t.Skipf("Failed to create engine: %v", err)
	}

	ctx := context.Background()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Failed to start engine: %v", err)
	}
	defer engine.Stop()

	def, err := NewSaga("test-async-fail").
		AddStep(Step{
			Name:             "Async Step",
			Type:             StepTypeAsync,
			ActionMethod:     "POST",
			ActionURL:        server.URL + "/async/action",
			CompensateMethod: "POST",
			CompensateURL:    server.URL + "/async/compensate",
			PollURL:          server.URL + "/async/status",
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
		t.Fatalf("Failed to build saga: %v", err)
	}

	txID, err := engine.Submit(ctx, def)
	if err != nil {
		t.Fatalf("Failed to submit saga: %v", err)
	}

	// Wait for completion
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		status, err := engine.Query(ctx, txID)
		if err != nil {
			t.Fatalf("Failed to query status: %v", err)
		}
		if status.Status == string(TxStatusFailed) {
			// Transaction should fail after polling returns failure
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("Transaction did not complete in time")
}

// TestStepRetry tests step retry functionality
func TestStepRetry(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	var callCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/retry/action":
			count := callCount.Add(1)
			if count < 3 {
				// Fail first two times
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte("temporary error"))
			} else {
				// Succeed on third try
				json.NewEncoder(w).Encode(map[string]any{"result": "ok"})
			}
		case "/retry/compensate":
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	engine, err := NewEngine(&Config{
		DSN:              getTestDSN(),
		WorkerCount:      2,
		PollScanInterval: 100 * time.Millisecond,
		HTTPTimeout:      5 * time.Second,
	})
	if err != nil {
		t.Skipf("Failed to create engine: %v", err)
	}

	ctx := context.Background()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Failed to start engine: %v", err)
	}
	defer engine.Stop()

	def, err := NewSaga("test-retry").
		AddStep(Step{
			Name:             "Retry Step",
			Type:             StepTypeSync,
			ActionMethod:     "POST",
			ActionURL:        server.URL + "/retry/action",
			CompensateMethod: "POST",
			CompensateURL:    server.URL + "/retry/compensate",
			MaxRetry:         5, // Allow enough retries
		}).
		Build()

	if err != nil {
		t.Fatalf("Failed to build saga: %v", err)
	}

	txID, err := engine.Submit(ctx, def)
	if err != nil {
		t.Fatalf("Failed to submit saga: %v", err)
	}

	// Wait for completion
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		status, err := engine.Query(ctx, txID)
		if err != nil {
			t.Fatalf("Failed to query status: %v", err)
		}
		if status.Status == string(TxStatusSucceeded) {
			// Verify retry count
			if callCount.Load() < 3 {
				t.Errorf("expected at least 3 calls, got %d", callCount.Load())
			}
			return
		}
		if status.Status == string(TxStatusFailed) {
			t.Fatalf("Transaction failed (should have succeeded after retries): %s", status.LastError)
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("Transaction did not complete in time")
}

// TestPollerConcurrency tests concurrent poll task processing
func TestPollerConcurrency(t *testing.T) {
	// This is a unit test that doesn't require database
	store := &mockStore{}
	executor := &Executor{
		client: &http.Client{Timeout: 5 * time.Second},
		store:  store,
	}

	poller := NewPoller(store, executor, &PollerConfig{
		ScanInterval: 50 * time.Millisecond,
		BatchSize:    10,
		InstanceID:   "test-instance",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	poller.Start(ctx)
	defer poller.Stop()

	// Register notification channels
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		txID := fmt.Sprintf("tx-%d", i)
		ch := poller.RegisterNotify(txID)
		wg.Add(1)
		go func(id string, c chan PollResult) {
			defer wg.Done()
			select {
			case <-c:
			case <-ctx.Done():
			}
			poller.UnregisterNotify(id)
		}(txID, ch)
	}

	// Wait a bit for processing
	time.Sleep(500 * time.Millisecond)

	// Cancel and wait for goroutines
	cancel()
	wg.Wait()
}

// mockStore is a mock implementation of Store for unit tests
type mockStore struct {
	mu sync.RWMutex
}

func (s *mockStore) CreateTransaction(ctx context.Context, tx *Transaction) error {
	return nil
}

func (s *mockStore) CreateTransactionWithSteps(ctx context.Context, tx *Transaction, steps []*Step) error {
	return nil
}

func (s *mockStore) GetTransaction(ctx context.Context, id string) (*Transaction, error) {
	return nil, nil
}

func (s *mockStore) UpdateTransactionStatus(ctx context.Context, id string, status TxStatus, lastError string) error {
	return nil
}

func (s *mockStore) UpdateTransactionStep(ctx context.Context, id string, currentStep int) error {
	return nil
}

func (s *mockStore) CreateSteps(ctx context.Context, steps []*Step) error {
	return nil
}

func (s *mockStore) GetSteps(ctx context.Context, txID string) ([]*Step, error) {
	return nil, nil
}

func (s *mockStore) GetStep(ctx context.Context, stepID string) (*Step, error) {
	return nil, nil
}

func (s *mockStore) UpdateStepStatus(ctx context.Context, stepID string, status StepStatus, lastError string) error {
	return nil
}

func (s *mockStore) UpdateStepResponse(ctx context.Context, stepID string, response map[string]any) error {
	return nil
}

func (s *mockStore) IncrementStepRetry(ctx context.Context, stepID string) error {
	return nil
}

func (s *mockStore) IncrementStepPollCount(ctx context.Context, stepID string, nextPollAt time.Time) error {
	return nil
}

func (s *mockStore) CreatePollTask(ctx context.Context, task *PollTask) error {
	return nil
}

func (s *mockStore) DeletePollTask(ctx context.Context, stepID string) error {
	return nil
}

func (s *mockStore) ListRecoverableTransactions(ctx context.Context, instanceID string, batchSize int, leaseDuration time.Duration) ([]*Transaction, error) {
	return nil, nil
}

func (s *mockStore) ListTimedOutTransactions(ctx context.Context, instanceID string, leaseDuration time.Duration) ([]*Transaction, error) {
	return nil, nil
}

func (s *mockStore) AcquirePollTasks(ctx context.Context, instanceID string, batchSize int) ([]*PollTask, error) {
	return nil, nil
}

func (s *mockStore) ReleasePollTask(ctx context.Context, stepID string) error {
	return nil
}

func (s *mockStore) ClaimTransaction(ctx context.Context, txID string, instanceID string, leaseDuration time.Duration) (bool, error) {
	return true, nil
}

func (s *mockStore) ReleaseTransaction(ctx context.Context, txID string, instanceID string) error {
	return nil
}

func (s *mockStore) UpdateTransactionStatusCAS(ctx context.Context, txID string, expectedStatus TxStatus, newStatus TxStatus, lastError string) (bool, error) {
	return true, nil
}
