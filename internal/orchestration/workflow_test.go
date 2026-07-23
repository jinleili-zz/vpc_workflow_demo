package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/jinleili-zz/nsp-platform/taskqueue"

	"workflow_qoder/internal/models"
)

type captureBroker struct {
	mu        sync.Mutex
	task      *taskqueue.Task
	publishes int
}

type concurrentReplyTaskStore struct {
	mu          sync.Mutex
	readCount   int
	readBarrier chan struct{}
	readers     int
	updates     int
	terminal    bool
}

func newConcurrentReplyTaskStore(readers int) *concurrentReplyTaskStore {
	return &concurrentReplyTaskStore{readBarrier: make(chan struct{}), readers: readers}
}

func (*concurrentReplyTaskStore) BatchCreate(context.Context, []*models.Task) error { return nil }
func (*concurrentReplyTaskStore) GetByResourceID(context.Context, string) ([]*models.Task, error) {
	return nil, nil
}
func (s *concurrentReplyTaskStore) GetByResourceAndOrder(context.Context, string, int) (*models.Task, error) {
	s.mu.Lock()
	s.readCount++
	if s.readCount == s.readers {
		close(s.readBarrier)
	}
	s.mu.Unlock()
	<-s.readBarrier
	return &models.Task{ID: "task-1", Status: models.TaskStatusQueued}, nil
}
func (*concurrentReplyTaskStore) GetNextPendingTask(context.Context, string) (*models.Task, error) {
	return &models.Task{
		ID:           "task-2",
		ResourceType: models.ResourceTypeVPC,
		ResourceID:   "resource-1",
		TaskType:     "create_vlan_subinterface",
		TaskOrder:    2,
		TaskParams:   `{}`,
		Status:       models.TaskStatusPending,
		DeviceType:   "switch",
	}, nil
}
func (*concurrentReplyTaskStore) UpdateQueued(context.Context, string, string) error { return nil }
func (*concurrentReplyTaskStore) UpdateRetryProgress(context.Context, string, int, int, string) (bool, error) {
	return true, nil
}
func (s *concurrentReplyTaskStore) UpdateResult(context.Context, string, models.TaskStatus, any, string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal {
		return false, nil
	}
	s.terminal = true
	s.updates++
	return true, nil
}

type countingResourceStore struct {
	mu        sync.Mutex
	completed int
	failed    int
	statuses  int
}

func (*countingResourceStore) UpdateTotalTasks(context.Context, string, int) error { return nil }
func (s *countingResourceStore) IncrementCompletedTasks(context.Context, string) error {
	s.mu.Lock()
	s.completed++
	s.mu.Unlock()
	return nil
}
func (s *countingResourceStore) IncrementFailedTasks(context.Context, string) error {
	s.mu.Lock()
	s.failed++
	s.mu.Unlock()
	return nil
}
func (s *countingResourceStore) UpdateStatus(context.Context, string, models.ResourceStatus, string) error {
	s.mu.Lock()
	s.statuses++
	s.mu.Unlock()
	return nil
}

func (b *captureBroker) Publish(_ context.Context, task *taskqueue.Task) (*taskqueue.TaskInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.task = task
	b.publishes++
	return &taskqueue.TaskInfo{BrokerTaskID: "broker-task"}, nil
}

func (b *captureBroker) Close() error { return nil }

type publishTaskStore struct{}

func (publishTaskStore) BatchCreate(context.Context, []*models.Task) error { return nil }
func (publishTaskStore) GetByResourceID(context.Context, string) ([]*models.Task, error) {
	return nil, nil
}
func (publishTaskStore) GetByResourceAndOrder(context.Context, string, int) (*models.Task, error) {
	return nil, nil
}
func (publishTaskStore) GetNextPendingTask(context.Context, string) (*models.Task, error) {
	return nil, nil
}
func (publishTaskStore) UpdateQueued(context.Context, string, string) error { return nil }
func (publishTaskStore) UpdateRetryProgress(context.Context, string, int, int, string) (bool, error) {
	return true, nil
}
func (publishTaskStore) UpdateResult(context.Context, string, models.TaskStatus, any, string) (bool, error) {
	return true, nil
}

