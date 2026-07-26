package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"workflow_qoder/internal/orchestration"

	"github.com/google/uuid"
	"github.com/jinleili-zz/nsp-platform/taskqueue"
	_ "github.com/lib/pq"
)

type recordingBroker struct {
	mu       sync.Mutex
	tasks    []*taskqueue.Task
	failNext bool
}

func (b *recordingBroker) Publish(_ context.Context, task *taskqueue.Task) (*taskqueue.TaskInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failNext {
		b.failNext = false
		return nil, errors.New("injected broker failure")
	}
	b.tasks = append(b.tasks, task)
	return &taskqueue.TaskInfo{BrokerTaskID: uuid.NewString(), Queue: task.Queue}, nil
}

func (b *recordingBroker) Close() error { return nil }

type countingDriver struct {
	DeviceDriver
	present atomic.Int32
	absent  atomic.Int32
}

func (d *countingDriver) EnsurePresent(ctx context.Context, target DeviceTarget, result json.RawMessage) error {
	d.present.Add(1)
	return d.DeviceDriver.EnsurePresent(ctx, target, result)
}

func (d *countingDriver) EnsureAbsent(ctx context.Context, target DeviceTarget) error {
	d.absent.Add(1)
	return d.DeviceDriver.EnsureAbsent(ctx, target)
}

func TestRuntimeReplaysSucceededDeviceOperationWithoutExecutingHandler(t *testing.T) {
	db := openWorkerTestDB(t)
	owner := "worker-test-" + uuid.NewString()
	broker := &recordingBroker{}
	runtime := NewRuntime(db, broker, owner)
	task := workerTestTask(t)
	cleanupWorkerTest(t, db, owner, task.Metadata[orchestration.MetadataKeyOperationKey])

	var executions atomic.Int32
	handler := runtime.Wrap(func(ctx context.Context, incoming *taskqueue.Task) error {
		executions.Add(1)
		return publishWorkerTestReply(ctx, runtime, incoming)
	})
	if err := handler(t.Context(), task); err != nil {
		t.Fatalf("first execution: %v", err)
	}
	if err := handler(t.Context(), task); err != nil {
		t.Fatalf("replayed execution: %v", err)
	}
	if got := executions.Load(); got != 1 {
		t.Fatalf("device executions = %d, want 1", got)
	}
	if published, err := runtime.DispatchOnce(t.Context()); err != nil || published != 1 {
		t.Fatalf("dispatch reply: published=%d err=%v", published, err)
	}
	if len(broker.tasks) != 1 {
		t.Fatalf("published replies = %d, want 1", len(broker.tasks))
	}
	var ledgerStatus, outboxStatus string
	if err := db.QueryRow(`SELECT status FROM worker_operations WHERE operation_key = $1`, task.Metadata[orchestration.MetadataKeyOperationKey]).Scan(&ledgerStatus); err != nil {
		t.Fatalf("load ledger: %v", err)
	}
	if err := db.QueryRow(`SELECT status FROM outbox_events WHERE owner_service = $1`, owner).Scan(&outboxStatus); err != nil {
		t.Fatalf("load outbox: %v", err)
	}
	if ledgerStatus != "succeeded" || outboxStatus != "published" {
		t.Fatalf("ledger/outbox status = %s/%s", ledgerStatus, outboxStatus)
	}
}

func TestRuntimeRejectsSameOperationKeyWithDifferentDesiredHash(t *testing.T) {
	db := openWorkerTestDB(t)
	owner := "worker-conflict-" + uuid.NewString()
	runtime := NewRuntime(db, &recordingBroker{}, owner)
	task := workerTestTask(t)
	cleanupWorkerTest(t, db, owner, task.Metadata[orchestration.MetadataKeyOperationKey])
	handler := runtime.Wrap(func(ctx context.Context, incoming *taskqueue.Task) error {
		return publishWorkerTestReply(ctx, runtime, incoming)
	})
	if err := handler(t.Context(), task); err != nil {
		t.Fatalf("first execution: %v", err)
	}
	conflict := *task
	conflict.Metadata = cloneMetadata(task.Metadata)
	conflict.Metadata[orchestration.MetadataKeyDesiredHash] = strings.Repeat("b", 64)
	if err := handler(t.Context(), &conflict); !errors.Is(err, ErrDesiredConflict) {
		t.Fatalf("conflicting desired hash = %v, want ErrDesiredConflict", err)
	}
}

