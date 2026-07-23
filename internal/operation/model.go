package operation

import (
	"encoding/json"
	"strings"
	"time"
)

type Decision string

const (
	DecisionNew      Decision = "new"
	DecisionReplay   Decision = "replay"
	DecisionConflict Decision = "conflict"
)

type Status string

const (
	StatusAccepted           Status = "accepted"
	StatusDispatching        Status = "dispatching"
	StatusRunning            Status = "running"
	StatusSucceeded          Status = "succeeded"
	StatusFailed             Status = "failed"
	StatusCancelled          Status = "cancelled"
	StatusCompensating       Status = "compensating"
	StatusCompensated        Status = "compensated"
	StatusCompensationFailed Status = "compensation_failed"
	StatusDeleted            Status = "deleted"
	StatusDeleteFailed       Status = "delete_failed"
)

type Operation struct {
	OperationID        string          `json:"operation_id"`
	RootOperationID    string          `json:"root_operation_id"`
	ParentOperationID  string          `json:"parent_operation_id,omitempty"`
	OwnerService       string          `json:"owner_service"`
	CallerScope        string          `json:"-"`
	RouteScope         string          `json:"route_scope"`
	OperationType      string          `json:"operation_type"`
	TargetScope        string          `json:"target_scope"`
	IdempotencyKey     string          `json:"-"`
	RequestHashVersion int16           `json:"-"`
	RequestHash        string          `json:"-"`
	RequestPayload     json.RawMessage `json:"-"`
	ResourceType       string          `json:"resource_type"`
	ResourceID         string          `json:"resource_id,omitempty"`
	Generation         int64           `json:"generation"`
	Status             Status          `json:"status"`
	ResponseCode       string          `json:"response_code,omitempty"`
	ResponsePayload    json.RawMessage `json:"response,omitempty"`
	ErrorCode          string          `json:"error_code,omitempty"`
	ErrorMessage       string          `json:"error_message,omitempty"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
	CompletedAt        *time.Time      `json:"completed_at,omitempty"`
	Version            int64           `json:"version"`
}

type BeginCommand struct {
	OperationID        string
	RootOperationID    string
	ParentOperationID  string
	OwnerService       string
	CallerScope        string
	RouteScope         string
	OperationType      string
	TargetScope        string
	IdempotencyKey     string
	RequestHashVersion int16
	RequestHash        string
	RequestPayload     json.RawMessage
	ResourceType       string
	ResourceID         string
	Generation         int64
}

func (s Status) Terminal() bool {
	switch s {
	case StatusSucceeded, StatusFailed, StatusCancelled,
		StatusCompensated, StatusCompensationFailed,
		StatusDeleted, StatusDeleteFailed:
		return true
	default:
		return false
	}
}

func (s Status) Valid() bool {
	switch s {
	case StatusAccepted, StatusDispatching, StatusRunning,
		StatusSucceeded, StatusFailed, StatusCancelled,
		StatusCompensating, StatusCompensated, StatusCompensationFailed,
		StatusDeleted, StatusDeleteFailed:
		return true
	default:
		return false
	}
}

// CanTransition selects the state machine from the stable operation action
// prefix. Unknown action families are rejected instead of silently choosing a
// graph.
func CanTransition(operationType string, current, next Status) bool {
	if !current.Valid() || !next.Valid() || current.Terminal() {
		return false
	}
	deletion, validType := operationStateMachine(operationType)
	if !validType {
		return false
	}
	switch current {
	case StatusAccepted:
		if deletion {
			return next == StatusDispatching || next == StatusDeleted || next == StatusDeleteFailed
		}
		return next == StatusDispatching || next == StatusFailed || next == StatusCancelled
	case StatusDispatching:
		if deletion {
			return next == StatusRunning || next == StatusDeleted || next == StatusDeleteFailed
		}
		return next == StatusRunning || next == StatusFailed || next == StatusCompensating
	case StatusRunning:
		if deletion {
			return next == StatusDeleted || next == StatusDeleteFailed
		}
		return next == StatusSucceeded || next == StatusFailed || next == StatusCompensating
	case StatusCompensating:
		return !deletion && (next == StatusCompensated || next == StatusCompensationFailed)
	default:
		return false
	}
}

func operationStateMachine(operationType string) (deletion bool, valid bool) {
	switch {
	case strings.HasPrefix(operationType, "delete_"):
		return true, true
	case strings.HasPrefix(operationType, "create_"),
		strings.HasPrefix(operationType, "apply_"),
		strings.HasPrefix(operationType, "update_"):
		return false, true
	default:
		return false, false
	}
}
