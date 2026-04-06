// Package logger provides a unified logging module for NSP platform microservices.
// category.go defines log categories and provides separate loggers for different log types.
package logger

import (
	"errors"
	"path/filepath"
	"sync"
)

// LogCategory represents a log category type.
type LogCategory string

// Log category constants.
const (
	// CategoryAccess is for HTTP access logs (request/response logs).
	// These logs record all incoming HTTP requests with method, path, status, latency, etc.
	// Typically output to: access.log
	CategoryAccess LogCategory = "access"

	// CategoryPlatform is for platform/framework logs.
	// These logs come from infrastructure components like asynq, saga, redis, database, etc.
	// Typically output to: platform.log
	CategoryPlatform LogCategory = "platform"

	// CategoryBusiness is for business logic logs.
	// These logs come from application business code (orchestrators, handlers, services).
	// Typically output to: app.log or business.log
	CategoryBusiness LogCategory = "business"
)

// CategoryConfig holds configuration for a single log category.
type CategoryConfig struct {
	// Level sets the minimum log level for this category.
	// Default: "info"
	Level Level `json:"level" yaml:"level"`

	// Format sets the log format for this category.
	// Default: "json"
	Format Format `json:"format" yaml:"format"`

	// Outputs defines output destinations for this category.
	// If empty, uses stdout.
	Outputs []OutputConfig `json:"outputs" yaml:"outputs"`

	// Sampling configures log sampling for this category.
	// Set to nil to disable sampling.
	Sampling *SamplingConfig `json:"sampling" yaml:"sampling"`

	// EnableCaller enables logging of caller file name and line number.
	// Default: true for business, false for access/platform
	EnableCaller bool `json:"enable_caller" yaml:"enable_caller"`

	// EnableStackTrace enables automatic stack trace for error level logs.
	// Default: true for business, false for access/platform
	EnableStackTrace bool `json:"enable_stack_trace" yaml:"enable_stack_trace"`
}

// MultiCategoryConfig holds configuration for all log categories.
type MultiCategoryConfig struct {
	// ServiceName is the name of the microservice (required).
	ServiceName string `json:"service_name" yaml:"service_name"`

	// Development enables development mode for all categories.
	Development bool `json:"development" yaml:"development"`

	// Access configures the access log category.
	// If nil, access logs use default configuration.
	Access *CategoryConfig `json:"access" yaml:"access"`

	// Platform configures the platform log category.
	// If nil, platform logs use default configuration.
	Platform *CategoryConfig `json:"platform" yaml:"platform"`

	// Business configures the business log category.
	// If nil, business logs use default configuration.
	Business *CategoryConfig `json:"business" yaml:"business"`
}

// categoryManager manages multiple category loggers.
type categoryManager struct {
	serviceName string
	access      Logger
	platform    Logger
	business    Logger
	mu          sync.RWMutex
}

// Global category manager instance.
var (
	catManager   *categoryManager
	catManagerMu sync.RWMutex
)

