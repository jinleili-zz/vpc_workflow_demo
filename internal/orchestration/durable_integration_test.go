package orchestration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jinleili-zz/nsp-platform/taskqueue"
	_ "github.com/lib/pq"

	"workflow_qoder/internal/models"
)

func TestSubmitWorkflowTxCommitsTasksResourceAndFirstOutboxAtomically(t *testing.T) {
	db := openDurableTestDB(t)
	resourceID := insertDurableTestVPC(t, db)
	t.Cleanup(func() { cleanupDurableTestWorkflow(t, db, resourceID) })
	repository := NewWorkflowRepository(db, "az-nsp-vpc")

	definition := durableTestWorkflow(resourceID)
	workflowID, err := repository.SubmitWorkflowTx(
		context.Background(),
		definition,
		func(string, int) string { return "tasks:test:switch" },
		"replies:test:vpc",
	)
	if err != nil {
		t.Fatalf("SubmitWorkflowTx: %v", err)
	}
	if workflowID != definition.WorkflowID {
		t.Fatalf("workflow ID = %s, want %s", workflowID, definition.WorkflowID)
	}

	var taskCount, pendingOutbox, totalTasks int
	var resourceStatus string
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE workflow_id = $1 AND generation = $2`, workflowID, definition.Generation).Scan(&taskCount); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM outbox_events WHERE aggregate_id IN (SELECT id FROM tasks WHERE workflow_id = $1) AND status = 'pending'`, workflowID).Scan(&pendingOutbox); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if err := db.QueryRow(`SELECT status::text, total_tasks FROM vpc_resources WHERE id = $1`, resourceID).Scan(&resourceStatus, &totalTasks); err != nil {
		t.Fatalf("load resource: %v", err)
	}
	if taskCount != 2 || pendingOutbox != 1 || totalTasks != 2 || resourceStatus != "creating" {
		t.Fatalf("tasks/outbox/total/status = %d/%d/%d/%s, want 2/1/2/creating", taskCount, pendingOutbox, totalTasks, resourceStatus)
	}
}

func TestSubmitWorkflowTxRollsBackEveryWriteWhenSecondTaskIsInvalid(t *testing.T) {
	db := openDurableTestDB(t)
	resourceID := insertDurableTestVPC(t, db)
	t.Cleanup(func() { cleanupDurableTestWorkflow(t, db, resourceID) })
	repository := NewWorkflowRepository(db, "az-nsp-vpc")
	definition := durableTestWorkflow(resourceID)
	definition.Steps[1].Payload = []byte(`{"invalid"`)

	if _, err := repository.SubmitWorkflowTx(context.Background(), definition, func(string, int) string { return "tasks:test:switch" }, "replies:test:vpc"); err == nil {
		t.Fatal("invalid second task unexpectedly committed")
	}

	var taskCount, outboxCount, totalTasks int
	var resourceStatus string
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE workflow_id = $1`, definition.WorkflowID).Scan(&taskCount); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM outbox_events WHERE event_key LIKE $1`, "task:%:"+definition.WorkflowID+":%").Scan(&outboxCount); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if err := db.QueryRow(`SELECT status::text, total_tasks FROM vpc_resources WHERE id = $1`, resourceID).Scan(&resourceStatus, &totalTasks); err != nil {
		t.Fatalf("load resource: %v", err)
	}
	if taskCount != 0 || outboxCount != 0 || totalTasks != 0 || resourceStatus != "pending" {
		t.Fatalf("rollback left tasks/outbox/total/status = %d/%d/%d/%s", taskCount, outboxCount, totalTasks, resourceStatus)
	}
}

func TestSubmitWorkflowTxRollsBackWhenRequiredOperationIsMissing(t *testing.T) {
	db := openDurableTestDB(t)
	resourceID := insertDurableTestVPC(t, db)
	t.Cleanup(func() { cleanupDurableTestWorkflow(t, db, resourceID) })
	repository := NewWorkflowRepository(db, "az-nsp-vpc")
	definition := durableTestWorkflow(resourceID)
	definition.OperationRequired = true

	if _, err := repository.SubmitWorkflowTx(context.Background(), definition, func(string, int) string { return "tasks:test:switch" }, "replies:test:vpc"); err == nil {
		t.Fatal("workflow without its required operation unexpectedly committed")
	}
	var taskCount, outboxCount, totalTasks int
	var resourceStatus string
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE workflow_id = $1`, definition.WorkflowID).Scan(&taskCount); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM outbox_events WHERE aggregate_id IN (SELECT id FROM tasks WHERE workflow_id = $1)`, definition.WorkflowID).Scan(&outboxCount); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if err := db.QueryRow(`SELECT status, total_tasks FROM vpc_resources WHERE id = $1`, resourceID).Scan(&resourceStatus, &totalTasks); err != nil {
		t.Fatalf("load resource: %v", err)
	}
	if taskCount != 0 || outboxCount != 0 || totalTasks != 0 || resourceStatus != "pending" {
		t.Fatalf("missing-operation rollback left tasks/outbox/total/status=%d/%d/%d/%s", taskCount, outboxCount, totalTasks, resourceStatus)
	}
}

