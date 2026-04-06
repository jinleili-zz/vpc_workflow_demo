// Package logger provides a unified logging module for NSP platform microservices.
// config.go defines configuration structures and preset constructors.
package logger

// Level represents log level as a string type.
// Valid values: "debug", "info", "warn", "error"
type Level string

// Log level constants.
const (
	LevelDebug Level = "debug"
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

// Format represents log output format.
// Valid values: "json", "console"
type Format string

// Log format constants.
const (
	FormatJSON    Format = "json"
	FormatConsole Format = "console"
)

// OutputType represents the type of output destination.
type OutputType string

// Output type constants.
const (
	OutputTypeStdout OutputType = "stdout"
	OutputTypeStderr OutputType = "stderr"
	OutputTypeFile   OutputType = "file"
)

// RotationConfig defines log file rotation settings.
// Uses lumberjack under the hood for log rotation.
type RotationConfig struct {
	// MaxSize is the maximum size in megabytes of the log file before rotation.
	// Default: 100 MB
	MaxSize int `json:"max_size" yaml:"max_size"`

	// MaxBackups is the maximum number of old log files to retain.
	// Default: 7
	MaxBackups int `json:"max_backups" yaml:"max_backups"`

	// MaxAge is the maximum number of days to retain old log files.
	// Default: 30 days
	MaxAge int `json:"max_age" yaml:"max_age"`

	// Compress determines if the rotated log files should be gzip compressed.
	// Default: true
	Compress bool `json:"compress" yaml:"compress"`

	// LocalTime determines if the time used for formatting backup file names
	// is the local time. If false, UTC time is used.
	// Default: true
	LocalTime bool `json:"local_time" yaml:"local_time"`
}

// DefaultRotationConfig returns default rotation settings.
func DefaultRotationConfig() *RotationConfig {
	return &RotationConfig{
		MaxSize:    100,
		MaxBackups: 7,
		MaxAge:     30,
		Compress:   true,
		LocalTime:  true,
	}
}

// OutputConfig defines a single output destination with its own settings.
type OutputConfig struct {
	// Type is the output type: "stdout", "stderr", or "file"
	Type OutputType `json:"type" yaml:"type"`

	// Path is the file path when Type is "file".
	// Ignored for stdout/stderr.
	Path string `json:"path" yaml:"path"`

	// Rotation configures log file rotation.
	// Only applies when Type is "file".
	// If nil, default rotation settings are used.
	Rotation *RotationConfig `json:"rotation" yaml:"rotation"`

	// Level sets the minimum log level for this output.
	// If empty, uses the global level from Config.
	// This allows different levels for different outputs.
	Level Level `json:"level" yaml:"level"`

	// Format sets the log format for this output.
	// If empty, uses the global format from Config.
	// This allows different formats for different outputs (e.g., JSON for file, console for stdout).
	Format Format `json:"format" yaml:"format"`
}

// SamplingConfig defines log sampling settings for high-throughput scenarios.
// Sampling reduces log volume while maintaining visibility.
type SamplingConfig struct {
	// Initial is the number of log entries per second to fully output
	// before sampling kicks in.
	// Default: 100
	Initial int `json:"initial" yaml:"initial"`

	// Thereafter is the sampling rate after Initial is exceeded.
	// Every Nth log entry will be output.
	// Default: 10 (meaning 1 in 10 logs will be output)
	Thereafter int `json:"thereafter" yaml:"thereafter"`
}

// Config holds all configuration options for the logger.
type Config struct {
	// Level sets the minimum log level.
	// Valid values: "debug", "info", "warn", "error"
	// Default: "info"
	Level Level `json:"level" yaml:"level"`

	// Format sets the log output format.
	// Valid values: "json", "console"
	// Default: "json"
	Format Format `json:"format" yaml:"format"`

	// ServiceName is the name of the microservice using this logger.
	// This field is required and will be included in every log entry.
	ServiceName string `json:"service_name" yaml:"service_name"`

	// OutputPaths is a list of output destinations (simple mode).
	// Supported values: "stdout", "stderr", or file paths.
	// Default: ["stdout"]
	// Note: For advanced configuration, use Outputs instead.
	OutputPaths []string `json:"output_paths" yaml:"output_paths"`

	// Outputs is a list of output configurations (advanced mode).
	// Each output can have its own rotation, level, and format settings.
	// If both OutputPaths and Outputs are specified, Outputs takes precedence.
	Outputs []OutputConfig `json:"outputs" yaml:"outputs"`

	// Rotation configures default log file rotation for OutputPaths mode.
	// Only applies when OutputPaths includes file paths.
	Rotation *RotationConfig `json:"rotation" yaml:"rotation"`

	// EnableCaller enables logging of caller file name and line number.
	// Default: true
	EnableCaller bool `json:"enable_caller" yaml:"enable_caller"`

	// EnableStackTrace enables automatic stack trace for error level logs.
	// Default: true
	EnableStackTrace bool `json:"enable_stack_trace" yaml:"enable_stack_trace"`

	// Development enables development mode.
	// In development mode:
	// - Console output uses colorized format
	// - Panics print full stack trace
	// - DPanic level causes panic
	// Default: false
	Development bool `json:"development" yaml:"development"`

	// Sampling configures log sampling for high-throughput scenarios.
	// Set to nil to disable sampling.
	Sampling *SamplingConfig `json:"sampling" yaml:"sampling"`
}

// DefaultConfig returns a production-ready configuration with sensible defaults.
// This configuration is optimized for production environments:
// - JSON format for machine parsing
// - Info level to reduce log volume
// - Caller info enabled for debugging
// - Stack trace enabled for errors
// - Sampling enabled to handle high throughput
//
// Parameters:
//   - serviceName: The name of the microservice (required)
//
// Returns:
//   - *Config: A new Config instance with production defaults
func DefaultConfig(serviceName string) *Config {
	return &Config{
		Level:            LevelInfo,
		Format:           FormatJSON,
		ServiceName:      serviceName,
		OutputPaths:      []string{"stdout"},
		EnableCaller:     true,
		EnableStackTrace: true,
		Development:      false,
		Rotation:         DefaultRotationConfig(),
		Sampling: &SamplingConfig{
			Initial:    100,
			Thereafter: 10,
		},
	}
}

// DevelopmentConfig returns a development-friendly configuration.
// This configuration is optimized for local development:
// - Console format for human readability
// - Debug level for verbose output
// - Development mode enabled for colorized output
// - No sampling to see all logs
//
// Parameters:
//   - serviceName: The name of the microservice (required)
//
// Returns:
//   - *Config: A new Config instance with development defaults
func DevelopmentConfig(serviceName string) *Config {
	return &Config{
		Level:            LevelDebug,
		Format:           FormatConsole,
		ServiceName:      serviceName,
		OutputPaths:      []string{"stdout"},
		EnableCaller:     true,
		EnableStackTrace: true,
		Development:      true,
		Rotation:         nil, // No rotation in development
		Sampling:         nil, // No sampling in development
	}
}

// MultiOutputConfig returns a configuration with multiple outputs.
// This is useful when you need:
// - Console output for development/debugging
// - File output for persistence and log aggregation
//
// Parameters:
//   - serviceName: The name of the microservice (required)
//   - logFile: The path to the log file
//
// Returns:
//   - *Config: A new Config instance with multi-output configuration
func MultiOutputConfig(serviceName string, logFile string) *Config {
	return &Config{
		Level:       LevelInfo,
		Format:      FormatJSON,
		ServiceName: serviceName,
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
		EnableCaller:     true,
		EnableStackTrace: true,
		Development:      false,
		Sampling: &SamplingConfig{
			Initial:    100,
			Thereafter: 10,
		},
	}
}

// Validate validates the configuration and returns an error if invalid.
func (c *Config) Validate() error {
	if c.ServiceName == "" {
		return ErrServiceNameRequired
	}

	switch c.Level {
	case LevelDebug, LevelInfo, LevelWarn, LevelError, "":
		// Valid levels
	default:
		return ErrInvalidLevel
	}

	switch c.Format {
	case FormatJSON, FormatConsole, "":
		// Valid formats
	default:
		return ErrInvalidFormat
	}

	// Validate Outputs if specified
	for _, output := range c.Outputs {
		if output.Type == OutputTypeFile && output.Path == "" {
			return ErrInvalidFormat
		}
	}

	return nil
}

// applyDefaults applies default values to empty fields.
func (c *Config) applyDefaults() {
	if c.Level == "" {
		c.Level = LevelInfo
	}
	if c.Format == "" {
		c.Format = FormatJSON
	}
	if len(c.OutputPaths) == 0 && len(c.Outputs) == 0 {
		c.OutputPaths = []string{"stdout"}
	}
}
