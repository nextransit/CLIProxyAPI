package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type refreshFailureStore struct {
	saveCount atomic.Int32
	lastAuth  atomic.Pointer[Auth]
}

func (s *refreshFailureStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (s *refreshFailureStore) Save(_ context.Context, auth *Auth) (string, error) {
	s.saveCount.Add(1)
	if auth != nil {
		s.lastAuth.Store(auth.Clone())
	}
	return "", nil
}

func (s *refreshFailureStore) Delete(context.Context, string) error { return nil }

type refreshFailureExecutor struct {
	id  string
	err error
}

func (e refreshFailureExecutor) Identifier() string { return e.id }

func (e refreshFailureExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e refreshFailureExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e refreshFailureExecutor) Refresh(context.Context, *Auth) (*Auth, error) {
	return nil, e.err
}

func (e refreshFailureExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e refreshFailureExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestManagerRefreshAuth_DisablesAuthWhenRefreshTokenRequiresRelogin(t *testing.T) {
	t.Parallel()

	store := &refreshFailureStore{}
	manager := NewManager(store, nil, nil)
	manager.RegisterExecutor(refreshFailureExecutor{
		id:  "codex",
		err: errors.New(`token refresh failed with status 401: {"error":{"message":"Your refresh token has already been used to generate a new access token. Please try signing in again.","code":"refresh_token_reused"}}`),
	})

	auth := &Auth{
		ID:       "codex-auth-1",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":          "codex",
			"refresh_token": "rt-1",
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}
	store.saveCount.Store(0)

	manager.refreshAuth(context.Background(), auth.ID)

	manager.mu.RLock()
	current := manager.auths[auth.ID]
	manager.mu.RUnlock()
	if current == nil {
		t.Fatal("expected auth to remain registered")
	}
	if !current.Disabled {
		t.Fatal("expected auth to be disabled after fatal refresh failure")
	}
	if current.Status != StatusDisabled {
		t.Fatalf("current.Status = %q, want %q", current.Status, StatusDisabled)
	}
	if current.LastError == nil {
		t.Fatal("expected LastError to be populated")
	}
	if !strings.Contains(strings.ToLower(current.LastError.Message), "refresh_token_reused") {
		t.Fatalf("LastError.Message = %q, want refresh_token_reused", current.LastError.Message)
	}
	if !strings.Contains(strings.ToLower(current.StatusMessage), "sign in again") {
		t.Fatalf("StatusMessage = %q, want sign in again guidance", current.StatusMessage)
	}
	if got := store.saveCount.Load(); got != 1 {
		t.Fatalf("expected fatal refresh state to persist once, got %d saves", got)
	}
	saved := store.lastAuth.Load()
	if saved == nil || !saved.Disabled || saved.Status != StatusDisabled {
		t.Fatalf("persisted auth = %#v, want disabled persisted auth", saved)
	}
}
