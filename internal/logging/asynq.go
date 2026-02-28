// Package logging provides framework-specific logging adapters for vpc_workflow_demo.
// This file contains Asynq logging adapter.
package logging

import (
	"fmt"

	"github.com/hibiken/asynq"
	"github.com/yourorg/nsp-common/pkg/logger"
)

// AsynqAdapter configures Asynq to use nsp-common logger.
type AsynqAdapter struct {
	logger logger.Logger
}

// NewAsynqAdapter creates a new Asynq logging adapter.
// If logger is nil, uses the global logger.
func NewAsynqAdapter(l logger.Logger) *AsynqAdapter {
	if l == nil {
		l = logger.GetLogger()
	}
	return &AsynqAdapter{logger: l}
}

// GetAsynqLogger returns an asynq.Logger implementation that uses nsp-common logger.
// This ensures all Asynq framework logs are formatted consistently.
//
// Usage:
//
//	adapter := logging.NewAsynqAdapter(nil)
//	asynqLogger := adapter.GetAsynqLogger()
//	srv := asynq.NewServer(redisOpt, asynq.Config{
//	    Logger: asynqLogger,
//	    // ... other config
//	})
func (a *AsynqAdapter) GetAsynqLogger() asynq.Logger {
	return &asynqLoggerImpl{
		logger: a.logger,
	}
}

// asynqLoggerImpl implements asynq.Logger interface.
type asynqLoggerImpl struct {
	logger logger.Logger
}

// Debug logs a message at debug level.
func (l *asynqLoggerImpl) Debug(args ...interface{}) {
	l.logger.Debug("[asynq] "+formatMessage(args...))
}

// Info logs a message at info level.
func (l *asynqLoggerImpl) Info(args ...interface{}) {
	l.logger.Info("[asynq] "+formatMessage(args...))
}

// Warn logs a message at warn level.
func (l *asynqLoggerImpl) Warn(args ...interface{}) {
	l.logger.Warn("[asynq] "+formatMessage(args...))
}

// Error logs a message at error level.
func (l *asynqLoggerImpl) Error(args ...interface{}) {
	l.logger.Error("[asynq] "+formatMessage(args...))
}

// Fatal logs a message at error level and exits.
func (l *asynqLoggerImpl) Fatal(args ...interface{}) {
	l.logger.Fatal("[asynq] "+formatMessage(args...))
}

// formatMessage formats variadic arguments into a single string message.
// This matches the behavior of log.Print* functions.
func formatMessage(args ...interface{}) string {
	if len(args) == 0 {
		return ""
	}
	
	// If first argument is a format string with % patterns, treat as format + args
	if len(args) > 1 {
		if format, ok := args[0].(string); ok {
			// Check if it looks like a format string
			if hasFormatVerbs(format) {
				// Use fmt.Sprintf for consistent formatting
				return fmt.Sprintf(format, args[1:]...)
			}
		}
	}
	
	// Otherwise, concatenate all arguments with spaces (like fmt.Sprint)
	return fmt.Sprint(args...)
}

// hasFormatVerbs checks if a string contains format verbs like %s, %d, %v, etc.
func hasFormatVerbs(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+1 < len(s) {
			next := s[i+1]
			// Check for common format verbs
			if next == 's' || next == 'd' || next == 'v' || next == 'f' || 
			   next == 'x' || next == 't' || next == 'q' || next == 'p' ||
			   next == 'e' || next == 'g' || next == 'T' {
				return true
			}
		}
	}
	return false
}
