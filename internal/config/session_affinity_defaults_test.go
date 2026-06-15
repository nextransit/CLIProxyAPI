package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigOptional_SessionAffinityMaxRequestsDefault(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte(`port: 8317
`)
	if err := os.WriteFile(configPath, content, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}
	if got := cfg.SessionAffinityMaxRequests; got != 20 {
		t.Fatalf("SessionAffinityMaxRequests = %d, want 20", got)
	}
}

func TestLoadConfigOptional_SessionAffinityMaxRequestsExplicitZero(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte(`port: 8317
session-affinity-max-requests: 0
`)
	if err := os.WriteFile(configPath, content, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}
	if got := cfg.SessionAffinityMaxRequests; got != 0 {
		t.Fatalf("SessionAffinityMaxRequests = %d, want 0", got)
	}
}

func TestLoadConfigOptional_SessionAffinityMaxRequestsNegative(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte(`port: 8317
session-affinity-max-requests: -1
`)
	if err := os.WriteFile(configPath, content, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}
	if got := cfg.SessionAffinityMaxRequests; got != 0 {
		t.Fatalf("SessionAffinityMaxRequests = %d, want 0", got)
	}
}
