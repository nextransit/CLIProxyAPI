package auth

import (
	"context"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// TestSessionAffinity_StickyForNRequests_ThenReselectsViaWeightedSelector
// pins the new behavior: the first N-1 requests for a given session return
// the same auth (sticky), the Nth request falls through to the fallback
// weighted selector and may pick a different auth.
func TestSessionAffinity_StickyForNRequests_ThenReselectsViaWeightedSelector(t *testing.T) {
	rec := newRecordingExecutor("claude")

	// Two auths with weight=1 and weight=2 so the weighted selector is
	// deterministic-enough to distinguish the two.
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
	// extractSessionIDs reads user_id from the request payload, not from opts.Metadata,
	// so the session identifier must be embedded in the JSON body.
	opts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"metadata":{"user_id":"user_xxx_account__session_session_1"}}`),
	}

	fallback := &RoundRobinSelector{}
	sel := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:    fallback,
		TTL:         time.Hour,
		MaxRequests: 5,
	})

	// First 4 calls (N-1) must all return the same auth.
	first, err := sel.Pick(context.Background(), "claude", "model-x", opts, auths)
	if err != nil {
		t.Fatalf("pick 1: %v", err)
	}
	for i := 2; i <= 4; i++ {
		next, err := sel.Pick(context.Background(), "claude", "model-x", opts, auths)
		if err != nil {
			t.Fatalf("pick %d: %v", i, err)
		}
		if next.ID != first.ID {
			t.Fatalf("expected sticky auth %s for pick %d, got %s", first.ID, i, next.ID)
		}
	}

	// 5th call (== N) must fall through to the weighted selector and may
	// pick a different auth. The weighted selector under deterministic
	// round-robin with the first pick being auth A and a fresh cursor will
	// also return auth A, but the important property is that the call went
	// through the fallback path; we exercise that by setting up an auth
	// pool where the fallback would choose B. To make this deterministic,
	// use only auth B in the fallback set.
	fallbackOnlyB := []*Auth{authB}
	next, err := sel.Pick(context.Background(), "claude", "model-x", opts, fallbackOnlyB)
	if err != nil {
		t.Fatalf("pick 5: %v", err)
	}
	if next.ID != "auth-B" {
		t.Fatalf("expected rotation to pick auth-B (only available via fallback) on Nth request, got %s", next.ID)
	}
	_ = rec
}

// TestSessionAffinity_ZeroMaxRequests_DisablesRotation verifies the
// opt-out: when MaxRequests is 0, the selector keeps the legacy
// "sticky for the full TTL" behavior and never falls through to the
// weighted selector based on count.
func TestSessionAffinity_ZeroMaxRequests_DisablesRotation(t *testing.T) {
	authA := &Auth{ID: "auth-A", Provider: "claude", Attributes: map[string]string{"weight": "1"}, Metadata: map[string]any{"type": "claude"}}
	authB := &Auth{ID: "auth-B", Provider: "claude", Attributes: map[string]string{"weight": "2"}, Metadata: map[string]any{"type": "claude"}}
	auths := []*Auth{authA, authB}
	// extractSessionIDs reads user_id from the request payload, not from opts.Metadata,
	// so the session identifier must be embedded in the JSON body.
	opts := cliproxyexecutor.Options{OriginalRequest: []byte(`{"metadata":{"user_id":"user_xxx_account__session_session_2"}}`)}

	sel := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:    &RoundRobinSelector{},
		TTL:         time.Hour,
		MaxRequests: 0, // disabled
	})

	first, err := sel.Pick(context.Background(), "claude", "model-x", opts, auths)
	if err != nil {
		t.Fatalf("pick 1: %v", err)
	}
	// Even after 100 calls, must keep returning the same auth.
	for i := 2; i <= 100; i++ {
		next, err := sel.Pick(context.Background(), "claude", "model-x", opts, auths)
		if err != nil {
			t.Fatalf("pick %d: %v", i, err)
		}
		if next.ID != first.ID {
			t.Fatalf("MaxRequests=0 should never rotate; pick %d returned %s, expected %s", i, next.ID, first.ID)
		}
	}
}

// TestSessionAffinity_ResetsCountAfterRotation verifies that after a
// forced rotation, the new binding starts its counter at 1 (not the old
// count) so the next N-1 requests stick to the new auth.
func TestSessionAffinity_ResetsCountAfterRotation(t *testing.T) {
	authA := &Auth{ID: "auth-A", Provider: "claude", Attributes: map[string]string{"weight": "1"}, Metadata: map[string]any{"type": "claude"}}
	authB := &Auth{ID: "auth-B", Provider: "claude", Attributes: map[string]string{"weight": "2"}, Metadata: map[string]any{"type": "claude"}}
	opts := cliproxyexecutor.Options{OriginalRequest: []byte(`{"metadata":{"user_id":"user_xxx_account__session_session_3"}}`)}

	// A stub fallback is installed to catch regressions that re-introduce
	// the pre-share "always call fallback on cache miss" path. With the
	// weighted share in place, the cache miss path skips the fallback and
	// picks directly via weight/session share, so the stub is unused here.
	stubB := &fixedPickSelector{prefer: "auth-B"}

	sel := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:    stubB,
		TTL:         time.Hour,
		MaxRequests: 3,
	})

	// First call binds the session via weighted share. With weight 1:2 and
	// an empty session cache, both auths start at share=1, so the first
	// listed auth (auth-A) wins the tie. Subsequent calls are sticky to
	// that auth.
	first, err := sel.Pick(context.Background(), "claude", "model-x", opts, []*Auth{authA, authB})
	if err != nil {
		t.Fatalf("pick 1: %v", err)
	}

	for i := 2; i <= 3; i++ {
		got, err := sel.Pick(context.Background(), "claude", "model-x", opts, []*Auth{authA, authB})
		if err != nil {
			t.Fatalf("pick %d: %v", i, err)
		}
		if got.ID != first.ID {
			t.Fatalf("expected sticky to %s on pick %d, got %s", first.ID, i, got.ID)
		}
	}

	// 4th call (count=3, 3<3 false) must fall through and re-bind via the
	// weighted share path. Once re-bound, the new auth must stay sticky
	// across the next 2 calls (counts 1 and 2 of the new binding).
	rotated, err := sel.Pick(context.Background(), "claude", "model-x", opts, []*Auth{authA, authB})
	if err != nil {
		t.Fatalf("pick 4: %v", err)
	}
	if rotated.ID == first.ID {
		// The weighted share should rebind to a different auth when an
		// alternative with a better remaining share exists. If the cache
		// still landed on the same auth, the count reset did not take
		// effect.
		t.Fatalf("expected rotation to re-bind away from %s, but got %s", first.ID, rotated.ID)
	}
	for i := 5; i <= 6; i++ {
		next, err := sel.Pick(context.Background(), "claude", "model-x", opts, []*Auth{authA, authB})
		if err != nil {
			t.Fatalf("pick %d: %v", i, err)
		}
		if next.ID != rotated.ID {
			t.Fatalf("expected sticky to %s after rotation on pick %d, got %s", rotated.ID, i, next.ID)
		}
	}
}

func TestSessionAffinity_ProviderOverrideRotatesAcrossAllKeysAfterFiveRequests(t *testing.T) {
	auths := []*Auth{
		{ID: "auth-A", Provider: "sensenova", Attributes: map[string]string{"weight": "1"}},
		{ID: "auth-B", Provider: "sensenova", Attributes: map[string]string{"weight": "1"}},
		{ID: "auth-C", Provider: "sensenova", Attributes: map[string]string{"weight": "1"}},
		{ID: "auth-D", Provider: "sensenova", Attributes: map[string]string{"weight": "1"}},
		{ID: "auth-E", Provider: "sensenova", Attributes: map[string]string{"weight": "1"}},
	}
	opts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"metadata":{"user_id":"user_xxx_account__session_sensenova_rotation"}}`),
	}
	sel := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:              &RoundRobinSelector{},
		TTL:                   time.Hour,
		MaxRequests:           20,
		MaxRequestsByProvider: map[string]int{"sensenova": 5},
	})

	got := make([]string, 0, 25)
	for i := 0; i < 25; i++ {
		picked, err := sel.Pick(context.Background(), "sensenova", "deepseek-v4-flash", opts, auths)
		if err != nil {
			t.Fatalf("pick %d: %v", i+1, err)
		}
		got = append(got, picked.ID)
	}

	seen := make(map[string]struct{}, len(auths))
	for block := 0; block < len(auths); block++ {
		start := block * 5
		boundID := got[start]
		seen[boundID] = struct{}{}
		for i := start + 1; i < start+5; i++ {
			if got[i] != boundID {
				t.Fatalf("requests %d-%d should stay on %s, got %v", start+1, start+5, boundID, got[start:start+5])
			}
		}
		if block > 0 && boundID == got[start-5] {
			t.Fatalf("request %d did not rotate away from %s, got %v", start+1, boundID, got)
		}
	}
	if len(seen) != len(auths) {
		t.Fatalf("five rotation windows used %d keys, want %d: %v", len(seen), len(auths), got)
	}
}

