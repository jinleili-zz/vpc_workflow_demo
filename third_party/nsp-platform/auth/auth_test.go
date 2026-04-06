// auth_test.go - Unit tests for AK/SK authentication module.
package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// Test credentials for testing.
var (
	testCred = &Credential{
		AccessKey: "test-ak-001",
		SecretKey: "test-sk-secret-key-12345",
		Label:     "test-service",
		Enabled:   true,
	}

	disabledCred = &Credential{
		AccessKey: "disabled-ak-001",
		SecretKey: "disabled-sk-secret-key",
		Label:     "disabled-service",
		Enabled:   false,
	}
)

// setupTestVerifier creates a Verifier with test credentials.
func setupTestVerifier() (*Verifier, *MemoryStore, *MemoryNonceStore) {
	store := NewMemoryStore([]*Credential{testCred, disabledCred})
	nonceStore := NewMemoryNonceStore()
	verifier := NewVerifier(store, nonceStore, nil)
	return verifier, store, nonceStore
}

// createTestRequest creates a signed test request.
func createTestRequest(method, path string, body []byte) (*http.Request, error) {
	var req *http.Request
	var err error

	if body != nil {
		req, err = http.NewRequest(method, path, bytes.NewReader(body))
	} else {
		req, err = http.NewRequest(method, path, nil)
	}
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

// TestSignAndVerifySuccess tests the normal signing and verification flow.
func TestSignAndVerifySuccess(t *testing.T) {
	verifier, _, nonceStore := setupTestVerifier()
	defer nonceStore.Stop()

	signer := NewSigner(testCred.AccessKey, testCred.SecretKey)

	// Create and sign request
	body := []byte(`{"key":"value"}`)
	req, err := createTestRequest("POST", "http://example.com/api/test?param=1", body)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	err = signer.Sign(req)
	if err != nil {
		t.Fatalf("Failed to sign request: %v", err)
	}

	// Verify
	cred, err := verifier.Verify(req)
	if err != nil {
		t.Fatalf("Verification failed: %v", err)
	}

	if cred.AccessKey != testCred.AccessKey {
		t.Errorf("Expected AccessKey %s, got %s", testCred.AccessKey, cred.AccessKey)
	}
	if cred.Label != testCred.Label {
		t.Errorf("Expected Label %s, got %s", testCred.Label, cred.Label)
	}
}

// TestSignatureTampering tests that body tampering is detected.
func TestSignatureTampering(t *testing.T) {
	verifier, _, nonceStore := setupTestVerifier()
	defer nonceStore.Stop()

	signer := NewSigner(testCred.AccessKey, testCred.SecretKey)

	// Create and sign request
	body := []byte(`{"key":"value"}`)
	req, err := createTestRequest("POST", "http://example.com/api/test", body)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	err = signer.Sign(req)
	if err != nil {
		t.Fatalf("Failed to sign request: %v", err)
	}

	// Tamper with the body
	tamperedBody := []byte(`{"key":"tampered"}`)
	req.Body = bytesReadCloser(tamperedBody)

	// Verify should fail
	_, err = verifier.Verify(req)
	if !errors.Is(err, ErrSignatureMismatch) {
		t.Errorf("Expected ErrSignatureMismatch, got: %v", err)
	}
}

// TestTimestampExpired tests that expired timestamps are rejected.
func TestTimestampExpired(t *testing.T) {
	verifier, _, nonceStore := setupTestVerifier()
	defer nonceStore.Stop()

	signer := NewSigner(testCred.AccessKey, testCred.SecretKey)

	// Create and sign request
	req, err := createTestRequest("GET", "http://example.com/api/test", nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	err = signer.Sign(req)
	if err != nil {
		t.Fatalf("Failed to sign request: %v", err)
	}

	// Set an expired timestamp (10 minutes ago)
	expiredTime := time.Now().Add(-10 * time.Minute).Unix()
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(expiredTime, 10))

	// Verify should fail
	_, err = verifier.Verify(req)
	if !errors.Is(err, ErrTimestampExpired) {
		t.Errorf("Expected ErrTimestampExpired, got: %v", err)
	}
}

// TestNonceReplay tests that nonce reuse is detected.
func TestNonceReplay(t *testing.T) {
	verifier, _, nonceStore := setupTestVerifier()
	defer nonceStore.Stop()

	signer := NewSigner(testCred.AccessKey, testCred.SecretKey)

	// Create and sign first request
	req1, err := createTestRequest("GET", "http://example.com/api/test", nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	err = signer.Sign(req1)
	if err != nil {
		t.Fatalf("Failed to sign request: %v", err)
	}

	// First verification should succeed
	_, err = verifier.Verify(req1)
	if err != nil {
		t.Fatalf("First verification failed: %v", err)
	}

	// Create second request with the same nonce
	req2, err := createTestRequest("GET", "http://example.com/api/test", nil)
	if err != nil {
		t.Fatalf("Failed to create second request: %v", err)
	}

	// Copy headers from first request (including nonce)
	req2.Header = req1.Header.Clone()

	// Second verification should fail with nonce reused
	_, err = verifier.Verify(req2)
	if !errors.Is(err, ErrNonceReused) {
		t.Errorf("Expected ErrNonceReused, got: %v", err)
	}
}

// TestAKNotFound tests that unknown AK is rejected.
func TestAKNotFound(t *testing.T) {
	verifier, _, nonceStore := setupTestVerifier()
	defer nonceStore.Stop()

	// Use unknown AK
	signer := NewSigner("unknown-ak", "some-sk")

	req, err := createTestRequest("GET", "http://example.com/api/test", nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	err = signer.Sign(req)
	if err != nil {
		t.Fatalf("Failed to sign request: %v", err)
	}

	// Verify should fail
	_, err = verifier.Verify(req)
	if !errors.Is(err, ErrAKNotFound) {
		t.Errorf("Expected ErrAKNotFound, got: %v", err)
	}
}

// TestAKDisabled tests that disabled AK is rejected.
func TestAKDisabled(t *testing.T) {
	verifier, _, nonceStore := setupTestVerifier()
	defer nonceStore.Stop()

	// Use disabled credential
	signer := NewSigner(disabledCred.AccessKey, disabledCred.SecretKey)

	req, err := createTestRequest("GET", "http://example.com/api/test", nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	err = signer.Sign(req)
	if err != nil {
		t.Fatalf("Failed to sign request: %v", err)
	}

	// Verify should fail
	_, err = verifier.Verify(req)
	if !errors.Is(err, ErrAKNotFound) {
		t.Errorf("Expected ErrAKNotFound, got: %v", err)
	}
}

// TestMissingAuthHeader tests that missing Authorization header is rejected.
func TestMissingAuthHeader(t *testing.T) {
	verifier, _, nonceStore := setupTestVerifier()
	defer nonceStore.Stop()

	// Create request without signing
	req, err := createTestRequest("GET", "http://example.com/api/test", nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	// Verify should fail
	_, err = verifier.Verify(req)
	if !errors.Is(err, ErrMissingAuthHeader) {
		t.Errorf("Expected ErrMissingAuthHeader, got: %v", err)
	}
}

// TestGinMiddlewareIntegration tests the Gin middleware with httptest.
func TestGinMiddlewareIntegration(t *testing.T) {
	gin.SetMode(gin.TestMode)

	verifier, _, nonceStore := setupTestVerifier()
	defer nonceStore.Stop()

	// Create Gin router with middleware
	router := gin.New()
	router.Use(AKSKAuthMiddleware(verifier, nil))

	// Add test endpoint that returns credential info
	router.POST("/api/test", func(c *gin.Context) {
		cred, exists := CredentialFromGin(c)
		if !exists {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "credential not found"})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"access_key": cred.AccessKey,
			"label":      cred.Label,
		})
	})

	// Test valid request
	t.Run("ValidRequest", func(t *testing.T) {
		signer := NewSigner(testCred.AccessKey, testCred.SecretKey)

		body := []byte(`{"test":"data"}`)
		req, err := http.NewRequest("POST", "/api/test", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")

		err = signer.Sign(req)
		if err != nil {
			t.Fatalf("Failed to sign request: %v", err)
		}

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var resp map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("Failed to parse response: %v", err)
		}

		if resp["access_key"] != testCred.AccessKey {
			t.Errorf("Expected access_key %s, got %s", testCred.AccessKey, resp["access_key"])
		}
	})

	// Test invalid request (no auth header)
	t.Run("InvalidRequest", func(t *testing.T) {
		req, err := http.NewRequest("POST", "/api/test", bytes.NewReader([]byte(`{}`)))
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("Expected status 400, got %d: %s", w.Code, w.Body.String())
		}
	})
}

// TestSkipperBypass tests that Skipper allows bypassing authentication.
func TestSkipperBypass(t *testing.T) {
	gin.SetMode(gin.TestMode)

	verifier, _, nonceStore := setupTestVerifier()
	defer nonceStore.Stop()

	// Create middleware with Skipper for /health path
	opt := &MiddlewareOption{
		Skipper: func(c *gin.Context) bool {
			return c.Request.URL.Path == "/health"
		},
	}

	router := gin.New()
	router.Use(AKSKAuthMiddleware(verifier, opt))

	router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	router.GET("/api/protected", func(c *gin.Context) {
		cred, exists := CredentialFromGin(c)
		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"access_key": cred.AccessKey})
	})

	// Test skipped path (no auth needed)
	t.Run("SkippedPath", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "/health", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200 for skipped path, got %d", w.Code)
		}
	})

	// Test protected path without auth
	t.Run("ProtectedPathNoAuth", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "/api/protected", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("Expected status 400 for protected path without auth, got %d", w.Code)
		}
	})

	// Test protected path with auth
	t.Run("ProtectedPathWithAuth", func(t *testing.T) {
		signer := NewSigner(testCred.AccessKey, testCred.SecretKey)
		req, _ := http.NewRequest("GET", "/api/protected", nil)
		req.Header.Set("Content-Type", "application/json")

		err := signer.Sign(req)
		if err != nil {
			t.Fatalf("Failed to sign request: %v", err)
		}

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200 for authenticated request, got %d: %s", w.Code, w.Body.String())
		}
	})
}