// InitMultiCategory initializes the multi-category logging system.
// This should be called once at application startup as an alternative to Init().
// If you use Init(), all categories share the same logger.
// If you use InitMultiCategory(), each category can have independent configuration.
//
// Example:
//
//	cfg := &logger.MultiCategoryConfig{
//	    ServiceName: "my-service",
//	    Access: &logger.CategoryConfig{
//	        Level:  logger.LevelInfo,
//	        Format: logger.FormatJSON,
//	        Outputs: []logger.OutputConfig{
//	            {Type: logger.OutputTypeFile, Path: "/var/log/access.log"},
//	        },
//	    },
//	    Platform: &logger.CategoryConfig{
//	        Level: logger.LevelWarn,
//	        Outputs: []logger.OutputConfig{
//	            {Type: logger.OutputTypeFile, Path: "/var/log/platform.log"},
//	        },
//	    },
//	    Business: &logger.CategoryConfig{
//	        Level: logger.LevelInfo,
//	        Outputs: []logger.OutputConfig{
//	            {Type: logger.OutputTypeFile, Path: "/var/log/app.log"},
//	            {Type: logger.OutputTypeStdout},
//	        },
//	    },
//	}
//	if err := logger.InitMultiCategory(cfg); err != nil {
//	    log.Fatal(err)
//	}
func InitMultiCategory(cfg *MultiCategoryConfig) error {
	if cfg == nil {
		return ErrServiceNameRequired
	}
	if cfg.ServiceName == "" {
		return ErrServiceNameRequired
	}

	manager := &categoryManager{
		serviceName: cfg.ServiceName,
	}

	// Initialize access logger with category field
	accessCfg := buildCategoryLoggerConfig(cfg, CategoryAccess)
	accessLogger, err := newZapLogger(accessCfg)
	if err != nil {
		return err
	}
	manager.access = accessLogger.With(FieldCategory, string(CategoryAccess))

	// Initialize platform logger with category field
	platformCfg := buildCategoryLoggerConfig(cfg, CategoryPlatform)
	platformLogger, err := newZapLogger(platformCfg)
	if err != nil {
		return err
	}
	manager.platform = platformLogger.With(FieldCategory, string(CategoryPlatform))

	// Initialize business logger with category field
	businessCfg := buildCategoryLoggerConfig(cfg, CategoryBusiness)
	businessLogger, err := newZapLogger(businessCfg)
	if err != nil {
		return err
	}
	manager.business = businessLogger.With(FieldCategory, string(CategoryBusiness))

	catManagerMu.Lock()
	catManager = manager
	catManagerMu.Unlock()

	// Also initialize global logger with business logger (with category field)
	// so that GetLogger() returns a logger consistent with Business()
	globalMu.Lock()
	globalLogger = manager.business
	initialized = true
	globalMu.Unlock()

	return nil
}

// buildCategoryLoggerConfig builds a Config from MultiCategoryConfig for a specific category.
func buildCategoryLoggerConfig(cfg *MultiCategoryConfig, category LogCategory) *Config {
	var catCfg *CategoryConfig
	switch category {
	case CategoryAccess:
		catCfg = cfg.Access
	case CategoryPlatform:
		catCfg = cfg.Platform
	case CategoryBusiness:
		catCfg = cfg.Business
	}

	// Apply defaults based on category
	result := &Config{
		ServiceName: cfg.ServiceName,
		Development: cfg.Development,
	}

	if catCfg != nil {
		result.Level = catCfg.Level
		result.Format = catCfg.Format
		result.Outputs = catCfg.Outputs
		result.Sampling = catCfg.Sampling
		result.EnableCaller = catCfg.EnableCaller
		result.EnableStackTrace = catCfg.EnableStackTrace
	}

	// Apply category-specific defaults
	switch category {
	case CategoryAccess:
		if result.Level == "" {
			result.Level = LevelInfo
		}
		if result.Format == "" {
			result.Format = FormatJSON
		}
		// Access logs: EnableCaller and EnableStackTrace default to false (zero value)
	case CategoryPlatform:
		if result.Level == "" {
			result.Level = LevelInfo
		}
		if result.Format == "" {
			result.Format = FormatJSON
		}
		// Platform logs: EnableCaller and EnableStackTrace default to false (zero value)
	case CategoryBusiness:
		if result.Level == "" {
			result.Level = LevelInfo
		}
		if result.Format == "" {
			result.Format = FormatJSON
		}
		// Business logs benefit from caller info
		if catCfg == nil {
			result.EnableCaller = true
			result.EnableStackTrace = true
		}
	}

	// Default output to stdout if not specified
	if len(result.Outputs) == 0 && len(result.OutputPaths) == 0 {
		result.OutputPaths = []string{"stdout"}
	}

	return result
}

// getCategoryManager returns the category manager, or nil if not initialized.
func getCategoryManager() *categoryManager {
	catManagerMu.RLock()
	defer catManagerMu.RUnlock()
	return catManager
}

// Access returns the access log logger.
// If multi-category logging is not initialized, returns the global logger.
func Access() Logger {
	if mgr := getCategoryManager(); mgr != nil {
		return mgr.access
	}
	return GetLogger()
}

// Platform returns the platform log logger.
// If multi-category logging is not initialized, returns the global logger.
func Platform() Logger {
	if mgr := getCategoryManager(); mgr != nil {
		return mgr.platform
	}
	return GetLogger()
}

// Business returns the business log logger.
// If multi-category logging is not initialized, returns the global logger.
func Business() Logger {
	if mgr := getCategoryManager(); mgr != nil {
		return mgr.business
	}
	return GetLogger()
}

