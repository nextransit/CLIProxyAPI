package management

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

type usageExportPayload struct {
	Version    int                      `json:"version"`
	ExportedAt time.Time                `json:"exported_at"`
	Usage      usage.StatisticsSnapshot `json:"usage"`
}

type usageImportPayload struct {
	Version int                      `json:"version"`
	Usage   usage.StatisticsSnapshot `json:"usage"`
}

// usageStatisticsResponse wraps StatisticsSnapshot and adds formatted source display names.
type usageStatisticsResponse struct {
	usage.StatisticsSnapshot
	SourceDisplay map[string]string `json:"source_display"`
}

// GetUsageStatistics returns the in-memory request statistics snapshot.
func (h *Handler) GetUsageStatistics(c *gin.Context) {
	var snapshot usage.StatisticsSnapshot
	if h != nil && h.usageStats != nil {
		if _, err := usage.RestoreStatisticsIfEmpty(h.usageStats); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		snapshot = h.usageStats.Snapshot()
	}
	if filtered, ok := filterUsageSnapshotByTimeRange(snapshot, c.Query("time_range"), time.Now()); ok {
		snapshot = filtered
	}

	// Build AuthIndex -> display name mapping
	displayMap := h.authIndexDisplayMap()

	// Transform snapshot to replace AuthIndex with formatted display name
	transformed := h.transformUsageSnapshotWithDisplayNames(&snapshot, displayMap)

	c.JSON(http.StatusOK, gin.H{
		"usage":           transformed,
		"failed_requests": snapshot.FailureCount,
	})
}

func filterUsageSnapshotByTimeRange(snapshot usage.StatisticsSnapshot, rawRange string, now time.Time) (usage.StatisticsSnapshot, bool) {
	start, end, ok := resolveUsageSnapshotWindow(rawRange, now)
	if !ok {
		return snapshot, false
	}
	return filterUsageSnapshotByWindow(snapshot, start, end), true
}

func resolveUsageSnapshotWindow(rawRange string, now time.Time) (time.Time, time.Time, bool) {
	key := strings.ToLower(strings.TrimSpace(rawRange))
	if key == "" || key == "all" {
		return time.Time{}, time.Time{}, false
	}
	if now.IsZero() {
		now = time.Now()
	}
	switch key {
	case "today":
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()), now, true
	case "7h":
		return now.Add(-7 * time.Hour), now, true
	case "24h":
		return now.Add(-24 * time.Hour), now, true
	case "7d":
		return now.Add(-7 * 24 * time.Hour), now, true
	case "30d":
		return now.Add(-30 * 24 * time.Hour), now, true
	default:
		return time.Time{}, time.Time{}, false
	}
}

