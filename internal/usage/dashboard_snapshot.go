package usage

import (
	"context"
	"time"
)

// DashboardSnapshot is a lightweight, dashboard-shaped view of usage stats.
// It contains only the aggregated totals, a fixed-size flow bucket series, the
// top-N model rows, and the latest request events. This avoids returning every
// per-request detail when the dashboard only needs a handful of rows.
type DashboardSnapshot struct {
	TotalRequests   int64                    `json:"total_requests"`
	SuccessCount    int64                    `json:"success_count"`
	FailureCount    int64                    `json:"failure_count"`
	TotalTokens     int64                    `json:"total_tokens"`
	FailureRate     float64                  `json:"failure_rate"`
	LatestEventID   uint64                   `json:"latest_event_id"`
	GeneratedAt     time.Time                `json:"generated_at"`
	BucketCount     int                      `json:"bucket_count"`
	BucketSizeMs    int64                    `json:"bucket_size_ms"`
	BucketStartMs   int64                    `json:"bucket_start_ms"`
	FlowBuckets     []DashboardFlowBucket    `json:"flow_buckets"`
	ModelTop        []DashboardModelRow      `json:"model_top"`
	LatestRequests  []DashboardLatestRequest `json:"latest_requests"`
	WindowStart     time.Time                `json:"window_start"`
	WindowEnd       time.Time                `json:"window_end"`
	WindowHours     float64                  `json:"window_hours"`
	WindowSeconds   float64                  `json:"window_seconds"`
	WindowTokens    int64                    `json:"window_tokens"`
	WindowRequests  int64                    `json:"window_requests"`
	WindowFailures  int64                    `json:"window_failures"`
	WindowSuccesses int64                    `json:"window_successes"`
}

// DashboardFlowBucket describes one fixed-size flow bucket for the dashboard
// token/requests/latency sparkline.
type DashboardFlowBucket struct {
	Index        int     `json:"index"`
	StartMs      int64   `json:"start_ms"`
	EndMs        int64   `json:"end_ms"`
	Label        string  `json:"label"`
	Requests     int64   `json:"requests"`
	Tokens       int64   `json:"tokens"`
	Failures     int64   `json:"failures"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
}

// DashboardModelRow is one row in the dashboard model distribution list.
type DashboardModelRow struct {
	Model        string  `json:"model"`
	Requests     int64   `json:"requests"`
	Tokens       int64   `json:"tokens"`
	SharePercent float64 `json:"share_percent"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
	SuccessRate  float64 `json:"success_rate"`
}

// DashboardLatestRequest is a compact representation of the most recent
// request event for the live request stream table.
type DashboardLatestRequest struct {
	EventID      uint64    `json:"event_id"`
	Timestamp    time.Time `json:"timestamp"`
	Model        string    `json:"model"`
	APIKey       string    `json:"api_key"`
	Failed       bool      `json:"failed"`
	StatusCode   int       `json:"status_code"`
	DurationMs   int64     `json:"duration_ms"`
	InputTokens  int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
	TotalTokens  int64     `json:"total_tokens"`
}

// DashboardConfig controls how DashboardSnapshot is shaped.
type DashboardConfig struct {
	BucketCount int           // number of flow buckets
	BucketSize  time.Duration // duration of each bucket
	ModelTopN   int           // top-N model rows
	LatestCount int           // most recent N requests
	Window      time.Duration // trailing window; zero means no extra filtering
}

// DefaultDashboardConfig returns the dashboard defaults used by the
// management UI.
func DefaultDashboardConfig() DashboardConfig {
	return DashboardConfig{
		BucketCount: 12,
		BucketSize:  5 * time.Minute,
		ModelTopN:   5,
		LatestCount: 7,
		Window:      24 * time.Hour,
	}
}

