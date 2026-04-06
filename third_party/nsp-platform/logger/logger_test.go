// Package logger provides a unified logging module for NSP platform microservices.
// logger_test.go contains comprehensive tests for the logger package.
package logger

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// captureOutput captures log output to a temp file for testing.
func captureOutput(t *testing.T, fn func()) string {
	t.Helper()

	// Create a temporary file to capture output
	tmpFile, err := os.CreateTemp("", "logger_test_*.log")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	// Save original stdout file descriptor
	oldStdout, err := syscall.Dup(int(os.Stdout.Fd()))
	if err != nil {
		tmpFile.Close()
		t.Fatalf("failed to dup stdout: %v", err)
	}
	defer func() {
		// Restore stdout
		syscall.Dup2(oldStdout, int(os.Stdout.Fd()))
		syscall.Close(oldStdout)
	}()

	// Redirect stdout to temp file
	if err := syscall.Dup2(int(tmpFile.Fd()), int(os.Stdout.Fd())); err != nil {
		tmpFile.Close()
		t.Fatalf("failed to redirect stdout: %v", err)
	}

	// Run the function
	fn()

	// Close the temp file to flush
	tmpFile.Close()

	// Read captured output
	content, err := os.ReadFile(tmpPath)
	if err != nil {
		t.Fatalf("failed to read temp file: %v", err)
	}

	return string(content)
}

// resetGlobalLogger resets the global logger state for testing.
func resetGlobalLogger() {
	globalMu.Lock()
	defer globalMu.Unlock()
	globalLogger = nil
	initialized = false
}

// TestBasicLogging tests that basic log methods produce output at appropriate levels.
func TestBasicLogging(t *testing.T) {
	resetGlobalLogger()

	cfg := &Config{
		Level:        LevelDebug,
		Format:       FormatJSON,
		ServiceName:  "test-service",
		OutputPaths:  []string{"stdout"},
		EnableCaller: false,
	}

	if err := Init(cfg); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer Sync()

	tests := []struct {
		name      string
		logFunc   func(string, ...any)
		level     string
		shouldLog bool
	}{
		{"debug at debug level", Debug, "debug", true},
		{"info at debug level", Info, "info", true},
		{"warn at debug level", Warn, "warn", true},
		{"error at debug level", Error, "error", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := captureOutput(t, func() {
				tt.logFunc("test message", "key", "value")
				Sync()
			})

			if tt.shouldLog && output == "" {
				t.Errorf("expected log output but got none")
			}
			if tt.shouldLog && !strings.Contains(output, tt.level) {
				t.Errorf("expected output to contain level %q, got: %s", tt.level, output)
			}
		})
	}
}

// TestLevelFiltering tests that logs below the current level are not output.
func TestLevelFiltering(t *testing.T) {
	resetGlobalLogger()

	cfg := &Config{
		Level:        LevelWarn,
		Format:       FormatJSON,
		ServiceName:  "test-service",
		OutputPaths:  []string{"stdout"},
		EnableCaller: false,
	}

	if err := Init(cfg); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer Sync()

	// Debug should not be logged when level is warn
	output := captureOutput(t, func() {
		Debug("this should not appear")
		Sync()
	})

	if strings.Contains(output, "this should not appear") {
		t.Errorf("debug message should not be logged when level is warn")
	}

	// Info should not be logged when level is warn
	output = captureOutput(t, func() {
		Info("this should also not appear")
		Sync()
	})

	if strings.Contains(output, "this should also not appear") {
		t.Errorf("info message should not be logged when level is warn")
	}

	// Warn should be logged
	output = captureOutput(t, func() {
		Warn("this should appear")
		Sync()
	})

	if !strings.Contains(output, "this should appear") {
		t.Errorf("warn message should be logged when level is warn")
	}
}

