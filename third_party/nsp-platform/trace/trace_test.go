// trace_test.go - 单元测试
// Package trace 提供分布式链路追踪功能
package trace

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestExtractNoHeaders 测试场景 1：无任何追踪头时（root span）
func TestExtractNoHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	tc := Extract(req, "test-instance")

	// 应生成新 TraceID
	if tc.TraceID == "" {
		t.Error("TraceID should be generated")
	}
	if len(tc.TraceID) != 32 {
		t.Errorf("TraceID length should be 32, got %d", len(tc.TraceID))
	}

	// ParentSpanId 应为空（root span）
	if tc.ParentSpanId != "" {
		t.Errorf("ParentSpanId should be empty for root span, got %s", tc.ParentSpanId)
	}

	// SpanId 应非空
	if tc.SpanId == "" {
		t.Error("SpanId should be generated")
	}
	if len(tc.SpanId) != 16 {
		t.Errorf("SpanId length should be 16, got %d", len(tc.SpanId))
	}

	// InstanceId 应正确
	if tc.InstanceId != "test-instance" {
		t.Errorf("InstanceId should be 'test-instance', got %s", tc.InstanceId)
	}

	// Sampled 默认为 true
	if !tc.Sampled {
		t.Error("Sampled should be true by default")
	}

	// 应为 root span
	if !tc.IsRoot() {
		t.Error("Should be root span")
	}
}

// TestExtractWithTraceIDOnly 测试场景 2：有 X-B3-TraceId 无 X-B3-SpanId 时
func TestExtractWithTraceIDOnly(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set(HeaderTraceID, "4bf92f3577b34da6a3ce929d0e0e4736")

	tc := Extract(req, "test-instance")

	// 应继承 TraceID
	if tc.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("TraceID should be inherited, got %s", tc.TraceID)
	}

	// ParentSpanId 应为空
	if tc.ParentSpanId != "" {
		t.Errorf("ParentSpanId should be empty, got %s", tc.ParentSpanId)
	}

	// SpanId 应新生成
	if tc.SpanId == "" {
		t.Error("SpanId should be generated")
	}
}

// TestExtractWithTraceIDAndSpanID 测试场景 3：有 X-B3-TraceId 有 X-B3-SpanId 时
func TestExtractWithTraceIDAndSpanID(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set(HeaderTraceID, "4bf92f3577b34da6a3ce929d0e0e4736")
	req.Header.Set(HeaderSpanId, "00f067aa0ba902b7")

	tc := Extract(req, "test-instance")

	// 应继承 TraceID
	if tc.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("TraceID should be inherited, got %s", tc.TraceID)
	}

	// ParentSpanId 应等于请求头中的 X-B3-SpanId
	if tc.ParentSpanId != "00f067aa0ba902b7" {
		t.Errorf("ParentSpanId should be '00f067aa0ba902b7', got %s", tc.ParentSpanId)
	}

	// SpanId 应新生成，不复用请求头中的值
	if tc.SpanId == "00f067aa0ba902b7" {
		t.Error("SpanId should be newly generated, not reused from header")
	}
	if tc.SpanId == "" {
		t.Error("SpanId should be generated")
	}
}

// TestExtractWithRequestIDOnly 测试场景 4：无 X-B3-TraceId 但有 X-Request-Id 时
func TestExtractWithRequestIDOnly(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set(HeaderRequestID, "4bf92f3577b34da6a3ce929d0e0e4736")

	tc := Extract(req, "test-instance")

	// TraceID 应等于 X-Request-Id 的值
	if tc.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("TraceID should be X-Request-Id value, got %s", tc.TraceID)
	}

	// ParentSpanId 应为空
	if tc.ParentSpanId != "" {
		t.Errorf("ParentSpanId should be empty, got %s", tc.ParentSpanId)
	}
}

