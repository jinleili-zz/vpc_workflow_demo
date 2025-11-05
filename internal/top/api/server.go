package api

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"workflow_qoder/internal/models"
	"workflow_qoder/internal/top/orchestrator"
	"workflow_qoder/internal/top/registry"

	"github.com/gin-gonic/gin"
)

// Server Top NSP API服务器
type Server struct {
	registry     *registry.Registry
	orchestrator *orchestrator.Orchestrator
	router       *gin.Engine
}

// NewServer 创建Top NSP服务器
func NewServer(reg *registry.Registry, orch *orchestrator.Orchestrator) *Server {
	router := gin.Default()

	server := &Server{
		registry:     reg,
		orchestrator: orch,
		router:       router,
	}

	// 注册路由
	server.setupRoutes()

	return server
}

// setupRoutes 设置路由
func (s *Server) setupRoutes() {
	api := s.router.Group("/api/v1")
	{
		// Region/AZ 管理
		api.POST("/register/az", s.registerAZ)
		api.POST("/heartbeat", s.heartbeat)
		api.GET("/regions", s.listRegions)
		api.GET("/regions/:region/azs", s.listRegionAZs)

		// Region级服务
		api.POST("/vpc", s.createVPC)
		api.GET("/vpc/:vpc_name/status", s.getVPCStatus)

		// AZ级服务
		api.POST("/subnet", s.createSubnet)

		// 健康检查
		api.GET("/health", s.health)
	}
}

// registerAZ AZ注册
func (s *Server) registerAZ(c *gin.Context) {
	var req models.RegisterAZRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": fmt.Sprintf("请求参数错误: %v", err),
		})
		return
	}

	ctx := context.Background()

	// 构造AZ信息
	az := &models.AZ{
		ID:      req.AZ,
		Region:  req.Region,
		NSPAddr: req.NSPAddr,
		Name:    req.AZ, // 简化处理，使用ID作为Name
	}

	// 注册AZ
	err := s.registry.RegisterAZ(ctx, az)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": fmt.Sprintf("注册AZ失败: %v", err),
		})
		return
	}

	log.Printf("[Top NSP] AZ注册成功: Region=%s, AZ=%s, Addr=%s", req.Region, req.AZ, req.NSPAddr)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "AZ注册成功",
	})
}

// heartbeat 心跳更新
func (s *Server) heartbeat(c *gin.Context) {
	var req models.HeartbeatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": fmt.Sprintf("请求参数错误: %v", err),
		})
		return
	}

	ctx := context.Background()
	err := s.registry.Heartbeat(ctx, req.Region, req.AZ)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": fmt.Sprintf("心跳更新失败: %v", err),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
	})
}

// listRegions 列出所有Region
func (s *Server) listRegions(c *gin.Context) {
	ctx := context.Background()
	regions, err := s.registry.ListAllRegions(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": fmt.Sprintf("获取Region列表失败: %v", err),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"regions": regions,
	})
}

// listRegionAZs 列出Region的所有AZ
func (s *Server) listRegionAZs(c *gin.Context) {
	region := c.Param("region")
	ctx := context.Background()

	azs, err := s.registry.GetRegionAZs(ctx, region)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": fmt.Sprintf("获取AZ列表失败: %v", err),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"azs":     azs,
	})
}

// createVPC 创建VPC（Region级）
func (s *Server) createVPC(c *gin.Context) {
	var req models.VPCRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": fmt.Sprintf("请求参数错误: %v", err),
		})
		return
	}

	ctx := context.Background()
	resp, err := s.orchestrator.CreateRegionVPC(ctx, &req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": fmt.Sprintf("创建VPC失败: %v", err),
		})
		return
	}

	c.JSON(http.StatusOK, resp)
}

// getVPCStatus 查询VPC状态
func (s *Server) getVPCStatus(c *gin.Context) {
	vpcName := c.Param("vpc_name")

	// TODO: 实现VPC状态聚合查询
	c.JSON(http.StatusOK, gin.H{
		"vpc_name": vpcName,
		"status":   "pending",
		"message":  "状态查询功能开发中",
	})
}

// createSubnet 创建子网（AZ级）
func (s *Server) createSubnet(c *gin.Context) {
	var req models.SubnetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": fmt.Sprintf("请求参数错误: %v", err),
		})
		return
	}

	ctx := context.Background()
	resp, err := s.orchestrator.CreateAZSubnet(ctx, &req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": fmt.Sprintf("创建子网失败: %v", err),
		})
		return
	}

	c.JSON(http.StatusOK, resp)
}

// health 健康检查
func (s *Server) health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"service": "top-nsp",
	})
}

// Run 启动服务器
func (s *Server) Run(addr string) error {
	log.Printf("[Top NSP] 服务启动在 %s", addr)
	return s.router.Run(addr)
}
