package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/access/apikeypolicy"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

type countingExecutor struct {
	calls atomic.Int64
}

func (e *countingExecutor) Identifier() string { return "codex" }

func (e *countingExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	e.calls.Add(1)
	return coreexecutor.Response{
		Payload: []byte(`{"ok":true}`),
		Headers: make(http.Header),
	}, nil
}

func (e *countingExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	ch := make(chan coreexecutor.StreamChunk)
	close(ch)
	return &coreexecutor.StreamResult{Chunks: ch, Headers: make(http.Header)}, nil
}

func (e *countingExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *countingExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{
		Payload: []byte(`{"count":1}`),
		Headers: make(http.Header),
	}, nil
}

func (e *countingExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, &coreauth.Error{Code: "not_implemented", Message: "HttpRequest not implemented", HTTPStatus: http.StatusNotImplemented}
}

func newPolicyAwareHandler(t *testing.T, policyEntries []config.APIKeyEntry) (*BaseAPIHandler, *countingExecutor) {
	t.Helper()

	executor := &countingExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	authEntry := &coreauth.Auth{
		ID:       "auth1",
		Provider: "codex",
		Status:   coreauth.StatusActive,
	}
	if _, err := manager.Register(context.Background(), authEntry); err != nil {
		t.Fatalf("manager.Register(auth1): %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(authEntry.ID, authEntry.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authEntry.ID)
	})

	base := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	policyManager := apikeypolicy.NewManager()
	policyManager.SetPolicies(policyEntries)
	base.SetAPIKeyPolicyManager(policyManager)
	return base, executor
}

func requestContextWithAPIKey(apiKey string) context.Context {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Set("apiKey", apiKey)
	return context.WithValue(context.Background(), "gin", ginCtx)
}

func TestExecuteWithAuthManager_RejectsDisallowedModelByPolicy(t *testing.T) {
	base, executor := newPolicyAwareHandler(t, []config.APIKeyEntry{
		{
			Key:    "client-key",
			Models: []string{"claude-*"},
		},
	})

	ctx := requestContextWithAPIKey("client-key")
	resp, _, errMsg := base.ExecuteWithAuthManager(ctx, "openai", "test-model", []byte(`{"model":"test-model"}`), "")
	if resp != nil {
		t.Fatalf("expected nil response when policy rejects model, got %s", string(resp))
	}
	if errMsg == nil {
		t.Fatal("expected policy rejection error")
	}
	if errMsg.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", errMsg.StatusCode, http.StatusForbidden)
	}
	if executor.calls.Load() != 0 {
		t.Fatalf("executor should not be called when policy rejects request, calls=%d", executor.calls.Load())
	}
}

func TestExecuteWithAuthManager_Returns429WhenRateLimitedByPolicy(t *testing.T) {
	base, executor := newPolicyAwareHandler(t, []config.APIKeyEntry{
		{
			Key: "client-key",
			Limits: config.APIKeyLimitSettings{
				Rate: config.APIKeyRateLimits{RPM: 1},
			},
		},
	})

	ctx := requestContextWithAPIKey("client-key")
	firstResp, _, firstErr := base.ExecuteWithAuthManager(ctx, "openai", "test-model", []byte(`{"model":"test-model"}`), "")
	if firstErr != nil {
		t.Fatalf("first request should pass: %+v", firstErr)
	}
	if string(firstResp) == "" {
		t.Fatal("expected first response payload")
	}

	secondResp, _, secondErr := base.ExecuteWithAuthManager(ctx, "openai", "test-model", []byte(`{"model":"test-model"}`), "")
	if secondResp != nil {
		t.Fatalf("expected second response nil when rate-limited, got %s", string(secondResp))
	}
	if secondErr == nil {
		t.Fatal("expected second request to be rate-limited")
	}
	if secondErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", secondErr.StatusCode, http.StatusTooManyRequests)
	}
	if executor.calls.Load() != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.calls.Load())
	}
}
