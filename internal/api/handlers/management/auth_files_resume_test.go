package management

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestResumeAuthFile_ClearsCooldown(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	tempDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)

	auth := &coreauth.Auth{
		ID:       "codex-resume-test",
		FileName: "codex-resume-test.json",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"type": "codex"},
		Attributes: map[string]string{
			"path": filepath.Join(tempDir, "codex-resume-test.json"),
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("failed to register auth: %v", errRegister)
	}

	// Simulate the exact "auth pinned out of rotation" state from a 404-style
	// upstream failure.
	manager.MarkResult(context.Background(), coreauth.Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    "gpt-5.3-codex",
		Success:  false,
		Error: &coreauth.Error{
			HTTPStatus: http.StatusNotFound,
			Message:    `{"detail":"模型已下架"}`,
		},
	})

	before, _ := manager.GetByID(auth.ID)
	if state := before.ModelStates["gpt-5.3-codex"]; state == nil || !state.Unavailable {
		t.Fatalf("expected per-model Unavailable=true pre-resume, got %#v", state)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: tempDir}, manager)
	h.tokenStore = &memoryAuthStore{}

	body, _ := json.Marshal(map[string]any{"name": auth.ID})
	req := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files/resume", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = req
	h.ResumeAuthFile(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}

	after, _ := manager.GetByID(auth.ID)
	state := after.ModelStates["gpt-5.3-codex"]
	if state == nil {
		t.Fatalf("expected model state to remain in the map after resume, got nil")
	}
	if state.Unavailable {
		t.Fatalf("expected Unavailable=false after resume, got true")
	}
	if !state.NextRetryAfter.IsZero() {
		t.Fatalf("expected NextRetryAfter cleared, got %v", state.NextRetryAfter)
	}
	if after.Unavailable {
		t.Fatalf("expected auth.Unavailable=false after resume, got true")
	}
}

func TestResumeAuthFile_AcceptsAuthIndex(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	tempDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)

	auth := &coreauth.Auth{
		ID:       "codex-resume-index-test",
		FileName: "codex-resume-index-test.json",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"type": "codex"},
		Attributes: map[string]string{
			"path": filepath.Join(tempDir, "codex-resume-index-test.json"),
		},
	}
	registered, errRegister := manager.Register(context.Background(), auth)
	if errRegister != nil {
		t.Fatalf("failed to register auth: %v", errRegister)
	}

	manager.MarkResult(context.Background(), coreauth.Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    "gpt-5.5",
		Success:  false,
		Error: &coreauth.Error{
			HTTPStatus: http.StatusBadGateway,
			Message:    "upstream unavailable",
		},
	})

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: tempDir}, manager)
	h.tokenStore = &memoryAuthStore{}

	body, _ := json.Marshal(map[string]any{"name": registered.Index, "models": []string{"gpt-5.5"}})
	req := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files/resume", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = req
	h.ResumeAuthFile(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}

	after, _ := manager.GetByID(auth.ID)
	state := after.ModelStates["gpt-5.5"]
	if state == nil || state.Unavailable {
		t.Fatalf("expected gpt-5.5 resumed by auth index, got %#v", state)
	}
}

func TestResumeAuthFile_RejectsMissingName(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	tempDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: tempDir}, manager)
	h.tokenStore = &memoryAuthStore{}

	body, _ := json.Marshal(map[string]any{})
	req := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files/resume", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = req
	h.ResumeAuthFile(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}
}
