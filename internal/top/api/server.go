package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"workflow_qoder/internal/models"
	"workflow_qoder/internal/top/orchestrator"
	"workflow_qoder/internal/top/registry"

	"github.com/gin-gonic/gin"
	"github.com/paic/nsp-common/pkg/logger"
	"github.com/paic/nsp-common/pkg/trace"
)

// Server Top NSP API服务器
type Server struct {
	registry     *registry.Registry
	orchestrator *orchestrator.Orchestrator
	tracedHTTP   *trace.TracedClient
	router       *gin.Engine
}

// NewServer 创建Top NSP服务器 (不立即设置路由，等待中间件配置后再调用 SetupRoutes)
func NewServer(reg *registry.Registry, orch *orchestrator.Orchestrator, tracedHTTP *trace.TracedClient) *Server {
	router := gin.New()

	server := &Server{
		registry:     reg,
		orchestrator: orch,
		tracedHTTP:   tracedHTTP,
		router:       router,
	}

	return server
}

// SetupRoutes 设置路由 (应在中间件配置之后调用)
func (s *Server) SetupRoutes() {
	s.setupRoutes()
}

// Engine 返回底层的gin.Engine
func (s *Server) Engine() *gin.Engine {
	return s.router
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
		api.DELETE("/vpc/:vpc_name", s.deleteVPCByName)
		api.GET("/vpc/id/:vpc_id", s.getVPCByID)
		api.DELETE("/vpc/id/:vpc_id", s.deleteVPCByID)
		api.GET("/vpc/id/:vpc_id/subnets", s.listSubnetsByVPCID)

		// AZ级服务
		api.POST("/subnet", s.createSubnet)
		api.GET("/subnet/id/:subnet_id", s.getSubnetByID)
		api.DELETE("/subnet/id/:subnet_id", s.deleteSubnetByID)

		// 运维接口
		api.POST("/task/replay/:task_id", s.replayTask)

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

	ctx := c.Request.Context()

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

	logger.InfoContext(ctx, "AZ注册成功", "region", req.Region, "az", req.AZ, "addr", req.NSPAddr)

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

	ctx := c.Request.Context()
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
	ctx := c.Request.Context()
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
	ctx := c.Request.Context()

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

	ctx := c.Request.Context()
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

// getVPCStatus 查询VPC状态（优先从DB查询，降级为并行查询所有AZ）
func (s *Server) getVPCStatus(c *gin.Context) {
	vpcName := c.Param("vpc_name")
	ctx := c.Request.Context()

	// 快路径：从 Top 层数据库查询（一个 VPC 一条记录）
	if s.orchestrator.HasTopDAO() {
		vpc, err := s.orchestrator.GetVPCByName(ctx, vpcName)
		if err != nil {
			logger.InfoContext(ctx, "DB查询VPC状态失败，降级为扇出查询", "vpc_name", vpcName, "error", err)
		}
		if err == nil && vpc != nil {
			azStatuses := make(map[string]interface{})
			for azID, detail := range vpc.AZDetails {
				azStatuses[azID] = map[string]interface{}{
					"az":     azID,
					"status": detail.Status,
				}
			}

			c.JSON(http.StatusOK, gin.H{
				"vpc_name":       vpcName,
				"overall_status": vpc.Status,
				"az_statuses":    azStatuses,
				"source":         "database",
			})
			return
		}
	}

	// 慢路径（降级）：扇出查询各 AZ
	azs, err := s.registry.ListAllAZs(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": fmt.Sprintf("获取AZ列表失败: %v", err),
		})
		return
	}

	type AZStatus struct {
		AZ           string                  `json:"az"`
		Status       string                  `json:"status"`
		Progress     models.ResourceProgress `json:"progress"`
		ErrorMessage string                  `json:"error_message,omitempty"`
	}

	azStatuses := make(map[string]*AZStatus)
	var mu sync.Mutex
	var wg sync.WaitGroup
	overallStatus := "running"
	hasCreating := false
	hasFailed := false

	for _, az := range azs {
		wg.Add(1)
		go func(az *models.AZ) {
			defer wg.Done()

			statusURL := fmt.Sprintf("%s/api/v1/vpc/%s/status", az.NSPAddr, vpcName)
			resp, err := s.tracedHTTP.Get(ctx, statusURL)
			if err != nil {
				logger.InfoContext(ctx, "查询AZ的VPC状态失败", "az", az.ID, "error", err)
				mu.Lock()
				azStatuses[az.ID] = &AZStatus{
					AZ:           az.ID,
					Status:       "unknown",
					ErrorMessage: fmt.Sprintf("查询失败: %v", err),
				}
				mu.Unlock()
				return
			}

			if resp.StatusCode == http.StatusNotFound {
				resp.Body.Close()
				mu.Lock()
				azStatuses[az.ID] = &AZStatus{
					AZ:     az.ID,
					Status: "not_found",
				}
				mu.Unlock()
				return
			}

			var vpcStatus models.VPCStatusResponse
			if err := json.NewDecoder(resp.Body).Decode(&vpcStatus); err != nil {
				resp.Body.Close()
				logger.InfoContext(ctx, "解析AZ的VPC状态失败", "az", az.ID, "error", err)
				return
			}
			resp.Body.Close()

			mu.Lock()
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
			mu.Unlock()
		}(az)
	}

	wg.Wait()

	if hasFailed {
		overallStatus = "failed"
	} else if hasCreating {
		overallStatus = "creating"
	}

	c.JSON(http.StatusOK, gin.H{
		"vpc_name":       vpcName,
		"overall_status": overallStatus,
		"az_statuses":    azStatuses,
		"source":         "fallback",
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

	ctx := c.Request.Context()
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
	ctx := c.Request.Context()

	// 快路径：从 DB 直查（每个 VPC 一条记录）
	if s.orchestrator.HasTopDAO() {
		vpcs, err := s.orchestrator.ListAllVPCs(ctx)
		if err != nil {
			logger.InfoContext(ctx, "DB查询VPC列表失败，降级为扇出查询", "error", err)
		}
		if err == nil {
			c.JSON(http.StatusOK, gin.H{
				"success": true,
				"vpcs":    vpcs,
				"source":  "database",
			})
			return
		}
	}

	// 降级：扇出查询各 AZ
	azs, err := s.registry.ListAllAZs(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": fmt.Sprintf("获取AZ列表失败: %v", err),
		})
		return
	}

	var allVPCs []interface{}
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, az := range azs {
		wg.Add(1)
		go func(az *models.AZ) {
			defer wg.Done()

			listURL := fmt.Sprintf("%s/api/v1/vpcs", az.NSPAddr)
			resp, err := s.tracedHTTP.Get(ctx, listURL)
			if err != nil {
				logger.InfoContext(ctx, "查询AZ的VPC列表失败", "az", az.ID, "error", err)
				return
			}

			var result struct {
				Success bool                     `json:"success"`
				VPCs    []map[string]interface{} `json:"vpcs"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				resp.Body.Close()
				logger.InfoContext(ctx, "解析AZ的VPC列表失败", "az", az.ID, "error", err)
				return
			}
			resp.Body.Close()

			mu.Lock()
			for _, vpc := range result.VPCs {
				allVPCs = append(allVPCs, vpc)
			}
			mu.Unlock()
		}(az)
	}

	wg.Wait()

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"vpcs":    allVPCs,
	})
}

func (s *Server) getVPCByID(c *gin.Context) {
	vpcID := c.Param("vpc_id")
	ctx := c.Request.Context()

	azs, err := s.registry.ListAllAZs(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": fmt.Sprintf("获取AZ列表失败: %v", err),
		})
		return
	}

	type result struct {
		found  bool
		data   map[string]interface{}
	}

	resChan := make(chan result, 1)
	var wg sync.WaitGroup

	for _, az := range azs {
		wg.Add(1)
		go func(az *models.AZ) {
			defer wg.Done()

			getURL := fmt.Sprintf("%s/api/v1/vpc/id/%s", az.NSPAddr, vpcID)
			resp, err := s.tracedHTTP.Get(ctx, getURL)
			if err != nil {
				return
			}

			if resp.StatusCode == http.StatusOK {
				var data map[string]interface{}
				if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
					resp.Body.Close()
					return
				}
				resp.Body.Close()
				select {
				case resChan <- result{found: true, data: data}:
				default:
				}
				return
			}
			resp.Body.Close()
		}(az)
	}

	go func() {
		wg.Wait()
		close(resChan)
	}()

	res, ok := <-resChan
	if ok && res.found {
		c.JSON(http.StatusOK, res.data)
		return
	}

	c.JSON(http.StatusNotFound, gin.H{
		"success": false,
		"message": "VPC不存在",
	})
}

// deleteVPCByName 按 vpc_name 删除 VPC（扇出到所有相关 AZ，然后更新 Top 层 vpc_registry）
func (s *Server) deleteVPCByName(c *gin.Context) {
	vpcName := c.Param("vpc_name")
	ctx := c.Request.Context()

	// 先从 DB 获取 VPC 信息，确定涉及的 AZ
	var targetAZs []*models.AZ
	if s.orchestrator.HasTopDAO() {
		vpc, err := s.orchestrator.GetVPCByName(ctx, vpcName)
		if err != nil || vpc == nil {
			c.JSON(http.StatusNotFound, gin.H{
				"success": false,
				"message": fmt.Sprintf("VPC不存在: %s", vpcName),
			})
			return
		}
		for azID := range vpc.AZDetails {
			az, err := s.registry.GetAZ(ctx, vpc.Region, azID)
			if err == nil && az != nil {
				targetAZs = append(targetAZs, az)
			}
		}
	}

	// 降级：如果没有 DB 或没找到目标 AZ，扇出到所有 AZ
	if len(targetAZs) == 0 {
		azs, err := s.registry.ListAllAZs(ctx)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"success": false,
				"message": fmt.Sprintf("获取AZ列表失败: %v", err),
			})
			return
		}
		targetAZs = azs
	}

	var deletedAZs []string
	var lastError string
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, az := range targetAZs {
		wg.Add(1)
		go func(az *models.AZ) {
			defer wg.Done()

			deleteURL := fmt.Sprintf("%s/api/v1/vpc/%s", az.NSPAddr, vpcName)
			req, _ := http.NewRequest(http.MethodDelete, deleteURL, nil)
			resp, err := s.tracedHTTP.Do(req.WithContext(ctx))
			if err != nil {
				mu.Lock()
				if lastError == "" {
					lastError = fmt.Sprintf("AZ %s 请求失败: %v", az.ID, err)
				}
				mu.Unlock()
				return
			}

			var result map[string]interface{}
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				resp.Body.Close()
				return
			}
			resp.Body.Close()

			mu.Lock()
			if resp.StatusCode == http.StatusOK {
				deletedAZs = append(deletedAZs, az.ID)
			} else if msg, ok := result["message"].(string); ok && lastError == "" {
				lastError = msg
			}
			mu.Unlock()
		}(az)
	}

	wg.Wait()

	if len(deletedAZs) > 0 {
		// 同步更新 Top 层 vpc_registry
		s.syncVPCDeletionStatus(ctx, vpcName, deletedAZs)

		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"message": fmt.Sprintf("VPC已成功删除，%d个AZ已清理", len(deletedAZs)),
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

func (s *Server) deleteVPCByID(c *gin.Context) {
	vpcID := c.Param("vpc_id")
	ctx := c.Request.Context()

	azs, err := s.registry.ListAllAZs(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": fmt.Sprintf("获取AZ列表失败: %v", err),
		})
		return
	}

	var deleted bool
	var deletedVPCName string
	var deletedAZs []string
	var lastError string
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, az := range azs {
		wg.Add(1)
		go func(az *models.AZ) {
			defer wg.Done()

			deleteURL := fmt.Sprintf("%s/api/v1/vpc/id/%s", az.NSPAddr, vpcID)
			req, _ := http.NewRequest(http.MethodDelete, deleteURL, nil)
			resp, err := s.tracedHTTP.Do(req.WithContext(ctx))
			if err != nil {
				return
			}

			var result map[string]interface{}
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				resp.Body.Close()
				return
			}
			resp.Body.Close()

			mu.Lock()
			if resp.StatusCode == http.StatusOK {
				deleted = true
				if name, ok := result["vpc_name"].(string); ok && name != "" {
					deletedVPCName = name
				}
				if azID, ok := result["az"].(string); ok && azID != "" {
					deletedAZs = append(deletedAZs, azID)
				}
			} else if msg, ok := result["message"].(string); ok && lastError == "" {
				lastError = msg
			}
			mu.Unlock()
		}(az)
	}

	wg.Wait()

	if deleted {
		// 同步更新 Top 层 vpc_registry
		s.syncVPCDeletionStatus(ctx, deletedVPCName, deletedAZs)

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

// syncVPCDeletionStatus 将 AZ 层的删除结果同步到 Top 层 vpc_registry
func (s *Server) syncVPCDeletionStatus(ctx context.Context, vpcName string, deletedAZs []string) {
	if !s.orchestrator.HasTopDAO() || vpcName == "" {
		return
	}
	vpc, err := s.orchestrator.GetVPCByName(ctx, vpcName)
	if err != nil || vpc == nil {
		return
	}

	azDetails := make(map[string]models.AZDetail)
	for azID, detail := range vpc.AZDetails {
		azDetails[azID] = detail
	}
	for _, azID := range deletedAZs {
		azDetails[azID] = models.AZDetail{Status: "deleted"}
	}

	overallStatus := "deleted"
	for _, d := range azDetails {
		if d.Status != "deleted" {
			overallStatus = "partial_deleted"
			break
		}
	}
	if err := s.orchestrator.UpdateVPCStatus(ctx, vpcName, overallStatus, azDetails); err != nil {
		logger.InfoContext(ctx, "更新Top层VPC状态失败", "vpc_name", vpcName, "error", err)
	}
}

func (s *Server) listSubnetsByVPCID(c *gin.Context) {
	vpcID := c.Param("vpc_id")
	ctx := c.Request.Context()

	azs, err := s.registry.ListAllAZs(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": fmt.Sprintf("获取AZ列表失败: %v", err),
		})
		return
	}

	var allSubnets []interface{}
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, az := range azs {
		wg.Add(1)
		go func(az *models.AZ) {
			defer wg.Done()

			listURL := fmt.Sprintf("%s/api/v1/vpc/id/%s/subnets", az.NSPAddr, vpcID)
			resp, err := s.tracedHTTP.Get(ctx, listURL)
			if err != nil {
				return
			}

			var result struct {
				Success bool                     `json:"success"`
				Subnets []map[string]interface{} `json:"subnets"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				resp.Body.Close()
				return
			}
			resp.Body.Close()

			mu.Lock()
			for _, subnet := range result.Subnets {
				allSubnets = append(allSubnets, subnet)
			}
			mu.Unlock()
		}(az)
	}

	wg.Wait()

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"subnets": allSubnets,
	})
}