func TestSubmitPreparedWorkflowTxRollsBackResourceWithInvalidTask(t *testing.T) {
	db := openDurableTestDB(t)
	repository := NewWorkflowRepository(db, "az-nsp-vpc")
	resourceID := uuid.NewString()
	t.Cleanup(func() { cleanupDurableTestWorkflow(t, db, resourceID) })

	_, _, err := repository.SubmitPreparedWorkflowTx(
		context.Background(),
		func(ctx context.Context, tx *sql.Tx) (WorkflowDef, error) {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO vpc_resources (id, vpc_name, region, az, status, total_tasks, completed_tasks, failed_tasks)
				VALUES ($1, $2, 'region-test', 'az-test', 'pending', 0, 0, 0)
			`, resourceID, "vpc-"+resourceID); err != nil {
				return WorkflowDef{}, err
			}
			definition := durableTestWorkflow(resourceID)
			definition.Steps[1].Payload = []byte(`{"invalid"`)
			return definition, nil
		},
		func(string, int) string { return "tasks:test:switch" },
		"replies:test:vpc",
	)
	if err == nil {
		t.Fatal("prepared workflow with invalid task unexpectedly committed")
	}
	var resourceCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM vpc_resources WHERE id = $1`, resourceID).Scan(&resourceCount); err != nil {
		t.Fatalf("count resource: %v", err)
	}
	if resourceCount != 0 {
		t.Fatalf("resource rows after rollback = %d, want 0", resourceCount)
	}
}

func TestSubmitWorkflowTxReplayReusesStableTasksAndOutbox(t *testing.T) {
	db := openDurableTestDB(t)
	resourceID := insertDurableTestVPC(t, db)
	t.Cleanup(func() { cleanupDurableTestWorkflow(t, db, resourceID) })
	repository := NewWorkflowRepository(db, "az-nsp-vpc")
	definition := durableTestWorkflow(resourceID)

	for i := 0; i < 2; i++ {
		if _, err := repository.SubmitWorkflowTx(context.Background(), definition, func(string, int) string { return "tasks:test:switch" }, "replies:test:vpc"); err != nil {
			t.Fatalf("SubmitWorkflowTx attempt %d: %v", i+1, err)
		}
	}
	var taskCount, outboxCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE workflow_id = $1`, definition.WorkflowID).Scan(&taskCount); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM outbox_events WHERE aggregate_id IN (SELECT id FROM tasks WHERE workflow_id = $1)`, definition.WorkflowID).Scan(&outboxCount); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if taskCount != 2 || outboxCount != 1 {
		t.Fatalf("replay tasks/outbox = %d/%d, want 2/1", taskCount, outboxCount)
	}
}

func TestSubmitWorkflowTxRejectsConflictingExistingOutbox(t *testing.T) {
	db := openDurableTestDB(t)
	resourceID := insertDurableTestVPC(t, db)
	t.Cleanup(func() { cleanupDurableTestWorkflow(t, db, resourceID) })
	repository := NewWorkflowRepository(db, "az-nsp-vpc")
	definition := durableTestWorkflow(resourceID)
	if _, err := repository.SubmitWorkflowTx(context.Background(), definition, func(string, int) string { return "tasks:test:switch" }, "replies:test:vpc"); err != nil {
		t.Fatalf("first submit: %v", err)
	}
	if _, err := db.Exec(`UPDATE outbox_events SET destination = 'tasks:wrong' WHERE aggregate_id IN (SELECT id FROM tasks WHERE workflow_id = $1 AND task_order = 1)`, definition.WorkflowID); err != nil {
		t.Fatalf("corrupt outbox: %v", err)
	}
	if _, err := repository.SubmitWorkflowTx(context.Background(), definition, func(string, int) string { return "tasks:test:switch" }, "replies:test:vpc"); err == nil {
		t.Fatal("replay accepted conflicting existing outbox")
	}
}

type durableCaptureBroker struct {
	mu    sync.Mutex
	tasks []*taskqueue.Task
}

type blockingDurableBroker struct {
	published chan *taskqueue.Task
	release   chan struct{}
}

func (b *blockingDurableBroker) Publish(_ context.Context, task *taskqueue.Task) (*taskqueue.TaskInfo, error) {
	b.published <- task
	<-b.release
	return &taskqueue.TaskInfo{BrokerTaskID: "broker-blocked", Queue: task.Queue}, nil
}

func (*blockingDurableBroker) Close() error { return nil }

func (b *durableCaptureBroker) Publish(_ context.Context, task *taskqueue.Task) (*taskqueue.TaskInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tasks = append(b.tasks, task)
	return &taskqueue.TaskInfo{BrokerTaskID: "broker-" + task.Metadata[MetadataKeyTaskID], Queue: task.Queue}, nil
}

func TestOutboxLeaseRecoveryMakesUnmarkedPublishClaimableAgain(t *testing.T) {
	db := openDurableTestDB(t)
	resourceID := insertDurableTestVPC(t, db)
	t.Cleanup(func() { cleanupDurableTestWorkflow(t, db, resourceID) })
	repository := NewWorkflowRepository(db, "az-nsp-vpc")
	definition := durableTestWorkflow(resourceID)
	insertDurableTestOperation(t, db, definition.OperationID, resourceID)
	definition.OperationRequired = true
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM orchestration_operations WHERE operation_id = $1`, definition.OperationID)
	})
	if _, err := repository.SubmitWorkflowTx(context.Background(), definition, func(string, int) string { return "tasks:test:switch" }, "replies:test:vpc"); err != nil {
		t.Fatalf("SubmitWorkflowTx: %v", err)
	}

	firstClaim, err := repository.ClaimOutboxBatch(context.Background(), "worker-a", 1)
	if err != nil || len(firstClaim) != 1 {
		t.Fatalf("first claim len=%d err=%v", len(firstClaim), err)
	}
	if _, err := db.Exec(`UPDATE outbox_events SET locked_at = NOW() - INTERVAL '2 minutes' WHERE event_id = $1`, firstClaim[0].EventID); err != nil {
		t.Fatalf("age lease: %v", err)
	}
	recovered, err := repository.RecoverExpiredOutboxLeases(context.Background(), time.Minute)
	if err != nil || recovered != 1 {
		t.Fatalf("recover leases=%d err=%v", recovered, err)
	}
	secondClaim, err := repository.ClaimOutboxBatch(context.Background(), "worker-b", 1)
	if err != nil || len(secondClaim) != 1 {
		t.Fatalf("second claim len=%d err=%v", len(secondClaim), err)
	}
	if secondClaim[0].EventID != firstClaim[0].EventID || secondClaim[0].PublishAttempts != 2 {
		t.Fatalf("reclaimed event/attempt = %s/%d, want %s/2", secondClaim[0].EventID, secondClaim[0].PublishAttempts, firstClaim[0].EventID)
	}
}

func TestOutboxFailedAtAttemptLimitMovesToDead(t *testing.T) {
	db := openDurableTestDB(t)
	resourceID := insertDurableTestVPC(t, db)
	t.Cleanup(func() { cleanupDurableTestWorkflow(t, db, resourceID) })
	repository := NewWorkflowRepository(db, "az-nsp-vpc")
	definition := durableTestWorkflow(resourceID)
	insertDurableTestOperation(t, db, definition.OperationID, resourceID)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM orchestration_operations WHERE operation_id = $1`, definition.OperationID)
	})
	if _, err := repository.SubmitWorkflowTx(context.Background(), definition, func(string, int) string { return "tasks:test:switch" }, "replies:test:vpc"); err != nil {
		t.Fatalf("SubmitWorkflowTx: %v", err)
	}
	events, err := repository.ClaimOutboxBatch(context.Background(), "worker-dead", 1)
	if err != nil || len(events) != 1 {
		t.Fatalf("claim len=%d err=%v", len(events), err)
	}
	events[0].PublishAttempts = defaultOutboxMaxAttempts
	if err := repository.MarkOutboxFailed(context.Background(), events[0], "worker-dead", errors.New("redis unavailable")); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	var status, lastError, operationStatus, resourceStatus, taskStatus string
	if err := db.QueryRow(`SELECT status, last_error FROM outbox_events WHERE event_id = $1`, events[0].EventID).Scan(&status, &lastError); err != nil {
		t.Fatalf("load dead event: %v", err)
	}
	if status != "dead" || lastError != "redis unavailable" {
		t.Fatalf("status/error = %s/%s, want dead/redis unavailable", status, lastError)
	}
	if err := db.QueryRow(`SELECT status FROM orchestration_operations WHERE operation_id = $1`, definition.OperationID).Scan(&operationStatus); err != nil {
		t.Fatalf("load operation: %v", err)
	}
	if err := db.QueryRow(`SELECT status::text FROM vpc_resources WHERE id = $1`, resourceID).Scan(&resourceStatus); err != nil {
		t.Fatalf("load resource: %v", err)
	}
	if err := db.QueryRow(`SELECT status FROM tasks WHERE id = $1`, events[0].AggregateID).Scan(&taskStatus); err != nil {
		t.Fatalf("load task: %v", err)
	}
	if operationStatus != "failed" || resourceStatus != "failed" || taskStatus != "failed" {
		t.Fatalf("operation/resource/task statuses = %s/%s/%s, want failed/failed/failed", operationStatus, resourceStatus, taskStatus)
	}
}