func TestPublishTaskMapsDatabaseMaxRetriesToBroker(t *testing.T) {
	t.Parallel()

	broker := &captureBroker{}
	manager := NewManager(broker, publishTaskStore{}, func(string, int) string { return "tasks:test" }, "replies:test")
	want := 7
	task := &models.Task{
		ID:           "task-1",
		ResourceID:   "resource-1",
		ResourceType: models.ResourceTypeVPC,
		TaskType:     "create_vrf_on_switch",
		TaskOrder:    1,
		TaskParams:   `{}`,
		MaxRetries:   want,
	}

	if err := manager.publishTask(context.Background(), task, 1); err != nil {
		t.Fatalf("publishTask: %v", err)
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if broker.task == nil || broker.task.MaxRetry == nil {
		t.Fatalf("published MaxRetry = nil, want %d", want)
	}
	if got := *broker.task.MaxRetry; got != want {
		t.Fatalf("published MaxRetry = %d, want %d", got, want)
	}
}

func TestConcurrentDuplicateIntermediateRepliesPublishNextStepOnce(t *testing.T) {
	const duplicates = 20
	taskStore := newConcurrentReplyTaskStore(duplicates)
	resourceStore := &countingResourceStore{}
	broker := &captureBroker{}
	manager := NewManager(broker, taskStore, func(string, int) string { return "tasks:test" }, "replies:test")
	manager.RegisterResourceStore(models.ResourceTypeVPC, resourceStore)

	runConcurrentReplies(t, manager, duplicates, ReplyPayload{Status: ReplyStatusSuccess, Result: json.RawMessage(`{"ok":true}`)}, 2)

	resourceStore.mu.Lock()
	if resourceStore.completed != 1 {
		t.Fatalf("completed increments = %d, want 1", resourceStore.completed)
	}
	if resourceStore.statuses != 0 {
		t.Fatalf("terminal status updates = %d, want 0 for intermediate step", resourceStore.statuses)
	}
	resourceStore.mu.Unlock()

	broker.mu.Lock()
	defer broker.mu.Unlock()
	if broker.publishes != 1 {
		t.Fatalf("next-step publishes = %d, want 1", broker.publishes)
	}
}

func TestConcurrentDuplicateFinalFailureRepliesAdvanceWorkflowOnce(t *testing.T) {
	const duplicates = 20
	taskStore := newConcurrentReplyTaskStore(duplicates)
	resourceStore := &countingResourceStore{}
	manager := NewManager(&captureBroker{}, taskStore, func(string, int) string { return "tasks:test" }, "replies:test")
	manager.RegisterResourceStore(models.ResourceTypeVPC, resourceStore)

	runConcurrentReplies(t, manager, duplicates, ReplyPayload{Status: ReplyStatusFailed, Error: "device failed", FinalFailure: true}, 1)

	resourceStore.mu.Lock()
	defer resourceStore.mu.Unlock()
	if resourceStore.failed != 1 {
		t.Fatalf("failed increments = %d, want 1", resourceStore.failed)
	}
	if resourceStore.statuses != 1 {
		t.Fatalf("terminal status updates = %d, want 1", resourceStore.statuses)
	}
}

func TestConcurrentDuplicateRepliesAdvanceWorkflowOnce(t *testing.T) {
	const duplicates = 20
	taskStore := newConcurrentReplyTaskStore(duplicates)
	resourceStore := &countingResourceStore{}
	manager := NewManager(&captureBroker{}, taskStore, func(string, int) string { return "tasks:test" }, "replies:test")
	manager.RegisterResourceStore(models.ResourceTypeVPC, resourceStore)

	runConcurrentReplies(t, manager, duplicates, ReplyPayload{Status: ReplyStatusSuccess, Result: json.RawMessage(`{"ok":true}`)}, 1)

	resourceStore.mu.Lock()
	defer resourceStore.mu.Unlock()
	if resourceStore.completed != 1 {
		t.Fatalf("completed increments = %d, want 1", resourceStore.completed)
	}
	if resourceStore.statuses != 1 {
		t.Fatalf("terminal status updates = %d, want 1", resourceStore.statuses)
	}
}

func runConcurrentReplies(t *testing.T, manager *Manager, duplicates int, reply ReplyPayload, totalSteps int) {
	t.Helper()
	payload, err := json.Marshal(reply)
	if err != nil {
		t.Fatalf("marshal reply: %v", err)
	}
	replyTask := &taskqueue.Task{
		Payload: payload,
		Metadata: map[string]string{
			MetadataKeyResourceID:   "resource-1",
			MetadataKeyResourceType: string(models.ResourceTypeVPC),
			MetadataKeyStepIndex:    "0",
			MetadataKeyTotalSteps:   fmt.Sprintf("%d", totalSteps),
		},
	}

	var wg sync.WaitGroup
	errCh := make(chan error, duplicates)
	for i := 0; i < duplicates; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errCh <- manager.HandleReply(context.Background(), replyTask)
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("HandleReply: %v", err)
		}
	}
}
