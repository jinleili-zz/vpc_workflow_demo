package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"workflow_qoder/internal/az/orchestrator"
	"workflow_qoder/internal/config"
	"workflow_qoder/internal/models"
	"workflow_qoder/internal/operation"

	"github.com/gin-gonic/gin"
	"github.com/jinleili-zz/nsp-platform/auth"
	"github.com/jinleili-zz/nsp-platform/logger"
	"github.com/jinleili-zz/nsp-platform/taskqueue"
	"github.com/jinleili-zz/nsp-platform/trace"
)

type Server struct {
	cfg          *config.NSPConfig
	orchestrator *orchestrator.AZOrchestrator
	opService    *operation.Service
	router       *gin.Engine
	db           *sql.DB
}

func NewServer(cfg *config.NSPConfig, broker taskqueue.Broker, inspector taskqueue.Inspector, tracedHTTP *trace.TracedClient, db *sql.DB, verifier *auth.Verifier) *Server {
	router := gin.New()
	router.Use(gin.Recovery())

	// Add trace middleware for distributed tracing
	instanceID := fmt.Sprintf("az-nsp-vpc-%s-%s", cfg.Region, cfg.AZ)
	router.Use(trace.TraceMiddleware(instanceID))
	router.Use(ginLoggerMiddleware())
	if cfg.Auth.EnableAuth && verifier != nil {
		skipPaths := cfg.Auth.SkipAuthPaths
		if len(skipPaths) == 0 {
			skipPaths = []string{"/api/v1/health"}
		}
		router.Use(auth.AKSKAuthMiddleware(verifier, &auth.MiddlewareOption{
			Skipper: auth.NewSkipperByPath(skipPaths...),
			OnAuthFailed: func(c *gin.Context, err error) {
				c.JSON(http.StatusUnauthorized, gin.H{
					"code":    http.StatusUnauthorized,
					"message": err.Error(),
				})
			},
		}))
	}

	orch := orchestrator.NewAZOrchestrator(db, broker, inspector, tracedHTTP, cfg.Region, cfg.AZ)

	server := &Server{
		cfg:          cfg,
		orchestrator: orch,
		opService:    operation.NewService(db, "az-nsp-vpc"),
		router:       router,
		db:           db,
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

		// PCCN routes
		api.POST("/pccn", s.createPCCN)
		api.GET("/pccn/:pccn_name/status", s.getPccnStatus)
		api.DELETE("/pccn/:pccn_name", s.deletePCCN)
		api.GET("/pccns", s.listPCCNs)

		api.POST("/task/replay/:task_id", s.replayTask)
		api.GET("/task/:task_id", s.getTaskByID)

		// 幂等 Operation 统一查询入口（设计文档 11.1 节）
		api.GET("/operations/:operation_id", s.opService.GetOperation)

		api.GET("/health", s.health)
	}
}

func (s *Server) createVPC(c *gin.Context) {
	var req models.VPCRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.VPCResponse{
			Success: false,
			Code:    operation.CodeInvalidRequest,
			Message: fmt.Sprintf("请求参数错误: %v", err),
		})
		return
	}

	// Saga Step 重试携带稳定的 X-Idempotency-Key（step.ID）与 X-Saga-Transaction-Id，
	// 相同 Step 重试将命中同一 AZ Operation 并重放第一次响应（设计文档 11.2 节）。
	if sagaTxID := c.GetHeader(operation.HeaderSagaTransactionID); sagaTxID != "" {
		logger.InfoContext(c.Request.Context(), "收到Saga VPC创建请求",
			"saga_tx_id", sagaTxID, "idempotency_key", c.GetHeader(operation.HeaderIdempotencyKey))
	}

	s.opService.HandleCreate(c, operation.BeginCommand{
		CallerScope:       operation.CallerScopeFromRequest(c, "top-nsp-vpc"),
		RouteScope:        "POST /api/v1/vpc",
		OperationType:     "create_vpc",
		IdempotencyKey:    c.GetHeader(operation.HeaderIdempotencyKey),
		RootOperationID:   c.GetHeader(operation.HeaderRootOperationID),
		ParentOperationID: c.GetHeader(operation.HeaderParentOperationID),
		Request:           req,
	}, func(ctx context.Context, op *operation.Operation) (int, any) {
		resp, err := s.orchestrator.CreateVPC(ctx, &req)
		if err != nil {
			return http.StatusInternalServerError, &models.VPCResponse{
				Success:     false,
				Code:        operation.CodeInternalError,
				OperationID: op.OperationID,
				Message:     fmt.Sprintf("创建VPC失败: %v", err),
			}
		}
		resp.OperationID = op.OperationID
		return http.StatusOK, resp
	})
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
			"code":    operation.CodeInternalError,
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success":  true,
		"code":     operation.CodeSuccess,
		"message":  "VPC已成功删除",
		"vpc_name": vpcName,
		"az":       s.orchestrator.GetAZ(),
	})
}