func TestConcurrentOutboxDispatchersClaimAnEventOnce(t *testing.T) {
	db := openDurableTestDB(t)
	resourceID := insertDurableTestVPC(t, db)
	t.Cleanup(func() { cleanupDurableTestWorkflow(t, db, resourceID) })
	repository := NewWorkflowRepository(db, "az-nsp-vpc")
	definition := durableTestWorkflow(resourceID)
	if _, err := repository.SubmitWorkflowTx(context.Background(), definition, func(string, int) string { return "tasks:test:switch" }, "replies:test:vpc"); err != nil {
		t.Fatalf("SubmitWorkflowTx: %v", err)
	}
	broker := &durableCaptureBroker{}
	dispatchers := []*OutboxDispatcher{
		NewOutboxDispatcher(repository, broker, "dispatcher-a", 1),
		NewOutboxDispatcher(repository, broker, "dispatcher-b", 1),
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, len(dispatchers))
	for _, dispatcher := range dispatchers {
		wg.Add(1)
		go func(dispatcher *OutboxDispatcher) {
			defer wg.Done()
			<-start
			_, err := dispatcher.DispatchOnce(context.Background())
			errCh <- err
		}(dispatcher)
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("DispatchOnce: %v", err)
		}
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if len(broker.tasks) != 1 {
		t.Fatalf("broker publishes = %d, want 1", len(broker.tasks))
	}
}

func TestOutboxDispatcherCancelsStaleResourceGenerationBeforePublish(t *testing.T) {
	db := openDurableTestDB(t)
	resourceID := insertDurableTestVPC(t, db)
	t.Cleanup(func() { cleanupDurableTestWorkflow(t, db, resourceID) })
	repository := NewWorkflowRepository(db, "az-nsp-vpc")
	definition := durableTestWorkflow(resourceID)
	if _, err := repository.SubmitWorkflowTx(context.Background(), definition, func(string, int) string { return "tasks:test:switch" }, "replies:test:vpc"); err != nil {
		t.Fatalf("SubmitWorkflowTx: %v", err)
	}
	if _, err := db.Exec(`UPDATE vpc_resources SET generation = 2, current_operation_id = $1 WHERE id = $2`, uuid.NewString(), resourceID); err != nil {
		t.Fatalf("advance resource generation: %v", err)
	}

	broker := &durableCaptureBroker{}
	processed, err := NewOutboxDispatcher(repository, broker, "dispatcher-stale", 10).DispatchOnce(context.Background())
	if err != nil {
		t.Fatalf("DispatchOnce: %v", err)
	}
	if processed != 0 || len(broker.tasks) != 0 {
		t.Fatalf("stale processed/published = %d/%d, want 0/0", processed, len(broker.tasks))
	}
	var eventStatus, taskStatus string
	if err := db.QueryRow(`SELECT status FROM outbox_events WHERE aggregate_id IN (SELECT id FROM tasks WHERE workflow_id = $1 AND task_order = 1)`, definition.WorkflowID).Scan(&eventStatus); err != nil {
		t.Fatalf("load outbox: %v", err)
	}
	if err := db.QueryRow(`SELECT status FROM tasks WHERE workflow_id = $1 AND task_order = 1`, definition.WorkflowID).Scan(&taskStatus); err != nil {
		t.Fatalf("load task: %v", err)
	}
	if eventStatus != "cancelled" || taskStatus != "cancelled" {
		t.Fatalf("stale outbox/task = %s/%s, want cancelled/cancelled", eventStatus, taskStatus)
	}
}

