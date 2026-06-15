package auth

import (
	"context"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// TestSessionAffinity_ReselectAfterAuthUnavailable_RoutesAwayFromFailedKey
// verifies that when a cached session's auth becomes unavailable, the
// reselect path uses the same weighted-share logic as a brand-new session
// instead of falling through to the legacy round-robin fallback. In
// particular, the failed key must not be the destination.
func TestSessionAffinity_ReselectAfterAuthUnavailable_RoutesAwayFromFailedKey(t *testing.T) {
	authA := &Auth{
		ID:         "auth-A",
		Provider:   "claude",
		Attributes: map[string]string{"weight": "1"},
		Metadata:   map[string]any{"type": "claude"},
	}
	authB := &Auth{
		ID:         "auth-B",
		Provider:   "claude",
		Attributes: map[string]string{"weight": "2"},
		Metadata:   map[string]any{"type": "claude"},
	}
	auths := []*Auth{authA, authB}

	sel := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:    &RoundRobinSelector{},
		TTL:         time.Hour,
		MaxRequests: 1000,
	})
	opts := cliproxyexecutor.Options{
		OriginalRequest: []byte(buildSessionPayload(99)),
	}

	// Bind the session. The first pick will land on auth-B (weight 2 wins
	// ties against weight 1 when both are at 0 bound).
	first, err := sel.Pick(context.Background(), "claude", "model-x", opts, auths)
	if err != nil {
		t.Fatalf("first pick: %v", err)
	}

	// Mark the bound auth as disabled (simulates API key revocation). The
	// session cache still holds the old binding, so the next Pick must
	// notice the bound auth is unavailable and reselect.
	if first.ID == "auth-A" {
		authA.Disabled = true
		authA.Status = StatusDisabled
		defer func() { authA.Disabled = false; authA.Status = "" }()
	} else {
		authB.Disabled = true
		authB.Status = StatusDisabled
		defer func() { authB.Disabled = false; authB.Status = "" }()
	}

	reselected, err := sel.Pick(context.Background(), "claude", "model-x", opts, auths)
	if err != nil {
		t.Fatalf("reselect pick: %v", err)
	}
	if reselected.ID == first.ID {
		t.Fatalf("reselect returned the disabled auth %s; expected the other one", first.ID)
	}

	// The new binding must be sticky across subsequent requests in the
	// same session.
	for i := 0; i < 5; i++ {
		next, err := sel.Pick(context.Background(), "claude", "model-x", opts, auths)
		if err != nil {
			t.Fatalf("sticky pick %d: %v", i, err)
		}
		if next.ID != reselected.ID {
			t.Fatalf("session drifted from %s to %s on pick %d", reselected.ID, next.ID, i)
		}
	}
}
