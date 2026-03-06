package bootstrap

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/paic/nsp-common/pkg/auth"
	"github.com/paic/nsp-common/pkg/logger"
	"github.com/paic/nsp-common/pkg/trace"
)

// ========== TC-LOG: Logger 模块测试 ==========

func TestLoggerInit(t *testing.T) {
	cfg := DefaultConfig("test-service")
	cfg.EnableAuth = false
	cfg.EnableSaga = false

	components, err := Initialize(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}
	defer components.Shutdown()

	if components.Logger == nil {
		t.Fatal("Logger should not be nil after initialization")
	}
}

func TestLoggerLevels(t *testing.T) {
	levels := []string{"debug", "info", "warn", "error"}
	for _, level := range levels {
		t.Run(level, func(t *testing.T) {
			cfg := DefaultConfig("test-level-" + level)
			cfg.LogLevel = level
			cfg.EnableAuth = false
			cfg.EnableSaga = false

			components, err := Initialize(context.Background(), cfg)
			if err != nil {
				t.Fatalf("Initialize with level %s failed: %v", level, err)
			}
			defer components.Shutdown()

			// Verify logger can be used at this level without panic
			logger.Info("test message at level", "configured_level", level)
		})
	}
}

func TestLoggerContextIntegration(t *testing.T) {
	cfg := DefaultConfig("test-context-log")
	cfg.EnableAuth = false
	cfg.EnableSaga = false

	components, err := Initialize(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}
	defer components.Shutdown()

	// Test logging with trace context
	ctx := logger.ContextWithTraceID(context.Background(), "test-trace-id-12345")
	ctx = logger.ContextWithSpanID(ctx, "test-span-id-67890")

	// Should not panic
	logger.InfoContext(ctx, "test context logging",
		"key1", "value1",
		"key2", 42,
	)
	logger.DebugContext(ctx, "debug with context")
	logger.WarnContext(ctx, "warn with context")
}

func TestLoggerDefaultConfig(t *testing.T) {
	cfg := DefaultConfig("my-service")
	if cfg.ServiceName != "my-service" {
		t.Errorf("ServiceName = %q, want %q", cfg.ServiceName, "my-service")
	}
	if cfg.InstanceID == "" {
		t.Error("InstanceID should not be empty")
	}
	if cfg.LogLevel != "info" {
		t.Errorf("default LogLevel = %q, want %q", cfg.LogLevel, "info")
	}
}

// ========== TC-TRACE: Trace 模块测试 ==========

func TestTraceMiddleware(t *testing.T) {
	cfg := DefaultConfig("test-trace")
	cfg.EnableAuth = false
	cfg.EnableSaga = false

	components, err := Initialize(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}
	defer components.Shutdown()

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(trace.TraceMiddleware(cfg.InstanceID))
	r.GET("/test", func(c *gin.Context) {
		tc, ok := trace.TraceFromGin(c)
		if !ok {
			t.Error("TraceContext should be available in Gin context")
			c.JSON(500, gin.H{"error": "no trace"})
			return
		}
		c.JSON(200, gin.H{
			"trace_id": tc.TraceID,
			"span_id":  tc.SpanId,
		})
	})

	// Test without incoming trace headers (new trace generated)
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("Status = %d, want 200", w.Code)
	}

	// Response should contain trace headers
	traceID := w.Header().Get("X-B3-TraceId")
	if traceID == "" {
		t.Error("Response should contain X-B3-TraceId header")
	}
	requestID := w.Header().Get("X-Request-Id")
	if requestID == "" {
		t.Error("Response should contain X-Request-Id header (B3 compat)")
	}
	if traceID != requestID {
		t.Errorf("X-B3-TraceId (%s) and X-Request-Id (%s) should match", traceID, requestID)
	}
}