// TestContextCredentialStorage tests storing and retrieving credentials from context.
func TestContextCredentialStorage(t *testing.T) {
	cred := &Credential{
		AccessKey: "ctx-test-ak",
		SecretKey: "ctx-test-sk",
		Label:     "context-test",
		Enabled:   true,
	}

	// Store in context
	ctx := context.Background()
	ctx = ContextWithCredential(ctx, cred)

	// Retrieve from context
	retrieved, ok := CredentialFromContext(ctx)
	if !ok {
		t.Fatal("Failed to retrieve credential from context")
	}

	if retrieved.AccessKey != cred.AccessKey {
		t.Errorf("Expected AccessKey %s, got %s", cred.AccessKey, retrieved.AccessKey)
	}
}

// TestMemoryStoreOperations tests MemoryStore operations.
func TestMemoryStoreOperations(t *testing.T) {
	store := NewMemoryStore(nil)

	// Test GetByAK for non-existent key
	cred, err := store.GetByAK(context.Background(), "non-existent")
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
	if cred != nil {
		t.Error("Expected nil credential for non-existent key")
	}

	// Add a credential
	newCred := &Credential{
		AccessKey: "new-ak",
		SecretKey: "new-sk",
		Label:     "new-service",
		Enabled:   true,
	}
	store.Add(newCred)

	// Test GetByAK for existing key
	cred, err = store.GetByAK(context.Background(), "new-ak")
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
	if cred == nil {
		t.Fatal("Expected credential, got nil")
	}
	if cred.Label != "new-service" {
		t.Errorf("Expected label 'new-service', got '%s'", cred.Label)
	}
}

