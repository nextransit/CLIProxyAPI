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

func TestResolveUsagePersistenceDirs_IncludesAuthDirFallbackWithWritablePath(t *testing.T) {
	writableBase := t.TempDir()
	authDir := filepath.Join(t.TempDir(), "auths")
	t.Setenv("WRITABLE_PATH", writableBase)

	got := resolveUsagePersistenceDirs(&config.Config{AuthDir: authDir}, filepath.Join(t.TempDir(), "config.yaml"))
	wantPrimary := filepath.Join(writableBase, ".cliproxy-state")
	wantFallback := filepath.Join(authDir, ".cliproxy-state")
	if len(got) < 2 {
		t.Fatalf("resolveUsagePersistenceDirs() len = %d, want at least 2: %#v", len(got), got)
	}
	if got[0] != wantPrimary {
		t.Fatalf("primary dir = %q, want %q", got[0], wantPrimary)
	}
	if got[1] != wantFallback {
		t.Fatalf("auth fallback dir = %q, want %q", got[1], wantFallback)
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
