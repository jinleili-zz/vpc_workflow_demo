// Package saga provides a SAGA distributed transaction module for microservices.
// It implements the SAGA pattern with compensation-based rollback, supporting both
// synchronous and asynchronous steps with polling capabilities.
package saga

import (
	"errors"
	"time"
)

// StepType defines the type of a SAGA step.
type StepType string

const (
	// StepTypeSync indicates a synchronous step that completes immediately.
	StepTypeSync StepType = "sync"
	// StepTypeAsync indicates an asynchronous step that requires polling for completion.
	StepTypeAsync StepType = "async"
)

// TxStatus defines the status of a SAGA transaction.
type TxStatus string

const (
	// TxStatusPending indicates the transaction is created but not yet started.
	TxStatusPending TxStatus = "pending"
	// TxStatusRunning indicates the transaction is currently executing.
	TxStatusRunning TxStatus = "running"
	// TxStatusCompensating indicates the transaction is rolling back.
	TxStatusCompensating TxStatus = "compensating"
	// TxStatusSucceeded indicates the transaction completed successfully.
	TxStatusSucceeded TxStatus = "succeeded"
	// TxStatusFailed indicates the transaction failed and compensation completed.
	TxStatusFailed TxStatus = "failed"
)

// StepStatus defines the status of a SAGA step.
type StepStatus string

const (
	// StepStatusPending indicates the step has not started yet.
	StepStatusPending StepStatus = "pending"
	// StepStatusRunning indicates the step is currently executing.
	StepStatusRunning StepStatus = "running"
	// StepStatusPolling indicates the async step is waiting for polling result.
	StepStatusPolling StepStatus = "polling"
	// StepStatusSucceeded indicates the step completed successfully.
	StepStatusSucceeded StepStatus = "succeeded"
	// StepStatusFailed indicates the step execution failed.
	StepStatusFailed StepStatus = "failed"
	// StepStatusCompensating indicates the step is being compensated.
	StepStatusCompensating StepStatus = "compensating"
	// StepStatusCompensated indicates the step has been compensated.
	StepStatusCompensated StepStatus = "compensated"
	// StepStatusSkipped indicates the step was skipped (not executed, no compensation needed).
	StepStatusSkipped StepStatus = "skipped"
)

// Step represents a single step in a SAGA transaction.
type Step struct {
	// ID is the unique identifier for this step (UUID).
	ID string
	// TransactionID is the ID of the parent transaction.
	TransactionID string
	// Index is the position of this step in the transaction (0-based).
	Index int
	// Name is a human-readable name for this step.
	Name string
	// Type indicates whether this is a sync or async step.
	Type StepType
	// Status is the current status of this step.
	Status StepStatus

	// ActionMethod is the HTTP method for the forward action (e.g., "POST", "PUT").
	ActionMethod string
	// ActionURL is the URL for the forward action. Supports template variables.
	ActionURL string
	// ActionPayload is the request body for the forward action. Supports template variables.
	ActionPayload map[string]any
	// ActionResponse stores the response from the forward action.
	ActionResponse map[string]any
	// AuthAK specifies the access key used to sign outbound HTTP requests for this step.
	AuthAK string

	// CompensateMethod is the HTTP method for the compensation action.
	CompensateMethod string
	// CompensateURL is the URL for the compensation action. Supports template variables.
	CompensateURL string
	// CompensatePayload is the request body for compensation. Supports template variables.
	CompensatePayload map[string]any

	// PollURL is the URL for polling async step status. Only used when Type == StepTypeAsync.
	PollURL string
	// PollMethod is the HTTP method for polling (default: "GET").
	PollMethod string
	// PollIntervalSec is the interval between polls in seconds (default: 5).
	PollIntervalSec int
	// PollMaxTimes is the maximum number of poll attempts (default: 60).
	PollMaxTimes int
	// PollCount is the current number of poll attempts.
	PollCount int
	// PollSuccessPath is the JSONPath to extract the success indicator from poll response.
	PollSuccessPath string
	// PollSuccessValue is the expected value indicating success.
	PollSuccessValue string
	// PollFailurePath is the JSONPath to extract the failure indicator from poll response.
	PollFailurePath string
	// PollFailureValue is the expected value indicating failure.
	PollFailureValue string
	// NextPollAt is the scheduled time for the next poll.
	NextPollAt *time.Time

	// RetryCount is the current number of retry attempts.
	RetryCount int
	// MaxRetry is the maximum number of retry attempts (default: 3).
	MaxRetry int
	// LastError stores the last error message.
	LastError string
	// StartedAt is the time when the step started execution.
	StartedAt *time.Time
	// FinishedAt is the time when the step finished execution.
	FinishedAt *time.Time
}

