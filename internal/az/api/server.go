package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"workflow_qoder/internal/config"
	"workflow_qoder/internal/models"
	"workflow_qoder/tasks"

	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

// Server AZ NSP API服务器
type Server struct {
	cfg         *config.NSPConfig
	asynqClient *asynq.Client
	router      *gin.Engine
	redisClient *redis.Client
	queueName   string
}

// NewServer 创建AZ NSP服务器
func NewServer(cfg *config.NSPConfig, asynqClient *asynq.Client, redisClient *redis.Client, queueName string) *Server {
	router := gin.Default()

	server := &Server{
		cfg:         cfg,
		asynqClient: asynqClient,
		router:      router,
		redisClient: redisClient,
		queueName:   queueName,
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

	// 构造任务请求数据
	vpcRequest := tasks.VPCRequest{
		VPCName:      req.VPCName,
		VPCID:        vpcID,
		VRFName:      req.VRFName,
		VLANId:       req.VLANId,
		FirewallZone: req.FirewallZone,
		WorkflowID:   workflowID,
	}

	requestJSON, err := json.Marshal(vpcRequest)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.VPCResponse{
			Success: false,
			Message: fmt.Sprintf("生成请求数据失败: %v", err),
		})
		return
	}

	// 创建第一个任务（后续任务会由handler链式发送）
	task := asynq.NewTask("create_vrf_on_switch", requestJSON)
	info, err := s.asynqClient.Enqueue(task, asynq.Queue(s.queueName))
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.VPCResponse{
			Success: false,
			Message: fmt.Sprintf("发送工作流失败: %v", err),
		})
		return
	}

	// 存储VPC名字到WorkflowID的映射
	mappingKey := fmt.Sprintf("vpc_mapping:%s", req.VPCName)
	ctx := context.Background()
	err = s.redisClient.Set(ctx, mappingKey, workflowID, 24*time.Hour).Err()
	if err != nil {
		log.Printf("[AZ NSP %s] 警告: 存储VPC映射失败: %v", s.cfg.AZ, err)
	}

	log.Printf("[AZ NSP %s] VPC工作流已创建: VPC=%s, WorkflowID=%s, TaskID=%s",
		s.cfg.AZ, req.VPCName, workflowID, info.ID)

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

	// 构造任务请求数据
	subnetRequest := tasks.SubnetRequest{
		SubnetName: req.SubnetName,
		VPCName:    req.VPCName,
		Region:     req.Region,
		AZ:         req.AZ,
		CIDR:       req.CIDR,
		WorkflowID: workflowID,
	}

	requestJSON, err := json.Marshal(subnetRequest)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.SubnetResponse{
			Success: false,
			Message: fmt.Sprintf("生成请求数据失败: %v", err),
		})
		return
	}

	// 创建第一个任务（后续任务会由handler链式发送）
	task := asynq.NewTask("create_subnet_on_switch", requestJSON)
	info, err := s.asynqClient.Enqueue(task, asynq.Queue(s.queueName))
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.SubnetResponse{
			Success: false,
			Message: fmt.Sprintf("发送工作流失败: %v", err),
		})
		return
	}

	log.Printf("[AZ NSP %s] 子网工作流已创建: Subnet=%s, WorkflowID=%s, TaskID=%s",
		s.cfg.AZ, req.SubnetName, workflowID, info.ID)

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
	ctx := context.Background()

	// 通过VPC名字获取WorkflowID
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

	// 通过workflow ID查询任务状态（从 Redis 中读取）
	stateKey := fmt.Sprintf("workflow:%s:state", workflowID)
	state, err := s.redisClient.Get(ctx, stateKey).Result()

	if err == redis.Nil {
		state = "PENDING" // 默认状态
	}

	response := gin.H{
		"az":          s.cfg.AZ,
		"vpc_name":    vpcName,
		"workflow_id": workflowID,
		"state":       state,
	}

	// 根据状态设置响应
	if state == "COMPLETED" {
		response["status"] = "completed"
		response["message"] = "工作流执行成功"
	} else if state == "FAILED" {
		response["status"] = "failed"
		response["message"] = "工作流执行失败"
	} else {
		response["status"] = "running"
		response["message"] = "工作流执行中"
	}

	c.JSON(http.StatusOK, response)
}

// deleteVPC 删除VPC（补偿操作）
func (s *Server) deleteVPC(c *gin.Context) {
	vpcName := c.Param("vpc_name")
	ctx := context.Background()

	log.Printf("[AZ NSP %s] 接收到VPC删除请求: %s", s.cfg.AZ, vpcName)

	// 获取WorkflowID
	mappingKey := fmt.Sprintf("vpc_mapping:%s", vpcName)
	workflowID, err := s.redisClient.Get(ctx, mappingKey).Result()

	if err == redis.Nil {
		log.Printf("[AZ NSP %s] VPC不存在: %s，视为删除成功", s.cfg.AZ, vpcName)
		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"message": "VPC不存在或已删除",
		})
		return
	}

	// TODO: 实际应该发送删除任务到Worker（删除VRF、VLAN、防火墙区域）
	// 这里为了简化，只记录日志并清理映射
	log.Printf("[AZ NSP %s] 删除VPC: %s (WorkflowID: %s)", s.cfg.AZ, vpcName, workflowID)

	// 清理Redis中的映射
	if err := s.redisClient.Del(ctx, mappingKey).Err(); err != nil {
		log.Printf("[AZ NSP %s] 清理VPC映射失败: %v", s.cfg.AZ, err)
	}

	// 清理任务状态
	stateKey := fmt.Sprintf("workflow:%s:state", workflowID)
	if err := s.redisClient.Del(ctx, stateKey).Err(); err != nil {
		log.Printf("[AZ NSP %s] 清理任务状态失败: %v", s.cfg.AZ, err)
	}

	log.Printf("[AZ NSP %s] VPC已删除: %s", s.cfg.AZ, vpcName)

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
	// 如果环境变量中有NSP_ADDR，则使用，否则使用默认格式（容器名称）
	nspAddr := os.Getenv("NSP_ADDR")
	if nspAddr == "" {
		// 容器名称格式: az-nsp-{AZ}
		nspAddr = fmt.Sprintf("http://az-nsp-%s:%d", s.cfg.AZ, s.cfg.Port)
	}

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
