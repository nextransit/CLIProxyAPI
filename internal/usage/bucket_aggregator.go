package usage

import (
	"sort"
	"sync"
	"time"
)

// BucketRing is a fixed-capacity rotating time-bucket aggregator. It keeps
// the running sums of (requests, tokens, failures, latency) for each bucket,
// plus a per-model and per-authIndex breakdown. The ring is sized at
// construction time and never grows, so memory usage is bounded regardless of
// the volume of ingest.
//
// BucketRing is safe for concurrent use. Record() takes a write lock; readers
// take a read lock. The locked region in Record() is O(1) amortized; the
// locked region in ReadSnapshot() is O(bucketCount) copy-only. This is the
// pre-aggregated data source that replaces the previous O(N) re-scan over
// every RequestDetail retained in memory.
type BucketRing struct {
	mu         sync.RWMutex
	bucketSize time.Duration
	buckets    []bucketSlot // chronological, oldest first
	head       int          // index of the newest bucket; always len(buckets)-1
	startTime  time.Time    // start time of the oldest bucket

	// modelBreakdown is keyed by (bucketIndex, modelName). We keep a
	// map-of-map to avoid allocating one map per ingest at the top level.
	// To keep the lock hot path simple we trade a small amount of memory for
	// direct O(1) access: a flat map[string]*modelAcc per bucket slot, with
	// the slot owned by that bucket only.
}

type bucketSlot struct {
	startTime        time.Time
	requests         int64
	tokens           int64
	failures         int64
	latencySumMs     int64
	latencySamples   int64
	modelBreakdown   map[string]*modelBucketAcc
	authIdxBreakdown map[string]*authBucketAcc
}

type modelBucketAcc struct {
	requests       int64
	tokens         int64
	failures       int64
	latencySumMs   int64
	latencySamples int64
}

type authBucketAcc struct {
	requests int64
	tokens   int64
	failures int64
}

// NewBucketRing creates a ring with the given bucket count and per-bucket
// duration. startAt is treated as the time the newest bucket represents
// (i.e. "now" at construction). The invariant is:
//
//	buckets[i].startTime = startAt.Truncate(bucketSize) - (N-1-i)*bucketSize
//
// so buckets[0] is the oldest bucket, buckets[N-1] is the newest (owns
// startAt), and head = N-1.
func NewBucketRing(bucketCount int, bucketSize time.Duration, startAt time.Time) *BucketRing {
	if bucketCount <= 0 {
		bucketCount = 1
	}
	if bucketSize <= 0 {
		bucketSize = time.Minute
	}
	newestBucketStart := startAt.Truncate(bucketSize)
	oldestBucketStart := newestBucketStart.Add(-time.Duration(bucketCount-1) * bucketSize)
	r := &BucketRing{
		bucketSize: bucketSize,
		buckets:    make([]bucketSlot, bucketCount),
		startTime:  oldestBucketStart,
	}
	for i := range r.buckets {
		r.buckets[i].startTime = oldestBucketStart.Add(time.Duration(i) * bucketSize)
	}
	r.head = bucketCount - 1
	return r
}

// BucketSize returns the duration of one bucket.
func (r *BucketRing) BucketSize() time.Duration { return r.bucketSize }

// BucketCount returns the number of buckets in the ring.
func (r *BucketRing) BucketCount() int { return len(r.buckets) }

// resetSlot clears all counters and breakdowns in slot, leaving startTime
// intact for the caller to set.
func (b *bucketSlot) resetSlot() {
	b.requests = 0
	b.tokens = 0
	b.failures = 0
	b.latencySumMs = 0
	b.latencySamples = 0
	b.modelBreakdown = nil
	b.authIdxBreakdown = nil
}

// locateBucket returns the index of the slot that owns t, or -1 if t falls
// outside the ring window. Caller must hold r.mu. The window is
// [r.startTime, r.startTime + N*bucketSize).
func (r *BucketRing) locateBucket(t time.Time) int {
	if len(r.buckets) == 0 {
		return -1
	}
	tBucket := t.Truncate(r.bucketSize)
	totalSpan := r.bucketSize * time.Duration(len(r.buckets))
	ringEnd := r.startTime.Add(totalSpan)
	if tBucket.Before(r.startTime) || !tBucket.Before(ringEnd) {
		return -1
	}
	return int(tBucket.Sub(r.startTime) / r.bucketSize)
}

