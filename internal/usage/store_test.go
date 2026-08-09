package usage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestNewFileStore_UsesNonJSONPersistenceFile(t *testing.T) {
	t.Parallel()

	store := NewFileStore(t.TempDir())
	if got, want := filepath.Ext(store.Path()), ".snapshot"; got != want {
		t.Fatalf("file extension = %q, want %q", got, want)
	}
}

func TestFileStore_SaveAndLoadSnapshot(t *testing.T) {
	t.Parallel()

	store := NewFileStore(t.TempDir())
	want := StatisticsSnapshot{
		TotalRequests: 7,
		SuccessCount:  6,
		FailureCount:  1,
		TotalTokens:   1234,
		APIs: map[string]APISnapshot{
			"codex-key-a": {
				TotalRequests: 7,
				TotalTokens:   1234,
				Models: map[string]ModelSnapshot{
					"gpt-5-codex": {
						TotalRequests: 7,
						TotalTokens:   1234,
					},
				},
			},
		},
	}

	if err := store.Save(want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if got.TotalRequests != want.TotalRequests {
		t.Fatalf("TotalRequests = %d, want %d", got.TotalRequests, want.TotalRequests)
	}
	if got.TotalTokens != want.TotalTokens {
		t.Fatalf("TotalTokens = %d, want %d", got.TotalTokens, want.TotalTokens)
	}
	if got.APIs["codex-key-a"].Models["gpt-5-codex"].TotalRequests != 7 {
		t.Fatalf("model total requests = %d, want 7", got.APIs["codex-key-a"].Models["gpt-5-codex"].TotalRequests)
	}
}

func TestRestoreStatisticsFromStore_RestoresIntoEmptyStats(t *testing.T) {
	t.Parallel()

	store := NewFileStore(t.TempDir())
	snapshot := StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"restore-key": {
				Models: map[string]ModelSnapshot{
					"gpt-5.4": {
						Details: []RequestDetail{
							{
								Timestamp: mustParseTestTime(t, "2026-03-27T13:50:00Z"),
								Source:    "restore-test",
								Tokens: TokenStats{
									InputTokens:  9,
									OutputTokens: 3,
									TotalTokens:  12,
								},
							},
						},
					},
				},
			},
		},
	}
	if err := store.Save(snapshot); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	stats := NewRequestStatistics()
	restored, err := RestoreStatisticsFromStore(stats, store)
	if err != nil {
		t.Fatalf("RestoreStatisticsFromStore() error = %v", err)
	}
	if !restored {
		t.Fatal("expected RestoreStatisticsFromStore() to restore snapshot")
	}

	got := stats.Snapshot()
	if got.TotalRequests != 1 {
		t.Fatalf("TotalRequests = %d, want 1", got.TotalRequests)
	}
	if got.TotalTokens != 12 {
		t.Fatalf("TotalTokens = %d, want 12", got.TotalTokens)
	}
}

func mustParseTestTime(t *testing.T, raw string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t.Fatalf("time.Parse(%q) error = %v", raw, err)
	}
	return ts
}

func TestAggregateFileStore_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	s := NewAggregateFileStore(tmp)
	in := AggregateSnapshot{
		Version:       1,
		TotalRequests: 1234,
		SuccessCount:  1200,
		FailureCount:  34,
		TotalTokens:   9_000_000,
		RequestsByDay: map[string]int64{"2026-08-08": 1200, "2026-08-07": 34},
		TokensByDay:   map[string]int64{"2026-08-08": 8_900_000, "2026-08-07": 100_000},
		ExportedAt:    time.Now().UTC(),
	}
	if err := s.Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.TotalRequests != in.TotalRequests || out.TotalTokens != in.TotalTokens {
		t.Fatalf("counters: got %+v want %+v", out, in)
	}
	if out.RequestsByDay["2026-08-08"] != 1200 {
		t.Fatalf("day buckets: %+v", out.RequestsByDay)
	}
}

