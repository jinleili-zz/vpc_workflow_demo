// Package logger provides a unified logging module for NSP platform microservices.
// zap_logger.go implements the Logger interface using zap as the underlying engine
// and zapslog as the bridge to slog.Handler.
package logger

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/exp/zapslog"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

// zapLogger implements the Logger interface using zap.
type zapLogger struct {
	// zlogger is the zap.Logger for actual logging with proper caller skip
	zlogger *zap.Logger
	// slogger is the slog.Logger for Handler() interface compatibility
	slogger *slog.Logger
	// handler is the underlying slog.Handler
	handler slog.Handler
	// atomicLevel allows dynamic level changes
	atomicLevel zap.AtomicLevel
	// zapCore is the underlying zap core for sync operations
	zapCore zapcore.Core
	// config holds the original configuration
	config *Config
}

// newZapLogger creates a new zapLogger instance based on the provided configuration.
func newZapLogger(cfg *Config) (*zapLogger, error) {
	// Create atomic level for dynamic level changes
	atomicLevel := zap.NewAtomicLevelAt(parseZapLevel(cfg.Level))

	var core zapcore.Core

	// Check if using advanced multi-output configuration
	if len(cfg.Outputs) > 0 {
		core = createMultiOutputCore(cfg, atomicLevel)
	} else {
		// Simple mode: single encoder for all outputs
		encoder := createEncoder(cfg)
		writeSyncer := createWriteSyncer(cfg)
		core = zapcore.NewCore(encoder, writeSyncer, atomicLevel)
	}

	// Apply sampling if configured (Error level excluded from sampling)
	if cfg.Sampling != nil && cfg.Sampling.Initial > 0 {
		sampledCore := zapcore.NewSamplerWithOptions(
			core,
			time.Second,
			cfg.Sampling.Initial,
			cfg.Sampling.Thereafter,
		)
		core = &errorExcludeSamplerCore{
			Core:         sampledCore,
			originalCore: core,
		}
	}

	// Build zap logger options
	zapOpts := []zap.Option{
		zap.WithCaller(cfg.EnableCaller),
		zap.Fields(zap.String(FieldService, cfg.ServiceName)),
	}

	// Add caller skip to skip our wrapper functions
	if cfg.EnableCaller {
		zapOpts = append(zapOpts, zap.AddCallerSkip(2))
	}

	if cfg.EnableStackTrace {
		zapOpts = append(zapOpts, zap.AddStacktrace(zapcore.ErrorLevel))
	}

	// Create zap logger
	zlogger := zap.New(core, zapOpts...)

	// Create zapslog handler for Handler() interface compatibility
	handlerOpts := []zapslog.HandlerOption{
		zapslog.WithName(cfg.ServiceName),
		zapslog.WithCaller(false),
	}

	if cfg.EnableStackTrace {
		handlerOpts = append(handlerOpts, zapslog.AddStacktraceAt(slog.LevelError))
	}

	handler := zapslog.NewHandler(core, handlerOpts...)
	handlerWithAttrs := handler.WithAttrs([]slog.Attr{
		slog.String(FieldService, cfg.ServiceName),
	})
	slogger := slog.New(handlerWithAttrs)

	return &zapLogger{
		zlogger:     zlogger,
		slogger:     slogger,
		handler:     handlerWithAttrs,
		atomicLevel: atomicLevel,
		zapCore:     core,
		config:      cfg,
	}, nil
}

