package orchestration

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"
)

func DesiredSpecHash(payload []byte) (string, error) {
	var normalized any
	if err := json.Unmarshal(payload, &normalized); err != nil {
		return "", fmt.Errorf("invalid desired spec JSON: %w", err)
	}
	canonical, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("canonicalize desired spec: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return fmt.Sprintf("%x", sum[:]), nil
}

const (
	ReplyTaskType = "workflow_step_reply"

	MetadataKeyResourceID      = "resource_id"
	MetadataKeyResourceType    = "resource_type"
	MetadataKeyStepIndex       = "step_index"
	MetadataKeyTotalSteps      = "total_steps"
	MetadataKeyProtocolVersion = "protocol_version"
	MetadataKeyEventID         = "event_id"
	MetadataKeyOperationID     = "operation_id"
	MetadataKeyRootOperationID = "root_operation_id"
	MetadataKeyWorkflowID      = "workflow_id"
	MetadataKeyTaskID          = "task_id"
	MetadataKeyGeneration      = "generation"
	MetadataKeyAttempt         = "attempt"
	MetadataKeyStepName        = "step_name"
	MetadataKeyStepOrdinal     = "step_ordinal"
	MetadataKeyOperationKey    = "operation_key"
	MetadataKeyDesiredHash     = "desired_hash"
	MetadataKeyDeviceType      = "device_type"
)

type ReplyStatus string

const (
	ReplyStatusSuccess ReplyStatus = "success"
	ReplyStatusFailed  ReplyStatus = "failed"
)

type ReplyPayload struct {
	ProtocolVersion int16           `json:"schema_version,omitempty"`
	EventID         string          `json:"event_id,omitempty"`
	OperationID     string          `json:"operation_id,omitempty"`
	RootOperationID string          `json:"root_operation_id,omitempty"`
	WorkflowID      string          `json:"workflow_id,omitempty"`
	TaskID          string          `json:"task_id,omitempty"`
	Generation      int64           `json:"generation,omitempty"`
	Attempt         int             `json:"attempt,omitempty"`
	StepName        string          `json:"step_name,omitempty"`
	StepOrdinal     int             `json:"step_ordinal,omitempty"`
	OperationKey    string          `json:"operation_key,omitempty"`
	DesiredHash     string          `json:"desired_hash,omitempty"`
	OccurredAt      time.Time       `json:"occurred_at,omitempty"`
	TaskType        string          `json:"task_type"`
	ResourceID      string          `json:"resource_id"`
	ResourceType    string          `json:"resource_type"`
	StepIndex       int             `json:"step_index"`
	TotalSteps      int             `json:"total_steps"`
	RetryCount      int             `json:"retry_count,omitempty"`
	MaxRetries      int             `json:"max_retries,omitempty"`
	FinalFailure    bool            `json:"final_failure,omitempty"`
	Status          ReplyStatus     `json:"status"`
	Result          json.RawMessage `json:"result,omitempty"`
	Error           string          `json:"error,omitempty"`
}
