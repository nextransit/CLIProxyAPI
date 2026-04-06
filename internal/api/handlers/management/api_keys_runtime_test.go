package management

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/access/apikeypolicy"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestGetAPIKeysRuntime_ReturnsPolicyRuntimeSnapshot(t *testing.T) {
	handler := newAPIKeyPolicyHandler(t, &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeyEntries: []config.APIKeyEntry{
				{
					Key: "team-a-key",
					Limits: config.APIKeyLimitSettings{
						Tokens: config.APIKeyTokenLimits{
							Lifetime: config.APIKeyLifetimeTokenLimit{Limit: 1000},
						},
					},
				},
			},
		},
	})
	manager := apikeypolicy.NewManager()
	manager.SetPolicies(handler.cfg.APIKeyEntries)
	manager.HandleUsage(context.Background(), coreusage.Record{
		APIKey: "team-a-key",
		Detail: coreusage.Detail{TotalTokens: 42},
	})
	handler.SetAPIKeyPolicyManager(manager)

	ctx, recorder := newJSONContext(t, http.MethodGet, "/api-keys/runtime", nil)
	handler.GetAPIKeysRuntime(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}

	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	items, ok := response["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("response items = %#v, want 1 runtime entry", response["items"])
	}
	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("runtime item type = %T, want map[string]any", items[0])
	}
	if got := item["key"]; got != "team-a-key" {
		t.Fatalf("runtime key = %#v, want team-a-key", got)
	}
	if got := int(item["lifetime-used"].(float64)); got != 42 {
		t.Fatalf("lifetime-used = %d, want 42", got)
	}
}

func TestResetAPIKeyTokens_ClearsTokenUsage(t *testing.T) {
	handler := newAPIKeyPolicyHandler(t, &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeyEntries: []config.APIKeyEntry{{Key: "team-a-key"}},
		},
	})
	manager := apikeypolicy.NewManager()
	manager.SetPolicies(handler.cfg.APIKeyEntries)
	manager.HandleUsage(context.Background(), coreusage.Record{
		APIKey: "team-a-key",
		Detail: coreusage.Detail{TotalTokens: 21},
	})
	handler.SetAPIKeyPolicyManager(manager)

	resetBody := []byte(`{"key":"team-a-key"}`)
	resetCtx, resetRecorder := newJSONContext(t, http.MethodPost, "/api-keys/reset-tokens", resetBody)
	handler.ResetAPIKeyTokens(resetCtx)
	if resetRecorder.Code != http.StatusOK {
		t.Fatalf("reset status = %d, want %d, body=%s", resetRecorder.Code, http.StatusOK, resetRecorder.Body.String())
	}

	runtimeCtx, runtimeRecorder := newJSONContext(t, http.MethodGet, "/api-keys/runtime", nil)
	handler.GetAPIKeysRuntime(runtimeCtx)
	if runtimeRecorder.Code != http.StatusOK {
		t.Fatalf("runtime status = %d, want %d", runtimeRecorder.Code, http.StatusOK)
	}
	var response map[string]any
	if err := json.Unmarshal(runtimeRecorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal(runtime) error = %v", err)
	}
	items := response["items"].([]any)
	item := items[0].(map[string]any)
	if got := int(item["lifetime-used"].(float64)); got != 0 {
		t.Fatalf("lifetime-used after reset = %d, want 0", got)
	}
}
