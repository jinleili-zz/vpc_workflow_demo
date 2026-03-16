// Package logging provides a unified setup for third-party framework logging adapters.
// This file provides a single entry point to configure all framework adapters at once.
package logging

import (
	"github.com/paic/nsp-common/pkg/logger"
)

// SetupAllAdapters configures all third-party frameworks to use nsp-common logger.
// This should be called early in the application startup, after logger initialization
// but before creating Gin routers or Asynq servers.
//
// Usage:
//
//	// In main.go, after logger.Init()
//	logging.SetupAllAdapters()
//	
//	// Then create Gin router
//	router := gin.Default()
//	
//	// Or create Asynq server
//	adapter := logging.NewAsynqAdapter(nil)
//	srv := asynq.NewServer(redisOpt, asynq.Config{
//	    Logger: adapter.GetAsynqLogger(),
//	})
func SetupAllAdapters() {
	// Setup Gin logging (using access logger)
	ginAdapter := NewGinAdapter(nil)
	ginAdapter.SetupGinLogging()
	
	logger.Platform().Info("Third-party framework logging adapters configured",
		"frameworks", []string{"gin", "asynq"})
}

// SetupGinAdapter configures only Gin framework to use nsp-common logger.
// Use this if you only need Gin logging without other frameworks.
//
// Usage:
//
//	logging.SetupGinAdapter()
//	router := gin.Default()
func SetupGinAdapter() {
	ginAdapter := NewGinAdapter(nil)
	ginAdapter.SetupGinLogging()
	
	logger.Platform().Info("Gin framework logging adapter configured")
}

// GetAsynqAdapter returns a configured Asynq adapter.
// This is a convenience function for getting an Asynq logger.
// The adapter uses platform logger for asynq framework logs.
//
// Usage:
//
//	asynqLogger := logging.GetAsynqAdapter().GetAsynqLogger()
//	srv := asynq.NewServer(redisOpt, asynq.Config{
//	    Logger: asynqLogger,
//	})
func GetAsynqAdapter() *AsynqAdapter {
	return NewAsynqAdapter(nil)
}
