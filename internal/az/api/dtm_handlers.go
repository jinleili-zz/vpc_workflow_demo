package api

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"

	"workflow_qoder/internal/models"

	"github.com/dtm-labs/client/dtmcli"
	"github.com/dtm-labs/client/dtmcli/dtmimp"
	"github.com/gin-gonic/gin"
)

// createVPCAction DTM Saga正向操作：创建VPC（幂等）
func (s *Server) createVPCAction(c *gin.Context) {
	var req models.VPCRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		log.Printf("[AZ NSP %s] DTM Action: 请求参数错误: %v", s.cfg.AZ, err)
		c.JSON(http.StatusOK, gin.H{
			"dtmResult": dtmcli.ResultFailure,
			"message":   fmt.Sprintf("请求参数错误: %v", err),
		})
		return
	}

	log.Printf("[AZ NSP %s] DTM Action: 开始创建VPC: %s (GID: %s)", s.cfg.AZ, req.VPCName, c.Query("gid"))

	// 使用DTM Barrier保证幂等性
	barrier, err := dtmcli.BarrierFromQuery(c.Request.URL.Query())
	if err != nil {
		log.Printf("[AZ NSP %s] DTM Action: Barrier解析失败: %v", s.cfg.AZ, err)
		c.JSON(http.StatusOK, gin.H{
			"dtmResult": dtmcli.ResultFailure,
			"message":   fmt.Sprintf("Barrier解析失败: %v", err),
		})
		return
	}

	// Barrier.Call保证即使重试多次，业务逻辑也只执行一次
	err = barrier.CallWithDB(s.db, func(tx *sql.Tx) error {
		ctx := context.Background()

		// 业务逻辑：创建VPC
		resp, err := s.orchestrator.CreateVPC(ctx, &req)
		if err != nil {
			return fmt.Errorf("创建VPC失败: %v", err)
		}
		if !resp.Success {
			return fmt.Errorf("创建VPC失败: %s", resp.Message)
		}

		log.Printf("[AZ NSP %s] DTM Action: VPC创建成功: %s (WorkflowID: %s)", s.cfg.AZ, req.VPCName, resp.WorkflowID)
		return nil
	})

	if err != nil {
		log.Printf("[AZ NSP %s] DTM Action: 创建VPC失败: %v", s.cfg.AZ, err)
		c.JSON(http.StatusOK, gin.H{
			"dtmResult": dtmcli.ResultFailure,
			"message":   err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"dtmResult": dtmcli.ResultSuccess,
		"message":   "VPC创建成功",
	})
}

// compensateVPCAction DTM Saga补偿操作：删除VPC（幂等）
func (s *Server) compensateVPCAction(c *gin.Context) {
	var req models.VPCRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		log.Printf("[AZ NSP %s] DTM Compensate: 请求参数错误: %v", s.cfg.AZ, err)
		c.JSON(http.StatusOK, gin.H{
			"dtmResult": dtmcli.ResultFailure,
			"message":   fmt.Sprintf("请求参数错误: %v", err),
		})
		return
	}

	log.Printf("[AZ NSP %s] DTM Compensate: 开始回滚VPC: %s (GID: %s)", s.cfg.AZ, req.VPCName, c.Query("gid"))

	// 使用DTM Barrier保证幂等性
	barrier, err := dtmcli.BarrierFromQuery(c.Request.URL.Query())
	if err != nil {
		log.Printf("[AZ NSP %s] DTM Compensate: Barrier解析失败: %v", s.cfg.AZ, err)
		c.JSON(http.StatusOK, gin.H{
			"dtmResult": dtmcli.ResultFailure,
			"message":   fmt.Sprintf("Barrier解析失败: %v", err),
		})
		return
	}

	// Barrier.Call保证补偿操作幂等
	err = barrier.CallWithDB(s.db, func(tx *sql.Tx) error {
		ctx := context.Background()

		// 补偿逻辑：删除VPC
		err := s.orchestrator.DeleteVPC(ctx, req.VPCName)
		if err != nil {
			// 如果VPC不存在，也认为补偿成功（幂等性）
			if err.Error() == fmt.Sprintf("VPC不存在: %s", req.VPCName) {
				log.Printf("[AZ NSP %s] DTM Compensate: VPC不存在，视为补偿成功: %s", s.cfg.AZ, req.VPCName)
				return nil
			}
			return fmt.Errorf("删除VPC失败: %v", err)
		}

		log.Printf("[AZ NSP %s] DTM Compensate: VPC删除成功: %s", s.cfg.AZ, req.VPCName)
		return nil
	})

	if err != nil {
		log.Printf("[AZ NSP %s] DTM Compensate: 删除VPC失败: %v", s.cfg.AZ, err)
		c.JSON(http.StatusOK, gin.H{
			"dtmResult": dtmcli.ResultFailure,
			"message":   err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"dtmResult": dtmcli.ResultSuccess,
		"message":   "VPC补偿成功",
	})
}