// TestExtractGeneratesNewSpanIdEachTime 测试场景 5：每次调用都生成新的 SpanId
func TestExtractGeneratesNewSpanIdEachTime(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set(HeaderTraceID, "4bf92f3577b34da6a3ce929d0e0e4736")
	req.Header.Set(HeaderSpanId, "00f067aa0ba902b7")

	tc1 := Extract(req, "test-instance")
	tc2 := Extract(req, "test-instance")

	// 两次提取的 SpanId 应不同
	if tc1.SpanId == tc2.SpanId {
		t.Error("Each Extract call should generate a new SpanId")
	}

	// SpanId 不应等于请求头中的 X-B3-SpanId
	if tc1.SpanId == "00f067aa0ba902b7" || tc2.SpanId == "00f067aa0ba902b7" {
		t.Error("SpanId should not be reused from header")
	}
}

// TestInjectHeaders 测试场景 6：Inject 验证写入的头字段
func TestInjectHeaders(t *testing.T) {
	tc := &TraceContext{
		TraceID:      "4bf92f3577b34da6a3ce929d0e0e4736",
		SpanId:       "00f067aa0ba902b7",
		ParentSpanId: "a3f2b1c4d5e6f7a8",
		Sampled:      true,
	}

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	Inject(req, tc)

	// 验证 X-B3-TraceId
	if req.Header.Get(HeaderTraceID) != tc.TraceID {
		t.Errorf("X-B3-TraceId should be %s, got %s", tc.TraceID, req.Header.Get(HeaderTraceID))
	}

	// 验证 X-B3-SpanId（是自己的 SpanId，不是 ParentSpanId）
	if req.Header.Get(HeaderSpanId) != tc.SpanId {
		t.Errorf("X-B3-SpanId should be %s, got %s", tc.SpanId, req.Header.Get(HeaderSpanId))
	}
	if req.Header.Get(HeaderSpanId) == tc.ParentSpanId {
		t.Error("X-B3-SpanId should not be ParentSpanId")
	}

	// 验证 X-B3-Sampled
	if req.Header.Get(HeaderSampled) != "1" {
		t.Errorf("X-B3-Sampled should be '1', got %s", req.Header.Get(HeaderSampled))
	}

	// 验证不写入 X-B3-ParentSpanId
	if req.Header.Get("X-B3-ParentSpanId") != "" {
		t.Error("X-B3-ParentSpanId should not be set")
	}
}

// TestInjectHeadersSampledFalse 测试 Sampled=false 时的注入
func TestInjectHeadersSampledFalse(t *testing.T) {
	tc := &TraceContext{
		TraceID: "4bf92f3577b34da6a3ce929d0e0e4736",
		SpanId:  "00f067aa0ba902b7",
		Sampled: false,
	}

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	Inject(req, tc)

	if req.Header.Get(HeaderSampled) != "0" {
		t.Errorf("X-B3-Sampled should be '0', got %s", req.Header.Get(HeaderSampled))
	}
}

// TestInjectResponseHeaders 测试场景 7：InjectResponse 验证响应头
func TestInjectResponseHeaders(t *testing.T) {
	tc := &TraceContext{
		TraceID: "4bf92f3577b34da6a3ce929d0e0e4736",
		SpanId:  "00f067aa0ba902b7",
	}

	recorder := httptest.NewRecorder()
	InjectResponse(recorder, tc)

	// 验证 X-B3-TraceId
	if recorder.Header().Get(HeaderTraceID) != tc.TraceID {
		t.Errorf("X-B3-TraceId should be %s, got %s", tc.TraceID, recorder.Header().Get(HeaderTraceID))
	}

	// 验证 X-Request-Id（兼容）
	if recorder.Header().Get(HeaderRequestID) != tc.TraceID {
		t.Errorf("X-Request-Id should be %s, got %s", tc.TraceID, recorder.Header().Get(HeaderRequestID))
	}
}

