package usage

import (
	"context"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestRequestStatisticsRecordIncludesLatency(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "test-key",
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC),
		Latency:     1500 * time.Millisecond,
		Detail: coreusage.Detail{
			InputTokens:  10,
			OutputTokens: 20,
			TotalTokens:  30,
		},
	})

	snapshot := stats.Snapshot()
	details := snapshot.APIs["test-key"].Models["gpt-5.4"].Details
	if len(details) != 1 {
		t.Fatalf("details len = %d, want 1", len(details))
	}
	if details[0].LatencyMs != 1500 {
		t.Fatalf("latency_ms = %d, want 1500", details[0].LatencyMs)
	}
}

func TestRequestStatisticsRecordIncludesThinking(t *testing.T) {
	stats := NewRequestStatistics()
	budget := int64(8192)
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "test-key",
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC),
		Detail: coreusage.Detail{
			InputTokens:  10,
			OutputTokens: 20,
			TotalTokens:  30,
			Thinking: &coreusage.Thinking{
				Intensity: "high",
				Mode:      "budget",
				Level:     "high",
				Budget:    &budget,
			},
		},
	})

	snapshot := stats.Snapshot()
	details := snapshot.APIs["test-key"].Models["gpt-5.4"].Details
	if len(details) != 1 {
		t.Fatalf("details len = %d, want 1", len(details))
	}
	if details[0].Thinking == nil {
		t.Fatal("thinking should not be nil")
	}
	if got := details[0].Thinking.Mode; got != "budget" {
		t.Fatalf("thinking.mode = %q, want %q", got, "budget")
	}
	if details[0].Thinking.Budget == nil || *details[0].Thinking.Budget != 8192 {
		t.Fatalf("thinking.budget = %v, want 8192", details[0].Thinking.Budget)
	}
}

func TestRequestStatisticsMergeSnapshotDedupIgnoresLatency(t *testing.T) {
	stats := NewRequestStatistics()
	timestamp := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	first := StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"test-key": {
				Models: map[string]ModelSnapshot{
					"gpt-5.4": {
						Details: []RequestDetail{{
							Timestamp: timestamp,
							LatencyMs: 0,
							Source:    "user@example.com",
							AuthIndex: "0",
							Tokens: TokenStats{
								InputTokens:  10,
								OutputTokens: 20,
								TotalTokens:  30,
							},
						}},
					},
				},
			},
		},
	}
	second := StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"test-key": {
				Models: map[string]ModelSnapshot{
					"gpt-5.4": {
						Details: []RequestDetail{{
							Timestamp: timestamp,
							LatencyMs: 2500,
							Source:    "user@example.com",
							AuthIndex: "0",
							Tokens: TokenStats{
								InputTokens:  10,
								OutputTokens: 20,
								TotalTokens:  30,
							},
						}},
					},
				},
			},
		},
	}

	result := stats.MergeSnapshot(first)
	if result.Added != 1 || result.Skipped != 0 {
		t.Fatalf("first merge = %+v, want added=1 skipped=0", result)
	}

	result = stats.MergeSnapshot(second)
	if result.Added != 0 || result.Skipped != 1 {
		t.Fatalf("second merge = %+v, want added=0 skipped=1", result)
	}

	snapshot := stats.Snapshot()
	details := snapshot.APIs["test-key"].Models["gpt-5.4"].Details
	if len(details) != 1 {
		t.Fatalf("details len = %d, want 1", len(details))
	}
}

func TestRequestStatisticsMergeSnapshotPreservesAggregateTotalsWhenDetailsAreIncomplete(t *testing.T) {
	stats := NewRequestStatistics()
	timestamp := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	snapshot := StatisticsSnapshot{
		TotalRequests: 3,
		SuccessCount:  2,
		FailureCount:  1,
		TotalTokens:   1000,
		APIs: map[string]APISnapshot{
			"test-key": {
				TotalRequests: 3,
				TotalTokens:   1000,
				Models: map[string]ModelSnapshot{
					"gpt-5.4": {
						TotalRequests: 3,
						TotalTokens:   1000,
						Details: []RequestDetail{{
							Timestamp: timestamp,
							Source:    "user@example.com",
							AuthIndex: "0",
							Tokens: TokenStats{
								InputTokens:  40,
								OutputTokens: 60,
								TotalTokens:  100,
							},
						}},
					},
				},
			},
		},
		RequestsByDay: map[string]int64{"2026-03-20": 3},
		RequestsByHour: map[string]int64{
			"12": 3,
		},
		TokensByDay: map[string]int64{"2026-03-20": 1000},
		TokensByHour: map[string]int64{
			"12": 1000,
		},
	}

	result := stats.MergeSnapshot(snapshot)
	if result.Added != 1 || result.Skipped != 0 {
		t.Fatalf("merge result = %+v, want added=1 skipped=0", result)
	}

	got := stats.Snapshot()
	if got.TotalRequests != 3 {
		t.Fatalf("TotalRequests = %d, want 3", got.TotalRequests)
	}
	if got.SuccessCount != 2 {
		t.Fatalf("SuccessCount = %d, want 2", got.SuccessCount)
	}
	if got.FailureCount != 1 {
		t.Fatalf("FailureCount = %d, want 1", got.FailureCount)
	}
	if got.TotalTokens != 1000 {
		t.Fatalf("TotalTokens = %d, want 1000", got.TotalTokens)
	}
	model := got.APIs["test-key"].Models["gpt-5.4"]
	if model.TotalRequests != 3 {
		t.Fatalf("model total requests = %d, want 3", model.TotalRequests)
	}
	if model.TotalTokens != 1000 {
		t.Fatalf("model total tokens = %d, want 1000", model.TotalTokens)
	}
	if len(model.Details) != 1 {
		t.Fatalf("details len = %d, want 1", len(model.Details))
	}

	result = stats.MergeSnapshot(snapshot)
	if result.Added != 0 || result.Skipped != 1 {
		t.Fatalf("second merge result = %+v, want added=0 skipped=1", result)
	}

	got = stats.Snapshot()
	if got.TotalRequests != 3 {
		t.Fatalf("after second merge TotalRequests = %d, want 3", got.TotalRequests)
	}
	if got.TotalTokens != 1000 {
		t.Fatalf("after second merge TotalTokens = %d, want 1000", got.TotalTokens)
	}
	model = got.APIs["test-key"].Models["gpt-5.4"]
	if model.TotalRequests != 3 {
		t.Fatalf("after second merge model total requests = %d, want 3", model.TotalRequests)
	}
	if model.TotalTokens != 1000 {
		t.Fatalf("after second merge model total tokens = %d, want 1000", model.TotalTokens)
	}
}
