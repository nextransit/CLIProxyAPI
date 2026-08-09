// Package usage provides usage tracking and logging functionality for the CLI Proxy API server.
// It includes plugins for monitoring API usage, token consumption, and other metrics
// to help with observability and billing purposes.
package usage

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

// UsageEvent represents a single usage event pushed in real-time to SSE subscribers.
// It is published by the Broker immediately (no debounce).
type UsageEvent struct {
	ID          uint64       `json:"id"`
	APIKey      string       `json:"api_key"`
	Model       string       `json:"model"`
	Source      string       `json:"source,omitempty"`
	AuthIndex   string       `json:"auth_index,omitempty"`
	RequestID   string       `json:"request_id,omitempty"`
	Failed      bool         `json:"failed"`
	Tokens      TokenSummary `json:"tokens"`
	Thinking    *Thinking    `json:"thinking,omitempty"`
	RequestedAt time.Time    `json:"requested_at"`
	DurationMs  int64        `json:"duration_ms"`
	StatusCode  int          `json:"status_code"`
}

// TokenSummary captures the token usage breakdown for a single usage event.
type TokenSummary struct {
	Input     int64 `json:"input"`
	Output    int64 `json:"output"`
	Reasoning int64 `json:"reasoning,omitempty"`
	Cached    int64 `json:"cached,omitempty"`
	Total     int64 `json:"total"`
}

// UsagePayload is a minimal subset of the snapshot used for SSE push.
type UsagePayload struct {
	TotalRequests int64  `json:"total_requests"`
	TotalTokens   int64  `json:"total_tokens"`
	LatestID      uint64 `json:"latest_event_id"`
}

var statisticsEnabled atomic.Bool

func init() {
	statisticsEnabled.Store(true)
	coreusage.RegisterPlugin(NewLoggerPlugin())
}

// LoggerPlugin collects in-memory request statistics for usage analysis.
// It implements coreusage.Plugin to receive usage records emitted by the runtime.
type LoggerPlugin struct {
	stats *RequestStatistics
}

// NewLoggerPlugin constructs a new logger plugin instance.
//
// Returns:
//   - *LoggerPlugin: A new logger plugin instance wired to the shared statistics store.
func NewLoggerPlugin() *LoggerPlugin { return &LoggerPlugin{stats: defaultRequestStatistics} }

// HandleUsage implements coreusage.Plugin.
// It updates the in-memory statistics store whenever a usage record is received.
//
// Parameters:
//   - ctx: The context for the usage record
//   - record: The usage record to aggregate
func (p *LoggerPlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	if !statisticsEnabled.Load() {
		return
	}
	if p == nil || p.stats == nil {
		return
	}
	p.stats.Record(ctx, record)
}

// SetStatisticsEnabled toggles whether in-memory statistics are recorded.
func SetStatisticsEnabled(enabled bool) { statisticsEnabled.Store(enabled) }

// StatisticsEnabled reports the current recording state.
func StatisticsEnabled() bool { return statisticsEnabled.Load() }

// RequestStatistics maintains aggregated request metrics in memory.
type RequestStatistics struct {
	mu sync.RWMutex

	totalRequests int64
	successCount  int64
	failureCount  int64
	totalTokens   int64

	apis map[string]*apiStats

	requestsByDay  map[string]int64
	requestsByHour map[int]int64
	tokensByDay    map[string]int64
	tokensByHour   map[int]int64

	broker      *Broker
	nextEventID atomic.Uint64
	recent      RecentBuffer

	// Pre-aggregated rotating bucket rings. These are the read path's
	// primary data source: Snapshot() and BuildDashboardSnapshot() can
	// answer most queries by reading these rings (O(bucketCount)) without
	// scanning the per-model Details[] slices (O(totalRequests)).
	//
	// The 5-minute ring covers the last hour (12 buckets); the 1-hour
	// ring covers the last day (24 buckets). Together they give the
	// dashboard and /usage/summary endpoints a fast read path while
	// keeping memory bounded regardless of ingest volume.
	bucketRing5m *BucketRing
	bucketRing1h *BucketRing
}

// apiStats holds aggregated metrics for a single API key.
type apiStats struct {
	TotalRequests int64
	TotalTokens   int64
	Models        map[string]*modelStats
}

// modelStats holds aggregated metrics for a specific model within an API.
type modelStats struct {
	TotalRequests int64
	TotalTokens   int64
	Details       []RequestDetail
}

// RequestDetail stores the timestamp, latency, and token usage for a single request.
type RequestDetail struct {
	EventID    uint64     `json:"event_id,omitempty"`
	Timestamp  time.Time  `json:"timestamp"`
	LatencyMs  int64      `json:"latency_ms"`
	Source     string     `json:"source"`
	AuthIndex  string     `json:"auth_index"`
	RequestID  string     `json:"request_id,omitempty"`
	StatusCode int        `json:"status_code"`
	Thinking   *Thinking  `json:"thinking,omitempty"`
	Tokens     TokenStats `json:"tokens"`
	Failed     bool       `json:"failed"`
}

// Thinking captures normalized thinking settings for one request.
type Thinking struct {
	Intensity string `json:"intensity,omitempty"`
	Mode      string `json:"mode,omitempty"`
	Level     string `json:"level,omitempty"`
	Budget    *int64 `json:"budget,omitempty"`
}

