package management

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

// SummaryBucket is one bucket of the pre-aggregated ring as exposed by
// GET /v0/management/usage/summary. It contains only aggregate counters;
// per-request details are not returned by this endpoint.
type SummaryBucket struct {
	Index        int     `json:"index"`
	StartMs      int64   `json:"start_ms"`
	EndMs        int64   `json:"end_ms"`
	Label        string  `json:"label"`
	Requests     int64   `json:"requests"`
	Tokens       int64   `json:"tokens"`
	Failures     int64   `json:"failures"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
}

// SummaryModelAgg is the model-level aggregate over the entire ring.
type SummaryModelAgg struct {
	Model       string  `json:"model"`
	Requests    int64   `json:"requests"`
	Tokens      int64   `json:"tokens"`
	Failures    int64   `json:"failures"`
	SharePct    float64 `json:"share_percent"`
	SuccessRate float64 `json:"success_rate"`
	AvgLatency  float64 `json:"avg_latency_ms"`
}

// SummaryAuthAgg is the authIndex-level aggregate.
type SummaryAuthAgg struct {
	AuthIndex string `json:"auth_index"`
	Requests  int64  `json:"requests"`
	Tokens    int64  `json:"tokens"`
	Failures  int64  `json:"failures"`
}

// usageSummaryResponse is the JSON shape returned by /usage/summary.
type usageSummaryResponse struct {
	GeneratedAt   time.Time         `json:"generated_at"`
	BucketSizeMs5 int64             `json:"bucket_size_ms_5m"`
	Count5m       int               `json:"bucket_count_5m"`
	BucketSizeMs1 int64             `json:"bucket_size_ms_1h"`
	Count1h       int               `json:"bucket_count_1h"`
	Buckets5m     []SummaryBucket   `json:"buckets_5m"`
	Buckets1h     []SummaryBucket   `json:"buckets_1h"`
	ModelTop      []SummaryModelAgg `json:"model_top"`
	AuthTop       []SummaryAuthAgg  `json:"auth_top"`
	TotalRequests int64             `json:"total_requests"`
	TotalTokens   int64             `json:"total_tokens"`
	SuccessCount  int64             `json:"success_count"`
	FailureCount  int64             `json:"failure_count"`
	FailureRate   float64           `json:"failure_rate"`
}

// GetUsageSummary returns the pre-aggregated summary view backed entirely by
// the rotating BucketRings. It is the lightweight counterpart of
// /usage/dashboard for callers that want stable aggregates without paying
// for a full /usage payload. The endpoint is intentionally additive; it does
// not change /usage or /usage/dashboard semantics.
func (h *Handler) GetUsageSummary(c *gin.Context) {
	if h == nil || h.usageStats == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "usage statistics unavailable"})
		return
	}
	if _, err := usage.RestoreStatisticsIfEmpty(h.usageStats); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	modelTop := parsePositiveInt(c.Query("model_top"), 0)
	if modelTop <= 0 || modelTop > 50 {
		modelTop = 10
	}
	authTop := parsePositiveInt(c.Query("auth_top"), 0)
	if authTop <= 0 || authTop > 50 {
		authTop = 10
	}

	resp := usageSummaryResponse{
		GeneratedAt:   time.Now().UTC(),
		TotalRequests: h.usageStats.TotalRequests(),
		TotalTokens:   h.usageStats.TotalTokens(),
		SuccessCount:  h.usageStats.SuccessCount(),
		FailureCount:  h.usageStats.FailureCount(),
	}
	if resp.TotalRequests > 0 {
		resp.FailureRate = float64(resp.FailureCount) / float64(resp.TotalRequests)
	}
	resp.Buckets5m = summaryBucketsFromRing(h.usageStats.BucketRing5m(), modelTop)
	resp.Buckets1h = summaryBucketsFromRing(h.usageStats.BucketRing1h(), modelTop)
	resp.BucketSizeMs5 = int64(len(resp.Buckets5m)) // placeholder; overwritten below
	if ring := h.usageStats.BucketRing5m(); ring != nil {
		resp.BucketSizeMs5 = ring.BucketSize().Milliseconds()
		resp.Count5m = ring.BucketCount()
	}
	if ring := h.usageStats.BucketRing1h(); ring != nil {
		resp.BucketSizeMs1 = ring.BucketSize().Milliseconds()
		resp.Count1h = ring.BucketCount()
	}
	resp.ModelTop = summaryTopModels(h.usageStats, modelTop)
	resp.AuthTop = summaryTopAuths(h.usageStats, authTop)

	c.JSON(http.StatusOK, resp)
}

// _ = strings.ToLower keeps strings imported even if future toggles drop in.
var _ = strings.ToLower

func summaryBucketsFromRing(ring *usage.BucketRing, _ int) []SummaryBucket {
	if ring == nil {
		return nil
	}
	snap := ring.ReadSnapshot()
	out := make([]SummaryBucket, 0, len(snap.Buckets))
	for i, b := range snap.Buckets {
		bucket := SummaryBucket{
			Index:    i,
			StartMs:  b.StartTime.UnixMilli(),
			EndMs:    b.StartTime.Add(snap.BucketSize).UnixMilli(),
			Label:    b.StartTime.Format("15:04"),
			Requests: b.Requests,
			Tokens:   b.Tokens,
			Failures: b.Failures,
		}
		if b.LatencyN > 0 {
			bucket.AvgLatencyMs = float64(b.LatencySum) / float64(b.LatencyN)
		}
		out = append(out, bucket)
	}
	return out
}

func summaryTopModels(stats *usage.RequestStatistics, top int) []SummaryModelAgg {
	if stats == nil {
		return nil
	}
	ring5m := stats.BucketRing5m()
	if ring5m == nil {
		return nil
	}
	snap := ring5m.ReadSnapshot()
	type agg struct {
		requests       int64
		tokens         int64
		failures       int64
		latencySum     int64
		latencySamples int64
	}
	m := map[string]*agg{}
	var totalTokens int64
	for _, b := range snap.Buckets {
		for _, mb := range b.Models {
			a, ok := m[mb.Model]
			if !ok {
				a = &agg{}
				m[mb.Model] = a
			}
			a.requests += mb.Requests
			a.tokens += mb.Tokens
			a.failures += mb.Failures
			a.latencySum += mb.LatencySum
			a.latencySamples += mb.LatencyN
			totalTokens += mb.Tokens
		}
	}
	rows := make([]SummaryModelAgg, 0, len(m))
	for name, a := range m {
		row := SummaryModelAgg{
			Model:    name,
			Requests: a.requests,
			Tokens:   a.tokens,
			Failures: a.failures,
		}
		if totalTokens > 0 {
			row.SharePct = float64(a.tokens) / float64(totalTokens) * 100.0
		}
		if a.requests > 0 {
			row.SuccessRate = float64(a.requests-a.failures) / float64(a.requests) * 100.0
		}
		if a.latencySamples > 0 {
			row.AvgLatency = float64(a.latencySum) / float64(a.latencySamples)
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Tokens != rows[j].Tokens {
			return rows[i].Tokens > rows[j].Tokens
		}
		return rows[i].Requests > rows[j].Requests
	})
	if len(rows) > top {
		rows = rows[:top]
	}
	return rows
}

func summaryTopAuths(stats *usage.RequestStatistics, top int) []SummaryAuthAgg {
	if stats == nil {
		return nil
	}
	ring5m := stats.BucketRing5m()
	if ring5m == nil {
		return nil
	}
	snap := ring5m.ReadSnapshot()
	m := map[string]*SummaryAuthAgg{}
	for _, b := range snap.Buckets {
		for _, ab := range b.Auths {
			a, ok := m[ab.AuthIndex]
			if !ok {
				a = &SummaryAuthAgg{AuthIndex: ab.AuthIndex}
				m[ab.AuthIndex] = a
			}
			a.Requests += ab.Requests
			a.Tokens += ab.Tokens
			a.Failures += ab.Failures
		}
	}
	rows := make([]SummaryAuthAgg, 0, len(m))
	for _, v := range m {
		rows = append(rows, *v)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Requests != rows[j].Requests {
			return rows[i].Requests > rows[j].Requests
		}
		return rows[i].Tokens > rows[j].Tokens
	})
	if len(rows) > top {
		rows = rows[:top]
	}
	return rows
}
