package usage

import (
	"path/filepath"
	"testing"
	"time"
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