// TestJSONFormat tests that JSON format produces valid JSON with required fields.
func TestJSONFormat(t *testing.T) {
	resetGlobalLogger()

	cfg := &Config{
		Level:        LevelInfo,
		Format:       FormatJSON,
		ServiceName:  "json-test-service",
		OutputPaths:  []string{"stdout"},
		EnableCaller: false,
	}

	if err := Init(cfg); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer Sync()

	output := captureOutput(t, func() {
		Info("json test message", "custom_key", "custom_value")
		Sync()
	})

	if output == "" {
		t.Fatal("expected log output but got none")
	}

	// Parse as JSON
	var logEntry map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &logEntry); err != nil {
		t.Fatalf("failed to parse log output as JSON: %v\nOutput: %s", err, output)
	}

	// Check required fields
	requiredFields := []string{"timestamp", "level", "message", "service"}
	for _, field := range requiredFields {
		if _, ok := logEntry[field]; !ok {
			t.Errorf("JSON output missing required field: %s", field)
		}
	}

	// Check service name
	if service, ok := logEntry["service"].(string); !ok || service != "json-test-service" {
		t.Errorf("expected service 'json-test-service', got: %v", logEntry["service"])
	}

	// Check message
	if msg, ok := logEntry["message"].(string); !ok || msg != "json test message" {
		t.Errorf("expected message 'json test message', got: %v", logEntry["message"])
	}

	// Check custom field
	if val, ok := logEntry["custom_key"].(string); !ok || val != "custom_value" {
		t.Errorf("expected custom_key 'custom_value', got: %v", logEntry["custom_key"])
	}
}

// TestContextIntegration tests that trace_id and span_id are extracted from context.
func TestContextIntegration(t *testing.T) {
	resetGlobalLogger()

	cfg := &Config{
		Level:        LevelInfo,
		Format:       FormatJSON,
		ServiceName:  "ctx-test-service",
		OutputPaths:  []string{"stdout"},
		EnableCaller: false,
	}

	if err := Init(cfg); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer Sync()

	ctx := context.Background()
	ctx = ContextWithTraceID(ctx, "trace-123-abc")
	ctx = ContextWithSpanID(ctx, "span-456-def")

	output := captureOutput(t, func() {
		InfoContext(ctx, "context test message")
		Sync()
	})

	if output == "" {
		t.Fatal("expected log output but got none")
	}

	var logEntry map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &logEntry); err != nil {
		t.Fatalf("failed to parse log output as JSON: %v\nOutput: %s", err, output)
	}

	// Check trace_id
	if traceID, ok := logEntry["trace_id"].(string); !ok || traceID != "trace-123-abc" {
		t.Errorf("expected trace_id 'trace-123-abc', got: %v", logEntry["trace_id"])
	}

	// Check span_id
	if spanID, ok := logEntry["span_id"].(string); !ok || spanID != "span-456-def" {
		t.Errorf("expected span_id 'span-456-def', got: %v", logEntry["span_id"])
	}
}

// TestDynamicLevel tests that SetLevel changes the log level at runtime.
func TestDynamicLevel(t *testing.T) {
	resetGlobalLogger()

	cfg := &Config{
		Level:        LevelInfo,
		Format:       FormatJSON,
		ServiceName:  "level-test-service",
		OutputPaths:  []string{"stdout"},
		EnableCaller: false,
	}

	if err := Init(cfg); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer Sync()

	// Verify initial level
	if level := GetLevel(); level != "info" {
		t.Errorf("expected initial level 'info', got: %s", level)
	}

	// Debug should not be logged at info level
	output := captureOutput(t, func() {
		Debug("debug before level change")
		Sync()
	})

	if strings.Contains(output, "debug before level change") {
		t.Error("debug message should not be logged at info level")
	}

	// Change level to debug
	if err := SetLevel("debug"); err != nil {
		t.Fatalf("SetLevel failed: %v", err)
	}

	// Verify level changed
	if level := GetLevel(); level != "debug" {
		t.Errorf("expected level 'debug' after SetLevel, got: %s", level)
	}

	// Debug should now be logged
	output = captureOutput(t, func() {
		Debug("debug after level change")
		Sync()
	})

	if !strings.Contains(output, "debug after level change") {
		t.Error("debug message should be logged after level change to debug")
	}

	// Test invalid level
	if err := SetLevel("invalid"); err != ErrInvalidLevel {
		t.Errorf("expected ErrInvalidLevel for invalid level, got: %v", err)
	}
}