// TestFullPropagationChain 测试场景 8：完整透传链路（核心测试）
// 模拟 gateway → order → stock 三跳调用
func TestFullPropagationChain(t *testing.T) {
	// 模拟 gateway（入口节点）
	gatewayReq := httptest.NewRequest(http.MethodGet, "/gateway", nil)
	gatewayTC := Extract(gatewayReq, "gateway-pod")

	// gateway 应为 root span
	if !gatewayTC.IsRoot() {
		t.Error("gateway should be root span")
	}
	if gatewayTC.ParentSpanId != "" {
		t.Error("gateway ParentSpanId should be empty")
	}

	// 模拟 gateway 调用 order（准备出站请求）
	orderReq := httptest.NewRequest(http.MethodGet, "/order", nil)
	Inject(orderReq, gatewayTC)

	// 验证 gateway 出站请求头无 X-B3-ParentSpanId
	if orderReq.Header.Get("X-B3-ParentSpanId") != "" {
		t.Error("gateway outgoing request should not have X-B3-ParentSpanId")
	}

	// 模拟 order 服务接收请求
	orderTC := Extract(orderReq, "order-pod")

	// order 应继承 TraceID
	if orderTC.TraceID != gatewayTC.TraceID {
		t.Error("order should inherit TraceID from gateway")
	}

	// order 的 ParentSpanId 应等于 gateway 的 SpanId
	if orderTC.ParentSpanId != gatewayTC.SpanId {
		t.Errorf("order ParentSpanId should be gateway SpanId, got %s, want %s", orderTC.ParentSpanId, gatewayTC.SpanId)
	}

	// order 应生成新的 SpanId
	if orderTC.SpanId == gatewayTC.SpanId {
		t.Error("order should generate new SpanId")
	}

	// 模拟 order 调用 stock（准备出站请求）
	stockReq := httptest.NewRequest(http.MethodGet, "/stock", nil)
	Inject(stockReq, orderTC)

	// 验证 order 出站请求头无 X-B3-ParentSpanId
	if stockReq.Header.Get("X-B3-ParentSpanId") != "" {
		t.Error("order outgoing request should not have X-B3-ParentSpanId")
	}

	// 模拟 stock 服务接收请求
	stockTC := Extract(stockReq, "stock-pod")

	// stock 应继承 TraceID
	if stockTC.TraceID != gatewayTC.TraceID {
		t.Error("stock should inherit TraceID from gateway")
	}

	// stock 的 ParentSpanId 应等于 order 的 SpanId
	if stockTC.ParentSpanId != orderTC.SpanId {
		t.Errorf("stock ParentSpanId should be order SpanId, got %s, want %s", stockTC.ParentSpanId, orderTC.SpanId)
	}

	// stock 应生成新的 SpanId
	if stockTC.SpanId == orderTC.SpanId || stockTC.SpanId == gatewayTC.SpanId {
		t.Error("stock should generate new SpanId")
	}

	// 验证 TraceID 三跳完全相同
	if gatewayTC.TraceID != orderTC.TraceID || orderTC.TraceID != stockTC.TraceID {
		t.Error("TraceID should be same across all hops")
	}

	// 验证 SpanId 三跳互不相同
	if gatewayTC.SpanId == orderTC.SpanId || orderTC.SpanId == stockTC.SpanId || gatewayTC.SpanId == stockTC.SpanId {
		t.Error("SpanId should be unique for each hop")
	}

	t.Logf("Full chain:\n  gateway: TraceID=%s, SpanId=%s, ParentSpanId=%s\n  order:   TraceID=%s, SpanId=%s, ParentSpanId=%s\n  stock:   TraceID=%s, SpanId=%s, ParentSpanId=%s",
		gatewayTC.TraceID, gatewayTC.SpanId, gatewayTC.ParentSpanId,
		orderTC.TraceID, orderTC.SpanId, orderTC.ParentSpanId,
		stockTC.TraceID, stockTC.SpanId, stockTC.ParentSpanId)
}

