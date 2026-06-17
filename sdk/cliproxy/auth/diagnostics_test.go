package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type diagnosticsExecutor struct {
	id string
}

func (e *diagnosticsExecutor) Identifier() string { return e.id }

func (e *diagnosticsExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *diagnosticsExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return &cliproxyexecutor.StreamResult{}, nil
}

func (e *diagnosticsExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *diagnosticsExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *diagnosticsExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestDiagnoseModelRoutingReportsEffectiveCandidates(t *testing.T) {
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:    &RoundRobinSelector{},
		TTL:         time.Hour,
		MaxRequests: 20,
	})
	manager := NewManager(nil, selector, nil)
	manager.RegisterExecutor(&diagnosticsExecutor{id: "codex"})

	active := &Auth{
		ID:       "diag-active",
		Provider: "codex",
		Status:   StatusActive,
		Attributes: map[string]string{
			"api_key":  "key-active",
			"base_url": "https://code.example.test/codex",
			"weight":   "2",
			"priority": "10",
		},
	}
	lowerPriority := &Auth{
		ID:       "diag-lower-priority",
		Provider: "codex",
		Status:   StatusActive,
		Attributes: map[string]string{
			"api_key":  "key-lower-priority",
			"weight":   "1",
			"priority": "1",
		},
	}
	disabled := &Auth{
		ID:       "diag-disabled",
		Provider: "codex",
		Status:   StatusDisabled,
		Disabled: true,
		Attributes: map[string]string{
			"api_key": "key-disabled",
			"weight":  "1",
		},
	}
	if _, err := manager.Register(context.Background(), active); err != nil {
		t.Fatalf("register active: %v", err)
	}
	if _, err := manager.Register(context.Background(), lowerPriority); err != nil {
		t.Fatalf("register lower priority: %v", err)
	}
	if _, err := manager.Register(context.Background(), disabled); err != nil {
		t.Fatalf("register disabled: %v", err)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(active.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.5"}})
	reg.RegisterClient(lowerPriority.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.5"}})
	reg.RegisterClient(disabled.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.5"}})
	t.Cleanup(func() {
		reg.UnregisterClient(active.ID)
		reg.UnregisterClient(lowerPriority.ID)
		reg.UnregisterClient(disabled.ID)
	})
	selector.cache.Set("codex::diag-session::gpt-5.5", active.ID)
	selector.cache.Set("codex::other-model-session::gpt-4", active.ID)
	selector.cache.Set("claude::other-provider-session::gpt-5.5", active.ID)

	diag := manager.DiagnoseModelRouting("gpt-5.5", []string{"codex"})
	if diag.EffectiveCandidateCount != 1 {
		t.Fatalf("effective count = %d, want 1: %+v", diag.EffectiveCandidateCount, diag)
	}
	if diag.SessionAffinity == nil || !diag.SessionAffinity.Enabled || diag.SessionAffinity.MaxRequests != 20 {
		t.Fatalf("unexpected session affinity diagnostics: %+v", diag.SessionAffinity)
	}
	if len(diag.ProviderDiagnostics) != 1 {
		t.Fatalf("provider diagnostics len = %d, want 1", len(diag.ProviderDiagnostics))
	}
	provider := diag.ProviderDiagnostics[0]
	if !provider.ExecutorRegistered {
		t.Fatal("expected executor_registered")
	}
	if provider.EffectiveCandidateCount != 1 {
		t.Fatalf("provider effective count = %d, want 1", provider.EffectiveCandidateCount)
	}

	byID := map[string]CredentialDiagnostics{}
	for _, candidate := range provider.Candidates {
		byID[candidate.AuthID] = candidate
	}
	if got := byID[active.ID]; !got.Effective || got.Weight != 2 || got.BlockedReason != "" {
		t.Fatalf("active diagnostics = %+v", got)
	} else if got.SessionBindingScope != "codex" || got.SessionBindings != 1 {
		t.Fatalf("active session diagnostics = %+v, want scope codex with one binding", got)
	}
	if got := byID[lowerPriority.ID]; got.Effective || got.BlockedReason != "lower_priority" {
		t.Fatalf("lower priority diagnostics = %+v", got)
	}
	if got := byID[disabled.ID]; got.Effective || got.BlockedReason != "disabled" {
		t.Fatalf("disabled diagnostics = %+v", got)
	}
}

func TestDiagnoseModelRoutingReportsUnsupportedModel(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(&diagnosticsExecutor{id: "claude"})
	auth := &Auth{ID: "diag-claude", Provider: "claude", Status: StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register: %v", err)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: "minimax-claude/MiniMax-M3"}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	diag := manager.DiagnoseModelRouting("MiniMax-M3", []string{"claude"})
	if diag.EffectiveCandidateCount != 0 {
		t.Fatalf("effective count = %d, want 0", diag.EffectiveCandidateCount)
	}
	if len(diag.ProviderDiagnostics) != 1 || len(diag.ProviderDiagnostics[0].Candidates) != 1 {
		t.Fatalf("unexpected diagnostics: %+v", diag)
	}
	got := diag.ProviderDiagnostics[0].Candidates[0]
	if got.SupportsModel || got.BlockedReason != "unsupported_model" {
		t.Fatalf("candidate diagnostics = %+v", got)
	}
}
