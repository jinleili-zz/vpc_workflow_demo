package models

// ServiceLevel 服务级别
type ServiceLevel string

const (
	RegionLevel ServiceLevel = "REGION" // Region级服务（如VPC）
	AZLevel     ServiceLevel = "AZ"     // AZ级服务（如子网）
)

// Region 信息
type Region struct {
	ID   string   `json:"id"`   // cn-beijing
	Name string   `json:"name"` // 北京
	AZs  []string `json:"azs"`  // [cn-beijing-1a, cn-beijing-1b]
}

// AZ 信息
type AZ struct {
	ID            string `json:"id"`             // cn-beijing-1a
	Region        string `json:"region"`         // cn-beijing
	Name          string `json:"name"`           // 可用区A
	NSPAddr       string `json:"nsp_addr"`       // http://az-nsp-cn-beijing-1a:8080
	Status        string `json:"status"`         // online/offline
	LastHeartbeat int64  `json:"last_heartbeat"` // 最后心跳时间（Unix时间戳）
}

// VPCRequest VPC创建请求（扩展）
type VPCRequest struct {
	VPCID        string `json:"vpc_id,omitempty"`                // Top层统一生成的VPC ID，AZ层使用此ID
	VPCName      string `json:"vpc_name" binding:"required"`
	Region       string `json:"region" binding:"required"` // 新增：指定Region
	VRFName      string `json:"vrf_name" binding:"required"`
	VLANId       int    `json:"vlan_id" binding:"required"`
	FirewallZone string `json:"firewall_zone" binding:"required"`
}

// VPCResponse VPC创建响应
type VPCResponse struct {
	Success    bool              `json:"success"`
	Message    string            `json:"message"`
	VPCID      string            `json:"vpc_id,omitempty"`
	WorkflowID string            `json:"workflow_id,omitempty"`
	AZResults  map[string]string `json:"az_results,omitempty"` // AZ级别的结果
}

// SubnetRequest 子网创建请求
type SubnetRequest struct {
	SubnetName string `json:"subnet_name" binding:"required"`
	VPCName    string `json:"vpc_name" binding:"required"`
	Region     string `json:"region" binding:"required"`
	AZ         string `json:"az" binding:"required"` // 指定具体AZ
	CIDR       string `json:"cidr" binding:"required"`
}

// SubnetResponse 子网创建响应
type SubnetResponse struct {
	Success    bool   `json:"success"`
	Message    string `json:"message"`
	SubnetID   string `json:"subnet_id,omitempty"`
	WorkflowID string `json:"workflow_id,omitempty"`
}

// RegisterAZRequest AZ注册请求
type RegisterAZRequest struct {
	Region  string `json:"region" binding:"required"`
	AZ      string `json:"az" binding:"required"`
	NSPAddr string `json:"nsp_addr" binding:"required"`
}

// HeartbeatRequest 心跳请求
type HeartbeatRequest struct {
	Region string `json:"region" binding:"required"`
	AZ     string `json:"az" binding:"required"`
}

// =====================================================
// PCCN Types (Private Cloud Connection Network)
// =====================================================

// VPCRef VPC引用（支持跨Region）
type VPCRef struct {
	VPCName string `json:"vpc_name" binding:"required"` // VPC名称
	Region  string `json:"region" binding:"required"`   // VPC所属Region
}

// PCCNRequest PCCN创建请求 (Top层)
type PCCNRequest struct {
	PCCNID   string `json:"pccn_id,omitempty"`             // Top层生成的PCCN ID，AZ层使用
	PCCNName string `json:"pccn_name" binding:"required"`  // PCCN名称
	VPC1     VPCRef `json:"vpc1" binding:"required"`       // VPC1引用（含Region）
	VPC2     VPCRef `json:"vpc2" binding:"required"`       // VPC2引用（含Region）
}

// PCCNResponse PCCN创建响应
type PCCNResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	PCCNID  string `json:"pccn_id,omitempty"`  // PCCN唯一标识
	TxID    string `json:"tx_id,omitempty"`    // Saga事务ID（Top层）或WorkflowID（AZ层）
}

// PCCNStatusQueryResponse PCCN状态查询响应 (Top层)
type PCCNStatusQueryResponse struct {
	PCCNName      string               `json:"pccn_name"`
	OverallStatus string               `json:"overall_status"`
	VPCDetails    map[string]VPCDetail `json:"vpc_details"`
	Source        string               `json:"source"` // "database" or "fallback"
}
