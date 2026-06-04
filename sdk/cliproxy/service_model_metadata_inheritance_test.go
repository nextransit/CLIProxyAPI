package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
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

func TestRegisterModelsForAuth_OpenAICompatibilityModelMetadataOverride(t *testing.T) {
	service := &Service{
		cfg: &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{{
				Name: "minimax",
				Models: []config.OpenAICompatibilityModel{{
					Name:                "MiniMax-M3",
					Alias:               "MiniMax-M3",
					ContextLength:       1000000,
					MaxCompletionTokens: 32768,
				}},
			}},
		},
	}
	auth := &coreauth.Auth{
		ID:         "auth-openai-compat-meta",
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
	for _, m := range models {
		if m == nil || m.ID != "MiniMax-M3" {
			continue
		}
		if m.ContextLength != 1000000 {
			t.Fatalf("context length = %d, want 1000000", m.ContextLength)
		}
		if m.MaxCompletionTokens != 32768 {
			t.Fatalf("max completion tokens = %d, want 32768", m.MaxCompletionTokens)
		}
		return
	}
	t.Fatal("expected to find registered model MiniMax-M3")
}

func TestRegisterModelsForAuth_InheritsMiniMaxM3StaticMetadata(t *testing.T) {
	service := &Service{
		cfg: &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{{
				Name: "minimax",
				Models: []config.OpenAICompatibilityModel{{
					Name:  "MiniMax-M3",
					Alias: "MiniMax-M3",
				}},
			}},
		},
	}
	auth := &coreauth.Auth{
		ID:         "auth-minimax-m3-static-meta",
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
	for _, m := range models {
		if m == nil || m.ID != "MiniMax-M3" {
			continue
		}
		if m.ContextLength != 1000000 {
			t.Fatalf("context length = %d, want 1000000", m.ContextLength)
		}
		if m.MaxCompletionTokens != 32768 {
			t.Fatalf("max completion tokens = %d, want 32768", m.MaxCompletionTokens)
		}
		return
	}
	t.Fatal("expected to find registered model MiniMax-M3")
}

func TestRegisterModelsForAuth_ClaudeConfigModelKeepsThinkingMetadata(t *testing.T) {
	service := &Service{
		cfg: &config.Config{
			ClaudeKey: []config.ClaudeKey{{
				APIKey: "test-key",
				Models: []config.ClaudeModel{{
					Name:                "MiniMax-M2.7-highspeed",
					Alias:               "minimax-claude/MiniMax-M2.7-highspeed",
					ContextLength:       204800,
					MaxCompletionTokens: 8192,
					Thinking:            &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}},
				}},
			}},
		},
	}
	auth := &coreauth.Auth{
		ID:         "auth-claude-thinking-meta",
		Provider:   "claude",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"auth_kind": "api_key", "api_key": "test-key"},
	}

	modelRegistry := GlobalModelRegistry()
	modelRegistry.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(auth)

	models := modelRegistry.GetAvailableModelsByProvider("claude")
	for _, m := range models {
		if m == nil || m.ID != "minimax-claude/MiniMax-M2.7-highspeed" {
			continue
		}
		if m.Thinking == nil {
			t.Fatal("expected thinking metadata")
		}
		if got := m.Thinking.Levels; len(got) != 3 || got[0] != "low" || got[1] != "medium" || got[2] != "high" {
			t.Fatalf("thinking levels = %#v, want [low medium high]", got)
		}
		if m.ContextLength != 204800 {
			t.Fatalf("context length = %d, want 204800", m.ContextLength)
		}
		if m.MaxCompletionTokens != 8192 {
			t.Fatalf("max completion tokens = %d, want 8192", m.MaxCompletionTokens)
		}
		return
	}
	t.Fatal("expected to find registered minimax-claude/MiniMax-M2.7-highspeed")
}
