package cliproxy

import (
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

// TestRegisterModelsForAuth_InheritsStaticModelMetadata verifies that when an
// OpenAI-compatible model name matches a known static model, the static
// context window metadata (ContextLength, MaxCompletionTokens, etc.) is
// inherited by the registered model info.
func TestRegisterModelsForAuth_InheritsStaticModelMetadata(t *testing.T) {
	service := &Service{
		cfg: &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{{
				Name: "minimax",
				Models: []config.OpenAICompatibilityModel{
					{Name: "gpt-5.5", Alias: "gpt-5.5"}, // matches static model definition
				},
			}},
		},
	}
	auth := &coreauth.Auth{
		ID:         "auth-static-meta",
		Provider:   "openai-compatibility",
		Status:     coreauth.StatusActive,
		Label:      "minimax",
		Attributes: map[string]string{"auth_kind": "api_key"},
	}

	registry := GlobalModelRegistry()
	registry.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		registry.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(auth)

	models := registry.GetAvailableModelsByProvider("minimax")
	found := false
	for _, m := range models {
		if m != nil && m.ID == "gpt-5.5" {
			found = true
			// gpt-5.5 static definition has ContextLength=272000, MaxCompletionTokens=128000
			if m.ContextLength != 272000 {
				t.Errorf("expected ContextLength 272000, got %d", m.ContextLength)
			}
			if m.MaxCompletionTokens != 128000 {
				t.Errorf("expected MaxCompletionTokens 128000, got %d", m.MaxCompletionTokens)
			}
			if m.OwnedBy != "minimax" {
				t.Errorf("expected OwnedBy minimax, got %s", m.OwnedBy)
			}
			break
		}
	}
	if !found {
		t.Fatal("expected to find registered model gpt-5.5")
	}
}

// TestRegisterModelsForAuth_UnknownModelHasNoStaticMeta verifies that a model
// name with no static definition (e.g. a MiniMax-specific model) does not get
// inherited metadata, keeping its zero values.
func TestRegisterModelsForAuth_UnknownModelHasNoStaticMeta(t *testing.T) {
	service := &Service{
		cfg: &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{{
				Name: "minimax",
				Models: []config.OpenAICompatibilityModel{
					{Name: "MiniMax-M2.7-highspeed", Alias: "MiniMax-M2.7-highspeed"},
				},
			}},
		},
	}
	auth := &coreauth.Auth{
		ID:         "auth-unknown-meta",
		Provider:   "openai-compatibility",
		Status:     coreauth.StatusActive,
		Label:      "minimax",
		Attributes: map[string]string{"auth_kind": "api_key"},
	}

	registry := GlobalModelRegistry()
	registry.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		registry.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(auth)

	models := registry.GetAvailableModelsByProvider("minimax")
	found := false
	for _, m := range models {
		if m != nil && m.ID == "MiniMax-M2.7-highspeed" {
			found = true
			// No static definition for MiniMax-M2.7-highspeed; fields stay zero
			if m.ContextLength != 0 {
				t.Errorf("expected ContextLength 0 for unknown model, got %d", m.ContextLength)
			}
			if m.MaxCompletionTokens != 0 {
				t.Errorf("expected MaxCompletionTokens 0 for unknown model, got %d", m.MaxCompletionTokens)
			}
			break
		}
	}
	if !found {
		t.Fatal("expected to find registered model MiniMax-M2.7-highspeed")
	}
}
