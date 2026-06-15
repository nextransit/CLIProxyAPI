package auth

import (
	"context"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

func TestSessionAffinity_WeightedSharePrefersCodexWebsocketAuths(t *testing.T) {
	authHTTP := &Auth{
		ID:         "auth-http",
		Provider:   "codex",
		Attributes: map[string]string{"weight": "10"},
	}
	authWS := &Auth{
		ID:         "auth-ws",
		Provider:   "codex",
		Attributes: map[string]string{"websockets": "true"},
	}
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:    &RoundRobinSelector{},
		TTL:         time.Hour,
		MaxRequests: 1000,
	})
	defer selector.Stop()

	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	opts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"metadata":{"user_id":"user_xxx_account__session_ac980658-63bd-4fb3-97ba-8da64cb1e344"}}`),
	}
	picked, err := selector.Pick(ctx, "codex", "gpt-5.3-codex", opts, []*Auth{authHTTP, authWS})
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if picked.ID != authWS.ID {
		t.Fatalf("Pick() auth.ID = %q, want websocket auth %q", picked.ID, authWS.ID)
	}
}

func TestSessionAffinity_CachedHTTPAuthDoesNotServeCodexWebsocketRequest(t *testing.T) {
	authHTTP := &Auth{
		ID:         "auth-http",
		Provider:   "codex",
		Attributes: map[string]string{"weight": "10"},
	}
	authWS := &Auth{
		ID:         "auth-ws",
		Provider:   "codex",
		Attributes: map[string]string{"websockets": "true"},
	}
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:    &RoundRobinSelector{},
		TTL:         time.Hour,
		MaxRequests: 1000,
	})
	defer selector.Stop()

	opts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"metadata":{"user_id":"user_xxx_account__session_bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"}}`),
	}
	picked, err := selector.Pick(context.Background(), "codex", "gpt-5.3-codex", opts, []*Auth{authHTTP, authWS})
	if err != nil {
		t.Fatalf("first Pick() error = %v", err)
	}
	if picked.ID != authHTTP.ID {
		t.Fatalf("first Pick() auth.ID = %q, want HTTP auth %q", picked.ID, authHTTP.ID)
	}

	wsCtx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	picked, err = selector.Pick(wsCtx, "codex", "gpt-5.3-codex", opts, []*Auth{authHTTP, authWS})
	if err != nil {
		t.Fatalf("websocket Pick() error = %v", err)
	}
	if picked.ID != authWS.ID {
		t.Fatalf("websocket Pick() auth.ID = %q, want websocket auth %q", picked.ID, authWS.ID)
	}
}

func TestSessionAffinity_WithFillFirstFallbackKeepsFillFirstSemantics(t *testing.T) {
	authA := &Auth{
		ID:         "auth-a",
		Provider:   "claude",
		Attributes: map[string]string{"weight": "1"},
	}
	authB := &Auth{
		ID:         "auth-b",
		Provider:   "claude",
		Attributes: map[string]string{"weight": "10"},
	}
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:    &FillFirstSelector{},
		TTL:         time.Hour,
		MaxRequests: 1000,
	})
	defer selector.Stop()

	opts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"metadata":{"user_id":"user_xxx_account__session_11111111-1111-1111-1111-111111111111"}}`),
	}
	picked, err := selector.Pick(context.Background(), "claude", "claude-3", opts, []*Auth{authB, authA})
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if picked.ID != authA.ID {
		t.Fatalf("Pick() auth.ID = %q, want fill-first auth %q", picked.ID, authA.ID)
	}
}