func filterUsageSnapshotByWindow(snapshot usage.StatisticsSnapshot, start, end time.Time) usage.StatisticsSnapshot {
	bucketLocation := start.Location()
	result := usage.StatisticsSnapshot{
		APIs:           make(map[string]usage.APISnapshot),
		RequestsByDay:  make(map[string]int64),
		RequestsByHour: make(map[string]int64),
		TokensByDay:    make(map[string]int64),
		TokensByHour:   make(map[string]int64),
	}
	// Carry forward the lifetime counters when the requested window
	// covers "everything" (Window = 0 / "all"). The detail-derived totals
	// below replace these when the window is narrower than the lifetime
	// history; that replacement keeps time-range queries honest at the
	// cost of slightly undercounting the window when Details have been
	// capped. The RequestsByDay map above is the canonical answer for
	// the day-level view either way.
	if start.IsZero() && end.IsZero() {
		result.TotalRequests = snapshot.TotalRequests
		result.TotalTokens = snapshot.TotalTokens
		result.SuccessCount = snapshot.SuccessCount
		result.FailureCount = snapshot.FailureCount
	}
	// Inherit the day/hour buckets from the source snapshot filtered to the
	// window. This is the canonical path for time-range queries: when the
	// per-model Details slice has been capped and does not retain every
	// request, the persisted day/hour bucket map is still accurate (it is
	// updated monotonically in Record() and survives restart via
	// ApplyAggregateSnapshot). Without this, /usage?time_range=today
	// returns totals that match only the retained Detail rows, which is
	// strictly less than the real counter.
	for k, v := range snapshot.RequestsByDay {
		dayTime, err := time.ParseInLocation("2006-01-02", k, bucketLocation)
		if err != nil {
			continue
		}
		// Include any day that overlaps the requested window. A day overlaps
		// when its start (dayTime) is before the window end AND its end
		// (dayTime+24h) is after the window start. This keeps partial-day
		// windows (e.g. 24h spanning two days) from dropping an entire day
		// when the window starts mid-day.
		if dayTime.Add(24*time.Hour).After(start) && !dayTime.After(end) {
			result.RequestsByDay[k] = v
		}
	}
	for k, v := range snapshot.TokensByDay {
		dayTime, err := time.ParseInLocation("2006-01-02", k, bucketLocation)
		if err != nil {
			continue
		}
		if dayTime.Add(24*time.Hour).After(start) && !dayTime.After(end) {
			result.TokensByDay[k] = v
		}
	}
	// Save the day-bucket-derived totals separately. The detail loop
	// below also accumulates into result.TotalRequests/Tokens; at the
	// end we take the max of the two, so when a snapshot carries both
	// day buckets (canonical, monotonic from Record) and a capped
	// Details slice, the day buckets win by being larger.
	dayBucketRequests := int64(0)
	dayBucketTokens := int64(0)
	for _, v := range result.RequestsByDay {
		dayBucketRequests += v
	}
	for _, v := range result.TokensByDay {
		dayBucketTokens += v
	}

	// Detail-derived counters track what we compute from the per-request
	// Details slice. Day-bucket-derived totals (from snapshot.RequestsByDay)
	// are the canonical source when present; at the end we take the max
	// of the two to avoid double-counting on restart (where both the day
	// bucket maps and the capped Details slice cover the same requests).
	var detailReqs, detailTokens, detailSuccess, detailFailure int64
	detailReqsByDay := make(map[string]int64)
	detailTokensByDay := make(map[string]int64)

	for apiName, apiSnapshot := range snapshot.APIs {
		apiCopy := usage.APISnapshot{
			Models: make(map[string]usage.ModelSnapshot),
		}
		for modelName, modelSnapshot := range apiSnapshot.Models {
			modelCopy := usage.ModelSnapshot{
				Details: make([]usage.RequestDetail, 0, len(modelSnapshot.Details)),
			}
			for _, detail := range modelSnapshot.Details {
				timestamp := detail.Timestamp
				if timestamp.IsZero() || timestamp.Before(start) || timestamp.After(end) {
					continue
				}
				modelCopy.Details = append(modelCopy.Details, detail)
				modelCopy.TotalRequests++
				modelCopy.TotalTokens += detail.Tokens.TotalTokens
				apiCopy.TotalRequests++
				apiCopy.TotalTokens += detail.Tokens.TotalTokens
				detailReqs++
				detailTokens += detail.Tokens.TotalTokens
				if detail.Failed {
					detailFailure++
				} else {
					detailSuccess++
				}
				bucketTimestamp := timestamp.In(bucketLocation)
				dayKey := bucketTimestamp.Format("2006-01-02")
				hourKey := bucketTimestamp.Format("15")
				detailReqsByDay[dayKey]++
				result.RequestsByHour[hourKey]++
				detailTokensByDay[dayKey] += detail.Tokens.TotalTokens
				result.TokensByHour[hourKey] += detail.Tokens.TotalTokens
			}
			if modelCopy.TotalRequests == 0 {
				continue
			}
			apiCopy.Models[modelName] = modelCopy
		}
		if apiCopy.TotalRequests == 0 {
			continue
		}
		result.APIs[apiName] = apiCopy
	}

	// Merge: day-bucket maps are canonical when present; detail-derived
	// values fill in days the snapshot lacked.
	for k, v := range detailReqsByDay {
		if _, ok := result.RequestsByDay[k]; !ok {
			result.RequestsByDay[k] = v
		}
	}
	for k, v := range detailTokensByDay {
		if _, ok := result.TokensByDay[k]; !ok {
			result.TokensByDay[k] = v
		}
	}

	// Totals: take the max of day-bucket-derived and detail-derived.
	if dayBucketRequests > detailReqs {
		result.TotalRequests = dayBucketRequests
	} else {
		result.TotalRequests = detailReqs
	}
	if dayBucketTokens > detailTokens {
		result.TotalTokens = dayBucketTokens
	} else {
		result.TotalTokens = detailTokens
	}
	result.SuccessCount = detailSuccess
	result.FailureCount = detailFailure

	return result
}