// advanceLocked shifts the chronological window forward so the newest slot
// owns the bucket containing now. Caller must hold r.mu.
func (r *BucketRing) advanceLocked(now time.Time) {
	if len(r.buckets) == 0 {
		return
	}
	nowBucket := now.Truncate(r.bucketSize)
	current := r.buckets[len(r.buckets)-1].startTime
	if !nowBucket.After(current) {
		return
	}
	delta := int(nowBucket.Sub(current) / r.bucketSize)
	if delta >= len(r.buckets) {
		r.startTime = nowBucket.Add(-r.bucketSize * time.Duration(len(r.buckets)-1))
		for i := range r.buckets {
			r.buckets[i].startTime = r.startTime.Add(time.Duration(i) * r.bucketSize)
			r.buckets[i].resetSlot()
		}
		r.head = len(r.buckets) - 1
		return
	}

	copy(r.buckets, r.buckets[delta:])
	firstNew := len(r.buckets) - delta
	for i := firstNew; i < len(r.buckets); i++ {
		r.buckets[i].resetSlot()
		r.buckets[i].startTime = current.Add(time.Duration(i-firstNew+1) * r.bucketSize)
	}
	r.startTime = r.buckets[0].startTime
	r.head = len(r.buckets) - 1
}

// Record updates the bucket that owns timestamp with the given breakdown.
// All counters are in-process atomic under the ring mutex. Callers should
// also hold RequestStatistics.mu if they are updating related aggregates,
// but the ring is internally consistent on its own.
func (r *BucketRing) Record(t time.Time, model string, authIndex string, tokens int64, failed bool, latencyMs int64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.advanceLocked(t)
	idx := r.locateBucket(t)
	if idx < 0 {
		return
	}
	slot := &r.buckets[idx]
	slot.requests++
	slot.tokens += tokens
	if failed {
		slot.failures++
	}
	if latencyMs > 0 {
		slot.latencySumMs += latencyMs
		slot.latencySamples++
	}
	if model != "" {
		mb, ok := slot.modelBreakdown[model]
		if !ok {
			if slot.modelBreakdown == nil {
				slot.modelBreakdown = make(map[string]*modelBucketAcc)
			}
			mb = &modelBucketAcc{}
			slot.modelBreakdown[model] = mb
		}
		mb.requests++
		mb.tokens += tokens
		if failed {
			mb.failures++
		}
		if latencyMs > 0 {
			mb.latencySumMs += latencyMs
			mb.latencySamples++
		}
	}
	if authIndex != "" {
		ab, ok := slot.authIdxBreakdown[authIndex]
		if !ok {
			if slot.authIdxBreakdown == nil {
				slot.authIdxBreakdown = make(map[string]*authBucketAcc)
			}
			ab = &authBucketAcc{}
			slot.authIdxBreakdown[authIndex] = ab
		}
		ab.requests++
		ab.tokens += tokens
		if failed {
			ab.failures++
		}
	}
}

// RingSnapshot is a flat, lock-free view of the ring at a point in time.
type RingSnapshot struct {
	BucketSize time.Duration
	HeadIndex  int
	Buckets    []RingBucket
}

// RingBucket is the read-side view of one bucket.
type RingBucket struct {
	StartTime  time.Time
	Requests   int64
	Tokens     int64
	Failures   int64
	LatencySum int64
	LatencyN   int64
	Models     []RingModelAgg
	Auths      []RingAuthAgg
}

// RingModelAgg is one model's per-bucket aggregate.
type RingModelAgg struct {
	Model      string
	Requests   int64
	Tokens     int64
	Failures   int64
	LatencySum int64
	LatencyN   int64
}

// RingAuthAgg is one authIndex's per-bucket aggregate.
type RingAuthAgg struct {
	AuthIndex string
	Requests  int64
	Tokens    int64
	Failures  int64
}

