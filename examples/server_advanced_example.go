package examples

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"workflow_qoder/tasks"

	"github.com/RichardKnop/machinery/v1"
	machineryTasks "github.com/RichardKnop/machinery/v1/tasks"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Server API服务器（优化版 - 使用Chord模式）
type Server struct {
	machineryServer *machinery.Server
	router          *gin.Engine
}

// CreateVPCRequest 创建VPC请求
type CreateVPCRequest struct {
	VPCName      string `json:"vpc_name" binding:"required"`
	VRFName      string `json:"vrf_name" binding:"required"`
	VLANId       int    `json:"vlan_id" binding:"required"`
	FirewallZone string `json:"firewall_zone" binding:"required"`
}

// CreateVPCResponse 创建VPC响应
type CreateVPCResponse struct {
	Success    bool   `json:"success"`
	Message    string `json:"message"`
	VPCID      string `json:"vpc_id"`
	WorkflowID string `json:"workflow_id"`
}

// NewServer 创建API服务器
func NewServer(machineryServer *machinery.Server) *Server {
	router := gin.Default()

	server := &Server{
		machineryServer: machineryServer,
		router:          router,
	}

	// 注册路由
	server.setupRoutes()

	return server
}

// setupRoutes 设置路由
func (s *Server) setupRoutes() {
	api := s.router.Group("/api/v1")
	{
		// 使用不同的任务编排方式
		api.POST("/vpc/independent", s.createVPCIndependent) // 独立任务
		api.POST("/vpc/chord", s.createVPCWithChord)         // Chord模式（推荐）
		api.POST("/vpc/group", s.createVPCWithGroup)         // Group模式
		api.GET("/health", s.health)
	}
}

// createVPCIndependent 方式1: 独立任务（当前使用的方式）
func (s *Server) createVPCIndependent(c *gin.Context) {
	var req CreateVPCRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, CreateVPCResponse{
			Success: false,
			Message: fmt.Sprintf("请求参数错误: %v", err),
		})
		return
	}

	vpcID := uuid.New().String()
	workflowID := uuid.New().String()

	vpcRequest := tasks.VPCRequest{
		VPCName:      req.VPCName,
		VPCID:        vpcID,
		VRFName:      req.VRFName,
		VLANId:       req.VLANId,
		FirewallZone: req.FirewallZone,
	}

	requestJSON, _ := json.Marshal(vpcRequest)

	// 发送三个独立任务
	task1 := &machineryTasks.Signature{
		Name: "create_vrf_on_switch",
		Args: []machineryTasks.Arg{{Type: "string", Value: string(requestJSON)}},
	}
	task2 := &machineryTasks.Signature{
		Name: "create_vlan_subinterface",
		Args: []machineryTasks.Arg{{Type: "string", Value: string(requestJSON)}},
	}
	task3 := &machineryTasks.Signature{
		Name: "create_firewall_zone",
		Args: []machineryTasks.Arg{{Type: "string", Value: string(requestJSON)}},
	}

	s.machineryServer.SendTask(task1)
	s.machineryServer.SendTask(task2)
	s.machineryServer.SendTask(task3)

	log.Printf("[API-独立] 创建VPC: %s (3个独立任务)", req.VPCName)

	c.JSON(http.StatusOK, CreateVPCResponse{
		Success:    true,
		Message:    "VPC创建工作流已启动（独立任务模式）",
		VPCID:      vpcID,
		WorkflowID: workflowID,
	})
}