// TestLogFields 测试场景 9：LogFields 返回值
func TestLogFields(t *testing.T) {
	// ParentSpanId 为空时
	tc1 := &TraceContext{
		TraceID:      "4bf92f3577b34da6a3ce929d0e0e4736",
		SpanId:       "00f067aa0ba902b7",
		ParentSpanId: "",
		InstanceId:   "test-pod",
	}
	fields1 := tc1.LogFields()

	if fields1["trace_id"] != tc1.TraceID {
		t.Error("trace_id should match")
	}
	if fields1["span_id"] != tc1.SpanId {
		t.Error("span_id should match")
	}
	if fields1["instance_id"] != tc1.InstanceId {
		t.Error("instance_id should match")
	}
	if _, exists := fields1["parent_span_id"]; exists {
		t.Error("parent_span_id should not exist when empty")
	}

	// ParentSpanId 不为空时
	tc2 := &TraceContext{
		TraceID:      "4bf92f3577b34da6a3ce929d0e0e4736",
		SpanId:       "00f067aa0ba902b7",
		ParentSpanId: "a3f2b1c4d5e6f7a8",
		InstanceId:   "test-pod",
	}
	fields2 := tc2.LogFields()

	if fields2["parent_span_id"] != tc2.ParentSpanId {
		t.Errorf("parent_span_id should be %s, got %s", tc2.ParentSpanId, fields2["parent_span_id"])
	}
}

// TestMustTraceFromContextEmpty 测试场景 10：MustTraceFromContext context 中无 TraceContext 时
func TestMustTraceFromContextEmpty(t *testing.T) {
	ctx := context.Background()
	tc := MustTraceFromContext(ctx)

	// 应返回空结构体，非 nil
	if tc == nil {
		t.Error("MustTraceFromContext should return non-nil TraceContext")
	}

	// 所有字段应为空
	if tc.TraceID != "" || tc.SpanId != "" || tc.ParentSpanId != "" || tc.InstanceId != "" {
		t.Error("Empty TraceContext should have all empty fields")
	}

	// 不应 panic
	_ = tc.LogFields()
	_ = tc.IsRoot()
}

// TestMustTraceFromContextWithValue 测试 MustTraceFromContext 有值时
func TestMustTraceFromContextWithValue(t *testing.T) {
	originalTC := &TraceContext{
		TraceID: "4bf92f3577b34da6a3ce929d0e0e4736",
		SpanId:  "00f067aa0ba902b7",
	}
	ctx := ContextWithTrace(context.Background(), originalTC)

	tc := MustTraceFromContext(ctx)
	if tc.TraceID != originalTC.TraceID {
		t.Error("Should return the original TraceContext")
	}
}

// TestTracedClientDo 测试场景 11：TracedClient.Do 自动注入追踪头
func TestTracedClientDo(t *testing.T) {
	// 创建测试服务器，验证接收到的请求头
	var receivedHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// 创建 TracedClient
	client := NewTracedClient(nil)

	// 创建带 TraceContext 的 context
	tc := &TraceContext{
		TraceID:      "4bf92f3577b34da6a3ce929d0e0e4736",
		SpanId:       "00f067aa0ba902b7",
		ParentSpanId: "a3f2b1c4d5e6f7a8",
		Sampled:      true,
	}
	ctx := ContextWithTrace(context.Background(), tc)

	// 发送请求
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	defer resp.Body.Close()

	// 验证下游收到的请求头
	if receivedHeaders.Get(HeaderTraceID) != tc.TraceID {
		t.Errorf("X-B3-TraceId should be %s, got %s", tc.TraceID, receivedHeaders.Get(HeaderTraceID))
	}

	// X-B3-SpanId 应是发送方的 SpanId
	if receivedHeaders.Get(HeaderSpanId) != tc.SpanId {
		t.Errorf("X-B3-SpanId should be %s, got %s", tc.SpanId, receivedHeaders.Get(HeaderSpanId))
	}

	// 应无 X-B3-ParentSpanId
	if receivedHeaders.Get("X-B3-ParentSpanId") != "" {
		t.Error("X-B3-ParentSpanId should not be set")
	}

	// 验证 Sampled
	if receivedHeaders.Get(HeaderSampled) != "1" {
		t.Errorf("X-B3-Sampled should be '1', got %s", receivedHeaders.Get(HeaderSampled))
	}
}

