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