// createVPCWithChord 方式2: Chord模式（推荐）
// VRF 和 VLAN 并行执行，都完成后再执行防火墙配置
func (s *Server) createVPCWithChord(c *gin.Context) {
	var req CreateVPCRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, CreateVPCResponse{
			Success: false,
			Message: fmt.Sprintf("请求参数错误: %v", err),
		})
		return
	}

	vpcID := uuid.New().String()
	workflowID := uuid.New().String()

	vpcRequest := tasks.VPCRequest{
		VPCName:      req.VPCName,
		VPCID:        vpcID,
		VRFName:      req.VRFName,
		VLANId:       req.VLANId,
		FirewallZone: req.FirewallZone,
	}

	requestJSON, _ := json.Marshal(vpcRequest)

	// 第一阶段：VRF 和 VLAN 并行执行
	task1 := &machineryTasks.Signature{
		Name: "create_vrf_on_switch",
		Args: []machineryTasks.Arg{{Type: "string", Value: string(requestJSON)}},
	}
	task2 := &machineryTasks.Signature{
		Name: "create_vlan_subinterface",
		Args: []machineryTasks.Arg{{Type: "string", Value: string(requestJSON)}},
	}

	// 第二阶段：防火墙配置（在VRF和VLAN都完成后执行）
	callback := &machineryTasks.Signature{
		Name: "create_firewall_zone",
		Args: []machineryTasks.Arg{{Type: "string", Value: string(requestJSON)}},
	}

	// 创建 Chord: (VRF || VLAN) -> Firewall
	group, _ := machineryTasks.NewGroup(task1, task2)
	chord, err := machineryTasks.NewChord(group, callback)
	if err != nil {
		c.JSON(http.StatusInternalServerError, CreateVPCResponse{
			Success: false,
			Message: fmt.Sprintf("创建Chord失败: %v", err),
		})
		return
	}

	_, err = s.machineryServer.SendChord(chord, 0)
	if err != nil {
		c.JSON(http.StatusInternalServerError, CreateVPCResponse{
			Success: false,
			Message: fmt.Sprintf("发送Chord失败: %v", err),
		})
		return
	}

	log.Printf("[API-Chord] 创建VPC: %s (VRF||VLAN -> Firewall)", req.VPCName)

	c.JSON(http.StatusOK, CreateVPCResponse{
		Success:    true,
		Message:    "VPC创建工作流已启动（Chord模式: 交换机任务并行，完成后配置防火墙）",
		VPCID:      vpcID,
		WorkflowID: workflowID,
	})
}

// createVPCWithGroup 方式3: Group模式
// 所有任务并行执行
func (s *Server) createVPCWithGroup(c *gin.Context) {
	var req CreateVPCRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, CreateVPCResponse{
			Success: false,
			Message: fmt.Sprintf("请求参数错误: %v", err),
		})
		return
	}

	vpcID := uuid.New().String()
	workflowID := uuid.New().String()

	vpcRequest := tasks.VPCRequest{
		VPCName:      req.VPCName,
		VPCID:        vpcID,
		VRFName:      req.VRFName,
		VLANId:       req.VLANId,
		FirewallZone: req.FirewallZone,
	}

	requestJSON, _ := json.Marshal(vpcRequest)

	// 创建任务组（所有任务并行）
	task1 := &machineryTasks.Signature{
		Name: "create_vrf_on_switch",
		Args: []machineryTasks.Arg{{Type: "string", Value: string(requestJSON)}},
	}
	task2 := &machineryTasks.Signature{
		Name: "create_vlan_subinterface",
		Args: []machineryTasks.Arg{{Type: "string", Value: string(requestJSON)}},
	}
	task3 := &machineryTasks.Signature{
		Name: "create_firewall_zone",
		Args: []machineryTasks.Arg{{Type: "string", Value: string(requestJSON)}},
	}

	group, _ := machineryTasks.NewGroup(task1, task2, task3)
	_, err := s.machineryServer.SendGroup(group, 0)
	if err != nil {
		c.JSON(http.StatusInternalServerError, CreateVPCResponse{
			Success: false,
			Message: fmt.Sprintf("发送Group失败: %v", err),
		})
		return
	}

	log.Printf("[API-Group] 创建VPC: %s (所有任务并行)", req.VPCName)

	c.JSON(http.StatusOK, CreateVPCResponse{
		Success:    true,
		Message:    "VPC创建工作流已启动（Group模式: 所有任务并行执行）",
		VPCID:      vpcID,
		WorkflowID: workflowID,
	})
}

// health 健康检查
func (s *Server) health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"service": "vpc-workflow-api-advanced",
		"modes": []string{
			"POST /api/v1/vpc/independent - 独立任务模式",
			"POST /api/v1/vpc/chord - Chord模式（推荐）",
			"POST /api/v1/vpc/group - Group模式",
		},
	})
}

// Run 启动服务器
func (s *Server) Run(addr string) error {
	log.Printf("[API] 高级任务编排服务启动在 %s", addr)
	log.Println("支持的编排模式:")
	log.Println("  - /api/v1/vpc/independent (独立任务)")
	log.Println("  - /api/v1/vpc/chord (Chord: VRF||VLAN -> Firewall)")
	log.Println("  - /api/v1/vpc/group (Group: 全部并行)")
	return s.router.Run(addr)
}
