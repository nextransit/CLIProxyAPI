package cliproxy

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/watcher"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

type codexAuthCaptureExecutor struct {
	mu      sync.Mutex
	authIDs []string
}

func (e *codexAuthCaptureExecutor) Identifier() string { return "codex" }

func (e *codexAuthCaptureExecutor) Execute(_ context.Context, auth *coreauth.Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.record(auth)
	return cliproxyexecutor.Response{}, nil
}

func (e *codexAuthCaptureExecutor) ExecuteStream(_ context.Context, auth *coreauth.Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.record(auth)
	return &cliproxyexecutor.StreamResult{}, nil
}

func (e *codexAuthCaptureExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *codexAuthCaptureExecutor) CountTokens(_ context.Context, auth *coreauth.Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.record(auth)
	return cliproxyexecutor.Response{}, nil
}

func (e *codexAuthCaptureExecutor) HttpRequest(_ context.Context, auth *coreauth.Auth, _ *http.Request) (*http.Response, error) {
	e.record(auth)
	return nil, nil
}

func (e *codexAuthCaptureExecutor) record(auth *coreauth.Auth) {
	if auth == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.authIDs = append(e.authIDs, auth.ID)
}

func (e *codexAuthCaptureExecutor) authIDsSnapshot() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.authIDs))
	copy(out, e.authIDs)
	return out
}

func testCodexRoundRobinConfig() *config.Config {
	return &config.Config{
		Routing: internalconfig.RoutingConfig{Strategy: "round-robin"},
		CodexKey: []config.CodexKey{
			{APIKey: "yls-key-0001-0315", BaseURL: "https://code.ylsagi.com/codex", Models: []internalconfig.CodexModel{{Name: "gpt-5.3-codex"}, {Name: "gpt-5.4"}}, ExcludedModels: []string{"*"}},
			{APIKey: "yls-key-0002-0316", BaseURL: "https://code.ylsagi.com/codex", Models: []internalconfig.CodexModel{{Name: "gpt-5.3-codex"}, {Name: "gpt-5.4"}}, ExcludedModels: []string{"*"}},
			{APIKey: "yls-key-0003-0316", BaseURL: "https://code.ylsagi.com/codex", Models: []internalconfig.CodexModel{{Name: "gpt-5.3-codex"}, {Name: "gpt-5.4"}}, ExcludedModels: []string{"*"}},
			{APIKey: "yls-key-0004-0316", BaseURL: "https://code.ylsagi.com/codex", Models: []internalconfig.CodexModel{{Name: "gpt-5.3-codex"}, {Name: "gpt-5.4"}}, ExcludedModels: []string{"*"}},
			{APIKey: "yls-key-0005-0317", BaseURL: "https://code.ylsagi.com/codex", Models: []internalconfig.CodexModel{{Name: "gpt-5.3-codex"}, {Name: "gpt-5.4"}}},
			{APIKey: "yls-key-0006-0317", BaseURL: "https://code.ylsagi.com/codex", Models: []internalconfig.CodexModel{{Name: "gpt-5.3-codex"}, {Name: "gpt-5.4"}}},
			{APIKey: "yls-key-0007-0317", BaseURL: "https://code.ylsagi.com/codex", Models: []internalconfig.CodexModel{{Name: "gpt-5.3-codex"}, {Name: "gpt-5.4"}}},
			{APIKey: "yls-key-0008-0317", BaseURL: "https://code.ylsagi.com/codex", Models: []internalconfig.CodexModel{{Name: "gpt-5.3-codex"}, {Name: "gpt-5.4"}}},
			{APIKey: "yls-key-0009-0317", BaseURL: "https://code.ylsagi.com/codex", Models: []internalconfig.CodexModel{{Name: "gpt-5.3-codex"}, {Name: "gpt-5.4"}}},
		},
	}
}