// TestTracedClientGet 测试 TracedClient.Get 方法
func TestTracedClientGet(t *testing.T) {
	var receivedHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewTracedClient(nil)
	tc := &TraceContext{
		TraceID: "4bf92f3577b34da6a3ce929d0e0e4736",
		SpanId:  "00f067aa0ba902b7",
		Sampled: true,
	}
	ctx := ContextWithTrace(context.Background(), tc)

	resp, err := client.Get(ctx, server.URL)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	defer resp.Body.Close()

	if receivedHeaders.Get(HeaderTraceID) != tc.TraceID {
		t.Error("TraceID not injected correctly")
	}
}

// TestTracedClientPost 测试 TracedClient.Post 方法
func TestTracedClientPost(t *testing.T) {
	var receivedHeaders http.Header
	var receivedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		body, _ := io.ReadAll(r.Body)
		receivedBody = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewTracedClient(nil)
	tc := &TraceContext{
		TraceID: "4bf92f3577b34da6a3ce929d0e0e4736",
		SpanId:  "00f067aa0ba902b7",
		Sampled: true,
	}
	ctx := ContextWithTrace(context.Background(), tc)

	resp, err := client.Post(ctx, server.URL, "application/json", strings.NewReader(`{"test":"data"}`))
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	defer resp.Body.Close()

	if receivedHeaders.Get(HeaderTraceID) != tc.TraceID {
		t.Error("TraceID not injected correctly")
	}
	if receivedHeaders.Get("Content-Type") != "application/json" {
		t.Error("Content-Type not set correctly")
	}
	if receivedBody != `{"test":"data"}` {
		t.Error("Body not sent correctly")
	}
}

// TestTracedClientWithoutContext 测试无 TraceContext 时正常发送
func TestTracedClientWithoutContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewTracedClient(nil)

	// 不带 TraceContext 的 context
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Request should succeed without TraceContext: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Error("Request without TraceContext should succeed")
	}
}

// TestGinMiddlewareIntegration 测试场景 12：Gin 中间件集成测试
func TestGinMiddlewareIntegration(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var handlerTC *TraceContext
	var handlerCtxTC *TraceContext

	r := gin.New()
	r.Use(TraceMiddleware("test-instance"))
	r.GET("/test", func(c *gin.Context) {
		// 从 gin.Context 取
		tc, ok := TraceFromGin(c)
		if ok {
			handlerTC = tc
		}

		// 从 c.Request.Context() 取
		tc2, ok := TraceFromContext(c.Request.Context())
		if ok {
			handlerCtxTC = tc2
		}

		c.String(http.StatusOK, "ok")
	})

	// 发送带追踪头的请求
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set(HeaderTraceID, "4bf92f3577b34da6a3ce929d0e0e4736")
	req.Header.Set(HeaderSpanId, "00f067aa0ba902b7")

	recorder := httptest.NewRecorder()
	r.ServeHTTP(recorder, req)

	// 验证响应状态
	if recorder.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", recorder.Code)
	}

	// 验证响应头含 X-B3-TraceId 和 X-Request-Id
	if recorder.Header().Get(HeaderTraceID) != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Error("Response should contain X-B3-TraceId")
	}
	if recorder.Header().Get(HeaderRequestID) != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Error("Response should contain X-Request-Id")
	}

	// 验证 Handler 内可通过 TraceFromGin 取到 TraceContext
	if handlerTC == nil {
		t.Error("Handler should be able to get TraceContext from gin.Context")
	}
	if handlerTC.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Error("TraceID should be inherited")
	}
	if handlerTC.ParentSpanId != "00f067aa0ba902b7" {
		t.Error("ParentSpanId should be the incoming X-B3-SpanId")
	}

	// 验证 Handler 内可通过 c.Request.Context() 取到 TraceContext
	if handlerCtxTC == nil {
		t.Error("Handler should be able to get TraceContext from request context")
	}

	// 验证两种方式取到的是同一个 TraceContext
	if handlerTC != handlerCtxTC {
		t.Error("Both methods should return the same TraceContext")
	}
}