func TestOutboxDispatcherHoldsGenerationFenceThroughPublishAndCommit(t *testing.T) {
	db := openDurableTestDB(t)
	resourceID := insertDurableTestVPC(t, db)
	t.Cleanup(func() { cleanupDurableTestWorkflow(t, db, resourceID) })
	repository := NewWorkflowRepository(db, "az-nsp-vpc")
	definition := durableTestWorkflow(resourceID)
	if _, err := repository.SubmitWorkflowTx(context.Background(), definition, func(string, int) string { return "tasks:test:switch" }, "replies:test:vpc"); err != nil {
		t.Fatalf("SubmitWorkflowTx: %v", err)
	}
	broker := &blockingDurableBroker{published: make(chan *taskqueue.Task, 1), release: make(chan struct{})}
	dispatchDone := make(chan error, 1)
	go func() {
		_, err := NewOutboxDispatcher(repository, broker, "dispatcher-fence", 1).DispatchOnce(context.Background())
		dispatchDone <- err
	}()
	<-broker.published

	advanceDone := make(chan error, 1)
	go func() {
		_, err := db.Exec(`UPDATE vpc_resources SET generation = 2, current_operation_id = $1 WHERE id = $2`, uuid.NewString(), resourceID)
		advanceDone <- err
	}()
	select {
	case err := <-advanceDone:
		t.Fatalf("new generation committed while old publish was in flight: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(broker.release)
	if err := <-dispatchDone; err != nil {
		t.Fatalf("DispatchOnce: %v", err)
	}
	if err := <-advanceDone; err != nil {
		t.Fatalf("advance generation: %v", err)
	}
	var outboxStatus string
	if err := db.QueryRow(`SELECT status FROM outbox_events WHERE aggregate_id IN (SELECT id FROM tasks WHERE workflow_id = $1 AND task_order = 1)`, definition.WorkflowID).Scan(&outboxStatus); err != nil {
		t.Fatalf("load outbox: %v", err)
	}
	if outboxStatus != "published" {
		t.Fatalf("outbox status=%s, want published before generation advance", outboxStatus)
	}
}

func TestReplyRacingPublishMarkConvergesWithoutOutboxRedelivery(t *testing.T) {
	db := openDurableTestDB(t)
	resourceID := insertDurableTestVPC(t, db)
	t.Cleanup(func() { cleanupDurableTestWorkflow(t, db, resourceID) })
	repository := NewWorkflowRepository(db, "az-nsp-vpc")
	definition := durableTestWorkflow(resourceID)
	insertDurableTestOperation(t, db, definition.OperationID, resourceID)
	definition.OperationRequired = true
	if _, err := repository.SubmitWorkflowTx(context.Background(), definition, func(string, int) string { return "tasks:test:switch" }, "replies:test:vpc"); err != nil {
		t.Fatalf("SubmitWorkflowTx: %v", err)
	}
	events, err := repository.ClaimOutboxBatch(context.Background(), "publisher-reply-race", 1)
	if err != nil || len(events) != 1 {
		t.Fatalf("claim events=%d err=%v", len(events), err)
	}
	replyDone := make(chan error, 1)
	published, err := repository.PublishClaimedTaskEvent(context.Background(), events[0], "publisher-reply-race", func(payload TaskDispatchPayload) (string, error) {
		task := &taskqueue.Task{
			Type: payload.TaskType, Payload: payload.Payload, Queue: payload.Queue,
			Reply: &taskqueue.ReplySpec{Queue: payload.ReplyQueue}, Metadata: payload.Metadata,
		}
		reply := durableReplyTask(t, task, ReplyStatusFailed, uuid.NewString())
		go func() {
			decision, replyErr := repository.HandleReplyTx(context.Background(), "az-vpc-reply", reply)
			if replyErr == nil && decision != ReplyDecisionApplied {
				replyErr = &unexpectedReplyDecisionError{decision: decision}
			}
			replyDone <- replyErr
		}()
		return "broker-fast-worker", nil
	})
	if err != nil || !published {
		t.Fatalf("publish claimed event published=%v err=%v", published, err)
	}
	if err := <-replyDone; err != nil {
		t.Fatalf("racing reply: %v", err)
	}
	var outboxStatus, taskStatus, operationStatus string
	if err := db.QueryRow(`SELECT status FROM outbox_events WHERE event_id = $1`, events[0].EventID).Scan(&outboxStatus); err != nil {
		t.Fatalf("load outbox: %v", err)
	}
	if err := db.QueryRow(`SELECT status FROM tasks WHERE id = $1`, events[0].AggregateID).Scan(&taskStatus); err != nil {
		t.Fatalf("load task: %v", err)
	}
	if err := db.QueryRow(`SELECT status FROM orchestration_operations WHERE operation_id = $1`, definition.OperationID).Scan(&operationStatus); err != nil {
		t.Fatalf("load operation: %v", err)
	}
	if outboxStatus != "published" || taskStatus != "failed" || operationStatus != "failed" {
		t.Fatalf("outbox/task/operation=%s/%s/%s, want published/failed/failed", outboxStatus, taskStatus, operationStatus)
	}
}

func (*durableCaptureBroker) Close() error { return nil }

func TestOutboxDispatcherPublishesAndMarksTaskQueued(t *testing.T) {
	db := openDurableTestDB(t)
	resourceID := insertDurableTestVPC(t, db)
	t.Cleanup(func() { cleanupDurableTestWorkflow(t, db, resourceID) })
	repository := NewWorkflowRepository(db, "az-nsp-vpc")
	definition := durableTestWorkflow(resourceID)
	if _, err := repository.SubmitWorkflowTx(context.Background(), definition, func(string, int) string { return "tasks:test:switch" }, "replies:test:vpc"); err != nil {
		t.Fatalf("SubmitWorkflowTx: %v", err)
	}

	broker := &durableCaptureBroker{}
	dispatcher := NewOutboxDispatcher(repository, broker, "dispatcher-test", 10)
	processed, err := dispatcher.DispatchOnce(context.Background())
	if err != nil {
		t.Fatalf("DispatchOnce: %v", err)
	}
	if processed != 1 || len(broker.tasks) != 1 {
		t.Fatalf("processed/published = %d/%d, want 1/1", processed, len(broker.tasks))
	}

	var outboxStatus, taskStatus, asynqTaskID string
	if err := db.QueryRow(`SELECT status FROM outbox_events WHERE aggregate_id = $1`, broker.tasks[0].Metadata[MetadataKeyTaskID]).Scan(&outboxStatus); err != nil {
		t.Fatalf("load outbox: %v", err)
	}
	if err := db.QueryRow(`SELECT status, asynq_task_id FROM tasks WHERE id = $1`, broker.tasks[0].Metadata[MetadataKeyTaskID]).Scan(&taskStatus, &asynqTaskID); err != nil {
		t.Fatalf("load task: %v", err)
	}
	if outboxStatus != "published" || taskStatus != "queued" || asynqTaskID == "" {
		t.Fatalf("outbox/task/asynq = %s/%s/%s", outboxStatus, taskStatus, asynqTaskID)
	}
}

func TestHandleReplyTxConcurrentDuplicatesAdvanceAndCreateNextOutboxOnce(t *testing.T) {
	db := openDurableTestDB(t)
	resourceID := insertDurableTestVPC(t, db)
	t.Cleanup(func() { cleanupDurableTestWorkflow(t, db, resourceID) })
	repository := NewWorkflowRepository(db, "az-nsp-vpc")
	definition := durableTestWorkflow(resourceID)
	publishedTask := submitAndDispatchFirstTask(t, repository, definition)
	replyTask := durableReplyTask(t, publishedTask, ReplyStatusSuccess, uuid.NewString())

	const duplicates = 100
	start := make(chan struct{})
	var ready sync.WaitGroup
	var done sync.WaitGroup
	ready.Add(duplicates)
	done.Add(duplicates)
	var applied atomic.Int32
	var duplicate atomic.Int32
	errCh := make(chan error, duplicates)
	for i := 0; i < duplicates; i++ {
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			decision, err := repository.HandleReplyTx(context.Background(), "az-vpc-reply", replyTask)
			if err != nil {
				errCh <- err
				return
			}
			switch decision {
			case ReplyDecisionApplied:
				applied.Add(1)
			case ReplyDecisionDuplicate:
				duplicate.Add(1)
			default:
				errCh <- &unexpectedReplyDecisionError{decision: decision}
			}
		}()
	}
	ready.Wait()
	close(start)
	done.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("HandleReplyTx: %v", err)
	}
	if applied.Load() != 1 || duplicate.Load() != duplicates-1 {
		t.Fatalf("applied/duplicate = %d/%d, want 1/%d", applied.Load(), duplicate.Load(), duplicates-1)
	}

	var inboxCount, completedTasks, nextOutbox, completedResources int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_events WHERE consumer_name = 'az-vpc-reply' AND event_id = $1`, replyTask.Metadata[MetadataKeyEventID]).Scan(&inboxCount); err != nil {
		t.Fatalf("count inbox: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE workflow_id = $1 AND status = 'completed'`, definition.WorkflowID).Scan(&completedTasks); err != nil {
		t.Fatalf("count completed tasks: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM outbox_events WHERE aggregate_id IN (SELECT id FROM tasks WHERE workflow_id = $1 AND task_order = 2)`, definition.WorkflowID).Scan(&nextOutbox); err != nil {
		t.Fatalf("count next outbox: %v", err)
	}
	if err := db.QueryRow(`SELECT completed_tasks FROM vpc_resources WHERE id = $1`, resourceID).Scan(&completedResources); err != nil {
		t.Fatalf("load resource count: %v", err)
	}
	if inboxCount != 1 || completedTasks != 1 || nextOutbox != 1 || completedResources != 1 {
		t.Fatalf("inbox/completed/next/resource = %d/%d/%d/%d, want 1/1/1/1", inboxCount, completedTasks, nextOutbox, completedResources)
	}
}