// createMultiOutputCore creates a tee core for multiple outputs with independent settings.
func createMultiOutputCore(cfg *Config, atomicLevel zap.AtomicLevel) zapcore.Core {
	var cores []zapcore.Core

	for _, output := range cfg.Outputs {
		// Determine format for this output
		format := output.Format
		if format == "" {
			format = cfg.Format
		}

		// Determine level for this output
		level := output.Level
		if level == "" {
			level = cfg.Level
		}

		// Create encoder for this output
		encoder := createEncoderWithFormat(cfg, format)

		// Create write syncer for this output
		var ws zapcore.WriteSyncer
		switch output.Type {
		case OutputTypeStdout:
			ws = zapcore.AddSync(os.Stdout)
		case OutputTypeStderr:
			ws = zapcore.AddSync(os.Stderr)
		case OutputTypeFile:
			rotation := output.Rotation
			if rotation == nil {
				rotation = DefaultRotationConfig()
			}
			ws = createFileWriter(output.Path, rotation)
		default:
			// Fallback to stdout
			ws = zapcore.AddSync(os.Stdout)
		}

		// Create level enabler for this output
		outputLevel := zap.NewAtomicLevelAt(parseZapLevel(level))

		// Create core for this output
		core := zapcore.NewCore(encoder, ws, outputLevel)
		cores = append(cores, core)
	}

	if len(cores) == 1 {
		return cores[0]
	}

	return zapcore.NewTee(cores...)
}

// createEncoderWithFormat creates encoder with specific format.
func createEncoderWithFormat(cfg *Config, format Format) zapcore.Encoder {
	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        "timestamp",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		FunctionKey:    zapcore.OmitKey,
		MessageKey:     "message",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.MillisDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	if !cfg.EnableCaller {
		encoderConfig.CallerKey = zapcore.OmitKey
	}

	if cfg.Development {
		encoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		return zapcore.NewConsoleEncoder(encoderConfig)
	}

	if format == FormatConsole {
		encoderConfig.EncodeLevel = zapcore.CapitalLevelEncoder
		return zapcore.NewConsoleEncoder(encoderConfig)
	}

	return zapcore.NewJSONEncoder(encoderConfig)
}

// errorExcludeSamplerCore wraps a sampler core to exclude error level from sampling.
type errorExcludeSamplerCore struct {
	zapcore.Core
	originalCore zapcore.Core
}

// Check implements zapcore.Core.
func (c *errorExcludeSamplerCore) Check(entry zapcore.Entry, checked *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if entry.Level >= zapcore.ErrorLevel {
		return c.originalCore.Check(entry, checked)
	}
	return c.Core.Check(entry, checked)
}

// With implements zapcore.Core.
func (c *errorExcludeSamplerCore) With(fields []zapcore.Field) zapcore.Core {
	return &errorExcludeSamplerCore{
		Core:         c.Core.With(fields),
		originalCore: c.originalCore.With(fields),
	}
}

// parseZapLevel converts string level to zapcore.Level.
func parseZapLevel(level Level) zapcore.Level {
	switch level {
	case LevelDebug:
		return zapcore.DebugLevel
	case LevelInfo:
		return zapcore.InfoLevel
	case LevelWarn:
		return zapcore.WarnLevel
	case LevelError:
		return zapcore.ErrorLevel
	default:
		return zapcore.InfoLevel
	}
}

// levelFromZap converts zapcore.Level to string level.
func levelFromZap(level zapcore.Level) string {
	switch level {
	case zapcore.DebugLevel:
		return string(LevelDebug)
	case zapcore.InfoLevel:
		return string(LevelInfo)
	case zapcore.WarnLevel:
		return string(LevelWarn)
	case zapcore.ErrorLevel:
		return string(LevelError)
	default:
		return string(LevelInfo)
	}
}

// createEncoder creates the appropriate encoder based on config.
func createEncoder(cfg *Config) zapcore.Encoder {
	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        "timestamp",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		FunctionKey:    zapcore.OmitKey,
		MessageKey:     "message",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.MillisDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	if !cfg.EnableCaller {
		encoderConfig.CallerKey = zapcore.OmitKey
	}

	if cfg.Development {
		encoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
		return zapcore.NewConsoleEncoder(encoderConfig)
	}

	if cfg.Format == FormatConsole {
		encoderConfig.EncodeLevel = zapcore.CapitalLevelEncoder
		return zapcore.NewConsoleEncoder(encoderConfig)
	}

	return zapcore.NewJSONEncoder(encoderConfig)
}