// TestGinMiddlewareRootSpan 测试 Gin 中间件处理 root span
func TestGinMiddlewareRootSpan(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var handlerTC *TraceContext

	r := gin.New()
	r.Use(TraceMiddleware("test-instance"))
	r.GET("/test", func(c *gin.Context) {
		tc, _ := TraceFromGin(c)
		handlerTC = tc
		c.String(http.StatusOK, "ok")
	})

	// 发送不带追踪头的请求
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder := httptest.NewRecorder()
	r.ServeHTTP(recorder, req)

	// 验证 TraceContext
	if handlerTC == nil {
		t.Fatal("Handler should have TraceContext")
	}
	if handlerTC.TraceID == "" {
		t.Error("TraceID should be generated")
	}
	if handlerTC.SpanId == "" {
		t.Error("SpanId should be generated")
	}
	if !handlerTC.IsRoot() {
		t.Error("Should be root span")
	}

	// 验证响应头
	if recorder.Header().Get(HeaderTraceID) == "" {
		t.Error("Response should contain X-B3-TraceId")
	}
}

// TestExtractSampledHeader 测试 Sampled 头的解析
func TestExtractSampledHeader(t *testing.T) {
	// Sampled = "0"
	req1 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req1.Header.Set(HeaderSampled, "0")
	tc1 := Extract(req1, "test")
	if tc1.Sampled {
		t.Error("Sampled should be false when header is '0'")
	}

	// Sampled = "1"
	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req2.Header.Set(HeaderSampled, "1")
	tc2 := Extract(req2, "test")
	if !tc2.Sampled {
		t.Error("Sampled should be true when header is '1'")
	}

	// Sampled 不存在
	req3 := httptest.NewRequest(http.MethodGet, "/test", nil)
	tc3 := Extract(req3, "test")
	if !tc3.Sampled {
		t.Error("Sampled should be true by default")
	}
}

// TestContextIntegration 测试 context 集成
func TestContextIntegration(t *testing.T) {
	tc := &TraceContext{
		TraceID: "4bf92f3577b34da6a3ce929d0e0e4736",
		SpanId:  "00f067aa0ba902b7",
	}

	ctx := ContextWithTrace(context.Background(), tc)

	// 验证可以取出
	retrieved, ok := TraceFromContext(ctx)
	if !ok {
		t.Error("Should be able to retrieve TraceContext")
	}
	if retrieved != tc {
		t.Error("Retrieved TraceContext should be the same")
	}
}

// TestTraceFromContextNil 测试 nil context
func TestTraceFromContextNil(t *testing.T) {
	tc, ok := TraceFromContext(nil)
	if ok || tc != nil {
		t.Error("Should return nil, false for nil context")
	}
}

// TestContextWithTraceNil 测试 nil TraceContext
func TestContextWithTraceNil(t *testing.T) {
	ctx := context.Background()
	newCtx := ContextWithTrace(ctx, nil)

	// 应返回原始 context
	tc, ok := TraceFromContext(newCtx)
	if ok || tc != nil {
		t.Error("Should not store nil TraceContext")
	}
}

// TestTraceIDGeneration 测试 TraceID 生成格式
func TestTraceIDGeneration(t *testing.T) {
	traceID := NewTraceID()

	if len(traceID) != 32 {
		t.Errorf("TraceID should be 32 characters, got %d", len(traceID))
	}

	// 验证是有效的 hex 字符串
	if !isValidHexString(traceID, 32) {
		t.Error("TraceID should be valid hex string")
	}

	// 验证唯一性
	traceID2 := NewTraceID()
	if traceID == traceID2 {
		t.Error("TraceIDs should be unique")
	}
}

// TestSpanIdGeneration 测试 SpanId 生成格式
func TestSpanIdGeneration(t *testing.T) {
	spanId := NewSpanId()

	if len(spanId) != 16 {
		t.Errorf("SpanId should be 16 characters, got %d", len(spanId))
	}

	// 验证是有效的 hex 字符串
	if !isValidHexString(spanId, 16) {
		t.Error("SpanId should be valid hex string")
	}

	// 验证唯一性
	spanId2 := NewSpanId()
	if spanId == spanId2 {
		t.Error("SpanIds should be unique")
	}
}

// TestGetInstanceId 测试 GetInstanceId
func TestGetInstanceId(t *testing.T) {
	instanceId := GetInstanceId()

	// 应返回非空值（至少是 "unknown"）
	if instanceId == "" {
		t.Error("GetInstanceId should return non-empty string")
	}
}