// TestMemoryNonceStoreOperations tests MemoryNonceStore operations.
func TestMemoryNonceStoreOperations(t *testing.T) {
	store := NewMemoryNonceStore()
	defer store.Stop()

	ctx := context.Background()
	ttl := 1 * time.Minute

	// First use of nonce
	used, err := store.CheckAndStore(ctx, "test-nonce-1", ttl)
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
	if used {
		t.Error("Expected nonce to be not used")
	}

	// Second use of same nonce
	used, err = store.CheckAndStore(ctx, "test-nonce-1", ttl)
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
	if !used {
		t.Error("Expected nonce to be marked as used")
	}

	// Different nonce
	used, err = store.CheckAndStore(ctx, "test-nonce-2", ttl)
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
	if used {
		t.Error("Expected different nonce to be not used")
	}
}

// TestErrorToHTTPStatus tests error to HTTP status code mapping.
func TestErrorToHTTPStatus(t *testing.T) {
	tests := []struct {
		err            error
		expectedStatus int
	}{
		{ErrMissingAuthHeader, http.StatusBadRequest},
		{ErrInvalidAuthFormat, http.StatusBadRequest},
		{ErrMissingTimestamp, http.StatusBadRequest},
		{ErrMissingNonce, http.StatusBadRequest},
		{ErrTimestampExpired, http.StatusUnauthorized},
		{ErrNonceReused, http.StatusUnauthorized},
		{ErrAKNotFound, http.StatusUnauthorized},
		{ErrSignatureMismatch, http.StatusUnauthorized},
		{errors.New("unknown error"), http.StatusInternalServerError},
	}

	for _, tt := range tests {
		status := ErrorToHTTPStatus(tt.err)
		if status != tt.expectedStatus {
			t.Errorf("ErrorToHTTPStatus(%v) = %d, expected %d", tt.err, status, tt.expectedStatus)
		}
	}
}

