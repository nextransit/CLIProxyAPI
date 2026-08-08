package usage

import (
	"context"
	"sync"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestBucketRing_BasicRecordAndRead(t *testing.T) {
	// Pin now to a minute boundary so the ring covers [now, now+3min) and
	// every test record lands inside the window regardless of clock drift.
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	r := NewBucketRing(3, time.Minute, now)
	r.Record(now.Add(15*time.Second), "model-a", "auth-1", 100, false, 50)
	r.Record(now.Add(30*time.Second), "model-b", "auth-2", 200, true, 100)
	snap := r.ReadSnapshot()
	if len(snap.Buckets) != 3 {
		t.Fatalf("len(buckets) = %d, want 3", len(snap.Buckets))
	}
	totalReqs := int64(0)
	for _, b := range snap.Buckets {
		t.Logf("bucket start=%v reqs=%d", b.StartTime, b.Requests)
		totalReqs += b.Requests
	}
	if totalReqs != 2 {
		t.Fatalf("total requests = %d, want 2", totalReqs)
	}
}

func TestBucketRing_BackDatedRecordIsSilentOrDropped(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	r := NewBucketRing(3, time.Minute, now)
	// Back-dated record: outside the ring window. Must not rotate forward.
	r.Record(now.Add(-2*time.Hour), "old", "", 1, false, 0)
	snap := r.ReadSnapshot()
	for _, b := range snap.Buckets {
		if b.Requests != 0 {
			t.Fatalf("back-dated record leaked into bucket %v", b.StartTime)
		}
	}
}

func TestBucketRing_ConcurrentRecordAndRead(t *testing.T) {
	r := NewBucketRing(12, 5*time.Minute, time.Now())
	const writers = 8
	const per = 500
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < per; i++ {
				select {
				case <-stop:
					return
				default:
				}
				ts := time.Now().Add(-time.Duration(i%5) * time.Minute)
				r.Record(ts, "m", "a", int64(i), i%7 == 0, int64(i))
			}
		}(w)
	}
	// Concurrent readers.
	for rdr := 0; rdr < 4; rdr++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				_ = r.ReadSnapshot()
			}
		}()
	}
	wg.Wait()
	close(stop)
	snap := r.ReadSnapshot()
	if len(snap.Buckets) != 12 {
		t.Fatalf("len(buckets) = %d, want 12", len(snap.Buckets))
	}
	var total int64
	for _, b := range snap.Buckets {
		total += b.Requests
	}
	if total == 0 {
		t.Fatalf("expected some requests recorded")
	}
}

func TestBucketRing_AdvanceForwardKeepsBucketsMonotonic(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	r := NewBucketRing(6, time.Minute, now)
	r.Record(now, "a", "x", 1, false, 1)
	r.Record(now.Add(3*time.Minute), "b", "y", 2, false, 2)
	r.Record(now.Add(20*time.Minute), "c", "z", 3, false, 3)
	snap := r.ReadSnapshot()
	var prev time.Time
	for i, b := range snap.Buckets {
		if i > 0 && !b.StartTime.After(prev) {
			t.Fatalf("buckets not monotonic: bucket[%d]=%v before bucket[%d]=%v", i-1, prev, i, b.StartTime)
		}
		prev = b.StartTime
	}
}

func TestBucketRing_RestoreFromSeed(t *testing.T) {
	origin := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	r := NewBucketRing(4, time.Minute, origin)
	r.Record(origin.Add(30*time.Second), "model-a", "auth-1", 100, false, 50)
	r.Record(origin.Add(2*time.Minute+30*time.Second), "model-b", "auth-2", 200, true, 100)

	// Build a fresh ring and restore from the first one via seed.
	r2 := NewBucketRing(4, time.Minute, origin.Add(10*time.Minute))
	snap := r.ReadSnapshot()
	r2.RestoreFromSeed(snap.Buckets[0].StartTime.UnixMilli(), 3 /* head */, []RingSeedBucket{
		{StartTimeMs: snap.Buckets[0].StartTime.UnixMilli(), Requests: snap.Buckets[0].Requests, Tokens: snap.Buckets[0].Tokens, Failures: snap.Buckets[0].Failures, LatencySum: snap.Buckets[0].LatencySum, LatencyN: snap.Buckets[0].LatencyN},
		{StartTimeMs: snap.Buckets[1].StartTime.UnixMilli(), Requests: snap.Buckets[1].Requests, Tokens: snap.Buckets[1].Tokens, Failures: snap.Buckets[1].Failures, LatencySum: snap.Buckets[1].LatencySum, LatencyN: snap.Buckets[1].LatencyN},
		{StartTimeMs: snap.Buckets[2].StartTime.UnixMilli(), Requests: snap.Buckets[2].Requests, Tokens: snap.Buckets[2].Tokens, Failures: snap.Buckets[2].Failures, LatencySum: snap.Buckets[2].LatencySum, LatencyN: snap.Buckets[2].LatencyN},
		{StartTimeMs: snap.Buckets[3].StartTime.UnixMilli(), Requests: snap.Buckets[3].Requests, Tokens: snap.Buckets[3].Tokens, Failures: snap.Buckets[3].Failures, LatencySum: snap.Buckets[3].LatencySum, LatencyN: snap.Buckets[3].LatencyN},
	})
	got := r2.ReadSnapshot()
	if got.Buckets[3].Requests != snap.Buckets[3].Requests {
		t.Fatalf("newest bucket requests = %d, want %d", got.Buckets[3].Requests, snap.Buckets[3].Requests)
	}
	if got.Buckets[0].Requests != snap.Buckets[0].Requests {
		t.Fatalf("oldest bucket requests = %d, want %d", got.Buckets[0].Requests, snap.Buckets[0].Requests)
	}
}

