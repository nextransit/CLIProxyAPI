package auth

import (
	"context"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// TestManager_UpdateDisabledAuthClearsSessionBindings verifies that when an
// auth transitions from active to disabled (e.g., API key revoked at the
// upstream), the session-affinity selector drops all cached bindings to that
// auth so the next request from a previously-stuck session does not have to
// pay a "cache hit but auth unavailable, reselected" round trip per session.
func TestManager_UpdateDisabledAuthClearsSessionBindings(t *testing.T) {
	sel := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:    &RoundRobinSelector{},
		TTL:         time.Hour,
		MaxRequests: 1000,
	})
	m := NewManager(nil, sel, nil)

	authA := &Auth{ID: "auth-A", Provider: "claude", Attributes: map[string]string{"weight": "1"}, Metadata: map[string]any{"type": "claude"}}
	authB := &Auth{ID: "auth-B", Provider: "claude", Attributes: map[string]string{"weight": "2"}, Metadata: map[string]any{"type": "claude"}}
	if _, err := m.Register(context.Background(), authA); err != nil {
		t.Fatalf("register A: %v", err)
	}
	if _, err := m.Register(context.Background(), authB); err != nil {
		t.Fatalf("register B: %v", err)
	}

	auths := []*Auth{authA, authB}
	optsA := cliproxyexecutor.Options{OriginalRequest: []byte(buildSessionPayload(1))}
	optsB := cliproxyexecutor.Options{OriginalRequest: []byte(buildSessionPayload(2))}

	// Bind two sessions. With weight 1:2 and the weighted-share path, the
	// first session lands on the higher-weight key (auth-B), the second
	// lands on the lower-weight key (auth-A) — record the actual bindings.
	first, err := sel.Pick(context.Background(), "claude", "model-x", optsA, auths)
	if err != nil {
		t.Fatalf("pick 1: %v", err)
	}
	second, err := sel.Pick(context.Background(), "claude", "model-x", optsB, auths)
	if err != nil {
		t.Fatalf("pick 2: %v", err)
	}
	bindings := sel.cache.CountByAuthFor("claude", "model-x")
	if bindings[first.ID] == 0 || bindings[second.ID] == 0 {
		t.Fatalf("expected each auth to have at least one binding, got %+v", bindings)
	}

	// Disable the auth that the first session is bound to. The session
	// cache entry for that session must be removed by the manager hook,
	// and CountByAuth must drop to 0 for the disabled key.
	disabled := first.Clone()
	disabled.Disabled = true
	disabled.Status = StatusDisabled
	if _, err := m.Update(context.Background(), disabled); err != nil {
		t.Fatalf("disable: %v", err)
	}

	after := sel.cache.CountByAuthFor("claude", "model-x")
	if after[first.ID] != 0 {
		t.Fatalf("disabled auth %s still has %d session bindings; expected 0", first.ID, after[first.ID])
	}
	if after[second.ID] == 0 {
		t.Fatalf("healthy auth %s unexpectedly lost its session binding", second.ID)
	}
}