func TestRecentEventsFileStore_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	s := NewRecentEventsFileStore(tmp)
	budget := int64(8192)
	in := RecentEventsSnapshot{
		Version: 2,
		Events: []UsageEvent{
			{
				ID:        1,
				APIKey:    "k",
				Model:     "m",
				Source:    "source-a",
				AuthIndex: "auth-a",
				RequestID: "request-a",
				Tokens: TokenSummary{
					Input:     1,
					Output:    2,
					Reasoning: 3,
					Cached:    4,
					Total:     10,
				},
				Thinking:    &Thinking{Mode: "budget", Budget: &budget},
				RequestedAt: time.Now().UTC(),
			},
			{ID: 2, APIKey: "k", Model: "m2", Failed: true, Tokens: TokenSummary{Total: 5}, RequestedAt: time.Now().UTC()},
		},
	}
	if err := s.Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(out.Events) != 2 || out.Events[0].Model != "m" || out.Events[1].Model != "m2" {
		t.Fatalf("events: %+v", out.Events)
	}
	event := out.Events[0]
	if event.Source != "source-a" || event.AuthIndex != "auth-a" || event.RequestID != "request-a" {
		t.Fatalf("event identity fields = %+v", event)
	}
	if event.Tokens.Reasoning != 3 || event.Tokens.Cached != 4 {
		t.Fatalf("event token fields = %+v", event.Tokens)
	}
	if event.Thinking == nil || event.Thinking.Budget == nil || *event.Thinking.Budget != budget {
		t.Fatalf("event thinking = %+v", event.Thinking)
	}
}

func TestPersistentLoggerPluginLoadRestoresDetailsBeforeAggregateSnapshot(t *testing.T) {
	tmp := t.TempDir()
	now := time.Now().Add(-time.Minute)

	source := NewRequestStatistics()
	budget := int64(4096)
	source.Record(context.Background(), coreusage.Record{
		APIKey:    "key",
		Model:     "model",
		Source:    "source",
		AuthIndex: "auth-index",
		RequestID: "request-id",
		Detail: coreusage.Detail{
			InputTokens:     7,
			OutputTokens:    5,
			ReasoningTokens: 3,
			CachedTokens:    2,
			TotalTokens:     17,
			Thinking:        &coreusage.Thinking{Mode: "budget", Budget: &budget},
		},
		RequestedAt: now,
		StatusCode:  200,
	})

	legacyStore := NewFileStore(tmp)
	legacy := source.Snapshot()
	legacy.TotalRequests = 999
	legacy.TotalTokens = 9999
	legacy.SuccessCount = 900
	legacy.FailureCount = 99
	legacyAPI := legacy.APIs["key"]
	legacyAPI.TotalRequests = 999
	legacyAPI.TotalTokens = 9999
	legacyModel := legacyAPI.Models["model"]
	legacyModel.TotalRequests = 999
	legacyModel.TotalTokens = 9999
	legacyAPI.Models["model"] = legacyModel
	legacy.APIs["key"] = legacyAPI
	if err := legacyStore.Save(legacy); err != nil {
		t.Fatalf("save legacy snapshot: %v", err)
	}
	aggregateStore := NewAggregateFileStore(tmp)
	if err := aggregateStore.Save(source.AggregateSnapshot()); err != nil {
		t.Fatalf("save aggregate snapshot: %v", err)
	}
	recentStore := NewRecentEventsFileStore(tmp)
	if err := recentStore.Save(source.RecentEventsSnapshot()); err != nil {
		t.Fatalf("save recent events: %v", err)
	}

	target := NewRequestStatistics()
	plugin := NewPersistentLoggerPlugin(legacyStore)
	plugin.LoggerPlugin = &LoggerPlugin{stats: target}
	plugin.AttachAggregateStores(aggregateStore, recentStore)
	if err := plugin.Load(); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	snapshot := target.Snapshot()
	model := snapshot.APIs["key"].Models["model"]
	if got := len(model.Details); got != 1 {
		t.Fatalf("details length = %d, want 1", got)
	}
	detail := model.Details[0]
	if detail.Source != "source" || detail.AuthIndex != "auth-index" || detail.RequestID != "request-id" {
		t.Fatalf("restored detail identity = %+v", detail)
	}
	if detail.EventID == 0 || detail.Tokens.ReasoningTokens != 3 || detail.Tokens.CachedTokens != 2 {
		t.Fatalf("restored detail event/tokens = %+v", detail)
	}
	if detail.Thinking == nil || detail.Thinking.Budget == nil || *detail.Thinking.Budget != budget {
		t.Fatalf("restored detail thinking = %+v", detail.Thinking)
	}
	if got := target.RecentSince(0); len(got) != 1 || got[0].Tokens.Total != 17 {
		t.Fatalf("recent events = %+v, want one restored event", got)
	}

	ring := target.BucketRing5m().ReadSnapshot()
	var ringRequests int64
	for _, bucket := range ring.Buckets {
		ringRequests += bucket.Requests
	}
	if ringRequests != 1 {
		t.Fatalf("5m ring requests = %d, want 1", ringRequests)
	}
	if target.TotalRequests() != 1 || target.TotalTokens() != 17 {
		t.Fatalf("totals = %d requests/%d tokens, want 1/17",
			target.TotalRequests(), target.TotalTokens())
	}
}