func TestTraceB3Propagation(t *testing.T) {
	cfg := DefaultConfig("test-b3")
	cfg.EnableAuth = false
	cfg.EnableSaga = false

	components, err := Initialize(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}
	defer components.Shutdown()

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(trace.TraceMiddleware(cfg.InstanceID))

	var capturedTraceID string
	r.GET("/test", func(c *gin.Context) {
		tc, ok := trace.TraceFromGin(c)
		if ok {
			capturedTraceID = tc.TraceID
		}
		c.JSON(200, gin.H{"ok": true})
	})

	// Send request with existing B3 trace headers
	incomingTraceID := "abcdef1234567890abcdef1234567890"
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-B3-TraceId", incomingTraceID)
	req.Header.Set("X-B3-SpanId", "1234567890abcdef")
	req.Header.Set("X-B3-Sampled", "1")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("Status = %d, want 200", w.Code)
	}
	if capturedTraceID != incomingTraceID {
		t.Errorf("TraceID = %q, want %q (should propagate incoming trace)", capturedTraceID, incomingTraceID)
	}
}

func TestTracedHTTPClient(t *testing.T) {
	cfg := DefaultConfig("test-client")
	cfg.EnableAuth = false
	cfg.EnableSaga = false

	components, err := Initialize(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}
	defer components.Shutdown()

	if components.TracedHTTP == nil {
		t.Fatal("TracedHTTP should not be nil")
	}

	// Create a test server to verify trace headers are injected
	var receivedTraceID string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedTraceID = r.Header.Get("X-B3-TraceId")
		w.WriteHeader(200)
	}))
	defer ts.Close()

	// Set up trace context
	tc := &trace.TraceContext{
		TraceID:    "test-trace-id-for-client",
		SpanId:     "test-span-id",
		InstanceId: "test-instance",
		Sampled:    true,
	}
	ctx := trace.ContextWithTrace(context.Background(), tc)

	resp, err := components.TracedHTTP.Get(ctx, ts.URL+"/test")
	if err != nil {
		t.Fatalf("TracedHTTP.Get failed: %v", err)
	}
	defer resp.Body.Close()

	if receivedTraceID != "test-trace-id-for-client" {
		t.Errorf("Server received TraceID = %q, want %q", receivedTraceID, "test-trace-id-for-client")
	}
}

// ========== TC-AUTH: Auth 模块测试 ==========

func TestAuthInit(t *testing.T) {
	cfg := DefaultConfig("test-auth")
	cfg.EnableAuth = true
	cfg.EnableSaga = false

	components, err := Initialize(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}
	defer components.Shutdown()

	if components.Verifier == nil {
		t.Error("Verifier should not be nil when auth is enabled")
	}
	if components.Signer == nil {
		t.Error("Signer should not be nil when auth is enabled")
	}
}

func TestAuthDisabled(t *testing.T) {
	cfg := DefaultConfig("test-no-auth")
	cfg.EnableAuth = false
	cfg.EnableSaga = false

	components, err := Initialize(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}
	defer components.Shutdown()

	if components.Verifier != nil {
		t.Error("Verifier should be nil when auth is disabled")
	}
}

func TestAKSKSignAndVerify(t *testing.T) {
	cfg := DefaultConfig("test-aksk")
	cfg.EnableAuth = true
	cfg.EnableSaga = false
	cfg.Credentials = []*auth.Credential{
		{AccessKey: "test-ak", SecretKey: "test-secret-key-12345", Label: "Test", Enabled: true},
	}

	components, err := Initialize(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}
	defer components.Shutdown()

	// Create a signer with the same credentials
	signer := auth.NewSigner("test-ak", "test-secret-key-12345")

	// Create a request and sign it
	req, _ := http.NewRequest("POST", "http://localhost/api/v1/vpc", nil)
	req.Header.Set("Content-Type", "application/json")

	if err := signer.Sign(req); err != nil {
		t.Fatalf("Signer.Sign failed: %v", err)
	}

	// Verify the signed request
	cred, err := components.Verifier.Verify(req)
	if err != nil {
		t.Fatalf("Verifier.Verify failed: %v", err)
	}
	if cred.AccessKey != "test-ak" {
		t.Errorf("Verified AK = %q, want %q", cred.AccessKey, "test-ak")
	}
}