func TestRequestStatistics_AggregateRoundTripRestoresRingsAndModels(t *testing.T) {
	// Use time.Now() so the record timestamps fall inside the live ring
	// window of bucketRing5m (which is initialised from time.Now() in
	// NewRequestStatistics).
	now := time.Now()
	s := NewRequestStatistics()
	s.Record(context.Background(), coreusage.Record{
		APIKey: "k", Model: "m",
		Detail:      coreusage.Detail{TotalTokens: 100},
		RequestedAt: now.Add(-30 * time.Second),
	})
	s.Record(context.Background(), coreusage.Record{
		APIKey: "k", Model: "m2",
		Detail:      coreusage.Detail{TotalTokens: 50},
		RequestedAt: now.Add(-4 * time.Minute),
	})
	snap := s.AggregateSnapshot()
	if snap.RingSeed5m == nil {
		t.Fatalf("expected RingSeed5m to be populated")
	}
	if len(snap.ModelTotals) == 0 {
		t.Fatalf("expected ModelTotals to be populated")
	}
	// Build a fresh stats and apply the snapshot.
	s2 := NewRequestStatistics()
	s2.ApplyAggregateSnapshot(snap)
	if s2.TotalRequests() != s.TotalRequests() {
		t.Fatalf("total_requests = %d, want %d", s2.TotalRequests(), s.TotalRequests())
	}
	if s2.TotalTokens() != s.TotalTokens() {
		t.Fatalf("total_tokens = %d, want %d", s2.TotalTokens(), s.TotalTokens())
	}
	got := s2.BucketRing5m().ReadSnapshot()
	var total int64
	for _, b := range got.Buckets {
		total += b.Requests
	}
	if total == 0 {
		t.Fatalf("expected some buckets to have requests after restore, got %d", total)
	}
}

func TestRequestStatistics_RestoreFromLegacySnapshotRebuildsRings(t *testing.T) {
	now := time.Now()
	legacy := StatisticsSnapshot{
		TotalRequests: 3,
		TotalTokens:   600,
		SuccessCount:  3,
		APIs: map[string]APISnapshot{
			"k": {
				TotalRequests: 3,
				TotalTokens:   600,
				Models: map[string]ModelSnapshot{
					"m": {
						TotalRequests: 3,
						TotalTokens:   600,
						Details: []RequestDetail{
							// All three records fall inside the 5-minute ring
							// window (1h) so they must all be reflushed into the
							// ring. The 90-minute record intentionally falls
							// outside the 5-minute window; the 1-hour ring
							// covers it.
							{Timestamp: now.Add(-1 * time.Minute), Tokens: TokenStats{TotalTokens: 100}, Failed: false, LatencyMs: 50},
							{Timestamp: now.Add(-15 * time.Minute), Tokens: TokenStats{TotalTokens: 200}, Failed: true, LatencyMs: 100},
							{Timestamp: now.Add(-30 * time.Minute), Tokens: TokenStats{TotalTokens: 300}, Failed: false, LatencyMs: 75},
						},
					},
				},
			},
		},
	}
	s := NewRequestStatistics()
	s.RestoreFromLegacySnapshot(legacy)
	if got := s.TotalRequests(); got != 3 {
		t.Fatalf("total_requests = %d, want 3", got)
	}
	if got := s.TotalTokens(); got != 600 {
		t.Fatalf("total_tokens = %d, want 600", got)
	}
	ring5 := s.BucketRing5m().ReadSnapshot()
	var ring5Total int64
	for _, b := range ring5.Buckets {
		ring5Total += b.Requests
	}
	if ring5Total != 3 {
		t.Fatalf("5m ring total = %d, want 3 (legacy details should refill the rings)", ring5Total)
	}
	ring1h := s.BucketRing1h().ReadSnapshot()
	var ring1hTotal int64
	for _, b := range ring1h.Buckets {
		ring1hTotal += b.Requests
	}
	if ring1hTotal != 3 {
		t.Fatalf("1h ring total = %d, want 3 (legacy details should refill the rings)", ring1hTotal)
	}
}