func (s *Server) getSubnetByID(c *gin.Context) {
	subnetID := c.Param("subnet_id")
	ctx := c.Request.Context()

	azs, err := s.registry.ListAllAZs(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": fmt.Sprintf("获取AZ列表失败: %v", err),
		})
		return
	}

	type result struct {
		found bool
		data  map[string]interface{}
	}

	resChan := make(chan result, 1)
	var wg sync.WaitGroup

	for _, az := range azs {
		wg.Add(1)
		go func(az *models.AZ) {
			defer wg.Done()

			getURL := fmt.Sprintf("%s/api/v1/subnet/id/%s", az.NSPAddr, subnetID)
			resp, err := s.tracedHTTP.Get(ctx, getURL)
			if err != nil {
				return
			}

			if resp.StatusCode == http.StatusOK {
				var data map[string]interface{}
				if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
					resp.Body.Close()
					return
				}
				resp.Body.Close()
				select {
				case resChan <- result{found: true, data: data}:
				default:
				}
				return
			}
			resp.Body.Close()
		}(az)
	}

	go func() {
		wg.Wait()
		close(resChan)
	}()

	res, ok := <-resChan
	if ok && res.found {
		c.JSON(http.StatusOK, res.data)
		return
	}

	c.JSON(http.StatusNotFound, gin.H{
		"success": false,
		"message": "子网不存在",
	})
}