// TokenStats captures the token usage breakdown for a request.
type TokenStats struct {
	InputTokens     int64 `json:"input_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	ReasoningTokens int64 `json:"reasoning_tokens"`
	CachedTokens    int64 `json:"cached_tokens"`
	TotalTokens     int64 `json:"total_tokens"`
}

// StatisticsSnapshot represents an immutable view of the aggregated metrics.
type StatisticsSnapshot struct {
	TotalRequests int64 `json:"total_requests"`
	SuccessCount  int64 `json:"success_count"`
	FailureCount  int64 `json:"failure_count"`
	TotalTokens   int64 `json:"total_tokens"`

	APIs map[string]APISnapshot `json:"apis"`

	RequestsByDay  map[string]int64 `json:"requests_by_day"`
	RequestsByHour map[string]int64 `json:"requests_by_hour"`
	TokensByDay    map[string]int64 `json:"tokens_by_day"`
	TokensByHour   map[string]int64 `json:"tokens_by_hour"`
}

// APISnapshot summarises metrics for a single API key.
type APISnapshot struct {
	TotalRequests int64                    `json:"total_requests"`
	TotalTokens   int64                    `json:"total_tokens"`
	Models        map[string]ModelSnapshot `json:"models"`
}

// ModelSnapshot summarises metrics for a specific model.
type ModelSnapshot struct {
	TotalRequests int64           `json:"total_requests"`
	TotalTokens   int64           `json:"total_tokens"`
	Details       []RequestDetail `json:"details"`
}

var defaultRequestStatistics = NewRequestStatistics()

// GetRequestStatistics returns the shared statistics store.
func GetRequestStatistics() *RequestStatistics { return defaultRequestStatistics }

// NewRequestStatistics constructs an empty statistics store.
func NewRequestStatistics() *RequestStatistics {
	now := time.Now()
	return &RequestStatistics{
		apis:           make(map[string]*apiStats),
		requestsByDay:  make(map[string]int64),
		requestsByHour: make(map[int]int64),
		tokensByDay:    make(map[string]int64),
		tokensByHour:   make(map[int]int64),
		broker:         NewBroker(),
		bucketRing5m:   NewBucketRing(12, 5*time.Minute, now),
		bucketRing1h:   NewBucketRing(24, time.Hour, now),
	}
}

// Broker returns the SSE publish broker for this statistics store.
// Subscribers receive debounced snapshots after each Record.
func (s *RequestStatistics) Broker() *Broker {
	if s == nil {
		return nil
	}
	return s.broker
}

// TotalRequests returns the lifetime request counter.
func (s *RequestStatistics) TotalRequests() int64 {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.totalRequests
}

// TotalTokens returns the lifetime token counter.
func (s *RequestStatistics) TotalTokens() int64 {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.totalTokens
}

// SuccessCount returns the lifetime success counter.
func (s *RequestStatistics) SuccessCount() int64 {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.successCount
}

// FailureCount returns the lifetime failure counter.
func (s *RequestStatistics) FailureCount() int64 {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.failureCount
}

// BucketRing5m returns the 5-minute pre-aggregated rotating ring, or nil if
// not initialized.
func (s *RequestStatistics) BucketRing5m() *BucketRing {
	if s == nil {
		return nil
	}
	return s.bucketRing5m
}

// BucketRing1h returns the 1-hour pre-aggregated rotating ring, or nil if
// not initialized.
func (s *RequestStatistics) BucketRing1h() *BucketRing {
	if s == nil {
		return nil
	}
	return s.bucketRing1h
}

// Record ingests a new usage record and updates the aggregates.
func (s *RequestStatistics) Record(ctx context.Context, record coreusage.Record) {
	if s == nil {
		return
	}
	if !statisticsEnabled.Load() {
		return
	}
	id := s.nextEventID.Add(1)
	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	detail := normaliseDetail(record.Detail)
	totalTokens := detail.TotalTokens
	latencyMs := normaliseLatency(record.Latency)
	requestID := strings.TrimSpace(record.RequestID)
	thinking := normaliseThinking(record.Detail.Thinking)
	statsKey := record.APIKey
	if statsKey == "" {
		statsKey = resolveAPIIdentifier(ctx, record)
	}
	failed := record.Failed
	if !failed {
		failed = !resolveSuccess(ctx)
	}
	success := !failed
	statusCode := normaliseStatusCode(record.StatusCode, failed)
	modelName := record.Model
	if modelName == "" {
		modelName = "unknown"
	}
	dayKey := timestamp.Format("2006-01-02")
	hourKey := timestamp.Hour()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.totalRequests++
	if success {
		s.successCount++
	} else {
		s.failureCount++
	}
	s.totalTokens += totalTokens

	stats, ok := s.apis[statsKey]
	if !ok {
		stats = &apiStats{Models: make(map[string]*modelStats)}
		s.apis[statsKey] = stats
	}
	s.updateAPIStats(stats, modelName, RequestDetail{
		EventID:    id,
		Timestamp:  timestamp,
		LatencyMs:  latencyMs,
		Source:     record.Source,
		AuthIndex:  record.AuthIndex,
		RequestID:  requestID,
		StatusCode: statusCode,
		Thinking:   thinking,
		Tokens:     detail,
		Failed:     failed,
	})

	s.requestsByDay[dayKey]++
	s.requestsByHour[hourKey]++
	s.tokensByDay[dayKey] += totalTokens
	s.tokensByHour[hourKey] += totalTokens

	// Update pre-aggregated rotating rings. These let the read path
	// answer dashboard / summary queries in O(bucketCount) instead of
	// O(totalRequests). We always update both rings; each ring rotates
	// its own head based on the request timestamp.
	s.bucketRing5m.Record(timestamp, modelName, record.AuthIndex, totalTokens, failed, latencyMs)
	s.bucketRing1h.Record(timestamp, modelName, record.AuthIndex, totalTokens, failed, latencyMs)

	// Publish individual UsageEvent to broker for real-time fan-out.
	// broker.Publish is non-blocking for slow consumers.
	evt := UsageEvent{
		ID:        id,
		APIKey:    statsKey,
		Model:     modelName,
		Source:    record.Source,
		AuthIndex: record.AuthIndex,
		RequestID: requestID,
		Failed:    failed,
		Tokens: TokenSummary{
			Input:     detail.InputTokens,
			Output:    detail.OutputTokens,
			Reasoning: detail.ReasoningTokens,
			Cached:    detail.CachedTokens,
			Total:     detail.TotalTokens,
		},
		RequestedAt: timestamp,
		DurationMs:  latencyMs,
		StatusCode:  statusCode,
		Thinking:    thinking,
	}
	s.recent.Push(evt)
	s.broker.Publish(evt)
}

// defaultModelDetailsCap caps the per-model Details slice so memory does
// not grow linearly with lifetime ingest. When the cap is reached, the
// oldest detail is dropped FIFO. The cap is conservative: on a busy
// server with 100 models, this keeps retained details at 500K rows
// regardless of how long the server has been running.
const defaultModelDetailsCap = 5000

// detailRetention bounds the age of retained Details so very old events
// do not stick around in memory once the cap kicks in. It is best-effort:
// details are dropped FIFO whenever the cap is exceeded, which usually
// also drops the oldest entries first.
const detailRetention = 24 * time.Hour

func (s *RequestStatistics) updateAPIStats(stats *apiStats, model string, detail RequestDetail) {
	stats.TotalRequests++
	stats.TotalTokens += detail.Tokens.TotalTokens
	modelStatsValue, ok := stats.Models[model]
	if !ok {
		modelStatsValue = &modelStats{}
		stats.Models[model] = modelStatsValue
	}
	modelStatsValue.TotalRequests++
	modelStatsValue.TotalTokens += detail.Tokens.TotalTokens
	modelStatsValue.Details = append(modelStatsValue.Details, detail)
	// Cap: drop oldest entries first when we exceed the per-model limit.
	// Truncating the head preserves the most-recent N, which is what the
	// /usage endpoint and the RecentBuffer consumers care about.
	if len(modelStatsValue.Details) > defaultModelDetailsCap {
		n := len(modelStatsValue.Details) - defaultModelDetailsCap
		// Reuse the backing array to avoid an allocation. slicecopy would
		// allocate a new slice; we copy in place to keep GC quiet.
		copy(modelStatsValue.Details, modelStatsValue.Details[n:])
		modelStatsValue.Details = modelStatsValue.Details[:defaultModelDetailsCap]
	}
}

// Snapshot returns a copy of the aggregated metrics for external consumption.
func (s *RequestStatistics) Snapshot() StatisticsSnapshot {
	result := StatisticsSnapshot{}
	if s == nil {
		return result
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	result.TotalRequests = s.totalRequests
	result.SuccessCount = s.successCount
	result.FailureCount = s.failureCount
	result.TotalTokens = s.totalTokens

	result.APIs = make(map[string]APISnapshot, len(s.apis))
	for apiName, stats := range s.apis {
		apiSnapshot := APISnapshot{
			TotalRequests: stats.TotalRequests,
			TotalTokens:   stats.TotalTokens,
			Models:        make(map[string]ModelSnapshot, len(stats.Models)),
		}
		for modelName, modelStatsValue := range stats.Models {
			requestDetails := make([]RequestDetail, len(modelStatsValue.Details))
			copy(requestDetails, modelStatsValue.Details)
			apiSnapshot.Models[modelName] = ModelSnapshot{
				TotalRequests: modelStatsValue.TotalRequests,
				TotalTokens:   modelStatsValue.TotalTokens,
				Details:       requestDetails,
			}
		}
		result.APIs[apiName] = apiSnapshot
	}

	result.RequestsByDay = make(map[string]int64, len(s.requestsByDay))
	for k, v := range s.requestsByDay {
		result.RequestsByDay[k] = v
	}

	result.RequestsByHour = make(map[string]int64, len(s.requestsByHour))
	for hour, v := range s.requestsByHour {
		key := formatHour(hour)
		result.RequestsByHour[key] = v
	}

	result.TokensByDay = make(map[string]int64, len(s.tokensByDay))
	for k, v := range s.tokensByDay {
		result.TokensByDay[k] = v
	}

	result.TokensByHour = make(map[string]int64, len(s.tokensByHour))
	for hour, v := range s.tokensByHour {
		key := formatHour(hour)
		result.TokensByHour[key] = v
	}

	return result
}

type MergeResult struct {
	Added   int64 `json:"added"`
	Skipped int64 `json:"skipped"`
}

// MergeSnapshot merges an exported statistics snapshot into the current store.
// Existing data is preserved and duplicate request details are skipped.
func (s *RequestStatistics) MergeSnapshot(snapshot StatisticsSnapshot) MergeResult {
	result := MergeResult{}
	if s == nil {
		return result
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	seen := make(map[string]struct{})
	for apiName, stats := range s.apis {
		if stats == nil {
			continue
		}
		for modelName, modelStatsValue := range stats.Models {
			if modelStatsValue == nil {
				continue
			}
			for _, detail := range modelStatsValue.Details {
				seen[dedupKey(apiName, modelName, detail)] = struct{}{}
			}
		}
	}

	for apiName, apiSnapshot := range snapshot.APIs {
		apiName = strings.TrimSpace(apiName)
		if apiName == "" {
			continue
		}
		stats, ok := s.apis[apiName]
		if !ok || stats == nil {
			stats = &apiStats{Models: make(map[string]*modelStats)}
			s.apis[apiName] = stats
		} else if stats.Models == nil {
			stats.Models = make(map[string]*modelStats)
		}
		for modelName, modelSnapshot := range apiSnapshot.Models {
			modelName = strings.TrimSpace(modelName)
			if modelName == "" {
				modelName = "unknown"
			}
			modelStatsValue, ok := stats.Models[modelName]
			if !ok || modelStatsValue == nil {
				modelStatsValue = &modelStats{}
				stats.Models[modelName] = modelStatsValue
			}
			details := modelSnapshot.Details
			if len(details) > defaultModelDetailsCap {
				result.Skipped += int64(len(details) - defaultModelDetailsCap)
				details = details[len(details)-defaultModelDetailsCap:]
			}
			aggregatesAlreadyCovered :=
				(modelSnapshot.TotalRequests > 0 || modelSnapshot.TotalTokens > 0) &&
					modelStatsValue.TotalRequests >= modelSnapshot.TotalRequests &&
					modelStatsValue.TotalTokens >= modelSnapshot.TotalTokens
			for _, detail := range details {
				detail.Tokens = normaliseTokenStats(detail.Tokens)
				detail.Thinking = normaliseThinkingValue(detail.Thinking)
				detail.StatusCode = normaliseStatusCode(detail.StatusCode, detail.Failed)
				if detail.LatencyMs < 0 {
					detail.LatencyMs = 0
				}
				if detail.Timestamp.IsZero() {
					detail.Timestamp = time.Now()
				}
				key := dedupKey(apiName, modelName, detail)
				if _, exists := seen[key]; exists {
					result.Skipped++
					continue
				}
				seen[key] = struct{}{}
				if aggregatesAlreadyCovered {
					s.appendImportedDetail(modelStatsValue, detail)
					s.recordIntoRingsOnly(
						detail.Timestamp,
						modelName,
						detail.AuthIndex,
						detail.Tokens.TotalTokens,
						detail.Failed,
						detail.LatencyMs,
					)
				} else {
					s.recordImported(apiName, modelName, stats, detail)
				}
				result.Added++
			}
			s.preserveModelAggregates(stats, modelStatsValue, modelSnapshot)
		}
		s.preserveAPIAggregates(stats, apiSnapshot)
	}
	s.preserveSnapshotAggregates(snapshot)
	s.preserveSnapshotBuckets(snapshot)

	return result
}

func (s *RequestStatistics) recordImported(apiName, modelName string, stats *apiStats, detail RequestDetail) {
	totalTokens := detail.Tokens.TotalTokens
	if totalTokens < 0 {
		totalTokens = 0
	}

	s.totalRequests++
	if detail.Failed {
		s.failureCount++
	} else {
		s.successCount++
	}
	s.totalTokens += totalTokens

	s.updateAPIStats(stats, modelName, detail)

	dayKey := detail.Timestamp.Format("2006-01-02")
	hourKey := detail.Timestamp.Hour()

	s.requestsByDay[dayKey]++
	s.requestsByHour[hourKey]++
	s.tokensByDay[dayKey] += totalTokens
	s.tokensByHour[hourKey] += totalTokens
	s.recordIntoRingsOnly(detail.Timestamp, modelName, detail.AuthIndex, totalTokens, detail.Failed, detail.LatencyMs)
}

func (s *RequestStatistics) appendImportedDetail(model *modelStats, detail RequestDetail) {
	if model == nil {
		return
	}
	model.Details = append(model.Details, detail)
	if len(model.Details) > defaultModelDetailsCap {
		model.Details = model.Details[len(model.Details)-defaultModelDetailsCap:]
	}
}

func (s *RequestStatistics) preserveModelAggregates(stats *apiStats, modelStatsValue *modelStats, snapshot ModelSnapshot) {
	if s == nil || stats == nil || modelStatsValue == nil {
		return
	}
	if requestDelta := positiveInt64(snapshot.TotalRequests - modelStatsValue.TotalRequests); requestDelta > 0 {
		modelStatsValue.TotalRequests += requestDelta
		stats.TotalRequests += requestDelta
		s.totalRequests += requestDelta
	}

	if tokenDelta := positiveInt64(snapshot.TotalTokens - modelStatsValue.TotalTokens); tokenDelta > 0 {
		modelStatsValue.TotalTokens += tokenDelta
		stats.TotalTokens += tokenDelta
		s.totalTokens += tokenDelta
	}
}

func (s *RequestStatistics) preserveAPIAggregates(stats *apiStats, snapshot APISnapshot) {
	if s == nil || stats == nil {
		return
	}
	if requestDelta := positiveInt64(snapshot.TotalRequests - stats.TotalRequests); requestDelta > 0 {
		stats.TotalRequests += requestDelta
		s.totalRequests += requestDelta
	}
	if tokenDelta := positiveInt64(snapshot.TotalTokens - stats.TotalTokens); tokenDelta > 0 {
		stats.TotalTokens += tokenDelta
		s.totalTokens += tokenDelta
	}
}

func (s *RequestStatistics) preserveSnapshotAggregates(snapshot StatisticsSnapshot) {
	if s == nil {
		return
	}
	if requestDelta := positiveInt64(snapshot.TotalRequests - s.totalRequests); requestDelta > 0 {
		s.totalRequests += requestDelta
	}
	if successDelta := positiveInt64(snapshot.SuccessCount - s.successCount); successDelta > 0 {
		s.successCount += successDelta
	}
	if failureDelta := positiveInt64(snapshot.FailureCount - s.failureCount); failureDelta > 0 {
		s.failureCount += failureDelta
	}
	if tokenDelta := positiveInt64(snapshot.TotalTokens - s.totalTokens); tokenDelta > 0 {
		s.totalTokens += tokenDelta
	}
}

func (s *RequestStatistics) preserveSnapshotBuckets(snapshot StatisticsSnapshot) {
	if s == nil {
		return
	}
	for day, requests := range snapshot.RequestsByDay {
		if requests > s.requestsByDay[day] {
			s.requestsByDay[day] = requests
		}
	}
	for hour, requests := range snapshot.RequestsByHour {
		parsedHour := parseHourKey(hour)
		if requests > s.requestsByHour[parsedHour] {
			s.requestsByHour[parsedHour] = requests
		}
	}
	for day, tokens := range snapshot.TokensByDay {
		if tokens > s.tokensByDay[day] {
			s.tokensByDay[day] = tokens
		}
	}
	for hour, tokens := range snapshot.TokensByHour {
		parsedHour := parseHourKey(hour)
		if tokens > s.tokensByHour[parsedHour] {
			s.tokensByHour[parsedHour] = tokens
		}
	}
}

func positiveInt64(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func normaliseStatusCode(statusCode int, failed bool) int {
	if statusCode > 0 {
		return statusCode
	}
	if !failed {
		return http.StatusOK
	}
	return 0
}

func parseHourKey(hour string) int {
	var parsed int
	if _, err := fmt.Sscanf(strings.TrimSpace(hour), "%d", &parsed); err != nil {
		return 0
	}
	if parsed < 0 {
		return 0
	}
	return parsed % 24
}

func dedupKey(apiName, modelName string, detail RequestDetail) string {
	if requestID := strings.TrimSpace(detail.RequestID); requestID != "" {
		return fmt.Sprintf("request|%s|%s|%s", apiName, modelName, requestID)
	}
	if detail.EventID > 0 {
		return fmt.Sprintf("event|%s|%s|%d", apiName, modelName, detail.EventID)
	}
	timestamp := detail.Timestamp.UTC().Format(time.RFC3339Nano)
	tokens := normaliseTokenStats(detail.Tokens)
	thinking := normaliseThinkingValue(detail.Thinking)
	thinkingBudget := int64(0)
	if thinking != nil && thinking.Budget != nil {
		thinkingBudget = *thinking.Budget
	}
	thinkingIntensity := ""
	thinkingMode := ""
	thinkingLevel := ""
	if thinking != nil {
		thinkingIntensity = thinking.Intensity
		thinkingMode = thinking.Mode
		thinkingLevel = thinking.Level
	}
	return fmt.Sprintf(
		"%s|%s|%s|%s|%s|%t|%d|%d|%d|%d|%d|%d|%s|%s|%s|%d",
		apiName,
		modelName,
		timestamp,
		detail.Source,
		detail.AuthIndex,
		detail.Failed,
		detail.StatusCode,
		tokens.InputTokens,
		tokens.OutputTokens,
		tokens.ReasoningTokens,
		tokens.CachedTokens,
		tokens.TotalTokens,
		thinkingIntensity,
		thinkingMode,
		thinkingLevel,
		thinkingBudget,
	)
}

func resolveAPIIdentifier(ctx context.Context, record coreusage.Record) string {
	if ctx != nil {
		if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil {
			path := ginCtx.FullPath()
			if path == "" && ginCtx.Request != nil {
				path = ginCtx.Request.URL.Path
			}
			method := ""
			if ginCtx.Request != nil {
				method = ginCtx.Request.Method
			}
			if path != "" {
				if method != "" {
					return method + " " + path
				}
				return path
			}
		}
	}
	if record.Provider != "" {
		return record.Provider
	}
	return "unknown"
}

func resolveSuccess(ctx context.Context) bool {
	if ctx == nil {
		return true
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil {
		return true
	}
	status := ginCtx.Writer.Status()
	if status == 0 {
		return true
	}
	return status < httpStatusBadRequest
}

const httpStatusBadRequest = 400

func normaliseDetail(detail coreusage.Detail) TokenStats {
	tokens := TokenStats{
		InputTokens:     detail.InputTokens,
		OutputTokens:    detail.OutputTokens,
		ReasoningTokens: detail.ReasoningTokens,
		CachedTokens:    detail.CachedTokens,
		TotalTokens:     detail.TotalTokens,
	}
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = detail.InputTokens + detail.OutputTokens + detail.ReasoningTokens
	}
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = detail.InputTokens + detail.OutputTokens + detail.ReasoningTokens + detail.CachedTokens
	}
	return tokens
}

func normaliseTokenStats(tokens TokenStats) TokenStats {
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = tokens.InputTokens + tokens.OutputTokens + tokens.ReasoningTokens
	}
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = tokens.InputTokens + tokens.OutputTokens + tokens.ReasoningTokens + tokens.CachedTokens
	}
	return tokens
}

func normaliseThinking(value *coreusage.Thinking) *Thinking {
	if value == nil {
		return nil
	}
	result := &Thinking{
		Intensity: strings.ToLower(strings.TrimSpace(value.Intensity)),
		Mode:      strings.ToLower(strings.TrimSpace(value.Mode)),
		Level:     strings.ToLower(strings.TrimSpace(value.Level)),
	}
	if value.Budget != nil {
		b := *value.Budget
		result.Budget = &b
	}
	return normaliseThinkingValue(result)
}

func normaliseThinkingValue(value *Thinking) *Thinking {
	if value == nil {
		return nil
	}
	result := &Thinking{
		Intensity: strings.ToLower(strings.TrimSpace(value.Intensity)),
		Mode:      strings.ToLower(strings.TrimSpace(value.Mode)),
		Level:     strings.ToLower(strings.TrimSpace(value.Level)),
	}
	if value.Budget != nil {
		b := *value.Budget
		result.Budget = &b
	}
	if result.Intensity == "" && result.Mode == "" && result.Level == "" && result.Budget == nil {
		return nil
	}
	return result
}

func normaliseLatency(latency time.Duration) int64 {
	if latency <= 0 {
		return 0
	}
	return latency.Milliseconds()
}

func formatHour(hour int) string {
	if hour < 0 {
		hour = 0
	}
	hour = hour % 24
	return fmt.Sprintf("%02d", hour)
}

// SnapshotPayload returns a lightweight snapshot of aggregate counters.
func (s *RequestStatistics) SnapshotPayload() UsagePayload {
	s.mu.Lock()
	defer s.mu.Unlock()
	return UsagePayload{
		TotalRequests: s.totalRequests,
		TotalTokens:   s.totalTokens,
		LatestID:      s.recent.LastID(),
	}
}

// AggregateSnapshot returns a small, details-free view of the counters
// suitable for periodic disk persistence. Capturing only aggregates keeps
// the auto-save loop cheap and the on-disk file size bounded regardless
// of how many per-request Details are retained in memory.
//
// In addition to cheap counters, the snapshot carries RingSeed5m /
// RingSeed1h (the current state of the rotating rings) and ModelTotals
// (per-(apiKey,model) counters). Together these let a freshly started
// server resume the management UI / dashboard view without waiting for
// new ingest to refill the rings or re-derive model totals from Details.
func (s *RequestStatistics) AggregateSnapshot() AggregateSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	req := make(map[string]int64, len(s.requestsByDay))
	for k, v := range s.requestsByDay {
		req[k] = v
	}
	ts := make(map[string]int64, len(s.tokensByDay))
	for k, v := range s.tokensByDay {
		ts[k] = v
	}
	out := AggregateSnapshot{
		Version:       2,
		TotalRequests: s.totalRequests,
		SuccessCount:  s.successCount,
		FailureCount:  s.failureCount,
		TotalTokens:   s.totalTokens,
		RequestsByDay: req,
		TokensByDay:   ts,
		ExportedAt:    time.Now().UTC(),
	}
	if s.bucketRing5m != nil {
		out.RingSeed5m = snapshotRing(s.bucketRing5m)
	}
	if s.bucketRing1h != nil {
		out.RingSeed1h = snapshotRing(s.bucketRing1h)
	}
	if len(s.apis) > 0 {
		out.ModelTotals = make(map[string]APITotals, len(s.apis))
		for apiName, api := range s.apis {
			if api == nil {
				continue
			}
			apiSnap := APITotals{TotalRequests: api.TotalRequests, TotalTokens: api.TotalTokens}
			if len(api.Models) > 0 {
				apiSnap.Models = make(map[string]ModelTotals, len(api.Models))
				for modelName, m := range api.Models {
					if m == nil {
						continue
					}
					apiSnap.Models[modelName] = ModelTotals{
						TotalRequests: m.TotalRequests,
						TotalTokens:   m.TotalTokens,
					}
				}
			}
			out.ModelTotals[apiName] = apiSnap
		}
	}
	return out
}

// snapshotRing walks a BucketRing under the ring lock and copies its
// buckets into a serialisable form. Caller must NOT hold s.mu because the
// ring uses its own mutex; the ring snapshot is atomic on its own.
func snapshotRing(r *BucketRing) *RingSeed {
	if r == nil {
		return nil
	}
	snap := r.ReadSnapshot()
	out := &RingSeed{
		BucketSizeMs: r.BucketSize().Milliseconds(),
		StartTimeMs:  snap.Buckets[0].StartTime.UnixMilli(),
		HeadIndex:    len(snap.Buckets) - 1,
		Buckets:      make([]RingSeedBucket, 0, len(snap.Buckets)),
	}
	for _, b := range snap.Buckets {
		rb := RingSeedBucket{
			StartTimeMs: b.StartTime.UnixMilli(),
			Requests:    b.Requests,
			Tokens:      b.Tokens,
			Failures:    b.Failures,
			LatencySum:  b.LatencySum,
			LatencyN:    b.LatencyN,
		}
		if len(b.Models) > 0 {
			rb.ModelBreakdown = make(map[string]ModelTotals, len(b.Models))
			for _, m := range b.Models {
				rb.ModelBreakdown[m.Model] = ModelTotals{
					TotalRequests: m.Requests,
					TotalTokens:   m.Tokens,
					Failures:      m.Failures,
					LatencySum:    m.LatencySum,
					LatencyN:      m.LatencyN,
				}
			}
		}
		if len(b.Auths) > 0 {
			rb.AuthBreakdown = make(map[string]AuthTotals, len(b.Auths))
			for _, a := range b.Auths {
				rb.AuthBreakdown[a.AuthIndex] = AuthTotals{
					Requests: a.Requests,
					Tokens:   a.Tokens,
					Failures: a.Failures,
				}
			}
		}
		out.Buckets = append(out.Buckets, rb)
	}
	return out
}

// ApplyAggregateSnapshot restores the cheap counters, the rotating rings,
// and the per-(apiKey,model) totals from a previously persisted aggregate
// snapshot. Per-request Details are restored separately from the legacy full
// snapshot and the recent-event journal by PersistentLoggerPlugin.Load.
//
// This call is monotonic: it never decreases an existing counter, so it is
// safe to call alongside the legacy MergeSnapshot path or on a hot server
// after a brief restart.
func (s *RequestStatistics) ApplyAggregateSnapshot(snap AggregateSnapshot) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if snap.TotalRequests > s.totalRequests {
		s.totalRequests = snap.TotalRequests
	}
	if snap.SuccessCount > s.successCount {
		s.successCount = snap.SuccessCount
	}
	if snap.FailureCount > s.failureCount {
		s.failureCount = snap.FailureCount
	}
	if snap.TotalTokens > s.totalTokens {
		s.totalTokens = snap.TotalTokens
	}
	for k, v := range snap.RequestsByDay {
		if v > s.requestsByDay[k] {
			s.requestsByDay[k] = v
		}
	}
	for k, v := range snap.TokensByDay {
		if v > s.tokensByDay[k] {
			s.tokensByDay[k] = v
		}
	}
	// Restore per-(apiKey,model) counter view so /usage and the management
	// UI can show model totals immediately. We do not recreate Details; the
	// rotating rings carry the time-windowed information.
	if len(snap.ModelTotals) > 0 {
		if s.apis == nil {
			s.apis = make(map[string]*apiStats, len(snap.ModelTotals))
		}
		for apiName, api := range snap.ModelTotals {
			existing, ok := s.apis[apiName]
			if !ok || existing == nil {
				existing = &apiStats{Models: make(map[string]*modelStats, len(api.Models))}
				s.apis[apiName] = existing
			}
			if api.TotalRequests > existing.TotalRequests {
				existing.TotalRequests = api.TotalRequests
			}
			if api.TotalTokens > existing.TotalTokens {
				existing.TotalTokens = api.TotalTokens
			}
			for modelName, m := range api.Models {
				ms, ok := existing.Models[modelName]
				if !ok || ms == nil {
					ms = &modelStats{}
					existing.Models[modelName] = ms
				}
				if m.TotalRequests > ms.TotalRequests {
					ms.TotalRequests = m.TotalRequests
				}
				if m.TotalTokens > ms.TotalTokens {
					ms.TotalTokens = m.TotalTokens
				}
			}
		}
	}
	s.mu.Unlock()

	// Version 1 seeds were written by the mixed physical/logical head
	// implementation and can contain mislabelled buckets. Rebuild those
	// rings from retained Details instead.
	if snap.Version >= 2 {
		s.restoreRingFromSeed(s.bucketRing5m, snap.RingSeed5m)
		s.restoreRingFromSeed(s.bucketRing1h, snap.RingSeed1h)
	}
}

// restoreRingFromSeed copies a RingSeed's bucket counters into the live
// ring without disturbing the live rotate-head semantics. The seed's
// bucket sizes must match the live ring's; otherwise the seed is ignored
// (a version mismatch from a config change should not corrupt runtime).
func (s *RequestStatistics) restoreRingFromSeed(r *BucketRing, seed *RingSeed) {
	if r == nil || seed == nil || len(seed.Buckets) == 0 {
		return
	}
	if r.BucketSize().Milliseconds() != seed.BucketSizeMs {
		return
	}
	if len(seed.Buckets) != r.BucketCount() {
		return
	}
	r.RestoreFromSeed(seed.StartTimeMs, seed.HeadIndex, seed.Buckets)
}

// RestoreFromLegacySnapshot rebuilds counters, model totals, details, and
// rotating rings when no aggregate snapshot is available.
func (s *RequestStatistics) RestoreFromLegacySnapshot(snap StatisticsSnapshot) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if snap.TotalRequests > s.totalRequests {
		s.totalRequests = snap.TotalRequests
	}
	if snap.SuccessCount > s.successCount {
		s.successCount = snap.SuccessCount
	}
	if snap.FailureCount > s.failureCount {
		s.failureCount = snap.FailureCount
	}
	if snap.TotalTokens > s.totalTokens {
		s.totalTokens = snap.TotalTokens
	}
	for k, v := range snap.RequestsByDay {
		if v > s.requestsByDay[k] {
			s.requestsByDay[k] = v
		}
	}
	for k, v := range snap.TokensByDay {
		if v > s.tokensByDay[k] {
			s.tokensByDay[k] = v
		}
	}
	for hour, v := range snap.RequestsByHour {
		parsed := parseHourKey(hour)
		if v > s.requestsByHour[parsed] {
			s.requestsByHour[parsed] = v
		}
	}
	for hour, v := range snap.TokensByHour {
		parsed := parseHourKey(hour)
		if v > s.tokensByHour[parsed] {
			s.tokensByHour[parsed] = v
		}
	}
	for apiName, apiSnap := range snap.APIs {
		apiName = strings.TrimSpace(apiName)
		if apiName == "" {
			continue
		}
		api := s.apis[apiName]
		if api == nil {
			api = &apiStats{Models: make(map[string]*modelStats, len(apiSnap.Models))}
			s.apis[apiName] = api
		}
		if apiSnap.TotalRequests > api.TotalRequests {
			api.TotalRequests = apiSnap.TotalRequests
		}
		if apiSnap.TotalTokens > api.TotalTokens {
			api.TotalTokens = apiSnap.TotalTokens
		}
		for modelName, modelSnap := range apiSnap.Models {
			modelName = normalizeUsageModelName(modelName)
			model := api.Models[modelName]
			if model == nil {
				model = &modelStats{}
				api.Models[modelName] = model
			}
			if modelSnap.TotalRequests > model.TotalRequests {
				model.TotalRequests = modelSnap.TotalRequests
			}
			if modelSnap.TotalTokens > model.TotalTokens {
				model.TotalTokens = modelSnap.TotalTokens
			}
		}
	}
	s.mu.Unlock()
	s.RestoreDetailsFromLegacySnapshot(snap, true)
}

// RestoreDetailsFromLegacySnapshot restores per-request rows without changing
// aggregate counters. When rebuildRings is true, the rows also seed rings for
// legacy snapshots that predate aggregate ring data.
func (s *RequestStatistics) RestoreDetailsFromLegacySnapshot(snap StatisticsSnapshot, rebuildRings bool) {
	if s == nil {
		return
	}
	type detailRow struct {
		modelName, authIndex string
		tokens               int64
		failed               bool
		latencyMs            int64
		timestamp            time.Time
	}
	var rows []detailRow
	var maxEventID uint64

	s.mu.Lock()
	for apiName, apiSnap := range snap.APIs {
		apiName = strings.TrimSpace(apiName)
		if apiName == "" {
			continue
		}
		api := s.apis[apiName]
		if api == nil {
			api = &apiStats{Models: make(map[string]*modelStats, len(apiSnap.Models))}
			s.apis[apiName] = api
		}
		for modelName, modelSnap := range apiSnap.Models {
			modelName = normalizeUsageModelName(modelName)
			model := api.Models[modelName]
			if model == nil {
				model = &modelStats{}
				api.Models[modelName] = model
			}
			if len(model.Details) > 0 || len(modelSnap.Details) == 0 {
				continue
			}
			capped := modelSnap.Details
			if len(capped) > defaultModelDetailsCap {
				capped = capped[len(capped)-defaultModelDetailsCap:]
			}
			model.Details = make([]RequestDetail, len(capped))
			for i := range capped {
				detail := capped[i]
				detail.Tokens = normaliseTokenStats(detail.Tokens)
				detail.Thinking = normaliseThinkingValue(detail.Thinking)
				detail.StatusCode = normaliseStatusCode(detail.StatusCode, detail.Failed)
				if detail.LatencyMs < 0 {
					detail.LatencyMs = 0
				}
				if detail.EventID > maxEventID {
					maxEventID = detail.EventID
				}
				model.Details[i] = detail
			}
			if !rebuildRings {
				continue
			}
			for i := range model.Details {
				detail := model.Details[i]
				if detail.Timestamp.IsZero() {
					continue
				}
				rows = append(rows, detailRow{
					modelName: modelName,
					authIndex: detail.AuthIndex,
					tokens:    detail.Tokens.TotalTokens,
					failed:    detail.Failed,
					latencyMs: detail.LatencyMs,
					timestamp: detail.Timestamp,
				})
			}
		}
	}
	s.mu.Unlock()
	s.advanceNextEventID(maxEventID)

	if !rebuildRings {
		return
	}
	for _, row := range rows {
		s.recordIntoRingsOnly(row.timestamp, row.modelName, row.authIndex, row.tokens, row.failed, row.latencyMs)
	}
}

// RestoreDetailsFromRecentEvents fills request-detail gaps from the bounded
// recent-event store without changing aggregate counters or ring buckets.
func (s *RequestStatistics) RestoreDetailsFromRecentEvents(events []UsageEvent) {
	if s == nil || len(events) == 0 {
		return
	}

	s.mu.Lock()
	touched := make(map[*modelStats]struct{})
	for _, event := range events {
		if event.RequestedAt.IsZero() {
			continue
		}
		apiName := strings.TrimSpace(event.APIKey)
		if apiName == "" {
			apiName = "unknown"
		}
		modelName := normalizeUsageModelName(event.Model)
		api := s.apis[apiName]
		if api == nil {
			api = &apiStats{Models: make(map[string]*modelStats)}
			s.apis[apiName] = api
		}
		model := api.Models[modelName]
		if model == nil {
			model = &modelStats{}
			api.Models[modelName] = model
		}
		detail := RequestDetail{
			EventID:    event.ID,
			Timestamp:  event.RequestedAt,
			LatencyMs:  event.DurationMs,
			Source:     event.Source,
			AuthIndex:  event.AuthIndex,
			RequestID:  strings.TrimSpace(event.RequestID),
			StatusCode: normaliseStatusCode(event.StatusCode, event.Failed),
			Thinking:   normaliseThinkingValue(event.Thinking),
			Tokens: TokenStats{
				InputTokens:     event.Tokens.Input,
				OutputTokens:    event.Tokens.Output,
				ReasoningTokens: event.Tokens.Reasoning,
				CachedTokens:    event.Tokens.Cached,
				TotalTokens:     event.Tokens.Total,
			},
			Failed: event.Failed,
		}
		duplicate := false
		for i := range model.Details {
			if sameUsageEventDetail(model.Details[i], event) {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		model.Details = append(model.Details, detail)
		touched[model] = struct{}{}
	}
	for model := range touched {
		sort.Slice(model.Details, func(i, j int) bool {
			return model.Details[i].Timestamp.Before(model.Details[j].Timestamp)
		})
		if len(model.Details) > defaultModelDetailsCap {
			model.Details = model.Details[len(model.Details)-defaultModelDetailsCap:]
		}
	}
	s.mu.Unlock()
}

func sameUsageEventDetail(detail RequestDetail, event UsageEvent) bool {
	detailRequestID := strings.TrimSpace(detail.RequestID)
	eventRequestID := strings.TrimSpace(event.RequestID)
	if detailRequestID != "" && eventRequestID != "" {
		return detailRequestID == eventRequestID
	}
	if detail.EventID > 0 && event.ID > 0 {
		return detail.EventID == event.ID
	}
	if !detail.Timestamp.Equal(event.RequestedAt) ||
		detail.StatusCode != normaliseStatusCode(event.StatusCode, event.Failed) ||
		detail.Failed != event.Failed {
		return false
	}
	tokens := normaliseTokenStats(detail.Tokens)
	if tokens.InputTokens != event.Tokens.Input ||
		tokens.OutputTokens != event.Tokens.Output ||
		tokens.ReasoningTokens != event.Tokens.Reasoning ||
		tokens.CachedTokens != event.Tokens.Cached ||
		tokens.TotalTokens != event.Tokens.Total {
		return false
	}
	if event.Source != "" && detail.Source != event.Source {
		return false
	}
	if event.AuthIndex != "" && detail.AuthIndex != event.AuthIndex {
		return false
	}
	return true
}

func normalizeUsageModelName(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "unknown"
	}
	return model
}

// recordIntoRingsOnly writes a single historical record into the rotating
// rings without touching counters, the daymap, or the apis/models totals.
// Used by RestoreFromLegacySnapshot to repopulate the dashboard buckets
// after a restart.
func (s *RequestStatistics) recordIntoRingsOnly(t time.Time, model, authIndex string, tokens int64, failed bool, latencyMs int64) {
	if s.bucketRing5m != nil {
		s.bucketRing5m.Record(t, model, authIndex, tokens, failed, latencyMs)
	}
	if s.bucketRing1h != nil {
		s.bucketRing1h.Record(t, model, authIndex, tokens, failed, latencyMs)
	}
}

// ApplyRecentEvents re-populates the RecentBuffer ring from a persisted
// snapshot. Events are pushed in order so the LastID matches the highest
// event ID in the buffer after this call returns.
func (s *RequestStatistics) ApplyRecentEvents(events []UsageEvent) {
	if s == nil || len(events) == 0 {
		return
	}
	for _, e := range events {
		s.recent.Push(e)
		s.advanceNextEventID(e.ID)
	}
}

func (s *RequestStatistics) advanceNextEventID(id uint64) {
	if s == nil || id == 0 {
		return
	}
	for {
		current := s.nextEventID.Load()
		if id <= current || s.nextEventID.CompareAndSwap(current, id) {
			return
		}
	}
}

// RecentEventsSnapshot returns the current ring buffer contents for
// persistence.
func (s *RequestStatistics) RecentEventsSnapshot() RecentEventsSnapshot {
	events := s.recent.Events()
	if events == nil {
		events = []UsageEvent{}
	}
	return RecentEventsSnapshot{Version: 2, Events: events}
}

// RecentSince returns events with id > sinceID from the ring buffer.
func (s *RequestStatistics) RecentSince(sinceID uint64) []UsageEvent {
	return s.recent.Since(sinceID)
}

// LatestEventID returns the most recent event ID from the ring buffer.
func (s *RequestStatistics) LatestEventID() uint64 {
	return s.recent.LastID()
}

// RecordFromTest ingests a UsageEvent directly for testing purposes.
func (s *RequestStatistics) RecordFromTest(evt UsageEvent) {
	if s == nil {
		return
	}
	if evt.ID == 0 {
		evt.ID = s.nextEventID.Add(1)
	}
	s.recent.Push(evt)
	s.broker.Publish(evt)
}
