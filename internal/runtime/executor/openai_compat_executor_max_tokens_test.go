package executor

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/tidwall/gjson"
)

func TestClampOpenAICompatMaxTokens_CapsDeepSeekV4Flash(t *testing.T) {
	input := []byte(`{"model":"deepseek-v4-flash","max_tokens":128000,"messages":[{"role":"user","content":"hi"}]}`)

	out := clampOpenAICompatMaxTokens(input, "deepseek-v4-flash", "openai-compatible")

	if got := gjson.GetBytes(out, "max_tokens").Int(); got != 65536 {
		t.Fatalf("max_tokens = %d, want 65536", got)
	}
}

func TestClampOpenAICompatMaxTokens_FloorsInvalidValue(t *testing.T) {
	input := []byte(`{"model":"deepseek-v4-flash","max_tokens":0,"messages":[{"role":"user","content":"hi"}]}`)

	out := clampOpenAICompatMaxTokens(input, "deepseek-v4-flash", "openai-compatible")

	if got := gjson.GetBytes(out, "max_tokens").Int(); got != 1 {
		t.Fatalf("max_tokens = %d, want 1", got)
	}
}

func TestClampOpenAICompatMaxTokens_UsesRegisteredMaxCompletionTokens(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	clientID := "test-openai-compat-max-tokens-client"
	modelID := "test-openai-compat-max-tokens-model"
	provider := "test-openai-compat-provider"
	reg.RegisterClient(clientID, provider, []*registry.ModelInfo{{
		ID:                  modelID,
		Type:                "openai",
		OwnedBy:             "test",
		Object:              "model",
		Created:             time.Now().Unix(),
		MaxCompletionTokens: 4096,
		UserDefined:         true,
	}})
	defer reg.UnregisterClient(clientID)

	input := []byte(`{"model":"test-openai-compat-max-tokens-model","max_tokens":32000,"messages":[{"role":"user","content":"hi"}]}`)

	out := clampOpenAICompatMaxTokens(input, modelID, provider)

	if got := gjson.GetBytes(out, "max_tokens").Int(); got != 4096 {
		t.Fatalf("max_tokens = %d, want 4096", got)
	}
}

func TestClampOpenAICompatMaxTokens_DeepSeekV4RegistryCannotRaiseHardLimit(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	clientID := "test-openai-compat-deepseek-v4-max-tokens-client"
	modelID := "deepseek-v4-flash"
	provider := "test-openai-compat-deepseek-provider"
	reg.RegisterClient(clientID, provider, []*registry.ModelInfo{{
		ID:                  modelID,
		Type:                "openai",
		OwnedBy:             "test",
		Object:              "model",
		Created:             time.Now().Unix(),
		MaxCompletionTokens: 128000,
		UserDefined:         true,
	}})
	defer reg.UnregisterClient(clientID)

	input := []byte(`{"model":"deepseek-v4-flash","max_tokens":128000,"messages":[{"role":"user","content":"hi"}]}`)

	out := clampOpenAICompatMaxTokens(input, modelID, provider)

	if got := gjson.GetBytes(out, "max_tokens").Int(); got != 65536 {
		t.Fatalf("max_tokens = %d, want 65536", got)
	}
}

func TestClampOpenAICompatMaxTokens_DoesNotAddMissingField(t *testing.T) {
	input := []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}]}`)

	out := clampOpenAICompatMaxTokens(input, "deepseek-v4-flash", "openai-compatible")

	if gjson.GetBytes(out, "max_tokens").Exists() {
		t.Fatalf("max_tokens should remain unset, got %s", gjson.GetBytes(out, "max_tokens").Raw)
	}
}