func TestReplyOutboxRetriesAfterBrokerFailure(t *testing.T) {
	db := openWorkerTestDB(t)
	owner := "worker-outbox-" + uuid.NewString()
	broker := &recordingBroker{failNext: true}
	runtime := NewRuntime(db, broker, owner)
	task := workerTestTask(t)
	cleanupWorkerTest(t, db, owner, task.Metadata[orchestration.MetadataKeyOperationKey])
	handler := runtime.Wrap(func(ctx context.Context, incoming *taskqueue.Task) error {
		return publishWorkerTestReply(ctx, runtime, incoming)
	})
	if err := handler(t.Context(), task); err != nil {
		t.Fatalf("execute device operation: %v", err)
	}
	if published, err := runtime.DispatchOnce(t.Context()); err == nil || published != 0 {
		t.Fatalf("first dispatch = %d/%v, want injected failure", published, err)
	}
	if _, err := db.Exec(`UPDATE outbox_events SET available_at = NOW() WHERE owner_service = $1`, owner); err != nil {
		t.Fatalf("make reply retry available: %v", err)
	}
	if published, err := runtime.DispatchOnce(t.Context()); err != nil || published != 1 {
		t.Fatalf("retry dispatch = %d/%v, want success", published, err)
	}
	if len(broker.tasks) != 1 {
		t.Fatalf("published replies after recovery = %d, want 1", len(broker.tasks))
	}
}

func TestRuntimeTakeoverChecksDeviceAfterCrashBeforeLedgerCommit(t *testing.T) {
	db := openWorkerTestDB(t)
	owner := "worker-takeover-" + uuid.NewString()
	runtime := NewRuntime(db, &recordingBroker{}, owner)
	driver := &countingDriver{DeviceDriver: runtime.driver}
	runtime.driver = driver
	task := workerTestTask(t)
	operationKey := task.Metadata[orchestration.MetadataKeyOperationKey]
	cleanupWorkerTest(t, db, owner, operationKey)

	injected := errors.New("injected crash after device ensure")
	runtime.afterEnsure = func(context.Context, DeviceTarget) error {
		runtime.afterEnsure = nil
		return injected
	}
	handler := runtime.Wrap(func(ctx context.Context, incoming *taskqueue.Task) error {
		return publishWorkerTestReply(ctx, runtime, incoming)
	})
	if err := handler(t.Context(), task); !errors.Is(err, injected) {
		t.Fatalf("first execution error = %v, want injected crash", err)
	}
	if driver.present.Load() != 1 {
		t.Fatalf("device ensures after crash = %d, want 1", driver.present.Load())
	}
	if _, err := db.Exec(`
		UPDATE worker_operations SET lease_expires_at = NOW() - interval '1 second'
		WHERE operation_key = $1
	`, operationKey); err != nil {
		t.Fatalf("expire first Worker lease: %v", err)
	}
	if err := handler(t.Context(), task); err != nil {
		t.Fatalf("take over execution: %v", err)
	}
	if driver.present.Load() != 1 {
		t.Fatalf("device ensures after takeover = %d, want no duplicate", driver.present.Load())
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM worker_operations WHERE operation_key = $1`, operationKey).Scan(&status); err != nil {
		t.Fatalf("load ledger status: %v", err)
	}
	if status != "succeeded" {
		t.Fatalf("ledger status after takeover = %s, want succeeded", status)
	}
}

func TestRuntimeDeleteUsesEnsureAbsentAndTreatsMissingAsSuccess(t *testing.T) {
	db := openWorkerTestDB(t)
	owner := "worker-delete-" + uuid.NewString()
	runtime := NewRuntime(db, &recordingBroker{}, owner)
	driver := &countingDriver{DeviceDriver: runtime.driver}
	runtime.driver = driver
	task := workerTestTask(t)
	task.Type = "delete_pccn_connection"
	task.Metadata[orchestration.MetadataKeyOperationKey] = "switch:delete_pccn:" + uuid.NewString() + ":gen:1"
	operationKey := task.Metadata[orchestration.MetadataKeyOperationKey]
	targetKey := task.Metadata[orchestration.MetadataKeyResourceID]
	cleanupWorkerTest(t, db, owner, operationKey)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM worker_device_state WHERE target_key = $1`, targetKey)
	})
	if _, err := db.Exec(`
		INSERT INTO worker_device_state (
			operation_key, device_type, target_key, desired_hash, result_payload
		) VALUES ($1, 'switch', $2, $3, '{}'::jsonb)
	`, "prior-create-"+uuid.NewString(), targetKey, strings.Repeat("c", 64)); err != nil {
		t.Fatalf("seed device state: %v", err)
	}
	handler := runtime.Wrap(func(ctx context.Context, incoming *taskqueue.Task) error {
		return publishWorkerTestReply(ctx, runtime, incoming)
	})
	if err := handler(t.Context(), task); err != nil {
		t.Fatalf("ensure device absent: %v", err)
	}
	if driver.absent.Load() != 1 {
		t.Fatalf("ensure-absent calls = %d, want 1", driver.absent.Load())
	}
	var states int
	if err := db.QueryRow(`SELECT COUNT(*) FROM worker_device_state WHERE target_key = $1`, targetKey).Scan(&states); err != nil {
		t.Fatalf("count device state: %v", err)
	}
	if states != 0 {
		t.Fatalf("device states after delete = %d, want absent", states)
	}

	duplicate := *task
	if err := handler(t.Context(), &duplicate); err != nil {
		t.Fatalf("repeat delete: %v", err)
	}
	if driver.absent.Load() != 1 {
		t.Fatalf("ensure-absent calls after duplicate = %d, want 1", driver.absent.Load())
	}
}