// ForCategory returns the logger for a specific category.
// If multi-category logging is not initialized, returns the global logger.
func ForCategory(category LogCategory) Logger {
	switch category {
	case CategoryAccess:
		return Access()
	case CategoryPlatform:
		return Platform()
	case CategoryBusiness:
		return Business()
	default:
		return GetLogger()
	}
}

// SyncAll flushes all category loggers.
// This should be called before program exit.
// If multiple loggers fail to sync, all errors are returned via errors.Join.
func SyncAll() error {
	mgr := getCategoryManager()
	if mgr == nil {
		return Sync()
	}

	var errs []error
	if err := mgr.access.Sync(); err != nil {
		errs = append(errs, err)
	}
	if err := mgr.platform.Sync(); err != nil {
		errs = append(errs, err)
	}
	if err := mgr.business.Sync(); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

// Biz is an alias for Business() for shorter code.
func Biz() Logger {
	return Business()
}

// ============================================================================
// Preset Configurations
// ============================================================================

// DefaultMultiCategoryConfig returns a default multi-category configuration.
// All categories log to stdout with JSON format.
//
// Parameters:
//   - serviceName: The name of the microservice (required)
func DefaultMultiCategoryConfig(serviceName string) *MultiCategoryConfig {
	return &MultiCategoryConfig{
		ServiceName: serviceName,
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
			Level:        LevelInfo,
			Format:       FormatJSON,
			EnableCaller: true,
			Outputs: []OutputConfig{
				{Type: OutputTypeStdout, Format: FormatJSON},
			},
		},
	}
}

// FileMultiCategoryConfig returns a configuration where each category logs to a separate file.
//
// Parameters:
//   - serviceName: The name of the microservice (required)
//   - logDir: The directory for log files (e.g., "/var/log/myapp")
func FileMultiCategoryConfig(serviceName, logDir string) *MultiCategoryConfig {
	return &MultiCategoryConfig{
		ServiceName: serviceName,
		Access: &CategoryConfig{
			Level:  LevelInfo,
			Format: FormatJSON,
			Outputs: []OutputConfig{
				{
					Type:     OutputTypeFile,
					Path:     filepath.Join(logDir, "access.log"),
					Format:   FormatJSON,
					Rotation: DefaultRotationConfig(),
				},
			},
		},
		Platform: &CategoryConfig{
			Level:  LevelInfo,
			Format: FormatJSON,
			Outputs: []OutputConfig{
				{
					Type:     OutputTypeFile,
					Path:     filepath.Join(logDir, "platform.log"),
					Format:   FormatJSON,
					Rotation: DefaultRotationConfig(),
				},
			},
		},
		Business: &CategoryConfig{
			Level:            LevelInfo,
			Format:           FormatJSON,
			EnableCaller:     true,
			EnableStackTrace: true,
			Outputs: []OutputConfig{
				{
					Type:     OutputTypeFile,
					Path:     filepath.Join(logDir, "app.log"),
					Format:   FormatJSON,
					Rotation: DefaultRotationConfig(),
				},
			},
		},
	}
}

// DevelopmentMultiCategoryConfig returns a development-friendly multi-category configuration.
// All categories log to stdout with console format.
//
// Parameters:
//   - serviceName: The name of the microservice (required)
func DevelopmentMultiCategoryConfig(serviceName string) *MultiCategoryConfig {
	return &MultiCategoryConfig{
		ServiceName: serviceName,
		Development: true,
		Access: &CategoryConfig{
			Level:  LevelDebug,
			Format: FormatConsole,
			Outputs: []OutputConfig{
				{Type: OutputTypeStdout, Format: FormatConsole},
			},
		},
		Platform: &CategoryConfig{
			Level:  LevelDebug,
			Format: FormatConsole,
			Outputs: []OutputConfig{
				{Type: OutputTypeStdout, Format: FormatConsole},
			},
		},
		Business: &CategoryConfig{
			Level:            LevelDebug,
			Format:           FormatConsole,
			EnableCaller:     true,
			EnableStackTrace: true,
			Outputs: []OutputConfig{
				{Type: OutputTypeStdout, Format: FormatConsole},
			},
		},
	}
}

// resetCategoryManager resets the category manager for testing.
func resetCategoryManager() {
	catManagerMu.Lock()
	defer catManagerMu.Unlock()
	catManager = nil
}
