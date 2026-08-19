package usage

import (
	"context"
	"strings"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestSQLiteUsageEventStoreInsertRollupsAndMasksSecrets(t *testing.T) {
	store, err := NewSQLiteUsageEventStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteUsageEventStore() error = %v", err)
	}
	defer store.Close()

	ts := time.Date(2026, 8, 9, 10, 15, 30, 0, time.UTC)
	record := coreusage.Record{
		Provider:  "openai",
		Model:     "gpt-test",
		APIKey:    "sk-test-secret-value",
		AuthID:    "auth-secret-id",
		AuthIndex: "auth-1",
		AuthType:  "api-key",
		Source:    "openai-compatible",
		RequestID: "req-1",
		Request: coreusage.RequestInfo{
			Type:        "http",
			Method:      "POST",
			DisplayName: "responses",
		},
		ModelInfo: coreusage.ModelInfo{
			PlatformModel: "gpt-test",
			UpstreamModel: "upstream-gpt-test",
		},
		RequestedAt: ts,
		Latency:     1200 * time.Millisecond,
		Detail: coreusage.Detail{
			InputTokens:     7,
			OutputTokens:    3,
			ReasoningTokens: 2,
			CachedTokens:    1,
			TotalTokens:     13,
		},
	}
	evt := UsageEvent{
		ID:          42,
		APIKey:      record.APIKey,
		Model:       record.Model,
		Source:      record.Source,
		AuthIndex:   record.AuthIndex,
		RequestID:   record.RequestID,
		Tokens:      TokenSummary{Input: 7, Output: 3, Reasoning: 2, Cached: 1, Total: 13},
		RequestedAt: ts,
		DurationMs:  1200,
		StatusCode:  200,
	}

	if err := store.InsertUsageEvent(context.Background(), record, evt); err != nil {
		t.Fatalf("InsertUsageEvent() error = %v", err)
	}

	requests, tokens, failures, err := store.QueryRollupTotals(
		context.Background(),
		"day",
		time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("QueryRollupTotals() error = %v", err)
	}
	if requests != 1 || tokens != 13 || failures != 0 {
		t.Fatalf("rollup totals = (%d,%d,%d), want (1,13,0)", requests, tokens, failures)
	}

	var apiHash, apiMask, authHash string
	if err := store.db.QueryRow(`SELECT api_key_hash, api_key_mask, auth_id_hash FROM usage_events WHERE event_id = 42`).
		Scan(&apiHash, &apiMask, &authHash); err != nil {
		t.Fatalf("query persisted event: %v", err)
	}
	if apiHash == "" || apiHash == record.APIKey || authHash == record.AuthID {
		t.Fatalf("secrets were not hashed: apiHash=%q authHash=%q", apiHash, authHash)
	}
	if strings.Contains(apiMask, "secret") || apiMask != "sk-t...alue" {
		t.Fatalf("api mask = %q, want prefix/suffix only", apiMask)
	}
}

func TestSQLiteUsageEventStoreImportSnapshotIsIdempotent(t *testing.T) {
	store, err := NewSQLiteUsageEventStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteUsageEventStore() error = %v", err)
	}
	defer store.Close()

	ts := time.Date(2026, 8, 9, 11, 0, 0, 0, time.UTC)
	snapshot := StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"sk-import-secret": {
				Models: map[string]ModelSnapshot{
					"gpt-import": {
						Details: []RequestDetail{{
							EventID:    7,
							Timestamp:  ts,
							LatencyMs:  80,
							Source:     "source",
							AuthIndex:  "auth",
							RequestID:  "request-id",
							StatusCode: 200,
							Tokens:     TokenStats{InputTokens: 4, OutputTokens: 6, TotalTokens: 10},
						}},
					},
				},
			},
		},
	}

	imported, err := store.ImportSnapshot(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("ImportSnapshot() error = %v", err)
	}
	if imported != 1 {
		t.Fatalf("first import = %d, want 1", imported)
	}
	imported, err = store.ImportSnapshot(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("second ImportSnapshot() error = %v", err)
	}
	if imported != 0 {
		t.Fatalf("second import = %d, want 0", imported)
	}

	requests, tokens, failures, err := store.QueryRollupTotals(
		context.Background(),
		"hour",
		ts.Truncate(time.Hour),
		ts.Truncate(time.Hour).Add(time.Hour),
	)
	if err != nil {
		t.Fatalf("QueryRollupTotals() error = %v", err)
	}
	if requests != 1 || tokens != 10 || failures != 0 {
		t.Fatalf("rollup after duplicate import = (%d,%d,%d), want (1,10,0)", requests, tokens, failures)
	}
}

func TestRequestStatisticsRecordPersistsUsageEventStore(t *testing.T) {
	store, err := NewSQLiteUsageEventStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteUsageEventStore() error = %v", err)
	}
	defer store.Close()
	setUsageEventStore(store)
	defer setUsageEventStore(nil)

	stats := NewRequestStatistics()
	ts := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	evt, ok := stats.Record(context.Background(), coreusage.Record{
		APIKey:      "sk-live-secret",
		Model:       "gpt-live",
		RequestedAt: ts,
		StatusCode:  200,
		Detail:      coreusage.Detail{InputTokens: 5, OutputTokens: 5, TotalTokens: 10},
	})
	if !ok || evt.ID == 0 {
		t.Fatalf("Record() event = %+v ok=%v, want persisted event", evt, ok)
	}

	requests, tokens, failures, err := store.QueryRollupTotals(
		context.Background(),
		"minute",
		ts.Truncate(time.Minute),
		ts.Truncate(time.Minute).Add(time.Minute),
	)
	if err != nil {
		t.Fatalf("QueryRollupTotals() error = %v", err)
	}
	if requests != 1 || tokens != 10 || failures != 0 {
		t.Fatalf("minute rollup = (%d,%d,%d), want (1,10,0)", requests, tokens, failures)
	}
}
