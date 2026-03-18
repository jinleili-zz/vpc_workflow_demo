// Package logging provides framework-specific logging adapters for vpc_workflow_demo.
// This package contains adapters for Gin, Asynq, and other third-party frameworks.
package logging

import (
	"io"

	"github.com/gin-gonic/gin"
	"github.com/jinleili-zz/nsp-platform/logger"
)

// GinAdapter configures Gin to use nsp-common logger.
// Uses access logger for HTTP request/response logs.
type GinAdapter struct {
	logger logger.Logger
}

// NewGinAdapter creates a new Gin logging adapter.
// If logger is nil, uses the access logger (for HTTP access logs).
func NewGinAdapter(l logger.Logger) *GinAdapter {
	if l == nil {
		l = logger.Access()
	}
	return &GinAdapter{logger: l}
}

// SetupGinLogging configures Gin's default writer to use nsp-common access logger.
// This ensures all Gin framework logs (startup messages, errors, etc.) are
// formatted consistently with the rest of the application.
//
// Usage:
//
//	adapter := logging.NewGinAdapter(nil)
//	adapter.SetupGinLogging()
//	router := gin.Default()
func (a *GinAdapter) SetupGinLogging() {
	// Set Gin's default writer to use access logger for request logs
	gin.DefaultWriter = logger.NewWriterAdapter(
		a.logger,
		logger.WithLevel("info"),
		logger.WithPrefix("[gin]"),
	)

	// Set Gin's error writer for error logs (still uses access logger)
	gin.DefaultErrorWriter = logger.NewWriterAdapter(
		a.logger,
		logger.WithLevel("error"),
		logger.WithPrefix("[gin]"),
	)
}

// GetWriter returns an io.Writer that can be used with gin.New().
// This is useful when you need more control over Gin router creation.
//
// Usage:
//
//	adapter := logging.NewGinAdapter(nil)
//	writer := adapter.GetWriter()
//	router := gin.New()
//	router.Use(gin.LoggerWithWriter(writer))
func (a *GinAdapter) GetWriter() io.Writer {
	return logger.NewWriterAdapter(
		a.logger,
		logger.WithLevel("info"),
		logger.WithPrefix("[gin]"),
	)
}

// GetErrorWriter returns an io.Writer for error logs.
func (a *GinAdapter) GetErrorWriter() io.Writer {
	return logger.NewWriterAdapter(
		a.logger,
		logger.WithLevel("error"),
		logger.WithPrefix("[gin]"),
	)
}