// TestQueryStringSorting tests that query parameters are correctly sorted.
func TestQueryStringSorting(t *testing.T) {
	verifier, _, nonceStore := setupTestVerifier()
	defer nonceStore.Stop()

	signer := NewSigner(testCred.AccessKey, testCred.SecretKey)

	// Create request with multiple query parameters in unsorted order
	req, err := createTestRequest("GET", "http://example.com/api/test?z=3&a=1&m=2", nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	err = signer.Sign(req)
	if err != nil {
		t.Fatalf("Failed to sign request: %v", err)
	}

	// Verify should succeed
	_, err = verifier.Verify(req)
	if err != nil {
		t.Fatalf("Verification failed: %v", err)
	}
}

// TestEmptyBody tests signing and verification with empty body.
func TestEmptyBody(t *testing.T) {
	verifier, _, nonceStore := setupTestVerifier()
	defer nonceStore.Stop()

	signer := NewSigner(testCred.AccessKey, testCred.SecretKey)

	req, err := createTestRequest("GET", "http://example.com/api/test", nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	err = signer.Sign(req)
	if err != nil {
		t.Fatalf("Failed to sign request: %v", err)
	}

	_, err = verifier.Verify(req)
	if err != nil {
		t.Fatalf("Verification failed for empty body: %v", err)
	}
}

// Helper function to create a ReadCloser from bytes.
func bytesReadCloser(b []byte) *bytesReaderCloser {
	return &bytesReaderCloser{bytes.NewReader(b)}
}

type bytesReaderCloser struct {
	*bytes.Reader
}

func (b *bytesReaderCloser) Close() error {
	return nil
}
