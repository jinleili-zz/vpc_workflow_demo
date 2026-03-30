package orchestration

import "encoding/json"

const (
	ReplyTaskType = "workflow_step_reply"

	MetadataKeyResourceID   = "resource_id"
	MetadataKeyResourceType = "resource_type"
	MetadataKeyStepIndex    = "step_index"
	MetadataKeyTotalSteps   = "total_steps"
)

type ReplyStatus string

const (
	ReplyStatusSuccess ReplyStatus = "success"
	ReplyStatusFailed  ReplyStatus = "failed"
)

type ReplyPayload struct {
	TaskType     string          `json:"task_type"`
	ResourceID   string          `json:"resource_id"`
	ResourceType string          `json:"resource_type"`
	StepIndex    int             `json:"step_index"`
	TotalSteps   int             `json:"total_steps"`
	RetryCount   int             `json:"retry_count,omitempty"`
	MaxRetries   int             `json:"max_retries,omitempty"`
	FinalFailure bool            `json:"final_failure,omitempty"`
	Status       ReplyStatus     `json:"status"`
	Result       json.RawMessage `json:"result,omitempty"`
	Error        string          `json:"error,omitempty"`
}