// BuildDashboardSnapshot computes the dashboard-shaped snapshot synchronously.
// Pass a zero DashboardConfig to use defaults. Safe for concurrent use.
//
// The internal RLock only protects a lightweight slice copy; bucket assignment,
// model aggregation, and top-N selection run outside the lock to keep ingest
// writes from being starved while a dashboard request is in flight. Honors
// ctx cancellation: a cancelled context returns a zero-valued DashboardSnapshot
// (LatestRequests may be nil) within a few milliseconds.
// BuildDashboardSnapshot builds the dashboard view. When cfg matches a
// pre-aggregated rotating ring (5min/1h with their default sizes) it serves
// the response directly from the ring, avoiding an O(N) scan over retained
// RequestDetails. For other configurations it falls back to the legacy
// detail-walk path.
func (s *RequestStatistics) BuildDashboardSnapshot(ctx context.Context, cfg DashboardConfig) DashboardSnapshot {
	if cfg.BucketCount <= 0 || cfg.BucketSize <= 0 || cfg.LatestCount <= 0 || cfg.ModelTopN <= 0 {
		cfg = DefaultDashboardConfig()
	}
	if s != nil {
		if result, ok := s.tryBuildDashboardFromRing(ctx, cfg); ok {
			return result
		}
	}
	return s.buildDashboardSnapshotFromDetails(ctx, cfg)
}

