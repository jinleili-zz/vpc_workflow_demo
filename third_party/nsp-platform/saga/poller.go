// File: poller.go
// Package saga - Polling worker for async SAGA steps

package saga

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// PollerConfig holds configuration for the Poller.
type PollerConfig struct {
	// ScanInterval is the interval between poll task scans (default: 3s).
	ScanInterval time.Duration
	// BatchSize is the maximum number of poll tasks to process per scan (default: 20).
	BatchSize int
	// InstanceID is the unique identifier for this instance (for distributed locking).
	InstanceID string
}

// DefaultPollerConfig returns the default poller configuration.
func DefaultPollerConfig() *PollerConfig {
	return &PollerConfig{
		ScanInterval: 3 * time.Second,
		BatchSize:    20,
		InstanceID:   "",
	}
}

// PollResult indicates the result of a poll operation.
type PollResult int

const (
	// PollResultPending indicates the async operation is still in progress.
	PollResultPending PollResult = iota
	// PollResultSuccess indicates the async operation succeeded.
	PollResultSuccess
	// PollResultFailure indicates the async operation failed.
	PollResultFailure
	// PollResultTimeout indicates the poll attempts exceeded the maximum.
	PollResultTimeout
	// PollResultError indicates an error occurred during polling.
	PollResultError
)

// Poller handles polling for async step completion.
type Poller struct {
	store      Store
	executor   *Executor
	config     *PollerConfig
	notifyMu   sync.RWMutex
	notifyChan map[string]chan PollResult
	stopCh     chan struct{}
	wg         sync.WaitGroup
}

// NewPoller creates a new Poller with the given store, executor, and configuration.
func NewPoller(store Store, executor *Executor, cfg *PollerConfig) *Poller {
	if cfg == nil {
		cfg = DefaultPollerConfig()
	}

	return &Poller{
		store:      store,
		executor:   executor,
		config:     cfg,
		notifyChan: make(map[string]chan PollResult),
		stopCh:     make(chan struct{}),
	}
}

// Start begins the polling loop in a background goroutine.
func (p *Poller) Start(ctx context.Context) {
	p.wg.Add(1)
	go p.pollLoop(ctx)
}

// Stop gracefully stops the poller and waits for all goroutines to finish.
func (p *Poller) Stop() {
	close(p.stopCh)
	p.wg.Wait()
}

// RegisterNotify creates or returns an existing notification channel for a transaction.
// The Coordinator should call this before starting an async step.
func (p *Poller) RegisterNotify(txID string) chan PollResult {
	p.notifyMu.Lock()
	defer p.notifyMu.Unlock()

	// Return existing channel if any (avoid overwriting)
	if ch, exists := p.notifyChan[txID]; exists {
		return ch
	}

	ch := make(chan PollResult, 1)
	p.notifyChan[txID] = ch
	return ch
}

// UnregisterNotify removes the notification channel for a transaction.
func (p *Poller) UnregisterNotify(txID string) {
	p.notifyMu.Lock()
	defer p.notifyMu.Unlock()

	if ch, exists := p.notifyChan[txID]; exists {
		close(ch)
		delete(p.notifyChan, txID)
	}
}

// notify sends a poll result to the registered channel for a transaction.
func (p *Poller) notify(txID string, result PollResult) {
	p.notifyMu.RLock()
	ch, exists := p.notifyChan[txID]
	p.notifyMu.RUnlock()

	if exists {
		select {
		case ch <- result:
		default:
			// Channel is full or closed, skip
		}
	}
}

// pollLoop is the main polling loop.
func (p *Poller) pollLoop(ctx context.Context) {
	defer p.wg.Done()

	ticker := time.NewTicker(p.config.ScanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-p.stopCh:
			return
		case <-ticker.C:
			p.scanAndProcess(ctx)
		}
	}
}

// scanAndProcess acquires and processes poll tasks.
func (p *Poller) scanAndProcess(ctx context.Context) {
	// Acquire poll tasks
	tasks, err := p.store.AcquirePollTasks(ctx, p.config.InstanceID, p.config.BatchSize)
	if err != nil {
		fmt.Printf("failed to acquire poll tasks: %v\n", err)
		return
	}

	// Process each task in a separate goroutine
	var wg sync.WaitGroup
	for _, task := range tasks {
		wg.Add(1)
		go func(t *PollTask) {
			defer wg.Done()
			p.processPollTask(ctx, t)
		}(task)
	}
	wg.Wait()
}