func TestHandleReplyTxRejectsCommandIdentityMismatchBeforeInbox(t *testing.T) {
	db := openDurableTestDB(t)
	resourceID := insertDurableTestVPC(t, db)
	t.Cleanup(func() { cleanupDurableTestWorkflow(t, db, resourceID) })
	repository := NewWorkflowRepository(db, "az-nsp-vpc")
	publishedTask := submitAndDispatchFirstTask(t, repository, durableTestWorkflow(resourceID))
	replyTask := durableReplyTask(t, publishedTask, ReplyStatusSuccess, uuid.NewString())
	var reply ReplyPayload
	if err := json.Unmarshal(replyTask.Payload, &reply); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	reply.OperationKey = "switch:other-step:other-resource:gen:1"
	replyTask.Payload, _ = json.Marshal(reply)

	if _, err := repository.HandleReplyTx(context.Background(), "az-vpc-reply", replyTask); err == nil {
		t.Fatal("reply with mismatched operation key was accepted")
	}
	var inboxCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_events WHERE consumer_name = 'az-vpc-reply' AND event_id = $1`, reply.EventID).Scan(&inboxCount); err != nil {
		t.Fatalf("count inbox: %v", err)
	}
	if inboxCount != 0 {
		t.Fatalf("invalid reply created %d inbox records", inboxCount)
	}
}

func TestHandleReplyTxRejectsEventIDPayloadConflict(t *testing.T) {
	db := openDurableTestDB(t)
	resourceID := insertDurableTestVPC(t, db)
	t.Cleanup(func() { cleanupDurableTestWorkflow(t, db, resourceID) })
	repository := NewWorkflowRepository(db, "az-nsp-vpc")
	definition := durableTestWorkflow(resourceID)
	publishedTask := submitAndDispatchFirstTask(t, repository, definition)
	eventID := uuid.NewString()
	success := durableReplyTask(t, publishedTask, ReplyStatusSuccess, eventID)
	if decision, err := repository.HandleReplyTx(context.Background(), "az-vpc-reply", success); err != nil || decision != ReplyDecisionApplied {
		t.Fatalf("first reply decision=%s err=%v", decision, err)
	}
	conflict := durableReplyTask(t, publishedTask, ReplyStatusFailed, eventID)
	if _, err := repository.HandleReplyTx(context.Background(), "az-vpc-reply", conflict); err == nil {
		t.Fatal("same event ID with different payload was accepted as duplicate")
	}
}

func TestHandleReplyTxFinalStepUsesTaskAggregateForResourceTerminalState(t *testing.T) {
	db := openDurableTestDB(t)
	resourceID := insertDurableTestVPC(t, db)
	t.Cleanup(func() { cleanupDurableTestWorkflow(t, db, resourceID) })
	repository := NewWorkflowRepository(db, "az-nsp-vpc")
	definition := durableTestWorkflow(resourceID)
	firstTask := submitAndDispatchFirstTask(t, repository, definition)
	if decision, err := repository.HandleReplyTx(context.Background(), "az-vpc-reply", durableReplyTask(t, firstTask, ReplyStatusSuccess, uuid.NewString())); err != nil || decision != ReplyDecisionApplied {
		t.Fatalf("first reply decision=%s err=%v", decision, err)
	}

	broker := &durableCaptureBroker{}
	dispatcher := NewOutboxDispatcher(repository, broker, "dispatcher-final", 10)
	if processed, err := dispatcher.DispatchOnce(context.Background()); err != nil || processed != 1 {
		t.Fatalf("dispatch second task: processed=%d err=%v", processed, err)
	}
	if decision, err := repository.HandleReplyTx(context.Background(), "az-vpc-reply", durableReplyTask(t, broker.tasks[0], ReplyStatusSuccess, uuid.NewString())); err != nil || decision != ReplyDecisionApplied {
		t.Fatalf("second reply decision=%s err=%v", decision, err)
	}

	var status string
	var completed, failed int
	if err := db.QueryRow(`SELECT status::text, completed_tasks, failed_tasks FROM vpc_resources WHERE id = $1`, resourceID).Scan(&status, &completed, &failed); err != nil {
		t.Fatalf("load resource: %v", err)
	}
	if status != "running" || completed != 2 || failed != 0 {
		t.Fatalf("resource status/completed/failed = %s/%d/%d, want running/2/0", status, completed, failed)
	}
}

func TestDurableWorkflowAdvancesPersistedOperationToTerminal(t *testing.T) {
	db := openDurableTestDB(t)
	resourceID := insertDurableTestVPC(t, db)
	definition := durableTestWorkflow(resourceID)
	insertDurableTestOperation(t, db, definition.OperationID, resourceID)
	t.Cleanup(func() {
		cleanupDurableTestWorkflow(t, db, resourceID)
		if _, err := db.Exec(`DELETE FROM orchestration_operations WHERE operation_id = $1`, definition.OperationID); err != nil {
			t.Errorf("delete operation: %v", err)
		}
	})
	repository := NewWorkflowRepository(db, "az-nsp-vpc")
	publishedTask := submitAndDispatchFirstTask(t, repository, definition)
	assertDurableOperationStatus(t, db, definition.OperationID, "running")
	if decision, err := repository.HandleReplyTx(context.Background(), "az-vpc-reply", durableReplyTask(t, publishedTask, ReplyStatusSuccess, uuid.NewString())); err != nil || decision != ReplyDecisionApplied {
		t.Fatalf("first reply decision=%s err=%v", decision, err)
	}
	broker := &durableCaptureBroker{}
	if processed, err := NewOutboxDispatcher(repository, broker, "dispatcher-operation", 10).DispatchOnce(context.Background()); err != nil || processed != 1 {
		t.Fatalf("dispatch second: processed=%d err=%v", processed, err)
	}
	if decision, err := repository.HandleReplyTx(context.Background(), "az-vpc-reply", durableReplyTask(t, broker.tasks[0], ReplyStatusSuccess, uuid.NewString())); err != nil || decision != ReplyDecisionApplied {
		t.Fatalf("second reply decision=%s err=%v", decision, err)
	}
	assertDurableOperationStatus(t, db, definition.OperationID, "succeeded")
}

func TestHandleReplyTxRecordsAndIgnoresStaleGeneration(t *testing.T) {
	db := openDurableTestDB(t)
	resourceID := insertDurableTestVPC(t, db)
	t.Cleanup(func() { cleanupDurableTestWorkflow(t, db, resourceID) })
	repository := NewWorkflowRepository(db, "az-nsp-vpc")
	definition := durableTestWorkflow(resourceID)
	definition.Generation = 3
	publishedTask := submitAndDispatchFirstTask(t, repository, definition)
	replyTask := durableReplyTask(t, publishedTask, ReplyStatusSuccess, uuid.NewString())
	var reply ReplyPayload
	if err := json.Unmarshal(replyTask.Payload, &reply); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	reply.Generation = 2
	replyTask.Metadata[MetadataKeyGeneration] = "2"
	replyTask.Payload, _ = json.Marshal(reply)

	decision, err := repository.HandleReplyTx(context.Background(), "az-vpc-reply", replyTask)
	if err != nil || decision != ReplyDecisionStale {
		t.Fatalf("stale reply decision=%s err=%v", decision, err)
	}
	var taskStatus string
	var inboxResult string
	if err := db.QueryRow(`SELECT status FROM tasks WHERE id = $1`, reply.TaskID).Scan(&taskStatus); err != nil {
		t.Fatalf("load task: %v", err)
	}
	if err := db.QueryRow(`SELECT result_code FROM inbox_events WHERE consumer_name = 'az-vpc-reply' AND event_id = $1`, reply.EventID).Scan(&inboxResult); err != nil {
		t.Fatalf("load inbox: %v", err)
	}
	if taskStatus != "queued" || inboxResult != "stale" {
		t.Fatalf("task/inbox = %s/%s, want queued/stale", taskStatus, inboxResult)
	}
}

func TestHandleReplyTxCannotCrossCurrentResourceGeneration(t *testing.T) {
	db := openDurableTestDB(t)
	resourceID := insertDurableTestVPC(t, db)
	t.Cleanup(func() { cleanupDurableTestWorkflow(t, db, resourceID) })
	repository := NewWorkflowRepository(db, "az-nsp-vpc")
	definition := durableTestWorkflow(resourceID)
	publishedTask := submitAndDispatchFirstTask(t, repository, definition)
	if _, err := db.Exec(`UPDATE vpc_resources SET generation = 2, current_operation_id = $1 WHERE id = $2`, uuid.NewString(), resourceID); err != nil {
		t.Fatalf("advance resource generation: %v", err)
	}
	replyTask := durableReplyTask(t, publishedTask, ReplyStatusSuccess, uuid.NewString())

	decision, err := repository.HandleReplyTx(context.Background(), "az-vpc-reply", replyTask)
	if err != nil || decision != ReplyDecisionStale {
		t.Fatalf("old resource generation decision=%s err=%v", decision, err)
	}
	var taskStatus string
	if err := db.QueryRow(`SELECT status FROM tasks WHERE id = $1`, publishedTask.Metadata[MetadataKeyTaskID]).Scan(&taskStatus); err != nil {
		t.Fatalf("load task: %v", err)
	}
	if taskStatus != "queued" {
		t.Fatalf("stale generation changed task status to %s", taskStatus)
	}
}

func TestHandleReplyTxAcknowledgesReplyAfterResourceWasDeleted(t *testing.T) {
	db := openDurableTestDB(t)
	resourceID := insertDurableTestVPC(t, db)
	t.Cleanup(func() { cleanupDurableTestWorkflow(t, db, resourceID) })
	repository := NewWorkflowRepository(db, "az-nsp-vpc")
	publishedTask := submitAndDispatchFirstTask(t, repository, durableTestWorkflow(resourceID))
	replyTask := durableReplyTask(t, publishedTask, ReplyStatusSuccess, uuid.NewString())
	var reply ReplyPayload
	if err := json.Unmarshal(replyTask.Payload, &reply); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM vpc_resources WHERE id = $1`, resourceID); err != nil {
		t.Fatalf("delete resource before late reply: %v", err)
	}
	decision, err := repository.HandleReplyTx(t.Context(), "az-vpc-reply", replyTask)
	if err != nil || decision != ReplyDecisionStale {
		t.Fatalf("late deleted-resource reply decision=%s err=%v", decision, err)
	}
	var resultCode string
	if err := db.QueryRow(`
		SELECT result_code FROM inbox_events
		WHERE consumer_name = 'az-vpc-reply' AND event_id = $1
	`, reply.EventID).Scan(&resultCode); err != nil {
		t.Fatalf("load late reply inbox result: %v", err)
	}
	if resultCode != "stale_resource_generation" {
		t.Fatalf("late reply result = %q", resultCode)
	}
}

