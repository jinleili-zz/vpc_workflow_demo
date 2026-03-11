package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"workflow_qoder/internal/az/orchestrator"
	"workflow_qoder/internal/config"
	"workflow_qoder/internal/models"
	"workflow_qoder/internal/queue"

	"github.com/gin-gonic/gin"
	"github.com/paic/nsp-common/pkg/logger"
	"github.com/paic/nsp-common/pkg/taskqueue"
	"github.com/paic/nsp-common/pkg/trace"
)

type Server struct {
	cfg               *config.NSPConfig
	orchestrator      *orchestrator.AZOrchestrator
	router            *gin.Engine
	db                *sql.DB
	callbackQueueName string
}

func NewServer(cfg *config.NSPConfig, broker taskqueue.Broker, tracedHTTP *trace.TracedClient, db *sql.DB) *Server {
	router := gin.New()
	router.Use(gin.Recovery())
	
	// Add trace middleware for distributed tracing
	instanceID := fmt.Sprintf("az-nsp-vpc-%s-%s", cfg.Region, cfg.AZ)
	router.Use(trace.TraceMiddleware(instanceID))
	router.Use(ginLoggerMiddleware())

	orch := orchestrator.NewAZOrchestrator(db, broker, tracedHTTP, cfg.Region, cfg.AZ)
	callbackQueueName := queue.GetCallbackQueueName(cfg.Region, cfg.AZ, "vpc")

	server := &Server{
		cfg:               cfg,
		orchestrator:      orch,
		router:            router,
		db:                db,
		callbackQueueName: callbackQueueName,
	}

	server.setupRoutes()

	return server
}

// ginLoggerMiddleware logs HTTP requests with trace context
func ginLoggerMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path

		c.Next()

		ctx := c.Request.Context()
		latency := time.Since(start)

		logger.InfoContext(ctx, "http request",
			"method", c.Request.Method,
			"path", path,
			"status", c.Writer.Status(),
			"latency_ms", latency.Milliseconds(),
			"client_ip", c.ClientIP(),
		)
	}
}

func (s *Server) setupRoutes() {
	api := s.router.Group("/api/v1")
	{
		api.GET("/vpcs", s.listVPCs)
		api.POST("/vpc", s.createVPC)
		api.GET("/vpc/:vpc_name/status", s.getVPCStatus)
		api.DELETE("/vpc/:vpc_name", s.deleteVPC)
		api.GET("/vpc/id/:vpc_id", s.getVPCByID)
		api.DELETE("/vpc/id/:vpc_id", s.deleteVPCByID)
		api.GET("/vpc/id/:vpc_id/subnets", s.listSubnetsByVPCID)

		api.POST("/subnet", s.createSubnet)
		api.GET("/subnet/:subnet_name/status", s.getSubnetStatus)
		api.DELETE("/subnet/:subnet_name", s.deleteSubnet)
		api.GET("/subnet/id/:subnet_id", s.getSubnetByID)
		api.DELETE("/subnet/id/:subnet_id", s.deleteSubnetByID)

		api.POST("/task/replay/:task_id", s.replayTask)
		api.GET("/task/:task_id", s.getTaskByID)

		api.GET("/health", s.health)
	}
}

func (s *Server) createVPC(c *gin.Context) {
	var req models.VPCRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.VPCResponse{
			Success: false,
			Message: fmt.Sprintf("请求参数错误: %v", err),
		})
		return
	}

	ctx := c.Request.Context()
	resp, err := s.orchestrator.CreateVPC(ctx, &req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.VPCResponse{
			Success: false,
			Message: fmt.Sprintf("创建VPC失败: %v", err),
		})
		return
	}

	c.JSON(http.StatusOK, resp)
}

func (s *Server) getVPCStatus(c *gin.Context) {
	vpcName := c.Param("vpc_name")
	ctx := c.Request.Context()

	status, err := s.orchestrator.GetVPCStatus(ctx, vpcName)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, status)
}

func (s *Server) deleteVPC(c *gin.Context) {
	vpcName := c.Param("vpc_name")
	ctx := c.Request.Context()

	err := s.orchestrator.DeleteVPC(ctx, vpcName)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "VPC已成功删除",
	})
}

func (s *Server) createSubnet(c *gin.Context) {
	var req models.SubnetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.SubnetResponse{
			Success: false,
			Message: fmt.Sprintf("请求参数错误: %v", err),
		})
		return
	}

	ctx := c.Request.Context()
	resp, err := s.orchestrator.CreateSubnet(ctx, &req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.SubnetResponse{
			Success: false,
			Message: fmt.Sprintf("创建子网失败: %v", err),
		})
		return
	}

	c.JSON(http.StatusOK, resp)
}

func (s *Server) getSubnetStatus(c *gin.Context) {
	subnetName := c.Param("subnet_name")
	ctx := c.Request.Context()

	status, err := s.orchestrator.GetSubnetStatus(ctx, subnetName)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, status)
}

func (s *Server) deleteSubnet(c *gin.Context) {
	subnetName := c.Param("subnet_name")
	ctx := c.Request.Context()

	err := s.orchestrator.DeleteSubnet(ctx, subnetName)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "子网已成功删除",
	})
}

func (s *Server) listVPCs(c *gin.Context) {
	ctx := c.Request.Context()
	vpcs, err := s.orchestrator.ListVPCs(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": fmt.Sprintf("查询VPC列表失败: %v", err),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"vpcs":    vpcs,
	})
}