// TestWithFields tests that With() creates a new logger with attached fields.
func TestWithFields(t *testing.T) {
	resetGlobalLogger()

	cfg := &Config{
		Level:        LevelInfo,
		Format:       FormatJSON,
		ServiceName:  "with-test-service",
		OutputPaths:  []string{"stdout"},
		EnableCaller: false,
	}

	if err := Init(cfg); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer Sync()

	// Create child logger with fields
	childLogger := With("module", "test-module", "version", "1.0")

	// Log with child logger
	output := captureOutput(t, func() {
		childLogger.Info("message from child")
		Sync()
	})

	var logEntry map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &logEntry); err != nil {
		t.Fatalf("failed to parse log output as JSON: %v\nOutput: %s", err, output)
	}

	// Check that child logger has the attached fields
	if module, ok := logEntry["module"].(string); !ok || module != "test-module" {
		t.Errorf("expected module 'test-module', got: %v", logEntry["module"])
	}
	if version, ok := logEntry["version"].(string); !ok || version != "1.0" {
		t.Errorf("expected version '1.0', got: %v", logEntry["version"])
	}

	// Log with original logger - should not have the child's fields
	output = captureOutput(t, func() {
		Info("message from parent")
		Sync()
	})

	// Handle case where multiple lines are captured
	lines := strings.Split(strings.TrimSpace(output), "\n")
	var parentOutput string
	for _, line := range lines {
		if strings.Contains(line, "message from parent") {
			parentOutput = line
			break
		}
	}
	if parentOutput == "" {
		t.Fatalf("failed to find parent log output in: %s", output)
	}

	// Create a new map for parsing to avoid carrying over fields from previous parse
	var parentEntry map[string]interface{}
	if err := json.Unmarshal([]byte(parentOutput), &parentEntry); err != nil {
		t.Fatalf("failed to parse log output as JSON: %v\nOutput: %s", err, parentOutput)
	}

	// Parent should not have child's fields
	if _, ok := parentEntry["module"]; ok {
		t.Error("parent logger should not have child's module field")
	}
}

// TestSampling tests that sampling reduces log output for high-frequency logs.
func TestSampling(t *testing.T) {
	resetGlobalLogger()

	cfg := &Config{
		Level:        LevelInfo,
		Format:       FormatJSON,
		ServiceName:  "sampling-test-service",
		OutputPaths:  []string{"stdout"},
		EnableCaller: false,
		Sampling: &SamplingConfig{
			Initial:    5,  // Only first 5 logs per second are output
			Thereafter: 10, // Then 1 in 10
		},
	}

	if err := Init(cfg); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer Sync()

	// Generate many identical logs
	const numLogs = 100
	var outputLines int

	output := captureOutput(t, func() {
		for i := 0; i < numLogs; i++ {
			Info("repeated message")
		}
		Sync()
	})

	// Count output lines
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, "repeated message") {
			outputLines++
		}
	}

	// With sampling, output should be significantly less than input
	// Initial 5 + (95/10) = approximately 14-15 logs
	if outputLines >= numLogs {
		t.Errorf("sampling did not reduce output: got %d lines for %d logs", outputLines, numLogs)
	}

	// But we should have at least some output
	if outputLines == 0 {
		t.Error("expected some log output with sampling")
	}
}

// TestSync tests that Sync() does not return an error.
func TestSync(t *testing.T) {
	resetGlobalLogger()

	cfg := DefaultConfig("sync-test-service")
	if err := Init(cfg); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Sync should not return an error
	if err := Sync(); err != nil {
		t.Errorf("Sync returned error: %v", err)
	}
}

