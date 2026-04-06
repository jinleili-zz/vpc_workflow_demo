// propagator.go - HTTP 请求头读取与写入
// Package trace 提供分布式链路追踪功能
package trace

import (
	"context"
	"encoding/hex"
	"net/http"
)

// HTTP 请求头常量（B3 标准命名）
const (
	// HeaderTraceID B3 标准 TraceId 请求头
	HeaderTraceID = "X-B3-TraceId"

	// HeaderSpanId B3 标准 SpanId 请求头
	HeaderSpanId = "X-B3-SpanId"

	// HeaderSampled B3 标准采样标志请求头
	HeaderSampled = "X-B3-Sampled"

	// HeaderRequestID 兼容网关和老客户端的请求 ID
	HeaderRequestID = "X-Request-Id"

	// 注意：不定义 X-B3-ParentSpanId 常量，本方案不透传该字段
	// 原因：采用现代独立 Span 模型，下游收到上游的 X-B3-SpanId 直接作为自己的 ParentSpanId
)

// isValidHexString 检查字符串是否为有效的 hex 格式
func isValidHexString(s string, expectedLen int) bool {
	if len(s) != expectedLen {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// Extract 从入站 HTTP 请求中提取 TraceContext
//
// TraceID 来源优先级：
//  1. X-B3-TraceId 有值且为有效 32 位 hex → 直接使用（链路中间节点）
//  2. X-Request-Id 有值且为有效 32 位 hex → 用作 TraceID（兼容网关）
//  3. 以上都不满足 → 生成新 TraceID（入口节点，root span）
//
// SpanId：始终为本服务生成新的 SpanId，不复用任何请求头中的值
// ParentSpanId：直接赋值为请求头中 X-B3-SpanId 的值（上游的 SpanId）
// instanceId 由调用方传入（服务启动时调用 GetInstanceId() 初始化一次）
func Extract(r *http.Request, instanceId string) *TraceContext {
	tc := &TraceContext{
		InstanceId: instanceId,
		Sampled:    true, // 默认采样
	}

	// 1. 提取 TraceID（优先级：X-B3-TraceId > X-Request-Id > 新生成）
	// 要求必须是有效的 32 位 hex 字符串，否则生成新 ID 以保证格式一致性
	traceID := r.Header.Get(HeaderTraceID)
	if traceID != "" && isValidHexString(traceID, 32) {
		tc.TraceID = traceID
	} else {
		// 尝试使用 X-Request-Id（仅接受有效 32 位 hex 格式）
		requestID := r.Header.Get(HeaderRequestID)
		if requestID != "" && isValidHexString(requestID, 32) {
			tc.TraceID = requestID
		} else {
			// 格式不符合要求，生成新的 TraceID
			tc.TraceID = NewTraceID()
		}
	}

	// 2. 提取 ParentSpanId（来自请求头中的 X-B3-SpanId）
	parentSpanId := r.Header.Get(HeaderSpanId)
	if parentSpanId != "" && isValidHexString(parentSpanId, 16) {
		tc.ParentSpanId = parentSpanId
	}
	// 格式不对时，ParentSpanId 保持为空（视为 root span）

	// 3. SpanId 始终新生成（不复用请求头中的值）
	tc.SpanId = NewSpanId()

	// 4. 提取 Sampled 标志
	sampled := r.Header.Get(HeaderSampled)
	if sampled == "0" {
		tc.Sampled = false
	}
	// "1" 或其他值（包括空）都视为 true

	return tc
}

// Inject 向出站 HTTP 请求注入追踪信息
//
// 写入规则：
//
//	X-B3-TraceId = tc.TraceID          // 透传 TraceID
//	X-B3-SpanId  = tc.SpanId           // 传自己的 SpanId（下游存为 ParentSpanId）
//	X-B3-Sampled = "1" 或 "0"
//
// 注意：不写入 X-B3-ParentSpanId
func Inject(req *http.Request, tc *TraceContext) {
	if tc == nil {
		return
	}

	// 写入 TraceID
	if tc.TraceID != "" {
		req.Header.Set(HeaderTraceID, tc.TraceID)
	}

	// 写入 SpanId（注意：是自己的 SpanId，下游会将其存为 ParentSpanId）
	if tc.SpanId != "" {
		req.Header.Set(HeaderSpanId, tc.SpanId)
	}

	// 写入 Sampled 标志
	if tc.Sampled {
		req.Header.Set(HeaderSampled, "1")
	} else {
		req.Header.Set(HeaderSampled, "0")
	}

	// 不写入 X-B3-ParentSpanId，本方案不透传该字段
}

// InjectResponse 向 HTTP 响应写入追踪信息（供中间件调用）
//
// 写入规则：
//
//	X-B3-TraceId = tc.TraceID
//	X-Request-Id = tc.TraceID          // 兼容只认 X-Request-Id 的客户端
func InjectResponse(w http.ResponseWriter, tc *TraceContext) {
	if tc == nil {
		return
	}

	// 写入 TraceID
	if tc.TraceID != "" {
		w.Header().Set(HeaderTraceID, tc.TraceID)
		// 同时写入 X-Request-Id，兼容只认 X-Request-Id 的客户端和网关
		w.Header().Set(HeaderRequestID, tc.TraceID)
	}
}

// MetadataFromContext extracts trace information from ctx as a string map,
// suitable for cross-message-queue propagation.
// Returns nil if ctx has no TraceContext.
func MetadataFromContext(ctx context.Context) map[string]string {
	tc, ok := TraceFromContext(ctx)
	if !ok || tc == nil {
		return nil
	}
	return MetadataFromTraceContext(tc)
}

// MetadataFromTraceContext converts a TraceContext to a metadata map.
// This is a helper to avoid duplicate map construction across packages.
// Returns nil if tc is nil.
func MetadataFromTraceContext(tc *TraceContext) map[string]string {
	if tc == nil {
		return nil
	}
	m := map[string]string{
		"trace_id": tc.TraceID,
		"span_id":  tc.SpanId,
		"sampled":  "1",
	}
	if !tc.Sampled {
		m["sampled"] = "0"
	}
	return m
}

// TraceFromMetadata restores a TraceContext from a metadata map.
// instanceId should be obtained via GetInstanceId().
// Returns nil if metadata is empty or has no valid trace_id.
// Performs validation on trace_id (must be 32-char hex) and span_id (must be 16-char hex if present).
func TraceFromMetadata(metadata map[string]string, instanceId string) *TraceContext {
	if len(metadata) == 0 {
		return nil
	}
	traceID := metadata["trace_id"]
	// Validate trace_id format: must be 32-character hex string
	if !isValidHexString(traceID, 32) {
		return nil
	}

	parentSpanId := metadata["span_id"]
	// Validate span_id format if present: must be 16-character hex string
	if parentSpanId != "" && !isValidHexString(parentSpanId, 16) {
		parentSpanId = "" // Invalid format, treat as no parent
	}

	return &TraceContext{
		TraceID:      traceID,
		ParentSpanId: parentSpanId,
		SpanId:       NewSpanId(),
		InstanceId:   instanceId,
		Sampled:      metadata["sampled"] != "0", // defaults to true if missing or not "0"
	}
}

/*
完整调用链示例（gateway → order → stock 三跳的完整字段变化）：

gateway（入口，无上游）：
  入站请求头：无追踪头
  TraceID      = 新生成 T1
  SpanId       = 新生成 S1
  ParentSpanId = ""
  出站请求头：
    X-B3-TraceId = T1
    X-B3-SpanId  = S1
    X-B3-Sampled = 1
  响应头：
    X-B3-TraceId = T1
    X-Request-Id = T1

order（中间节点）：
  入站请求头：
    X-B3-TraceId = T1
    X-B3-SpanId  = S1
  TraceID      = T1        ← 继承自请求头
  SpanId       = 新生成 S2  ← 自己生成，不复用 S1
  ParentSpanId = S1        ← 来自请求头 X-B3-SpanId
  出站请求头：
    X-B3-TraceId = T1
    X-B3-SpanId  = S2      ← 传自己的 SpanId
    X-B3-Sampled = 1
    （无 X-B3-ParentSpanId）

stock（末端节点）：
  入站请求头：
    X-B3-TraceId = T1
    X-B3-SpanId  = S2
  TraceID      = T1
  SpanId       = 新生成 S3
  ParentSpanId = S2        ← 来自请求头 X-B3-SpanId
  出站请求头：无下游调用

通过日志还原调用树：
  WHERE trace_id = T1 ORDER BY timestamp
  S1（parent=""）   → gateway （root）
  S2（parent=S1）   → order   （gateway 的子节点）
  S3（parent=S2）   → stock   （order 的子节点）
*/
