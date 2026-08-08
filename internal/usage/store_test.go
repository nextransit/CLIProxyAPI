package usage

import (
	"context"
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
	in := RecentEventsSnapshot{
		Version: 1,
		Events: []UsageEvent{
			{ID: 1, APIKey: "k", Model: "m", Tokens: TokenSummary{Input: 1, Output: 2, Total: 3}, RequestedAt: time.Now().UTC()},
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