func TestAuthMiddleware(t *testing.T) {
	cfg := DefaultConfig("test-auth-mw")
	cfg.EnableAuth = true
	cfg.EnableSaga = false
	cfg.SkipAuthPaths = []string{"/api/v1/health"}
	cfg.Credentials = []*auth.Credential{
		{AccessKey: "test-ak", SecretKey: "test-secret-key-12345", Label: "Test", Enabled: true},
	}

	components, err := Initialize(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}
	defer components.Shutdown()

	gin.SetMode(gin.TestMode)
	r := gin.New()
	components.SetupGinMiddlewares(r)

	r.GET("/api/v1/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})
	r.GET("/api/v1/vpc", func(c *gin.Context) {
		c.JSON(200, gin.H{"data": "vpc-list"})
	})

	// Test: skip auth path should succeed without auth
	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("Health endpoint (skip auth) returned %d, want 200", w.Code)
	}

	// Test: protected path without auth should fail
	req = httptest.NewRequest("GET", "/api/v1/vpc", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code == 200 {
		t.Error("Protected endpoint without auth should not return 200")
	}

	// Test: protected path with valid auth should succeed
	signer := auth.NewSigner("test-ak", "test-secret-key-12345")
	req = httptest.NewRequest("GET", "/api/v1/vpc", nil)
	req.Header.Set("Content-Type", "application/json")
	signer.Sign(req)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("Protected endpoint with valid auth returned %d, want 200", w.Code)
	}
}

// ========== TC-BOOTSTRAP: Bootstrap 集成测试 ==========

func TestFullBootstrap(t *testing.T) {
	cfg := DefaultConfig("test-full-bootstrap")
	cfg.EnableAuth = true
	cfg.EnableSaga = false // No PostgreSQL available in test env
	cfg.LogLevel = "debug"
	cfg.Development = true

	components, err := Initialize(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}
	defer components.Shutdown()

	// Verify all expected components
	if components.Logger == nil {
		t.Error("Logger should be initialized")
	}
	if components.Verifier == nil {
		t.Error("Verifier should be initialized")
	}
	if components.Signer == nil {
		t.Error("Signer should be initialized")
	}
	if components.TracedHTTP == nil {
		t.Error("TracedHTTP should be initialized")
	}
	// SagaEngine is nil because PostgresDSN is empty
	if components.SagaEngine != nil {
		t.Error("SagaEngine should be nil without PostgresDSN")
	}
}

func TestGinLoggerMiddleware(t *testing.T) {
	cfg := DefaultConfig("test-gin-logger")
	cfg.EnableAuth = false
	cfg.EnableSaga = false

	components, err := Initialize(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}
	defer components.Shutdown()

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(GinLoggerMiddleware())
	r.GET("/test", func(c *gin.Context) {
		c.JSON(200, gin.H{"ok": true})
	})

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	start := time.Now()
	r.ServeHTTP(w, req)
	elapsed := time.Since(start)

	if w.Code != 200 {
		t.Errorf("Status = %d, want 200", w.Code)
	}
	// Middleware should not add significant overhead
	if elapsed > 5*time.Second {
		t.Errorf("Request took too long: %v", elapsed)
	}
}

func TestSetupGinMiddlewares(t *testing.T) {
	cfg := DefaultConfig("test-setup-mw")
	cfg.EnableAuth = false
	cfg.EnableSaga = false

	components, err := Initialize(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}
	defer components.Shutdown()

	gin.SetMode(gin.TestMode)
	r := gin.New()

	// Should not panic
	components.SetupGinMiddlewares(r)

	r.GET("/test", func(c *gin.Context) {
		c.JSON(200, gin.H{"ok": true})
	})

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("Status = %d, want 200", w.Code)
	}
}

func TestShutdownIdempotent(t *testing.T) {
	cfg := DefaultConfig("test-shutdown")
	cfg.EnableAuth = false
	cfg.EnableSaga = false

	components, err := Initialize(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	// Should not panic on multiple calls
	components.Shutdown()
	components.Shutdown()
}
