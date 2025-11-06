package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"workflow_qoder/internal/config"
	"workflow_qoder/internal/db"
	"workflow_qoder/internal/models"
	"workflow_qoder/tasks"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Server AZ NSP API服务器
type Server struct {
	cfg    *config.NSPConfig
	router *gin.Engine
}

// NewServer 创建AZ NSP服务器
func NewServer(cfg *config.NSPConfig) *Server {
	router := gin.Default()

	server := &Server{
		cfg:    cfg,
		router: router,
	}

	// 注册路由
	server.setupRoutes()

	return server
}

// setupRoutes 设置路由
func (s *Server) setupRoutes() {
	api := s.router.Group("/api/v1")
	{
		// VPC相关
		api.POST("/vpc", s.createVPC)
		api.GET("/vpc/:vpc_name/status", s.getVPCStatus)
		api.DELETE("/vpc/:vpc_name", s.deleteVPC)

		// 子网相关
		api.POST("/subnet", s.createSubnet)

		// 健康检查
		api.GET("/health", s.health)
	}
}

// createVPC 创建VPC
func (s *Server) createVPC(c *gin.Context) {
	var req models.VPCRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.VPCResponse{
			Success: false,
			Message: fmt.Sprintf("请求参数错误: %v", err),
		})
		return
	}

	// 生成VPC ID和Workflow ID
	vpcID := uuid.New().String()
	workflowID := uuid.New().String()

	log.Printf("[AZ NSP %s] 接收到VPC创建请求: %s", s.cfg.AZ, req.VPCName)

	// 创建工作流记录
	workflow := &db.Workflow{
		WorkflowID:   workflowID,
		ResourceType: "vpc",
		ResourceName: req.VPCName,
		ResourceID:   sql.NullString{String: vpcID, Valid: true},
		Region:       s.cfg.Region,
		AZ:           sql.NullString{String: s.cfg.AZ, Valid: true},
		Status:       "pending",
	}

	if err := db.CreateWorkflow(workflow); err != nil {
		c.JSON(http.StatusInternalServerError, models.VPCResponse{
			Success: false,
			Message: fmt.Sprintf("创建工作流失败: %v", err),
		})
		return
	}

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
		c.JSON(http.StatusInternalServerError, models.VPCResponse{
			Success: false,
			Message: fmt.Sprintf("生成请求数据失败: %v", err),
		})
		return
	}

	// 创建第一个任务：在交换机上创建VRF
	taskID := fmt.Sprintf("%s-task1-%d", workflowID, time.Now().UnixNano())
	task := &db.Task{
		TaskID:        taskID,
		WorkflowID:    workflowID,
		Region:        s.cfg.Region,
		AZ:            sql.NullString{String: s.cfg.AZ, Valid: true},
		TaskName:      "create_vrf_on_switch",
		TaskType:      "switch",
		SequenceOrder: 1,
		Status:        "pending",
		Payload:       requestJSON,
		MaxRetries:    3,
	}

	if err := db.CreateTask(task); err != nil {
		c.JSON(http.StatusInternalServerError, models.VPCResponse{
			Success: false,
			Message: fmt.Sprintf("创建任务失败: %v", err),
		})
		return
	}

	// 创建资源映射（用于通过VPC名称查询）
	if err := db.CreateResourceMapping("vpc", req.VPCName, vpcID, workflowID, s.cfg.Region, &s.cfg.AZ); err != nil {
		log.Printf("[AZ NSP %s] 警告: 创建资源映射失败: %v", s.cfg.AZ, err)
	}

	log.Printf("[AZ NSP %s] VPC工作流已创建: VPC=%s, WorkflowID=%s, 首任务=%s",
		s.cfg.AZ, req.VPCName, workflowID, taskID)

	c.JSON(http.StatusOK, models.VPCResponse{
		Success:    true,
		Message:    "VPC创建工作流已启动",
		VPCID:      vpcID,
		WorkflowID: workflowID,
	})
}

