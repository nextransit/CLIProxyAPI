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

// TestClaudeWeight_SaveThenGet_RoundTrip is the canonical e2e for the user
// complaint: "save shows success, but reopening the page shows the old
// weight." It writes a real config.yaml, PUTs through PutClaudeKeys, then
// GETs through GetClaudeKeys / claudeKeysWithAuthIndex to confirm the GET
// response carries the new weight for the client.
func TestClaudeWeight_SaveThenGet_RoundTrip(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	initial := `claude-api-key:
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

	// Step 1: PUT (the path the management UI takes when the user clicks
	// save on the standalone claude-api-key page) with weight=2 on entry 1.
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
		t.Fatalf("PUT expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	// Step 2: GET through the actual management endpoint and confirm the
	// response embeds weight=2 on the second key. This is what the page
	// reads to populate the weight input on reopen.
	getRec := httptest.NewRecorder()
	getCtx, _ := gin.CreateTestContext(getRec)
	getCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/claude-api-key", nil)
	h.GetClaudeKeys(getCtx)

	if getRec.Code != http.StatusOK {
		t.Fatalf("GET expected 200, got %d", getRec.Code)
	}

	var resp struct {
		ClaudeAPIKey []map[string]any `json:"claude-api-key"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal GET response: %v body=%s", err, getRec.Body.String())
	}
	if len(resp.ClaudeAPIKey) < 2 {
		t.Fatalf("expected >=2 keys in GET response, got %d body=%s", len(resp.ClaudeAPIKey), getRec.Body.String())
	}

	// Locate the second key by api-key value (order is preserved but this
	// is more robust to future field shuffling).
	var entryB map[string]any
	for _, k := range resp.ClaudeAPIKey {
		if apiKey, _ := k["api-key"].(string); apiKey == "sk-B" {
			entryB = k
			break
		}
	}
	if entryB == nil {
		t.Fatalf("did not find sk-B in GET response: %s", getRec.Body.String())
	}
	gotWeight, ok := entryB["weight"]
	if !ok {
		t.Fatalf("expected weight field on sk-B in GET response, got body: %s", getRec.Body.String())
	}
	// weight may decode as float64 when going through json.
	if gotWeight == nil {
		t.Fatalf("weight is nil for sk-B; full body: %s", getRec.Body.String())
	}
	if got, _ := gotWeight.(float64); int(got) != 2 {
		t.Fatalf("expected weight=2 for sk-B in GET, got %v (type %T)", gotWeight, gotWeight)
	}

	// Also confirm disk persistence so we can be sure the chain is intact.
	disk, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("re-read disk: %v", err)
	}
	if !strings.Contains(string(disk), "weight: 2") {
		t.Fatalf("expected on-disk yaml to contain 'weight: 2', got:\n%s", string(disk))
	}
}