// TestExtractInvalidHexTraceID 测试无效 hex 格式的 TraceID
func TestExtractInvalidHexTraceID(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set(HeaderTraceID, "invalid-trace-id")

	tc := Extract(req, "test")

	// 应生成新的 TraceID
	if tc.TraceID == "invalid-trace-id" {
		t.Error("Invalid TraceID should not be used")
	}
	if len(tc.TraceID) != 32 {
		t.Error("Should generate valid TraceID")
	}
}

// TestExtractInvalidHexSpanID 测试无效 hex 格式的 SpanId
func TestExtractInvalidHexSpanID(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set(HeaderTraceID, "4bf92f3577b34da6a3ce929d0e0e4736")
	req.Header.Set(HeaderSpanId, "invalid-span")

	tc := Extract(req, "test")

	// ParentSpanId 应为空（无效格式被忽略）
	if tc.ParentSpanId != "" {
		t.Error("Invalid SpanId should result in empty ParentSpanId")
	}
}

// TestMetadataFromContext_NoTrace 测试无 TraceContext 时返回 nil
func TestMetadataFromContext_NoTrace(t *testing.T) {
	ctx := context.Background()
	m := MetadataFromContext(ctx)
	if m != nil {
		t.Errorf("Expected nil when no trace in context, got %v", m)
	}
}

// TestMetadataFromContext_WithTrace 测试从 context 正确提取 metadata
func TestMetadataFromContext_WithTrace(t *testing.T) {
	tc := &TraceContext{
		TraceID: "4bf92f3577b34da6a3ce929d0e0e4736",
		SpanId:  "00f067aa0ba902b7",
		Sampled: true,
	}
	ctx := ContextWithTrace(context.Background(), tc)

	m := MetadataFromContext(ctx)
	if m == nil {
		t.Fatal("Expected non-nil metadata")
	}
	if m["trace_id"] != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("Expected trace_id '4bf92f3577b34da6a3ce929d0e0e4736', got %s", m["trace_id"])
	}
	if m["span_id"] != "00f067aa0ba902b7" {
		t.Errorf("Expected span_id '00f067aa0ba902b7', got %s", m["span_id"])
	}
	if m["sampled"] != "1" {
		t.Errorf("Expected sampled '1', got %s", m["sampled"])
	}
}

// TestMetadataFromContext_NotSampled 测试 Sampled=false 时返回 "0"
func TestMetadataFromContext_NotSampled(t *testing.T) {
	tc := &TraceContext{
		TraceID: "4bf92f3577b34da6a3ce929d0e0e4736",
		SpanId:  "00f067aa0ba902b7",
		Sampled: false,
	}
	ctx := ContextWithTrace(context.Background(), tc)

	m := MetadataFromContext(ctx)
	if m["sampled"] != "0" {
		t.Errorf("Expected sampled '0' when not sampled, got %s", m["sampled"])
	}
}

// TestMetadataFromTraceContext_Nil 测试 nil TraceContext 返回 nil
func TestMetadataFromTraceContext_Nil(t *testing.T) {
	m := MetadataFromTraceContext(nil)
	if m != nil {
		t.Errorf("Expected nil for nil TraceContext, got %v", m)
	}
}

// TestMetadataFromTraceContext_Valid 测试有效的 TraceContext 转换
func TestMetadataFromTraceContext_Valid(t *testing.T) {
	tc := &TraceContext{
		TraceID: "4bf92f3577b34da6a3ce929d0e0e4736",
		SpanId:  "00f067aa0ba902b7",
		Sampled: true,
	}
	m := MetadataFromTraceContext(tc)
	if m["trace_id"] != tc.TraceID {
		t.Errorf("trace_id mismatch: expected %s, got %s", tc.TraceID, m["trace_id"])
	}
	if m["span_id"] != tc.SpanId {
		t.Errorf("span_id mismatch: expected %s, got %s", tc.SpanId, m["span_id"])
	}
}

