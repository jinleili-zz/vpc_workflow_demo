package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAuthCredentialsResolveEnv(t *testing.T) {
	t.Setenv("TOP_NSP_SK", "resolved-secret")

	cfg := &NSPConfig{
		Auth: AuthConfig{
			Credentials: []AuthCredentialConfig{
				{
					AccessKey: "top-nsp",
					SecretKey: "${TOP_NSP_SK}",
					Label:     "Top NSP",
					Enabled:   true,
				},
			},
		},
	}

	creds, err := cfg.AuthCredentials()
	if err != nil {
		t.Fatalf("AuthCredentials failed: %v", err)
	}
	if len(creds) != 1 {
		t.Fatalf("len(creds) = %d, want 1", len(creds))
	}
	if creds[0].SecretKey != "resolved-secret" {
		t.Fatalf("secret key = %q, want %q", creds[0].SecretKey, "resolved-secret")
	}
}

func TestResolveSignerCredentialUsesPreferredAK(t *testing.T) {
	cfg := &NSPConfig{
		Auth: AuthConfig{
			Credentials: []AuthCredentialConfig{
				{AccessKey: "disabled", SecretKey: "secret-1", Enabled: false},
				{AccessKey: "top-nsp", SecretKey: "secret-2", Enabled: true},
			},
		},
	}

	cred, err := cfg.ResolveSignerCredential("top-nsp")
	if err != nil {
		t.Fatalf("ResolveSignerCredential failed: %v", err)
	}
	if cred.AccessKey != "top-nsp" {
		t.Fatalf("access key = %q, want %q", cred.AccessKey, "top-nsp")
	}
}

func TestTLSDefaultsFromConfigLoader(t *testing.T) {
	configFile := writeTestConfig(t, "region: \"cn-test-1\"\n")

	loader, err := NewConfigLoader(configFile, "NSP", false)
	if err != nil {
		t.Fatalf("NewConfigLoader failed: %v", err)
	}
	defer loader.Close()

	cfg := loader.GetConfig()
	if cfg.TLS.Enabled {
		t.Fatalf("TLS.Enabled = true, want false")
	}
	if cfg.TLS.Mode != "process" {
		t.Fatalf("TLS.Mode = %q, want %q", cfg.TLS.Mode, "process")
	}
	if !cfg.TLS.ClientAuth {
		t.Fatalf("TLS.ClientAuth = false, want true")
	}
	if cfg.TLS.CAReloadInterval != 5*time.Minute {
		t.Fatalf("TLS.CAReloadInterval = %v, want %v", cfg.TLS.CAReloadInterval, 5*time.Minute)
	}
	if cfg.TLS.InsecureSkipVerify {
		t.Fatalf("TLS.InsecureSkipVerify = true, want false")
	}
}

func TestTLSEnvOverrides(t *testing.T) {
	t.Setenv("NSP_TLS_ENABLED", "true")
	t.Setenv("NSP_TLS_CA_CERT_PATH", "/certs/ca.pem")
	t.Setenv("NSP_TLS_CERT_PATH", "/certs/client.crt")
	t.Setenv("NSP_TLS_KEY_PATH", "/certs/client.key")
	t.Setenv("NSP_TLS_CLIENT_AUTH", "false")

	configFile := writeTestConfig(t, "region: \"cn-test-1\"\n")
	loader, err := NewConfigLoader(configFile, "NSP", false)
	if err != nil {
		t.Fatalf("NewConfigLoader failed: %v", err)
	}
	defer loader.Close()

	cfg := loader.GetConfig()
	if !cfg.TLS.Enabled {
		t.Fatalf("TLS.Enabled = false, want true")
	}
	if cfg.TLS.CACertPath != "/certs/ca.pem" {
		t.Fatalf("TLS.CACertPath = %q", cfg.TLS.CACertPath)
	}
	if cfg.TLS.CertPath != "/certs/client.crt" {
		t.Fatalf("TLS.CertPath = %q", cfg.TLS.CertPath)
	}
	if cfg.TLS.KeyPath != "/certs/client.key" {
		t.Fatalf("TLS.KeyPath = %q", cfg.TLS.KeyPath)
	}
	if cfg.TLS.ClientAuth {
		t.Fatalf("TLS.ClientAuth = true, want false")
	}
}

func TestValidateTLSStartup(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *NSPConfig
		nspAddr string
		wantErr bool
	}{
		{
			name: "top requires ca and client cert",
			cfg: &NSPConfig{
				ServiceType: "top",
				TLS: TLSConfig{
					Enabled: true,
				},
			},
			wantErr: true,
		},
		{
			name: "az process requires cert key and ca when client auth enabled",
			cfg: &NSPConfig{
				ServiceType: "az",
				TLS: TLSConfig{
					Enabled:    true,
					Mode:       "process",
					ClientAuth: true,
				},
			},
			wantErr: true,
		},
		{
			name: "az lb requires nsp addr",
			cfg: &NSPConfig{
				ServiceType: "az",
				TLS: TLSConfig{
					Enabled: true,
					Mode:    "lb",
				},
			},
			wantErr: true,
		},
		{
			name: "az process valid",
			cfg: &NSPConfig{
				ServiceType: "az",
				TLS: TLSConfig{
					Enabled:    true,
					Mode:       "process",
					CACertPath: "/certs/ca.pem",
					CertPath:   "/certs/server.crt",
					KeyPath:    "/certs/server.key",
					ClientAuth: true,
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.ValidateTLSStartup(tt.nspAddr)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateTLSStartup error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func writeTestConfig(t *testing.T, content string) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}
