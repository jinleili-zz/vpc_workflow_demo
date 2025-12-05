package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"workflow_qoder/internal/az/orchestrator"
	"workflow_qoder/internal/config"
	"workflow_qoder/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/hibiken/asynq"
)

type Server struct {
	cfg               *config.NSPConfig
	orchestrator      *orchestrator.AZOrchestrator
	router            *gin.Engine
	db                *sql.DB
	callbackQueueName string
}

func NewServer(cfg *config.NSPConfig, asynqClient *asynq.Client, mysqlDB *sql.DB, queueName string, callbackQueueName string) *Server {
	router := gin.Default()

	orch := orchestrator.NewAZOrchestrator(mysqlDB, asynqClient, queueName, cfg.AZ)

	server := &Server{
		cfg:               cfg,
		orchestrator:      orch,
		router:            router,
		db:                mysqlDB,
		callbackQueueName: callbackQueueName,
	}

	server.setupRoutes()

	return server
}

func (s *Server) setupRoutes() {
	api := s.router.Group("/api/v1")
	{
		api.POST("/vpc", s.createVPC)
		api.GET("/vpc/:vpc_name/status", s.getVPCStatus)
		api.DELETE("/vpc/:vpc_name", s.deleteVPC)

		api.POST("/subnet", s.createSubnet)
		api.GET("/subnet/:subnet_name/status", s.getSubnetStatus)
		api.DELETE("/subnet/:subnet_name", s.deleteSubnet)

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

	ctx := context.Background()
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
	ctx := context.Background()

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
	ctx := context.Background()

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

	ctx := context.Background()
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
	ctx := context.Background()

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
	ctx := context.Background()

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

func (s *Server) HandleTaskCallback() func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var payload struct {
			TaskID       string      `json:"task_id"`
			Status       string      `json:"status"`
			Result       interface{} `json:"result"`
			ErrorMessage string      `json:"error_message"`
		}

		if err := json.Unmarshal(t.Payload(), &payload); err != nil {
			return fmt.Errorf("解析回调载荷失败: %v", err)
		}

		log.Printf("[AZ NSP %s] 收到任务回调: taskID=%s, status=%s", s.cfg.AZ, payload.TaskID, payload.Status)

		status := models.TaskStatus(payload.Status)
		err := s.orchestrator.HandleTaskCallback(ctx, payload.TaskID, status, payload.Result, payload.ErrorMessage)
		if err != nil {
			log.Printf("[AZ NSP %s] 任务回调处理失败: %v", s.cfg.AZ, err)
			return err
		}

		log.Printf("[AZ NSP %s] 任务回调处理成功: taskID=%s", s.cfg.AZ, payload.TaskID)
		return nil
	}
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
	log.Printf("[AZ NSP %s] 服务启动在 %s", s.cfg.AZ, addr)
	return s.router.Run(addr)
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

	log.Printf("[AZ NSP %s] 成功注册到Top NSP: %s", s.cfg.AZ, topNSPAddr)
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