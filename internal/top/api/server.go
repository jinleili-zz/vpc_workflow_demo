package api

import (
	"fmt"
	"net/http"

	"workflow_qoder/internal/models"
	"workflow_qoder/internal/top/orchestrator"
	"workflow_qoder/internal/top/registry"

	"github.com/gin-gonic/gin"
	"github.com/jinleili-zz/nsp-platform/logger"
	"github.com/jinleili-zz/nsp-platform/trace"
)

type Server struct {
	registry     *registry.Registry
	orchestrator *orchestrator.Orchestrator
	tracedHTTP   *trace.TracedClient
	router       *gin.Engine
}

func NewServer(registry *registry.Registry, orchestrator *orchestrator.Orchestrator, tracedHTTP *trace.TracedClient) *Server {
	router := gin.New()

	server := &Server{
		registry:     registry,
		orchestrator: orchestrator,
		tracedHTTP:   tracedHTTP,
		router:       router,
	}

	return server
}

func (s *Server) Engine() *gin.Engine {
	return s.router
}

func (s *Server) SetupRoutes() {
	api := s.router.Group("/api/v1")
	{
		api.GET("/health", s.health)
		api.POST("/vpc", s.createVPC)
		api.GET("/vpcs", s.listVPCs)
		api.GET("/vpc/:vpc_name/status", s.getVPCStatus)
		api.DELETE("/vpc/:vpc_name", s.deleteVPC)
		api.GET("/azs", s.listAZs)
		api.POST("/az", s.registerAZ)
		api.POST("/az/heartbeat", s.heartbeat)
		api.POST("/register/az", s.registerAZ) // alias
		api.POST("/heartbeat", s.heartbeat)    // alias
		api.POST("/subnet", s.createSubnet)
		api.GET("/subnet/:subnet_name/status", s.getSubnetStatus)
		api.DELETE("/subnet/:subnet_name", s.deleteSubnet)

		// PCCN routes
		api.POST("/pccn", s.createPCCN)
		api.GET("/pccn/:pccn_name/status", s.getPCCNStatus)
		api.GET("/pccns", s.listPCCNs)
		api.DELETE("/pccn/:pccn_name", s.deletePCCN)
	}
}

func (s *Server) Run(addr string) error {
	logger.Info("服务启动", "service", "top-nsp", "addr", addr)
	return s.router.Run(addr)
}

// =====================================================
// Health & AZ Management
// =====================================================

func (s *Server) health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"service": "top-nsp-vpc",
	})
}

func (s *Server) registerAZ(c *gin.Context) {
	var req models.RegisterAZRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": fmt.Sprintf("请求参数错误: %v", err),
		})
		return
	}

	ctx := c.Request.Context()
	az := &models.AZ{
		ID:      req.AZ,
		Region:  req.Region,
		NSPAddr: req.NSPAddr,
	}
	if err := s.registry.RegisterAZ(ctx, az); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": fmt.Sprintf("注册AZ失败: %v", err),
		})
		return
	}

	logger.InfoContext(ctx, "AZ注册成功", "region", req.Region, "az", req.AZ, "addr", req.NSPAddr)
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

	ctx := c.Request.Context()
	if err := s.registry.Heartbeat(ctx, req.Region, req.AZ); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": fmt.Sprintf("更新心跳失败: %v", err),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
	})
}

func (s *Server) listAZs(c *gin.Context) {
	ctx := c.Request.Context()
	azs, err := s.registry.ListAllAZs(ctx)
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

// =====================================================
// VPC Handlers
// =====================================================

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
	resp, err := s.orchestrator.CreateRegionVPC(ctx, &req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.VPCResponse{
			Success: false,
			Message: fmt.Sprintf("创建VPC失败: %v", err),
		})
		return
	}

	c.JSON(http.StatusOK, resp)
}

func (s *Server) listVPCs(c *gin.Context) {
	ctx := c.Request.Context()

	if !s.orchestrator.HasTopDAO() {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"success": false,
			"message": "数据库未配置",
		})
		return
	}

	vpcs, err := s.orchestrator.ListAllVPCs(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": fmt.Sprintf("获取VPC列表失败: %v", err),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"vpcs":    vpcs,
	})
}

func (s *Server) getVPCStatus(c *gin.Context) {
	vpcName := c.Param("vpc_name")
	ctx := c.Request.Context()

	if !s.orchestrator.HasTopDAO() {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"success": false,
			"message": "数据库未配置",
		})
		return
	}

	vpc, err := s.orchestrator.GetVPCByName(ctx, vpcName)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"success": false,
			"message": fmt.Sprintf("VPC不存在: %v", err),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success":    true,
		"vpc_name":   vpc.VPCName,
		"region":     vpc.Region,
		"status":     vpc.Status,
		"az_details": vpc.AZDetails,
		"source":     "database",
	})
}

