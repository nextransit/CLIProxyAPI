package usage

import (
	"sync"
	"testing"
	"time"
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
