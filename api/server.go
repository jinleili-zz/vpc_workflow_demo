package api

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

// Server API服务器
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
	Success   bool   `json:"success"`
	Message   string `json:"message"`
	VPCID     string `json:"vpc_id"`
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
		api.POST("/vpc", s.createVPC)
		api.GET("/vpc/:workflow_id", s.getVPCStatus)
		api.GET("/health", s.health)
	}
}

// createVPC 创建VPC
func (s *Server) createVPC(c *gin.Context) {
	var req CreateVPCRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, CreateVPCResponse{
			Success: false,
			Message: fmt.Sprintf("请求参数错误: %v", err),
		})
		return
	}

	// 生成VPC ID和Workflow ID
	vpcID := uuid.New().String()
	workflowID := uuid.New().String()

	// 构造任务请求数据
	vpcRequest := tasks.VPCRequest{
		VPCName:      req.VPCName,
		VPCID:        vpcID,
		VRFName:      req.VRFName,
		VLANId:       req.VLANId,
		FirewallZone: req.FirewallZone,
	}

	requestJSON, err := json.Marshal(vpcRequest)
	if err != nil {
		c.JSON(http.StatusInternalServerError, CreateVPCResponse{
			Success: false,
			Message: fmt.Sprintf("生成请求数据失败: %v", err),
		})
		return
	}

	// 创建任务: VRF -> VLAN子接口 -> 防火墙 (顺序执行)
	task1 := machineryTasks.Signature{
		Name: "create_vrf_on_switch",
		Args: []machineryTasks.Arg{
			{
				Type:  "string",
				Value: string(requestJSON),
			},
		},
	}

	task2 := machineryTasks.Signature{
		Name: "create_vlan_subinterface",
		Args: []machineryTasks.Arg{
			{
				Type:  "string",
				Value: string(requestJSON),
			},
		},
	}

	task3 := machineryTasks.Signature{
		Name: "create_firewall_zone",
		Args: []machineryTasks.Arg{
			{
				Type:  "string",
				Value: string(requestJSON),
			},
		},
	}

	// 发送三个独立任务，由worker分别处理
	_, err = s.machineryServer.SendTask(&task1)
	if err != nil {
		c.JSON(http.StatusInternalServerError, CreateVPCResponse{
			Success: false,
			Message: fmt.Sprintf("发送VRF任务失败: %v", err),
		})
		return
	}

	_, err = s.machineryServer.SendTask(&task2)
	if err != nil {
		c.JSON(http.StatusInternalServerError, CreateVPCResponse{
			Success: false,
			Message: fmt.Sprintf("发送VLAN任务失败: %v", err),
		})
		return
	}

	_, err = s.machineryServer.SendTask(&task3)
	if err != nil {
		c.JSON(http.StatusInternalServerError, CreateVPCResponse{
			Success: false,
			Message: fmt.Sprintf("发送防火墙任务失败: %v", err),
		})
		return
	}

	log.Printf("[API] 创建VPC工作流: VPC=%s, WorkflowID=%s", 
		req.VPCName, workflowID)

	c.JSON(http.StatusOK, CreateVPCResponse{
		Success:    true,
		Message:    "VPC创建工作流已启动",
		VPCID:      vpcID,
		WorkflowID: workflowID,
	})
}

// getVPCStatus 获取VPC创建状态
func (s *Server) getVPCStatus(c *gin.Context) {
	workflowID := c.Param("workflow_id")
	
	c.JSON(http.StatusOK, gin.H{
		"workflow_id": workflowID,
		"message":     "请查看worker日志了解任务执行状态",
	})
}

// health 健康检查
func (s *Server) health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status": "ok",
		"service": "vpc-workflow-api",
	})
}

// Run 启动服务器
func (s *Server) Run(addr string) error {
	log.Printf("[API] 服务启动在 %s", addr)
	return s.router.Run(addr)
}