func (s *Server) deleteVPC(c *gin.Context) {
	vpcName := c.Param("vpc_name")
	ctx := c.Request.Context()

	// 检查 VPC 下是否有 PCCN 连接
	if s.orchestrator.HasPCCNDAO() {
		pccns, err := s.orchestrator.GetPCCNsByVPC(ctx, vpcName)
		if err == nil && len(pccns) > 0 {
			c.JSON(http.StatusBadRequest, gin.H{
				"success": false,
				"message": fmt.Sprintf("VPC存在 %d 个PCCN连接，请先删除PCCN连接", len(pccns)),
			})
			return
		}
	}

	// 检查 VPC 是否存在防火墙策略
	vpc, err := s.orchestrator.GetVPCByName(ctx, vpcName)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"success": false,
			"message": fmt.Sprintf("VPC不存在: %v", err),
		})
		return
	}

	if vpc.FirewallZone != "" {
		policyCount, err := s.orchestrator.CheckZonePolicies(ctx, vpc.FirewallZone)
		if err == nil && policyCount > 0 {
			c.JSON(http.StatusBadRequest, gin.H{
				"success": false,
				"message": fmt.Sprintf("VPC关联的Zone %s 下存在 %d 条防火墙策略，无法删除", vpc.FirewallZone, policyCount),
			})
			return
		}
	}

	// 更新状态为 deleted
	if err := s.orchestrator.UpdateVPCStatus(ctx, vpcName, "deleted", nil); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": fmt.Sprintf("删除VPC失败: %v", err),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "VPC已删除",
	})
}

// =====================================================
// Subnet Handlers
// =====================================================

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
	resp, err := s.orchestrator.CreateAZSubnet(ctx, &req)
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

	// 从请求中获取 region 和 az 参数
	region := c.Query("region")
	az := c.Query("az")

	if region == "" || az == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "缺少region或az参数",
		})
		return
	}

	// 获取 AZ 信息
	azInfo, err := s.registry.GetAZ(ctx, region, az)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"success": false,
			"message": fmt.Sprintf("AZ不存在: %v", err),
		})
		return
	}

	// 直接查询 AZ 获取子网状态
	subnetStatus, err := s.tracedHTTP.Get(ctx, fmt.Sprintf("%s/api/v1/subnet/%s/status", azInfo.NSPAddr, subnetName))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": fmt.Sprintf("查询子网状态失败: %v", err),
		})
		return
	}
	defer subnetStatus.Body.Close()

	if subnetStatus.StatusCode != http.StatusOK {
		c.JSON(subnetStatus.StatusCode, gin.H{
			"success": false,
			"message": "子网不存在",
		})
		return
	}

	// 透传 AZ 返回的响应
	c.DataFromReader(http.StatusOK, subnetStatus.ContentLength, "application/json", subnetStatus.Body, nil)
}

func (s *Server) deleteSubnet(c *gin.Context) {
	subnetName := c.Param("subnet_name")
	ctx := c.Request.Context()

	region := c.Query("region")
	az := c.Query("az")

	if region == "" || az == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "缺少region或az参数",
		})
		return
	}

	azInfo, err := s.registry.GetAZ(ctx, region, az)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"success": false,
			"message": fmt.Sprintf("AZ不存在: %v", err),
		})
		return
	}

	// 转发删除请求到 AZ
	req, _ := http.NewRequestWithContext(ctx, "DELETE", fmt.Sprintf("%s/api/v1/subnet/%s", azInfo.NSPAddr, subnetName), nil)
	resp, err := s.tracedHTTP.Do(req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": fmt.Sprintf("删除子网失败: %v", err),
		})
		return
	}
	defer resp.Body.Close()

	c.DataFromReader(resp.StatusCode, resp.ContentLength, "application/json", resp.Body, nil)
}

// =====================================================
// PCCN Handlers
// =====================================================

func (s *Server) createPCCN(c *gin.Context) {
	var req models.PCCNRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.PCCNResponse{
			Success: false,
			Message: fmt.Sprintf("请求参数错误: %v", err),
		})
		return
	}

	ctx := c.Request.Context()
	resp, err := s.orchestrator.CreatePCCN(ctx, &req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.PCCNResponse{
			Success: false,
			Message: fmt.Sprintf("创建PCCN失败: %v", err),
		})
		return
	}

	if !resp.Success {
		c.JSON(http.StatusBadRequest, resp)
		return
	}

	c.JSON(http.StatusOK, resp)
}

func (s *Server) getPCCNStatus(c *gin.Context) {
	pccnName := c.Param("pccn_name")
	ctx := c.Request.Context()

	if !s.orchestrator.HasPCCNDAO() {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"success": false,
			"message": "PCCN DAO未配置",
		})
		return
	}

	resp, err := s.orchestrator.GetPCCNStatus(ctx, pccnName)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"success": false,
			"message": fmt.Sprintf("PCCN不存在: %v", err),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success":        true,
		"pccn_name":      resp.PCCNName,
		"overall_status": resp.OverallStatus,
		"vpc_details":    resp.VPCDetails,
		"source":         resp.Source,
	})
}

func (s *Server) listPCCNs(c *gin.Context) {
	ctx := c.Request.Context()

	if !s.orchestrator.HasPCCNDAO() {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"success": false,
			"message": "PCCN DAO未配置",
		})
		return
	}

	pccns, err := s.orchestrator.ListPCCNs(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": fmt.Sprintf("获取PCCN列表失败: %v", err),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"pccns":   pccns,
	})
}

func (s *Server) deletePCCN(c *gin.Context) {
	pccnName := c.Param("pccn_name")
	ctx := c.Request.Context()

	if !s.orchestrator.HasPCCNDAO() {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"success": false,
			"message": "PCCN DAO未配置",
		})
		return
	}

	resp, err := s.orchestrator.DeletePCCN(ctx, pccnName)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.PCCNResponse{
			Success: false,
			Message: fmt.Sprintf("删除PCCN失败: %v", err),
		})
		return
	}

	if !resp.Success {
		c.JSON(http.StatusBadRequest, resp)
		return
	}

	c.JSON(http.StatusOK, resp)
}
