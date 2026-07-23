package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"workflow_qoder/internal/config"
	"workflow_qoder/internal/operation"

	"github.com/gin-gonic/gin"
	"github.com/jinleili-zz/nsp-platform/auth"
	"github.com/jinleili-zz/nsp-platform/trace"
)

func TestServerAuthMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.NSPConfig{
		Region: "cn-test-1",
		AZ:     "cn-test-1a",
		TLS: config.TLSConfig{
			Enabled:    true,
			Mode:       "process",
			ClientAuth: true,
		},
		Auth: config.AuthConfig{
			EnableAuth:    true,
			SkipAuthPaths: []string{"/api/v1/health"},
		},
	}

	verifier := auth.NewVerifier(
		auth.NewMemoryStore([]*auth.Credential{
			{AccessKey: "top-nsp", SecretKey: "test-secret-key-12345", Enabled: true},
		}),
		auth.NewMemoryNonceStore(),
		nil,
	)

	server := NewServer(cfg, nil, nil, trace.NewTracedClient(nil), nil, verifier)
	server.router.GET("/api/v1/protected", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("health status = %d, want %d", w.Code, http.StatusOK)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/protected", nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned protected status = %d, want %d", w.Code, http.StatusUnauthorized)
	}

	signer := auth.NewSigner("top-nsp", "test-secret-key-12345")
	req = httptest.NewRequest(http.MethodGet, "/api/v1/protected", nil)
	if err := signer.Sign(req); err != nil {
		t.Fatalf("sign request: %v", err)
	}
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("signed protected status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestServerCapturesSagaIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.NSPConfig{Region: "cn-test-1", AZ: "cn-test-1a"}
	server := NewServer(cfg, nil, nil, trace.NewTracedClient(nil), nil, nil)
	server.router.POST("/test/saga-identity", func(c *gin.Context) {
		identity, ok := operation.IdentityFromContext(c.Request.Context())
		if !ok {
			c.Status(http.StatusInternalServerError)
			return
		}
		c.JSON(http.StatusOK, identity)
	})

	req := httptest.NewRequest(http.MethodPost, "/test/saga-identity", nil)
	req.Header.Set(operation.HeaderSagaTransactionID, "saga-vfw-1")
	req.Header.Set(operation.HeaderIdempotencyKey, "step-vfw-1")
	recorder := httptest.NewRecorder()
	server.router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if got := recorder.Body.String(); got != `{"saga_transaction_id":"saga-vfw-1","idempotency_key":"step-vfw-1"}` {
		t.Fatalf("captured identity = %s", got)
	}
}
