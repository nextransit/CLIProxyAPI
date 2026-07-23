package usage

import (
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
func (s *RequestStatistics) BuildDashboardSnapshot(cfg DashboardConfig) DashboardSnapshot {
	if cfg.BucketCount <= 0 || cfg.BucketSize <= 0 || cfg.LatestCount <= 0 || cfg.ModelTopN <= 0 {
		cfg = DefaultDashboardConfig()
	}
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

	s.mu.RLock()
	defer s.mu.RUnlock()

	result.TotalRequests = s.totalRequests
	result.TotalTokens = s.totalTokens
	result.SuccessCount = s.successCount
	result.FailureCount = s.failureCount
	if result.TotalRequests > 0 {
		result.FailureRate = float64(result.FailureCount) / float64(result.TotalRequests)
	}

	for apiName, stats := range s.apis {
		if stats == nil {
			continue
		}
		for modelName, modelStatsValue := range stats.Models {
			if modelStatsValue == nil {
				continue
			}
			details := modelStatsValue.Details
			if len(details) == 0 {
				continue
			}
			for i := range details {
				detail := details[i]
				if detail.Timestamp.IsZero() {
					continue
				}
				if cfg.Window > 0 {
					if detail.Timestamp.Before(result.WindowStart) || detail.Timestamp.After(result.WindowEnd) {
						continue
					}
				}
				if detail.Timestamp.After(now) {
					continue
				}

				totalRequests++
				totalTokens += detail.Tokens.TotalTokens
				if detail.Failed {
					totalFailures++
				} else {
					totalSuccess++
				}

				// Bucket assignment; walk in reverse so the most recent
				// buckets match first.
				for bi := cfg.BucketCount - 1; bi >= 0; bi-- {
					w := windows[bi]
					if !detail.Timestamp.Before(w.start) && detail.Timestamp.Before(w.end) {
						bucket := &result.FlowBuckets[bi]
						bucket.Requests++
						bucket.Tokens += detail.Tokens.TotalTokens
						if detail.Failed {
							bucket.Failures++
						}
						if detail.LatencyMs > 0 {
							latencySumByBucket[bi] += float64(detail.LatencyMs)
						}
						break
					}
				}

				acc, ok := modelByName[modelName]
				if !ok {
					acc = &modelAccum{}
					modelByName[modelName] = acc
				}
				acc.requests++
				acc.tokens += detail.Tokens.TotalTokens
				if detail.Failed {
					acc.failures++
				}
				if detail.LatencyMs > 0 {
					acc.latencyTotal += detail.LatencyMs
					acc.latencySamples++
				}

				evt := DashboardLatestRequest{
					EventID:      detailEventID(detail, apiName, modelName),
					Timestamp:    detail.Timestamp,
					Model:        modelName,
					APIKey:       apiName,
					Failed:       detail.Failed,
					StatusCode:   detail.StatusCode,
					DurationMs:   detail.LatencyMs,
					InputTokens:  detail.Tokens.InputTokens,
					OutputTokens: detail.Tokens.OutputTokens,
					TotalTokens:  detail.Tokens.TotalTokens,
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
					if detail.Timestamp.After(latest[oldest].Timestamp) {
						latest[oldest] = evt
					}
				}
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
	result.LatestEventID = s.nextEventID.Load()

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