func (s *Server) deleteSubnetByID(c *gin.Context) {
	subnetID := c.Param("subnet_id")
	ctx := c.Request.Context()

	azs, err := s.registry.ListAllAZs(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": fmt.Sprintf("获取AZ列表失败: %v", err),
		})
		return
	}

	var deleted bool
	var lastError string
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, az := range azs {
		wg.Add(1)
		go func(az *models.AZ) {
			defer wg.Done()

			deleteURL := fmt.Sprintf("%s/api/v1/subnet/id/%s", az.NSPAddr, subnetID)
			req, _ := http.NewRequest(http.MethodDelete, deleteURL, nil)
			resp, err := s.tracedHTTP.Do(req.WithContext(ctx))
			if err != nil {
				return
			}

			var result map[string]interface{}
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				resp.Body.Close()
				return
			}
			resp.Body.Close()

			mu.Lock()
			if resp.StatusCode == http.StatusOK {
				deleted = true
			} else if msg, ok := result["message"].(string); ok && lastError == "" {
				lastError = msg
			}
			mu.Unlock()
		}(az)
	}

	wg.Wait()

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

func (s *Server) replayTask(c *gin.Context) {
	taskID := c.Param("task_id")
	ctx := c.Request.Context()

	azs, err := s.registry.ListAllAZs(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": fmt.Sprintf("获取AZ列表失败: %v", err),
		})
		return
	}

	type result struct {
		found bool
		data  map[string]interface{}
	}

	resChan := make(chan result, 1)
	var wg sync.WaitGroup

	for _, az := range azs {
		wg.Add(1)
		go func(az *models.AZ) {
			defer wg.Done()

			replayURL := fmt.Sprintf("%s/api/v1/task/replay/%s", az.NSPAddr, taskID)
			req, _ := http.NewRequest(http.MethodPost, replayURL, nil)
			resp, err := s.tracedHTTP.Do(req.WithContext(ctx))
			if err != nil {
				return
			}

			if resp.StatusCode == http.StatusOK {
				var data map[string]interface{}
				if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
					resp.Body.Close()
					return
				}
				resp.Body.Close()
				select {
				case resChan <- result{found: true, data: data}:
				default:
				}
				return
			}
			resp.Body.Close()
		}(az)
	}

	go func() {
		wg.Wait()
		close(resChan)
	}()

	res, ok := <-resChan
	if ok && res.found {
		c.JSON(http.StatusOK, res.data)
		return
	}

	c.JSON(http.StatusNotFound, gin.H{
		"success": false,
		"message": "任务不存在或重做失败",
	})
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
	logger.Info("服务启动", "service", "top-nsp", "addr", addr)
	return s.router.Run(addr)
}
