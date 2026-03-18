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

	"workflow_qoder/internal/az/vfw/orchestrator"
	"workflow_qoder/internal/config"
	"workflow_qoder/internal/models"
	"workflow_qoder/internal/queue"

	"github.com/gin-gonic/gin"
	"github.com/jinleili-zz/nsp-platform/logger"
	"github.com/jinleili-zz/nsp-platform/taskqueue"
	"github.com/jinleili-zz/nsp-platform/trace"
)

type Server struct {
	cfg               *config.NSPConfig
	orchestrator      *orchestrator.VFWOrchestrator
	router            *gin.Engine
	db                *sql.DB
	callbackQueueName string
}

func NewServer(cfg *config.NSPConfig, engine *taskqueue.Engine, tracedHTTP *trace.TracedClient, db *sql.DB) *Server {
	router := gin.New()
	router.Use(gin.Recovery())

	// Add trace middleware for distributed tracing
	instanceID := fmt.Sprintf("az-nsp-vfw-%s-%s", cfg.Region, cfg.AZ)
	router.Use(trace.TraceMiddleware(instanceID))
	router.Use(ginLoggerMiddleware())

	orch := orchestrator.NewVFWOrchestrator(db, engine, tracedHTTP, cfg.Region, cfg.AZ)
	callbackQueueName := queue.GetCallbackQueueName(cfg.Region, cfg.AZ, "vfw")

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
		api.GET("/firewall/policies", s.listPolicies)
		api.POST("/firewall/policy", s.createPolicy)
		api.GET("/firewall/policy/:policy_name/status", s.getPolicyStatus)
		api.DELETE("/firewall/policy/:policy_name", s.deletePolicy)
		api.GET("/firewall/policy/id/:policy_id", s.getPolicyByID)

		api.GET("/firewall/zone/:zone/policy-count", s.countPoliciesByZone)

		api.GET("/health", s.health)
	}
}

func (s *Server) createPolicy(c *gin.Context) {
	var req models.AZFirewallPolicyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.AZFirewallPolicyResponse{
			Success: false,
			Message: fmt.Sprintf("请求参数错误: %v", err),
		})
		return
	}

	ctx := c.Request.Context()
	resp, err := s.orchestrator.CreatePolicy(ctx, &req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.AZFirewallPolicyResponse{
			Success: false,
			Message: fmt.Sprintf("创建策略失败: %v", err),
		})
		return
	}

	c.JSON(http.StatusOK, resp)
}

func (s *Server) getPolicyStatus(c *gin.Context) {
	policyName := c.Param("policy_name")
	ctx := c.Request.Context()

	status, err := s.orchestrator.GetPolicyStatus(ctx, policyName)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, status)
}

func (s *Server) deletePolicy(c *gin.Context) {
	policyName := c.Param("policy_name")
	ctx := c.Request.Context()

	err := s.orchestrator.DeletePolicy(ctx, policyName)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "策略已成功删除",
	})
}

func (s *Server) listPolicies(c *gin.Context) {
	ctx := c.Request.Context()
	policies, err := s.orchestrator.ListPolicies(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": fmt.Sprintf("查询策略列表失败: %v", err),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success":  true,
		"policies": policies,
	})
}

func (s *Server) getPolicyByID(c *gin.Context) {
	policyID := c.Param("policy_id")
	ctx := c.Request.Context()

	policy, err := s.orchestrator.GetPolicyByID(ctx, policyID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"policy":  policy,
	})
}

func (s *Server) countPoliciesByZone(c *gin.Context) {
	zone := c.Param("zone")
	ctx := c.Request.Context()

	count, err := s.orchestrator.CountPoliciesByZone(ctx, zone)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"zone":    zone,
		"count":   count,
	})
}

func (s *Server) HandleTaskCallback(ctx context.Context, payload []byte) error {
	return s.orchestrator.HandleTaskCallback(ctx, payload)
}

func (s *Server) GetCallbackQueueName() string {
	return s.callbackQueueName
}

func (s *Server) health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"service": "az-nsp-vfw",
		"az":      s.cfg.AZ,
		"region":  s.cfg.Region,
	})
}

func (s *Server) Run(addr string) error {
	logger.Info("AZ NSP VFW 服务启动", "az", s.cfg.AZ, "addr", addr)
	return s.router.Run(addr)
}

func (s *Server) RegisterToTopNSP() error {
	topNSPVFWAddr := os.Getenv("TOP_NSP_VFW_ADDR")
	if topNSPVFWAddr == "" {
		topNSPVFWAddr = "http://top-nsp-vfw:8082"
	}

	registerURL := fmt.Sprintf("%s/api/v1/register/az", topNSPVFWAddr)

	nspAddr := os.Getenv("NSP_VFW_ADDR")
	if nspAddr == "" {
		nspAddr = fmt.Sprintf("http://az-nsp-vfw-%s:%d", s.cfg.AZ, s.cfg.Port)
	}

	reqData := map[string]string{
		"region":   s.cfg.Region,
		"az":       s.cfg.AZ,
		"nsp_addr": nspAddr,
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

	logger.Info("成功注册到Top NSP VFW", "az", s.cfg.AZ, "top_addr", topNSPVFWAddr)
	return nil
}

func (s *Server) StartHeartbeat(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	topNSPVFWAddr := os.Getenv("TOP_NSP_VFW_ADDR")
	if topNSPVFWAddr == "" {
		topNSPVFWAddr = "http://top-nsp-vfw:8082"
	}

	heartbeatURL := fmt.Sprintf("%s/api/v1/heartbeat", topNSPVFWAddr)

	reqData := map[string]string{
		"region": s.cfg.Region,
		"az":     s.cfg.AZ,
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

// StartCompensationTask starts the background compensation task that repairs
// inconsistencies between workflow state and policy state.
func (s *Server) StartCompensationTask(ctx context.Context, interval time.Duration) {
	s.orchestrator.StartCompensationTask(ctx, interval)
}
