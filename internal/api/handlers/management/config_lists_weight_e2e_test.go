package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

// TestPutClaudeKeys_RoundTripsWeightThroughRealFile exercises the full
// end-to-end flow that the management UI takes when the user edits a Claude
// key's weight:
//
//  1. form state → n2() → JSON body
//  2. PUT /claude-api-key → SaveConfigPreserveComments → config.yaml on disk
//  3. reload config → confirm weight persisted
//
// The n2 formatter in the frontend only emits weight when the parsed value is
// > 0 (it uses `if (a > 0) e.weight = a`), so this test models that gate.
func TestPutClaudeKeys_RoundTripsWeightThroughRealFile(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	initial := `# claude-api-key section
claude-api-key:
  - api-key: sk-A
    base-url: https://api.minimaxi.com/anthropic
    models:
      - name: claude-opus-4-6
        alias: ""
  - api-key: sk-B
    base-url: https://api.minimaxi.com/anthropic
    models:
      - name: claude-opus-4-6
        alias: ""
`
	if err := os.WriteFile(configPath, []byte(initial), 0o600); err != nil {
		t.Fatalf("write initial: %v", err)
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	h := NewHandler(cfg, configPath, coreauth.NewManager(nil, nil, nil))

	// Model what the frontend would PUT after the user changes entry 1's
	// weight to 2: every entry shipped as the form sees it, with the edited
	// weight reflected on the second key. The frontend n2() drops weight
	// when it's 0/empty, so the unchanged first key omits the field.
	put := []config.ClaudeKey{
		{
			APIKey:  "sk-A",
			BaseURL: "https://api.minimaxi.com/anthropic",
			Models:  []config.ClaudeModel{{Name: "claude-opus-4-6"}},
			Weight:  1,
		},
		{
			APIKey:  "sk-B",
			BaseURL: "https://api.minimaxi.com/anthropic",
			Models:  []config.ClaudeModel{{Name: "claude-opus-4-6"}},
			Weight:  2,
		},
	}
	body, _ := json.Marshal(put)
	req := httptest.NewRequest(http.MethodPut, "/v0/management/claude-api-key", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = req
	h.PutClaudeKeys(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}

	// Reload from disk and confirm both weights persisted.
	reloaded, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(reloaded.ClaudeKey) != 2 {
		t.Fatalf("expected 2 claude keys, got %d", len(reloaded.ClaudeKey))
	}
	if reloaded.ClaudeKey[0].Weight != 1 {
		t.Errorf("expected first key weight=1, got %d", reloaded.ClaudeKey[0].Weight)
	}
	if reloaded.ClaudeKey[1].Weight != 2 {
		t.Errorf("expected second key weight=2, got %d", reloaded.ClaudeKey[1].Weight)
	}

	// Confirm the on-disk YAML actually has a `weight: 2` line for entry 2
	// and not a `weight: 0` that the omitempty tag would have stripped.
	onDisk, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(onDisk), "weight: 2") {
		t.Fatalf("expected yaml to contain 'weight: 2', got:\n%s", string(onDisk))
	}
}
