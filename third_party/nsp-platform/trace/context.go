// context.go - TraceContext 结构体 + context.Context 集成
// Package trace 提供分布式链路追踪功能
package trace

import (
	"context"
)

// traceContextKey 是用于在 context 中存储 TraceContext 的私有 key 类型
// 使用空结构体类型避免与其他包的 key 冲突
type traceContextKey struct{}

// TraceContext 链路追踪上下文，包含完整的追踪信息
type TraceContext struct {
	// TraceID 全链路唯一标识（32位hex字符串，16字节随机数）
	TraceID string

	// SpanId 当前服务本次处理的标识（16位hex字符串，8字节随机数）
	SpanId string

	// ParentSpanId 上游服务的 SpanId，root span 时为空字符串
	ParentSpanId string

	// InstanceId 当前服务实例标识（来自 HOSTNAME 环境变量）
	InstanceId string

	// Sampled 是否采样（默认 true）
	Sampled bool
}

// ContextWithTrace 将 TraceContext 注入标准 context
func ContextWithTrace(ctx context.Context, tc *TraceContext) context.Context {
	if tc == nil {
		return ctx
	}
	return context.WithValue(ctx, traceContextKey{}, tc)
}

// TraceFromContext 从 context 中取出 TraceContext
// 不存在时返回 nil, false
func TraceFromContext(ctx context.Context) (*TraceContext, bool) {
	if ctx == nil {
		return nil, false
	}
	tc, ok := ctx.Value(traceContextKey{}).(*TraceContext)
	return tc, ok
}

// MustTraceFromContext 从 context 中取出 TraceContext
// 不存在时返回一个空的 TraceContext（所有字段为空字符串），不 panic
func MustTraceFromContext(ctx context.Context) *TraceContext {
	tc, ok := TraceFromContext(ctx)
	if !ok || tc == nil {
		return &TraceContext{}
	}
	return tc
}

// IsRoot 判断是否为根 Span（ParentSpanId 为空）
func (tc *TraceContext) IsRoot() bool {
	return tc.ParentSpanId == ""
}

// LogFields 返回适合写入结构化日志的字段 map
// 固定包含：trace_id / span_id / instance_id
// ParentSpanId 不为空时额外包含：parent_span_id
// Sampled=false 时所有字段照常返回（采样控制由日志层决定是否输出）
func (tc *TraceContext) LogFields() map[string]string {
	fields := map[string]string{
		"trace_id":    tc.TraceID,
		"span_id":     tc.SpanId,
		"instance_id": tc.InstanceId,
	}

	// ParentSpanId 不为空时才包含该字段
	if tc.ParentSpanId != "" {
		fields["parent_span_id"] = tc.ParentSpanId
	}

	return fields
}

/*
日志集成约定说明：

TraceContext 不直接依赖日志库，通过 LogFields() 与日志模块解耦：

  tc.LogFields() 返回示例：
  {
    "trace_id":      "4bf92f3577b34da6a3ce929d0e0e4736",
    "span_id":       "00f067aa0ba902b7",
    "parent_span_id":"a3f2b1c4d5e6f7a8",   // 有上游时才有此字段
    "instance_id":   "order-pod-7d9f2b"
  }

  日志完整输出示例：
  {
    "time":           "2026-02-27T11:22:59Z",
    "level":          "info",
    "service":        "nsp-order",
    "trace_id":       "4bf92f3577b34da6a3ce929d0e0e4736",
    "span_id":        "00f067aa0ba902b7",
    "parent_span_id": "a3f2b1c4d5e6f7a8",
    "instance_id":    "order-pod-7d9f2b",
    "msg":            "处理订单"
  }

  通过 trace_id 关联同一链路的所有日志
  通过 parent_span_id → span_id 的关系还原调用树
  通过 instance_id 定位到具体 Pod 实例
*/