func TestRuntimeRenewsLeaseDuringSlowDeviceHandler(t *testing.T) {
	db := openWorkerTestDB(t)
	owner := "worker-renew-" + uuid.NewString()
	runtime := NewRuntime(db, &recordingBroker{}, owner)
	runtime.lease = 90 * time.Millisecond
	task := workerTestTask(t)
	operationKey := task.Metadata[orchestration.MetadataKeyOperationKey]
	cleanupWorkerTest(t, db, owner, operationKey)

	started := make(chan struct{})
	release := make(chan struct{})
	handler := runtime.Wrap(func(ctx context.Context, incoming *taskqueue.Task) error {
		close(started)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
			return publishWorkerTestReply(ctx, runtime, incoming)
		}
	})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- handler(t.Context(), task)
	}()
	<-started
	time.Sleep(180 * time.Millisecond)
	if err := runtime.Wrap(func(context.Context, *taskqueue.Task) error {
		return errors.New("duplicate handler must not run")
	})(t.Context(), task); !errors.Is(err, ErrExecutionBusy) {
		t.Fatalf("concurrent duplicate = %v, want ErrExecutionBusy", err)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("slow execution: %v", err)
	}
}

func workerTestTask(t *testing.T) *taskqueue.Task {
	t.Helper()
	operationKey := "switch:create_vrf:" + uuid.NewString() + ":gen:1"
	return &taskqueue.Task{
		Type:    "create_vrf_on_switch",
		Payload: json.RawMessage(`{"vrf_name":"vrf-a"}`),
		Reply:   &taskqueue.ReplySpec{Queue: "replies:test:az:vpc"},
		Metadata: map[string]string{
			orchestration.MetadataKeyOperationKey:    operationKey,
			orchestration.MetadataKeyDesiredHash:     strings.Repeat("a", 64),
			orchestration.MetadataKeyGeneration:      "1",
			orchestration.MetadataKeyRootOperationID: uuid.NewString(),
			orchestration.MetadataKeyOperationID:     uuid.NewString(),
			orchestration.MetadataKeyWorkflowID:      uuid.NewString(),
			orchestration.MetadataKeyTaskID:          uuid.NewString(),
			orchestration.MetadataKeyResourceID:      uuid.NewString(),
			orchestration.MetadataKeyDeviceType:      "switch",
		},
	}
}

func publishWorkerTestReply(ctx context.Context, runtime *Runtime, task *taskqueue.Task) error {
	payload, _ := json.Marshal(orchestration.ReplyPayload{
		ProtocolVersion: orchestration.TaskProtocolVersion,
		EventID:         uuid.NewSHA1(uuid.NameSpaceOID, []byte("reply:"+task.Metadata[orchestration.MetadataKeyTaskID])).String(),
		OperationID:     task.Metadata[orchestration.MetadataKeyOperationID],
		RootOperationID: task.Metadata[orchestration.MetadataKeyRootOperationID],
		WorkflowID:      task.Metadata[orchestration.MetadataKeyWorkflowID],
		TaskID:          task.Metadata[orchestration.MetadataKeyTaskID],
		Generation:      1,
		OperationKey:    task.Metadata[orchestration.MetadataKeyOperationKey],
		DesiredHash:     task.Metadata[orchestration.MetadataKeyDesiredHash],
		ResourceID:      task.Metadata[orchestration.MetadataKeyResourceID],
		ResourceType:    "vpc",
		TaskType:        task.Type,
		Status:          orchestration.ReplyStatusSuccess,
	})
	_, err := runtime.Publish(ctx, &taskqueue.Task{
		Type: orchestration.ReplyTaskType, Payload: payload,
		Queue: task.Reply.Queue, Metadata: cloneMetadata(task.Metadata),
	})
	return err
}

func cloneMetadata(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func openWorkerTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("NSP_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("NSP_TEST_POSTGRES_DSN is required")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, path := range []string{
		"../db/migrations/001_init_postgresql.sql",
		"../db/migrations/004_create_pccn_tables.sql",
		"../db/migrations/005_create_operations.sql",
		"../db/migrations/006_create_outbox_inbox.sql",
		"../db/migrations/008_create_worker_ledger.sql",
	} {
		migration, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read migration %s: %v", path, err)
		}
		if _, err := db.Exec(string(migration)); err != nil {
			t.Fatalf("apply migration %s: %v", path, err)
		}
	}
	return db
}

func cleanupWorkerTest(t *testing.T, db *sql.DB, owner, operationKey string) {
	t.Helper()
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM outbox_events WHERE owner_service = $1`, owner)
		_, _ = db.Exec(`DELETE FROM worker_device_state WHERE operation_key = $1`, operationKey)
		_, _ = db.Exec(`DELETE FROM worker_operations WHERE operation_key = $1`, operationKey)
	})
}
