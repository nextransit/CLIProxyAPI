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
	"gopkg.in/yaml.v3"
)

func newAPIKeyPolicyHandler(t *testing.T, cfg *config.Config) *Handler {
	t.Helper()

	gin.SetMode(gin.TestMode)

	if cfg == nil {
		cfg = &config.Config{}
	}
	cfg.SanitizeAPIKeyEntries()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("yaml.Marshal() error = %v", err)
	}
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	return NewHandler(cfg, configPath, nil)
}

func newJSONContext(t *testing.T, method string, target string, body []byte) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(method, target, bytes.NewReader(body))
	if body != nil {
		ctx.Request.Header.Set("Content-Type", "application/json")
	}
	return ctx, recorder
}

func TestGetAPIKeys_ReturnsStructuredEntries(t *testing.T) {
	handler := newAPIKeyPolicyHandler(t, &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeyEntries: []config.APIKeyEntry{
				{Key: "legacy-key"},
				{Key: "team-a-key", Name: "Team A", Models: []string{"gpt-4o*"}},
			},
		},
	})

	ctx, recorder := newJSONContext(t, http.MethodGet, "/api-keys", nil)
	handler.GetAPIKeys(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}

	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	items, ok := response["api-keys"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("response api-keys = %#v, want 2 items", response["api-keys"])
	}
	if _, ok := items[0].(map[string]any); !ok {
		t.Fatalf("response api-keys[0] type = %T, want object", items[0])
	}
}

func TestPutAPIKeys_AcceptsStructuredEntries(t *testing.T) {
	handler := newAPIKeyPolicyHandler(t, &config.Config{})

	body := []byte(`[
		{"key":"team-a-key","name":"Team A","models":[" gpt-4o* "],"super":true},
		"legacy-key"
	]`)

	ctx, recorder := newJSONContext(t, http.MethodPut, "/api-keys", body)
	handler.PutAPIKeys(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if len(handler.cfg.APIKeyEntries) != 2 {
		t.Fatalf("APIKeyEntries length = %d, want 2", len(handler.cfg.APIKeyEntries))
	}
	if handler.cfg.APIKeyEntries[0].Key != "team-a-key" || !handler.cfg.APIKeyEntries[0].Super {
		t.Fatalf("first entry = %#v, want structured team-a-key super entry", handler.cfg.APIKeyEntries[0])
	}
	if got := handler.cfg.APIKeyEntries[0].Models; len(got) != 1 || got[0] != "gpt-4o*" {
		t.Fatalf("first entry models = %#v, want [gpt-4o*]", got)
	}
	if got := handler.cfg.APIKeys; len(got) != 2 || got[1] != "legacy-key" {
		t.Fatalf("APIKeys = %#v, want [team-a-key legacy-key]", got)
	}
}

func TestPatchAPIKeys_UpdatesStructuredEntryByIndex(t *testing.T) {
	handler := newAPIKeyPolicyHandler(t, &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeyEntries: []config.APIKeyEntry{
				{Key: "team-a-key"},
			},
		},
	})

	body := []byte(`{
		"index": 0,
		"value": {
			"key": "team-a-key",
			"name": "Team A",
			"models": ["claude-3-7-sonnet*"]
		}
	}`)

	ctx, recorder := newJSONContext(t, http.MethodPatch, "/api-keys", body)
	handler.PatchAPIKeys(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	entry := handler.cfg.APIKeyEntries[0]
	if entry.Name != "Team A" {
		t.Fatalf("entry.Name = %q, want Team A", entry.Name)
	}
	if got := entry.Models; len(got) != 1 || got[0] != "claude-3-7-sonnet*" {
		t.Fatalf("entry.Models = %#v, want [claude-3-7-sonnet*]", got)
	}
}

func TestDeleteAPIKeys_RemovesEntryByKey(t *testing.T) {
	handler := newAPIKeyPolicyHandler(t, &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeyEntries: []config.APIKeyEntry{
				{Key: "team-a-key"},
				{Key: "team-b-key"},
			},
		},
	})

	ctx, recorder := newJSONContext(t, http.MethodDelete, "/api-keys?key=team-a-key", nil)
	handler.DeleteAPIKeys(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if got := handler.cfg.APIKeys; len(got) != 1 || got[0] != "team-b-key" {
		t.Fatalf("APIKeys = %#v, want [team-b-key]", got)
	}
}
