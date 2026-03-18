package api

import (
	"context"
	"fmt"
	"net/http"

	"workflow_qoder/internal/models"
	"workflow_qoder/internal/top/vfw/service"

	"github.com/gin-gonic/gin"
	"github.com/jinleili-zz/nsp-platform/logger"
)

type Server struct {
	policyService *service.PolicyService
	router        *gin.Engine
}

func NewServer(policyService *service.PolicyService) *Server {
	router := gin.Default()

	server := &Server{
		policyService: policyService,
		router:        router,
	}

	server.setupRoutes()

	return server
}

func (s *Server) setupRoutes() {
	api := s.router.Group("/api/v1")
	{
		api.POST("/register/az", s.registerAZ)
		api.POST("/heartbeat", s.heartbeat)

		api.POST("/firewall/policy", s.createPolicy)
		api.GET("/firewall/policy/:policy_id/status", s.getPolicyStatus)
		api.DELETE("/firewall/policy/:policy_id", s.deletePolicy)
		api.GET("/firewall/policies", s.listPolicies)
		api.GET("/firewall/zone/:zone/policy-count", s.countPoliciesByZone)

		api.GET("/health", s.health)
	}
}

func (s *Server) registerAZ(c *gin.Context) {
	var req struct {
		Region  string `json:"region" binding:"required"`
		AZ      string `json:"az" binding:"required"`
		NSPAddr string `json:"nsp_addr" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": fmt.Sprintf("请求参数错误: %v", err),
		})
		return
	}

	s.policyService.RegisterAZ(req.Region, req.AZ, req.NSPAddr)

	logger.Info("AZ注册成功", "region", req.Region, "az", req.AZ, "addr", req.NSPAddr)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "AZ注册成功",
	})
}

func (s *Server) heartbeat(c *gin.Context) {
	var req models.HeartbeatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": fmt.Sprintf("请求参数错误: %v", err),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
	})
}

func (s *Server) createPolicy(c *gin.Context) {
	var req models.FirewallPolicyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.FirewallPolicyResponse{
			Success: false,
			Message: fmt.Sprintf("请求参数错误: %v", err),
		})
		return
	}

	ctx := context.Background()
	resp, err := s.policyService.CreatePolicy(ctx, &req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.FirewallPolicyResponse{
			Success: false,
			Message: fmt.Sprintf("创建策略失败: %v", err),
		})
		return
	}

	c.JSON(http.StatusOK, resp)
}

func (s *Server) getPolicyStatus(c *gin.Context) {
	policyID := c.Param("policy_id")
	ctx := context.Background()

	policy, records, err := s.policyService.GetPolicyStatus(ctx, policyID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success":    true,
		"policy":     policy,
		"az_records": records,
	})
}

func (s *Server) deletePolicy(c *gin.Context) {
	policyID := c.Param("policy_id")
	ctx := context.Background()

	err := s.policyService.DeletePolicy(ctx, policyID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "策略已删除",
	})
}

func (s *Server) listPolicies(c *gin.Context) {
	ctx := context.Background()

	policies, err := s.policyService.ListPolicies(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success":  true,
		"policies": policies,
	})
}

func (s *Server) countPoliciesByZone(c *gin.Context) {
	zone := c.Param("zone")
	ctx := context.Background()

	count, err := s.policyService.CountPoliciesByZone(ctx, zone)
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

func (s *Server) health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"service": "top-nsp-vfw",
	})
}

func (s *Server) Run(addr string) error {
	logger.Info("Top NSP VFW 服务启动", "addr", addr)
	return s.router.Run(addr)
}