func (s *Server) getVPCByID(c *gin.Context) {
	vpcID := c.Param("vpc_id")
	ctx := c.Request.Context()

	vpc, err := s.orchestrator.GetVPCByID(ctx, vpcID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"vpc":     vpc,
	})
}

func (s *Server) deleteVPCByID(c *gin.Context) {
	vpcID := c.Param("vpc_id")
	ctx := c.Request.Context()

	err := s.orchestrator.DeleteVPCByID(ctx, vpcID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "VPC已成功删除",
	})
}

func (s *Server) listSubnetsByVPCID(c *gin.Context) {
	vpcID := c.Param("vpc_id")
	ctx := c.Request.Context()

	subnets, err := s.orchestrator.ListSubnetsByVPCID(ctx, vpcID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": fmt.Sprintf("查询子网列表失败: %v", err),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"subnets": subnets,
	})
}

func (s *Server) getSubnetByID(c *gin.Context) {
	subnetID := c.Param("subnet_id")
	ctx := c.Request.Context()

	subnet, err := s.orchestrator.GetSubnetByID(ctx, subnetID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"subnet":  subnet,
	})
}

func (s *Server) deleteSubnetByID(c *gin.Context) {
	subnetID := c.Param("subnet_id")
	ctx := c.Request.Context()

	err := s.orchestrator.DeleteSubnetByID(ctx, subnetID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "子网已成功删除",
	})
}

func (s *Server) HandleTaskCallback(ctx context.Context, payload []byte) error {
	var cb struct {
		TaskID       string      `json:"task_id"`
		Status       string      `json:"status"`
		Result       interface{} `json:"result"`
		ErrorMessage string      `json:"error_message"`
	}

	if err := json.Unmarshal(payload, &cb); err != nil {
		return fmt.Errorf("解析回调载荷失败: %v", err)
	}

	logger.InfoContext(ctx, "收到任务回调", "az", s.cfg.AZ, "taskID", cb.TaskID, "status", cb.Status)

	status := models.TaskStatus(cb.Status)
	err := s.orchestrator.HandleTaskCallback(ctx, cb.TaskID, status, cb.Result, cb.ErrorMessage)
	if err != nil {
		logger.InfoContext(ctx, "任务回调处理失败", "az", s.cfg.AZ, "error", err)
		return err
	}

	logger.InfoContext(ctx, "任务回调处理成功", "az", s.cfg.AZ, "taskID", cb.TaskID)
	return nil
}

func (s *Server) GetCallbackQueueName() string {
	return s.callbackQueueName
}

func (s *Server) replayTask(c *gin.Context) {
	taskID := c.Param("task_id")
	ctx := c.Request.Context()

	task, err := s.orchestrator.GetTaskByID(ctx, taskID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"success": false,
			"message": fmt.Sprintf("任务不存在: %v", err),
		})
		return
	}

	if task.Status != models.TaskStatusFailed {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": fmt.Sprintf("任务状态不是failed，无法重做 (当前状态: %s)", task.Status),
		})
		return
	}

	if err := s.orchestrator.ReplayTask(ctx, taskID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": fmt.Sprintf("重做任务失败: %v", err),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "任务已重新入队",
		"task_id": taskID,
	})
}

func (s *Server) getTaskByID(c *gin.Context) {
	taskID := c.Param("task_id")
	ctx := c.Request.Context()

	task, err := s.orchestrator.GetTaskByID(ctx, taskID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"success": false,
			"message": fmt.Sprintf("任务不存在: %v", err),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"task":    task,
	})
}

func (s *Server) health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"service": "az-nsp",
		"az":      s.cfg.AZ,
		"region":  s.cfg.Region,
	})
}

func (s *Server) Run(addr string) error {
	logger.Info("服务启动", "az", s.cfg.AZ, "addr", addr)
	return s.router.Run(addr)
}

func (s *Server) Engine() *gin.Engine {
	return s.router
}

func (s *Server) RegisterToTopNSP() error {
	if s.cfg.ServiceType != "az" {
		return nil
	}

	topNSPAddr := s.cfg.AZNSP.TopNSPAddr
	registerURL := fmt.Sprintf("%s/api/v1/register/az", topNSPAddr)

	nspAddr := os.Getenv("NSP_ADDR")
	if nspAddr == "" {
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

	logger.Info("成功注册到Top NSP", "az", s.cfg.AZ, "topNSPAddr", topNSPAddr)
	return nil
}

func (s *Server) StartHeartbeat(ctx context.Context) {
	if s.cfg.ServiceType != "az" {
		return
	}

	ticker := time.NewTicker(60 * time.Second)
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
			logger.InfoContext(ctx, "心跳停止", "az", s.cfg.AZ)
			return
		case <-ticker.C:
			body, _ := json.Marshal(reqData)
			resp, err := http.Post(heartbeatURL, "application/json", bytes.NewBuffer(body))
			if err != nil {
				logger.InfoContext(ctx, "心跳发送失败", "az", s.cfg.AZ, "error", err)
				continue
			}
			resp.Body.Close()

			if resp.StatusCode == http.StatusOK {
				logger.InfoContext(ctx, "心跳成功", "az", s.cfg.AZ)
			} else {
				logger.InfoContext(ctx, "心跳失败", "az", s.cfg.AZ, "statusCode", resp.StatusCode)
			}
		}
	}
}