func synthesizeConfigAuthsForTest(t *testing.T, cfg *config.Config) []*coreauth.Auth {
	t.Helper()
	auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{
		Config:      cfg,
		AuthDir:     t.TempDir(),
		Now:         time.Date(2026, 3, 17, 0, 0, 0, 0, time.UTC),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	return auths
}

func TestServiceCodexAPIKeysRoundRobinAcrossEligibleKeys(t *testing.T) {
	cfg := testCodexRoundRobinConfig()
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	manager.SetConfig(cfg)

	service := &Service{
		cfg:         cfg,
		coreManager: manager,
	}

	auths := synthesizeConfigAuthsForTest(t, cfg)
	if len(auths) != len(cfg.CodexKey) {
		t.Fatalf("synthesized auth count = %d, want %d", len(auths), len(cfg.CodexKey))
	}

	reg := registry.GetGlobalRegistry()
	registered := make([]string, 0, len(auths))
	for _, auth := range auths {
		service.applyCoreAuthAddOrUpdate(context.Background(), auth)
		registered = append(registered, auth.ID)
	}
	t.Cleanup(func() {
		for _, authID := range registered {
			reg.UnregisterClient(authID)
		}
	})

	eligible := make([]string, 0, len(auths))
	for _, auth := range auths {
		models := reg.GetModelsForClient(auth.ID)
		for _, model := range models {
			if model != nil && model.ID == "gpt-5.4" {
				eligible = append(eligible, auth.ID)
				break
			}
		}
	}
	sort.Strings(eligible)
	if len(eligible) != 5 {
		t.Fatalf("eligible auths for gpt-5.4 = %d, want 5", len(eligible))
	}

	capture := &codexAuthCaptureExecutor{}
	manager.RegisterExecutor(capture)

	req := cliproxyexecutor.Request{Model: "gpt-5.4"}
	for range eligible {
		if _, err := manager.Execute(context.Background(), []string{"codex"}, req, cliproxyexecutor.Options{}); err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
	}

	got := capture.authIDsSnapshot()
	if len(got) != len(eligible) {
		t.Fatalf("captured auth count = %d, want %d", len(got), len(eligible))
	}

	gotSorted := append([]string(nil), got...)
	sort.Strings(gotSorted)
	if len(gotSorted) != len(eligible) {
		t.Fatalf("sorted captured auth count = %d, want %d", len(gotSorted), len(eligible))
	}
	for i := range eligible {
		if gotSorted[i] != eligible[i] {
			t.Fatalf("captured auths = %v, want same set as eligible auths %v", got, eligible)
		}
	}

	if _, err := manager.Execute(context.Background(), []string{"codex"}, req, cliproxyexecutor.Options{}); err != nil {
		t.Fatalf("Execute() extra call error = %v", err)
	}
	got = capture.authIDsSnapshot()
	if got[len(eligible)] != got[0] {
		t.Fatalf("round-robin wraparound auth = %q, want %q", got[len(eligible)], got[0])
	}
}

func TestWatcherStartQueuesCodexConfigAuthUpdates(t *testing.T) {
	cfg := testCodexRoundRobinConfig()
	authDir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("auth_dir: "+authDir+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	w, err := watcher.NewWatcher(configPath, authDir, func(*config.Config) {})
	if err != nil {
		t.Fatalf("NewWatcher() error = %v", err)
	}
	defer func() {
		_ = w.Stop()
	}()
	w.SetConfig(cfg)

	queue := make(chan watcher.AuthUpdate, 32)
	w.SetAuthUpdateQueue(queue)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := w.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	got := make(map[string]watcher.AuthUpdate)
	deadline := time.After(3 * time.Second)
	for len(got) < len(cfg.CodexKey) {
		select {
		case update := <-queue:
			if update.ID == "" {
				t.Fatalf("received update without auth id: %+v", update)
			}
			got[update.ID] = update
		case <-deadline:
			t.Fatalf("timed out waiting for %d auth updates, got %d", len(cfg.CodexKey), len(got))
		}
	}

	for id, update := range got {
		if update.Action != watcher.AuthUpdateActionAdd {
			t.Fatalf("update[%s].Action = %q, want %q", id, update.Action, watcher.AuthUpdateActionAdd)
		}
		if update.Auth == nil {
			t.Fatalf("update[%s].Auth is nil", id)
		}
		if update.Auth.Provider != "codex" {
			t.Fatalf("update[%s].Auth.Provider = %q, want %q", id, update.Auth.Provider, "codex")
		}
	}
}
