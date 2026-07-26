package dao

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	_ "github.com/lib/pq"

	"workflow_qoder/internal/models"
)

func TestTaskUpdateResultAllowsOneConcurrentTerminalWinner(t *testing.T) {
	dsn := os.Getenv("NSP_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("NSP_TEST_POSTGRES_DSN is required for PostgreSQL integration tests")
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Ping(); err != nil {
		t.Fatalf("ping PostgreSQL: %v", err)
	}

	ctx := context.Background()
	taskID := uuid.NewString()
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM tasks WHERE id = $1`, taskID)
	})

	dao := NewTaskDAO(db)
	task := &models.Task{
		ID:           taskID,
		ResourceType: models.ResourceTypeVPC,
		ResourceID:   uuid.NewString(),
		TaskType:     "create_vrf_on_switch",
		TaskName:     "create vrf",
		TaskOrder:    1,
		TaskParams:   `{}`,
		Status:       models.TaskStatusQueued,
		DeviceType:   "switch",
		MaxRetries:   3,
		AZ:           "az-test",
	}
	if err := dao.BatchCreate(ctx, []*models.Task{task}); err != nil {
		t.Fatalf("create task: %v", err)
	}

	const contenders = 32
	start := make(chan struct{})
	var winners atomic.Int32
	var wg sync.WaitGroup
	errCh := make(chan error, contenders)
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			updated, err := dao.UpdateResult(ctx, taskID, models.TaskStatusCompleted, `{"ok":true}`, "")
			if err != nil {
				errCh <- err
				return
			}
			if updated {
				winners.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("UpdateResult: %v", err)
		}
	}

	if got := winners.Load(); got != 1 {
		t.Fatalf("terminal CAS winners = %d, want 1", got)
	}
	persisted, err := dao.GetByID(ctx, taskID)
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	if persisted.Status != models.TaskStatusCompleted {
		t.Fatalf("status = %q, want %q", persisted.Status, models.TaskStatusCompleted)
	}
}

func TestTaskRetryProgressCannotRegressTerminalTask(t *testing.T) {
	dsn := os.Getenv("NSP_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("NSP_TEST_POSTGRES_DSN is required for PostgreSQL integration tests")
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Ping(); err != nil {
		t.Fatalf("ping PostgreSQL: %v", err)
	}

	ctx := context.Background()
	taskID := uuid.NewString()
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM tasks WHERE id = $1`, taskID)
	})
	dao := NewTaskDAO(db)
	createIntegrationTask(t, ctx, dao, taskID)

	updated, err := dao.UpdateResult(ctx, taskID, models.TaskStatusCompleted, `{"ok":true}`, "")
	if err != nil || !updated {
		t.Fatalf("complete task: updated=%v err=%v", updated, err)
	}
	retried, err := dao.UpdateRetryProgress(ctx, taskID, 1, 3, "late retry")
	if err != nil {
		t.Fatalf("apply late retry progress: %v", err)
	}
	if retried {
		t.Fatal("late retry unexpectedly updated a terminal task")
	}

	persisted, err := dao.GetByID(ctx, taskID)
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	if persisted.Status != models.TaskStatusCompleted {
		t.Fatalf("late retry regressed status to %q, want %q", persisted.Status, models.TaskStatusCompleted)
	}
}

func createIntegrationTask(t *testing.T, ctx context.Context, dao *TaskDAO, taskID string) {
	t.Helper()
	task := &models.Task{
		ID:           taskID,
		ResourceType: models.ResourceTypeVPC,
		ResourceID:   uuid.NewString(),
		TaskType:     "create_vrf_on_switch",
		TaskName:     "create vrf",
		TaskOrder:    1,
		TaskParams:   `{}`,
		Status:       models.TaskStatusQueued,
		DeviceType:   "switch",
		MaxRetries:   3,
		AZ:           "az-test",
	}
	if err := dao.BatchCreate(ctx, []*models.Task{task}); err != nil {
		t.Fatalf("create task: %v", err)
	}
}
