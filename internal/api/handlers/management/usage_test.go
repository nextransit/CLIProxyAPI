package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
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