// ReadSnapshot returns a chronological (oldest first) view of every bucket
// in the ring. The caller gets an independent copy and can iterate without
// holding the ring lock. Counts older than the ring window are gone; this is
// the trade for O(1) writes and bounded memory.
func (r *BucketRing) ReadSnapshot() RingSnapshot {
	if r == nil {
		return RingSnapshot{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := RingSnapshot{
		HeadIndex:  r.head,
		BucketSize: r.bucketSize,
		Buckets:    make([]RingBucket, len(r.buckets)),
	}
	for i := 0; i < len(r.buckets); i++ {
		slot := r.buckets[i]
		rb := RingBucket{
			StartTime:  slot.startTime,
			Requests:   slot.requests,
			Tokens:     slot.tokens,
			Failures:   slot.failures,
			LatencySum: slot.latencySumMs,
			LatencyN:   slot.latencySamples,
		}
		if len(slot.modelBreakdown) > 0 {
			rb.Models = make([]RingModelAgg, 0, len(slot.modelBreakdown))
			for k, v := range slot.modelBreakdown {
				rb.Models = append(rb.Models, RingModelAgg{
					Model:      k,
					Requests:   v.requests,
					Tokens:     v.tokens,
					Failures:   v.failures,
					LatencySum: v.latencySumMs,
					LatencyN:   v.latencySamples,
				})
			}
			sort.Slice(rb.Models, func(a, b int) bool { return rb.Models[a].Tokens > rb.Models[b].Tokens })
		}
		if len(slot.authIdxBreakdown) > 0 {
			rb.Auths = make([]RingAuthAgg, 0, len(slot.authIdxBreakdown))
			for k, v := range slot.authIdxBreakdown {
				rb.Auths = append(rb.Auths, RingAuthAgg{
					AuthIndex: k,
					Requests:  v.requests,
					Tokens:    v.tokens,
					Failures:  v.failures,
				})
			}
		}
		out.Buckets[i] = rb
	}
	return out
}

// RestoreFromSeed replaces the live ring's state with a previously
// serialised snapshot. Seed buckets are sorted chronologically so snapshots
// written by the older rotating-head implementation remain readable.
//
// The caller is responsible for ensuring seed.Buckets aligns with the
// live ring's bucket count and bucket size. Mismatches are rejected by
// the surrounding guard in (RequestStatistics).restoreRingFromSeed.
func (r *BucketRing) RestoreFromSeed(startTimeMs int64, headIndex int, buckets []RingSeedBucket) {
	if r == nil || len(buckets) != len(r.buckets) {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	ordered := append([]RingSeedBucket(nil), buckets...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].StartTimeMs < ordered[j].StartTimeMs
	})
	if ordered[0].StartTimeMs == 0 {
		ordered[0].StartTimeMs = startTimeMs
	}
	r.startTime = time.UnixMilli(ordered[0].StartTimeMs)
	r.head = len(r.buckets) - 1
	_ = headIndex
	for i, sb := range ordered {
		slot := &r.buckets[i]
		slot.startTime = r.startTime.Add(time.Duration(i) * r.bucketSize)
		slot.requests = sb.Requests
		slot.tokens = sb.Tokens
		slot.failures = sb.Failures
		slot.latencySumMs = sb.LatencySum
		slot.latencySamples = sb.LatencyN
		if len(sb.ModelBreakdown) > 0 {
			slot.modelBreakdown = make(map[string]*modelBucketAcc, len(sb.ModelBreakdown))
			for k, m := range sb.ModelBreakdown {
				slot.modelBreakdown[k] = &modelBucketAcc{
					requests:       m.TotalRequests,
					tokens:         m.TotalTokens,
					failures:       m.Failures,
					latencySumMs:   m.LatencySum,
					latencySamples: m.LatencyN,
				}
			}
		} else {
			slot.modelBreakdown = nil
		}
		if len(sb.AuthBreakdown) > 0 {
			slot.authIdxBreakdown = make(map[string]*authBucketAcc, len(sb.AuthBreakdown))
			for k, a := range sb.AuthBreakdown {
				slot.authIdxBreakdown[k] = &authBucketAcc{
					requests: a.Requests,
					tokens:   a.Tokens,
					failures: a.Failures,
				}
			}
		} else {
			slot.authIdxBreakdown = nil
		}
	}
}
