package models

import "time"

type ResourceStatus string

const (
	ResourceStatusPending  ResourceStatus = "pending"
	ResourceStatusCreating ResourceStatus = "creating"
	ResourceStatusRunning  ResourceStatus = "running"
	ResourceStatusFailed   ResourceStatus = "failed"
	ResourceStatusDeleting ResourceStatus = "deleting"
	ResourceStatusDeleted  ResourceStatus = "deleted"
)

type VPCResource struct {
	ID           string         `json:"id"`
	VPCName      string         `json:"vpc_name"`
	Region       string         `json:"region"`
	AZ           string         `json:"az"`
	VRFName      string         `json:"vrf_name"`
	VLANId       int            `json:"vlan_id"`
	FirewallZone string         `json:"firewall_zone"`
	Status       ResourceStatus `json:"status"`
	ErrorMessage string         `json:"error_message,omitempty"`
	TotalTasks   int            `json:"total_tasks"`
	CompletedTasks int          `json:"completed_tasks"`
	FailedTasks  int            `json:"failed_tasks"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

type SubnetResource struct {
	ID             string         `json:"id"`
	SubnetName     string         `json:"subnet_name"`
	VPCName        string         `json:"vpc_name"`
	Region         string         `json:"region"`
	AZ             string         `json:"az"`
	CIDR           string         `json:"cidr"`
	Status         ResourceStatus `json:"status"`
	ErrorMessage   string         `json:"error_message,omitempty"`
	TotalTasks     int            `json:"total_tasks"`
	CompletedTasks int            `json:"completed_tasks"`
	FailedTasks    int            `json:"failed_tasks"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

type TaskStatus string

const (
	TaskStatusPending   TaskStatus = "pending"
	TaskStatusQueued    TaskStatus = "queued"
	TaskStatusRunning   TaskStatus = "running"
	TaskStatusCompleted TaskStatus = "completed"
	TaskStatusFailed    TaskStatus = "failed"
	TaskStatusCancelled TaskStatus = "cancelled"
)

type ResourceType string

const (
	ResourceTypeVPC    ResourceType = "vpc"
	ResourceTypeSubnet ResourceType = "subnet"
)

type Task struct {
	ID           string       `json:"id"`
	ResourceType ResourceType `json:"resource_type"`
	ResourceID   string       `json:"resource_id"`
	TaskType     string       `json:"task_type"`
	TaskName     string       `json:"task_name"`
	TaskOrder    int          `json:"task_order"`
	TaskParams   string       `json:"task_params"`
	Status       TaskStatus   `json:"status"`
	Priority     int          `json:"priority"`
	DeviceType   string       `json:"device_type"`
	AsynqTaskID  string       `json:"asynq_task_id,omitempty"`
	Result       string       `json:"result,omitempty"`
	ErrorMessage string       `json:"error_message,omitempty"`
	RetryCount   int          `json:"retry_count"`
	MaxRetries   int          `json:"max_retries"`
	AZ           string       `json:"az"`
	CreatedAt    time.Time    `json:"created_at"`
	QueuedAt     *time.Time   `json:"queued_at,omitempty"`
	StartedAt    *time.Time   `json:"started_at,omitempty"`
	CompletedAt  *time.Time   `json:"completed_at,omitempty"`
	UpdatedAt    time.Time    `json:"updated_at"`
}

type ResourceProgress struct {
	Total     int `json:"total"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
	Pending   int `json:"pending"`
}

type VPCStatusResponse struct {
	VPCID        string           `json:"vpc_id"`
	VPCName      string           `json:"vpc_name"`
	AZ           string           `json:"az"`
	Status       ResourceStatus   `json:"status"`
	Progress     ResourceProgress `json:"progress"`
	Tasks        []*Task          `json:"tasks"`
	ErrorMessage string           `json:"error_message,omitempty"`
	CreatedAt    time.Time        `json:"created_at"`
	UpdatedAt    time.Time        `json:"updated_at"`
}

type SubnetStatusResponse struct {
	SubnetID     string           `json:"subnet_id"`
	SubnetName   string           `json:"subnet_name"`
	AZ           string           `json:"az"`
	Status       ResourceStatus   `json:"status"`
	Progress     ResourceProgress `json:"progress"`
	Tasks        []*Task          `json:"tasks"`
	ErrorMessage string           `json:"error_message,omitempty"`
	CreatedAt    time.Time        `json:"created_at"`
	UpdatedAt    time.Time        `json:"updated_at"`
}