// createSubnetAction DTM Saga正向操作：创建子网（幂等）
func (s *Server) createSubnetAction(c *gin.Context) {
	var req models.SubnetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		log.Printf("[AZ NSP %s] DTM Action: 请求参数错误: %v", s.cfg.AZ, err)
		c.JSON(http.StatusOK, gin.H{
			"dtmResult": dtmcli.ResultFailure,
			"message":   fmt.Sprintf("请求参数错误: %v", err),
		})
		return
	}

	log.Printf("[AZ NSP %s] DTM Action: 开始创建子网: %s (GID: %s)", s.cfg.AZ, req.SubnetName, c.Query("gid"))

	barrier, err := dtmcli.BarrierFromQuery(c.Request.URL.Query())
	if err != nil {
		log.Printf("[AZ NSP %s] DTM Action: Barrier解析失败: %v", s.cfg.AZ, err)
		c.JSON(http.StatusOK, gin.H{
			"dtmResult": dtmcli.ResultFailure,
			"message":   fmt.Sprintf("Barrier解析失败: %v", err),
		})
		return
	}

	err = barrier.CallWithDB(s.db, func(tx *sql.Tx) error {
		ctx := context.Background()

		resp, err := s.orchestrator.CreateSubnet(ctx, &req)
		if err != nil {
			return fmt.Errorf("创建子网失败: %v", err)
		}
		if !resp.Success {
			return fmt.Errorf("创建子网失败: %s", resp.Message)
		}

		log.Printf("[AZ NSP %s] DTM Action: 子网创建成功: %s (WorkflowID: %s)", s.cfg.AZ, req.SubnetName, resp.WorkflowID)
		return nil
	})

	if err != nil {
		log.Printf("[AZ NSP %s] DTM Action: 创建子网失败: %v", s.cfg.AZ, err)
		c.JSON(http.StatusOK, gin.H{
			"dtmResult": dtmcli.ResultFailure,
			"message":   err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"dtmResult": dtmcli.ResultSuccess,
		"message":   "子网创建成功",
	})
}

// compensateSubnetAction DTM Saga补偿操作：删除子网（幂等）
func (s *Server) compensateSubnetAction(c *gin.Context) {
	var req models.SubnetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		log.Printf("[AZ NSP %s] DTM Compensate: 请求参数错误: %v", s.cfg.AZ, err)
		c.JSON(http.StatusOK, gin.H{
			"dtmResult": dtmcli.ResultFailure,
			"message":   fmt.Sprintf("请求参数错误: %v", err),
		})
		return
	}

	log.Printf("[AZ NSP %s] DTM Compensate: 开始回滚子网: %s (GID: %s)", s.cfg.AZ, req.SubnetName, c.Query("gid"))

	barrier, err := dtmcli.BarrierFromQuery(c.Request.URL.Query())
	if err != nil {
		log.Printf("[AZ NSP %s] DTM Compensate: Barrier解析失败: %v", s.cfg.AZ, err)
		c.JSON(http.StatusOK, gin.H{
			"dtmResult": dtmcli.ResultFailure,
			"message":   fmt.Sprintf("Barrier解析失败: %v", err),
		})
		return
	}

	err = barrier.CallWithDB(s.db, func(tx *sql.Tx) error {
		ctx := context.Background()

		err := s.orchestrator.DeleteSubnet(ctx, req.SubnetName)
		if err != nil {
			if err.Error() == fmt.Sprintf("子网不存在: %s", req.SubnetName) {
				log.Printf("[AZ NSP %s] DTM Compensate: 子网不存在，视为补偿成功: %s", s.cfg.AZ, req.SubnetName)
				return nil
			}
			return fmt.Errorf("删除子网失败: %v", err)
		}

		log.Printf("[AZ NSP %s] DTM Compensate: 子网删除成功: %s", s.cfg.AZ, req.SubnetName)
		return nil
	})

	if err != nil {
		log.Printf("[AZ NSP %s] DTM Compensate: 删除子网失败: %v", s.cfg.AZ, err)
		c.JSON(http.StatusOK, gin.H{
			"dtmResult": dtmcli.ResultFailure,
			"message":   err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"dtmResult": dtmcli.ResultSuccess,
		"message":   "子网补偿成功",
	})
}

// initBarrierDB 初始化DTM Barrier（确保dtmimp使用AZ NSP的数据库连接）
func (s *Server) initBarrierDB() {
	dtmimp.SetCurrentDBType(dtmimp.DBTypeMysql)
	log.Printf("[AZ NSP %s] DTM Barrier初始化完成 (数据库类型: MySQL)", s.cfg.AZ)
}
