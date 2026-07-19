package cliproxy

import (
	"context"
	"path/filepath"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestBuilderAppliesSessionAffinityMaxRequestsOnColdStart(t *testing.T) {
	cfg := &config.Config{
		Routing: internalconfig.RoutingConfig{
			Strategy:        "round-robin",
			SessionAffinity: true,
		},
		SessionAffinityMaxRequests: 3,
	}
	service, err := NewBuilder().
		WithConfig(cfg).
		WithConfigPath(filepath.Join(t.TempDir(), "config.yaml")).
		Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	defer service.coreManager.StopAutoRefresh()

	capture := &codexAuthCaptureExecutor{}
	service.coreManager.RegisterExecutor(capture)
	authA := &coreauth.Auth{ID: "auth-a", Provider: "codex", Attributes: map[string]string{"weight": "1"}}
	authB := &coreauth.Auth{ID: "auth-b", Provider: "codex", Attributes: map[string]string{"weight": "2"}}
	if _, err := service.coreManager.Register(context.Background(), authA); err != nil {
		t.Fatalf("register A: %v", err)
	}
	if _, err := service.coreManager.Register(context.Background(), authB); err != nil {
		t.Fatalf("register B: %v", err)
	}

	opts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"metadata":{"user_id":"user_xxx_account__session_22222222-2222-2222-2222-222222222222"}}`),
	}
	for i := 0; i < 4; i++ {
		if _, err := service.coreManager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{}, opts); err != nil {
			t.Fatalf("Execute() #%d error = %v", i+1, err)
		}
	}

	got := capture.authIDsSnapshot()
	if len(got) != 4 {
		t.Fatalf("captured auth count = %d, want 4", len(got))
	}
	if got[0] != got[1] || got[1] != got[2] {
		t.Fatalf("first three requests should stay sticky, got %v", got)
	}
	if got[3] == got[0] {
		t.Fatalf("fourth request did not re-rotate after MaxRequests=3, got %v", got)
	}
}

func TestBuilderAppliesProviderSessionAffinityMaxRequestsOnColdStart(t *testing.T) {
	maxRequests := 5
	cfg := &config.Config{
		Routing: internalconfig.RoutingConfig{
			Strategy:        "round-robin",
			SessionAffinity: true,
		},
		SessionAffinityMaxRequests: 20,
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:                       "codex",
			SessionAffinityMaxRequests: &maxRequests,
		}},
	}
	service, err := NewBuilder().
		WithConfig(cfg).
		WithConfigPath(filepath.Join(t.TempDir(), "config.yaml")).
		Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	defer service.coreManager.StopAutoRefresh()

	capture := &codexAuthCaptureExecutor{}
	service.coreManager.RegisterExecutor(capture)
	authA := &coreauth.Auth{ID: "auth-a", Provider: "codex", Attributes: map[string]string{"weight": "1"}}
	authB := &coreauth.Auth{ID: "auth-b", Provider: "codex", Attributes: map[string]string{"weight": "1"}}
	if _, err := service.coreManager.Register(context.Background(), authA); err != nil {
		t.Fatalf("register A: %v", err)
	}
	if _, err := service.coreManager.Register(context.Background(), authB); err != nil {
		t.Fatalf("register B: %v", err)
	}

	opts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"metadata":{"user_id":"user_xxx_account__session_33333333-3333-3333-3333-333333333333"}}`),
	}
	for i := 0; i < 6; i++ {
		if _, err := service.coreManager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{}, opts); err != nil {
			t.Fatalf("Execute() #%d error = %v", i+1, err)
		}
	}

	got := capture.authIDsSnapshot()
	if len(got) != 6 {
		t.Fatalf("captured auth count = %d, want 6", len(got))
	}
	for i := 1; i < 5; i++ {
		if got[i] != got[0] {
			t.Fatalf("first five requests should stay sticky, got %v", got)
		}
	}
	if got[5] == got[0] {
		t.Fatalf("sixth request did not rotate after provider MaxRequests=5, got %v", got)
	}
}
