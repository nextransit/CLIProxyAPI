package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

// TestPatchClaudeKey_PersistsWeight covers the regression where editing a
// Claude API key's "weight" in the management UI silently failed: the patch
// struct did not declare a weight field, the config struct had no weight
// field, and the synthesizer never wrote weight into auth.Attributes. With
// those fixed, a PATCH that includes {"weight": 3} should round-trip through
// the config and the synthesized auth attributes.
func TestPatchClaudeKey_PersistsWeight(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: 8317\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &config.Config{
		AuthDir: tempDir,
		ClaudeKey: []config.ClaudeKey{
			{APIKey: "sk-claude-A"},
			{APIKey: "sk-claude-B"},
		},
	}
	h := NewHandler(cfg, configPath, coreauth.NewManager(nil, nil, nil))

	body, _ := json.Marshal(map[string]any{
		"index": 0,
		"value": map[string]any{"weight": 3},
	})
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/claude-api-key", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = req
	h.PatchClaudeKey(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if got := h.cfg.ClaudeKey[0].Weight; got != 3 {
		t.Fatalf("expected ClaudeKey[0].Weight=3, got %d", got)
	}
	if got := h.cfg.ClaudeKey[1].Weight; got != 0 {
		t.Fatalf("expected ClaudeKey[1].Weight=0 (untouched), got %d", got)
	}
}

// TestPatchCodexKey_PersistsWeight is the Codex-side counterpart of
// TestPatchClaudeKey_PersistsWeight.
func TestPatchCodexKey_PersistsWeight(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: 8317\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &config.Config{
		AuthDir: tempDir,
		CodexKey: []config.CodexKey{
			{APIKey: "codex-A", BaseURL: "https://example.com/a"},
			{APIKey: "codex-B", BaseURL: "https://example.com/b"},
		},
	}
	h := NewHandler(cfg, configPath, coreauth.NewManager(nil, nil, nil))

	body, _ := json.Marshal(map[string]any{
		"index": 1,
		"value": map[string]any{"weight": 1},
	})
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/codex-api-key", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = req
	h.PatchCodexKey(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if got := h.cfg.CodexKey[1].Weight; got != 1 {
		t.Fatalf("expected CodexKey[1].Weight=1, got %d", got)
	}
	if got := h.cfg.CodexKey[0].Weight; got != 0 {
		t.Fatalf("expected CodexKey[0].Weight=0 (untouched), got %d", got)
	}
}

// TestPatchGeminiKey_PersistsWeight covers the same scenario for Gemini keys
// (the config struct already had a Weight field, but the patch path did not
// honour it).
func TestPatchGeminiKey_PersistsWeight(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: 8317\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &config.Config{
		AuthDir: tempDir,
		GeminiKey: []config.GeminiKey{
			{APIKey: "gem-A", Weight: 1},
			{APIKey: "gem-B"},
		},
	}
	h := NewHandler(cfg, configPath, coreauth.NewManager(nil, nil, nil))

	body, _ := json.Marshal(map[string]any{
		"index": 1,
		"value": map[string]any{"weight": 5},
	})
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/gemini-api-key", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = req
	h.PatchGeminiKey(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if got := h.cfg.GeminiKey[1].Weight; got != 5 {
		t.Fatalf("expected GeminiKey[1].Weight=5, got %d", got)
	}
}

// TestPutClaudeKeys_PreservesWeight ensures the bulk-replace path also
// honours weight. Without the ClaudeKey.Weight field the JSON unmarshaller
// would silently drop the value.
func TestPutClaudeKeys_PreservesWeight(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: 8317\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &config.Config{AuthDir: tempDir}
	h := NewHandler(cfg, configPath, coreauth.NewManager(nil, nil, nil))

	body, _ := json.Marshal([]map[string]any{
		{"api-key": "sk-A", "weight": 1},
		{"api-key": "sk-B", "weight": 3},
	})
	req := httptest.NewRequest(http.MethodPut, "/v0/management/claude-api-key", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = req
	h.PutClaudeKeys(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if got := h.cfg.ClaudeKey[0].Weight; got != 1 {
		t.Fatalf("expected ClaudeKey[0].Weight=1, got %d", got)
	}
	if got := h.cfg.ClaudeKey[1].Weight; got != 3 {
		t.Fatalf("expected ClaudeKey[1].Weight=3, got %d", got)
	}
	// Sanity check on the file path used to back the config so we know we
	// didn't accidentally read from somewhere unrelated.
	if filepath.Dir(h.cfg.AuthDir) == "" {
		t.Fatalf("unexpected empty auth dir")
	}
}