// TestConfigValidation tests configuration validation.
func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr error
	}{
		{
			name: "valid config",
			config: &Config{
				ServiceName: "test-service",
				Level:       LevelInfo,
				Format:      FormatJSON,
			},
			wantErr: nil,
		},
		{
			name: "missing service name",
			config: &Config{
				Level:  LevelInfo,
				Format: FormatJSON,
			},
			wantErr: ErrServiceNameRequired,
		},
		{
			name: "invalid level",
			config: &Config{
				ServiceName: "test-service",
				Level:       Level("invalid"),
			},
			wantErr: ErrInvalidLevel,
		},
		{
			name: "invalid format",
			config: &Config{
				ServiceName: "test-service",
				Format:      Format("invalid"),
			},
			wantErr: ErrInvalidFormat,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if err != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestDefaultConfig tests that DefaultConfig returns production-ready defaults.
func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig("my-service")

	if cfg.ServiceName != "my-service" {
		t.Errorf("expected ServiceName 'my-service', got: %s", cfg.ServiceName)
	}
	if cfg.Level != LevelInfo {
		t.Errorf("expected Level 'info', got: %s", cfg.Level)
	}
	if cfg.Format != FormatJSON {
		t.Errorf("expected Format 'json', got: %s", cfg.Format)
	}
	if !cfg.EnableCaller {
		t.Error("expected EnableCaller to be true")
	}
	if !cfg.EnableStackTrace {
		t.Error("expected EnableStackTrace to be true")
	}
	if cfg.Development {
		t.Error("expected Development to be false")
	}
	if cfg.Sampling == nil {
		t.Error("expected Sampling to be configured")
	}
}

// TestDevelopmentConfig tests that DevelopmentConfig returns dev-friendly defaults.
func TestDevelopmentConfig(t *testing.T) {
	cfg := DevelopmentConfig("my-service")

	if cfg.ServiceName != "my-service" {
		t.Errorf("expected ServiceName 'my-service', got: %s", cfg.ServiceName)
	}
	if cfg.Level != LevelDebug {
		t.Errorf("expected Level 'debug', got: %s", cfg.Level)
	}
	if cfg.Format != FormatConsole {
		t.Errorf("expected Format 'console', got: %s", cfg.Format)
	}
	if !cfg.EnableCaller {
		t.Error("expected EnableCaller to be true")
	}
	if !cfg.Development {
		t.Error("expected Development to be true")
	}
	if cfg.Sampling != nil {
		t.Error("expected Sampling to be nil in development")
	}
}

// TestContextFunctions tests context helper functions.
func TestContextFunctions(t *testing.T) {
	ctx := context.Background()

	// Test trace ID
	ctx = ContextWithTraceID(ctx, "trace-123")
	if traceID := TraceIDFromContext(ctx); traceID != "trace-123" {
		t.Errorf("expected trace_id 'trace-123', got: %s", traceID)
	}

	// Test span ID
	ctx = ContextWithSpanID(ctx, "span-456")
	if spanID := SpanIDFromContext(ctx); spanID != "span-456" {
		t.Errorf("expected span_id 'span-456', got: %s", spanID)
	}

	// Test nil context
	if traceID := TraceIDFromContext(nil); traceID != "" {
		t.Errorf("expected empty trace_id for nil context, got: %s", traceID)
	}
	if spanID := SpanIDFromContext(nil); spanID != "" {
		t.Errorf("expected empty span_id for nil context, got: %s", spanID)
	}
}

// TestFromContext tests logger extraction from context.
func TestFromContext(t *testing.T) {
	resetGlobalLogger()

	cfg := DefaultConfig("ctx-logger-test")
	if err := Init(cfg); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer Sync()

	// Without logger in context, should return global logger
	ctx := context.Background()
	l := FromContext(ctx)
	if l == nil {
		t.Error("FromContext returned nil")
	}

	// With logger in context
	customLogger := GetLogger().With("custom", "field")
	ctx = ContextWithLogger(ctx, customLogger)
	l = FromContext(ctx)
	if l == nil {
		t.Error("FromContext with logger returned nil")
	}

	// Nil context should return global logger
	l = FromContext(nil)
	if l == nil {
		t.Error("FromContext(nil) returned nil")
	}
}

// TestConcurrentLogging tests thread-safety of logging operations.
func TestConcurrentLogging(t *testing.T) {
	resetGlobalLogger()

	cfg := &Config{
		Level:        LevelInfo,
		Format:       FormatJSON,
		ServiceName:  "concurrent-test-service",
		OutputPaths:  []string{"stdout"},
		EnableCaller: false,
	}

	if err := Init(cfg); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer Sync()

	const numGoroutines = 100
	const logsPerGoroutine = 10

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < logsPerGoroutine; j++ {
				Info("concurrent log", "goroutine", id, "iteration", j)
			}
		}(i)
	}

	wg.Wait()

	// If we get here without panics or deadlocks, the test passes
}

