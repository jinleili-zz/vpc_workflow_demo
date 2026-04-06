// middleware.go - Gin 服务端中间件
// Package trace 提供分布式链路追踪功能
package trace

import (
	"github.com/gin-gonic/gin"
	"github.com/jinleili-zz/nsp-platform/logger"
)

// ginTraceKey 用于在 gin.Context 中存储 TraceContext 的 key
const ginTraceKey = "nsp.trace"

// TraceMiddleware 返回一个 Gin 中间件，用于处理分布式链路追踪
//
// 执行逻辑：
//  1. 调用 Extract 提取或生成 TraceContext
//  2. 写入标准 context 并更新 c.Request
//  3. 写入 gin.Context（供不使用标准 context 的 Handler 直接访问）
//  4. 调用 InjectResponse 向响应头写入追踪信息
//  5. 调用 c.Next()
//
// instanceId 应在服务启动时调用 GetInstanceId() 初始化一次后传入
func TraceMiddleware(instanceId string) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 1. 从请求中提取或生成 TraceContext
		tc := Extract(c.Request, instanceId)

		// 2. 将 TraceContext 注入标准 context，并更新 c.Request
		ctx := ContextWithTrace(c.Request.Context(), tc)

		// 3. 同时写入 logger 模块的 context keys，保证日志自动关联 trace_id 和 span_id
		ctx = logger.ContextWithTraceID(ctx, tc.TraceID)
		ctx = logger.ContextWithSpanID(ctx, tc.SpanId)

		c.Request = c.Request.WithContext(ctx)

		// 4. 同时写入 gin.Context（供不使用标准 context 的 Handler 直接访问）
		c.Set(ginTraceKey, tc)

		// 5. 向响应头写入追踪信息
		InjectResponse(c.Writer, tc)

		// 6. 继续处理请求
		c.Next()
	}
}

// TraceFromGin 从 gin.Context 取出 TraceContext
// 先尝试从 gin.Context 取，取不到再从 c.Request.Context() 取
// 两者都取不到时返回 nil, false
func TraceFromGin(c *gin.Context) (*TraceContext, bool) {
	// 先尝试从 gin.Context 取
	if val, exists := c.Get(ginTraceKey); exists {
		if tc, ok := val.(*TraceContext); ok {
			return tc, true
		}
	}

	// 再尝试从 c.Request.Context() 取
	if c.Request != nil {
		return TraceFromContext(c.Request.Context())
	}

	return nil, false
}
