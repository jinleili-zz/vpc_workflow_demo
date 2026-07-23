package operation

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestHTTPMiddlewareCapturesSagaIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	router.Use(HTTPMiddleware())
	router.POST("/work", func(c *gin.Context) {
		identity, ok := IdentityFromContext(c.Request.Context())
		if !ok {
			c.Status(http.StatusInternalServerError)
			return
		}
		c.JSON(http.StatusOK, identity)
	})

	req := httptest.NewRequest(http.MethodPost, "/work", nil)
	req.Header.Set(HeaderSagaTransactionID, "saga-123")
	req.Header.Set(HeaderIdempotencyKey, "stable-step-key")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	identity, ok := IdentityFromContext(req.Context())
	if ok {
		t.Fatalf("middleware mutated original request context: %#v", identity)
	}
	if got := recorder.Body.String(); got != `{"saga_transaction_id":"saga-123","idempotency_key":"stable-step-key"}` {
		t.Fatalf("body = %s", got)
	}
}

func TestHTTPMiddlewareRejectsInvalidExplicitResourceGeneration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, value := range []string{"not-a-number", "0", "-1"} {
		t.Run(value, func(t *testing.T) {
			router := gin.New()
			router.Use(HTTPMiddleware())
			router.POST("/work", func(c *gin.Context) { c.Status(http.StatusNoContent) })
			req := httptest.NewRequest(http.MethodPost, "/work", nil)
			req.Header.Set(HeaderResourceGeneration, value)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "INVALID_RESOURCE_GENERATION") {
				t.Fatalf("generation %q status/body=%d/%s", value, recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestHTTPMiddlewareLogsSagaIdentityWithoutRawKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(HTTPMiddleware())
	router.POST("/work", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	const sagaID = "saga-log-contract-123"
	const rawKey = "raw-key-must-never-appear-456"
	req := httptest.NewRequest(http.MethodPost, "/work", nil)
	req.Header.Set(HeaderSagaTransactionID, sagaID)
	req.Header.Set(HeaderIdempotencyKey, rawKey)
	recorder := httptest.NewRecorder()

	output := captureStdout(t, func() {
		router.ServeHTTP(recorder, req)
	})
	if !strings.Contains(output, sagaID) {
		t.Fatalf("log missing saga ID; output=%s", output)
	}
	if !strings.Contains(output, KeyDigest(rawKey)) {
		t.Fatalf("log missing idempotency key digest; output=%s", output)
	}
	if strings.Contains(output, rawKey) {
		t.Fatalf("log leaked raw idempotency key; output=%s", output)
	}
}

func TestKeyDigestDoesNotExposeRawKey(t *testing.T) {
	const key = "secret-like-client-key"
	digest := KeyDigest(key)
	if digest == "" || digest == key {
		t.Fatalf("digest = %q, must be non-empty and different from raw key", digest)
	}
	if digest != KeyDigest(key) {
		t.Fatal("same key produced different digests")
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	tmpFile, err := os.CreateTemp(t.TempDir(), "operation-log-*.json")
	if err != nil {
		t.Fatalf("create log capture: %v", err)
	}
	oldStdout, err := syscall.Dup(int(os.Stdout.Fd()))
	if err != nil {
		_ = tmpFile.Close()
		t.Fatalf("dup stdout: %v", err)
	}
	defer func() {
		_ = syscall.Dup2(oldStdout, int(os.Stdout.Fd()))
		_ = syscall.Close(oldStdout)
	}()
	if err := syscall.Dup2(int(tmpFile.Fd()), int(os.Stdout.Fd())); err != nil {
		_ = tmpFile.Close()
		t.Fatalf("redirect stdout: %v", err)
	}

	fn()
	if err := tmpFile.Close(); err != nil {
		t.Fatalf("close log capture: %v", err)
	}
	content, err := os.ReadFile(tmpFile.Name())
	if err != nil {
		t.Fatalf("read log capture: %v", err)
	}
	return string(content)
}