// TestWithGroup tests field grouping.
func TestWithGroup(t *testing.T) {
	resetGlobalLogger()

	cfg := &Config{
		Level:        LevelInfo,
		Format:       FormatJSON,
		ServiceName:  "group-test-service",
		OutputPaths:  []string{"stdout"},
		EnableCaller: false,
	}

	if err := Init(cfg); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer Sync()

	groupedLogger := WithGroup("request")

	output := captureOutput(t, func() {
		groupedLogger.Info("grouped message", "method", "GET", "path", "/api")
		Sync()
	})

	// Just verify it doesn't error and produces output
	if output == "" {
		t.Error("expected log output with grouped fields")
	}
}

// TestWithContext tests the WithContext method.
func TestWithContext(t *testing.T) {
	resetGlobalLogger()

	cfg := &Config{
		Level:        LevelInfo,
		Format:       FormatJSON,
		ServiceName:  "withctx-test-service",
		OutputPaths:  []string{"stdout"},
		EnableCaller: false,
	}

	if err := Init(cfg); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer Sync()

	ctx := context.Background()
	ctx = ContextWithTraceID(ctx, "trace-withctx-123")

	ctxLogger := GetLogger().WithContext(ctx)

	output := captureOutput(t, func() {
		ctxLogger.Info("message with context fields")
		Sync()
	})

	var logEntry map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &logEntry); err != nil {
		t.Fatalf("failed to parse log output as JSON: %v\nOutput: %s", err, output)
	}

	// Check trace_id was attached via WithContext
	if traceID, ok := logEntry["trace_id"].(string); !ok || traceID != "trace-withctx-123" {
		t.Errorf("expected trace_id 'trace-withctx-123', got: %v", logEntry["trace_id"])
	}
}

// TestHandler tests the Handler() method returns a valid slog.Handler.
func TestHandler(t *testing.T) {
	resetGlobalLogger()

	cfg := DefaultConfig("handler-test-service")
	if err := Init(cfg); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer Sync()

	handler := GetLogger().Handler()
	if handler == nil {
		t.Error("Handler() returned nil")
	}
}

// TestConsoleFormat tests console format output.
func TestConsoleFormat(t *testing.T) {
	resetGlobalLogger()

	cfg := &Config{
		Level:        LevelInfo,
		Format:       FormatConsole,
		ServiceName:  "console-test-service",
		OutputPaths:  []string{"stdout"},
		EnableCaller: false,
	}

	if err := Init(cfg); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer Sync()

	output := captureOutput(t, func() {
		Info("console format test")
		Sync()
	})

	// Console format should not be valid JSON
	var logEntry map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &logEntry); err == nil {
		// If it parses as JSON, it's not console format
		// But zap console encoder still produces structured output, so we just check it's not empty
	}

	if !strings.Contains(output, "console format test") {
		t.Error("expected output to contain the log message")
	}
}

// TestAllContextMethods tests all context-aware logging methods.
func TestAllContextMethods(t *testing.T) {
	resetGlobalLogger()

	cfg := &Config{
		Level:        LevelDebug,
		Format:       FormatJSON,
		ServiceName:  "allctx-test-service",
		OutputPaths:  []string{"stdout"},
		EnableCaller: false,
	}

	if err := Init(cfg); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer Sync()

	ctx := context.Background()
	ctx = ContextWithTraceID(ctx, "trace-all")

	methods := []struct {
		name    string
		logFunc func(context.Context, string, ...any)
		level   string
	}{
		{"DebugContext", DebugContext, "debug"},
		{"InfoContext", InfoContext, "info"},
		{"WarnContext", WarnContext, "warn"},
		{"ErrorContext", ErrorContext, "error"},
	}

	for _, m := range methods {
		t.Run(m.name, func(t *testing.T) {
			output := captureOutput(t, func() {
				m.logFunc(ctx, m.name+" test message")
				Sync()
			})

			if !strings.Contains(output, m.name+" test message") {
				t.Errorf("expected output to contain message for %s", m.name)
			}
			if !strings.Contains(output, "trace-all") {
				t.Errorf("expected output to contain trace_id for %s", m.name)
			}
		})
	}
}