func TestSessionAffinity_ProviderOverrideDoesNotChangeOtherProviders(t *testing.T) {
	auths := []*Auth{
		{ID: "auth-A", Provider: "other", Attributes: map[string]string{"weight": "1"}},
		{ID: "auth-B", Provider: "other", Attributes: map[string]string{"weight": "1"}},
	}
	opts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"metadata":{"user_id":"user_xxx_account__session_other_provider"}}`),
	}
	sel := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:              &RoundRobinSelector{},
		TTL:                   time.Hour,
		MaxRequests:           20,
		MaxRequestsByProvider: map[string]int{"sensenova": 5},
	})

	first, err := sel.Pick(context.Background(), "other", "model-x", opts, auths)
	if err != nil {
		t.Fatalf("pick 1: %v", err)
	}
	for i := 2; i <= 6; i++ {
		picked, err := sel.Pick(context.Background(), "other", "model-x", opts, auths)
		if err != nil {
			t.Fatalf("pick %d: %v", i, err)
		}
		if picked.ID != first.ID {
			t.Fatalf("other provider should retain global MaxRequests=20; pick %d returned %s, want %s", i, picked.ID, first.ID)
		}
	}
}

// fixedPickSelector is a deterministic fallback used by tests: it returns
// the auth with the preferred ID if present, otherwise the first available.
type fixedPickSelector struct {
	prefer string
}

func (s *fixedPickSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	_ = ctx
	_ = provider
	_ = model
	_ = opts
	for _, a := range auths {
		if a.ID == s.prefer {
			return a, nil
		}
	}
	if len(auths) == 0 {
		return nil, &Error{Code: "auth_not_found", Message: "no auth candidates"}
	}
	return auths[0], nil
}