// createWriteSyncer creates write syncer based on output paths.
func createWriteSyncer(cfg *Config) zapcore.WriteSyncer {
	var writers []zapcore.WriteSyncer

	for _, path := range cfg.OutputPaths {
		switch strings.ToLower(path) {
		case "stdout":
			writers = append(writers, zapcore.AddSync(os.Stdout))
		case "stderr":
			writers = append(writers, zapcore.AddSync(os.Stderr))
		default:
			writers = append(writers, createFileWriter(path, cfg.Rotation))
		}
	}

	if len(writers) == 0 {
		return zapcore.AddSync(os.Stdout)
	}

	if len(writers) == 1 {
		return writers[0]
	}

	return zapcore.NewMultiWriteSyncer(writers...)
}

// createFileWriter creates a file writer with rotation support using lumberjack.
func createFileWriter(filename string, rotation *RotationConfig) zapcore.WriteSyncer {
	if rotation == nil {
		rotation = DefaultRotationConfig()
	}

	// Apply defaults for zero values
	maxSize := rotation.MaxSize
	if maxSize <= 0 {
		maxSize = 100
	}
	maxBackups := rotation.MaxBackups
	if maxBackups <= 0 {
		maxBackups = 7
	}
	maxAge := rotation.MaxAge
	if maxAge <= 0 {
		maxAge = 30
	}

	lj := &lumberjack.Logger{
		Filename:   filename,
		MaxSize:    maxSize,
		MaxBackups: maxBackups,
		MaxAge:     maxAge,
		Compress:   rotation.Compress,
		LocalTime:  rotation.LocalTime,
	}

	return zapcore.AddSync(lj)
}

// argsToZapFields converts logger args to zap fields.
func argsToZapFields(args ...any) []zap.Field {
	if len(args) == 0 {
		return nil
	}

	fields := make([]zap.Field, 0, len(args)/2+1)
	for i := 0; i < len(args); i += 2 {
		if i+1 < len(args) {
			key, ok := args[i].(string)
			if !ok {
				key = ""
			}
			fields = append(fields, zap.Any(key, args[i+1]))
		}
	}
	return fields
}

// Debug implements Logger.
func (l *zapLogger) Debug(msg string, args ...any) {
	l.zlogger.Debug(msg, argsToZapFields(args...)...)
}

// Info implements Logger.
func (l *zapLogger) Info(msg string, args ...any) {
	l.zlogger.Info(msg, argsToZapFields(args...)...)
}

// Warn implements Logger.
func (l *zapLogger) Warn(msg string, args ...any) {
	l.zlogger.Warn(msg, argsToZapFields(args...)...)
}

// Error implements Logger.
func (l *zapLogger) Error(msg string, args ...any) {
	l.zlogger.Error(msg, argsToZapFields(args...)...)
}

// Fatal implements Logger.
func (l *zapLogger) Fatal(msg string, args ...any) {
	l.zlogger.Error(msg, argsToZapFields(args...)...)
	l.Sync()
	exitFunc(1)
}

// contextToZapFields extracts trace/span from context and converts to zap fields.
func contextToZapFields(ctx context.Context) []zap.Field {
	var fields []zap.Field
	if traceID := TraceIDFromContext(ctx); traceID != "" {
		fields = append(fields, zap.String(FieldTraceID, traceID))
	}
	if spanID := SpanIDFromContext(ctx); spanID != "" {
		fields = append(fields, zap.String(FieldSpanID, spanID))
	}
	return fields
}

// DebugContext implements Logger.
func (l *zapLogger) DebugContext(ctx context.Context, msg string, args ...any) {
	fields := contextToZapFields(ctx)
	fields = append(fields, argsToZapFields(args...)...)
	l.zlogger.Debug(msg, fields...)
}

