package api

import (
	"context"
	"encoding/json"
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
		api.GET("/vpcs", s.listVPCs)
		api.POST("/vpc", s.createVPC)
		api.GET("/vpc/:vpc_name/status", s.getVPCStatus)
		api.GET("/vpc/id/:vpc_id", s.getVPCByID)
		api.DELETE("/vpc/id/:vpc_id", s.deleteVPCByID)
		api.GET("/vpc/id/:vpc_id/subnets", s.listSubnetsByVPCID)

		// AZ级服务
		api.POST("/subnet", s.createSubnet)
		api.GET("/subnet/id/:subnet_id", s.getSubnetByID)
		api.DELETE("/subnet/id/:subnet_id", s.deleteSubnetByID)

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
	ctx := context.Background()

	azs, err := s.registry.ListAllAZs(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": fmt.Sprintf("获取AZ列表失败: %v", err),
		})
		return
	}

	type AZStatus struct {
		AZ           string                 `json:"az"`
		Status       string                 `json:"status"`
		Progress     models.ResourceProgress `json:"progress"`
		ErrorMessage string                 `json:"error_message,omitempty"`
	}

	azStatuses := make(map[string]*AZStatus)
	overallStatus := "running"
	hasCreating := false
	hasFailed := false

	for _, az := range azs {
		statusURL := fmt.Sprintf("%s/api/v1/vpc/%s/status", az.NSPAddr, vpcName)
		resp, err := http.Get(statusURL)
		if err != nil {
			log.Printf("[Top NSP] 查询AZ %s的VPC状态失败: %v", az.ID, err)
			azStatuses[az.ID] = &AZStatus{
				AZ:           az.ID,
				Status:       "unknown",
				ErrorMessage: fmt.Sprintf("查询失败: %v", err),
			}
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusNotFound {
			azStatuses[az.ID] = &AZStatus{
				AZ:     az.ID,
				Status: "not_found",
			}
			continue
		}

		var vpcStatus models.VPCStatusResponse
		if err := json.NewDecoder(resp.Body).Decode(&vpcStatus); err != nil {
			log.Printf("[Top NSP] 解析AZ %s的VPC状态失败: %v", az.ID, err)
			continue
		}

		azStatuses[az.ID] = &AZStatus{
			AZ:           az.ID,
			Status:       string(vpcStatus.Status),
			Progress:     vpcStatus.Progress,
			ErrorMessage: vpcStatus.ErrorMessage,
		}

		if vpcStatus.Status == models.ResourceStatusCreating {
			hasCreating = true
		}
		if vpcStatus.Status == models.ResourceStatusFailed {
			hasFailed = true
		}
	}

	if hasFailed {
		overallStatus = "failed"
	} else if hasCreating {
		overallStatus = "creating"
	}

	c.JSON(http.StatusOK, gin.H{
		"vpc_name":       vpcName,
		"overall_status": overallStatus,
		"az_statuses":    azStatuses,
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

func (s *Server) listVPCs(c *gin.Context) {
	ctx := context.Background()

	azs, err := s.registry.ListAllAZs(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": fmt.Sprintf("获取AZ列表失败: %v", err),
		})
		return
	}

	var allVPCs []interface{}

	for _, az := range azs {
		listURL := fmt.Sprintf("%s/api/v1/vpcs", az.NSPAddr)
		resp, err := http.Get(listURL)
		if err != nil {
			log.Printf("[Top NSP] 查询AZ %s的VPC列表失败: %v", az.ID, err)
			continue
		}
		defer resp.Body.Close()

		var result struct {
			Success bool                     `json:"success"`
			VPCs    []map[string]interface{} `json:"vpcs"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			log.Printf("[Top NSP] 解析AZ %s的VPC列表失败: %v", az.ID, err)
			continue
		}

		for _, vpc := range result.VPCs {
			allVPCs = append(allVPCs, vpc)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"vpcs":    allVPCs,
	})
}

func (s *Server) getVPCByID(c *gin.Context) {
	vpcID := c.Param("vpc_id")
	ctx := context.Background()

	azs, err := s.registry.ListAllAZs(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": fmt.Sprintf("获取AZ列表失败: %v", err),
		})
		return
	}

	for _, az := range azs {
		getURL := fmt.Sprintf("%s/api/v1/vpc/id/%s", az.NSPAddr, vpcID)
		resp, err := http.Get(getURL)
		if err != nil {
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			var result map[string]interface{}
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				continue
			}
			c.JSON(http.StatusOK, result)
			return
		}
	}

	c.JSON(http.StatusNotFound, gin.H{
		"success": false,
		"message": "VPC不存在",
	})
}

func (s *Server) deleteVPCByID(c *gin.Context) {
	vpcID := c.Param("vpc_id")
	ctx := context.Background()

	azs, err := s.registry.ListAllAZs(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": fmt.Sprintf("获取AZ列表失败: %v", err),
		})
		return
	}

	deleted := false
	var lastError string

	for _, az := range azs {
		deleteURL := fmt.Sprintf("%s/api/v1/vpc/id/%s", az.NSPAddr, vpcID)
		req, _ := http.NewRequest(http.MethodDelete, deleteURL, nil)
		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		defer resp.Body.Close()

		var result map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			continue
		}

		if resp.StatusCode == http.StatusOK {
			deleted = true
		} else if msg, ok := result["message"].(string); ok {
			lastError = msg
		}
	}

	if deleted {
		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"message": "VPC已成功删除",
		})
	} else if lastError != "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": lastError,
		})
	} else {
		c.JSON(http.StatusNotFound, gin.H{
			"success": false,
			"message": "VPC不存在",
		})
	}
}

func (s *Server) listSubnetsByVPCID(c *gin.Context) {
	vpcID := c.Param("vpc_id")
	ctx := context.Background()

	azs, err := s.registry.ListAllAZs(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": fmt.Sprintf("获取AZ列表失败: %v", err),
		})
		return
	}

	var allSubnets []interface{}

	for _, az := range azs {
		listURL := fmt.Sprintf("%s/api/v1/vpc/id/%s/subnets", az.NSPAddr, vpcID)
		resp, err := http.Get(listURL)
		if err != nil {
			continue
		}
		defer resp.Body.Close()

		var result struct {
			Success bool                     `json:"success"`
			Subnets []map[string]interface{} `json:"subnets"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			continue
		}

		for _, subnet := range result.Subnets {
			allSubnets = append(allSubnets, subnet)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"subnets": allSubnets,
	})
}

func (s *Server) getSubnetByID(c *gin.Context) {
	subnetID := c.Param("subnet_id")
	ctx := context.Background()

	azs, err := s.registry.ListAllAZs(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": fmt.Sprintf("获取AZ列表失败: %v", err),
		})
		return
	}

	for _, az := range azs {
		getURL := fmt.Sprintf("%s/api/v1/subnet/id/%s", az.NSPAddr, subnetID)
		resp, err := http.Get(getURL)
		if err != nil {
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			var result map[string]interface{}
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				continue
			}
			c.JSON(http.StatusOK, result)
			return
		}
	}

	c.JSON(http.StatusNotFound, gin.H{
		"success": false,
		"message": "子网不存在",
	})
}

func (s *Server) deleteSubnetByID(c *gin.Context) {
	subnetID := c.Param("subnet_id")
	ctx := context.Background()

	azs, err := s.registry.ListAllAZs(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": fmt.Sprintf("获取AZ列表失败: %v", err),
		})
		return
	}

	deleted := false
	var lastError string

	for _, az := range azs {
		deleteURL := fmt.Sprintf("%s/api/v1/subnet/id/%s", az.NSPAddr, subnetID)
		req, _ := http.NewRequest(http.MethodDelete, deleteURL, nil)
		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		defer resp.Body.Close()

		var result map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			continue
		}

		if resp.StatusCode == http.StatusOK {
			deleted = true
		} else if msg, ok := result["message"].(string); ok {
			lastError = msg
		}
	}

	if deleted {
		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"message": "子网已成功删除",
		})
	} else if lastError != "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": lastError,
		})
	} else {
		c.JSON(http.StatusNotFound, gin.H{
			"success": false,
			"message": "子网不存在",
		})
	}
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