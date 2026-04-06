// Copyright 2026 NSP Authors. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package config

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/viper"
)

// viperLoader implements the Loader interface using spf13/viper as the underlying library.
type viperLoader struct {
	v         *viper.Viper            // viper instance
	mu        sync.RWMutex            // protects callbacks list and viper internal state
	callbacks []func(func(any) error) // registered change callbacks
	done      chan struct{}            // signal channel for Close
	closeOnce sync.Once               // ensures Close is idempotent
	watchOnce sync.Once               // ensures startWatching is called at most once
	watch     bool                    // whether file watching is enabled
}

// newViperLoader creates a new viperLoader instance.
func newViperLoader(opt Option) (*viperLoader, error) {
	v := viper.New()

	// Configure file path
	if opt.ConfigFile != "" {
		v.SetConfigFile(opt.ConfigFile)
	} else {
		if opt.ConfigName == "" {
			return nil, fmt.Errorf("ConfigName must be specified when ConfigFile is empty")
		}
		if len(opt.ConfigPaths) == 0 {
			return nil, fmt.Errorf("ConfigPaths must be specified when ConfigFile is empty")
		}
		v.SetConfigName(opt.ConfigName)
		for _, path := range opt.ConfigPaths {
			v.AddConfigPath(path)
		}
	}

	// Set config type
	if opt.ConfigType != "" {
		v.SetConfigType(opt.ConfigType)
	}

	// Set default values
	for key, value := range opt.Defaults {
		v.SetDefault(key, value)
	}

	// Bind environment variables
	if opt.EnvPrefix != "" {
		v.SetEnvPrefix(opt.EnvPrefix)
		// Replace "." with "_" in key names so that "server.port" maps to "NSP_SERVER_PORT"
		v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
		v.AutomaticEnv()
	}

	return &viperLoader{
		v:     v,
		done:  make(chan struct{}),
		watch: opt.Watch,
	}, nil
}

// Load loads the configuration file and unmarshals it into target.
// Each call re-reads the file content.
// Uses UnmarshalExact which returns an error for any unknown fields in the
// config file, preventing typos from being silently ignored.
//
// If Watch is enabled, the file watcher is started after the first
// successful Load call, ensuring that viper's internal state is fully
// initialised before any concurrent file-change events can fire.
func (l *viperLoader) Load(target any) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if err := l.v.ReadInConfig(); err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}
	if err := l.v.UnmarshalExact(target); err != nil {
		return err
	}

	// Start watching after the first successful Load so that viper's
	// internal config map is populated before the watcher can fire.
	if l.watch {
		l.watchOnce.Do(func() { l.startWatching() })
	}

	return nil
}

// OnChange registers a configuration change callback.
// If Watch=false, registering callbacks does not error but they will never be triggered.
func (l *viperLoader) OnChange(fn func(func(any) error)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.callbacks = append(l.callbacks, fn)
}

// Close stops file watching and releases resources.
// Safe to call multiple times; subsequent calls are no-ops.
func (l *viperLoader) Close() {
	l.closeOnce.Do(func() { close(l.done) })
}

// startWatching starts file watching if Watch=true is configured.
// Must be called with l.mu held (called from Load).
func (l *viperLoader) startWatching() {
	l.v.WatchConfig()

	// Debounce timer to coalesce multiple fsnotify events into one callback invocation.
	// fsnotify on Linux may fire multiple events for a single file write (e.g., WRITE + CHMOD,
	// or multiple WRITE events). We debounce with a 50ms window to handle this.
	var debounceTimer *time.Timer
	var debounceMu sync.Mutex

	l.v.OnConfigChange(func(e fsnotify.Event) {
		// Check if loader is closed
		select {
		case <-l.done:
			return // Skip execution if closed
		default:
		}

		debounceMu.Lock()
		if debounceTimer != nil {
			debounceTimer.Stop()
		}
		debounceTimer = time.AfterFunc(50*time.Millisecond, func() {
			l.executeCallbacks()
		})
		debounceMu.Unlock()
	})
}

// executeCallbacks invokes all registered callbacks with the current configuration.
func (l *viperLoader) executeCallbacks() {
	// Check if loader is closed
	select {
	case <-l.done:
		return
	default:
	}

	// Note: Kubernetes Secret volume updates use atomic symlink replacement,
	// which triggers fsnotify Create events. viper's internal fsnotify
	// already handles this scenario correctly, no additional adaptation needed.

	// Acquire write lock to protect callbacks list and viper state
	l.mu.Lock()

	// applyFn is provided to each callback so that it can deserialize the
	// latest in-memory configuration. Uses UnmarshalExact to maintain the
	// same strict behaviour as Load (unknown fields cause an error).
	applyFn := func(target any) error {
		return l.v.UnmarshalExact(target)
	}

	// Copy callbacks to avoid holding lock during execution
	callbacks := make([]func(func(any) error), len(l.callbacks))
	copy(callbacks, l.callbacks)
	l.mu.Unlock()

	// Execute callbacks in registration order.
	// Individual callback panics are recovered to ensure subsequent callbacks execute.
	for _, cb := range callbacks {
		func() {
			defer func() {
				if r := recover(); r != nil {
					// Log panic but continue executing remaining callbacks.
					// Cannot use logger here to avoid dependency cycle.
					fmt.Printf("config callback panic recovered: %v\n", r)
				}
			}()
			cb(applyFn)
		}()
	}
}