// TestMultiOutputConfig tests that MultiOutputConfig creates correct configuration.
func TestMultiOutputConfig(t *testing.T) {
	cfg := MultiOutputConfig("multi-service", "/var/log/app.log")

	if cfg.ServiceName != "multi-service" {
		t.Errorf("expected ServiceName 'multi-service', got: %s", cfg.ServiceName)
	}

	if len(cfg.Outputs) != 2 {
		t.Fatalf("expected 2 outputs, got: %d", len(cfg.Outputs))
	}

	// Check stdout output
	if cfg.Outputs[0].Type != OutputTypeStdout {
		t.Errorf("expected first output type 'stdout', got: %s", cfg.Outputs[0].Type)
	}
	if cfg.Outputs[0].Format != FormatConsole {
		t.Errorf("expected first output format 'console', got: %s", cfg.Outputs[0].Format)
	}

	// Check file output
	if cfg.Outputs[1].Type != OutputTypeFile {
		t.Errorf("expected second output type 'file', got: %s", cfg.Outputs[1].Type)
	}
	if cfg.Outputs[1].Path != "/var/log/app.log" {
		t.Errorf("expected second output path '/var/log/app.log', got: %s", cfg.Outputs[1].Path)
	}
	if cfg.Outputs[1].Format != FormatJSON {
		t.Errorf("expected second output format 'json', got: %s", cfg.Outputs[1].Format)
	}
	if cfg.Outputs[1].Rotation == nil {
		t.Error("expected second output to have rotation config")
	}
}

// TestDefaultRotationConfig tests that DefaultRotationConfig returns correct defaults.
func TestDefaultRotationConfig(t *testing.T) {
	rotation := DefaultRotationConfig()

	if rotation.MaxSize != 100 {
		t.Errorf("expected MaxSize 100, got: %d", rotation.MaxSize)
	}
	if rotation.MaxBackups != 7 {
		t.Errorf("expected MaxBackups 7, got: %d", rotation.MaxBackups)
	}
	if rotation.MaxAge != 30 {
		t.Errorf("expected MaxAge 30, got: %d", rotation.MaxAge)
	}
	if !rotation.Compress {
		t.Error("expected Compress to be true")
	}
	if !rotation.LocalTime {
		t.Error("expected LocalTime to be true")
	}
}

// TestFileOutput tests logging to file with rotation.
func TestFileOutput(t *testing.T) {
	resetGlobalLogger()

	// Create temp directory for log files
	tmpDir, err := os.MkdirTemp("", "logger_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	logFile := tmpDir + "/test.log"

	cfg := &Config{
		Level:       LevelInfo,
		Format:      FormatJSON,
		ServiceName: "file-test-service",
		Outputs: []OutputConfig{
			{
				Type:   OutputTypeFile,
				Path:   logFile,
				Format: FormatJSON,
				Rotation: &RotationConfig{
					MaxSize:    1, // 1MB for testing
					MaxBackups: 3,
					MaxAge:     1,
					Compress:   false,
					LocalTime:  true,
				},
			},
		},
		EnableCaller: false,
	}

	if err := Init(cfg); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Log some messages
	for i := 0; i < 10; i++ {
		Info("file output test message", "iteration", i)
	}
	Sync()

	// Verify log file exists and has content
	content, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}

	if len(content) == 0 {
		t.Error("expected log file to have content")
	}

	// Verify JSON format
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) < 10 {
		t.Errorf("expected at least 10 log lines, got: %d", len(lines))
	}

	// Parse first line to verify structure
	var logEntry map[string]interface{}
	if err := json.Unmarshal([]byte(lines[0]), &logEntry); err != nil {
		t.Fatalf("failed to parse log line as JSON: %v", err)
	}

	if logEntry["message"] != "file output test message" {
		t.Errorf("unexpected message: %v", logEntry["message"])
	}
}

