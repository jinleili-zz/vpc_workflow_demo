package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"workflow_qoder/tasks"

	"github.com/RichardKnop/machinery/v1"
	machineryTasks "github.com/RichardKnop/machinery/v1/tasks"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
)

// Server API服务器
type Server struct {
	machineryServer *machinery.Server
	router          *gin.Engine
	redisClient     *redis.Client // 用于存储VPC映射
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

	// 创建Redis客户端（用于存储VPC映射）
	redisClient := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
		DB:   0,
	})

	server := &Server{
		machineryServer: machineryServer,
		router:          router,
		redisClient:     redisClient,
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
		// 使用VPC名字查询状态
		api.GET("/vpc/:vpc_name/status", s.getVPCStatus)
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

	// 创建任务链: VRF -> VLAN子接口 -> 防火墙 (顺序执行)
	// 使用Chain确保任务按顺序执行，前一个任务完成后才执行下一个
	task1 := machineryTasks.Signature{
		UUID: workflowID, // 使用workflow ID作为第一个任务的UUID
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

	// 使用Chain构建workflow，确保任务顺序执行
	chain, err := machineryTasks.NewChain(&task1, &task2, &task3)
	if err != nil {
		c.JSON(http.StatusInternalServerError, CreateVPCResponse{
			Success: false,
			Message: fmt.Sprintf("创建任务链失败: %v", err),
		})
		return
	}

	// 发送任务链到消息队列
	_, err = s.machineryServer.SendChain(chain)
	if err != nil {
		c.JSON(http.StatusInternalServerError, CreateVPCResponse{
			Success: false,
			Message: fmt.Sprintf("发送工作流失败: %v", err),
		})
		return
	}

	// 存储VPC名字到WorkflowID的映射（使用Redis，24小时过期）
	mappingKey := fmt.Sprintf("vpc_mapping:%s", req.VPCName)
	ctx := context.Background()
	err = s.redisClient.Set(ctx, mappingKey, workflowID, 24*time.Hour).Err()
	if err != nil {
		log.Printf("[API] 警告: 存储VPC映射失败: %v", err)
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

// getVPCStatus 获取VPC创建状态（通过VPC名字查询）
func (s *Server) getVPCStatus(c *gin.Context) {
	vpcName := c.Param("vpc_name")
	ctx := context.Background()

	// 1. 先通过VPC名字获取WorkflowID
	mappingKey := fmt.Sprintf("vpc_mapping:%s", vpcName)
	workflowID, err := s.redisClient.Get(ctx, mappingKey).Result()

	if err == redis.Nil {
		c.JSON(http.StatusNotFound, gin.H{
			"vpc_name": vpcName,
			"status":   "not_found",
			"message":  fmt.Sprintf("找不到VPC: %s", vpcName),
		})
		return
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"vpc_name": vpcName,
			"status":   "error",
			"message":  fmt.Sprintf("查询VPC映射失败: %v", err),
		})
		return
	}

	// 2. 通过workflow ID查询任务状态
	backend := s.machineryServer.GetBackend()
	taskState, err := backend.GetState(workflowID)

	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"vpc_name":    vpcName,
			"workflow_id": workflowID,
			"status":      "not_found",
			"message":     fmt.Sprintf("找不到工作流: %v", err),
		})
		return
	}

	response := gin.H{
		"vpc_name":    vpcName,
		"workflow_id": workflowID,
		"task_name":   taskState.TaskName,
		"state":       taskState.State,
	}

	if taskState.IsCompleted() {
		response["status"] = "completed"
		response["message"] = "工作流执行成功"
		if len(taskState.Results) > 0 {
			response["results"] = taskState.Results
		}
	} else if taskState.IsFailure() {
		response["status"] = "failed"
		response["message"] = "工作流执行失败"
		response["error"] = taskState.Error
	} else if taskState.IsSuccess() {
		response["status"] = "success"
		response["message"] = "工作流执行成功"
		if len(taskState.Results) > 0 {
			response["results"] = taskState.Results
		}
	} else {
		response["status"] = "pending"
		response["message"] = "工作流执行中"
	}

	c.JSON(http.StatusOK, response)
}

// health 健康检查
func (s *Server) health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"service": "vpc-workflow-api",
	})
}

// Run 启动服务器
func (s *Server) Run(addr string) error {
	log.Printf("[API] 服务启动在 %s", addr)
	return s.router.Run(addr)
}
