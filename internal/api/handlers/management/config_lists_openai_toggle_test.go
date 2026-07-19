package management

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestPatchOpenAICompat_TogglesDisabledState(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{
				{
					Name:     "Ali",
					BaseURL:  "https://dashscope.aliyuncs.com/compatible-mode/v1",
					Disabled: false,
				},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(
		http.MethodPatch,
		"/v0/management/openai-compatibility",
		bytes.NewBufferString(`{"index":0,"value":{"disabled":true}}`),
	)
	c.Request.Header.Set("Content-Type", "application/json")

	h.PatchOpenAICompat(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !h.cfg.OpenAICompatibility[0].Disabled {
		t.Fatal("expected provider to be disabled")
	}
}

func TestPatchOpenAICompat_UsesNameWhenIndexMismatch(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{
				{
					Name:     "Ali",
					BaseURL:  "https://dashscope.aliyuncs.com/compatible-mode/v1",
					Disabled: false,
				},
				{
					Name:     "MiniMax",
					BaseURL:  "https://api.minimaxi.com/v1",
					Disabled: false,
				},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(
		http.MethodPatch,
		"/v0/management/openai-compatibility",
		bytes.NewBufferString(`{"index":0,"name":"MiniMax","value":{"disabled":true}}`),
	)
	c.Request.Header.Set("Content-Type", "application/json")

	h.PatchOpenAICompat(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if h.cfg.OpenAICompatibility[0].Disabled {
		t.Fatal("expected index 0 provider to remain enabled")
	}
	if !h.cfg.OpenAICompatibility[1].Disabled {
		t.Fatal("expected MiniMax provider to be disabled")
	}
}

func TestPatchOpenAICompat_UpdatesSessionAffinityMaxRequests(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{
				{
					Name:    "SenseNova",
					BaseURL: "https://token.sensenova.cn/v1",
				},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(
		http.MethodPatch,
		"/v0/management/openai-compatibility",
		bytes.NewBufferString(`{"index":0,"value":{"session-affinity-max-requests":5}}`),
	)
	c.Request.Header.Set("Content-Type", "application/json")

	h.PatchOpenAICompat(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	got := h.cfg.OpenAICompatibility[0].SessionAffinityMaxRequests
	if got == nil || *got != 5 {
		t.Fatalf("session affinity max requests = %v, want 5", got)
	}
}
