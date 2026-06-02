package management

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

const (
	aiOpsDefaultRange              = "24h"
	aiOpsDefaultAIModel            = "MiniMax-M2.7-highspeed"
	aiOpsDefaultAIMaxTokens        = 600
	aiOpsMaxAIMaxTokens            = 4096
	aiOpsDefaultTopModelLimit      = 20
	aiOpsDefaultTopFailureLimit    = 10
	aiOpsDefaultFailureSampleLimit = 20
)

type aiOpsQueryRequest struct {
	Query     string `json:"query"`
	TimeRange string `json:"time_range"`
	Range     string `json:"range"`
	Model     string `json:"model"`
	Source    string `json:"source"`

	IncludeAI *bool `json:"include_ai"`

	AIAPIKey    string `json:"ai_api_key"`
	AIBaseURL   string `json:"ai_base_url"`
	AIModel     string `json:"ai_model"`
	AIMaxTokens int    `json:"ai_max_tokens"`

	AI aiOpsAIRequest `json:"ai"`
}

type aiOpsAIRequest struct {
	Enabled   bool   `json:"enabled"`
	APIKey    string `json:"api_key"`
	BaseURL   string `json:"base_url"`
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens"`
}

type aiOpsWindow struct {
	Range   string    `json:"range"`
	StartAt time.Time `json:"start_at,omitempty"`
	EndAt   time.Time `json:"end_at"`
}

type aiOpsFilters struct {
	Model  string `json:"model,omitempty"`
	Source string `json:"source,omitempty"`
}

type aiOpsOverview struct {
	TotalRequests   int64   `json:"total_requests"`
	SuccessRequests int64   `json:"success_requests"`
	FailedRequests  int64   `json:"failed_requests"`
	SuccessRate     float64 `json:"success_rate"`

	TotalTokens     int64   `json:"total_tokens"`
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	ReasoningTokens int64   `json:"reasoning_tokens"`
	CachedTokens    int64   `json:"cached_tokens"`
	CacheHitRate    float64 `json:"cache_hit_rate"`
}

type aiOpsModelMetric struct {
	Model string `json:"model"`

	TotalRequests   int64   `json:"total_requests"`
	SuccessRequests int64   `json:"success_requests"`
	FailedRequests  int64   `json:"failed_requests"`
	SuccessRate     float64 `json:"success_rate"`

	TotalTokens     int64   `json:"total_tokens"`
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	ReasoningTokens int64   `json:"reasoning_tokens"`
	CachedTokens    int64   `json:"cached_tokens"`
	CacheHitRate    float64 `json:"cache_hit_rate"`
}

type aiOpsNamedCount struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

type aiOpsFailureSample struct {
	Timestamp time.Time `json:"timestamp"`
	Model     string    `json:"model"`
	Source    string    `json:"source"`
	AuthIndex string    `json:"auth_index,omitempty"`
}

type aiOpsNetworkSummary struct {
	SuccessRate        float64              `json:"success_rate"`
	FailedBySource     []aiOpsNamedCount    `json:"failed_by_source"`
	FailedByModel      []aiOpsNamedCount    `json:"failed_by_model"`
	FailureSamples     []aiOpsFailureSample `json:"failure_samples"`
	WindowRequestCount int64                `json:"window_request_count"`
}

type aiOpsDailyModelMetric struct {
	Date          string  `json:"date"`
	Model         string  `json:"model_name"`
	TotalRequests int64   `json:"total_requests"`
	SuccessRate   float64 `json:"success_rate"`
	TotalTokens   int64   `json:"total_tokens"`
	InputTokens   int64   `json:"input_tokens"`
	CachedTokens  int64   `json:"cached_tokens"`
	CacheHitRate  float64 `json:"cache_hit_rate"`
}

