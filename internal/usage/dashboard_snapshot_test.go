package usage

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func makeRecord(model, auth, reqID string, ts time.Time, latency time.Duration, total int64, failed bool) coreusage.Record {
	return coreusage.Record{
		Model:       model,
		Source:      "src",
		AuthIndex:   auth,
		RequestID:   reqID,
		RequestedAt: ts,
		Latency:     latency,
		Failed:      failed,
		Detail: coreusage.Detail{
			InputTokens:  total / 2,
			OutputTokens: total / 2,
			TotalTokens:  total,
		},
	}
}

// TestBuildDashboardSnapshot_ShapesBucketTopAndLatest validates that the
// dashboard snapshot returns the expected aggregates and slices after a
// handful of records have been ingested.
func TestBuildDashboardSnapshot_ShapesBucketTopAndLatest(t *testing.T) {
	stats := NewRequestStatistics()
	ctx := context.Background()
	now := time.Now()
	for i := 0; i < 5; i++ {
		stats.Record(ctx, makeRecord("model-a", "auth-1",
			"req-a-"+string(rune('a'+i)),
			now.Add(-time.Duration(i)*time.Minute),
			1200*time.Millisecond, 150, false))
	}
	for i := 0; i < 3; i++ {
		stats.Record(ctx, makeRecord("model-b", "auth-2",
			"req-b-"+string(rune('a'+i)),
			now.Add(-time.Duration(i)*time.Minute-30*time.Minute),
			800*time.Millisecond, 120, false))
	}
	// Two records outside the default 24h window to assert filtering.
	for i := 0; i < 2; i++ {
		stats.Record(ctx, makeRecord("model-c", "auth-3",
			"req-c-old",
			now.Add(-48*time.Hour),
			200*time.Millisecond, 9999, false))
	}

	cfg := DefaultDashboardConfig()
	snap := stats.BuildDashboardSnapshot(cfg)

	if snap.BucketCount != cfg.BucketCount {
		t.Fatalf("BucketCount = %d, want %d", snap.BucketCount, cfg.BucketCount)
	}
	if len(snap.FlowBuckets) != cfg.BucketCount {
		t.Fatalf("len(FlowBuckets) = %d, want %d", len(snap.FlowBuckets), cfg.BucketCount)
	}
	if snap.WindowRequests != 8 {
		t.Fatalf("WindowRequests = %d, want 8 (5 model-a + 3 model-b, model-c filtered)", snap.WindowRequests)
	}
	want := int64(5*150 + 3*120)
	if snap.WindowTokens != want {
		t.Fatalf("WindowTokens = %d, want %d", snap.WindowTokens, want)
	}
	if len(snap.ModelTop) != 2 {
		t.Fatalf("len(ModelTop) = %d, want 2", len(snap.ModelTop))
	}
	if snap.ModelTop[0].Model != "model-a" || snap.ModelTop[1].Model != "model-b" {
		t.Fatalf("ModelTop order = %q, %q; want model-a, model-b", snap.ModelTop[0].Model, snap.ModelTop[1].Model)
	}
	if math.Abs(snap.ModelTop[0].SharePercent-(750.0/1110.0*100.0)) > 0.001 {
		t.Fatalf("ModelTop[0].SharePercent = %f, want %f", snap.ModelTop[0].SharePercent, 750.0/1110.0*100.0)
	}
	if len(snap.LatestRequests) == 0 || len(snap.LatestRequests) > cfg.LatestCount {
		t.Fatalf("len(LatestRequests) = %d, want 1..%d", len(snap.LatestRequests), cfg.LatestCount)
	}
	if snap.LatestEventID == 0 {
		t.Fatalf("LatestEventID = 0, want >0")
	}
	if snap.WindowHours != 24 {
		t.Fatalf("WindowHours = %f, want 24", snap.WindowHours)
	}

	out, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("Marshal(DashboardSnapshot) error = %v", err)
	}
	for _, key := range []string{"total_requests", "flow_buckets", "model_top", "latest_requests", "latest_event_id"} {
		if !containsBytes(out, key) {
			t.Fatalf("Marshal output missing key %q: %s", key, string(out))
		}
	}
}

// TestBuildDashboardSnapshot_WindowFilter ensures records outside the
// configured window are excluded from every aggregate.
func TestBuildDashboardSnapshot_WindowFilter(t *testing.T) {
	stats := NewRequestStatistics()
	ctx := context.Background()
	now := time.Now()
	stats.Record(ctx, makeRecord("old-model", "auth-x", "old",
		now.Add(-90*time.Minute), 200*time.Millisecond, 42, false))
	stats.Record(ctx, makeRecord("new-model", "auth-y", "new",
		now.Add(-5*time.Minute), 100*time.Millisecond, 7, false))

	cfg := DefaultDashboardConfig()
	cfg.Window = time.Hour
	snap := stats.BuildDashboardSnapshot(cfg)

	if snap.WindowRequests != 1 {
		t.Fatalf("WindowRequests = %d, want 1 (old-model excluded)", snap.WindowRequests)
	}
	if snap.WindowTokens != 7 {
		t.Fatalf("WindowTokens = %d, want 7", snap.WindowTokens)
	}
	if len(snap.ModelTop) != 1 || snap.ModelTop[0].Model != "new-model" {
		names := make([]string, 0, len(snap.ModelTop))
		for _, m := range snap.ModelTop {
			names = append(names, m.Model)
		}
		t.Fatalf("ModelTop = %v, want only new-model", names)
	}
}

// TestBuildDashboardSnapshot_AllWindow disables the time-window filter.
func TestBuildDashboardSnapshot_AllWindow(t *testing.T) {
	stats := NewRequestStatistics()
	ctx := context.Background()
	now := time.Now()
	for i, when := range []time.Duration{-10 * time.Minute, -72 * time.Hour} {
		stats.Record(ctx, makeRecord("model-x", "auth",
			"rid-"+string(rune('a'+i)),
			now.Add(when), 50*time.Millisecond, 10, false))
	}

	cfg := DefaultDashboardConfig()
	cfg.Window = 0 // all
	snap := stats.BuildDashboardSnapshot(cfg)
	if snap.WindowRequests != 2 {
		t.Fatalf("WindowRequests = %d, want 2 (all window)", snap.WindowRequests)
	}
	if snap.WindowTokens != 20 {
		t.Fatalf("WindowTokens = %d, want 20", snap.WindowTokens)
	}
}

func containsBytes(haystack []byte, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if string(haystack[i:i+len(needle)]) == needle {
			return true
		}
	}
	return false
}
