package config

import (
	"testing"
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
