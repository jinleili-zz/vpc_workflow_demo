package operation

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// 幂等相关 HTTP Header（设计文档 11.1/11.2 节）
const (
	// HeaderIdempotencyKey 是北向调用方与 Saga Step 共用的幂等键 Header。
	// gin/http 对 Header 名大小写不敏感，"Idempotency-Key" 与 "X-Idempotency-Key" 均可读取。
	HeaderIdempotencyKey     = "X-Idempotency-Key"
	HeaderSagaTransactionID  = "X-Saga-Transaction-Id"
	HeaderRootOperationID    = "X-Root-Operation-Id"
	HeaderParentOperationID  = "X-Parent-Operation-Id"
	defaultReplayWaitTimeout = 10 * time.Second
)

// CallerScopeFromRequest 推导调用方作用域。认证关闭的过渡期使用固定命名空间：
// 携带 Saga Header 的请求视为 Top/Saga 调用，其余视为北向直连调用。
// 不得使用来源 IP 作为作用域（设计文档 7.13 节）。
func CallerScopeFromRequest(c *gin.Context, sagaCaller string) string {
	if c.GetHeader(HeaderSagaTransactionID) != "" {
		return sagaCaller
	}
	return "northbound"
}

// HandleCreate 实现幂等写接口的统一接入流程（设计文档 10.1 节算法）：
//  1. Begin：唯一约束线性化，只有 INSERT 胜出者执行 fn；
//  2. 相同 Key + 相同请求：重放已保存响应；首个请求仍在执行则短暂等待其完成；
//  3. 相同 Key + 不同请求：409 IDEMPOTENCY_KEY_REUSED；
//  4. fn 的响应在写回客户端前持久化到 Operation，保证响应丢失后可重放。
//
// fn 返回 (httpCode, responseBody)；httpCode >= 400 时 Operation 记为 failed，
// 失败结果同样会被重放（相同 Key 不会悄悄重新执行）。
func (s *Service) HandleCreate(c *gin.Context, cmd BeginCommand, fn func(ctx context.Context, op *Operation) (int, any)) {
	ctx := c.Request.Context()

	op, created, err := s.Begin(ctx, cmd)
	if err == ErrRequestConflict {
		c.JSON(http.StatusConflict, gin.H{
			"success":      false,
			"code":         CodeIdempotencyKeyReused,
			"message":      "相同幂等键携带了不同的请求参数",
			"operation_id": op.OperationID,
		})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"code":    CodeInternalError,
			"message": err.Error(),
		})
		return
	}

	if !created {
		s.replay(c, op)
		return
	}

	httpCode, resp := fn(ctx, op)

	payload, merr := json.Marshal(resp)
	status := StatusSucceeded
	if httpCode >= http.StatusBadRequest {
		status = StatusFailed
	}
	if merr == nil {
		if cerr := s.Complete(ctx, op.OperationID, status, httpCode, payload); cerr != nil {
			// 完成状态写库失败不影响本次响应；相同 Key 的后续重试会在等待后得到 503，
			// 而不会产生第二次业务执行。
			c.Header("X-Operation-Complete-Error", "true")
		}
	}

	c.JSON(httpCode, resp)
}

// replay 重放已有 Operation 的响应；仍在执行中的 Operation 会等待其完成。
func (s *Service) replay(c *gin.Context, op *Operation) {
	if !op.IsTerminal() {
		waited, err := s.WaitTerminal(c.Request.Context(), op.OperationID, defaultReplayWaitTimeout)
		if err != nil || !waited.IsTerminal() {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"success":      false,
				"code":         CodeOperationUnavailable,
				"message":      "相同请求仍在执行中，请稍后通过 operation_id 查询结果",
				"operation_id": op.OperationID,
			})
			return
		}
		op = waited
	}

	if len(op.ResponsePayload) == 0 {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"success":      false,
			"code":         CodeOperationUnavailable,
			"message":      "Operation 已完成但响应不可重放，请通过 operation_id 查询",
			"operation_id": op.OperationID,
		})
		return
	}

	httpCode := op.ResponseCode
	if httpCode == 0 {
		httpCode = http.StatusOK
	}
	c.Data(httpCode, "application/json; charset=utf-8", op.ResponsePayload)
}

// GetOperation 是 GET /api/v1/operations/:operation_id 的统一处理器。
func (s *Service) GetOperation(c *gin.Context) {
	op, err := s.GetByID(c.Request.Context(), c.Param("operation_id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"success": false,
			"code":    CodeInvalidRequest,
			"message": err.Error(),
		})
		return
	}
	c.JSON(http.StatusOK, op)
}