// transformUsageSnapshotWithDisplayNames replaces AuthIndex with formatted display names.
func (h *Handler) transformUsageSnapshotWithDisplayNames(snapshot *usage.StatisticsSnapshot, displayMap map[string]string) *usage.StatisticsSnapshot {
	if snapshot == nil {
		return nil
	}

	// Deep copy the snapshot to avoid modifying the original
	result := &usage.StatisticsSnapshot{
		TotalRequests:  snapshot.TotalRequests,
		SuccessCount:   snapshot.SuccessCount,
		FailureCount:   snapshot.FailureCount,
		TotalTokens:    snapshot.TotalTokens,
		RequestsByDay:  snapshot.RequestsByDay,
		RequestsByHour: snapshot.RequestsByHour,
		TokensByDay:    snapshot.TokensByDay,
		TokensByHour:   snapshot.TokensByHour,
		APIs:           make(map[string]usage.APISnapshot, len(snapshot.APIs)),
	}

	for apiKey, apiSnap := range snapshot.APIs {
		apiCopy := usage.APISnapshot{
			TotalRequests: apiSnap.TotalRequests,
			TotalTokens:   apiSnap.TotalTokens,
			Models:        make(map[string]usage.ModelSnapshot, len(apiSnap.Models)),
		}
		for modelName, modelSnap := range apiSnap.Models {
			modelCopy := usage.ModelSnapshot{
				TotalRequests: modelSnap.TotalRequests,
				TotalTokens:   modelSnap.TotalTokens,
				Details:       make([]usage.RequestDetail, len(modelSnap.Details)),
			}
			for i, detail := range modelSnap.Details {
				detailCopy := detail
				// Replace AuthIndex with formatted display name
				if detailCopy.AuthIndex != "" {
					if displayName, ok := displayMap[detailCopy.AuthIndex]; ok {
						detailCopy.AuthIndex = displayName
					}
				}
				modelCopy.Details[i] = detailCopy
			}
			apiCopy.Models[modelName] = modelCopy
		}
		result.APIs[apiKey] = apiCopy
	}

	return result
}

// ExportUsageStatistics returns a complete usage snapshot for backup/migration.
func (h *Handler) ExportUsageStatistics(c *gin.Context) {
	var snapshot usage.StatisticsSnapshot
	if h != nil && h.usageStats != nil {
		if _, err := usage.RestoreStatisticsIfEmpty(h.usageStats); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		snapshot = h.usageStats.Snapshot()
	}
	c.JSON(http.StatusOK, usageExportPayload{
		Version:    1,
		ExportedAt: time.Now().UTC(),
		Usage:      snapshot,
	})
}

// ImportUsageStatistics merges a previously exported usage snapshot into memory.
func (h *Handler) ImportUsageStatistics(c *gin.Context) {
	if h == nil || h.usageStats == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "usage statistics unavailable"})
		return
	}

	data, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read request body"})
		return
	}

	var payload usageImportPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
		return
	}
	if payload.Version != 0 && payload.Version != 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported version"})
		return
	}

	result := h.usageStats.MergeSnapshot(payload.Usage)
	snapshot := h.usageStats.Snapshot()
	response := gin.H{
		"added":           result.Added,
		"skipped":         result.Skipped,
		"total_requests":  snapshot.TotalRequests,
		"failed_requests": snapshot.FailureCount,
	}
	if usage.GetPersistentPlugin() != nil {
		if err := usage.SaveStatistics(); err != nil {
			response["persisted"] = false
			response["persist_error"] = err.Error()
		} else {
			response["persisted"] = true
		}
	}
	c.JSON(http.StatusOK, response)
}

// dashboardViewResponse wraps DashboardSnapshot so the JSON shape stays
// stable across schema additions and so future fields can ride alongside
// without touching the frontend consumer.
type dashboardViewResponse struct {
	Dashboard   usage.DashboardSnapshot `json:"dashboard"`
	GeneratedAt time.Time               `json:"generated_at"`
}