type unexpectedReplyDecisionError struct {
	decision ReplyDecision
}

func (e *unexpectedReplyDecisionError) Error() string {
	return "unexpected reply decision: " + string(e.decision)
}

func submitAndDispatchFirstTask(t *testing.T, repository *WorkflowRepository, definition WorkflowDef) *taskqueue.Task {
	t.Helper()
	if _, err := repository.SubmitWorkflowTx(context.Background(), definition, func(string, int) string { return "tasks:test:switch" }, "replies:test:vpc"); err != nil {
		t.Fatalf("SubmitWorkflowTx: %v", err)
	}
	broker := &durableCaptureBroker{}
	dispatcher := NewOutboxDispatcher(repository, broker, "dispatcher-test", 10)
	if processed, err := dispatcher.DispatchOnce(context.Background()); err != nil || processed != 1 {
		t.Fatalf("DispatchOnce: processed=%d err=%v", processed, err)
	}
	return broker.tasks[0]
}

func durableReplyTask(t *testing.T, published *taskqueue.Task, status ReplyStatus, eventID string) *taskqueue.Task {
	t.Helper()
	stepIndex, _ := strconv.Atoi(published.Metadata[MetadataKeyStepIndex])
	totalSteps, _ := strconv.Atoi(published.Metadata[MetadataKeyTotalSteps])
	generation, _ := strconv.ParseInt(published.Metadata[MetadataKeyGeneration], 10, 64)
	attempt, _ := strconv.Atoi(published.Metadata[MetadataKeyAttempt])
	stepOrdinal, _ := strconv.Atoi(published.Metadata[MetadataKeyStepOrdinal])
	payload, err := json.Marshal(ReplyPayload{
		ProtocolVersion: TaskProtocolVersion,
		EventID:         eventID,
		OperationID:     published.Metadata[MetadataKeyOperationID],
		RootOperationID: published.Metadata[MetadataKeyRootOperationID],
		WorkflowID:      published.Metadata[MetadataKeyWorkflowID],
		TaskID:          published.Metadata[MetadataKeyTaskID],
		Generation:      generation,
		Attempt:         attempt,
		StepName:        published.Metadata[MetadataKeyStepName],
		StepOrdinal:     stepOrdinal,
		OperationKey:    published.Metadata[MetadataKeyOperationKey],
		DesiredHash:     published.Metadata[MetadataKeyDesiredHash],
		OccurredAt:      time.Now().UTC(),
		TaskType:        published.Type,
		ResourceID:      published.Metadata[MetadataKeyResourceID],
		ResourceType:    published.Metadata[MetadataKeyResourceType],
		StepIndex:       stepIndex,
		TotalSteps:      totalSteps,
		FinalFailure:    status == ReplyStatusFailed,
		Status:          status,
		Result:          json.RawMessage(`{"ok":true}`),
		Error:           map[bool]string{true: "device failed"}[status == ReplyStatusFailed],
	})
	if err != nil {
		t.Fatalf("marshal reply: %v", err)
	}
	metadata := make(map[string]string, len(published.Metadata))
	for key, value := range published.Metadata {
		metadata[key] = value
	}
	metadata[MetadataKeyEventID] = eventID
	return &taskqueue.Task{Type: ReplyTaskType, Payload: payload, Queue: published.Reply.Queue, Metadata: metadata}
}

