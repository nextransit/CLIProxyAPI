package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

type stubUsageStore struct {
	saveCalls int
	snapshot  usage.StatisticsSnapshot
	load      usage.StatisticsSnapshot
}

func (s *stubUsageStore) Save(snapshot usage.StatisticsSnapshot) error {
	s.saveCalls++
	s.snapshot = snapshot
	return nil
}

func (s *stubUsageStore) Load() (usage.StatisticsSnapshot, error) {
	return s.load, nil
}

func (s *stubUsageStore) Path() string { return "/tmp/usage.snapshot" }

func TestImportUsageStatistics_PersistsMergedSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)

	if err := usage.InitializePersistence(t.TempDir()); err != nil {
		t.Fatalf("InitializePersistence() error = %v", err)
	}

	store := &stubUsageStore{}
	usage.SetUsageStore(store)

	handler := NewHandler(&config.Config{}, "", nil)
	apiKey := "codex-import-" + time.Now().UTC().Format("20060102150405.000000000")

	payload := usageImportPayload{
		Version: 1,
		Usage: usage.StatisticsSnapshot{
			APIs: map[string]usage.APISnapshot{
				apiKey: {
					Models: map[string]usage.ModelSnapshot{
						"gpt-5-codex": {
							Details: []usage.RequestDetail{
								{
									Timestamp: time.Now().UTC(),
									Source:    "import-test",
									Tokens: usage.TokenStats{
										InputTokens:  40,
										OutputTokens: 80,
										TotalTokens:  120,
									},
								},
							},
						},
					},
				},
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/usage/import", strings.NewReader(string(body)))
	ctx.Request.Header.Set("Content-Type", "application/json")

	handler.ImportUsageStatistics(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", recorder.Code, http.StatusOK)
	}
	if store.saveCalls == 0 {
		t.Fatal("expected Save() to be called after import")
	}
	if got := store.snapshot.APIs[apiKey].Models["gpt-5-codex"].TotalRequests; got != 1 {
		t.Fatalf("persisted model requests = %d, want 1", got)
	}

	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("Unmarshal(response) error = %v", err)
	}
	if persisted, ok := response["persisted"].(bool); !ok || !persisted {
		t.Fatalf("persisted response = %#v, want true", response["persisted"])
	}
	if added, ok := response["added"].(float64); !ok || added < 1 {
		t.Fatalf("added response = %#v, want at least 1", response["added"])
	}
}

func TestGetUsageStatistics_RestoresSnapshotFromStoreWhenMemoryEmpty(t *testing.T) {
	gin.SetMode(gin.TestMode)

	if err := usage.InitializePersistence(t.TempDir()); err != nil {
		t.Fatalf("InitializePersistence() error = %v", err)
	}

	now := time.Now().UTC()
	store := &stubUsageStore{
		load: usage.StatisticsSnapshot{
			APIs: map[string]usage.APISnapshot{
				"restore-key": {
					Models: map[string]usage.ModelSnapshot{
						"gpt-5.4": {
							Details: []usage.RequestDetail{
								{
									Timestamp: now,
									Source:    "restore-test",
									Tokens: usage.TokenStats{
										InputTokens:  11,
										OutputTokens: 7,
										TotalTokens:  18,
									},
								},
							},
						},
					},
				},
			},
		},
	}
	usage.SetUsageStore(store)

	handler := NewHandler(&config.Config{}, "", nil)
	freshStats := usage.NewRequestStatistics()
	handler.SetUsageStatistics(freshStats)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage", nil)

	handler.GetUsageStatistics(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d, body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	snapshot := freshStats.Snapshot()
	if snapshot.TotalRequests != 1 {
		t.Fatalf("freshStats.TotalRequests = %d, want 1", snapshot.TotalRequests)
	}
	if snapshot.TotalTokens != 18 {
		t.Fatalf("freshStats.TotalTokens = %d, want 18", snapshot.TotalTokens)
	}

	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("Unmarshal(response) error = %v", err)
	}
	usagePayload, ok := response["usage"].(map[string]any)
	if !ok {
		t.Fatalf("response.usage type = %T, want map[string]any", response["usage"])
	}
	if got := int(usagePayload["total_requests"].(float64)); got != 1 {
		t.Fatalf("response usage.total_requests = %d, want 1", got)
	}
}

func TestFilterUsageSnapshotByTimeRange(t *testing.T) {
	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	snapshot := usage.StatisticsSnapshot{
		APIs: map[string]usage.APISnapshot{
			"test-key": {
				Models: map[string]usage.ModelSnapshot{
					"gpt-5.5": {
						Details: []usage.RequestDetail{
							{
								Timestamp: now.Add(-2 * time.Hour),
								Tokens:    usage.TokenStats{TotalTokens: 100},
							},
							{
								Timestamp: now.Add(-48 * time.Hour),
								Failed:    true,
								Tokens:    usage.TokenStats{TotalTokens: 900},
							},
						},
					},
				},
			},
		},
	}

	filtered, ok := filterUsageSnapshotByTimeRange(snapshot, "24h", now)
	if !ok {
		t.Fatalf("filter ok = false, want true")
	}
	if filtered.TotalRequests != 1 {
		t.Fatalf("total_requests = %d, want 1", filtered.TotalRequests)
	}
	if filtered.TotalTokens != 100 {
		t.Fatalf("total_tokens = %d, want 100", filtered.TotalTokens)
	}
	if filtered.FailureCount != 0 || filtered.SuccessCount != 1 {
		t.Fatalf("success/failure = %d/%d, want 1/0", filtered.SuccessCount, filtered.FailureCount)
	}
}

func TestFilterUsageSnapshotByTimeRangeTodayUsesLocalDay(t *testing.T) {
	localZone := time.FixedZone("Asia/Shanghai", 8*60*60)
	now := time.Date(2026, 6, 4, 10, 0, 0, 0, localZone)
	snapshot := usage.StatisticsSnapshot{
		APIs: map[string]usage.APISnapshot{
			"test-key": {
				Models: map[string]usage.ModelSnapshot{
					"gpt-5.5": {
						Details: []usage.RequestDetail{
							{
								Timestamp: time.Date(2026, 6, 4, 7, 30, 0, 0, localZone),
								Tokens:    usage.TokenStats{TotalTokens: 100},
							},
							{
								Timestamp: time.Date(2026, 6, 4, 9, 30, 0, 0, localZone),
								Tokens:    usage.TokenStats{TotalTokens: 200},
							},
							{
								Timestamp: time.Date(2026, 6, 3, 23, 30, 0, 0, localZone),
								Tokens:    usage.TokenStats{TotalTokens: 900},
							},
						},
					},
				},
			},
		},
	}

	filtered, ok := filterUsageSnapshotByTimeRange(snapshot, "today", now)
	if !ok {
		t.Fatalf("filter ok = false, want true")
	}
	if filtered.TotalRequests != 2 {
		t.Fatalf("total_requests = %d, want 2", filtered.TotalRequests)
	}
	if filtered.TotalTokens != 300 {
		t.Fatalf("total_tokens = %d, want 300", filtered.TotalTokens)
	}
	if got := filtered.RequestsByDay["2026-06-04"]; got != 2 {
		t.Fatalf("requests_by_day[2026-06-04] = %d, want 2", got)
	}
	if got := filtered.TokensByDay["2026-06-04"]; got != 300 {
		t.Fatalf("tokens_by_day[2026-06-04] = %d, want 300", got)
	}
}

func TestGetUsageDashboard_RendersAggregatePayload(t *testing.T) {
	gin.SetMode(gin.TestMode)

	handler := NewHandler(&config.Config{}, "", nil)
	stats := usage.NewRequestStatistics()
	handler.SetUsageStatistics(stats)

	now := time.Now().UTC()
	for i, when := range []time.Duration{-10 * time.Minute, -25 * time.Minute, -3 * time.Hour} {
		stats.Record(context.Background(), coreusage.Record{
			Provider:    "test",
			Model:       "gpt-5.4",
			APIKey:      "test-key",
			AuthIndex:   "test-auth",
			Source:      "test-source",
			RequestID:   "rid-" + string(rune('a'+i)),
			StatusCode:  200,
			RequestedAt: now.Add(when),
			Latency:     200 * time.Millisecond,
			Failed:      false,
			Detail: coreusage.Detail{
				InputTokens:  10,
				OutputTokens: 5,
				TotalTokens:  15,
			},
		})
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage/dashboard?window=24h&bucket_count=12&model_top=5&latest_count=7", nil)
	handler.GetUsageDashboard(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200, body=%s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("Unmarshal(response) error = %v", err)
	}
	dash, ok := response["dashboard"].(map[string]any)
	if !ok {
		t.Fatalf("response.dashboard type = %T, want map", response["dashboard"])
	}
	if totalRequests := int(dash["window_requests"].(float64)); totalRequests != 3 {
		t.Fatalf("dashboard.window_requests = %d, want 3", totalRequests)
	}
	if flowBuckets, ok := dash["flow_buckets"].([]any); !ok || len(flowBuckets) != 12 {
		t.Fatalf("dashboard.flow_buckets length/type = %d %T, want 12 []any", len(flowBuckets), dash["flow_buckets"])
	}
	if modelTop, ok := dash["model_top"].([]any); !ok || len(modelTop) != 1 {
		t.Fatalf("dashboard.model_top length/type = %d %T, want 1 []any", len(modelTop), dash["model_top"])
	}
	if size := recorder.Body.Len(); size > 4_000 {
		t.Fatalf("dashboard payload size = %d bytes, want < 2000", size)
	}
}

// TestGetUsageDashboard_ReturnsServiceUnavailableWhenCancelled ensures the
// dashboard handler exposes a clean 503 when its ctx is cancelled before the
// snapshot completes.
func TestGetUsageDashboard_ReturnsServiceUnavailableWhenCancelled(t *testing.T) {
	gin.SetMode(gin.TestMode)

	handler := NewHandler(&config.Config{}, "", nil)
	stats := usage.NewRequestStatistics()
	handler.SetUsageStatistics(stats)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage/dashboard?window=24h", nil)
	cancelledCtx, cancel := context.WithCancel(ctx.Request.Context())
	cancel()
	ctx.Request = ctx.Request.WithContext(cancelledCtx)

	handler.GetUsageDashboard(ctx)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d (body=%s)", recorder.Code, http.StatusServiceUnavailable, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "dashboard_build_cancelled") {
		t.Fatalf("body = %q, want it to contain dashboard_build_cancelled", recorder.Body.String())
	}
}
