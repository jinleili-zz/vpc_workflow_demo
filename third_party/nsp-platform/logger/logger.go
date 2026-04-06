// Package logger provides a unified logging module for NSP platform microservices.
// logger.go defines the Logger interface and global convenience functions.
package logger

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
)

// Common errors returned by the logger package.
var (
	// ErrNotInitialized is returned when operations are attempted before Init is called.
	ErrNotInitialized = errors.New("logger: not initialized")

	// ErrServiceNameRequired is returned when ServiceName is empty in Config.
	ErrServiceNameRequired = errors.New("logger: service name is required")

	// ErrInvalidLevel is returned when an invalid log level is provided.
	ErrInvalidLevel = errors.New("logger: invalid log level")

	// ErrInvalidFormat is returned when an invalid log format is provided.
	ErrInvalidFormat = errors.New("logger: invalid log format")
)

// Logger is the main logging interface.
// It provides structured logging capabilities with context awareness
// and support for both slog-style key-value pairs and slog.Attr.
type Logger interface {
	// Debug logs a message at debug level.
	// args can be alternating key-value pairs or slog.Attr.
	// Example:
	//   logger.Debug("processing item", "item_id", 123)
	//   logger.Debug("processing item", slog.Int("item_id", 123))
	Debug(msg string, args ...any)

	// Info logs a message at info level.
	// args can be alternating key-value pairs or slog.Attr.
	Info(msg string, args ...any)

	// Warn logs a message at warn level.
	// args can be alternating key-value pairs or slog.Attr.
	Warn(msg string, args ...any)

	// Error logs a message at error level.
	// args can be alternating key-value pairs or slog.Attr.
	Error(msg string, args ...any)

	// Fatal logs a message at error level and then calls os.Exit(1).
	// args can be alternating key-value pairs or slog.Attr.
	Fatal(msg string, args ...any)

	// DebugContext logs a message at debug level with context.
	// Automatically extracts trace_id and span_id from the context.
	DebugContext(ctx context.Context, msg string, args ...any)

	// InfoContext logs a message at info level with context.
	// Automatically extracts trace_id and span_id from the context.
	InfoContext(ctx context.Context, msg string, args ...any)

	// WarnContext logs a message at warn level with context.
	// Automatically extracts trace_id and span_id from the context.
	WarnContext(ctx context.Context, msg string, args ...any)

	// ErrorContext logs a message at error level with context.
	// Automatically extracts trace_id and span_id from the context.
	ErrorContext(ctx context.Context, msg string, args ...any)

	// With returns a new Logger with the given fields attached.
	// The new Logger includes all fields from the parent plus the new fields.
	// args can be alternating key-value pairs or slog.Attr.
	// Example:
	//   childLogger := logger.With("module", "order-service", "version", "1.0")
	With(args ...any) Logger

	// WithGroup returns a new Logger that groups all subsequent fields under the given name.
	// This is useful for organizing related fields.
	// Example:
	//   logger.WithGroup("request").Info("received", "method", "GET", "path", "/api/v1/orders")
	//   // Output: {"request": {"method": "GET", "path": "/api/v1/orders"}}
	WithGroup(name string) Logger

	// WithContext returns a new Logger with trace fields extracted from the context.
	// This is a convenience method that combines extracting trace fields and creating a child logger.
	WithContext(ctx context.Context) Logger

	// Sync flushes any buffered log entries.
	// This should be called before program exit to ensure all logs are written.
	Sync() error

	// SetLevel dynamically changes the log level at runtime.
	// This does not require reinitializing the logger.
	// Valid levels: "debug", "info", "warn", "error"
	SetLevel(level string) error

	// GetLevel returns the current log level.
	GetLevel() string

	// Handler returns the underlying slog.Handler.
	// This can be used when direct access to slog functionality is needed.
	Handler() slog.Handler
}

// Global logger instance and mutex for thread-safe initialization.
var (
	globalLogger Logger
	globalMu     sync.RWMutex
	initialized  bool
)