// processPollTask processes a single poll task.
func (p *Poller) processPollTask(ctx context.Context, task *PollTask) {
	// Get step information
	step, err := p.store.GetStep(ctx, task.StepID)
	if err != nil {
		fmt.Printf("failed to get step %s: %v\n", task.StepID, err)
		p.releasePollTask(ctx, task.StepID)
		return
	}
	if step == nil {
		fmt.Printf("step %s not found, deleting poll task\n", task.StepID)
		p.store.DeletePollTask(ctx, task.StepID)
		return
	}

	// Check if step is still in polling state
	if step.Status != StepStatusPolling {
		// Step is no longer polling, delete the task
		p.store.DeletePollTask(ctx, task.StepID)
		return
	}

	// Get transaction information
	tx, err := p.store.GetTransaction(ctx, task.TransactionID)
	if err != nil || tx == nil {
		fmt.Printf("failed to get transaction %s: %v\n", task.TransactionID, err)
		p.releasePollTask(ctx, task.StepID)
		return
	}

	// Get all steps for template rendering
	allSteps, err := p.store.GetSteps(ctx, task.TransactionID)
	if err != nil {
		fmt.Printf("failed to get steps for transaction %s: %v\n", task.TransactionID, err)
		p.releasePollTask(ctx, task.StepID)
		return
	}

	// Execute poll request
	response, err := p.executor.Poll(ctx, tx, step, allSteps)
	if err != nil {
		fmt.Printf("poll request failed for step %s: %v\n", step.ID, err)
		p.releasePollTask(ctx, task.StepID)
		return
	}

	// Check poll result
	success, failure, err := MatchPollResult(response, step)
	if err != nil {
		fmt.Printf("failed to match poll result for step %s: %v\n", step.ID, err)
		p.releasePollTask(ctx, task.StepID)
		return
	}

	if success {
		// Poll succeeded
		p.handlePollSuccess(ctx, task, step, response)
		return
	}

	if failure {
		// Poll explicitly failed
		p.handlePollFailure(ctx, task, step)
		return
	}

	// Still processing - check if we've exceeded max poll attempts
	if step.PollCount >= step.PollMaxTimes {
		// Poll timeout
		p.handlePollTimeout(ctx, task, step)
		return
	}

	// Still processing - schedule next poll
	p.scheduleNextPoll(ctx, task, step)
}

// handlePollSuccess handles a successful poll result.
func (p *Poller) handlePollSuccess(ctx context.Context, task *PollTask, step *Step, response map[string]any) {
	// Update step response
	if err := p.store.UpdateStepResponse(ctx, step.ID, response); err != nil {
		fmt.Printf("failed to update step response: %v\n", err)
	}

	// Update step status to succeeded
	if err := p.store.UpdateStepStatus(ctx, step.ID, StepStatusSucceeded, ""); err != nil {
		fmt.Printf("failed to update step status to succeeded: %v\n", err)
	}

	// Delete poll task
	if err := p.store.DeletePollTask(ctx, task.StepID); err != nil {
		fmt.Printf("failed to delete poll task: %v\n", err)
	}

	// Notify coordinator
	p.notify(task.TransactionID, PollResultSuccess)
}

// handlePollFailure handles an explicit poll failure.
func (p *Poller) handlePollFailure(ctx context.Context, task *PollTask, step *Step) {
	// Update step status to failed
	if err := p.store.UpdateStepStatus(ctx, step.ID, StepStatusFailed, "poll returned failure value"); err != nil {
		fmt.Printf("failed to update step status to failed: %v\n", err)
	}

	// Delete poll task
	if err := p.store.DeletePollTask(ctx, task.StepID); err != nil {
		fmt.Printf("failed to delete poll task: %v\n", err)
	}

	// Notify coordinator
	p.notify(task.TransactionID, PollResultFailure)
}

// handlePollTimeout handles a poll timeout (exceeded max attempts).
func (p *Poller) handlePollTimeout(ctx context.Context, task *PollTask, step *Step) {
	// Update step status to failed
	errMsg := fmt.Sprintf("poll timeout: exceeded %d attempts", step.PollMaxTimes)
	if err := p.store.UpdateStepStatus(ctx, step.ID, StepStatusFailed, errMsg); err != nil {
		fmt.Printf("failed to update step status to failed: %v\n", err)
	}

	// Delete poll task
	if err := p.store.DeletePollTask(ctx, task.StepID); err != nil {
		fmt.Printf("failed to delete poll task: %v\n", err)
	}

	// Notify coordinator
	p.notify(task.TransactionID, PollResultTimeout)
}

// scheduleNextPoll schedules the next poll attempt.
func (p *Poller) scheduleNextPoll(ctx context.Context, task *PollTask, step *Step) {
	// Calculate next poll time
	nextPollAt := time.Now().Add(time.Duration(step.PollIntervalSec) * time.Second)

	// Increment poll count and update next poll time
	if err := p.store.IncrementStepPollCount(ctx, step.ID, nextPollAt); err != nil {
		fmt.Printf("failed to increment poll count: %v\n", err)
	}

	// Release the poll task (unlock it)
	p.releasePollTask(ctx, task.StepID)
}

// releasePollTask releases the lock on a poll task.
func (p *Poller) releasePollTask(ctx context.Context, stepID string) {
	if err := p.store.ReleasePollTask(ctx, stepID); err != nil {
		fmt.Printf("failed to release poll task: %v\n", err)
	}
}
