package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestQueryAIOps_BasicWindowModelAndRegistration(t *testing.T) {
	gin.SetMode(gin.TestMode)

	now := time.Now().UTC()
	stats := usage.NewRequestStatistics()
	record := func(rec coreusage.Record) {
		stats.Record(context.Background(), rec)
	}
	record(coreusage.Record{
		Model:       "gpt-5.5",
		Source:      "openai",
		RequestedAt: now.Add(-2 * time.Hour),
		Failed:      false,
		Detail: coreusage.Detail{
			InputTokens:  100,
			OutputTokens: 20,
			CachedTokens: 50,
			TotalTokens:  170,
		},
	})
	record(coreusage.Record{
		Model:       "gpt-5.5",
		Source:      "openai",
		RequestedAt: now.Add(-1 * time.Hour),
		Failed:      true,
		Detail: coreusage.Detail{
			InputTokens:  30,
			OutputTokens: 10,
			CachedTokens: 10,
			TotalTokens:  50,
		},
	})
	record(coreusage.Record{
		Model:       "gpt-5.4",
		Source:      "openai",
		RequestedAt: now.Add(-12 * time.Hour),
		Failed:      false,
		Detail: coreusage.Detail{
			InputTokens: 20, OutputTokens: 5, TotalTokens: 25,
		},
	})
	record(coreusage.Record{
		Model:       "gpt-5.5",
		Source:      "openai",
		RequestedAt: now.Add(-400 * 24 * time.Hour),
		Failed:      false,
		Detail: coreusage.Detail{
			InputTokens: 10, OutputTokens: 2, TotalTokens: 12,
		},
	})

	manager := coreauth.NewManager(nil, nil, nil)
	_, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:            "recent-abnormal",
		Provider:      "codex",
		Status:        coreauth.StatusActive,
		StatusMessage: "quota exhausted",
		CreatedAt:     now.Add(-3 * time.Hour),
		UpdatedAt:     now.Add(-2 * time.Hour),
	})
	if err != nil {
		t.Fatalf("register recent abnormal auth: %v", err)
	}
	_, err = manager.Register(context.Background(), &coreauth.Auth{
		ID:        "recent-normal",
		Provider:  "codex",
		Status:    coreauth.StatusActive,
		CreatedAt: now.Add(-4 * time.Hour),
		UpdatedAt: now.Add(-1 * time.Hour),
	})
	if err != nil {
		t.Fatalf("register recent normal auth: %v", err)
	}

	handler := NewHandler(&config.Config{}, "", manager)
	handler.SetUsageStatistics(stats)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(
		http.MethodPost,
		"/v0/management/ai-ops/query",
		strings.NewReader(`{"time_range":"365d","model":"gpt-5.5"}`),
	)
	ctx.Request.Header.Set("Content-Type", "application/json")

	handler.QueryAIOps(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200, body=%s", recorder.Code, recorder.Body.String())
	}

	var response aiOpsQueryResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if response.Overview.TotalRequests != 2 {
		t.Fatalf("overview.total_requests = %d, want 2", response.Overview.TotalRequests)
	}
	if response.Overview.FailedRequests != 1 {
		t.Fatalf("overview.failed_requests = %d, want 1", response.Overview.FailedRequests)
	}
	if response.Overview.TotalTokens != 220 {
		t.Fatalf("overview.total_tokens = %d, want 220", response.Overview.TotalTokens)
	}
	if response.Overview.CacheHitRate <= 31 || response.Overview.CacheHitRate >= 32 {
		t.Fatalf("overview.cache_hit_rate = %v, want around 31.58", response.Overview.CacheHitRate)
	}
	if len(response.Models) != 1 || response.Models[0].Model != "gpt-5.5" {
		t.Fatalf("models = %#v, want only gpt-5.5", response.Models)
	}
	if response.Network.WindowRequestCount != 2 {
		t.Fatalf("network.window_request_count = %d, want 2", response.Network.WindowRequestCount)
	}
	if len(response.Network.FailedByModel) == 0 || response.Network.FailedByModel[0].Name != "gpt-5.5" {
		t.Fatalf("network.failed_by_model = %#v, want gpt-5.5 entry", response.Network.FailedByModel)
	}
	if response.Registrations.RegistrationsLast24h != 2 {
		t.Fatalf("registrations_last_24h = %d, want 2", response.Registrations.RegistrationsLast24h)
	}
	if response.Registrations.AbnormalRegistrations24h != 1 {
		t.Fatalf("abnormal_registrations_24h = %d, want 1", response.Registrations.AbnormalRegistrations24h)
	}
}

