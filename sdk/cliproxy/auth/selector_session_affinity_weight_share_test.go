package auth

import (
	"context"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// TestSessionAffinity_WeightedShareDistributesByWeight verifies that new
// sessions get distributed across auths in proportion to their configured
// weight, regardless of session-affinity stickiness. With weight 1:2 and 6
// concurrent sessions, the steady state should be roughly 2 sessions on
// auth-A and 4 on auth-B.
func TestSessionAffinity_WeightedShareDistributesByWeight(t *testing.T) {
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

	// High MaxRequests keeps the new-share path sticky inside each session,
	// so we are testing only the cache-miss distribution.
	sel := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:    &RoundRobinSelector{},
		TTL:         time.Hour,
		MaxRequests: 1000,
	})

	counts := map[string]int{}
	for i := 0; i < 6; i++ {
		opts := cliproxyexecutor.Options{
			OriginalRequest: []byte(buildSessionPayload(i + 1)),
		}
		picked, err := sel.Pick(context.Background(), "claude", "model-x", opts, auths)
		if err != nil {
			t.Fatalf("session %d: %v", i+1, err)
		}
		counts[picked.ID]++
	}

	// With 6 sessions and 1:2 weights, the share algorithm should produce
	// exactly 2 on A and 4 on B. Allow ±1 slack to absorb any tie-breaking
	// edge case we may add later.
	if counts["auth-A"] < 1 || counts["auth-A"] > 3 {
		t.Fatalf("expected 2 sessions on auth-A (weight 1), got %d (auth-B: %d)", counts["auth-A"], counts["auth-B"])
	}
	if counts["auth-B"] < 3 || counts["auth-B"] > 5 {
		t.Fatalf("expected 4 sessions on auth-B (weight 2), got %d (auth-A: %d)", counts["auth-B"], counts["auth-A"])
	}
	if counts["auth-A"]+counts["auth-B"] != 6 {
		t.Fatalf("total sessions %d != 6", counts["auth-A"]+counts["auth-B"])
	}
}

// TestSessionAffinity_StickyWithinSessionAfterWeightedPick ensures that the
// weighted share only kicks in on cache miss. Once a session is bound to an
// auth, subsequent requests from that same session must stay sticky and not
// be rebalanced to "share" load more evenly.
func TestSessionAffinity_StickyWithinSessionAfterWeightedPick(t *testing.T) {
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
		OriginalRequest: []byte(buildSessionPayload(42)),
	}

	first, err := sel.Pick(context.Background(), "claude", "model-x", opts, auths)
	if err != nil {
		t.Fatalf("first pick: %v", err)
	}
	for i := 0; i < 10; i++ {
		next, err := sel.Pick(context.Background(), "claude", "model-x", opts, auths)
		if err != nil {
			t.Fatalf("sticky pick %d: %v", i, err)
		}
		if next.ID != first.ID {
			t.Fatalf("session drifted from %s to %s on pick %d", first.ID, next.ID, i)
		}
	}
}

// buildSessionPayload builds a Claude-Code-style metadata.user_id string with
// a unique session UUID, matching the format extractSessionIDs parses.
func buildSessionPayload(idx int) string {
	return `{"metadata":{"user_id":"user_hash_account__session_session_` + itoa(idx) + `"}}`
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	out := ""
	for n > 0 {
		out = string(rune('0'+n%10)) + out
		n /= 10
	}
	return out
}
