package usage

import (
	"context"
	"net/http"
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

func TestRequestStatisticsRecordIncludesRequestID(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "test-key",
		Model:       "gpt-5.4",
		RequestID:   "deadbeef",
		RequestedAt: time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC),
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
	if details[0].RequestID != "deadbeef" {
		t.Fatalf("request_id = %q, want deadbeef", details[0].RequestID)
	}
}

func TestRequestStatisticsRecordDefaultsSuccessfulMissingStatusToOK(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "test-key",
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC),
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
	if details[0].StatusCode != http.StatusOK {
		t.Fatalf("status_code = %d, want %d", details[0].StatusCode, http.StatusOK)
	}
}

func TestRequestStatisticsRecordPublishesToBroker(t *testing.T) {
	stats := NewRequestStatistics()
	ch, cancel := stats.Broker().Subscribe()
	defer cancel()

	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "test-key",
		Model:       "gpt-5.4",
		RequestedAt: time.Now(),
		Detail:      coreusage.Detail{TotalTokens: 10},
	})

	select {
	case payload := <-ch:
		if payload.ID < 1 {
			t.Fatalf("ID = %d, want >= 1", payload.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for broker publish")
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

func TestRequestStatisticsMergeSnapshotIdempotentWithCappedDetails(t *testing.T) {
	// Regression test: when a snapshot has more than defaultModelDetailsCap (5000)
	// details per model, a second MergeSnapshot with the same snapshot must not
	// double-count tokens. The "seen" set is built from the in-memory Details,
	// which are capped; the bug was that excess details beyond the cap were
	// re-imported on the second merge, inflating the counters.
	detailsCount := defaultModelDetailsCap + 1000 // 6000
	stats := NewRequestStatistics()
	baseTime := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	details := make([]RequestDetail, detailsCount)
	for i := range details {
		details[i] = RequestDetail{
			Timestamp:  baseTime.Add(time.Duration(i) * time.Second),
			Source:     "test",
			AuthIndex:  "0",
			StatusCode: 200,
			Tokens: TokenStats{
				InputTokens:  10,
				OutputTokens: 20,
				TotalTokens:  30,
			},
		}
	}
	var totalTokens int64 = int64(detailsCount) * 30 // 6000 * 30 = 180000
	var totalRequests int64 = int64(detailsCount)
	snapshot := StatisticsSnapshot{
		TotalRequests: totalRequests,
		SuccessCount:  totalRequests,
		FailureCount:  0,
		TotalTokens:   totalTokens,
		APIs: map[string]APISnapshot{
			"test-key": {
				TotalRequests: totalRequests,
				TotalTokens:   totalTokens,
				Models: map[string]ModelSnapshot{
					"gpt-5.4": {
						TotalRequests: totalRequests,
						TotalTokens:   totalTokens,
						Details:       details,
					},
				},
			},
		},
		RequestsByDay: map[string]int64{"2026-03-20": totalRequests},
		TokensByDay:   map[string]int64{"2026-03-20": totalTokens},
	}

	// First merge retains only the newest capped detail set while preserving
	// the snapshot's full aggregate counters.
	result := stats.MergeSnapshot(snapshot)
	if result.Added != defaultModelDetailsCap {
		t.Fatalf("first merge: added=%d, want %d", result.Added, defaultModelDetailsCap)
	}
	if result.Skipped != int64(detailsCount-defaultModelDetailsCap) {
		t.Fatalf("first merge: skipped=%d, want %d", result.Skipped, detailsCount-defaultModelDetailsCap)
	}

	got := stats.Snapshot()
	if got.TotalTokens != totalTokens {
		t.Fatalf("after first merge: total_tokens=%d, want %d", got.TotalTokens, totalTokens)
	}
	if got.TotalRequests != totalRequests {
		t.Fatalf("after first merge: total_requests=%d, want %d", got.TotalRequests, totalRequests)
	}

	// Second merge: ALL details must be skipped (added=0) because the data
	// is already present. The bug was that the "seen" set only captured the
	// capped 5000 details, so the remaining 1000 were re-imported.
	result = stats.MergeSnapshot(snapshot)
	if result.Added != 0 {
		t.Fatalf("second merge: added=%d, want 0 (no double-counting)", result.Added)
	}
	if result.Skipped != int64(detailsCount) {
		t.Fatalf("second merge: skipped=%d, want %d", result.Skipped, detailsCount)
	}

	got = stats.Snapshot()
	if got.TotalTokens != totalTokens {
		t.Fatalf("after second merge: total_tokens=%d, want %d (no change)", got.TotalTokens, totalTokens)
	}
	if got.TotalRequests != totalRequests {
		t.Fatalf("after second merge: total_requests=%d, want %d (no change)", got.TotalRequests, totalRequests)
	}
}

func TestRequestStatisticsMergeSnapshotRestoresMissingDetailsWithoutInflatingCoveredTotals(t *testing.T) {
	stats := NewRequestStatistics()
	stats.ApplyAggregateSnapshot(AggregateSnapshot{
		Version:       2,
		TotalRequests: 1,
		SuccessCount:  1,
		TotalTokens:   30,
		ModelTotals: map[string]APITotals{
			"test-key": {
				TotalRequests: 1,
				TotalTokens:   30,
				Models: map[string]ModelTotals{
					"gpt-5.4": {TotalRequests: 1, TotalTokens: 30},
				},
			},
		},
	})

	snapshot := StatisticsSnapshot{
		TotalRequests: 1,
		SuccessCount:  1,
		TotalTokens:   30,
		APIs: map[string]APISnapshot{
			"test-key": {
				TotalRequests: 1,
				TotalTokens:   30,
				Models: map[string]ModelSnapshot{
					"gpt-5.4": {
						TotalRequests: 1,
						TotalTokens:   30,
						Details: []RequestDetail{{
							RequestID:  "req-covered",
							Timestamp:  time.Now().Add(-time.Minute),
							StatusCode: http.StatusOK,
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

	result := stats.MergeSnapshot(snapshot)
	if result.Added != 1 || result.Skipped != 0 {
		t.Fatalf("merge result = %+v, want one restored detail", result)
	}
	got := stats.Snapshot()
	if got.TotalRequests != 1 || got.TotalTokens != 30 {
		t.Fatalf("totals = %d/%d, want 1/30", got.TotalRequests, got.TotalTokens)
	}
	details := got.APIs["test-key"].Models["gpt-5.4"].Details
	if len(details) != 1 || details[0].RequestID != "req-covered" {
		t.Fatalf("details = %+v, want restored covered request", details)
	}
}

func TestRequestStatistics_RecordEmitsUsageEvent(t *testing.T) {
	s := NewRequestStatistics()
	subs, cancel := s.Broker().Subscribe()
	defer cancel()

	rec := coreusage.Record{
		APIKey: "key-1",
		Model:  "gpt-4o",
		Detail: coreusage.Detail{
			InputTokens:  10,
			OutputTokens: 20,
			TotalTokens:  30,
		},
		StatusCode: 200,
	}
	s.Record(context.Background(), rec)

	select {
	case evt := <-subs:
		if evt.ID != 1 {
			t.Errorf("want id=1, got %d", evt.ID)
		}
		if evt.APIKey != "key-1" || evt.Model != "gpt-4o" {
			t.Errorf("event mismatch: %+v", evt)
		}
		if evt.Tokens.Total != 30 {
			t.Errorf("want tokens.total=30, got %d", evt.Tokens.Total)
		}
	case <-time.After(time.Second):
		t.Fatal("no event received within 1s")
	}

	if got := s.RecentSince(0); len(got) != 1 || got[0].ID != 1 {
		t.Errorf("ring buffer mismatch: %+v", got)
	}
	if got := s.LatestEventID(); got != 1 {
		t.Errorf("latest id want 1, got %d", got)
	}
}

func TestRequestStatistics_RecordMonotonicIDs(t *testing.T) {
	s := NewRequestStatistics()
	subs, cancel := s.Broker().Subscribe()
	defer cancel()

	for i := 0; i < 50; i++ {
		s.Record(context.Background(), coreusage.Record{
			APIKey:     "k",
			Model:      "m",
			Detail:     coreusage.Detail{TotalTokens: 1},
			StatusCode: 200,
		})
	}

	prev := uint64(0)
	for i := 0; i < 50; i++ {
		select {
		case evt := <-subs:
			if evt.ID <= prev {
				t.Errorf("non-monotonic: id=%d after %d", evt.ID, prev)
			}
			prev = evt.ID
		case <-time.After(time.Second):
			t.Fatalf("only %d/50 events received", i)
		}
	}
}

func TestRestoreDetailsFromRecentEventsPreservesDistinctSameShapeRequests(t *testing.T) {
	stats := NewRequestStatistics()
	timestamp := time.Now().Add(-time.Minute).UTC()
	stats.RestoreDetailsFromRecentEvents([]UsageEvent{
		{
			ID:          41,
			APIKey:      "key",
			Model:       "model",
			RequestID:   "request-41",
			Source:      "source-a",
			AuthIndex:   "auth-a",
			RequestedAt: timestamp,
			StatusCode:  http.StatusOK,
			Tokens:      TokenSummary{Input: 10, Output: 5, Reasoning: 2, Cached: 1, Total: 17},
		},
		{
			ID:          42,
			APIKey:      "key",
			Model:       "model",
			RequestID:   "request-42",
			Source:      "source-b",
			AuthIndex:   "auth-b",
			RequestedAt: timestamp,
			StatusCode:  http.StatusOK,
			Tokens:      TokenSummary{Input: 10, Output: 5, Reasoning: 2, Cached: 1, Total: 17},
		},
	})

	details := stats.Snapshot().APIs["key"].Models["model"].Details
	if len(details) != 2 {
		t.Fatalf("details length = %d, want 2 distinct requests", len(details))
	}
	if details[0].RequestID == details[1].RequestID {
		t.Fatalf("request IDs collapsed: %+v", details)
	}
	if details[0].Tokens.ReasoningTokens != 2 || details[0].Tokens.CachedTokens != 1 {
		t.Fatalf("token breakdown not restored: %+v", details[0].Tokens)
	}
}

func TestRestoreDetailsFromRecentEventsDeduplicatesLegacyEventShape(t *testing.T) {
	stats := NewRequestStatistics()
	timestamp := time.Now().Add(-time.Minute).UTC()
	stats.RestoreDetailsFromLegacySnapshot(StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"key": {
				Models: map[string]ModelSnapshot{
					"model": {
						Details: []RequestDetail{{
							Timestamp:  timestamp,
							StatusCode: http.StatusOK,
							Tokens: TokenStats{
								InputTokens:  10,
								OutputTokens: 5,
								TotalTokens:  15,
							},
						}},
					},
				},
			},
		},
	}, false)
	stats.RestoreDetailsFromRecentEvents([]UsageEvent{{
		ID:          99,
		APIKey:      "key",
		Model:       "model",
		RequestedAt: timestamp,
		StatusCode:  http.StatusOK,
		Tokens:      TokenSummary{Input: 10, Output: 5, Total: 15},
	}})

	details := stats.Snapshot().APIs["key"].Models["model"].Details
	if len(details) != 1 {
		t.Fatalf("details length = %d, want legacy event deduplicated", len(details))
	}
}
