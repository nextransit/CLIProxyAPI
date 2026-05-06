package util

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestIsOpenAICompatibilityAlias_IgnoresDisabledProviders(t *testing.T) {
	cfg := &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:     "disabled-provider",
				Disabled: true,
				Models: []config.OpenAICompatibilityModel{
					{Name: "deepseek-v4-pro", Alias: "deepseek/deepseek-v4-pro-free"},
				},
			},
			{
				Name: "enabled-provider",
				Models: []config.OpenAICompatibilityModel{
					{Name: "glm-5.1", Alias: "z-ai/glm-5.1"},
				},
			},
		},
	}

	if IsOpenAICompatibilityAlias("deepseek/deepseek-v4-pro-free", cfg) {
		t.Fatal("expected disabled provider alias to be ignored")
	}
	if !IsOpenAICompatibilityAlias("z-ai/glm-5.1", cfg) {
		t.Fatal("expected enabled provider alias to be discoverable")
	}
}

func TestGetOpenAICompatibilityConfig_IgnoresDisabledProviders(t *testing.T) {
	cfg := &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:     "disabled-provider",
				Disabled: true,
				Models: []config.OpenAICompatibilityModel{
					{Name: "deepseek-v4-pro", Alias: "deepseek/deepseek-v4-pro-free"},
				},
			},
			{
				Name: "enabled-provider",
				Models: []config.OpenAICompatibilityModel{
					{Name: "glm-5.1", Alias: "z-ai/glm-5.1"},
				},
			},
		},
	}

	compat, model := GetOpenAICompatibilityConfig("deepseek/deepseek-v4-pro-free", cfg)
	if compat != nil || model != nil {
		t.Fatalf("expected disabled provider to be ignored, got compat=%v model=%v", compat, model)
	}

	compat, model = GetOpenAICompatibilityConfig("z-ai/glm-5.1", cfg)
	if compat == nil || model == nil {
		t.Fatal("expected enabled provider to be returned")
	}
	if compat.Name != "enabled-provider" {
		t.Fatalf("compat name = %q, want %q", compat.Name, "enabled-provider")
	}
	if model.Alias != "z-ai/glm-5.1" {
		t.Fatalf("model alias = %q, want %q", model.Alias, "z-ai/glm-5.1")
	}
}
