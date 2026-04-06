// File: engine.go
// Package saga - Engine is the main entry point for SAGA transactions

package saga

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jinleili-zz/nsp-platform/auth"
	"github.com/jinleili-zz/nsp-platform/trace"
)

// Config holds configuration for the SAGA engine.
type Config struct {
	// DSN is the PostgreSQL connection string.
	DSN string
	// WorkerCount is the number of concurrent coordinator workers (default: 4).
	WorkerCount int
	// PollBatchSize is the number of poll tasks to process per scan (default: 20).
	PollBatchSize int
	// PollScanInterval is the interval between poll task scans (default: 3s).
	PollScanInterval time.Duration
	// CoordScanInterval is the interval for coordinator scans (default: 5s).
	CoordScanInterval time.Duration
	// HTTPTimeout is the timeout for HTTP requests (default: 30s).
	HTTPTimeout time.Duration
	// InstanceID is the unique identifier for this instance (auto-generated if empty).
	InstanceID string
	// CredentialStore provides AK/SK credentials for signed step execution.
	CredentialStore auth.CredentialStore
}

// DefaultConfig returns the default engine configuration.
func DefaultConfig() *Config {
	return &Config{
		WorkerCount:       4,
		PollBatchSize:     20,
		PollScanInterval:  3 * time.Second,
		CoordScanInterval: 5 * time.Second,
		HTTPTimeout:       30 * time.Second,
		InstanceID:        "",
	}
}

// TransactionStatus represents the external view of a SAGA transaction status.
type TransactionStatus struct {
	// ID is the unique transaction identifier.
	ID string
	// Status is the current status (pending/running/compensating/succeeded/failed).
	Status string
	// CurrentStep is the index of the current step being executed.
	CurrentStep int
	// Steps contains the status of each step.
	Steps []StepStatusView
	// LastError contains the last error message.
	LastError string
	// CreatedAt is the transaction creation time.
	CreatedAt time.Time
	// FinishedAt is the transaction completion time (nil if not finished).
	FinishedAt *time.Time
}

// StepStatusView represents the external view of a step status.
type StepStatusView struct {
	// Index is the step index (0-based).
	Index int
	// Name is the step name.
	Name string
	// Status is the current status.
	Status string
	// PollCount is the number of poll attempts (for async steps).
	PollCount int
	// LastError contains the last error message.
	LastError string
}

// Engine is the main entry point for SAGA transactions.
type Engine struct {
	db          *sql.DB
	store       Store
	executor    *Executor
	poller      *Poller
	coordinator *Coordinator
	config      *Config
	cancelFunc  context.CancelFunc
}

