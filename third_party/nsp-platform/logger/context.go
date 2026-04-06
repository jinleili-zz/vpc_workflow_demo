// Package logger provides a unified logging module for NSP platform microservices.
// context.go provides context integration for trace ID, span ID, and logger propagation.
package logger

import (
	"context"
)

// contextKey is a private type for context keys to avoid collisions with other packages.
type contextKey int

// Context keys for storing logger-related values.
const (
	contextKeyTraceID contextKey = iota
	contextKeySpanID
	contextKeyLogger
)

// ContextWithTraceID returns a new context with the trace ID attached.
// The trace ID will be automatically extracted and included in log entries
// when using *Context logging methods (e.g., InfoContext, ErrorContext).
//
// Parameters:
//   - ctx: The parent context
//   - traceID: The distributed tracing trace ID
//
// Returns:
//   - context.Context: A new context with the trace ID attached
func ContextWithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, contextKeyTraceID, traceID)
}

// ContextWithSpanID returns a new context with the span ID attached.
// The span ID will be automatically extracted and included in log entries
// when using *Context logging methods.
//
// Parameters:
//   - ctx: The parent context
//   - spanID: The distributed tracing span ID
//
// Returns:
//   - context.Context: A new context with the span ID attached
func ContextWithSpanID(ctx context.Context, spanID string) context.Context {
	return context.WithValue(ctx, contextKeySpanID, spanID)
}

// ContextWithLogger returns a new context with the logger attached.
// This allows passing a logger with pre-configured fields through the call chain.
//
// Parameters:
//   - ctx: The parent context
//   - l: The logger instance to attach
//
// Returns:
//   - context.Context: A new context with the logger attached
func ContextWithLogger(ctx context.Context, l Logger) context.Context {
	return context.WithValue(ctx, contextKeyLogger, l)
}

// TraceIDFromContext extracts the trace ID from the context.
// Returns an empty string if no trace ID is found.
//
// Parameters:
//   - ctx: The context to extract from
//
// Returns:
//   - string: The trace ID, or empty string if not found
func TraceIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if traceID, ok := ctx.Value(contextKeyTraceID).(string); ok {
		return traceID
	}
	return ""
}

// SpanIDFromContext extracts the span ID from the context.
// Returns an empty string if no span ID is found.
//
// Parameters:
//   - ctx: The context to extract from
//
// Returns:
//   - string: The span ID, or empty string if not found
func SpanIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if spanID, ok := ctx.Value(contextKeySpanID).(string); ok {
		return spanID
	}
	return ""
}

// FromContext extracts the logger from the context.
// If no logger is found in the context, returns the global logger.
// This ensures logging is always available even if no logger was explicitly set.
//
// Parameters:
//   - ctx: The context to extract from
//
// Returns:
//   - Logger: The logger from context, or the global logger if not found
func FromContext(ctx context.Context) Logger {
	if ctx == nil {
		return GetLogger()
	}
	if l, ok := ctx.Value(contextKeyLogger).(Logger); ok && l != nil {
		return l
	}
	return GetLogger()
}

// extractContextFields extracts trace_id and span_id from context as slog attributes.
// This is an internal function used by logger implementations to automatically
// include tracing information in log entries.
//
// Parameters:
//   - ctx: The context to extract fields from
//
// Returns:
//   - []any: A slice of slog.Attr for trace_id and span_id (if present)
func extractContextFields(ctx context.Context) []any {
	if ctx == nil {
		return nil
	}

	var attrs []any

	if traceID := TraceIDFromContext(ctx); traceID != "" {
		attrs = append(attrs, FieldTraceID, traceID)
	}

	if spanID := SpanIDFromContext(ctx); spanID != "" {
		attrs = append(attrs, FieldSpanID, spanID)
	}

	return attrs
}