type aiOpsDailyMetric struct {
	Date          string  `json:"date"`
	TotalRequests int64   `json:"total_requests"`
	SuccessRate   float64 `json:"success_rate"`
	TotalTokens   int64   `json:"total_tokens"`
	InputTokens   int64   `json:"input_tokens"`
	CachedTokens  int64   `json:"cached_tokens"`
	CacheHitRate  float64 `json:"cache_hit_rate"`
}

type aiOpsRegistrationRecord struct {
	ID            string    `json:"id"`
	AuthIndex     string    `json:"auth_index,omitempty"`
	Provider      string    `json:"provider,omitempty"`
	Label         string    `json:"label,omitempty"`
	CreatedAt     time.Time `json:"created_at,omitempty"`
	UpdatedAt     time.Time `json:"updated_at,omitempty"`
	Status        string    `json:"status,omitempty"`
	StatusMessage string    `json:"status_message,omitempty"`
	Unavailable   bool      `json:"unavailable"`
	Disabled      bool      `json:"disabled"`
	ErrorCode     string    `json:"error_code,omitempty"`
	ErrorMessage  string    `json:"error_message,omitempty"`
}

type aiOpsRegistrationSummary struct {
	RegistrationsLast24h        int64                     `json:"registrations_last_24h"`
	AbnormalRegistrations24h    int64                     `json:"abnormal_registrations_24h"`
	RecentAbnormalRegistrations []aiOpsRegistrationRecord `json:"recent_abnormal_registrations"`
}

type aiOpsAIResult struct {
	Enabled   bool   `json:"enabled"`
	Status    string `json:"status"`
	Model     string `json:"model,omitempty"`
	BaseURL   string `json:"base_url,omitempty"`
	MaxTokens int    `json:"max_tokens,omitempty"`
	Analysis  string `json:"analysis,omitempty"`
	Error     string `json:"error,omitempty"`
}

type aiOpsQueryResponse struct {
	GeneratedAt   time.Time                `json:"generated_at"`
	Query         string                   `json:"query,omitempty"`
	Window        aiOpsWindow              `json:"window"`
	Filters       aiOpsFilters             `json:"filters"`
	Overview      aiOpsOverview            `json:"overview"`
	Models        []aiOpsModelMetric       `json:"models"`
	TokensByDay   map[string]int64         `json:"tokens_by_day"`
	InputByDay    map[string]int64         `json:"input_tokens_by_day"`
	CachedByDay   map[string]int64         `json:"cached_tokens_by_day"`
	DailyMetrics  []aiOpsDailyMetric       `json:"daily_metrics"`
	DailyByModel  []aiOpsDailyModelMetric  `json:"daily_model_series"`
	Network       aiOpsNetworkSummary      `json:"network"`
	Registrations aiOpsRegistrationSummary `json:"registrations"`
	AI            *aiOpsAIResult           `json:"ai,omitempty"`
}

type aiOpsRangeSpec struct {
	RangeKey string
	StartAt  time.Time
	EndAt    time.Time
}

type aiOpsMutableMetric struct {
	Requests  int64
	Success   int64
	Failed    int64
	Total     int64
	Input     int64
	Output    int64
	Reasoning int64
	Cached    int64
}

type aiOpsAICallRequest struct {
	Model           string                      `json:"model"`
	Messages        []aiOpsAICallRequestMessage `json:"messages"`
	MaxTokens       int                         `json:"max_tokens,omitempty"`
	ReasoningEffort string                      `json:"reasoning_effort,omitempty"`
}

type aiOpsAICallRequestMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type aiOpsAICallResponse struct {
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content any `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

type aiOpsAIResolvedConfig struct {
	BaseURL   string
	Model     string
	MaxTokens int
	APIKey    string
	Enabled   bool
}

// QueryAIOps returns an AI Ops snapshot for the selected time window and optional model/source filters.
// Optional AI analysis can be enabled with ai.enabled=true and a valid ai_api_key.
func (h *Handler) QueryAIOps(c *gin.Context) {
	var req aiOpsQueryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	rangeSpec, err := parseAIOpsRange(&req, time.Now().UTC())
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var snapshot usage.StatisticsSnapshot
	if h != nil && h.usageStats != nil {
		if _, errRestore := usage.RestoreStatisticsIfEmpty(h.usageStats); errRestore != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": errRestore.Error()})
			return
		}
		snapshot = h.usageStats.Snapshot()
	}

	response := h.buildAIOpsQueryResponse(snapshot, req, rangeSpec)
	aiCfg := resolveAIOpsAIConfig(req, h)
	if aiCfg.Enabled {
		response.AI = &aiOpsAIResult{
			Enabled:   true,
			Status:    "error",
			Model:     aiCfg.Model,
			BaseURL:   aiCfg.BaseURL,
			MaxTokens: aiCfg.MaxTokens,
		}
		analysis, errAnalyze := h.runAIOpsAnalysis(c.Request.Context(), aiCfg, response)
		if errAnalyze != nil {
			response.AI.Error = errAnalyze.Error()
		} else {
			response.AI.Status = "success"
			response.AI.Analysis = analysis
		}
	}

	c.JSON(http.StatusOK, response)
}

func parseAIOpsRange(req *aiOpsQueryRequest, now time.Time) (aiOpsRangeSpec, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}

	raw := strings.TrimSpace(req.TimeRange)
	if raw == "" {
		raw = strings.TrimSpace(req.Range)
	}
	if raw == "" {
		raw = aiOpsDefaultRange
	}
	key := strings.ToLower(raw)
	spec := aiOpsRangeSpec{RangeKey: key, EndAt: now.UTC()}
	switch key {
	case "all":
		return spec, nil
	case "24h":
		spec.StartAt = now.Add(-24 * time.Hour).UTC()
		return spec, nil
	case "7d":
		spec.StartAt = now.Add(-7 * 24 * time.Hour).UTC()
		return spec, nil
	case "30d":
		spec.StartAt = now.Add(-30 * 24 * time.Hour).UTC()
		return spec, nil
	case "365d", "1y":
		spec.RangeKey = "365d"
		spec.StartAt = now.Add(-365 * 24 * time.Hour).UTC()
		return spec, nil
	default:
		duration, err := time.ParseDuration(key)
		if err != nil || duration <= 0 {
			return aiOpsRangeSpec{}, fmt.Errorf("unsupported time_range: %s", raw)
		}
		spec.RangeKey = key
		spec.StartAt = now.Add(-duration).UTC()
		return spec, nil
	}
}

func (h *Handler) buildAIOpsQueryResponse(
	snapshot usage.StatisticsSnapshot,
	req aiOpsQueryRequest,
	rangeSpec aiOpsRangeSpec,
) aiOpsQueryResponse {
	modelFilter := strings.TrimSpace(req.Model)
	sourceFilter := strings.TrimSpace(req.Source)

	response := aiOpsQueryResponse{
		GeneratedAt: time.Now().UTC(),
		Query:       strings.TrimSpace(req.Query),
		Window: aiOpsWindow{
			Range:   rangeSpec.RangeKey,
			StartAt: rangeSpec.StartAt,
			EndAt:   rangeSpec.EndAt,
		},
		Filters: aiOpsFilters{
			Model:  modelFilter,
			Source: sourceFilter,
		},
		TokensByDay: make(map[string]int64),
		InputByDay:  make(map[string]int64),
		CachedByDay: make(map[string]int64),
	}

	modelStats := make(map[string]*aiOpsMutableMetric)
	dailyStats := make(map[string]*aiOpsMutableMetric)
	dailyModelStats := make(map[string]map[string]*aiOpsMutableMetric)
	failedBySource := make(map[string]int64)
	failedByModel := make(map[string]int64)
	failureSamples := make([]aiOpsFailureSample, 0)

	overview := aiOpsMutableMetric{}
	for _, apiSnap := range snapshot.APIs {
		for modelName, modelSnap := range apiSnap.Models {
			if !matchesAIOpsModelFilter(modelName, modelFilter) {
				continue
			}
			for _, detail := range modelSnap.Details {
				timestamp := detail.Timestamp.UTC()
				if timestamp.IsZero() {
					continue
				}
				if !rangeSpec.StartAt.IsZero() {
					if timestamp.Before(rangeSpec.StartAt) || timestamp.After(rangeSpec.EndAt) {
						continue
					}
				}
				if sourceFilter != "" && !strings.EqualFold(strings.TrimSpace(detail.Source), sourceFilter) {
					continue
				}

				tokens := normaliseAIOpsTokens(detail.Tokens)
				modelMetric := modelStats[modelName]
				if modelMetric == nil {
					modelMetric = &aiOpsMutableMetric{}
					modelStats[modelName] = modelMetric
				}

				recordSuccess := !detail.Failed
				updateAIOpsMetric(&overview, tokens, recordSuccess)
				updateAIOpsMetric(modelMetric, tokens, recordSuccess)

				dayKey := timestamp.Format("2006-01-02")
				response.TokensByDay[dayKey] += tokens.TotalTokens
				response.InputByDay[dayKey] += tokens.InputTokens
				response.CachedByDay[dayKey] += tokens.CachedTokens
				dailyMetric := dailyStats[dayKey]
				if dailyMetric == nil {
					dailyMetric = &aiOpsMutableMetric{}
					dailyStats[dayKey] = dailyMetric
				}
				updateAIOpsMetric(dailyMetric, tokens, recordSuccess)
				modelDailyStats := dailyModelStats[modelName]
				if modelDailyStats == nil {
					modelDailyStats = make(map[string]*aiOpsMutableMetric)
					dailyModelStats[modelName] = modelDailyStats
				}
				modelDailyMetric := modelDailyStats[dayKey]
				if modelDailyMetric == nil {
					modelDailyMetric = &aiOpsMutableMetric{}
					modelDailyStats[dayKey] = modelDailyMetric
				}
				updateAIOpsMetric(modelDailyMetric, tokens, recordSuccess)

				if detail.Failed {
					sourceKey := strings.TrimSpace(detail.Source)
					if sourceKey == "" {
						sourceKey = "unknown"
					}
					failedBySource[sourceKey]++
					failedByModel[modelName]++
					failureSamples = append(failureSamples, aiOpsFailureSample{
						Timestamp: timestamp,
						Model:     modelName,
						Source:    sourceKey,
						AuthIndex: strings.TrimSpace(detail.AuthIndex),
					})
				}
			}
		}
	}

	response.Overview = aiOpsOverview{
		TotalRequests:   overview.Requests,
		SuccessRequests: overview.Success,
		FailedRequests:  overview.Failed,
		SuccessRate:     roundAIOpsRate(ratioPercent(overview.Success, overview.Requests)),
		TotalTokens:     overview.Total,
		InputTokens:     overview.Input,
		OutputTokens:    overview.Output,
		ReasoningTokens: overview.Reasoning,
		CachedTokens:    overview.Cached,
		CacheHitRate:    roundAIOpsRate(ratioPercent(overview.Cached, overview.Input+overview.Cached)),
	}

	modelMetrics := make([]aiOpsModelMetric, 0, len(modelStats))
	for modelName, metric := range modelStats {
		modelMetrics = append(modelMetrics, aiOpsModelMetric{
			Model:           modelName,
			TotalRequests:   metric.Requests,
			SuccessRequests: metric.Success,
			FailedRequests:  metric.Failed,
			SuccessRate:     roundAIOpsRate(ratioPercent(metric.Success, metric.Requests)),
			TotalTokens:     metric.Total,
			InputTokens:     metric.Input,
			OutputTokens:    metric.Output,
			ReasoningTokens: metric.Reasoning,
			CachedTokens:    metric.Cached,
			CacheHitRate:    roundAIOpsRate(ratioPercent(metric.Cached, metric.Input+metric.Cached)),
		})
	}
	sort.Slice(modelMetrics, func(i, j int) bool {
		if modelMetrics[i].TotalTokens == modelMetrics[j].TotalTokens {
			return modelMetrics[i].Model < modelMetrics[j].Model
		}
		return modelMetrics[i].TotalTokens > modelMetrics[j].TotalTokens
	})
	if len(modelMetrics) > aiOpsDefaultTopModelLimit {
		modelMetrics = modelMetrics[:aiOpsDefaultTopModelLimit]
	}
	response.Models = modelMetrics
	response.DailyMetrics = flattenAIOpsDailyStats(dailyStats)
	response.DailyByModel = flattenAIOpsDailyModelStats(dailyModelStats)

	sort.Slice(failureSamples, func(i, j int) bool {
		return failureSamples[i].Timestamp.After(failureSamples[j].Timestamp)
	})
	if len(failureSamples) > aiOpsDefaultFailureSampleLimit {
		failureSamples = failureSamples[:aiOpsDefaultFailureSampleLimit]
	}
	response.Network = aiOpsNetworkSummary{
		SuccessRate:        roundAIOpsRate(ratioPercent(overview.Success, overview.Requests)),
		FailedBySource:     topAIOpsCounts(failedBySource, aiOpsDefaultTopFailureLimit),
		FailedByModel:      topAIOpsCounts(failedByModel, aiOpsDefaultTopFailureLimit),
		FailureSamples:     failureSamples,
		WindowRequestCount: overview.Requests,
	}

	response.Registrations = h.buildAIOpsRegistrationSummary(rangeSpec.EndAt)
	return response
}

func matchesAIOpsModelFilter(modelName string, modelFilter string) bool {
	filter := strings.TrimSpace(modelFilter)
	if filter == "" {
		return true
	}
	model := strings.TrimSpace(modelName)
	if strings.EqualFold(model, filter) {
		return true
	}
	canonicalModel := canonicalTextOpsModelName(model)
	canonicalFilter := canonicalTextOpsModelName(filter)
	if canonicalModel != "" && canonicalModel == canonicalFilter {
		return true
	}
	modelLower := strings.ToLower(model)
	filterLower := strings.ToLower(filter)
	if strings.Contains(modelLower, filterLower) {
		return true
	}
	return canonicalModel != "" && canonicalFilter != "" && strings.Contains(canonicalModel, canonicalFilter)
}

func flattenAIOpsDailyStats(stats map[string]*aiOpsMutableMetric) []aiOpsDailyMetric {
	if len(stats) == 0 {
		return []aiOpsDailyMetric{}
	}
	result := make([]aiOpsDailyMetric, 0, len(stats))
	for day, metric := range stats {
		if metric == nil {
			continue
		}
		result = append(result, aiOpsDailyMetric{
			Date:          day,
			TotalRequests: metric.Requests,
			SuccessRate:   roundAIOpsRate(ratioPercent(metric.Success, metric.Requests)),
			TotalTokens:   metric.Total,
			InputTokens:   metric.Input,
			CachedTokens:  metric.Cached,
			CacheHitRate:  roundAIOpsRate(ratioPercent(metric.Cached, metric.Input+metric.Cached)),
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Date < result[j].Date
	})
	return result
}

func flattenAIOpsDailyModelStats(stats map[string]map[string]*aiOpsMutableMetric) []aiOpsDailyModelMetric {
	if len(stats) == 0 {
		return []aiOpsDailyModelMetric{}
	}
	result := make([]aiOpsDailyModelMetric, 0)
	for modelName, byDay := range stats {
		for day, metric := range byDay {
			if metric == nil {
				continue
			}
			result = append(result, aiOpsDailyModelMetric{
				Date:          day,
				Model:         modelName,
				TotalRequests: metric.Requests,
				SuccessRate:   roundAIOpsRate(ratioPercent(metric.Success, metric.Requests)),
				TotalTokens:   metric.Total,
				InputTokens:   metric.Input,
				CachedTokens:  metric.Cached,
				CacheHitRate:  roundAIOpsRate(ratioPercent(metric.Cached, metric.Input+metric.Cached)),
			})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Date == result[j].Date {
			return result[i].Model < result[j].Model
		}
		return result[i].Date < result[j].Date
	})
	return result
}

func updateAIOpsMetric(metric *aiOpsMutableMetric, tokens usage.TokenStats, success bool) {
	if metric == nil {
		return
	}
	metric.Requests++
	if success {
		metric.Success++
	} else {
		metric.Failed++
	}
	metric.Total += tokens.TotalTokens
	metric.Input += tokens.InputTokens
	metric.Output += tokens.OutputTokens
	metric.Reasoning += tokens.ReasoningTokens
	metric.Cached += tokens.CachedTokens
}

func normaliseAIOpsTokens(tokens usage.TokenStats) usage.TokenStats {
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = tokens.InputTokens + tokens.OutputTokens + tokens.ReasoningTokens + tokens.CachedTokens
	}
	if tokens.TotalTokens < 0 {
		tokens.TotalTokens = 0
	}
	if tokens.InputTokens < 0 {
		tokens.InputTokens = 0
	}
	if tokens.OutputTokens < 0 {
		tokens.OutputTokens = 0
	}
	if tokens.ReasoningTokens < 0 {
		tokens.ReasoningTokens = 0
	}
	if tokens.CachedTokens < 0 {
		tokens.CachedTokens = 0
	}
	return tokens
}

func ratioPercent(numerator, denominator int64) float64 {
	if denominator <= 0 || numerator <= 0 {
		return 0
	}
	return float64(numerator) * 100.0 / float64(denominator)
}

func roundAIOpsRate(value float64) float64 {
	if value == 0 {
		return 0
	}
	return float64(int(value*100+0.5)) / 100
}

func topAIOpsCounts(counts map[string]int64, limit int) []aiOpsNamedCount {
	if len(counts) == 0 || limit <= 0 {
		return []aiOpsNamedCount{}
	}
	rows := make([]aiOpsNamedCount, 0, len(counts))
	for name, count := range counts {
		if count <= 0 {
			continue
		}
		rows = append(rows, aiOpsNamedCount{Name: name, Count: count})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Count == rows[j].Count {
			return rows[i].Name < rows[j].Name
		}
		return rows[i].Count > rows[j].Count
	})
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

func (h *Handler) buildAIOpsRegistrationSummary(now time.Time) aiOpsRegistrationSummary {
	summary := aiOpsRegistrationSummary{
		RecentAbnormalRegistrations: make([]aiOpsRegistrationRecord, 0),
	}
	if h == nil || h.authManager == nil {
		return summary
	}

	windowStart := now.Add(-24 * time.Hour)
	auths := h.authManager.List()
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		created := auth.CreatedAt.UTC()
		isRecent := !created.IsZero() && !created.Before(windowStart) && !created.After(now)
		if isRecent {
			summary.RegistrationsLast24h++
		}

		abnormal := auth.Disabled || auth.Unavailable || strings.TrimSpace(auth.StatusMessage) != "" || auth.LastError != nil
		if !isRecent || !abnormal {
			continue
		}

		summary.AbnormalRegistrations24h++
		auth.EnsureIndex()
		record := aiOpsRegistrationRecord{
			ID:            strings.TrimSpace(auth.ID),
			AuthIndex:     strings.TrimSpace(auth.Index),
			Provider:      strings.TrimSpace(auth.Provider),
			Label:         strings.TrimSpace(auth.Label),
			CreatedAt:     created,
			UpdatedAt:     auth.UpdatedAt.UTC(),
			Status:        string(auth.Status),
			StatusMessage: strings.TrimSpace(auth.StatusMessage),
			Unavailable:   auth.Unavailable,
			Disabled:      auth.Disabled,
		}
		if auth.LastError != nil {
			record.ErrorCode = strings.TrimSpace(auth.LastError.Code)
			record.ErrorMessage = strings.TrimSpace(auth.LastError.Message)
		}
		summary.RecentAbnormalRegistrations = append(summary.RecentAbnormalRegistrations, record)
	}

	sort.Slice(summary.RecentAbnormalRegistrations, func(i, j int) bool {
		return summary.RecentAbnormalRegistrations[i].CreatedAt.After(summary.RecentAbnormalRegistrations[j].CreatedAt)
	})
	if len(summary.RecentAbnormalRegistrations) > aiOpsDefaultFailureSampleLimit {
		summary.RecentAbnormalRegistrations = summary.RecentAbnormalRegistrations[:aiOpsDefaultFailureSampleLimit]
	}

	return summary
}

func resolveAIOpsAIConfig(req aiOpsQueryRequest, h *Handler) aiOpsAIResolvedConfig {
	enabled := req.AI.Enabled
	if req.IncludeAI != nil {
		enabled = enabled || *req.IncludeAI
	}

	apiKey := strings.TrimSpace(req.AI.APIKey)
	if apiKey == "" {
		apiKey = strings.TrimSpace(req.AIAPIKey)
	}

	model := strings.TrimSpace(req.AI.Model)
	if model == "" {
		model = strings.TrimSpace(req.AIModel)
	}
	if model == "" {
		model = aiOpsDefaultAIModel
	}

	maxTokens := req.AI.MaxTokens
	if maxTokens <= 0 {
		maxTokens = req.AIMaxTokens
	}
	if maxTokens <= 0 {
		maxTokens = aiOpsDefaultAIMaxTokens
	}
	if maxTokens > aiOpsMaxAIMaxTokens {
		maxTokens = aiOpsMaxAIMaxTokens
	}

	baseURL := strings.TrimSpace(req.AI.BaseURL)
	if baseURL == "" {
		baseURL = strings.TrimSpace(req.AIBaseURL)
	}
	baseURL = normalizeAIOpsAIBaseURL(baseURL, h)

	if !enabled || apiKey == "" || baseURL == "" {
		return aiOpsAIResolvedConfig{Enabled: false}
	}
	return aiOpsAIResolvedConfig{
		Enabled:   true,
		APIKey:    apiKey,
		BaseURL:   baseURL,
		Model:     model,
		MaxTokens: maxTokens,
	}
}

func normalizeAIOpsAIBaseURL(raw string, h *Handler) string {
	base := strings.TrimSpace(raw)
	if base == "" && h != nil && h.cfg != nil && h.cfg.Port > 0 {
		scheme := "http"
		if h.cfg.TLS.Enable {
			scheme = "https"
		}
		base = fmt.Sprintf("%s://127.0.0.1:%d/v1", scheme, h.cfg.Port)
	}
	if base == "" {
		return ""
	}
	if !strings.HasPrefix(strings.ToLower(base), "http://") && !strings.HasPrefix(strings.ToLower(base), "https://") {
		base = "http://" + base
	}
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(strings.ToLower(base), "/v1") {
		return base
	}
	return base + "/v1"
}

func (h *Handler) runAIOpsAnalysis(ctx context.Context, aiCfg aiOpsAIResolvedConfig, result aiOpsQueryResponse) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	analysisPayload := map[string]any{
		"generated_at":  result.GeneratedAt,
		"query":         result.Query,
		"window":        result.Window,
		"filters":       result.Filters,
		"overview":      result.Overview,
		"models":        result.Models,
		"network":       result.Network,
		"registrations": result.Registrations,
	}
	summaryJSON, errMarshal := json.MarshalIndent(analysisPayload, "", "  ")
	if errMarshal != nil {
		return "", fmt.Errorf("failed to build analysis payload: %w", errMarshal)
	}

	queryText := strings.TrimSpace(result.Query)
	if queryText == "" {
		queryText = "请根据运维数据给出风险评估和可执行优化建议。"
	}
	systemPrompt := "You are an AI Ops assistant for CLIProxyAPI. Respond in Chinese with three sections: 1) 结论 2) 风险点 3) 可执行动作. Do not include <think>, reasoning, analysis, or chain-of-thought."
	userPrompt := fmt.Sprintf("用户问题：%s\n\n以下是平台数据（JSON）：\n%s", queryText, string(summaryJSON))
	body := aiOpsAICallRequest{
		Model: aiCfg.Model,
		Messages: []aiOpsAICallRequestMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		MaxTokens:       aiCfg.MaxTokens,
		ReasoningEffort: resolveManagementLLMReasoningEffort(aiCfg.Model),
	}

	data, errMarshalBody := json.Marshal(body)
	if errMarshalBody != nil {
		return "", fmt.Errorf("failed to encode AI request: %w", errMarshalBody)
	}

	endpoint := strings.TrimRight(aiCfg.BaseURL, "/") + "/chat/completions"
	req, errNewRequest := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if errNewRequest != nil {
		return "", fmt.Errorf("failed to build AI request: %w", errNewRequest)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+aiCfg.APIKey)

	httpClient := &http.Client{
		Transport: h.apiCallTransport(nil),
	}
	resp, errDo := httpClient.Do(req)
	if errDo != nil {
		return "", fmt.Errorf("AI request failed: %w", errDo)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	respBody, errReadAll := io.ReadAll(resp.Body)
	if errReadAll != nil {
		return "", fmt.Errorf("failed to read AI response: %w", errReadAll)
	}

	var parsed aiOpsAICallResponse
	if errUnmarshal := json.Unmarshal(respBody, &parsed); errUnmarshal != nil {
		return "", fmt.Errorf("invalid AI response: %w", errUnmarshal)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		msg := strings.TrimSpace(extractAIOpsErrorMessage(parsed, string(respBody)))
		if msg == "" {
			msg = "unknown upstream error"
		}
		return "", fmt.Errorf("AI request failed: %s", msg)
	}
	if parsed.Error != nil {
		msg := strings.TrimSpace(parsed.Error.Message)
		if msg == "" {
			msg = "upstream returned error"
		}
		return "", fmt.Errorf("AI request failed: %s", msg)
	}

	content := extractAIOpsMessageContent(parsed)
	if strings.TrimSpace(content) == "" {
		return "", fmt.Errorf("AI response content is empty")
	}
	return content, nil
}

func extractAIOpsErrorMessage(resp aiOpsAICallResponse, fallback string) string {
	if resp.Error != nil {
		if strings.TrimSpace(resp.Error.Message) != "" {
			return strings.TrimSpace(resp.Error.Message)
		}
	}
	fallback = strings.TrimSpace(fallback)
	if fallback == "" {
		return ""
	}
	if len(fallback) > 500 {
		return fallback[:500]
	}
	return fallback
}

func extractAIOpsMessageContent(resp aiOpsAICallResponse) string {
	if len(resp.Choices) == 0 {
		return ""
	}
	raw := resp.Choices[0].Message.Content
	switch typed := raw.(type) {
	case string:
		return stripTextOpsThinkBlocks(typed)
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			obj, ok := item.(map[string]any)
			if !ok {
				continue
			}
			blockType, _ := obj["type"].(string)
			if strings.EqualFold(blockType, "thinking") || strings.EqualFold(blockType, "reasoning") {
				continue
			}
			text, _ := obj["text"].(string)
			if strings.TrimSpace(text) != "" {
				parts = append(parts, strings.TrimSpace(text))
			}
		}
		return stripTextOpsThinkBlocks(strings.Join(parts, "\n"))
	default:
		return ""
	}
}
