package cliproxy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestResolveUsagePersistenceDir_PrefersWritablePath(t *testing.T) {
	base := t.TempDir()
	t.Setenv("WRITABLE_PATH", base)

	got := resolveUsagePersistenceDir(&config.Config{AuthDir: filepath.Join(t.TempDir(), "auths")}, filepath.Join(t.TempDir(), "config.yaml"))
	want := filepath.Join(base, ".cliproxy-state")
	if got != want {
		t.Fatalf("resolveUsagePersistenceDir() = %q, want %q", got, want)
	}
}

func TestResolveUsagePersistenceDir_PrefersAuthDir(t *testing.T) {
	t.Setenv("WRITABLE_PATH", "")
	home := t.TempDir()
	t.Setenv("HOME", home)

	got := resolveUsagePersistenceDir(&config.Config{AuthDir: "~/.cli-proxy-api"}, filepath.Join(t.TempDir(), "config.yaml"))
	want := filepath.Join(home, ".cli-proxy-api", ".cliproxy-state")
	if got != want {
		t.Fatalf("resolveUsagePersistenceDir() = %q, want %q", got, want)
	}
}

func TestResolveUsagePersistenceDir_FallsBackToConfigDir(t *testing.T) {
	t.Setenv("WRITABLE_PATH", "")

	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: 8317\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got := resolveUsagePersistenceDir(&config.Config{}, configPath)
	want := filepath.Join(configDir, ".cliproxy-state")
	if got != want {
		t.Fatalf("resolveUsagePersistenceDir() = %q, want %q", got, want)
	}
}
