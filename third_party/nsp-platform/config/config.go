// Copyright 2026 NSP Authors. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

// Package config provides a unified configuration management SDK
// that encapsulates spf13/viper as the underlying implementation.
//
// Business services only depend on the Loader interface and Option struct,
// without directly depending on viper. This allows switching the
// underlying implementation in the future by only modifying the New function.
package config

// Loader is the core interface for configuration loader.
// Business code should only depend on this interface.
type Loader interface {
	// Load loads the configuration file and unmarshals it into target (must be a pointer).
	// Each call will re-read the file content.
	Load(target any) error

	// OnChange registers a configuration change callback.
	//
	// After the configuration source changes and is successfully reloaded,
	// all registered callbacks are triggered in registration order.
	//
	// The apply function passed to each callback is provided by the wrapper layer.
	// Calling apply(&cfg) deserializes the latest configuration into cfg.
	// Business code does not need to know whether the underlying source is a
	// watched file, a configuration center push (Nacos/Apollo), or any other
	// mechanism – the wrapper layer handles the details.
	//
	// Callbacks are executed sequentially in a single goroutine.
	// A panic in one callback is recovered so that subsequent callbacks still run.
	//
	// If Watch=false, registering a callback does not error, but it will never
	// be triggered.
	OnChange(fn func(apply func(any) error))

	// Close stops configuration watching and releases associated resources
	// (file descriptors, background goroutines, etc.).
	// Safe to call multiple times; subsequent calls are no-ops.
	// Should be called during graceful shutdown and at the end of test cases.
	Close()
}

// Option contains configuration options for creating a Loader.
type Option struct {
	// ConfigFile is the full path to the configuration file, e.g. "./config/config.yaml".
	// Mutually exclusive with ConfigName+ConfigPaths. ConfigFile has higher priority.
	ConfigFile string

	// ConfigName is the configuration file name (without extension), e.g. "config".
	// Must be used together with ConfigPaths.
	ConfigName string

	// ConfigPaths is a list of configuration file search paths.
	// Searched in order until the first matching file is found.
	ConfigPaths []string

	// ConfigType is the configuration file format, e.g. "yaml", "json", "toml".
	// If empty, determined automatically by file extension.
	ConfigType string

	// Defaults are default values for configuration items.
	// Key supports dot notation, e.g. "server.port".
	// Takes effect when neither the configuration file nor environment variables provide the item.
	Defaults map[string]any

	// Watch enables hot reloading.
	// When true, monitors configuration file changes, automatically reloads and triggers OnChange callbacks.
	Watch bool

	// EnvPrefix is the environment variable prefix.
	// When non-empty, automatically binds environment variables.
	// E.g. with prefix "NSP", environment variable NSP_SERVER_PORT overrides configuration item server.port.
	// Environment variable matching is case-insensitive.
	EnvPrefix string
}

// New creates a Loader instance, currently returns the viper implementation.
// This is the only place that depends on the specific implementation.
// To switch libraries, only this function needs to be modified.
func New(opt Option) (Loader, error) {
	loader, err := newViperLoader(opt)
	if err != nil {
		return nil, err
	}

	return loader, nil
}

// Example usage:
/*
type ServerConfig struct {
    Host string `mapstructure:"host"`
    Port int    `mapstructure:"port"`
}

type AppConfig struct {
    Server ServerConfig `mapstructure:"server"`
    Debug  bool         `mapstructure:"debug"`
}

func main() {
    loader, err := config.New(config.Option{
        ConfigFile: "./config/config.yaml",
        Watch:      true,
        EnvPrefix:  "NSP",
        Defaults: map[string]any{
            "server.port": 8080,
            "debug":       false,
        },
    })
    if err != nil {
        panic(err)
    }
    defer loader.Close()

    var cfg AppConfig
    if err := loader.Load(&cfg); err != nil {
        panic(err)
    }

    // Register hot-reload callback.
    // apply is provided by the wrapper layer; calling apply(&newCfg) deserializes
    // the latest configuration regardless of whether the source is a file,
    // Nacos, Apollo, or any other backend.
    loader.OnChange(func(apply func(target any) error) {
        var newCfg AppConfig
        if err := apply(&newCfg); err != nil {
            // New configuration parsing failed, log error and continue using old configuration
            return
        }
        cfg = newCfg
    })

    // Business code uses cfg, without calling any viper APIs directly
}
*/
