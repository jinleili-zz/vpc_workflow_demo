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
	ResourceTypePCCN   ResourceType = "pccn"
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

// =====================================================
// PCCN Models (Private Cloud Connection Network)
// =====================================================

// PCCNResource PCCN资源表 (AZ层) - 每个AZ一条记录
type PCCNResource struct {
	ID             string         `json:"id"`
	PCCNName       string         `json:"pccn_name"`
	VPCName        string         `json:"vpc_name"`
	VPCRegion      string         `json:"vpc_region"`
	PeerVPCName    string         `json:"peer_vpc_name"`
	PeerVPCRegion  string         `json:"peer_vpc_region"`
	AZ             string         `json:"az"`
	Status         ResourceStatus `json:"status"`
	Subnets        []string       `json:"subnets,omitempty"`
	ErrorMessage   string         `json:"error_message,omitempty"`
	TotalTasks     int            `json:"total_tasks"`
	CompletedTasks int            `json:"completed_tasks"`
	FailedTasks    int            `json:"failed_tasks"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

// PCCNStatusResponse PCCN状态查询响应
type PCCNStatusResponse struct {
	PCCNID        string           `json:"pccn_id"`
	PCCNName      string           `json:"pccn_name"`
	VPCName       string           `json:"vpc_name,omitempty"`
	VPCRegion     string           `json:"vpc_region,omitempty"`
	PeerVPCName   string           `json:"peer_vpc_name,omitempty"`
	PeerVPCRegion string           `json:"peer_vpc_region,omitempty"`
	AZ            string           `json:"az,omitempty"`
	Status        ResourceStatus   `json:"status"`
	Subnets       []string         `json:"subnets,omitempty"`
	Progress      ResourceProgress `json:"progress"`
	Tasks         []*Task          `json:"tasks,omitempty"`
	ErrorMessage  string           `json:"error_message,omitempty"`
	CreatedAt     time.Time        `json:"created_at"`
	UpdatedAt     time.Time        `json:"updated_at"`
}

// PCCNRegistry PCCN注册表 (Top层) - 一个PCCN一条记录，per-VPC详情存于VPCDetails JSONB
type PCCNRegistry struct {
	ID          string                  `json:"id"`
	PCCNName    string                  `json:"pccn_name"`
	VPC1Name    string                  `json:"vpc1_name"`
	VPC1Region  string                  `json:"vpc1_region"`
	VPC2Name    string                  `json:"vpc2_name"`
	VPC2Region  string                  `json:"vpc2_region"`
	Status      string                  `json:"status"`
	TxID        string                  `json:"tx_id,omitempty"`
	VPCDetails  map[string]VPCDetail    `json:"vpc_details"`
	CreatedAt   time.Time               `json:"created_at"`
	UpdatedAt   time.Time               `json:"updated_at"`
}

// VPCDetail VPC在PCCN中的详情
type VPCDetail struct {
	Region   string   `json:"region"`
	AZs      []string `json:"azs,omitempty"`
	Status   string   `json:"status"`
	Subnets  []string `json:"subnets,omitempty"`
	Error    string   `json:"error,omitempty"`
}

