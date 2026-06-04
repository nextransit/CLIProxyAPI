package management

import (
	"encoding/json"
	"net/http"
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
				result.TotalRequests++
				result.TotalTokens += detail.Tokens.TotalTokens
				if detail.Failed {
					result.FailureCount++
				} else {
					result.SuccessCount++
				}
				bucketTimestamp := timestamp.In(bucketLocation)
				dayKey := bucketTimestamp.Format("2006-01-02")
				hourKey := bucketTimestamp.Format("15")
				result.RequestsByDay[dayKey]++
				result.RequestsByHour[hourKey]++
				result.TokensByDay[dayKey] += detail.Tokens.TotalTokens
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