func durableTestWorkflow(resourceID string) WorkflowDef {
	return WorkflowDef{
		OperationID:  uuid.NewString(),
		WorkflowID:   uuid.NewString(),
		Generation:   1,
		ResourceType: models.ResourceTypeVPC,
		ResourceID:   resourceID,
		AZ:           "az-test",
		Steps: []WorkflowStep{
			{TaskType: "create_vrf_on_switch", TaskName: "create vrf", DeviceType: "switch", Priority: 3, MaxRetries: 3, Payload: []byte(`{"step":1}`)},
			{TaskType: "create_vlan_subinterface", TaskName: "create vlan", DeviceType: "switch", Priority: 3, MaxRetries: 3, Payload: []byte(`{"step":2}`)},
		},
	}
}

func openDurableTestDB(t *testing.T) *sql.DB {
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
	for _, migrationPath := range []string{
		"../db/migrations/001_init_postgresql.sql",
		"../db/migrations/004_create_pccn_tables.sql",
		"../db/migrations/005_create_operations.sql",
		"../db/migrations/006_create_outbox_inbox.sql",
	} {
		migration, err := os.ReadFile(migrationPath)
		if err != nil {
			t.Fatalf("read migration %s: %v", migrationPath, err)
		}
		if _, err := db.Exec(string(migration)); err != nil {
			t.Fatalf("apply migration %s: %v", migrationPath, err)
		}
	}
	return db
}