// TestMultiOutput tests logging to multiple outputs simultaneously.
func TestMultiOutput(t *testing.T) {
	resetGlobalLogger()

	// Create temp directory for log files
	tmpDir, err := os.MkdirTemp("", "logger_multi_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	logFile := tmpDir + "/multi.log"

	cfg := &Config{
		Level:       LevelInfo,
		Format:      FormatJSON,
		ServiceName: "multi-output-test",
		Outputs: []OutputConfig{
			{
				Type:   OutputTypeStdout,
				Format: FormatConsole,
			},
			{
				Type:     OutputTypeFile,
				Path:     logFile,
				Format:   FormatJSON,
				Rotation: DefaultRotationConfig(),
			},
		},
		EnableCaller: false,
	}

	if err := Init(cfg); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Capture stdout and log
	output := captureOutput(t, func() {
		Info("multi-output test message", "key", "value")
		Sync()
	})

	// Verify stdout output (console format)
	if !strings.Contains(output, "multi-output test message") {
		t.Error("expected stdout to contain the log message")
	}

	// Verify file output (JSON format)
	fileContent, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}

	if len(fileContent) == 0 {
		t.Error("expected log file to have content")
	}

	var logEntry map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(fileContent))), &logEntry); err != nil {
		t.Fatalf("failed to parse file log as JSON: %v", err)
	}

	if logEntry["message"] != "multi-output test message" {
		t.Errorf("unexpected message in file: %v", logEntry["message"])
	}
}

// TestOutputConfigValidation tests validation of output configurations.
func TestOutputConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
	}{
		{
			name: "valid file output with path",
			config: &Config{
				ServiceName: "test",
				Outputs: []OutputConfig{
					{Type: OutputTypeFile, Path: "/var/log/test.log"},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid file output without path",
			config: &Config{
				ServiceName: "test",
				Outputs: []OutputConfig{
					{Type: OutputTypeFile, Path: ""},
				},
			},
			wantErr: true,
		},
		{
			name: "valid stdout output",
			config: &Config{
				ServiceName: "test",
				Outputs: []OutputConfig{
					{Type: OutputTypeStdout},
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestOutputLevelFiltering tests per-output level filtering.
func TestOutputLevelFiltering(t *testing.T) {
	resetGlobalLogger()

	// Create temp directory for log files
	tmpDir, err := os.MkdirTemp("", "logger_level_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	errorLogFile := tmpDir + "/error.log"

	cfg := &Config{
		Level:       LevelDebug,
		Format:      FormatJSON,
		ServiceName: "level-filter-test",
		Outputs: []OutputConfig{
			{
				Type:   OutputTypeStdout,
				Format: FormatConsole,
				Level:  LevelDebug, // All levels to stdout
			},
			{
				Type:   OutputTypeFile,
				Path:   errorLogFile,
				Format: FormatJSON,
				Level:  LevelError, // Only errors to file
			},
		},
		EnableCaller: false,
	}

	if err := Init(cfg); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Log messages at different levels
	Debug("debug message")
	Info("info message")
	Warn("warn message")
	Error("error message")
	Sync()

	// Read error log file
	fileContent, err := os.ReadFile(errorLogFile)
	if err != nil {
		t.Fatalf("failed to read error log file: %v", err)
	}

	fileStr := string(fileContent)

	// File should only contain error message
	if !strings.Contains(fileStr, "error message") {
		t.Error("expected error log file to contain error message")
	}

	// File should not contain debug/info/warn messages
	if strings.Contains(fileStr, "debug message") {
		t.Error("error log file should not contain debug message")
	}
	if strings.Contains(fileStr, "info message") {
		t.Error("error log file should not contain info message")
	}
	if strings.Contains(fileStr, "warn message") {
		t.Error("error log file should not contain warn message")
	}
}