// Transaction represents a SAGA transaction instance.
type Transaction struct {
	// ID is the unique identifier for this transaction (UUID).
	ID string
	// Status is the current status of this transaction.
	Status TxStatus
	// Payload is the global payload data accessible to all steps.
	Payload map[string]any
	// CurrentStep is the index of the current step being executed.
	CurrentStep int
	// CreatedAt is the time when the transaction was created.
	CreatedAt time.Time
	// UpdatedAt is the time when the transaction was last updated.
	UpdatedAt time.Time
	// FinishedAt is the time when the transaction finished (nil if not finished).
	FinishedAt *time.Time
	// TimeoutAt is the deadline for this transaction (nil if no timeout).
	TimeoutAt *time.Time
	// RetryCount is the number of times this transaction has been retried.
	RetryCount int
	// LastError stores the last error message.
	LastError string
}

// PollTask represents a polling task for an async step.
type PollTask struct {
	// ID is the auto-generated primary key.
	ID int64
	// StepID is the ID of the associated step.
	StepID string
	// TransactionID is the ID of the associated transaction.
	TransactionID string
	// NextPollAt is the scheduled time for the next poll.
	NextPollAt time.Time
	// LockedUntil is the deadline for the distributed lock.
	LockedUntil *time.Time
	// LockedBy is the instance ID that holds the lock.
	LockedBy string
}

// SagaDefinition defines the structure of a SAGA transaction.
type SagaDefinition struct {
	// ID is the unique identifier for this transaction (generated by engine).
	ID string
	// Name is a human-readable name for this transaction (for logging purposes).
	Name string
	// Steps is the ordered list of steps to execute.
	Steps []Step
	// TimeoutSec is the total timeout for this transaction in seconds (0 means no timeout).
	TimeoutSec int
	// Payload is the initial global payload data.
	Payload map[string]any
}

// Builder errors
var (
	// ErrNoSteps is returned when building a SAGA with no steps.
	ErrNoSteps = errors.New("saga must have at least one step")
	// ErrAsyncStepMissingPollConfig is returned when an async step is missing required poll configuration.
	ErrAsyncStepMissingPollConfig = errors.New("async step must have PollURL, PollSuccessPath, and PollSuccessValue")
	// ErrStepMissingAction is returned when a step is missing action configuration.
	ErrStepMissingAction = errors.New("step must have ActionMethod and ActionURL")
	// ErrStepMissingCompensate is returned when a step is missing compensation configuration.
	ErrStepMissingCompensate = errors.New("step must have CompensateMethod and CompensateURL")
)

// SagaBuilder provides a fluent interface for building SAGA definitions.
type SagaBuilder struct {
	name       string
	steps      []Step
	timeoutSec int
	payload    map[string]any
}

// NewSaga creates a new SagaBuilder with the given name.
func NewSaga(name string) *SagaBuilder {
	return &SagaBuilder{
		name:    name,
		steps:   make([]Step, 0),
		payload: make(map[string]any),
	}
}

// AddStep adds a step to the SAGA definition.
// Steps are executed in the order they are added.
func (b *SagaBuilder) AddStep(step Step) *SagaBuilder {
	b.steps = append(b.steps, step)
	return b
}

// WithTimeout sets the total timeout for the transaction in seconds.
// A value of 0 means no timeout.
func (b *SagaBuilder) WithTimeout(seconds int) *SagaBuilder {
	b.timeoutSec = seconds
	return b
}

// WithPayload sets the initial global payload data for the transaction.
func (b *SagaBuilder) WithPayload(payload map[string]any) *SagaBuilder {
	b.payload = payload
	return b
}

// Build validates and creates the SagaDefinition.
// Returns an error if validation fails.
func (b *SagaBuilder) Build() (*SagaDefinition, error) {
	// Validate step count
	if len(b.steps) == 0 {
		return nil, ErrNoSteps
	}

	// Validate each step
	for i, step := range b.steps {
		// Check action configuration
		if step.ActionMethod == "" || step.ActionURL == "" {
			return nil, ErrStepMissingAction
		}

		// Check compensation configuration
		if step.CompensateMethod == "" || step.CompensateURL == "" {
			return nil, ErrStepMissingCompensate
		}

		// Check async step poll configuration
		if step.Type == StepTypeAsync {
			if step.PollURL == "" || step.PollSuccessPath == "" || step.PollSuccessValue == "" {
				return nil, ErrAsyncStepMissingPollConfig
			}
		}

		// Set defaults
		if step.Type == "" {
			b.steps[i].Type = StepTypeSync
		}
		if step.PollMethod == "" {
			b.steps[i].PollMethod = "GET"
		}
		if step.PollIntervalSec == 0 {
			b.steps[i].PollIntervalSec = 5
		}
		if step.PollMaxTimes == 0 {
			b.steps[i].PollMaxTimes = 60
		}
		if step.MaxRetry == 0 {
			b.steps[i].MaxRetry = 3
		}

		// Set step index
		b.steps[i].Index = i
		b.steps[i].Status = StepStatusPending
	}

	return &SagaDefinition{
		Name:       b.name,
		Steps:      b.steps,
		TimeoutSec: b.timeoutSec,
		Payload:    b.payload,
	}, nil
}