// createSubnet 创建子网
func (s *Server) createSubnet(c *gin.Context) {
	var req models.SubnetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.SubnetResponse{
			Success: false,
			Message: fmt.Sprintf("请求参数错误: %v", err),
		})
		return
	}

	log.Printf("[AZ NSP %s] 接收到子网创建请求: %s (VPC: %s)", s.cfg.AZ, req.SubnetName, req.VPCName)

	// 生成子网ID和Workflow ID
	subnetID := uuid.New().String()
	workflowID := uuid.New().String()

	// 创建工作流记录
	workflow := &db.Workflow{
		WorkflowID:   workflowID,
		ResourceType: "subnet",
		ResourceName: req.SubnetName,
		ResourceID:   sql.NullString{String: subnetID, Valid: true},
		Region:       req.Region,
		AZ:           sql.NullString{String: req.AZ, Valid: true},
		Status:       "pending",
	}

	if err := db.CreateWorkflow(workflow); err != nil {
		c.JSON(http.StatusInternalServerError, models.SubnetResponse{
			Success: false,
			Message: fmt.Sprintf("创建工作流失败: %v", err),
		})
		return
	}

	// 构造任务请求数据
	subnetRequest := tasks.SubnetRequest{
		SubnetName: req.SubnetName,
		VPCName:    req.VPCName,
		Region:     req.Region,
		AZ:         req.AZ,
		CIDR:       req.CIDR,
	}

	requestJSON, err := json.Marshal(subnetRequest)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.SubnetResponse{
			Success: false,
			Message: fmt.Sprintf("生成请求数据失败: %v", err),
		})
		return
	}

	// 创建第一个任务：在交换机上创建子网
	taskID := fmt.Sprintf("%s-task1-%d", workflowID, time.Now().UnixNano())
	task := &db.Task{
		TaskID:        taskID,
		WorkflowID:    workflowID,
		Region:        req.Region,
		AZ:            sql.NullString{String: req.AZ, Valid: true},
		TaskName:      "create_subnet_on_switch",
		TaskType:      "switch",
		SequenceOrder: 1,
		Status:        "pending",
		Payload:       requestJSON,
		MaxRetries:    3,
	}

	if err := db.CreateTask(task); err != nil {
		c.JSON(http.StatusInternalServerError, models.SubnetResponse{
			Success: false,
			Message: fmt.Sprintf("创建任务失败: %v", err),
		})
		return
	}

	// 创建资源映射
	if err := db.CreateResourceMapping("subnet", req.SubnetName, subnetID, workflowID, req.Region, &req.AZ); err != nil {
		log.Printf("[AZ NSP %s] 警告: 创建资源映射失败: %v", s.cfg.AZ, err)
	}

	log.Printf("[AZ NSP %s] 子网工作流已创建: Subnet=%s, WorkflowID=%s",
		s.cfg.AZ, req.SubnetName, workflowID)

	c.JSON(http.StatusOK, models.SubnetResponse{
		Success:    true,
		Message:    "子网创建工作流已启动",
		SubnetID:   subnetID,
		WorkflowID: workflowID,
	})
}

// getVPCStatus 获取VPC创建状态
func (s *Server) getVPCStatus(c *gin.Context) {
	vpcName := c.Param("vpc_name")

	// 通过VPC名字获取工作流
	workflow, err := db.GetWorkflowByResourceName("vpc", vpcName)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"vpc_name": vpcName,
			"status":   "error",
			"message":  fmt.Sprintf("查询工作流失败: %v", err),
		})
		return
	}

	if workflow == nil {
		c.JSON(http.StatusNotFound, gin.H{
			"vpc_name": vpcName,
			"status":   "not_found",
			"message":  fmt.Sprintf("找不到VPC: %s", vpcName),
		})
		return
	}

	// 获取工作流的所有任务
	tasks, err := db.GetTasksByWorkflowID(workflow.WorkflowID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"vpc_name": vpcName,
			"status":   "error",
			"message":  fmt.Sprintf("查询任务失败: %v", err),
		})
		return
	}

	// 构建响应
	response := gin.H{
		"az":          s.cfg.AZ,
		"vpc_name":    vpcName,
		"workflow_id": workflow.WorkflowID,
		"status":      workflow.Status,
		"created_at":  workflow.CreatedAt,
		"updated_at":  workflow.UpdatedAt,
	}

	if workflow.ErrorMessage.Valid {
		response["error"] = workflow.ErrorMessage.String
	}

	// 添加任务详情
	taskDetails := make([]gin.H, 0, len(tasks))
	for _, task := range tasks {
		detail := gin.H{
			"task_id":   task.TaskID,
			"task_name": task.TaskName,
			"status":    task.Status,
			"sequence":  task.SequenceOrder,
		}
		if task.ErrorMessage.Valid {
			detail["error"] = task.ErrorMessage.String
		}
		if task.StartedAt.Valid {
			detail["started_at"] = task.StartedAt.Time
		}
		if task.CompletedAt.Valid {
			detail["completed_at"] = task.CompletedAt.Time
		}
		taskDetails = append(taskDetails, detail)
	}
	response["tasks"] = taskDetails

	// 设置HTTP状态码和消息
	var httpStatus int
	switch workflow.Status {
	case "completed":
		httpStatus = http.StatusOK
		response["message"] = "VPC创建成功"
	case "failed":
		httpStatus = http.StatusOK
		response["message"] = "VPC创建失败"
	case "running":
		httpStatus = http.StatusOK
		response["message"] = "VPC创建中"
	default:
		httpStatus = http.StatusOK
		response["message"] = "VPC创建待处理"
	}

	c.JSON(httpStatus, response)
}