func TestPersistentLoggerPluginLoadRestoresLegacySnapshotWithoutAggregateStore(t *testing.T) {
	tmp := t.TempDir()
	source := NewRequestStatistics()
	source.Record(context.Background(), coreusage.Record{
		APIKey:      "key",
		Model:       "model",
		Detail:      coreusage.Detail{InputTokens: 7, OutputTokens: 5, TotalTokens: 12},
		RequestedAt: time.Now().Add(-time.Minute),
		StatusCode:  200,
	})

	legacyStore := NewFileStore(tmp)
	if err := legacyStore.Save(source.Snapshot()); err != nil {
		t.Fatalf("save legacy snapshot: %v", err)
	}

	target := NewRequestStatistics()
	plugin := NewPersistentLoggerPlugin(legacyStore)
	plugin.LoggerPlugin = &LoggerPlugin{stats: target}
	if err := plugin.Load(); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	snapshot := target.Snapshot()
	if target.TotalRequests() != 1 || target.TotalTokens() != 12 {
		t.Fatalf("totals = %d requests/%d tokens, want 1/12",
			target.TotalRequests(), target.TotalTokens())
	}
	if got := len(snapshot.APIs["key"].Models["model"].Details); got != 1 {
		t.Fatalf("details length = %d, want 1", got)
	}

	target.Record(context.Background(), coreusage.Record{
		APIKey:      "key",
		Model:       "model",
		RequestID:   "request-after-restart",
		Detail:      coreusage.Detail{TotalTokens: 1},
		RequestedAt: time.Now(),
		StatusCode:  200,
	})
	restoredDetails := target.Snapshot().APIs["key"].Models["model"].Details
	if got := restoredDetails[len(restoredDetails)-1].EventID; got != 2 {
		t.Fatalf("new event ID after full-snapshot restore = %d, want 2", got)
	}
}

func TestPersistentLoggerPluginLoadFallsBackWhenAggregateFileIsCorrupt(t *testing.T) {
	tmp := t.TempDir()
	source := NewRequestStatistics()
	source.Record(context.Background(), coreusage.Record{
		APIKey:      "key",
		Model:       "model",
		RequestID:   "request",
		Detail:      coreusage.Detail{InputTokens: 7, OutputTokens: 5, TotalTokens: 12},
		RequestedAt: time.Now().Add(-time.Minute),
		StatusCode:  200,
	})

	legacyStore := NewFileStore(tmp)
	if err := legacyStore.Save(source.Snapshot()); err != nil {
		t.Fatalf("save legacy snapshot: %v", err)
	}
	aggregateStore := NewAggregateFileStore(tmp)
	if err := os.WriteFile(aggregateStore.Path(), []byte("{invalid"), 0o644); err != nil {
		t.Fatalf("write corrupt aggregate: %v", err)
	}

	target := NewRequestStatistics()
	plugin := NewPersistentLoggerPlugin(legacyStore)
	plugin.LoggerPlugin = &LoggerPlugin{stats: target}
	plugin.AttachAggregateStores(aggregateStore, nil)
	if err := plugin.Load(); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	snapshot := target.Snapshot()
	if target.TotalRequests() != 1 || target.TotalTokens() != 12 {
		t.Fatalf("totals = %d requests/%d tokens, want 1/12",
			target.TotalRequests(), target.TotalTokens())
	}
	if got := snapshot.APIs["key"].Models["model"].Details; len(got) != 1 || got[0].RequestID != "request" {
		t.Fatalf("details = %+v, want fallback snapshot detail", got)
	}
}

func TestRequestStatistics_DetailsAreCapped(t *testing.T) {
	stats := NewRequestStatistics()
	for i := 0; i < defaultModelDetailsCap+200; i++ {
		stats.Record(context.Background(), coreusage.Record{
			APIKey: "k", Model: "m",
			Detail:      coreusage.Detail{TotalTokens: 1},
			RequestedAt: time.Unix(int64(1700000000+i), 0),
		})
	}
	snap := stats.Snapshot()
	if got := len(snap.APIs["k"].Models["m"].Details); got != defaultModelDetailsCap {
		t.Fatalf("details length = %d, want %d", got, defaultModelDetailsCap)
	}
}
