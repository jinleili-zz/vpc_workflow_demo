// Package logger provides io.Writer adapter for third-party frameworks.
// This file provides a generic WriterAdapter that allows third-party frameworks
// (like Gin, GORM, Asynq) to output logs through the nsp-common logger.
package logger

import (
	"bytes"
	"context"
)

// WriterAdapter adapts the Logger interface to io.Writer.
// This allows third-party frameworks that accept io.Writer to use nsp-common logger.
// The adapter automatically handles line buffering and log level classification.
type WriterAdapter struct {
	logger   Logger
	level    string // "debug", "info", "warn", "error"
	prefix   string // optional prefix for log messages (e.g., "[gin]", "[asynq]")
	ctx      context.Context
	useCtx   bool // whether to use Context methods for trace propagation
}

// WriterOption configures the WriterAdapter.
type WriterOption func(*WriterAdapter)

// WithLevel sets the log level for the adapter.
// Valid levels: "debug", "info", "warn", "error"
// Default: "info"
func WithLevel(level string) WriterOption {
	return func(w *WriterAdapter) {
		w.level = level
	}
}

// WithPrefix sets a prefix for all log messages.
// This helps identify the source framework in logs.
// Example: "[gin]", "[asynq]", "[gorm]"
func WithPrefix(prefix string) WriterOption {
	return func(w *WriterAdapter) {
		w.prefix = prefix
	}
}

// WithContext enables context-aware logging for trace propagation.
// When enabled, the adapter uses InfoContext, ErrorContext, etc.
// to automatically include trace_id and span_id in logs.
func WithContext(ctx context.Context) WriterOption {
	return func(w *WriterAdapter) {
		w.ctx = ctx
		w.useCtx = true
	}
}

// NewWriterAdapter creates a new WriterAdapter.
// If logger is nil, uses the global logger from GetLogger().
//
// Example:
//
//	// Basic usage
//	writer := logger.NewWriterAdapter(nil, logger.WithLevel("info"), logger.WithPrefix("[gin]"))
//	gin.DefaultWriter = writer
//
//	// With context for trace propagation
//	writer := logger.NewWriterAdapter(myLogger, logger.WithContext(ctx), logger.WithLevel("info"))
func NewWriterAdapter(logger Logger, opts ...WriterOption) *WriterAdapter {
	if logger == nil {
		logger = GetLogger()
	}

	w := &WriterAdapter{
		logger: logger,
		level:  "info", // default level
	}

	for _, opt := range opts {
		opt(w)
	}

	return w
}

// Write implements io.Writer interface.
// It buffers input and writes complete lines to the logger.
func (w *WriterAdapter) Write(p []byte) (n int, err error) {
	if len(p) == 0 {
		return 0, nil
	}

	// Trim trailing newlines
	msg := string(bytes.TrimRight(p, "\n\r"))
	if msg == "" {
		return len(p), nil
	}

	// Add prefix if configured
	if w.prefix != "" {
		msg = w.prefix + " " + msg
	}

	// Log at the configured level
	if w.useCtx && w.ctx != nil {
		w.logWithContext(msg)
	} else {
		w.log(msg)
	}

	return len(p), nil
}

// log writes the message without context.
func (w *WriterAdapter) log(msg string) {
	switch w.level {
	case "debug":
		w.logger.Debug(msg)
	case "warn":
		w.logger.Warn(msg)
	case "error":
		w.logger.Error(msg)
	default: // "info"
		w.logger.Info(msg)
	}
}

// logWithContext writes the message with context for trace propagation.
func (w *WriterAdapter) logWithContext(msg string) {
	switch w.level {
	case "debug":
		w.logger.DebugContext(w.ctx, msg)
	case "warn":
		w.logger.WarnContext(w.ctx, msg)
	case "error":
		w.logger.ErrorContext(w.ctx, msg)
	default: // "info"
		w.logger.InfoContext(w.ctx, msg)
	}
}

// UpdateContext updates the context for trace propagation.
// This is useful for long-lived adapters that need to update trace context per request.
func (w *WriterAdapter) UpdateContext(ctx context.Context) {
	w.ctx = ctx
	w.useCtx = ctx != nil
}