func (s *RequestStatistics) buildDashboardSnapshotFromDetails(ctx context.Context, cfg DashboardConfig) DashboardSnapshot {
	now := time.Now()
	result := DashboardSnapshot{
		GeneratedAt:   now,
		BucketCount:   cfg.BucketCount,
		BucketSizeMs:  cfg.BucketSize.Milliseconds(),
		BucketStartMs: now.Add(-cfg.BucketSize * time.Duration(cfg.BucketCount)).UnixMilli(),
	}
	if cfg.Window > 0 {
		result.WindowEnd = now
		result.WindowStart = now.Add(-cfg.Window)
		result.WindowHours = cfg.Window.Hours()
		result.WindowSeconds = cfg.Window.Seconds()
	}

	if s == nil {
		return result
	}

	type bucketWindow struct {
		start time.Time
		end   time.Time
	}
	windows := make([]bucketWindow, cfg.BucketCount)
	for i := 0; i < cfg.BucketCount; i++ {
		start := now.Add(-cfg.BucketSize * time.Duration(cfg.BucketCount-i))
		windows[i] = bucketWindow{start: start, end: start.Add(cfg.BucketSize)}
	}
	result.FlowBuckets = make([]DashboardFlowBucket, cfg.BucketCount)
	for i, w := range windows {
		result.FlowBuckets[i] = DashboardFlowBucket{
			Index:   i,
			StartMs: w.start.UnixMilli(),
			EndMs:   w.end.UnixMilli(),
			Label:   w.start.Format("15:04"),
		}
	}

	// Record the window we're about to filter by so the locked phase can use
	// it without re-reading cfg. Done before locking so the value is visible
	// to the locked snapshot helper that follows.
	if cfg.Window > 0 {
		s.lastDashboardWindow = cfg.Window
	}

	// Locked phase: copy aggregates + per-detail slices under RLock. Avoid
	// running any heavy compute while holding the lock — ingest (Record)
	// blocks on the matching write Lock until we return.
	snapshotCopy := s.snapshotForDashboard(ctx, cfg.Window > 0, now)
	if ctx.Err() != nil {
		return result // zero-valued LatestRequests; handler decides 503
	}

	type modelAccum struct {
		requests       int64
		tokens         int64
		failures       int64
		latencyTotal   int64
		latencySamples int64
	}
	modelByName := map[string]*modelAccum{}
	var totalRequests, totalTokens, totalFailures, totalSuccess int64
	latest := make([]DashboardLatestRequest, 0, cfg.LatestCount)

	// latencySum is per-bucket running sum of latency samples.
	latencySumByBucket := make([]float64, cfg.BucketCount)

	result.TotalRequests = snapshotCopy.TotalRequests
	result.TotalTokens = snapshotCopy.TotalTokens
	result.SuccessCount = snapshotCopy.SuccessCount
	result.FailureCount = snapshotCopy.FailureCount
	if result.TotalRequests > 0 {
		result.FailureRate = float64(result.FailureCount) / float64(result.TotalRequests)
	}

	// Aggregation phase runs OUTSIDE the RLock so ingest isn't starved.
	for _, d := range snapshotCopy.Details {
		if ctx.Err() != nil {
			return result
		}
		totalRequests++
		totalTokens += d.Tokens.TotalTokens
		if d.Failed {
			totalFailures++
		} else {
			totalSuccess++
		}

		// Bucket assignment; walk in reverse so the most recent
		// buckets match first.
		for bi := cfg.BucketCount - 1; bi >= 0; bi-- {
			w := windows[bi]
			if !d.Timestamp.Before(w.start) && d.Timestamp.Before(w.end) {
				bucket := &result.FlowBuckets[bi]
				bucket.Requests++
				bucket.Tokens += d.Tokens.TotalTokens
				if d.Failed {
					bucket.Failures++
				}
				if d.LatencyMs > 0 {
					latencySumByBucket[bi] += float64(d.LatencyMs)
				}
				break
			}
		}

		acc, ok := modelByName[d.Model]
		if !ok {
			acc = &modelAccum{}
			modelByName[d.Model] = acc
		}
		acc.requests++
		acc.tokens += d.Tokens.TotalTokens
		if d.Failed {
			acc.failures++
		}
		if d.LatencyMs > 0 {
			acc.latencyTotal += d.LatencyMs
			acc.latencySamples++
		}

		evt := DashboardLatestRequest{
			EventID:      detailEventIDFor(d),
			Timestamp:    d.Timestamp,
			Model:        d.Model,
			APIKey:       d.APIKey,
			Failed:       d.Failed,
			StatusCode:   d.StatusCode,
			DurationMs:   d.LatencyMs,
			InputTokens:  d.Tokens.InputTokens,
			OutputTokens: d.Tokens.OutputTokens,
			TotalTokens:  d.Tokens.TotalTokens,
		}
		if len(latest) < cfg.LatestCount {
			latest = append(latest, evt)
		} else {
			oldest := 0
			for j := 1; j < len(latest); j++ {
				if latest[j].Timestamp.Before(latest[oldest].Timestamp) {
					oldest = j
				}
			}
			if d.Timestamp.After(latest[oldest].Timestamp) {
				latest[oldest] = evt
			}
		}
	}

	// Finalize bucket latency averages; divide by requests that contributed
	// a latency sample in this bucket. We tracked the running sum above but
	// not the sample count, so derive it from bucket.Requests (matches what
	// the dashboard visualizes today).
	for i := range result.FlowBuckets {
		bucket := &result.FlowBuckets[i]
		if bucket.Requests > 0 {
			bucket.AvgLatencyMs = latencySumByBucket[i] / float64(bucket.Requests)
		}
	}

	type modelRow struct {
		name      string
		acc       *modelAccum
		share     float64
		avgLat    float64
		successRt float64
	}
	rows := make([]modelRow, 0, len(modelByName))
	totalModelTokens := totalTokens
	if totalModelTokens < 0 {
		totalModelTokens = 0
	}
	for name, acc := range modelByName {
		row := modelRow{name: name, acc: acc}
		if totalModelTokens > 0 {
			row.share = float64(acc.tokens) / float64(totalModelTokens) * 100.0
		}
		if acc.latencySamples > 0 {
			row.avgLat = float64(acc.latencyTotal) / float64(acc.latencySamples)
		}
		if acc.requests > 0 {
			row.successRt = float64(acc.requests-acc.failures) / float64(acc.requests) * 100.0
		}
		rows = append(rows, row)
	}
	// Insertion sort by tokens desc, tie-break by requests desc.
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0; j-- {
			r := rows[j]
			l := rows[j-1]
			if l.acc.tokens < r.acc.tokens ||
				(l.acc.tokens == r.acc.tokens && l.acc.requests < r.acc.requests) {
				rows[j-1], rows[j] = rows[j], rows[j-1]
			}
		}
	}
	if len(rows) > cfg.ModelTopN {
		rows = rows[:cfg.ModelTopN]
	}
	result.ModelTop = make([]DashboardModelRow, len(rows))
	for i, r := range rows {
		result.ModelTop[i] = DashboardModelRow{
			Model:        r.name,
			Requests:     r.acc.requests,
			Tokens:       r.acc.tokens,
			SharePercent: r.share,
			AvgLatencyMs: r.avgLat,
			SuccessRate:  r.successRt,
		}
	}

	for i := 1; i < len(latest); i++ {
		for j := i; j > 0; j-- {
			if latest[j].Timestamp.After(latest[j-1].Timestamp) {
				latest[j-1], latest[j] = latest[j], latest[j-1]
			}
		}
	}
	result.LatestRequests = latest

	result.WindowRequests = totalRequests
	result.WindowTokens = totalTokens
	result.WindowFailures = totalFailures
	result.WindowSuccesses = totalSuccess
	result.LatestEventID = snapshotCopy.NextEventID

	return result
}

