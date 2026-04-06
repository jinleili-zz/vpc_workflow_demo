// Package logger provides a unified logging module for NSP platform microservices.
// access_middleware.go provides HTTP access log middleware for web frameworks.
package logger

import (
	"context"
	"time"
)

// AccessLogEntry represents a single HTTP access log entry.
// This struct provides all the fields typically needed for access logs.
type AccessLogEntry struct {
	// Request info
	Method     string `json:"method"`
	Path       string `json:"path"`
	Query      string `json:"query,omitempty"`
	ClientIP   string `json:"client_ip"`
	UserAgent  string `json:"user_agent,omitempty"`
	Referer    string `json:"referer,omitempty"`
	RequestID  string `json:"request_id,omitempty"`

	// Response info
	Status       int   `json:"status"`
	BodySize     int   `json:"body_size"`
	LatencyMS    int64 `json:"latency_ms"`

	// Trace info
	TraceID string `json:"trace_id,omitempty"`
	SpanID  string `json:"span_id,omitempty"`

	// Error info (if any)
	Error string `json:"error,omitempty"`
}

// LogAccess logs an HTTP access log entry using the access logger.
// This is a convenience function for logging access entries with all standard fields.
//
// Example:
//
//	logger.LogAccess(ctx, &logger.AccessLogEntry{
//	    Method:    "GET",
//	    Path:      "/api/v1/users",
//	    ClientIP:  "192.168.1.100",
//	    Status:    200,
//	    LatencyMS: 45,
//	})
func LogAccess(ctx context.Context, entry *AccessLogEntry) {
	if entry == nil {
		return
	}

	// Build log args
	args := []any{
		FieldHTTPMethod, entry.Method,
		FieldHTTPPath, entry.Path,
		FieldHTTPStatus, entry.Status,
		FieldHTTPLatency, entry.LatencyMS,
		FieldClientIP, entry.ClientIP,
	}

	if entry.Query != "" {
		args = append(args, FieldHTTPQuery, entry.Query)
	}
	if entry.UserAgent != "" {
		args = append(args, FieldUserAgent, entry.UserAgent)
	}
	if entry.Referer != "" {
		args = append(args, FieldReferer, entry.Referer)
	}
	if entry.RequestID != "" {
		args = append(args, FieldRequestID, entry.RequestID)
	}
	if entry.BodySize > 0 {
		args = append(args, FieldResponseSize, entry.BodySize)
	}
	if entry.TraceID != "" {
		args = append(args, FieldTraceID, entry.TraceID)
	}
	if entry.SpanID != "" {
		args = append(args, FieldSpanID, entry.SpanID)
	}
	if entry.Error != "" {
		args = append(args, FieldError, entry.Error)
	}

	// Log at appropriate level based on status code
	accessLogger := Access()
	msg := "HTTP Request"

	switch {
	case entry.Status >= 500:
		accessLogger.ErrorContext(ctx, msg, args...)
	case entry.Status >= 400:
		accessLogger.WarnContext(ctx, msg, args...)
	default:
		accessLogger.InfoContext(ctx, msg, args...)
	}
}

// AccessLogConfig holds configuration for the access log middleware.
//
// Note: This struct is intended for use by framework-specific middleware adapters
// (e.g., Gin, Echo, Fiber middleware). The low-level LogAccess function does not
// consume this configuration directly - it is the responsibility of the middleware
// adapter to apply these settings when constructing AccessLogEntry instances.
type AccessLogConfig struct {
	// SkipPaths is a list of paths to skip logging (e.g., health check endpoints).
	SkipPaths []string

	// SlowRequestThreshold is the threshold in milliseconds to mark a request as slow.
	// Slow requests are logged at warn level even if successful.
	// Default: 0 (disabled)
	SlowRequestThreshold time.Duration

	// IncludeQuery determines whether to include query string in logs.
	// Default: false (for security, to avoid logging sensitive data)
	IncludeQuery bool

	// IncludeUserAgent determines whether to include user agent in logs.
	// Default: true
	IncludeUserAgent bool

	// IncludeReferer determines whether to include referer in logs.
	// Default: false
	IncludeReferer bool
}

// DefaultAccessLogConfig returns default access log configuration.
func DefaultAccessLogConfig() *AccessLogConfig {
	return &AccessLogConfig{
		SkipPaths: []string{
			"/health",
			"/healthz",
			"/ready",
			"/readyz",
			"/metrics",
			"/favicon.ico",
		},
		SlowRequestThreshold: 0,
		IncludeQuery:         false,
		IncludeUserAgent:     true,
		IncludeReferer:       false,
	}
}

// shouldSkipPath checks if a path should be skipped for access logging.
func (c *AccessLogConfig) shouldSkipPath(path string) bool {
	for _, skip := range c.SkipPaths {
		if path == skip {
			return true
		}
	}
	return false
}
