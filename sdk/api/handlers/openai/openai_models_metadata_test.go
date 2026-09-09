package openai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

func TestOpenAIModels_ReturnsContextLength(t *testing.T) {
	gin.SetMode(gin.TestMode)

	registry.GetGlobalRegistry().RegisterClient("test-auth", "test-provider", []*registry.ModelInfo{
		{
			ID:                  "test-model-with-context",
			Object:              "model",
			OwnedBy:             "test-org",
			ContextLength:       128000,
			MaxCompletionTokens: 32768,
		},
	})
	defer registry.GetGlobalRegistry().UnregisterClient("test-auth")

	handler := NewOpenAIAPIHandler(nil)
	router := gin.New()
	router.GET("/v1/models", handler.OpenAIModels)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var resp struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	found := false
	var model map[string]any
	for _, m := range resp.Data {
		if m["id"] == "test-model-with-context" {
			found = true
			model = m
			break
		}
	}
	if !found {
		t.Fatal("test-model-with-context not found in /v1/models response")
	}

	if ctxLen, ok := model["context_length"].(float64); !ok || ctxLen != 128000 {
		t.Errorf("expected context_length 128000, got %v", model["context_length"])
	}
	if maxTokens, ok := model["max_completion_tokens"].(float64); !ok || maxTokens != 32768 {
		t.Errorf("expected max_completion_tokens 32768, got %v", model["max_completion_tokens"])
	}
}

func TestOpenAIModels_ClientVersionReturnsCodexCatalog(t *testing.T) {
	gin.SetMode(gin.TestMode)

	registry.GetGlobalRegistry().RegisterClient("test-auth-codex", "test-provider", []*registry.ModelInfo{
		{
			ID:            "gpt-5.5",
			Object:        "model",
			OwnedBy:       "openai",
			DisplayName:   "GPT 5.5",
			Description:   "Frontier model for complex coding",
			ContextLength: 272000,
			Thinking:      &registry.ThinkingSupport{Levels: []string{"low", "medium", "high", "xhigh"}},
		},
		{
			ID:            "custom-codex-model-test",
			Object:        "model",
			OwnedBy:       "test-org",
			DisplayName:   "Custom Codex Model",
			Description:   "Custom model from registry",
			ContextLength: 123456,
			Thinking:      &registry.ThinkingSupport{Levels: []string{"none", "minimal", "low", "medium", "unsupported", "high"}},
		},
		{
			ID:      "MiniMax-M3",
			Object:  "model",
			OwnedBy: "minimax",
		},
	})
	defer registry.GetGlobalRegistry().UnregisterClient("test-auth-codex")

	handler := NewOpenAIAPIHandler(nil)
	router := gin.New()
	router.GET("/v1/models", handler.OpenAIModels)

	req := httptest.NewRequest(http.MethodGet, "/v1/models?client_version=0.150.1", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Models []map[string]any `json:"models"`
		Object string           `json:"object"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Object != "" {
		t.Fatalf("expected Codex catalog response, got object=%q", resp.Object)
	}
	if len(resp.Models) == 0 {
		t.Fatal("expected non-empty Codex catalog")
	}

	var custom map[string]any
	var minimax map[string]any
	for _, model := range resp.Models {
		if model["slug"] == "custom-codex-model-test" {
			custom = model
		}
		if model["slug"] == "MiniMax-M3" {
			minimax = model
		}
	}
	if custom == nil {
		t.Fatal("custom-codex-model-test not found in Codex catalog")
	}
	if custom["display_name"] != "Custom Codex Model" {
		t.Fatalf("unexpected display_name: %v", custom["display_name"])
	}
	if custom["context_window"] != float64(123456) {
		t.Fatalf("unexpected context_window: %v", custom["context_window"])
	}

	levels, ok := custom["supported_reasoning_levels"].([]any)
	if !ok || len(levels) != 4 {
		t.Fatalf("unexpected supported_reasoning_levels: %#v", custom["supported_reasoning_levels"])
	}
	if minimax == nil {
		t.Fatal("MiniMax-M3 not found in Codex catalog")
	}
	if minimax["display_name"] != "MiniMax-M3" {
		t.Fatalf("unexpected MiniMax-M3 display_name: %v", minimax["display_name"])
	}
}