func insertDurableTestVPC(t *testing.T, db *sql.DB) string {
	t.Helper()
	resourceID := uuid.NewString()
	if _, err := db.Exec(`
		INSERT INTO vpc_resources (id, vpc_name, region, az, status, total_tasks, completed_tasks, failed_tasks)
		VALUES ($1, $2, 'region-test', 'az-test', 'pending', 0, 0, 0)
	`, resourceID, "vpc-"+resourceID); err != nil {
		t.Fatalf("insert VPC: %v", err)
	}
	return resourceID
}

func insertDurableTestOperation(t *testing.T, db *sql.DB, operationID, resourceID string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO orchestration_operations (
			operation_id, root_operation_id, owner_service, caller_scope, route_scope,
			operation_type, target_scope, idempotency_key, request_hash_version,
			request_hash, request_payload, resource_type, resource_id, generation, status
		) VALUES ($1, $1, 'az-nsp-vpc', 'top-nsp-vpc', 'POST /api/v1/vpc',
		          'create_vpc', $2, $3, 1, $4, '{}'::jsonb, 'vpc', $2, 1, 'accepted')
	`, operationID, resourceID, "key-"+operationID, strings.Repeat("0", 64)); err != nil {
		t.Fatalf("insert operation: %v", err)
	}
}

func assertDurableOperationStatus(t *testing.T, db *sql.DB, operationID, want string) {
	t.Helper()
	var status string
	if err := db.QueryRow(`SELECT status FROM orchestration_operations WHERE operation_id = $1`, operationID).Scan(&status); err != nil {
		t.Fatalf("load operation: %v", err)
	}
	if status != want {
		t.Fatalf("operation status = %s, want %s", status, want)
	}
}

func cleanupDurableTestWorkflow(t *testing.T, db *sql.DB, resourceID string) {
	t.Helper()
	if _, err := db.Exec(`DELETE FROM outbox_events WHERE aggregate_id IN (SELECT id FROM tasks WHERE resource_id = $1)`, resourceID); err != nil {
		t.Errorf("delete outbox: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM tasks WHERE resource_id = $1`, resourceID); err != nil {
		t.Errorf("delete tasks: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM orchestration_operations WHERE resource_id = $1`, resourceID); err != nil {
		t.Errorf("delete operations: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM vpc_resources WHERE id = $1`, resourceID); err != nil {
		t.Errorf("delete VPC: %v", err)
	}
}
