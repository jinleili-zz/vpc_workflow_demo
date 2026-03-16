// Package bootstrap provides unified initialization for nsp-common modules
// including logger, trace, auth, and saga components.
package bootstrap

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/paic/nsp-common/pkg/auth"
	"github.com/paic/nsp-common/pkg/logger"
	"github.com/paic/nsp-common/pkg/saga"
	"github.com/paic/nsp-common/pkg/trace"
	"workflow_qoder/internal/logging"
)

// Config holds the bootstrap configuration
type Config struct {
	// Service identification
	ServiceName string
	InstanceID  string
	
	// Logger settings
	LogLevel    string // debug, info, warn, error
	LogFormat   string // json, console
	Development bool
	
	// Database
	PostgresDSN string
	
	// Auth settings
	EnableAuth     bool
	Credentials    []*auth.Credential
	SkipAuthPaths  []string
	
	// SAGA settings
	EnableSaga      bool
	SagaWorkerCount int
}

// DefaultConfig returns a default configuration
func DefaultConfig(serviceName string) *Config {
	return &Config{
		ServiceName:     serviceName,
		InstanceID:      getInstanceID(),
		LogLevel:        getEnvOrDefault("LOG_LEVEL", "info"),
		LogFormat:       getEnvOrDefault("LOG_FORMAT", "json"),
		Development:     os.Getenv("DEVELOPMENT") == "true",
		PostgresDSN:     os.Getenv("POSTGRES_DSN"),
		EnableAuth:      os.Getenv("ENABLE_AUTH") != "false",
		EnableSaga:      true,
		SagaWorkerCount: 4,
		SkipAuthPaths:   []string{"/api/v1/health", "/api/v1/register/az", "/api/v1/heartbeat"},
	}
}

// Components holds initialized nsp-common components
type Components struct {
	Logger      logger.Logger
	Verifier    *auth.Verifier
	Signer      *auth.Signer
	SagaEngine  *saga.Engine
	TracedHTTP  *trace.TracedClient
	
	config      *Config
}

// Initialize bootstraps all nsp-common components
func Initialize(ctx context.Context, cfg *Config) (*Components, error) {
	c := &Components{
		config:      cfg,
	}
	
	// 1. Initialize Logger
	if err := c.initLogger(); err != nil {
		return nil, fmt.Errorf("failed to initialize logger: %w", err)
	}
	
	// 2. Initialize Auth (if enabled)
	if cfg.EnableAuth {
		if err := c.initAuth(); err != nil {
			return nil, fmt.Errorf("failed to initialize auth: %w", err)
		}
	}
	
	// 3. Initialize SAGA Engine (if enabled and DSN provided)
	if cfg.EnableSaga && cfg.PostgresDSN != "" {
		if err := c.initSaga(ctx); err != nil {
			return nil, fmt.Errorf("failed to initialize saga: %w", err)
		}
	}
	
	// 4. Initialize Traced HTTP Client
	c.TracedHTTP = trace.NewTracedClient(nil)
	
	logger.Platform().Info("nsp-common components initialized",
		"service", cfg.ServiceName,
		"instance", cfg.InstanceID,
		"auth_enabled", cfg.EnableAuth,
		"saga_enabled", cfg.EnableSaga && cfg.PostgresDSN != "")
	
	return c, nil
}

// initLogger initializes the logger module
func (c *Components) initLogger() error {
	var cfg *logger.Config
	
	if c.config.Development {
		cfg = logger.DevelopmentConfig(c.config.ServiceName)
	} else {
		cfg = logger.DefaultConfig(c.config.ServiceName)
	}
	
	// Override log level if specified
	if c.config.LogLevel != "" {
		switch c.config.LogLevel {
		case "debug":
			cfg.Level = logger.LevelDebug
		case "info":
			cfg.Level = logger.LevelInfo
		case "warn":
			cfg.Level = logger.LevelWarn
		case "error":
			cfg.Level = logger.LevelError
		}
	}
	
	if err := logger.Init(cfg); err != nil {
		return err
	}
	
	c.Logger = logger.GetLogger()
	
	// Setup third-party framework logging adapters
	logging.SetupAllAdapters()
	
	return nil
}

