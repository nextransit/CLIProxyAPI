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

func TestLoadConfigOptional_OpenAICompatSessionAffinityMaxRequests(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte(`
openai-compatibility:
  - name: sensenova
    base-url: https://token.sensenova.cn/v1
    session-affinity-max-requests: 5
    api-key-entries:
      - api-key: test-key
    models:
      - name: deepseek-v4-flash
  - name: inherited
    base-url: https://example.com/v1
    api-key-entries:
      - api-key: test-key
    models:
      - name: model-x
`)
	if err := os.WriteFile(configPath, content, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}
	if got := cfg.OpenAICompatibility[0].SessionAffinityMaxRequests; got == nil || *got != 5 {
		t.Fatalf("SenseNova SessionAffinityMaxRequests = %v, want 5", got)
	}
	if got := cfg.OpenAICompatibility[1].SessionAffinityMaxRequests; got != nil {
		t.Fatalf("inherited SessionAffinityMaxRequests = %v, want nil", *got)
	}
}