// NewEngine creates a new SAGA engine with the given configuration.
// It establishes database connection but does not start background tasks.
func NewEngine(cfg *Config) (*Engine, error) {
	if cfg == nil {
		cfg = DefaultConfig()
	}

	// Apply defaults for zero values
	defaults := DefaultConfig()
	if cfg.WorkerCount <= 0 {
		cfg.WorkerCount = defaults.WorkerCount
	}
	if cfg.PollBatchSize <= 0 {
		cfg.PollBatchSize = defaults.PollBatchSize
	}
	if cfg.PollScanInterval <= 0 {
		cfg.PollScanInterval = defaults.PollScanInterval
	}
	if cfg.CoordScanInterval <= 0 {
		cfg.CoordScanInterval = defaults.CoordScanInterval
	}
	if cfg.HTTPTimeout <= 0 {
		cfg.HTTPTimeout = defaults.HTTPTimeout
	}

	// Validate configuration
	if cfg.DSN == "" {
		return nil, fmt.Errorf("DSN is required")
	}

	// Generate instance ID if not provided
	if cfg.InstanceID == "" {
		hostname, _ := os.Hostname()
		cfg.InstanceID = fmt.Sprintf("%s-%d", hostname, os.Getpid())
	}

	// Connect to database
	db, err := sql.Open("postgres", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	// Test connection
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	// Configure connection pool
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	// Create components
	store := NewPostgresStore(db)

	executorCfg := &ExecutorConfig{
		HTTPTimeout:     cfg.HTTPTimeout,
		CredentialStore: cfg.CredentialStore,
	}
	executor := NewExecutor(store, executorCfg)

	pollerCfg := &PollerConfig{
		ScanInterval: cfg.PollScanInterval,
		BatchSize:    cfg.PollBatchSize,
		InstanceID:   cfg.InstanceID,
	}
	poller := NewPoller(store, executor, pollerCfg)

	coordCfg := &CoordinatorConfig{
		WorkerCount:         cfg.WorkerCount,
		ScanInterval:        cfg.CoordScanInterval,
		TimeoutScanInterval: 30 * time.Second,
		AsyncStepTimeout:    10 * time.Minute,
		InstanceID:          cfg.InstanceID,
		LeaseDuration:       5 * time.Minute,
	}
	coordinator := NewCoordinator(store, executor, poller, coordCfg)

	return &Engine{
		db:          db,
		store:       store,
		executor:    executor,
		poller:      poller,
		coordinator: coordinator,
		config:      cfg,
	}, nil
}

// Start begins the engine's background tasks.
// The context cancellation will gracefully stop all tasks.
func (e *Engine) Start(ctx context.Context) error {
	// Create cancellable context
	ctx, cancel := context.WithCancel(ctx)
	e.cancelFunc = cancel

	// Start poller
	e.poller.Start(ctx)

	// Start coordinator
	e.coordinator.Start(ctx)

	return nil
}

// Stop gracefully stops the engine and releases resources.
func (e *Engine) Stop() error {
	// Cancel context to stop background tasks
	if e.cancelFunc != nil {
		e.cancelFunc()
	}

	// Stop components
	e.coordinator.Stop()
	e.poller.Stop()

	// Close database connection
	if e.db != nil {
		return e.db.Close()
	}

	return nil
}

// Submit submits a SAGA transaction definition for execution.
// Returns the generated transaction ID.
func (e *Engine) Submit(ctx context.Context, def *SagaDefinition) (string, error) {
	if def == nil {
		return "", fmt.Errorf("saga definition is required")
	}

	if err := validateStepCredentials(ctx, e.config.CredentialStore, def.Steps); err != nil {
		return "", err
	}

	// Generate transaction ID
	txID := uuid.New().String()
	def.ID = txID

	// Deep copy payload to avoid modifying caller's map
	payload := make(map[string]any, len(def.Payload)+2)
	for k, v := range def.Payload {
		payload[k] = v
	}

	// Extract trace context and store in payload for propagation
	if tc, ok := trace.TraceFromContext(ctx); ok && tc != nil {
		payload["_trace_id"] = tc.TraceID
		payload["_span_id"] = tc.SpanId
	}

	// Create transaction record
	now := time.Now()
	tx := &Transaction{
		ID:          txID,
		Status:      TxStatusPending,
		Payload:     payload,
		CurrentStep: 0,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	// Set timeout if specified
	if def.TimeoutSec > 0 {
		timeoutAt := now.Add(time.Duration(def.TimeoutSec) * time.Second)
		tx.TimeoutAt = &timeoutAt
	}

	// Create step records
	steps := make([]*Step, len(def.Steps))
	for i, stepDef := range def.Steps {
		stepID := uuid.New().String()
		steps[i] = &Step{
			ID:                stepID,
			TransactionID:     txID,
			Index:             i,
			Name:              stepDef.Name,
			Type:              stepDef.Type,
			Status:            StepStatusPending,
			ActionMethod:      stepDef.ActionMethod,
			ActionURL:         stepDef.ActionURL,
			ActionPayload:     stepDef.ActionPayload,
			AuthAK:            stepDef.AuthAK,
			CompensateMethod:  stepDef.CompensateMethod,
			CompensateURL:     stepDef.CompensateURL,
			CompensatePayload: stepDef.CompensatePayload,
			PollURL:           stepDef.PollURL,
			PollMethod:        stepDef.PollMethod,
			PollIntervalSec:   stepDef.PollIntervalSec,
			PollMaxTimes:      stepDef.PollMaxTimes,
			PollSuccessPath:   stepDef.PollSuccessPath,
			PollSuccessValue:  stepDef.PollSuccessValue,
			PollFailurePath:   stepDef.PollFailurePath,
			PollFailureValue:  stepDef.PollFailureValue,
			MaxRetry:          stepDef.MaxRetry,
		}
	}

	// Create transaction and steps atomically in a single database transaction
	if err := e.store.CreateTransactionWithSteps(ctx, tx, steps); err != nil {
		return "", fmt.Errorf("failed to create transaction: %w", err)
	}

	// Submit to coordinator for execution
	e.coordinator.Submit(txID)

	return txID, nil
}

func validateStepCredentials(ctx context.Context, store auth.CredentialStore, steps []Step) error {
	for _, step := range steps {
		if step.AuthAK == "" {
			continue
		}
		if store == nil {
			return fmt.Errorf("credential store is required for step auth access key %q", step.AuthAK)
		}
		cred, err := store.GetByAK(ctx, step.AuthAK)
		if err != nil {
			return fmt.Errorf("failed to load credential %q: %w", step.AuthAK, err)
		}
		if cred == nil || !cred.Enabled {
			return fmt.Errorf("credential %q not found or disabled", step.AuthAK)
		}
	}
	return nil
}

// Query retrieves the status of a SAGA transaction.
func (e *Engine) Query(ctx context.Context, txID string) (*TransactionStatus, error) {
	tx, err := e.store.GetTransaction(ctx, txID)
	if err != nil {
		return nil, fmt.Errorf("failed to get transaction: %w", err)
	}
	if tx == nil {
		return nil, nil
	}

	steps, err := e.store.GetSteps(ctx, txID)
	if err != nil {
		return nil, fmt.Errorf("failed to get steps: %w", err)
	}

	stepViews := make([]StepStatusView, len(steps))
	for i, step := range steps {
		stepViews[i] = StepStatusView{
			Index:     step.Index,
			Name:      step.Name,
			Status:    string(step.Status),
			PollCount: step.PollCount,
			LastError: step.LastError,
		}
	}

	return &TransactionStatus{
		ID:          tx.ID,
		Status:      string(tx.Status),
		CurrentStep: tx.CurrentStep,
		Steps:       stepViews,
		LastError:   tx.LastError,
		CreatedAt:   tx.CreatedAt,
		FinishedAt:  tx.FinishedAt,
	}, nil
}

// Store returns the underlying store for direct access.
func (e *Engine) Store() Store {
	return e.store
}

// DB returns the underlying database connection for migrations.
func (e *Engine) DB() *sql.DB {
	return e.db
}

/*
使用示例:

示例一：纯同步步骤事务

	engine, err := saga.NewEngine(&saga.Config{
		DSN:         "postgres://user:pass@localhost:5432/dbname?sslmode=disable",
		WorkerCount: 4,
	})
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	if err := engine.Start(ctx); err != nil {
		log.Fatal(err)
	}
	defer engine.Stop()

	def, err := saga.NewSaga("order-checkout").
		AddStep(saga.Step{
			Name:             "扣减库存",
			Type:             saga.StepTypeSync,
			ActionMethod:     "POST",
			ActionURL:        "http://stock-service/api/v1/stock/deduct",
			ActionPayload:    map[string]any{"item_id": "SKU-001", "count": 2},
			CompensateMethod: "POST",
			CompensateURL:    "http://stock-service/api/v1/stock/rollback",
			CompensatePayload: map[string]any{"item_id": "SKU-001", "count": 2},
		}).
		AddStep(saga.Step{
			Name:             "创建订单",
			Type:             saga.StepTypeSync,
			ActionMethod:     "POST",
			ActionURL:        "http://order-service/api/v1/orders",
			ActionPayload:    map[string]any{"user_id": "U-001"},
			CompensateMethod: "DELETE",
			CompensateURL:    "http://order-service/api/v1/orders/{action_response.order_id}",
		}).
		Build()
	if err != nil {
		log.Fatal(err)
	}

	txID, err := engine.Submit(ctx, def)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Transaction submitted: %s\n", txID)

示例二：含异步轮询步骤的事务

	def, err := saga.NewSaga("device-config").
		AddStep(saga.Step{
			Name:             "设备配置下发",
			Type:             saga.StepTypeAsync,
			ActionMethod:     "POST",
			ActionURL:        "http://device-service/api/v1/config/apply",
			ActionPayload:    map[string]any{"device_id": "DEV-001"},
			CompensateMethod: "POST",
			CompensateURL:    "http://device-service/api/v1/config/rollback",
			CompensatePayload: map[string]any{"device_id": "DEV-001"},
			PollURL:          "http://device-service/api/v1/config/status?task_id={action_response.task_id}",
			PollMethod:       "GET",
			PollIntervalSec:  10,
			PollMaxTimes:     30,
			PollSuccessPath:  "$.status",
			PollSuccessValue: "success",
			PollFailurePath:  "$.status",
			PollFailureValue: "failed",
		}).
		Build()
	if err != nil {
		log.Fatal(err)
	}

	txID, err := engine.Submit(ctx, def)
	if err != nil {
		log.Fatal(err)
	}

	// 查询事务状态
	status, err := engine.Query(ctx, txID)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Transaction %s status: %s\n", status.ID, status.Status)

示例三：带全局 Payload 的事务

	def, err := saga.NewSaga("transfer").
		WithPayload(map[string]any{
			"from_account": "ACC-001",
			"to_account":   "ACC-002",
			"amount":       1000,
		}).
		WithTimeout(300). // 5分钟超时
		AddStep(saga.Step{
			Name:             "扣款",
			Type:             saga.StepTypeSync,
			ActionMethod:     "POST",
			ActionURL:        "http://account-service/api/v1/debit",
			ActionPayload:    map[string]any{
				"account": "{transaction.payload.from_account}",
				"amount":  "{transaction.payload.amount}",
			},
			CompensateMethod: "POST",
			CompensateURL:    "http://account-service/api/v1/credit",
			CompensatePayload: map[string]any{
				"account": "{transaction.payload.from_account}",
				"amount":  "{transaction.payload.amount}",
			},
		}).
		AddStep(saga.Step{
			Name:             "入账",
			Type:             saga.StepTypeSync,
			ActionMethod:     "POST",
			ActionURL:        "http://account-service/api/v1/credit",
			ActionPayload:    map[string]any{
				"account": "{transaction.payload.to_account}",
				"amount":  "{transaction.payload.amount}",
			},
			CompensateMethod: "POST",
			CompensateURL:    "http://account-service/api/v1/debit",
			CompensatePayload: map[string]any{
				"account": "{transaction.payload.to_account}",
				"amount":  "{transaction.payload.amount}",
			},
		}).
		Build()
	if err != nil {
		log.Fatal(err)
	}

	txID, err := engine.Submit(ctx, def)
*/