// InfoContext implements Logger.
func (l *zapLogger) InfoContext(ctx context.Context, msg string, args ...any) {
	fields := contextToZapFields(ctx)
	fields = append(fields, argsToZapFields(args...)...)
	l.zlogger.Info(msg, fields...)
}

// WarnContext implements Logger.
func (l *zapLogger) WarnContext(ctx context.Context, msg string, args ...any) {
	fields := contextToZapFields(ctx)
	fields = append(fields, argsToZapFields(args...)...)
	l.zlogger.Warn(msg, fields...)
}

// ErrorContext implements Logger.
func (l *zapLogger) ErrorContext(ctx context.Context, msg string, args ...any) {
	fields := contextToZapFields(ctx)
	fields = append(fields, argsToZapFields(args...)...)
	l.zlogger.Error(msg, fields...)
}

// With implements Logger.
func (l *zapLogger) With(args ...any) Logger {
	newZlogger := l.zlogger.With(argsToZapFields(args...)...)
	return &zapLogger{
		zlogger:     newZlogger,
		slogger:     l.slogger.With(args...),
		handler:     l.slogger.With(args...).Handler(),
		atomicLevel: l.atomicLevel,
		zapCore:     l.zapCore,
		config:      l.config,
	}
}

// WithGroup implements Logger.
func (l *zapLogger) WithGroup(name string) Logger {
	return &zapLogger{
		zlogger:     l.zlogger,
		slogger:     l.slogger.WithGroup(name),
		handler:     l.slogger.WithGroup(name).Handler(),
		atomicLevel: l.atomicLevel,
		zapCore:     l.zapCore,
		config:      l.config,
	}
}

// WithContext implements Logger.
func (l *zapLogger) WithContext(ctx context.Context) Logger {
	ctxFields := extractContextFields(ctx)
	if len(ctxFields) == 0 {
		return l
	}
	return l.With(ctxFields...)
}

// Sync implements Logger.
func (l *zapLogger) Sync() error {
	// Sync zap logger first
	_ = l.zlogger.Sync()

	// Try to sync the core
	if syncer, ok := l.zapCore.(interface{ Sync() error }); ok {
		err := syncer.Sync()
		if err != nil && !isIgnorableSyncError(err) {
			return err
		}
	}

	// Try to sync through the handler
	if syncer, ok := l.handler.(interface{ Sync() error }); ok {
		err := syncer.Sync()
		if err != nil && !isIgnorableSyncError(err) {
			return err
		}
	}

	// Try to sync stdout/stderr
	_ = os.Stdout.Sync()
	_ = os.Stderr.Sync()

	return nil
}

// isIgnorableSyncError returns true for common non-fatal sync errors.
func isIgnorableSyncError(err error) bool {
	if err == nil {
		return true
	}
	errStr := err.Error()
	return strings.Contains(errStr, "invalid argument") ||
		strings.Contains(errStr, "inappropriate ioctl") ||
		strings.Contains(errStr, "bad file descriptor")
}

// SetLevel implements Logger.
func (l *zapLogger) SetLevel(level string) error {
	lvl := Level(strings.ToLower(level))
	switch lvl {
	case LevelDebug, LevelInfo, LevelWarn, LevelError:
		l.atomicLevel.SetLevel(parseZapLevel(lvl))
		return nil
	default:
		return ErrInvalidLevel
	}
}

// GetLevel implements Logger.
func (l *zapLogger) GetLevel() string {
	return levelFromZap(l.atomicLevel.Level())
}

// Handler implements Logger.
func (l *zapLogger) Handler() slog.Handler {
	return l.handler
}

// syncableCore wraps a zapcore.Core to provide Sync functionality.
type syncableCore struct {
	zapcore.Core
	writers []io.Writer
}

// Sync syncs all underlying writers.
func (c *syncableCore) Sync() error {
	for _, w := range c.writers {
		if syncer, ok := w.(interface{ Sync() error }); ok {
			_ = syncer.Sync()
		}
	}
	return nil
}