func TestQueryAIOps_AIAnalysis(t *testing.T) {
	gin.SetMode(gin.TestMode)

	aiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %s, want /v1/chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-ai-key" {
			t.Fatalf("authorization = %q, want Bearer test-ai-key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"这是 AI 分析结果"}}]}`))
	}))
	defer aiServer.Close()

	stats := usage.NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		Model:       "gpt-5.5",
		Source:      "openai",
		RequestedAt: time.Now().UTC().Add(-15 * time.Minute),
		Detail: coreusage.Detail{
			InputTokens: 20, OutputTokens: 10, TotalTokens: 30,
		},
	})

	handler := NewHandler(&config.Config{}, "", nil)
	handler.SetUsageStatistics(stats)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(
		http.MethodPost,
		"/v0/management/ai-ops/query",
		strings.NewReader(fmt.Sprintf(`{
			"time_range":"24h",
			"query":"请给出风险和建议",
			"ai":{
				"enabled":true,
				"api_key":"test-ai-key",
				"base_url":"%s",
				"model":"gpt-5.3-codex",
				"max_tokens":256
			}
		}`, aiServer.URL)),
	)
	ctx.Request.Header.Set("Content-Type", "application/json")

	handler.QueryAIOps(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200, body=%s", recorder.Code, recorder.Body.String())
	}

	var response aiOpsQueryResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if response.AI == nil {
		t.Fatalf("response.ai is nil")
	}
	if response.AI.Status != "success" {
		t.Fatalf("response.ai.status = %q, want success, error=%q", response.AI.Status, response.AI.Error)
	}
	if !strings.Contains(response.AI.Analysis, "AI 分析结果") {
		t.Fatalf("response.ai.analysis = %q, want analysis text", response.AI.Analysis)
	}
}

func TestAIOpsAndTextOpsDefaultLLMModel(t *testing.T) {
	if aiOpsDefaultAIModel != "MiniMax-M2.7-highspeed" {
		t.Fatalf("aiOpsDefaultAIModel = %q, want MiniMax-M2.7-highspeed", aiOpsDefaultAIModel)
	}
	if textOpsDefaultRouterModel != "MiniMax-M2.7-highspeed" {
		t.Fatalf("textOpsDefaultRouterModel = %q, want MiniMax-M2.7-highspeed", textOpsDefaultRouterModel)
	}
	if textOpsDefaultPresenterModel != "MiniMax-M2.7-highspeed" {
		t.Fatalf("textOpsDefaultPresenterModel = %q, want MiniMax-M2.7-highspeed", textOpsDefaultPresenterModel)
	}
}

func TestBuildAIOpsQueryResponseMatchesModelAlias(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	stats := usage.NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		Model:       "minimax/minimax-m3",
		Source:      "openai",
		RequestedAt: now.Add(-time.Hour),
		Detail: coreusage.Detail{
			InputTokens: 100, OutputTokens: 50, TotalTokens: 150,
		},
	})

	response := (&Handler{}).buildAIOpsQueryResponse(
		stats.Snapshot(),
		aiOpsQueryRequest{Model: "MiniMax-M3"},
		aiOpsRangeSpec{RangeKey: "24h", StartAt: now.Add(-24 * time.Hour), EndAt: now},
	)
	if response.Overview.TotalRequests != 1 {
		t.Fatalf("overview.total_requests = %d, want 1", response.Overview.TotalRequests)
	}
	if len(response.Models) != 1 || response.Models[0].Model != "minimax/minimax-m3" {
		t.Fatalf("models = %#v, want minimax/minimax-m3", response.Models)
	}
}