// Init initializes the global logger with the given configuration.
// This should be called once at application startup.
// If called multiple times, subsequent calls will replace the global logger.
//
// Parameters:
//   - cfg: The logger configuration
//
// Returns:
//   - error: An error if initialization fails
//
// Example:
//
//	cfg := logger.DefaultConfig("my-service")
//	if err := logger.Init(cfg); err != nil {
//	    log.Fatalf("failed to initialize logger: %v", err)
//	}
//	defer logger.Sync()
func Init(cfg *Config) error {
	if cfg == nil {
		cfg = DefaultConfig("unknown-service")
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	cfg.applyDefaults()

	l, err := newZapLogger(cfg)
	if err != nil {
		return err
	}

	globalMu.Lock()
	defer globalMu.Unlock()
	globalLogger = l
	initialized = true

	return nil
}

// GetLogger returns the global logger instance.
// If the logger has not been initialized, returns a default no-op logger
// that writes to stdout with minimal configuration.
//
// Returns:
//   - Logger: The global logger instance
func GetLogger() Logger {
	globalMu.RLock()
	defer globalMu.RUnlock()

	if !initialized || globalLogger == nil {
		// Return a basic fallback logger if not initialized
		return newFallbackLogger()
	}

	return globalLogger
}

// newFallbackLogger creates a minimal logger for use before initialization.
func newFallbackLogger() Logger {
	cfg := &Config{
		Level:       LevelInfo,
		Format:      FormatJSON,
		ServiceName: "uninitialized",
		OutputPaths: []string{"stdout"},
	}
	l, _ := newZapLogger(cfg)
	return l
}

// Debug logs a message at debug level using the global logger.
func Debug(msg string, args ...any) {
	GetLogger().Debug(msg, args...)
}

// Info logs a message at info level using the global logger.
func Info(msg string, args ...any) {
	GetLogger().Info(msg, args...)
}

// Warn logs a message at warn level using the global logger.
func Warn(msg string, args ...any) {
	GetLogger().Warn(msg, args...)
}

// Error logs a message at error level using the global logger.
func Error(msg string, args ...any) {
	GetLogger().Error(msg, args...)
}

// Fatal logs a message at error level using the global logger and exits.
func Fatal(msg string, args ...any) {
	GetLogger().Fatal(msg, args...)
}

// DebugContext logs a message at debug level with context using the global logger.
func DebugContext(ctx context.Context, msg string, args ...any) {
	GetLogger().DebugContext(ctx, msg, args...)
}

// InfoContext logs a message at info level with context using the global logger.
func InfoContext(ctx context.Context, msg string, args ...any) {
	GetLogger().InfoContext(ctx, msg, args...)
}

// WarnContext logs a message at warn level with context using the global logger.
func WarnContext(ctx context.Context, msg string, args ...any) {
	GetLogger().WarnContext(ctx, msg, args...)
}

// ErrorContext logs a message at error level with context using the global logger.
func ErrorContext(ctx context.Context, msg string, args ...any) {
	GetLogger().ErrorContext(ctx, msg, args...)
}

// With returns a new Logger with the given fields attached using the global logger.
func With(args ...any) Logger {
	return GetLogger().With(args...)
}

// WithGroup returns a new Logger with field grouping using the global logger.
func WithGroup(name string) Logger {
	return GetLogger().WithGroup(name)
}

// Sync flushes any buffered log entries from the global logger.
// This should be called before program exit.
func Sync() error {
	globalMu.RLock()
	defer globalMu.RUnlock()

	if globalLogger != nil {
		return globalLogger.Sync()
	}
	return nil
}

// SetLevel dynamically changes the log level of the global logger.
func SetLevel(level string) error {
	return GetLogger().SetLevel(level)
}

// GetLevel returns the current log level of the global logger.
func GetLevel() string {
	return GetLogger().GetLevel()
}

// exitFunc is the function called by Fatal. Can be overridden for testing.
var exitFunc = os.Exit