// initAuth initializes the auth module
func (c *Components) initAuth() error {
	// Load credentials from config or environment
	credentials := c.config.Credentials
	if len(credentials) == 0 {
		// Default credentials for development
		credentials = []*auth.Credential{
			{AccessKey: "top-nsp", SecretKey: getEnvOrDefault("TOP_NSP_SK", "top-nsp-secret-key"), Label: "Top NSP", Enabled: true},
			{AccessKey: "az-nsp", SecretKey: getEnvOrDefault("AZ_NSP_SK", "az-nsp-secret-key"), Label: "AZ NSP", Enabled: true},
			{AccessKey: "worker", SecretKey: getEnvOrDefault("WORKER_SK", "worker-secret-key"), Label: "Worker", Enabled: true},
		}
	}
	
	credStore := auth.NewMemoryStore(credentials)
	nonceStore := auth.NewMemoryNonceStore()
	
	c.Verifier = auth.NewVerifier(credStore, nonceStore, nil)
	
	// Create signer with this service's credentials
	ak := getEnvOrDefault("SERVICE_AK", c.config.ServiceName)
	sk := getEnvOrDefault("SERVICE_SK", c.config.ServiceName+"-secret-key")
	c.Signer = auth.NewSigner(ak, sk)
	
	return nil
}

// initSaga initializes the SAGA engine
func (c *Components) initSaga(ctx context.Context) error {
	sagaCfg := &saga.Config{
		DSN:         c.config.PostgresDSN,
		WorkerCount: c.config.SagaWorkerCount,
		InstanceID:  c.config.InstanceID,
	}
	
	engine, err := saga.NewEngine(sagaCfg)
	if err != nil {
		return err
	}
	
	// Run migrations for saga tables separately (via SQL migrations)
	// The saga store does not have a built-in Migrate method;
	// use external SQL migration files instead.
	
	// Start engine
	if err := engine.Start(ctx); err != nil {
		return err
	}
	
	c.SagaEngine = engine
	return nil
}

// SetupGinMiddlewares configures Gin with nsp-common middlewares
func (c *Components) SetupGinMiddlewares(r *gin.Engine) {
	// 1. Recovery middleware
	r.Use(gin.Recovery())
	
	// 2. Trace middleware
	r.Use(trace.TraceMiddleware(c.config.InstanceID))
	
	// 3. Logger middleware (with trace integration)
	r.Use(GinLoggerMiddleware())
	
	// 4. Auth middleware (if enabled)
	if c.config.EnableAuth && c.Verifier != nil {
		skipper := auth.NewSkipperByPath(c.config.SkipAuthPaths...)
		r.Use(auth.AKSKAuthMiddleware(c.Verifier, &auth.MiddlewareOption{
			Skipper: skipper,
		}))
	}
}

// GinLoggerMiddleware returns a Gin middleware that logs requests with trace context
func GinLoggerMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		
		c.Next()
		
		// Log with trace context using Access logger
		ctx := c.Request.Context()
		latency := time.Since(start)
		
		logger.Access().InfoContext(ctx, "http request",
			logger.FieldHTTPMethod, c.Request.Method,
			logger.FieldHTTPPath, path,
			logger.FieldHTTPStatus, c.Writer.Status(),
			logger.FieldHTTPLatency, latency.Milliseconds(),
			logger.FieldClientIP, c.ClientIP(),
		)
	}
}

// Shutdown gracefully shuts down all components
func (c *Components) Shutdown() {
	if c.SagaEngine != nil {
		c.SagaEngine.Stop()
	}
	logger.Sync()
}

// GetPostgresDB opens a PostgreSQL connection with the configured DSN
func (c *Components) GetPostgresDB() (*sql.DB, error) {
	if c.config.PostgresDSN == "" {
		return nil, fmt.Errorf("POSTGRES_DSN not configured")
	}
	
	db, err := sql.Open("postgres", c.config.PostgresDSN)
	if err != nil {
		return nil, err
	}
	
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	
	return db, nil
}

// Helper functions

func getInstanceID() string {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}
	return fmt.Sprintf("%s-%d", hostname, os.Getpid())
}

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