func (s *Server) createSubnet(c *gin.Context) {
	var req models.SubnetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.SubnetResponse{
			Success: false,
			Code:    operation.CodeInvalidRequest,
			Message: fmt.Sprintf("请求参数错误: %v", err),
		})
		return
	}

	s.opService.HandleCreate(c, operation.BeginCommand{
		CallerScope:       operation.CallerScopeFromRequest(c, "top-nsp-vpc"),
		RouteScope:        "POST /api/v1/subnet",
		OperationType:     "create_subnet",
		IdempotencyKey:    c.GetHeader(operation.HeaderIdempotencyKey),
		RootOperationID:   c.GetHeader(operation.HeaderRootOperationID),
		ParentOperationID: c.GetHeader(operation.HeaderParentOperationID),
		Request:           req,
	}, func(ctx context.Context, op *operation.Operation) (int, any) {
		resp, err := s.orchestrator.CreateSubnet(ctx, &req)
		if err != nil {
			return http.StatusInternalServerError, &models.SubnetResponse{
				Success:     false,
				Code:        operation.CodeInternalError,
				OperationID: op.OperationID,
				Message:     fmt.Sprintf("创建子网失败: %v", err),
			}
		}
		resp.OperationID = op.OperationID
		return http.StatusOK, resp
	})
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
			"code":    operation.CodeInternalError,
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"code":    operation.CodeSuccess,
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

	// 先获取 VPC 信息用于响应
	vpc, _ := s.orchestrator.GetVPCByID(ctx, vpcID)

	err := s.orchestrator.DeleteVPCByID(ctx, vpcID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"code":    operation.CodeInternalError,
			"message": err.Error(),
		})
		return
	}

	resp := gin.H{
		"success": true,
		"code":    operation.CodeSuccess,
		"message": "VPC已成功删除",
		"az":      s.orchestrator.GetAZ(),
	}
	if vpc != nil {
		resp["vpc_name"] = vpc.VPCName
	}
	c.JSON(http.StatusOK, resp)
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
			"code":    operation.CodeInternalError,
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"code":    operation.CodeSuccess,
		"message": "子网已成功删除",
	})
}

func (s *Server) HandleReplyTask(ctx context.Context, task *taskqueue.Task) error {
	return s.orchestrator.HandleReplyTask(ctx, task)
}

