package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type routingDiagnosticsExecutor struct {
	id string
}

func (e routingDiagnosticsExecutor) Identifier() string { return e.id }

func (e routingDiagnosticsExecutor) Execute(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e routingDiagnosticsExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return &cliproxyexecutor.StreamResult{}, nil
}

func (e routingDiagnosticsExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e routingDiagnosticsExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e routingDiagnosticsExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestGetModelRoutingDiagnosticsRequiresModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &Handler{}
	router := gin.New()
	router.GET("/routing/model-candidates", handler.GetModelRoutingDiagnostics)

	req := httptest.NewRequest(http.MethodGet, "/routing/model-candidates", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestGetModelRoutingDiagnosticsReturnsCandidates(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(routingDiagnosticsExecutor{id: "claude"})
	auth := &coreauth.Auth{ID: "mgmt-diag-auth", Provider: "claude", Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: "minimax-claude/MiniMax-M3"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	handler := &Handler{authManager: manager}
	router := gin.New()
	router.GET("/routing/model-candidates", handler.GetModelRoutingDiagnostics)

	req := httptest.NewRequest(http.MethodGet, "/routing/model-candidates?model=minimax-claude/MiniMax-M3&provider=claude", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"model":"minimax-claude/MiniMax-M3"`, `"provider":"claude"`, `"effective_candidate_count":1`} {
		if !strings.Contains(body, want) {
			t.Fatalf("response missing %s: %s", want, body)
		}
	}
}
