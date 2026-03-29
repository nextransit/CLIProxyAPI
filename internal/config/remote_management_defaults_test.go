package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigOptional_DisablePanelRemoteUpdate_DefaultTrue(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte(`port: 8317
remote-management:
  allow-remote: false
`)
	if err := os.WriteFile(configPath, content, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}
	if !cfg.RemoteManagement.DisablePanelRemoteUpdate {
		t.Fatalf("expected remote-management.disable-panel-remote-update default to be true")
	}
}

func TestLoadConfigOptional_DisablePanelRemoteUpdate_ExplicitFalse(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte(`port: 8317
remote-management:
  disable-panel-remote-update: false
`)
	if err := os.WriteFile(configPath, content, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}
	if cfg.RemoteManagement.DisablePanelRemoteUpdate {
		t.Fatalf("expected remote-management.disable-panel-remote-update to remain false when explicitly configured")
	}
}
