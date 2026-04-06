// Package logger provides tests for multi-category logging.
package logger

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestMultiCategoryInit tests that InitMultiCategory initializes all loggers correctly.
func TestMultiCategoryInit(t *testing.T) {
	resetGlobalLogger()
	resetCategoryManager()

	cfg := DefaultMultiCategoryConfig("multi-cat-test")
	if err := InitMultiCategory(cfg); err != nil {
		t.Fatalf("InitMultiCategory failed: %v", err)
	}
	defer SyncAll()

	// Verify all category loggers are accessible
	if Access() == nil {
		t.Error("Access() returned nil")
	}
	if Platform() == nil {
		t.Error("Platform() returned nil")
	}
	if Business() == nil {
		t.Error("Business() returned nil")
	}

	// Global logger should also work (defaults to business)
	if GetLogger() == nil {
		t.Error("GetLogger() returned nil after InitMultiCategory")
	}
}

// TestMultiCategoryConfigValidation tests configuration validation.
func TestMultiCategoryConfigValidation(t *testing.T) {
	resetGlobalLogger()
	resetCategoryManager()

	tests := []struct {
		name    string
		config  *MultiCategoryConfig
		wantErr bool
	}{
		{
			name:    "nil config",
			config:  nil,
			wantErr: true,
		},
		{
			name:    "empty service name",
			config:  &MultiCategoryConfig{},
			wantErr: true,
		},
		{
			name: "valid minimal config",
			config: &MultiCategoryConfig{
				ServiceName: "test-service",
			},
			wantErr: false,
		},
		{
			name:    "valid full config",
			config:  DefaultMultiCategoryConfig("test-service"),
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetGlobalLogger()
			resetCategoryManager()
			
			err := InitMultiCategory(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("InitMultiCategory() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestCategoryLogging tests that each category logs independently.
func TestCategoryLogging(t *testing.T) {
	resetGlobalLogger()
	resetCategoryManager()

	cfg := &MultiCategoryConfig{
		ServiceName: "cat-log-test",
		Access: &CategoryConfig{
			Level:  LevelInfo,
			Format: FormatJSON,
			Outputs: []OutputConfig{
				{Type: OutputTypeStdout, Format: FormatJSON},
			},
		},
		Platform: &CategoryConfig{
			Level:  LevelInfo,
			Format: FormatJSON,
			Outputs: []OutputConfig{
				{Type: OutputTypeStdout, Format: FormatJSON},
			},
		},
		Business: &CategoryConfig{
			Level:  LevelInfo,
			Format: FormatJSON,
			Outputs: []OutputConfig{
				{Type: OutputTypeStdout, Format: FormatJSON},
			},
		},
	}

	if err := InitMultiCategory(cfg); err != nil {
		t.Fatalf("InitMultiCategory failed: %v", err)
	}
	defer SyncAll()

	// Test Access logging
	output := captureOutput(t, func() {
		Access().Info("access test message", "path", "/api/test")
		SyncAll()
	})

	if !strings.Contains(output, "access test message") {
		t.Error("expected access log output")
	}

	// Test Platform logging
	output = captureOutput(t, func() {
		Platform().Info("platform test message", "component", "asynq")
		SyncAll()
	})

	if !strings.Contains(output, "platform test message") {
		t.Error("expected platform log output")
	}

	// Test Business logging
	output = captureOutput(t, func() {
		Biz().Info("business test message", "module", "order-service")
		SyncAll()
	})

	if !strings.Contains(output, "business test message") {
		t.Error("expected business log output")
	}
}

// TestCategoryContextLogging tests context-aware logging for each category.
func TestCategoryContextLogging(t *testing.T) {
	resetGlobalLogger()
	resetCategoryManager()

	cfg := DefaultMultiCategoryConfig("cat-ctx-test")
	if err := InitMultiCategory(cfg); err != nil {
		t.Fatalf("InitMultiCategory failed: %v", err)
	}
	defer SyncAll()

	ctx := context.Background()
	ctx = ContextWithTraceID(ctx, "trace-cat-123")
	ctx = ContextWithSpanID(ctx, "span-cat-456")

	// Test Access context logging
	output := captureOutput(t, func() {
		Access().InfoContext(ctx, "access with context")
		SyncAll()
	})

	if !strings.Contains(output, "trace-cat-123") {
		t.Error("expected access log to contain trace_id")
	}

	// Test Platform context logging
	output = captureOutput(t, func() {
		Platform().InfoContext(ctx, "platform with context")
		SyncAll()
	})

	if !strings.Contains(output, "trace-cat-123") {
		t.Error("expected platform log to contain trace_id")
	}

	// Test Business context logging
	output = captureOutput(t, func() {
		Biz().InfoContext(ctx, "business with context")
		SyncAll()
	})

	if !strings.Contains(output, "trace-cat-123") {
		t.Error("expected business log to contain trace_id")
	}
}

// TestForCategory tests the ForCategory function.
func TestForCategory(t *testing.T) {
	resetGlobalLogger()
	resetCategoryManager()

	cfg := DefaultMultiCategoryConfig("for-cat-test")
	if err := InitMultiCategory(cfg); err != nil {
		t.Fatalf("InitMultiCategory failed: %v", err)
	}
	defer SyncAll()

	categories := []LogCategory{CategoryAccess, CategoryPlatform, CategoryBusiness}
	for _, cat := range categories {
		l := ForCategory(cat)
		if l == nil {
			t.Errorf("ForCategory(%s) returned nil", cat)
		}
	}

	// Test unknown category returns default logger
	l := ForCategory(LogCategory("unknown"))
	if l == nil {
		t.Error("ForCategory(unknown) returned nil, expected default logger")
	}
}

// TestFallbackToGlobalLogger tests that category functions fall back to global logger
// when multi-category is not initialized.
func TestFallbackToGlobalLogger(t *testing.T) {
	resetGlobalLogger()
	resetCategoryManager()

	// Initialize only global logger, not multi-category
	cfg := DefaultConfig("fallback-test")
	if err := Init(cfg); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer Sync()

	// Category functions should return global logger
	if Access() != GetLogger() {
		t.Error("Access() should return global logger when multi-category not initialized")
	}
	if Platform() != GetLogger() {
		t.Error("Platform() should return global logger when multi-category not initialized")
	}
	if Business() != GetLogger() {
		t.Error("Business() should return global logger when multi-category not initialized")
	}

	// Should still be able to log without errors
	output := captureOutput(t, func() {
		Access().Info("fallback access log")
		Platform().Info("fallback platform log")
		Biz().Info("fallback business log")
		Sync()
	})

	if output == "" {
		t.Error("expected log output from fallback logging")
	}
}

// TestFileMultiCategoryConfig tests logging to separate files per category.
func TestFileMultiCategoryConfig(t *testing.T) {
	resetGlobalLogger()
	resetCategoryManager()

	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "logger_cat_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := FileMultiCategoryConfig("file-cat-test", tmpDir)
	if err := InitMultiCategory(cfg); err != nil {
		t.Fatalf("InitMultiCategory failed: %v", err)
	}

	// Log to each category
	Access().Info("access file test", "path", "/test")
	Platform().Info("platform file test", "component", "test")
	Biz().Info("business file test", "module", "test")
	SyncAll()

	// Verify access log file
	accessContent, err := os.ReadFile(tmpDir + "/access.log")
	if err != nil {
		t.Fatalf("failed to read access log: %v", err)
	}
	if !strings.Contains(string(accessContent), "access file test") {
		t.Error("expected access log file to contain access message")
	}

	// Verify platform log file
	platformContent, err := os.ReadFile(tmpDir + "/platform.log")
	if err != nil {
		t.Fatalf("failed to read platform log: %v", err)
	}
	if !strings.Contains(string(platformContent), "platform file test") {
		t.Error("expected platform log file to contain platform message")
	}

	// Verify business log file
	businessContent, err := os.ReadFile(tmpDir + "/app.log")
	if err != nil {
		t.Fatalf("failed to read business log: %v", err)
	}
	if !strings.Contains(string(businessContent), "business file test") {
		t.Error("expected business log file to contain business message")
	}
}

// TestDevelopmentMultiCategoryConfig tests development configuration.
func TestDevelopmentMultiCategoryConfig(t *testing.T) {
	resetGlobalLogger()
	resetCategoryManager()

	cfg := DevelopmentMultiCategoryConfig("dev-cat-test")
	if err := InitMultiCategory(cfg); err != nil {
		t.Fatalf("InitMultiCategory failed: %v", err)
	}
	defer SyncAll()

	// In development mode, debug logs should be visible
	output := captureOutput(t, func() {
		Access().Debug("debug access")
		Platform().Debug("debug platform")
		Business().Debug("debug business")
		SyncAll()
	})

	if !strings.Contains(output, "debug access") {
		t.Error("expected debug access log in development mode")
	}
	if !strings.Contains(output, "debug platform") {
		t.Error("expected debug platform log in development mode")
	}
	if !strings.Contains(output, "debug business") {
		t.Error("expected debug business log in development mode")
	}
}

// TestLogAccess tests the LogAccess convenience function.
func TestLogAccess(t *testing.T) {
	resetGlobalLogger()
	resetCategoryManager()

	cfg := DefaultMultiCategoryConfig("log-access-test")
	if err := InitMultiCategory(cfg); err != nil {
		t.Fatalf("InitMultiCategory failed: %v", err)
	}
	defer SyncAll()

	ctx := context.Background()
	ctx = ContextWithTraceID(ctx, "trace-access-123")

	output := captureOutput(t, func() {
		LogAccess(ctx, &AccessLogEntry{
			Method:    "GET",
			Path:      "/api/v1/users",
			ClientIP:  "192.168.1.100",
			Status:    200,
			LatencyMS: 45,
			TraceID:   TraceIDFromContext(ctx),
		})
		SyncAll()
	})

	// Parse JSON output
	var logEntry map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &logEntry); err != nil {
		t.Fatalf("failed to parse log output as JSON: %v\nOutput: %s", err, output)
	}

	// Verify fields
	if logEntry["http_method"] != "GET" {
		t.Errorf("expected http_method 'GET', got: %v", logEntry["http_method"])
	}
	if logEntry["http_path"] != "/api/v1/users" {
		t.Errorf("expected http_path '/api/v1/users', got: %v", logEntry["http_path"])
	}
	if logEntry["http_status"].(float64) != 200 {
		t.Errorf("expected http_status 200, got: %v", logEntry["http_status"])
	}
	if logEntry["trace_id"] != "trace-access-123" {
		t.Errorf("expected trace_id 'trace-access-123', got: %v", logEntry["trace_id"])
	}
}

// TestLogAccessLevels tests that LogAccess uses appropriate log levels based on status.
func TestLogAccessLevels(t *testing.T) {
	resetGlobalLogger()
	resetCategoryManager()

	cfg := DefaultMultiCategoryConfig("access-level-test")
	if err := InitMultiCategory(cfg); err != nil {
		t.Fatalf("InitMultiCategory failed: %v", err)
	}
	defer SyncAll()

	tests := []struct {
		name          string
		status        int
		expectedLevel string
	}{
		{"success", 200, "info"},
		{"redirect", 302, "info"},
		{"client error", 400, "warn"},
		{"not found", 404, "warn"},
		{"server error", 500, "error"},
		{"bad gateway", 502, "error"},
	}

	ctx := context.Background()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := captureOutput(t, func() {
				LogAccess(ctx, &AccessLogEntry{
					Method:   "GET",
					Path:     "/test",
					ClientIP: "127.0.0.1",
					Status:   tt.status,
				})
				SyncAll()
			})

			if !strings.Contains(output, tt.expectedLevel) {
				t.Errorf("expected level '%s' for status %d, got output: %s", tt.expectedLevel, tt.status, output)
			}
		})
	}
}

// TestAccessLogConfigSkipPaths tests path skipping in access log config.
func TestAccessLogConfigSkipPaths(t *testing.T) {
	cfg := DefaultAccessLogConfig()

	tests := []struct {
		path     string
		expected bool
	}{
		{"/health", true},
		{"/healthz", true},
		{"/metrics", true},
		{"/api/v1/users", false},
		{"/orders", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			result := cfg.shouldSkipPath(tt.path)
			if result != tt.expected {
				t.Errorf("shouldSkipPath(%s) = %v, want %v", tt.path, result, tt.expected)
			}
		})
	}
}