// TestTraceFromMetadata_EmptyMetadata 测试空 metadata 返回 nil
func TestTraceFromMetadata_EmptyMetadata(t *testing.T) {
	tc := TraceFromMetadata(map[string]string{}, "test-instance")
	if tc != nil {
		t.Errorf("Expected nil for empty metadata, got %v", tc)
	}
}

// TestTraceFromMetadata_NoTraceID 测试无 trace_id 返回 nil
func TestTraceFromMetadata_NoTraceID(t *testing.T) {
	m := map[string]string{"span_id": "00f067aa0ba902b7"}
	tc := TraceFromMetadata(m, "test-instance")
	if tc != nil {
		t.Errorf("Expected nil when no trace_id, got %v", tc)
	}
}

// TestTraceFromMetadata_InvalidTraceID 测试无效 trace_id 格式返回 nil
func TestTraceFromMetadata_InvalidTraceID(t *testing.T) {
	m := map[string]string{
		"trace_id": "invalid-trace-id",
		"span_id":  "00f067aa0ba902b7",
	}
	tc := TraceFromMetadata(m, "test-instance")
	if tc != nil {
		t.Errorf("Expected nil for invalid trace_id, got %v", tc)
	}
}

// TestTraceFromMetadata_InvalidSpanID 测试无效 span_id 被忽略
func TestTraceFromMetadata_InvalidSpanID(t *testing.T) {
	m := map[string]string{
		"trace_id": "4bf92f3577b34da6a3ce929d0e0e4736",
		"span_id":  "invalid-span",
	}
	tc := TraceFromMetadata(m, "test-instance")
	if tc == nil {
		t.Fatal("Expected non-nil TraceContext")
	}
	if tc.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("TraceID mismatch")
	}
	// Invalid span_id should result in empty ParentSpanId
	if tc.ParentSpanId != "" {
		t.Errorf("Expected empty ParentSpanId for invalid span_id, got %s", tc.ParentSpanId)
	}
}

// TestTraceFromMetadata_Valid 测试有效的 metadata 恢复
func TestTraceFromMetadata_Valid(t *testing.T) {
	m := map[string]string{
		"trace_id": "4bf92f3577b34da6a3ce929d0e0e4736",
		"span_id":  "00f067aa0ba902b7",
		"sampled":  "1",
	}
	tc := TraceFromMetadata(m, "test-instance")
	if tc == nil {
		t.Fatal("Expected non-nil TraceContext")
	}
	if tc.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("TraceID mismatch")
	}
	if tc.ParentSpanId != "00f067aa0ba902b7" {
		t.Errorf("ParentSpanId mismatch")
	}
	if tc.InstanceId != "test-instance" {
		t.Errorf("InstanceId mismatch")
	}
	if !tc.Sampled {
		t.Error("Sampled should be true")
	}
	// SpanId should be newly generated
	if tc.SpanId == "" {
		t.Error("SpanId should be generated")
	}
	if tc.SpanId == "00f067aa0ba902b7" {
		t.Error("SpanId should be newly generated, not copied from parent")
	}
}

// TestTraceFromMetadata_NotSampled 测试 sampled="0"
func TestTraceFromMetadata_NotSampled(t *testing.T) {
	m := map[string]string{
		"trace_id": "4bf92f3577b34da6a3ce929d0e0e4736",
		"sampled":  "0",
	}
	tc := TraceFromMetadata(m, "test-instance")
	if tc == nil {
		t.Fatal("Expected non-nil TraceContext")
	}
	if tc.Sampled {
		t.Error("Sampled should be false when '0'")
	}
}

// TestTraceFromMetadata_DefaultSampled 测试 sampled 字段缺失时默认为 true
func TestTraceFromMetadata_DefaultSampled(t *testing.T) {
	m := map[string]string{
		"trace_id": "4bf92f3577b34da6a3ce929d0e0e4736",
	}
	tc := TraceFromMetadata(m, "test-instance")
	if tc == nil {
		t.Fatal("Expected non-nil TraceContext")
	}
	if !tc.Sampled {
		t.Error("Sampled should default to true when missing")
	}
}