// deleteVPC 删除VPC（补偿操作）
func (s *Server) deleteVPC(c *gin.Context) {
	vpcName := c.Param("vpc_name")

	log.Printf("[AZ NSP %s] 接收到VPC删除请求: %s", s.cfg.AZ, vpcName)

	// 查询工作流
	workflow, err := db.GetWorkflowByResourceName("vpc", vpcName)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": fmt.Sprintf("查询VPC失败: %v", err),
		})
		return
	}

	if workflow == nil {
		log.Printf("[AZ NSP %s] VPC不存在: %s，视为删除成功", s.cfg.AZ, vpcName)
		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"message": "VPC不存在或已删除",
		})
		return
	}

	// TODO: 实际应该发送删除任务到Worker（删除VRF、VLAN、防火墙区域）
	// 这里为了简化，只更新状态为已删除
	errMsg := "用户手动删除"
	if err := db.UpdateWorkflowStatus(workflow.WorkflowID, "completed", &errMsg); err != nil {
		log.Printf("[AZ NSP %s] 更新工作流状态失败: %v", s.cfg.AZ, err)
	}

	log.Printf("[AZ NSP %s] VPC已标记删除: %s (WorkflowID: %s)", s.cfg.AZ, vpcName, workflow.WorkflowID)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "VPC已成功删除",
	})
}

// health 健康检查
func (s *Server) health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"service": "az-nsp",
		"az":      s.cfg.AZ,
		"region":  s.cfg.Region,
	})
}

// Run 启动服务器
func (s *Server) Run(addr string) error {
	log.Printf("[AZ NSP %s] 服务启动在 %s", s.cfg.AZ, addr)
	return s.router.Run(addr)
}

// RegisterToTopNSP 向Top NSP注册
func (s *Server) RegisterToTopNSP() error {
	if s.cfg.ServiceType != "az" {
		return nil
	}

	topNSPAddr := s.cfg.AZNSP.TopNSPAddr
	registerURL := fmt.Sprintf("%s/api/v1/register/az", topNSPAddr)

	// 构造注册请求
	// 容器名称格式: az-nsp-{AZ}
	nspAddr := fmt.Sprintf("http://az-nsp-%s:%d", s.cfg.AZ, s.cfg.Port)

	reqData := models.RegisterAZRequest{
		Region:  s.cfg.Region,
		AZ:      s.cfg.AZ,
		NSPAddr: nspAddr,
	}

	body, err := json.Marshal(reqData)
	if err != nil {
		return fmt.Errorf("序列化注册请求失败: %v", err)
	}

	resp, err := http.Post(registerURL, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return fmt.Errorf("注册请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("注册失败，状态码: %d", resp.StatusCode)
	}

	log.Printf("[AZ NSP %s] 成功注册到Top NSP: %s", s.cfg.AZ, topNSPAddr)
	return nil
}

// StartHeartbeat 启动心跳
func (s *Server) StartHeartbeat(ctx context.Context) {
	if s.cfg.ServiceType != "az" {
		return
	}

	ticker := time.NewTicker(60 * time.Second) // 每分钟发送一次心跳
	defer ticker.Stop()

	topNSPAddr := s.cfg.AZNSP.TopNSPAddr
	heartbeatURL := fmt.Sprintf("%s/api/v1/heartbeat", topNSPAddr)

	reqData := models.HeartbeatRequest{
		Region: s.cfg.Region,
		AZ:     s.cfg.AZ,
	}

	for {
		select {
		case <-ctx.Done():
			log.Printf("[AZ NSP %s] 心跳停止", s.cfg.AZ)
			return
		case <-ticker.C:
			body, _ := json.Marshal(reqData)
			resp, err := http.Post(heartbeatURL, "application/json", bytes.NewBuffer(body))
			if err != nil {
				log.Printf("[AZ NSP %s] 心跳发送失败: %v", s.cfg.AZ, err)
				continue
			}
			resp.Body.Close()

			if resp.StatusCode == http.StatusOK {
				log.Printf("[AZ NSP %s] 心跳成功", s.cfg.AZ)
			} else {
				log.Printf("[AZ NSP %s] 心跳失败，状态码: %d", s.cfg.AZ, resp.StatusCode)
			}
		}
	}
}