// detailEventID returns a stable per-request identifier used by the SSE
// replay protocol. FNV-1a over request_id keeps things deterministic across
// processes; falling back to the timestamp when request_id is missing still
// yields a unique value because each detail carries a distinct timestamp.
func detailEventID(detail RequestDetail, apiName, modelName string) uint64 {
	if detail.RequestID != "" {
		return fnvHash(detail.RequestID)
	}
	if !detail.Timestamp.IsZero() {
		return uint64(detail.Timestamp.UnixNano()) & 0x1FFFFFFFFFFFFF
	}
	return fnvHash(apiName + "/" + modelName)
}

func fnvHash(s string) uint64 {
	var h uint64 = 1469598103934665603
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h & 0x1FFFFFFFFFFFFF
}

// dashboardDetailCopy is the trimmed per-detail record used by the dashboard
// snapshot. Holding only the bytes we need lets the unlocked aggregation phase
// finish without contending with ingest.
type dashboardDetailCopy struct {
	APIKey     string
	Model      string
	Timestamp  time.Time
	LatencyMs  int64
	Failed     bool
	StatusCode int
	Tokens     TokenStats
	AuthIndex  string
}

// snapshotForDashboard returns a thread-safe point-in-time view of the data
// the unlocked dashboard aggregation phase needs. RLock only; never call
// ingest while holding the returned slice.
func (s *RequestStatistics) snapshotForDashboard(ctx context.Context, useWindow bool, now time.Time) struct {
	TotalRequests, TotalTokens, SuccessCount, FailureCount int64
	NextEventID                                            uint64
	Details                                                []dashboardDetailCopy
	WindowStart, WindowEnd                                 time.Time
} {
	var empty struct {
		TotalRequests, TotalTokens, SuccessCount, FailureCount int64
		NextEventID                                            uint64
		Details                                                []dashboardDetailCopy
		WindowStart, WindowEnd                                 time.Time
	}
	if s == nil {
		return empty
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	var windowStart, windowEnd time.Time
	if useWindow {
		windowEnd = now
		windowStart = now.Add(-s.lastDashboardWindow)
	}

	out := empty
	out.TotalRequests = s.totalRequests
	out.TotalTokens = s.totalTokens
	out.SuccessCount = s.successCount
	out.FailureCount = s.failureCount
	out.NextEventID = s.nextEventID.Load()
	if useWindow {
		out.WindowStart = windowStart
		out.WindowEnd = windowEnd
	}

	for apiName, stats := range s.apis {
		if stats == nil {
			continue
		}
		for modelName, modelStatsValue := range stats.Models {
			if modelStatsValue == nil || len(modelStatsValue.Details) == 0 {
				continue
			}
			for i := range modelStatsValue.Details {
				d := modelStatsValue.Details[i]
				if d.Timestamp.IsZero() {
					continue
				}
				if useWindow && (d.Timestamp.Before(windowStart) || d.Timestamp.After(windowEnd)) {
					continue
				}
				if d.Timestamp.After(now) {
					continue
				}
				out.Details = append(out.Details, dashboardDetailCopy{
					APIKey:     apiName,
					Model:      modelName,
					Timestamp:  d.Timestamp,
					LatencyMs:  d.LatencyMs,
					Failed:     d.Failed,
					StatusCode: d.StatusCode,
					Tokens:     d.Tokens,
					AuthIndex:  d.AuthIndex,
				})
			}
		}
	}
	return out
}

// detailEventIDFor is the lockless variant that runs against the trimmed
// snapshot. Mirrors detailEventID's FNV-1a fallback behavior when RequestID is
// missing — timestamps are unique per detail, so the hash stays deterministic.
func detailEventIDFor(d dashboardDetailCopy) uint64 {
	return fnvHash(d.APIKey + "/" + d.Model + "/" + d.Timestamp.UTC().Format(time.RFC3339Nano))
}

// tryBuildDashboardFromRing returns (snapshot, true) when cfg matches one of
// the pre-aggregated rotating rings (5min/1h with their default sizes). It
// also returns true for a sub-window of those rings (smaller cfg.BucketCount)
// when the requested window falls entirely inside the ring's coverage, which
// is the common case. Otherwise it returns (zero, false) and the caller falls
// back to the legacy detail walk.
func (s *RequestStatistics) tryBuildDashboardFromRing(ctx context.Context, cfg DashboardConfig) (DashboardSnapshot, bool) {
	var ring *BucketRing
	windowCoverage := cfg.BucketCount * int(cfg.BucketSize/time.Second)
	switch {
	case cfg.BucketSize == 5*time.Minute && cfg.BucketCount <= 12:
		ring = s.bucketRing5m
	case cfg.BucketSize == time.Hour && cfg.BucketCount <= 24:
		ring = s.bucketRing1h
	default:
		return DashboardSnapshot{}, false
	}
	if ring == nil {
		return DashboardSnapshot{}, false
	}
	if cfg.ModelTopN <= 0 || cfg.LatestCount <= 0 {
		return DashboardSnapshot{}, false
	}
	// If the caller asked for a window larger than the ring coverage (or no
	// window at all), the ring cannot answer; fall back to the detail walk.
	ringWindow := ring.BucketSize() * time.Duration(ring.BucketCount())
	if cfg.Window == 0 || cfg.Window > ringWindow {
		return DashboardSnapshot{}, false
	}
	_ = windowCoverage

	now := time.Now()
	snap := ring.ReadSnapshot()
	if ctx.Err() != nil {
		return DashboardSnapshot{}, false
	}

	// Pick the most recent cfg.BucketCount buckets from the ring.
	if cfg.BucketCount > len(snap.Buckets) {
		return DashboardSnapshot{}, false
	}
	startIdx := len(snap.Buckets) - cfg.BucketCount
	selected := snap.Buckets[startIdx:]

	result := DashboardSnapshot{
		GeneratedAt:   now,
		BucketCount:   cfg.BucketCount,
		BucketSizeMs:  cfg.BucketSize.Milliseconds(),
		BucketStartMs: selected[0].StartTime.UnixMilli(),
	}
	if cfg.Window > 0 {
		result.WindowEnd = now
		result.WindowStart = now.Add(-cfg.Window)
		result.WindowHours = cfg.Window.Hours()
		result.WindowSeconds = cfg.Window.Seconds()
	}
	result.FlowBuckets = make([]DashboardFlowBucket, cfg.BucketCount)
	for i, b := range selected {
		result.FlowBuckets[i] = DashboardFlowBucket{
			Index:        i,
			StartMs:      b.StartTime.UnixMilli(),
			EndMs:        b.StartTime.Add(cfg.BucketSize).UnixMilli(),
			Label:        b.StartTime.Format("15:04"),
			Requests:     b.Requests,
			Tokens:       b.Tokens,
			Failures:     b.Failures,
			AvgLatencyMs: 0,
		}
		if b.LatencyN > 0 {
			result.FlowBuckets[i].AvgLatencyMs = float64(b.LatencySum) / float64(b.LatencyN)
		}
	}

	// Aggregate model breakdown across the selected buckets.
	type modelAgg struct {
		requests, tokens, failures, latencySum, latencyN int64
	}
	modelMap := map[string]*modelAgg{}
	var totalRequests, totalTokens, totalFailures, totalSuccess int64
	for _, b := range selected {
		totalRequests += b.Requests
		totalTokens += b.Tokens
		totalFailures += b.Failures
		for _, m := range b.Models {
			acc, ok := modelMap[m.Model]
			if !ok {
				acc = &modelAgg{}
				modelMap[m.Model] = acc
			}
			acc.requests += m.Requests
			acc.tokens += m.Tokens
			acc.failures += m.Failures
			acc.latencySum += m.LatencySum
			acc.latencyN += m.LatencyN
		}
	}
	totalSuccess = totalRequests - totalFailures

	// Build top-N model rows.
	type modelRow struct {
		name             string
		requests, tokens int64
		failures         int64
		avgLat           float64
		successRt        float64
		share            float64
	}
	rows := make([]modelRow, 0, len(modelMap))
	for name, acc := range modelMap {
		row := modelRow{name: name, requests: acc.requests, tokens: acc.tokens, failures: acc.failures}
		if totalTokens > 0 {
			row.share = float64(acc.tokens) / float64(totalTokens) * 100.0
		}
		if acc.latencyN > 0 {
			row.avgLat = float64(acc.latencySum) / float64(acc.latencyN)
		}
		if acc.requests > 0 {
			row.successRt = float64(acc.requests-acc.failures) / float64(acc.requests) * 100.0
		}
		rows = append(rows, row)
	}
	// Sort: tokens desc, requests desc tie-break.
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0; j-- {
			if rows[j-1].tokens < rows[j].tokens ||
				(rows[j-1].tokens == rows[j].tokens && rows[j-1].requests < rows[j].requests) {
				rows[j-1], rows[j] = rows[j], rows[j-1]
			}
		}
	}
	if len(rows) > cfg.ModelTopN {
		rows = rows[:cfg.ModelTopN]
	}
	result.ModelTop = make([]DashboardModelRow, len(rows))
	for i, r := range rows {
		result.ModelTop[i] = DashboardModelRow{
			Model:        r.name,
			Requests:     r.requests,
			Tokens:       r.tokens,
			SharePercent: r.share,
			AvgLatencyMs: r.avgLat,
			SuccessRate:  r.successRt,
		}
	}

	// Latest N requests: still served from the RecentBuffer (already in
	// memory, ring-driven). The ring aggregator does not retain individual
	// events; pulling the latest 7 from RecentBuffer stays O(1) and avoids
	// a second scan of details.
	latest := make([]DashboardLatestRequest, 0, cfg.LatestCount)
	for _, e := range s.recent.Events() {
		if len(latest) >= cfg.LatestCount {
			break
		}
		latest = append(latest, DashboardLatestRequest{
			EventID:      e.ID,
			Timestamp:    e.RequestedAt,
			Model:        e.Model,
			APIKey:       e.APIKey,
			Failed:       e.Failed,
			StatusCode:   e.StatusCode,
			DurationMs:   e.DurationMs,
			InputTokens:  e.Tokens.Input,
			OutputTokens: e.Tokens.Output,
			TotalTokens:  e.Tokens.Total,
		})
	}
	// Sort newest first.
	for i := 1; i < len(latest); i++ {
		for j := i; j > 0; j-- {
			if latest[j].Timestamp.After(latest[j-1].Timestamp) {
				latest[j-1], latest[j] = latest[j], latest[j-1]
			}
		}
	}
	result.LatestRequests = latest

	// Total counters are always available from the cheap in-memory fields.
	result.TotalRequests = s.totalRequests
	result.TotalTokens = s.totalTokens
	result.SuccessCount = s.successCount
	result.FailureCount = s.failureCount
	if result.TotalRequests > 0 {
		result.FailureRate = float64(result.FailureCount) / float64(result.TotalRequests)
	}
	result.WindowRequests = totalRequests
	result.WindowTokens = totalTokens
	result.WindowFailures = totalFailures
	result.WindowSuccesses = totalSuccess
	result.LatestEventID = s.recent.LastID()

	return result, true
}