func (s *Server) ReplyQueueName() string {
	return s.orchestrator.ReplyQueueName()
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

// =====================================================
// PCCN Handlers
// =====================================================

func (s *Server) createPCCN(c *gin.Context) {
	var req models.PCCNRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.PCCNResponse{
			Success: false,
			Code:    operation.CodeInvalidRequest,
			Message: fmt.Sprintf("请求参数错误: %v", err),
		})
		return
	}

	if sagaTxID := c.GetHeader(operation.HeaderSagaTransactionID); sagaTxID != "" {
		logger.InfoContext(c.Request.Context(), "收到Saga PCCN创建请求",
			"saga_tx_id", sagaTxID, "idempotency_key", c.GetHeader(operation.HeaderIdempotencyKey), "pccn_name", req.PCCNName, "az", s.cfg.AZ)
	} else {
		logger.InfoContext(c.Request.Context(), "收到PCCN创建请求", "pccn_name", req.PCCNName, "az", s.cfg.AZ)
	}

	s.opService.HandleCreate(c, operation.BeginCommand{
		CallerScope:       operation.CallerScopeFromRequest(c, "top-nsp-vpc"),
		RouteScope:        "POST /api/v1/pccn",
		OperationType:     "create_pccn",
		IdempotencyKey:    c.GetHeader(operation.HeaderIdempotencyKey),
		RootOperationID:   c.GetHeader(operation.HeaderRootOperationID),
		ParentOperationID: c.GetHeader(operation.HeaderParentOperationID),
		Request:           req,
	}, func(ctx context.Context, op *operation.Operation) (int, any) {
		resp, err := s.orchestrator.CreatePCCN(ctx, &req)
		if err != nil {
			return http.StatusInternalServerError, &models.PCCNResponse{
				Success:     false,
				Code:        operation.CodeInternalError,
				OperationID: op.OperationID,
				Message:     fmt.Sprintf("创建PCCN失败: %v", err),
			}
		}
		resp.OperationID = op.OperationID
		if resp.Success {
			return http.StatusOK, resp
		}
		return http.StatusBadRequest, resp
	})
}

func (s *Server) getPccnStatus(c *gin.Context) {
	ctx := c.Request.Context()
	pccnName := c.Param("pccn_name")

	if pccnName == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "pccn_name参数缺失",
		})
		return
	}

	status, err := s.orchestrator.GetPCCNStatus(ctx, pccnName)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, status)
}

func (s *Server) deletePCCN(c *gin.Context) {
	ctx := c.Request.Context()
	pccnName := c.Param("pccn_name")

	if pccnName == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "pccn_name参数缺失",
		})
		return
	}

	if err := s.orchestrator.DeletePCCN(ctx, pccnName); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"code":    operation.CodeInternalError,
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"code":    operation.CodeSuccess,
		"message": "PCCN删除成功",
	})
}

func (s *Server) listPCCNs(c *gin.Context) {
	ctx := c.Request.Context()

	pccns, err := s.orchestrator.ListPCCNs(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": fmt.Sprintf("查询PCCN列表失败: %v", err),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"pccns":   pccns,
	})
}

func (s *Server) Run(addr string) error {
	logger.Info("服务启动", "az", s.cfg.AZ, "addr", addr)
	if s.cfg.TLS.Enabled && s.cfg.TLS.Mode == "process" {
		tlsCtx, cancel := context.WithCancel(context.Background())
		defer cancel()

		tlsConfig, err := newServerTLSConfig(tlsCtx, s.cfg.TLS)
		if err != nil {
			return err
		}

		listener, err := net.Listen("tcp", addr)
		if err != nil {
			return err
		}

		server := &http.Server{
			Addr:      addr,
			Handler:   s.router,
			TLSConfig: tlsConfig,
		}
		return server.ServeTLS(listener, "", "")
	}
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

	nspAddr := s.advertisedNSPAddr()
	if nspAddr == "" {
		return fmt.Errorf("advertised NSP address is empty")
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

func (s *Server) advertisedNSPAddr() string {
	nspAddr := os.Getenv("NSP_ADDR")
	if nspAddr != "" {
		return nspAddr
	}

	scheme := "http"
	if s.cfg.TLS.Enabled && s.cfg.TLS.Mode == "process" {
		scheme = "https"
	}
	return fmt.Sprintf("%s://az-nsp-%s:%d", scheme, s.cfg.AZ, s.cfg.Port)
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

// StartCompensationTask starts the background compensation task that repairs
// inconsistencies between workflow state and resource state.
func (s *Server) StartCompensationTask(ctx context.Context, interval time.Duration) {
	s.orchestrator.StartCompensationTask(ctx, interval)
}