// GetUsageDashboard returns the lightweight dashboard view that the
// management home page renders. Compared to the full /usage endpoint it
// returns only the aggregates, a fixed number of flow buckets, a top-N
// model slice, and the most recent request events, which lets the dashboard
// hydrate with a small payload even on busy servers.
//
// The handler enforces a 10s context timeout and surfaces a 503 with
// dashboard_build_cancelled if the snapshot builder aborts via ctx.
func (h *Handler) GetUsageDashboard(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	cfg := usage.DefaultDashboardConfig()
	switch strings.ToLower(strings.TrimSpace(c.Query("window"))) {
	case "1h":
		cfg.Window = time.Hour
	case "7h":
		cfg.Window = 7 * time.Hour
	case "24h":
		cfg.Window = 24 * time.Hour
	case "7d":
		cfg.Window = 7 * 24 * time.Hour
	case "all", "":
		cfg.Window = 0
	default:
		cfg.Window = 24 * time.Hour
	}
	if v := parsePositiveInt(c.Query("bucket_count"), 0); v > 0 && v <= 96 {
		cfg.BucketCount = v
	}
	if v := parsePositiveInt(c.Query("model_top"), 0); v > 0 && v <= 50 {
		cfg.ModelTopN = v
	}
	if v := parsePositiveInt(c.Query("latest_count"), 0); v > 0 && v <= 50 {
		cfg.LatestCount = v
	}

	var snap usage.DashboardSnapshot
	var cheapTotals usage.StatisticsSnapshot
	if h != nil && h.usageStats != nil {
		if _, err := usage.RestoreStatisticsIfEmpty(h.usageStats); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		cheapTotals = h.usageStats.Snapshot()
		if match := strings.TrimSpace(c.GetHeader("If-None-Match")); match != "" {
			candidate := `"` + dashboardComputeETag(dashboardViewResponse{
				Dashboard: usage.DashboardSnapshot{
					TotalRequests: cheapTotals.TotalRequests,
					TotalTokens:   cheapTotals.TotalTokens,
					SuccessCount:  cheapTotals.SuccessCount,
					FailureCount:  cheapTotals.FailureCount,
					BucketCount:   cfg.BucketCount,
					BucketSizeMs:  cfg.BucketSize.Milliseconds(),
				},
				GeneratedAt: time.Now().UTC(),
			}) + `"`
			if match == candidate {
				c.Header("ETag", candidate)
				c.Status(http.StatusNotModified)
				return
			}
		}
		snap = h.usageStats.BuildDashboardSnapshot(ctx, cfg)
	}

	if ctx.Err() != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error":  "dashboard_build_cancelled",
			"reason": ctx.Err().Error(),
			"window": cfg.Window.String(),
		})
		return
	}

	if h != nil {
		displayMap := h.authIndexDisplayMap()
		if len(displayMap) > 0 {
			for i := range snap.LatestRequests {
				req := &snap.LatestRequests[i]
				if req.APIKey == "" {
					continue
				}
				if displayName, ok := displayMap[req.APIKey]; ok {
					req.APIKey = displayName
				}
			}
		}
	}

	resp := dashboardViewResponse{
		Dashboard:   snap,
		GeneratedAt: time.Now().UTC(),
	}
	etag := dashboardComputeETag(resp)
	if etag != "" {
		c.Header("ETag", etag)
	}
	c.JSON(http.StatusOK, resp)
}

// dashboardComputeETag derives a stable ETag from the cheap counters that
// change on every request plus a coarse-grained timestamp. It deliberately
// ignores LatestEventID (which changes per ingest and would invalidate the tag
// on every single request) and the heavy fields
// (flow_buckets, model_top). The 30s-aligned timestamp means the tag stays
// stable for 30s while the cheap counters hold, giving the client a usable
// 304 window without forcing it to refetch the full payload on every poll.
// The value is short (sha1 hex, 40 chars) and safe to use as a header value.
func dashboardComputeETag(resp dashboardViewResponse) string {
	h := sha1.New()
	const alignSeconds = int64(30)
	aligned := resp.GeneratedAt.UTC().Unix() / alignSeconds * alignSeconds
	fmt.Fprintf(h, "v1|req=%d|tok=%d|succ=%d|fail=%d|bucket=%d|size=%d|aligned=%d",
		resp.Dashboard.TotalRequests,
		resp.Dashboard.TotalTokens,
		resp.Dashboard.SuccessCount,
		resp.Dashboard.FailureCount,
		resp.Dashboard.BucketCount,
		resp.Dashboard.BucketSizeMs,
		aligned,
	)
	return `"` + hex.EncodeToString(h.Sum(nil)) + `"`
}

func parsePositiveInt(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return fallback
	}
	return v
}
