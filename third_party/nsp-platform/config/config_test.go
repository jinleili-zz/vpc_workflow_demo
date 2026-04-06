// Copyright 2026 NSP Authors. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Test types for configuration
type ServerConfig struct {
	Host string `mapstructure:"host"`
	Port int    `mapstructure:"port"`
}

type DatabaseConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	Username string `mapstructure:"username"`
	Password string `mapstructure:"password"`
}

type AppConfig struct {
	Server   ServerConfig   `mapstructure:"server"`
	Database DatabaseConfig `mapstructure:"database"`
	Debug    bool           `mapstructure:"debug"`
	Version  string         `mapstructure:"version"`
}

// Test_Load_YAML tests loading YAML format configuration file.
func Test_Load_YAML(t *testing.T) {
	content := `
server:
  host: localhost
  port: 8080
database:
  host: db.example.com
  port: 5432
  username: testuser
  password: testpass
debug: true
version: v1.0.0
`
	file := createTempConfigFile(t, "config.yaml", content)

	loader, err := New(Option{
		ConfigFile: file,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer loader.Close()

	var cfg AppConfig
	if err := loader.Load(&cfg); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Server.Host != "localhost" {
		t.Errorf("Server.Host = %v, want localhost", cfg.Server.Host)
	}
	if cfg.Server.Port != 8080 {
		t.Errorf("Server.Port = %v, want 8080", cfg.Server.Port)
	}
	if cfg.Database.Host != "db.example.com" {
		t.Errorf("Database.Host = %v, want db.example.com", cfg.Database.Host)
	}
	if cfg.Database.Port != 5432 {
		t.Errorf("Database.Port = %v, want 5432", cfg.Database.Port)
	}
	if cfg.Debug != true {
		t.Errorf("Debug = %v, want true", cfg.Debug)
	}
	if cfg.Version != "v1.0.0" {
		t.Errorf("Version = %v, want v1.0.0", cfg.Version)
	}
}

// Test_Load_JSON tests loading JSON format configuration file.
func Test_Load_JSON(t *testing.T) {
	content := `{
  "server": {
    "host": "localhost",
    "port": 8080
  },
  "database": {
    "host": "db.example.com",
    "port": 5432,
    "username": "testuser",
    "password": "testpass"
  },
  "debug": true,
  "version": "v1.0.0"
}`
	file := createTempConfigFile(t, "config.json", content)

	loader, err := New(Option{
		ConfigFile: file,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer loader.Close()

	var cfg AppConfig
	if err := loader.Load(&cfg); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Server.Host != "localhost" {
		t.Errorf("Server.Host = %v, want localhost", cfg.Server.Host)
	}
	if cfg.Server.Port != 8080 {
		t.Errorf("Server.Port = %v, want 8080", cfg.Server.Port)
	}
	if cfg.Database.Username != "testuser" {
		t.Errorf("Database.Username = %v, want testuser", cfg.Database.Username)
	}
	if cfg.Debug != true {
		t.Errorf("Debug = %v, want true", cfg.Debug)
	}
}

// Test_Load_DefaultValue tests using default values for missing configuration items.
func Test_Load_DefaultValue(t *testing.T) {
	content := `
server:
  host: localhost
database:
  host: db.example.com
`
	file := createTempConfigFile(t, "config.yaml", content)

	loader, err := New(Option{
		ConfigFile: file,
		Defaults: map[string]any{
			"server.port":       9000,
			"database.port":     5432,
			"database.username": "defaultuser",
			"debug":             false,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer loader.Close()

	var cfg AppConfig
	if err := loader.Load(&cfg); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Server.Port != 9000 {
		t.Errorf("Server.Port = %v, want 9000 (default)", cfg.Server.Port)
	}
	if cfg.Database.Port != 5432 {
		t.Errorf("Database.Port = %v, want 5432 (default)", cfg.Database.Port)
	}
	if cfg.Database.Username != "defaultuser" {
		t.Errorf("Database.Username = %v, want defaultuser (default)", cfg.Database.Username)
	}
	if cfg.Debug != false {
		t.Errorf("Debug = %v, want false (default)", cfg.Debug)
	}
}

// Test_Load_EnvOverride tests environment variables overriding configuration file values.
func Test_Load_EnvOverride(t *testing.T) {
	content := `
server:
  host: localhost
  port: 8080
debug: false
`
	file := createTempConfigFile(t, "config.yaml", content)

	// Set environment variables
	os.Setenv("TEST_SERVER_PORT", "9000")
	os.Setenv("TEST_DEBUG", "true")
	defer func() {
		os.Unsetenv("TEST_SERVER_PORT")
		os.Unsetenv("TEST_DEBUG")
	}()

	loader, err := New(Option{
		ConfigFile: file,
		EnvPrefix:  "TEST",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer loader.Close()

	var cfg AppConfig
	if err := loader.Load(&cfg); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Server.Port != 9000 {
		t.Errorf("Server.Port = %v, want 9000 (from env TEST_SERVER_PORT)", cfg.Server.Port)
	}
	if cfg.Debug != true {
		t.Errorf("Debug = %v, want true (from env TEST_DEBUG)", cfg.Debug)
	}
}

// Test_Load_UnknownField tests that unknown fields in config file cause Load to return error.
func Test_Load_UnknownField(t *testing.T) {
	content := `
server:
  host: localhost
  port: 8080
  unknown_field: bad_value  # This field doesn't exist in ServerConfig
debug: true
`
	file := createTempConfigFile(t, "config.yaml", content)

	loader, err := New(Option{
		ConfigFile: file,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer loader.Close()

	var cfg AppConfig
	err = loader.Load(&cfg)
	if err == nil {
		t.Fatal("Load() should return error for unknown fields")
	}
}

// Test_Load_FileNotFound tests that Load returns clear error when config file doesn't exist.
func Test_Load_FileNotFound(t *testing.T) {
	loader, err := New(Option{
		ConfigFile: "/nonexistent/config.yaml",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer loader.Close()

	var cfg AppConfig
	err = loader.Load(&cfg)
	if err == nil {
		t.Fatal("Load() should return error for non-existent file")
	}
}

// Test_OnChange_Triggered tests that OnChange callback is triggered when file changes.
func Test_OnChange_Triggered(t *testing.T) {
	content1 := `
server:
  port: 8080
debug: false
`
	content2 := `
server:
  port: 9000
debug: true
`
	file := createTempConfigFile(t, "config.yaml", content1)

	loader, err := New(Option{
		ConfigFile: file,
		Watch:      true,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer loader.Close()

	// Load must be called before OnChange callbacks can fire (starts watcher).
	var cfg AppConfig
	if err := loader.Load(&cfg); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	changed := make(chan struct{}, 1)
	var newCfg AppConfig

	loader.OnChange(func(apply func(any) error) {
		if err := apply(&newCfg); err != nil {
			t.Errorf("apply in callback error = %v", err)
			return
		}
		changed <- struct{}{}
	})

	// Give watcher time to start
	time.Sleep(200 * time.Millisecond)

	// Modify file
	if err := os.WriteFile(file, []byte(content2), 0644); err != nil {
		t.Fatalf("Failed to write file: %v", err)
	}

	// Wait for callback (timeout 3s)
	select {
	case <-changed:
		if newCfg.Server.Port != 9000 {
			t.Errorf("newCfg.Server.Port = %v, want 9000", newCfg.Server.Port)
		}
		if newCfg.Debug != true {
			t.Errorf("newCfg.Debug = %v, want true", newCfg.Debug)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("OnChange callback was not triggered within 3 seconds")
	}
}

// Test_OnChange_MultipleCallbacks tests that multiple callbacks are triggered in registration order.
func Test_OnChange_MultipleCallbacks(t *testing.T) {
	content1 := `debug: false`
	content2 := `debug: true`
	file := createTempConfigFile(t, "config.yaml", content1)

	loader, err := New(Option{
		ConfigFile: file,
		Watch:      true,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer loader.Close()

	var cfg AppConfig
	if err := loader.Load(&cfg); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	callOrder := make([]int, 0, 2)
	done := make(chan struct{}, 2)

	// First callback
	loader.OnChange(func(apply func(any) error) {
		callOrder = append(callOrder, 1)
		done <- struct{}{}
	})

	// Second callback
	loader.OnChange(func(apply func(any) error) {
		callOrder = append(callOrder, 2)
		done <- struct{}{}
	})

	// Modify file
	time.Sleep(100 * time.Millisecond)
	if err := os.WriteFile(file, []byte(content2), 0644); err != nil {
		t.Fatalf("Failed to write file: %v", err)
	}

	// Wait for both callbacks
	timeout := time.After(3 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-timeout:
			t.Fatal("Not all callbacks were triggered within 3 seconds")
		}
	}

	if len(callOrder) != 2 {
		t.Fatalf("Expected 2 callbacks, got %d", len(callOrder))
	}
	if callOrder[0] != 1 || callOrder[1] != 2 {
		t.Errorf("Callback order incorrect: %v, want [1, 2]", callOrder)
	}
}

// Test_OnChange_WatchFalse tests that registering OnChange callbacks doesn't error when Watch=false,
// but callbacks are not triggered when file changes.
func Test_OnChange_WatchFalse(t *testing.T) {
	content1 := `debug: false`
	content2 := `debug: true`
	file := createTempConfigFile(t, "config.yaml", content1)

	loader, err := New(Option{
		ConfigFile: file,
		Watch:      false, // Watch disabled
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer loader.Close()

	callbackCalled := false
	loader.OnChange(func(apply func(any) error) {
		callbackCalled = true
	})

	// Modify file
	if err := os.WriteFile(file, []byte(content2), 0644); err != nil {
		t.Fatalf("Failed to write file: %v", err)
	}

	// Wait a bit to ensure no callback
	time.Sleep(500 * time.Millisecond)
	if callbackCalled {
		t.Error("Callback should not be triggered when Watch=false")
	}
}

// Test_Close_StopsWatch tests that Close stops watching and callbacks are not triggered.
func Test_Close_StopsWatch(t *testing.T) {
	content1 := `debug: false`
	content2 := `debug: true`
	file := createTempConfigFile(t, "config.yaml", content1)

	loader, err := New(Option{
		ConfigFile: file,
		Watch:      true,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	var cfg AppConfig
	if err := loader.Load(&cfg); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	callbackCalled := false
	loader.OnChange(func(apply func(any) error) {
		callbackCalled = true
	})

	// Close loader
	loader.Close()

	// Modify file after Close
	if err := os.WriteFile(file, []byte(content2), 0644); err != nil {
		t.Fatalf("Failed to write file: %v", err)
	}

	// Wait a bit to ensure no callback
	time.Sleep(500 * time.Millisecond)
	if callbackCalled {
		t.Error("Callback should not be triggered after Close()")
	}
}

// Test_OnChange_PanicRecovery tests that when the first callback panics,
// the second callback still executes normally.
func Test_OnChange_PanicRecovery(t *testing.T) {
	content1 := `debug: false`
	content2 := `debug: true`
	file := createTempConfigFile(t, "config.yaml", content1)

	loader, err := New(Option{
		ConfigFile: file,
		Watch:      true,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer loader.Close()

	var cfg AppConfig
	if err := loader.Load(&cfg); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	panicked := false
	executed := false
	done := make(chan struct{}, 2)

	// First callback - will panic
	loader.OnChange(func(apply func(any) error) {
		defer func() { done <- struct{}{} }()
		panicked = true
		panic("test panic")
	})

	// Second callback - should still execute
	loader.OnChange(func(apply func(any) error) {
		defer func() { done <- struct{}{} }()
		executed = true
	})

	// Modify file
	time.Sleep(100 * time.Millisecond)
	if err := os.WriteFile(file, []byte(content2), 0644); err != nil {
		t.Fatalf("Failed to write file: %v", err)
	}

	// Wait for both callbacks
	timeout := time.After(3 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-timeout:
			t.Fatal("Not all callbacks completed within 3 seconds")
		}
	}

	if !panicked {
		t.Error("First callback should have panicked")
	}
	if !executed {
		t.Error("Second callback should have executed despite first callback panic")
	}
}

// Helper function to create temporary config file
func createTempConfigFile(t *testing.T, name, content string) string {
	t.Helper()

	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, name)
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write temp file: %v", err)
	}

	return filePath
}
