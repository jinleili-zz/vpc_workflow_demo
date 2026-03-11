package models

import "time"

type FirewallPolicyRequest struct {
	PolicyName  string `json:"policy_name" binding:"required"`
	SourceIP    string `json:"source_ip" binding:"required"`
	DestIP      string `json:"dest_ip" binding:"required"`
	SourcePort  string `json:"source_port" binding:"required"`
	DestPort    string `json:"dest_port" binding:"required"`
	Protocol    string `json:"protocol" binding:"required"`
	Action      string `json:"action" binding:"required"`
	Description string `json:"description"`
}

type FirewallPolicyResponse struct {
	Success    bool              `json:"success"`
	Message    string            `json:"message"`
	PolicyID   string            `json:"policy_id,omitempty"`
	SourceZone string            `json:"source_zone,omitempty"`
	DestZone   string            `json:"dest_zone,omitempty"`
	AZResults  map[string]string `json:"az_results,omitempty"`
}

type AZFirewallPolicyRequest struct {
	PolicyName  string `json:"policy_name" binding:"required"`
	SourceZone  string `json:"source_zone" binding:"required"`
	DestZone    string `json:"dest_zone" binding:"required"`
	SourceIP    string `json:"source_ip" binding:"required"`
	DestIP      string `json:"dest_ip" binding:"required"`
	SourcePort  string `json:"source_port" binding:"required"`
	DestPort    string `json:"dest_port" binding:"required"`
	Protocol    string `json:"protocol" binding:"required"`
	Action      string `json:"action" binding:"required"`
	Description string `json:"description"`
	Region      string `json:"region" binding:"required"`
	AZ          string `json:"az" binding:"required"`
}

type AZFirewallPolicyResponse struct {
	Success    bool   `json:"success"`
	Message    string `json:"message"`
	PolicyID   string `json:"policy_id,omitempty"`
	WorkflowID string `json:"workflow_id,omitempty"`
}

type PolicyRegistry struct {
	ID           string    `json:"id"`
	PolicyName   string    `json:"policy_name"`
	SourceIP     string    `json:"source_ip"`
	DestIP       string    `json:"dest_ip"`
	SourcePort   string    `json:"source_port"`
	DestPort     string    `json:"dest_port"`
	Protocol     string    `json:"protocol"`
	Action       string    `json:"action"`
	Description  string    `json:"description"`
	SourceVPC    string    `json:"source_vpc"`
	DestVPC      string    `json:"dest_vpc"`
	SourceZone   string    `json:"source_zone"`
	DestZone     string    `json:"dest_zone"`
	SourceRegion string    `json:"source_region"`
	DestRegion   string    `json:"dest_region"`
	SourceAZ     string    `json:"source_az"`
	DestAZ       string    `json:"dest_az"`
	Status       string    `json:"status"`
	ErrorMessage string    `json:"error_message,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type PolicyAZRecord struct {
	ID           string    `json:"id"`
	PolicyID     string    `json:"policy_id"`
	AZ           string    `json:"az"`
	AZPolicyID   string    `json:"az_policy_id"`
	Status       string    `json:"status"`
	ErrorMessage string    `json:"error_message,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type FirewallPolicy struct {
	ID             string         `json:"id"`
	PolicyName     string         `json:"policy_name"`
	SourceZone     string         `json:"source_zone"`
	DestZone       string         `json:"dest_zone"`
	SourceIP       string         `json:"source_ip"`
	DestIP         string         `json:"dest_ip"`
	SourcePort     string         `json:"source_port"`
	DestPort       string         `json:"dest_port"`
	Protocol       string         `json:"protocol"`
	Action         string         `json:"action"`
	Description    string         `json:"description"`
	Status         ResourceStatus `json:"status"`
	ErrorMessage   string         `json:"error_message,omitempty"`
	TotalTasks     int            `json:"total_tasks"`
	CompletedTasks int            `json:"completed_tasks"`
	FailedTasks    int            `json:"failed_tasks"`
	Region         string         `json:"region"`
	AZ             string         `json:"az"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

type FirewallPolicyStatusResponse struct {
	PolicyID     string           `json:"policy_id"`
	PolicyName   string           `json:"policy_name"`
	SourceZone   string           `json:"source_zone"`
	DestZone     string           `json:"dest_zone"`
	Status       ResourceStatus   `json:"status"`
	Progress     ResourceProgress `json:"progress"`
	Tasks        []*Task          `json:"tasks"`
	ErrorMessage string           `json:"error_message,omitempty"`
	CreatedAt    time.Time        `json:"created_at"`
	UpdatedAt    time.Time        `json:"updated_at"`
}

type VPCRegistry struct {
	ID           string    `json:"id"`
	VPCName      string    `json:"vpc_name"`
	Region       string    `json:"region"`
	AZ           string    `json:"az"`
	AZVpcID      string    `json:"az_vpc_id"`
	VRFName      string    `json:"vrf_name"`
	VLANId       int       `json:"vlan_id"`
	FirewallZone string    `json:"firewall_zone"`
	Status       string    `json:"status"`
	SagaTxID     string    `json:"saga_tx_id,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type SubnetRegistry struct {
	ID           string    `json:"id"`
	SubnetName   string    `json:"subnet_name"`
	VPCName      string    `json:"vpc_name"`
	Region       string    `json:"region"`
	AZ           string    `json:"az"`
	AZSubnetID   string    `json:"az_subnet_id"`
	CIDR         string    `json:"cidr"`
	FirewallZone string    `json:"firewall_zone"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type CIDRZoneMapping struct {
	ID           string    `json:"id"`
	CIDR         string    `json:"cidr"`
	CIDRStart    uint64    `json:"cidr_start"`
	CIDREnd      uint64    `json:"cidr_end"`
	VPCName      string    `json:"vpc_name"`
	SubnetName   string    `json:"subnet_name"`
	Region       string    `json:"region"`
	AZ           string    `json:"az"`
	FirewallZone string    `json:"firewall_zone"`
	CreatedAt    time.Time `json:"created_at"`
}

type ZoneInfo struct {
	VPCName      string `json:"vpc_name"`
	SubnetName   string `json:"subnet_name"`
	Region       string `json:"region"`
	AZ           string `json:"az"`
	FirewallZone string `json:"firewall_zone"`
	CIDR         string `json:"cidr"`
}

const (
	ResourceTypeFirewallPolicy ResourceType = "firewall_policy"
)
