package management

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

const (
	textOpsIntentTokenConsumption = "TOKEN_CONSUMPTION"
	textOpsIntentCacheMetrics     = "CACHE_METRICS"
	textOpsIntentFinancialStatus  = "FINANCIAL_STATUS"
	textOpsIntentGeneralAssistant = "GENERAL_ASSISTANT"

	textOpsRoleAdmin    = "admin"
	textOpsRoleReseller = "reseller"
	textOpsRoleCustomer = "customer"

	textOpsDefaultRouterModel    = "MiniMax-M2.7-highspeed"
	textOpsDefaultPresenterModel = "MiniMax-M2.7-highspeed"
	textOpsDefaultRouterTokens   = 2000
	textOpsDefaultPresenterToken = 4096
	textOpsMaxLLMToken           = 4096
	textOpsTokensPerPriceUnit    = 1_000_000
)

var (
	textOpsCycleIDPattern   = regexp.MustCompile(`(?i)\b\d{4}-(?:w\d{1,2}|q[1-4]|m\d{1,2})\b`)
	textOpsModelPattern     = regexp.MustCompile(`(?i)(?:模型|model)\s*[:：=]?\s*([a-zA-Z0-9._:-]{2,})`)
	textOpsBareModelPattern = regexp.MustCompile(`[A-Za-z][A-Za-z0-9]*(?:[._:/-][A-Za-z0-9]+)+`)
	textOpsUserIDPatternCN  = regexp.MustCompile(`(?i)(?:用户|user(?:_id| id)?)\s*[:：=]?\s*(\d+)`)
	textOpsLastNDays        = regexp.MustCompile(`(?i)(?:近|最近|last)\s*(\d+)\s*(?:天|day|days)`)
	textOpsLastNHours       = regexp.MustCompile(`(?i)(?:近|最近|last)\s*(\d+)\s*(?:小时|hour|hours|h)`)
	textOpsCycleQuarter     = regexp.MustCompile(`(?i)\b(\d{4})[-\s]?q([1-4])\b`)
	textOpsCycleMonth       = regexp.MustCompile(`(?i)\b(\d{4})[-\s]?m(\d{1,2})\b`)
	textOpsCycleWeek        = regexp.MustCompile(`(?i)\b(\d{4})[-\s]?w(\d{1,2})\b`)
	textOpsSlashDateRange   = regexp.MustCompile(`(?i)(?:(\d{4})[/-])?(\d{1,2})[/-](\d{1,2})\s*(?:-|~|–|—|至|到|to|through)\s*(?:(\d{4})[/-])?(\d{1,2})[/-](\d{1,2})`)
	textOpsCNDateRange      = regexp.MustCompile(`(?:(\d{4})年)?\s*(\d{1,2})月(\d{1,2})日?\s*(?:-|~|–|—|至|到)\s*(?:(\d{4})年)?\s*(\d{1,2})月(\d{1,2})日?`)
)

type textOpsQueryRequest struct {
	UserQuery string `json:"user_query"`

	CurrentTime string `json:"current_time"`
	FastMode    bool   `json:"fast_mode"`

	OperatorContext textOpsOperatorContext `json:"operator_context"`

	Router    textOpsLLMConfig `json:"router"`
	Presenter textOpsLLMConfig `json:"presenter"`
}

type textOpsOperatorContext struct {
	UserID         int64   `json:"user_id"`
	Role           string  `json:"role"`
	AllowedUserIDs []int64 `json:"allowed_user_ids"`
}

type textOpsLLMConfig struct {
	Enabled   bool   `json:"enabled"`
	APIKey    string `json:"api_key"`
	BaseURL   string `json:"base_url"`
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens"`
}

type textOpsQueryResponse struct {
	GeneratedAt time.Time `json:"generated_at"`
	UserQuery   string    `json:"user_query"`
	CurrentTime string    `json:"current_time"`

	Router    textOpsRouterResult    `json:"router"`
	Guardrail textOpsGuardrailResult `json:"guardrail"`
	Data      textOpsDataResult      `json:"data"`

	Presentation textOpsPresentation `json:"presentation"`
	Warnings     []string            `json:"warnings,omitempty"`
}

type textOpsRouterResult struct {
	Intent  string         `json:"intent"`
	Filters textOpsFilters `json:"filters"`
	GroupBy []string       `json:"group_by"`
	Route   string         `json:"route"`
}

type textOpsFilters struct {
	ModelName        *string `json:"model_name,omitempty"`
	StartTime        string  `json:"start_time"`
	EndTime          string  `json:"end_time"`
	UserID           *int64  `json:"user_id,omitempty"`
	FinancialCycleID *string `json:"financial_cycle_id,omitempty"`
	Source           *string `json:"source,omitempty"`
}

type textOpsGuardrailResult struct {
	Blocked           bool           `json:"blocked"`
	Reason            string         `json:"reason,omitempty"`
	PromptInjection   bool           `json:"prompt_injection"`
	Rewritten         bool           `json:"rewritten"`
	AppliedRole       string         `json:"applied_role,omitempty"`
	EffectiveFilters  textOpsFilters `json:"effective_filters"`
	RewriteOperations []string       `json:"rewrite_operations,omitempty"`
}

type textOpsDataResult struct {
	Intent  string           `json:"intent"`
	Summary map[string]any   `json:"summary"`
	Rows    []map[string]any `json:"rows"`
	Raw     map[string]any   `json:"raw,omitempty"`
}

type textOpsPresentation struct {
	Markdown string                `json:"markdown"`
	Blocks   []textOpsDisplayBlock `json:"blocks"`
}

type textOpsDisplayBlock struct {
	Type  string `json:"type"`
	Title string `json:"title,omitempty"`
	Data  any    `json:"data"`
}

type textOpsIntentSpec struct {
	Intent  string                   `json:"intent"`
	Filters textOpsIntentSpecFilters `json:"filters"`
	GroupBy []string                 `json:"group_by"`
}

type textOpsIntentSpecFilters struct {
	ModelName        *string `json:"model_name"`
	StartTime        string  `json:"start_time"`
	EndTime          string  `json:"end_time"`
	UserID           *int64  `json:"user_id"`
	FinancialCycleID *string `json:"financial_cycle_id"`
	Source           *string `json:"source"`
}

type textOpsResolvedWindow struct {
	Start time.Time
	End   time.Time
}

type textOpsLLMChatRequest struct {
	Model           string                      `json:"model"`
	Messages        []aiOpsAICallRequestMessage `json:"messages"`
	MaxTokens       int                         `json:"max_tokens,omitempty"`
	Temperature     float64                     `json:"temperature,omitempty"`
	ReasoningEffort string                      `json:"reasoning_effort,omitempty"`
	ResponseFormat  map[string]string           `json:"response_format,omitempty"`
}

type textOpsPresenterResponse struct {
	Markdown       string                  `json:"markdown"`
	EChartsJSON    string                  `json:"echarts_option_json,omitempty"`
	ChartTitle     string                  `json:"chart_title,omitempty"`
	EChartsOptions []textOpsPresenterChart `json:"echarts_options,omitempty"`
}

type textOpsPresenterChart struct {
	Title       string `json:"title,omitempty"`
	OptionJSON  string `json:"option_json,omitempty"`
	EChartsJSON string `json:"echarts_option_json,omitempty"`
}

// QueryTextOps provides a natural-language Text-to-Ops entry for management users.
// It routes user intent, applies guardrails, executes constrained analytics, and formats results.
func (h *Handler) QueryTextOps(c *gin.Context) {
	var req textOpsQueryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	userQuery := strings.TrimSpace(req.UserQuery)
	if userQuery == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user_query is required"})
		return
	}

	currentTime := resolveTextOpsCurrentTime(req.CurrentTime)
	response := textOpsQueryResponse{
		GeneratedAt: currentTime,
		UserQuery:   userQuery,
		CurrentTime: currentTime.Format(time.RFC3339),
	}

	injected, reason := detectTextOpsPromptInjection(userQuery)
	if injected {
		response.Router = textOpsRouterResult{
			Intent: textOpsIntentTokenConsumption,
			Filters: textOpsFilters{
				StartTime: currentTime.Add(-24 * time.Hour).Format(time.RFC3339),
				EndTime:   currentTime.Format(time.RFC3339),
			},
			GroupBy: []string{"time_bucket_day"},
			Route:   "blocked_by_guardrail",
		}
		response.Guardrail = textOpsGuardrailResult{
			Blocked:          true,
			Reason:           reason,
			PromptInjection:  true,
			EffectiveFilters: response.Router.Filters,
		}
		c.JSON(http.StatusForbidden, response)
		return
	}

	queryKind := classifyTextOpsQueryKind(userQuery)
	if queryKind != textOpsQueryKindAnalytics {
		response = h.executeTextOpsGeneralQuery(c.Request.Context(), req, currentTime, response, queryKind)
		if response.Guardrail.Blocked {
			c.JSON(http.StatusForbidden, response)
			return
		}
		c.JSON(http.StatusOK, response)
		return
	}

	var intentSpec textOpsIntentSpec
	var route string
	var warnings []string
	if req.FastMode {
		intentSpec = completeTextOpsIntentSpecFromQuery(h.parseTextOpsIntentHeuristic(userQuery, currentTime), userQuery)
		route = "heuristic_fast"
	} else {
		intentSpec, route, warnings = h.parseTextOpsIntent(c.Request.Context(), req, currentTime)
	}
	response.Warnings = append(response.Warnings, warnings...)

	filters, window, err := normalizeTextOpsFilters(intentSpec.Filters, currentTime)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	response.Router = textOpsRouterResult{
		Intent:  normalizeTextOpsIntent(intentSpec.Intent),
		Filters: filters,
		GroupBy: normalizeTextOpsGroupBy(intentSpec.GroupBy),
		Route:   route,
	}

	guardrail := applyTextOpsGuardrail(userQuery, response.Router.Filters, req.OperatorContext)
	response.Guardrail = guardrail
	if guardrail.Blocked {
		c.JSON(http.StatusForbidden, response)
		return
	}
	response.Router.Filters = guardrail.EffectiveFilters

	dataResult, snapshot := h.executeTextOpsQuery(
		c.Request.Context(),
		response.Router.Intent,
		response.Router.Filters,
		response.Router.GroupBy,
		window,
		currentTime,
	)
	response.Data = dataResult
	if len(snapshot.Warnings) > 0 {
		response.Warnings = append(response.Warnings, snapshot.Warnings...)
	}

	markdown, blocks := buildTextOpsPresentation(response.Data, response.Router.Filters, response.Router.GroupBy, currentTime)
	response.Presentation = textOpsPresentation{
		Markdown: markdown,
		Blocks:   blocks,
	}

	presenterCfg := resolveTextOpsLLMConfig(req.Presenter, h, textOpsDefaultPresenterModel, textOpsDefaultPresenterToken)
	if presenterCfg.Enabled && !req.FastMode {
		if llmPresentation, errPresent := h.enhanceTextOpsPresentation(c.Request.Context(), presenterCfg, response); errPresent == nil {
			response.Presentation = llmPresentation
		} else {
			response.Warnings = append(response.Warnings, "presenter_llm_failed: "+errPresent.Error())
		}
	}

	c.JSON(http.StatusOK, response)
}

func resolveTextOpsCurrentTime(raw string) time.Time {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return time.Now().UTC()
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, trimmed); err == nil {
			return parsed.UTC()
		}
	}
	return time.Now().UTC()
}

func detectTextOpsPromptInjection(query string) (bool, string) {
	normalized := strings.ToLower(strings.TrimSpace(query))
	if normalized == "" {
		return false, ""
	}

	patterns := []struct {
		needle string
		reason string
	}{
		{"ignore previous", "detected prompt-injection phrase: ignore previous"},
		{"ignore all previous", "detected prompt-injection phrase: ignore all previous"},
		{"忽略上述", "detected prompt-injection phrase: 忽略上述"},
		{"忽略之前", "detected prompt-injection phrase: 忽略之前"},
		{"system prompt", "detected prompt-injection phrase: system prompt"},
		{"developer prompt", "detected prompt-injection phrase: developer prompt"},
		{"drop table", "detected destructive pattern: drop table"},
		{"delete from", "detected destructive pattern: delete from"},
		{"truncate", "detected destructive pattern: truncate"},
		{"把所有数据删除", "detected destructive pattern: 删除数据"},
		{"将所有数据删除", "detected destructive pattern: 删除数据"},
		{"改为0", "detected suspicious mutation pattern: 改为0"},
		{"set .* = 0", "detected suspicious mutation pattern: set = 0"},
	}
	for _, pattern := range patterns {
		if strings.Contains(normalized, strings.ToLower(pattern.needle)) {
			return true, pattern.reason
		}
	}
	return false, ""
}

func (h *Handler) parseTextOpsIntent(ctx context.Context, req textOpsQueryRequest, now time.Time) (textOpsIntentSpec, string, []string) {
	routerCfg := resolveTextOpsLLMConfig(req.Router, h, textOpsDefaultRouterModel, textOpsDefaultRouterTokens)
	if routerCfg.Enabled {
		spec, err := h.parseTextOpsIntentWithLLM(ctx, req.UserQuery, now, routerCfg)
		if err == nil {
			return completeTextOpsIntentSpecFromQuery(spec, req.UserQuery), "llm_router", nil
		}
		return completeTextOpsIntentSpecFromQuery(h.parseTextOpsIntentHeuristic(req.UserQuery, now), req.UserQuery), "heuristic_fallback", []string{"router_llm_failed: " + err.Error()}
	}
	return completeTextOpsIntentSpecFromQuery(h.parseTextOpsIntentHeuristic(req.UserQuery, now), req.UserQuery), "heuristic", nil
}

func resolveTextOpsLLMConfig(cfg textOpsLLMConfig, h *Handler, defaultModel string, defaultMaxTokens int) textOpsLLMConfig {
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	cfg.BaseURL = normalizeAIOpsAIBaseURL(strings.TrimSpace(cfg.BaseURL), h)
	cfg.Model = strings.TrimSpace(cfg.Model)
	if cfg.Model == "" {
		cfg.Model = defaultModel
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = defaultMaxTokens
	}
	if cfg.MaxTokens > textOpsMaxLLMToken {
		cfg.MaxTokens = textOpsMaxLLMToken
	}
	if cfg.APIKey == "" {
		cfg.APIKey = firstTextOpsCPAClientAPIKey(h)
	}
	if !cfg.Enabled || cfg.APIKey == "" || cfg.BaseURL == "" {
		cfg.Enabled = false
	}
	return cfg
}

func firstTextOpsCPAClientAPIKey(h *Handler) string {
	if h == nil || h.cfg == nil {
		return ""
	}
	for _, key := range h.cfg.APIKeys {
		if trimmed := strings.TrimSpace(key); trimmed != "" {
			return trimmed
		}
	}
	for _, entry := range h.cfg.APIKeyEntries {
		if trimmed := strings.TrimSpace(entry.Key); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func (h *Handler) parseTextOpsIntentWithLLM(ctx context.Context, userQuery string, now time.Time, cfg textOpsLLMConfig) (textOpsIntentSpec, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	systemPrompt := strings.Join([]string{
		"You are an intent router for a Text-to-Ops platform.",
		"Output a JSON object only. Do not include markdown.",
		"Do not include <think>, reasoning, analysis, or chain-of-thought.",
		"Supported intents: TOKEN_CONSUMPTION, CACHE_METRICS, FINANCIAL_STATUS.",
		"Translate relative time to absolute ISO8601 UTC.",
		"All-time cue: when the user asks for a 'total' or 'all-time' aggregate without specifying a window, set start_time to " + textOpsAllTimeStart + " and end_time to the current time. Do NOT default to the last 24h.",
		"Current time is " + now.Format(time.RFC3339) + ".",
		"Current date is " + now.Format("2006-01-02") + ".",
		"Current weekday is " + now.Weekday().String() + ".",
		"Schema:",
		`{"intent":"...","filters":{"model_name":string|null,"start_time":string,"end_time":string,"user_id":number|null,"financial_cycle_id":string|null,"source":string|null},"group_by":["model_name"|"time_bucket_day"|"user_id"]}`,
	}, "\n")

	fewShot := strings.Join([]string{
		`Example Input: 帮我查上周 DeepSeek 模型的 Token 消耗`,
		`Example Output: {"intent":"TOKEN_CONSUMPTION","filters":{"model_name":"deepseek","start_time":"2026-05-18T00:00:00Z","end_time":"2026-05-25T00:00:00Z","user_id":null,"financial_cycle_id":null,"source":null},"group_by":["time_bucket_day","model_name"]}`,
		`Example Input: 查询 5/20-5/31 总的token请求数/token总数/总花费的情况`,
		`Example Output: {"intent":"FINANCIAL_STATUS","filters":{"model_name":null,"start_time":"2026-05-20T00:00:00Z","end_time":"2026-06-01T00:00:00Z","user_id":null,"financial_cycle_id":null,"source":null},"group_by":["model_name"]}`,
		`Example Input: 近24小时缓存命中率`,
		`Example Output: {"intent":"CACHE_METRICS","filters":{"model_name":null,"start_time":"2026-05-29T00:00:00Z","end_time":"2026-05-30T00:00:00Z","user_id":null,"financial_cycle_id":null,"source":null},"group_by":["time_bucket_day"]}`,
		`Example Input: 看一下 2026-Q2 财务情况`,
		`Example Output: {"intent":"FINANCIAL_STATUS","filters":{"model_name":null,"start_time":"2026-04-01T00:00:00Z","end_time":"2026-06-30T23:59:59Z","user_id":null,"financial_cycle_id":"2026-Q2","source":null},"group_by":["model_name"]}`,
		`Example Input: 查询下 MiniMax-M2.7-highspeed 总的 token 用量`,
		`Example Output: {"intent":"TOKEN_CONSUMPTION","filters":{"model_name":"MiniMax-M2.7-highspeed","start_time":"` + textOpsAllTimeStart + `","end_time":"<current UTC ISO8601>","user_id":null,"financial_cycle_id":null,"source":null},"group_by":["model_name"]}`,
	}, "\n")

	requestBody := textOpsLLMChatRequest{
		Model: cfg.Model,
		Messages: []aiOpsAICallRequestMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: fewShot + "\n\nUser Input: " + strings.TrimSpace(userQuery)},
		},
		MaxTokens:       cfg.MaxTokens,
		Temperature:     0,
		ReasoningEffort: resolveManagementLLMReasoningEffort(cfg.Model),
		ResponseFormat: map[string]string{
			"type": "json_object",
		},
	}
	rawResp, err := h.callTextOpsLLM(ctx, cfg, requestBody)
	if err != nil {
		return textOpsIntentSpec{}, err
	}

	var spec textOpsIntentSpec
	rawJSON, errExtract := extractTextOpsJSONObject(rawResp)
	if errExtract != nil {
		return textOpsIntentSpec{}, errExtract
	}
	if errUnmarshal := json.Unmarshal([]byte(rawJSON), &spec); errUnmarshal != nil {
		return textOpsIntentSpec{}, fmt.Errorf("router output is not valid json: %w", errUnmarshal)
	}
	spec.Intent = normalizeTextOpsIntent(spec.Intent)
	return spec, nil
}

func extractTextOpsJSONObject(raw string) (string, error) {
	text := stripTextOpsThinkBlocks(raw)
	if text == "" {
		return "", fmt.Errorf("router output is empty")
	}
	if json.Valid([]byte(text)) {
		return text, nil
	}

	firstValid := ""
	for start := strings.IndexByte(text, '{'); start >= 0 && start < len(text); {
		depth := 0
		inString := false
		escaped := false
		for index := start; index < len(text); index++ {
			ch := text[index]
			if inString {
				if escaped {
					escaped = false
					continue
				}
				switch ch {
				case '\\':
					escaped = true
				case '"':
					inString = false
				}
				continue
			}
			switch ch {
			case '"':
				inString = true
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					candidate := strings.TrimSpace(text[start : index+1])
					if json.Valid([]byte(candidate)) {
						if firstValid == "" {
							firstValid = candidate
						}
						var obj map[string]json.RawMessage
						if errUnmarshal := json.Unmarshal([]byte(candidate), &obj); errUnmarshal == nil {
							if _, ok := obj["intent"]; ok {
								return candidate, nil
							}
						}
					}
					index = len(text)
				}
			}
		}
		next := strings.IndexByte(text[start+1:], '{')
		if next < 0 {
			break
		}
		start = start + 1 + next
	}
	if firstValid != "" {
		return firstValid, nil
	}
	return "", fmt.Errorf("router output is not valid json")
}

func (h *Handler) parseTextOpsIntentHeuristic(userQuery string, now time.Time) textOpsIntentSpec {
	normalized := strings.ToLower(strings.TrimSpace(userQuery))
	intent := textOpsIntentTokenConsumption
	switch {
	case strings.Contains(normalized, "缓存") || strings.Contains(normalized, "cache"):
		intent = textOpsIntentCacheMetrics
	case strings.Contains(normalized, "财务") ||
		strings.Contains(normalized, "对账") ||
		strings.Contains(normalized, "结算") ||
		strings.Contains(normalized, "账单") ||
		strings.Contains(normalized, "花费") ||
		strings.Contains(normalized, "费用") ||
		strings.Contains(normalized, "成本") ||
		strings.Contains(normalized, "总花费") ||
		strings.Contains(normalized, "cost") ||
		strings.Contains(normalized, "spend") ||
		strings.Contains(normalized, "amount") ||
		strings.Contains(normalized, "financial"):
		intent = textOpsIntentFinancialStatus
	}

	startAt, endAt := inferTextOpsTimeRange(normalized, now)

	modelName := inferTextOpsModelName(userQuery)

	var userID *int64
	if m := textOpsUserIDPatternCN.FindStringSubmatch(userQuery); len(m) > 1 {
		if parsed, err := strconv.ParseInt(strings.TrimSpace(m[1]), 10, 64); err == nil {
			userID = &parsed
		}
	}

	var cycleID *string
	if m := textOpsCycleIDPattern.FindString(userQuery); strings.TrimSpace(m) != "" {
		value := strings.ToUpper(strings.TrimSpace(m))
		cycleID = &value
	}

	groupBy := []string{}
	if strings.Contains(normalized, "趋势") ||
		strings.Contains(normalized, "按天") ||
		strings.Contains(normalized, "每天") ||
		strings.Contains(normalized, "daily") ||
		strings.Contains(normalized, "trend") {
		groupBy = append(groupBy, "time_bucket_day")
	}
	if strings.Contains(normalized, "模型") || strings.Contains(normalized, "model") || strings.Contains(normalized, "top") {
		groupBy = append(groupBy, "model_name")
	}
	if strings.Contains(normalized, "用户") || strings.Contains(normalized, "user") {
		groupBy = append(groupBy, "user_id")
	}
	if len(groupBy) == 0 {
		if intent == textOpsIntentFinancialStatus || strings.Contains(normalized, "总的") || strings.Contains(normalized, "总计") || strings.Contains(normalized, "汇总") {
			groupBy = []string{"model_name"}
		} else {
			groupBy = []string{"time_bucket_day"}
		}
	}

	return textOpsIntentSpec{
		Intent: intent,
		Filters: textOpsIntentSpecFilters{
			ModelName:        modelName,
			StartTime:        startAt.Format(time.RFC3339),
			EndTime:          endAt.Format(time.RFC3339),
			UserID:           userID,
			FinancialCycleID: cycleID,
		},
		GroupBy: groupBy,
	}
}

func completeTextOpsIntentSpecFromQuery(spec textOpsIntentSpec, userQuery string) textOpsIntentSpec {
	if spec.Filters.ModelName == nil || strings.TrimSpace(*spec.Filters.ModelName) == "" {
		spec.Filters.ModelName = inferTextOpsModelName(userQuery)
	}
	return spec
}

func inferTextOpsModelName(userQuery string) *string {
	if m := textOpsModelPattern.FindStringSubmatch(userQuery); len(m) > 1 {
		value := normalizeTextOpsModelCandidate(m[1])
		if value != "" {
			return &value
		}
	}
	matches := textOpsBareModelPattern.FindAllString(userQuery, -1)
	for _, match := range matches {
		value := normalizeTextOpsModelCandidate(match)
		if isLikelyTextOpsModelCandidate(value) {
			return &value
		}
	}
	return nil
}

func normalizeTextOpsModelCandidate(raw string) string {
	value := strings.TrimSpace(raw)
	value = strings.Trim(value, "`'\"()[]{}，。,.；;：:")
	return strings.TrimSpace(value)
}

func isLikelyTextOpsModelCandidate(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 3 {
		return false
	}
	lower := strings.ToLower(value)
	skipExact := map[string]struct{}{
		"token_consumption": {},
		"cache_metrics":     {},
		"financial_status":  {},
	}
	if _, skip := skipExact[lower]; skip {
		return false
	}
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return false
	}
	if textOpsCycleIDPattern.MatchString(value) {
		return false
	}
	hasLetter := false
	hasDigit := false
	for _, ch := range value {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') {
			hasLetter = true
		}
		if ch >= '0' && ch <= '9' {
			hasDigit = true
		}
	}
	return hasLetter && hasDigit
}

// textOpsAllTimeStart is a wide-window sentinel that effectively means "all available
// history". 10 years is well beyond any realistic deployment of this service, so it
// covers every persisted record. The SQL layer treats this like any other time range
// (it doesn't need to know the difference).
const textOpsAllTimeStart = "2016-01-01T00:00:00Z"

// inferTextOpsAllTimeQuery reports whether the user is asking for an "all time" /
// "total ever" view (e.g. "总的 token 用量", "全部时间", "all time", "since the beginning").
// We require an explicit "all-time" cue AND no opposing narrow-time cue, otherwise
// "本周 Top 3" would incorrectly be treated as all-time.
func inferTextOpsAllTimeQuery(lowerQuery string) bool {
	if strings.Contains(lowerQuery, "全部时间") ||
		strings.Contains(lowerQuery, "所有时间") ||
		strings.Contains(lowerQuery, "自上线") ||
		strings.Contains(lowerQuery, "至今为止") ||
		strings.Contains(lowerQuery, "到目前为止") ||
		strings.Contains(lowerQuery, "all time") ||
		strings.Contains(lowerQuery, "all-time") ||
		strings.Contains(lowerQuery, "since launch") ||
		strings.Contains(lowerQuery, "since the beginning") ||
		strings.Contains(lowerQuery, "ever") {
		return true
	}
	// "总的 + 用量/消耗/请求" with no narrow time cue = all-time.
	hasTotalCue := strings.Contains(lowerQuery, "总的") ||
		strings.Contains(lowerQuery, "全部的") ||
		strings.Contains(lowerQuery, "总用量") ||
		strings.Contains(lowerQuery, "总消耗") ||
		strings.Contains(lowerQuery, "总请求数") ||
		strings.Contains(lowerQuery, "总调用")
	hasAggregateCue := strings.Contains(lowerQuery, "用量") ||
		strings.Contains(lowerQuery, "消耗") ||
		strings.Contains(lowerQuery, "花费") ||
		strings.Contains(lowerQuery, "调用") ||
		strings.Contains(lowerQuery, "请求")
	hasNarrowCue := strings.Contains(lowerQuery, "今天") ||
		strings.Contains(lowerQuery, "今日") ||
		strings.Contains(lowerQuery, "今天") ||
		strings.Contains(lowerQuery, "昨天") ||
		strings.Contains(lowerQuery, "本周") ||
		strings.Contains(lowerQuery, "这周") ||
		strings.Contains(lowerQuery, "上周") ||
		strings.Contains(lowerQuery, "本月") ||
		strings.Contains(lowerQuery, "这个月") ||
		strings.Contains(lowerQuery, "上个月") ||
		strings.Contains(lowerQuery, "本季度") ||
		strings.Contains(lowerQuery, "上季度") ||
		strings.Contains(lowerQuery, "今年") ||
		strings.Contains(lowerQuery, "去年") ||
		strings.Contains(lowerQuery, "近") ||
		strings.Contains(lowerQuery, "最近") ||
		strings.Contains(lowerQuery, "last ") ||
		strings.Contains(lowerQuery, "this ") ||
		strings.Contains(lowerQuery, "today") ||
		strings.Contains(lowerQuery, "yesterday")
	if hasTotalCue && hasAggregateCue && !hasNarrowCue {
		return true
	}
	_ = hasAggregateCue // keep variable for future expansion; signature stability.
	return false
}

func inferTextOpsTimeRange(query string, now time.Time) (time.Time, time.Time) {
	lower := strings.ToLower(strings.TrimSpace(query))
	if lower == "" {
		return now.Add(-24 * time.Hour), now
	}

	// Explicit numeric/cycle windows win over the "总" / "all time" cue, because a query
	// like "5/20-5/31 总的 token 请求数" wants the totals *within* the supplied range.
	if startAt, endAt, ok := inferTextOpsRangeFromCycleReference(lower); ok {
		return startAt, endAt
	}
	if startAt, endAt, ok := inferTextOpsExplicitDateRange(lower, now); ok {
		return startAt, endAt
	}
	if startAt, endAt, ok := inferTextOpsRangeFromRelativeNumber(lower, now); ok {
		return startAt, endAt
	}

	// All-time / total queries: skip every other inference and return the wide window.
	if inferTextOpsAllTimeQuery(lower) {
		allStart, _ := time.Parse(time.RFC3339, textOpsAllTimeStart)
		return allStart, now
	}

	startToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	currentWeekStart := startToday.AddDate(0, 0, -((int(startToday.Weekday()) + 6) % 7))
	currentMonthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	currentQuarterStart := startOfTextOpsQuarter(now)

	switch {
	case strings.Contains(lower, "上个财务周期") || strings.Contains(lower, "last financial cycle"):
		prevMonthStart := currentMonthStart.AddDate(0, -1, 0)
		return prevMonthStart, currentMonthStart
	case strings.Contains(lower, "上周") || strings.Contains(lower, "last week"):
		if weekdayDate, ok := inferTextOpsWeekdayDate(lower, currentWeekStart.AddDate(0, 0, -7)); ok {
			return weekdayDate, weekdayDate.Add(24 * time.Hour)
		}
		return currentWeekStart.AddDate(0, 0, -7), currentWeekStart
	case strings.Contains(lower, "本周") || strings.Contains(lower, "这周") || strings.Contains(lower, "this week"):
		if weekdayDate, ok := inferTextOpsWeekdayDate(lower, currentWeekStart); ok {
			return weekdayDate, weekdayDate.Add(24 * time.Hour)
		}
		return currentWeekStart, now
	case strings.Contains(lower, "上季度") || strings.Contains(lower, "last quarter"):
		prevQuarterStart := currentQuarterStart.AddDate(0, -3, 0)
		return prevQuarterStart, currentQuarterStart
	case strings.Contains(lower, "本季度") || strings.Contains(lower, "这季度") || strings.Contains(lower, "this quarter"):
		return currentQuarterStart, now
	case strings.Contains(lower, "上个月") || strings.Contains(lower, "last month"):
		prevMonthStart := currentMonthStart.AddDate(0, -1, 0)
		return prevMonthStart, currentMonthStart
	case strings.Contains(lower, "这个月") || strings.Contains(lower, "本月") || strings.Contains(lower, "this month"):
		return currentMonthStart, now
	case strings.Contains(lower, "去年") || strings.Contains(lower, "last year"):
		yearStart := time.Date(now.Year()-1, time.January, 1, 0, 0, 0, 0, time.UTC)
		thisYearStart := time.Date(now.Year(), time.January, 1, 0, 0, 0, 0, time.UTC)
		return yearStart, thisYearStart
	case strings.Contains(lower, "今年") || strings.Contains(lower, "this year"):
		thisYearStart := time.Date(now.Year(), time.January, 1, 0, 0, 0, 0, time.UTC)
		return thisYearStart, now
	case strings.Contains(lower, "近7天") || strings.Contains(lower, "最近7天") || strings.Contains(lower, "last 7 days"):
		return now.Add(-7 * 24 * time.Hour), now
	case strings.Contains(lower, "近30天") || strings.Contains(lower, "最近30天") || strings.Contains(lower, "last 30 days"):
		return now.Add(-30 * 24 * time.Hour), now
	case strings.Contains(lower, "近24小时") || strings.Contains(lower, "最近24小时") || strings.Contains(lower, "last 24 hours"):
		return now.Add(-24 * time.Hour), now
	case strings.Contains(lower, "一年") || strings.Contains(lower, "近一年"):
		return now.Add(-365 * 24 * time.Hour), now
	case strings.Contains(lower, "今天") || strings.Contains(lower, "今日") || strings.Contains(lower, "today"):
		return startToday, now
	case strings.Contains(lower, "昨天") || strings.Contains(lower, "yesterday"):
		yesterdayStart := startToday.Add(-24 * time.Hour)
		return yesterdayStart, startToday
	default:
		return now.Add(-24 * time.Hour), now
	}
}

func inferTextOpsRangeFromRelativeNumber(query string, now time.Time) (time.Time, time.Time, bool) {
	if m := textOpsLastNDays.FindStringSubmatch(query); len(m) > 1 {
		days, err := strconv.Atoi(strings.TrimSpace(m[1]))
		if err == nil && days > 0 && days <= 3660 {
			return now.Add(-time.Duration(days) * 24 * time.Hour), now, true
		}
	}
	if m := textOpsLastNHours.FindStringSubmatch(query); len(m) > 1 {
		hours, err := strconv.Atoi(strings.TrimSpace(m[1]))
		if err == nil && hours > 0 && hours <= 24*366 {
			return now.Add(-time.Duration(hours) * time.Hour), now, true
		}
	}
	return time.Time{}, time.Time{}, false
}

func inferTextOpsExplicitDateRange(query string, now time.Time) (time.Time, time.Time, bool) {
	if m := textOpsSlashDateRange.FindStringSubmatch(query); len(m) == 7 {
		startYear := parseTextOpsYear(m[1], now.Year())
		endYear := parseTextOpsYear(m[4], startYear)
		start, okStart := buildTextOpsDate(startYear, m[2], m[3])
		end, okEnd := buildTextOpsDate(endYear, m[5], m[6])
		if okStart && okEnd {
			if end.Before(start) {
				end = end.AddDate(1, 0, 0)
			}
			return start, end.AddDate(0, 0, 1), true
		}
	}
	if m := textOpsCNDateRange.FindStringSubmatch(query); len(m) == 7 {
		startYear := parseTextOpsYear(m[1], now.Year())
		endYear := parseTextOpsYear(m[4], startYear)
		start, okStart := buildTextOpsDate(startYear, m[2], m[3])
		end, okEnd := buildTextOpsDate(endYear, m[5], m[6])
		if okStart && okEnd {
			if end.Before(start) {
				end = end.AddDate(1, 0, 0)
			}
			return start, end.AddDate(0, 0, 1), true
		}
	}
	return time.Time{}, time.Time{}, false
}

func parseTextOpsYear(raw string, fallback int) int {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fallback
	}
	year, err := strconv.Atoi(trimmed)
	if err != nil || year <= 0 {
		return fallback
	}
	return year
}

func buildTextOpsDate(year int, rawMonth string, rawDay string) (time.Time, bool) {
	month, errMonth := strconv.Atoi(strings.TrimSpace(rawMonth))
	day, errDay := strconv.Atoi(strings.TrimSpace(rawDay))
	if errMonth != nil || errDay != nil || year <= 0 || month < 1 || month > 12 || day < 1 || day > 31 {
		return time.Time{}, false
	}
	date := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if date.Year() != year || int(date.Month()) != month || date.Day() != day {
		return time.Time{}, false
	}
	return date, true
}

func inferTextOpsRangeFromCycleReference(query string) (time.Time, time.Time, bool) {
	if m := textOpsCycleQuarter.FindStringSubmatch(query); len(m) > 2 {
		year, errYear := strconv.Atoi(strings.TrimSpace(m[1]))
		quarter, errQuarter := strconv.Atoi(strings.TrimSpace(m[2]))
		if errYear == nil && errQuarter == nil && quarter >= 1 && quarter <= 4 {
			start := time.Date(year, time.Month((quarter-1)*3+1), 1, 0, 0, 0, 0, time.UTC)
			return start, start.AddDate(0, 3, 0), true
		}
	}
	if m := textOpsCycleMonth.FindStringSubmatch(query); len(m) > 2 {
		year, errYear := strconv.Atoi(strings.TrimSpace(m[1]))
		month, errMonth := strconv.Atoi(strings.TrimSpace(m[2]))
		if errYear == nil && errMonth == nil && month >= 1 && month <= 12 {
			start := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
			return start, start.AddDate(0, 1, 0), true
		}
	}
	if m := textOpsCycleWeek.FindStringSubmatch(query); len(m) > 2 {
		year, errYear := strconv.Atoi(strings.TrimSpace(m[1]))
		week, errWeek := strconv.Atoi(strings.TrimSpace(m[2]))
		if errYear == nil && errWeek == nil && week >= 1 && week <= 53 {
			if start, ok := isoTextOpsWeekStart(year, week); ok {
				return start, start.AddDate(0, 0, 7), true
			}
		}
	}
	return time.Time{}, time.Time{}, false
}

func inferTextOpsWeekdayDate(query string, weekStart time.Time) (time.Time, bool) {
	offsets := []struct {
		tokens []string
		offset int
	}{
		{tokens: []string{"周一", "星期一", "monday"}, offset: 0},
		{tokens: []string{"周二", "星期二", "tuesday"}, offset: 1},
		{tokens: []string{"周三", "星期三", "wednesday"}, offset: 2},
		{tokens: []string{"周四", "星期四", "thursday"}, offset: 3},
		{tokens: []string{"周五", "星期五", "friday"}, offset: 4},
		{tokens: []string{"周六", "星期六", "saturday"}, offset: 5},
		{tokens: []string{"周日", "周天", "星期日", "星期天", "sunday"}, offset: 6},
	}
	for _, item := range offsets {
		for _, token := range item.tokens {
			if strings.Contains(query, token) {
				start := time.Date(weekStart.Year(), weekStart.Month(), weekStart.Day(), 0, 0, 0, 0, time.UTC)
				return start.AddDate(0, 0, item.offset), true
			}
		}
	}
	return time.Time{}, false
}

func startOfTextOpsQuarter(now time.Time) time.Time {
	month := int(now.Month())
	quarterStartMonth := ((month-1)/3)*3 + 1
	return time.Date(now.Year(), time.Month(quarterStartMonth), 1, 0, 0, 0, 0, time.UTC)
}

func isoTextOpsWeekStart(year, week int) (time.Time, bool) {
	if year <= 0 || week <= 0 || week > 53 {
		return time.Time{}, false
	}
	jan4 := time.Date(year, time.January, 4, 0, 0, 0, 0, time.UTC)
	weekday := int(jan4.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	week1Monday := jan4.AddDate(0, 0, -(weekday - 1))
	start := week1Monday.AddDate(0, 0, (week-1)*7)
	y, w := start.ISOWeek()
	if y != year || w != week {
		return time.Time{}, false
	}
	return start, true
}

func normalizeTextOpsIntent(intent string) string {
	value := strings.ToUpper(strings.TrimSpace(intent))
	switch value {
	case textOpsIntentTokenConsumption, textOpsIntentCacheMetrics, textOpsIntentFinancialStatus, textOpsIntentGeneralAssistant:
		return value
	default:
		return textOpsIntentTokenConsumption
	}
}

func normalizeTextOpsGroupBy(groupBy []string) []string {
	allowed := map[string]struct{}{
		"model_name":      {},
		"time_bucket_day": {},
		"user_id":         {},
	}
	result := make([]string, 0, len(groupBy))
	seen := make(map[string]struct{}, len(groupBy))
	for _, item := range groupBy {
		key := strings.ToLower(strings.TrimSpace(item))
		if _, ok := allowed[key]; !ok {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, key)
	}
	if len(result) == 0 {
		return []string{"time_bucket_day"}
	}
	return result
}

func normalizeTextOpsFilters(filters textOpsIntentSpecFilters, now time.Time) (textOpsFilters, textOpsResolvedWindow, error) {
	startAt := now.Add(-24 * time.Hour)
	endAt := now
	if value := strings.TrimSpace(filters.StartTime); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return textOpsFilters{}, textOpsResolvedWindow{}, fmt.Errorf("invalid filters.start_time: %w", err)
		}
		startAt = parsed.UTC()
	}
	if value := strings.TrimSpace(filters.EndTime); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return textOpsFilters{}, textOpsResolvedWindow{}, fmt.Errorf("invalid filters.end_time: %w", err)
		}
		endAt = parsed.UTC()
	}
	if endAt.Before(startAt) {
		return textOpsFilters{}, textOpsResolvedWindow{}, fmt.Errorf("filters.end_time must be >= filters.start_time")
	}

	out := textOpsFilters{
		StartTime: startAt.Format(time.RFC3339),
		EndTime:   endAt.Format(time.RFC3339),
		UserID:    filters.UserID,
	}
	if filters.ModelName != nil {
		trimmed := strings.TrimSpace(*filters.ModelName)
		if trimmed != "" {
			out.ModelName = &trimmed
		}
	}
	if filters.FinancialCycleID != nil {
		trimmed := strings.TrimSpace(*filters.FinancialCycleID)
		if trimmed != "" {
			out.FinancialCycleID = &trimmed
		}
	}
	if filters.Source != nil {
		trimmed := strings.TrimSpace(*filters.Source)
		if trimmed != "" {
			out.Source = &trimmed
		}
	}
	return out, textOpsResolvedWindow{Start: startAt, End: endAt}, nil
}

func applyTextOpsGuardrail(userQuery string, filters textOpsFilters, ctx textOpsOperatorContext) textOpsGuardrailResult {
	result := textOpsGuardrailResult{
		Blocked:          false,
		PromptInjection:  false,
		Rewritten:        false,
		EffectiveFilters: filters,
	}

	role := strings.ToLower(strings.TrimSpace(ctx.Role))
	if role == "" {
		role = textOpsRoleAdmin
	}
	result.AppliedRole = role

	switch role {
	case textOpsRoleCustomer:
		if ctx.UserID <= 0 {
			result.Blocked = true
			result.Reason = "customer role requires operator_context.user_id"
			return result
		}
		result.EffectiveFilters.UserID = &ctx.UserID
		result.Rewritten = true
		result.RewriteOperations = append(result.RewriteOperations, "forced filters.user_id to current customer user_id")
	case textOpsRoleReseller:
		allowedSet := make(map[int64]struct{}, len(ctx.AllowedUserIDs))
		for _, id := range ctx.AllowedUserIDs {
			allowedSet[id] = struct{}{}
		}
		if result.EffectiveFilters.UserID == nil {
			result.RewriteOperations = append(result.RewriteOperations, "reseller kept broad scope within downstream set")
			return result
		}
		if _, ok := allowedSet[*result.EffectiveFilters.UserID]; !ok {
			result.Blocked = true
			result.Reason = "requested user_id is outside reseller downstream scope"
			return result
		}
	case textOpsRoleAdmin:
		// no scope rewrite
	default:
		result.Blocked = true
		result.Reason = "unknown role"
	}

	return result
}

type textOpsExecutionSnapshot struct {
	Overview         aiOpsOverview
	ModelRows        []aiOpsModelMetric
	TokensByDay      map[string]int64
	InputByDay       map[string]int64
	CachedByDay      map[string]int64
	DailyMetrics     []aiOpsDailyMetric
	DailyModelSeries []aiOpsDailyModelMetric
	Network          aiOpsNetworkSummary
	Warnings         []string
}

func (h *Handler) executeTextOpsQuery(
	ctx context.Context,
	intent string,
	filters textOpsFilters,
	groupBy []string,
	window textOpsResolvedWindow,
	now time.Time,
) (textOpsDataResult, textOpsExecutionSnapshot) {
	if ctx == nil {
		ctx = context.Background()
	}
	if h != nil && h.textOpsStore != nil {
		dataFromStore, snapshotFromStore, errStore := h.textOpsStore.Query(ctx, intent, filters, groupBy, window, now)
		if errStore == nil {
			return sanitizeTextOpsDataResult(dataFromStore), sanitizeTextOpsExecutionSnapshot(snapshotFromStore)
		}
		dataFromSnapshot, snapshotFromSnapshot := h.executeTextOpsQueryFromSnapshot(
			intent,
			filters,
			groupBy,
			window,
			now,
			[]string{"postgres_analytics_failed: " + errStore.Error()},
		)
		return sanitizeTextOpsDataResult(dataFromSnapshot), sanitizeTextOpsExecutionSnapshot(snapshotFromSnapshot)
	}
	dataFromSnapshot, snapshotFromSnapshot := h.executeTextOpsQueryFromSnapshot(intent, filters, groupBy, window, now, nil)
	return sanitizeTextOpsDataResult(dataFromSnapshot), sanitizeTextOpsExecutionSnapshot(snapshotFromSnapshot)
}

func sanitizeTextOpsDataResult(data textOpsDataResult) textOpsDataResult {
	if data.Raw == nil {
		return data
	}
	raw := make(map[string]any, len(data.Raw))
	for key, value := range data.Raw {
		raw[key] = value
	}
	switch network := raw["network"].(type) {
	case aiOpsNetworkSummary:
		raw["network"] = sanitizeTextOpsNetworkSummary(network)
	case *aiOpsNetworkSummary:
		if network != nil {
			sanitized := sanitizeTextOpsNetworkSummary(*network)
			raw["network"] = sanitized
		}
	}
	data.Raw = raw
	return data
}

func sanitizeTextOpsExecutionSnapshot(snapshot textOpsExecutionSnapshot) textOpsExecutionSnapshot {
	snapshot.Network = sanitizeTextOpsNetworkSummary(snapshot.Network)
	return snapshot
}

func sanitizeTextOpsNetworkSummary(summary aiOpsNetworkSummary) aiOpsNetworkSummary {
	if len(summary.FailedBySource) > 0 {
		rows := make([]aiOpsNamedCount, len(summary.FailedBySource))
		copy(rows, summary.FailedBySource)
		for index := range rows {
			rows[index].Name = maskTextOpsSensitiveSource(rows[index].Name)
		}
		summary.FailedBySource = rows
	}
	if len(summary.FailureSamples) > 0 {
		samples := make([]aiOpsFailureSample, len(summary.FailureSamples))
		copy(samples, summary.FailureSamples)
		for index := range samples {
			samples[index].Source = maskTextOpsSensitiveSource(samples[index].Source)
		}
		summary.FailureSamples = samples
	}
	return summary
}

func maskTextOpsSensitiveSource(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "unknown"
	}
	if !isLikelySensitiveTextOpsSource(trimmed) {
		return trimmed
	}
	sum := sha1.Sum([]byte(trimmed))
	return "source:" + hex.EncodeToString(sum[:])[:12]
}

func isLikelySensitiveTextOpsSource(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	if lower == "" || lower == "unknown" {
		return false
	}
	if strings.Contains(lower, "@") {
		return true
	}
	sensitivePrefixes := []string{"sk-", "sk_", "sk-cp-", "fe_", "yls-", "nvapi-", "api-key-", "apikey-"}
	for _, prefix := range sensitivePrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	if len(lower) >= 32 {
		return true
	}
	return false
}

func (h *Handler) executeTextOpsQueryFromSnapshot(
	intent string,
	filters textOpsFilters,
	groupBy []string,
	window textOpsResolvedWindow,
	now time.Time,
	warnings []string,
) (textOpsDataResult, textOpsExecutionSnapshot) {
	aiReq := aiOpsQueryRequest{}
	if filters.ModelName != nil {
		aiReq.Model = *filters.ModelName
	}
	if filters.Source != nil {
		aiReq.Source = *filters.Source
	}
	rangeSpec := aiOpsRangeSpec{
		RangeKey: "custom",
		StartAt:  window.Start,
		EndAt:    window.End,
	}

	var snapshot usage.StatisticsSnapshot
	warnings = append([]string{}, warnings...)
	if h != nil && h.usageStats != nil {
		if _, errRestore := usage.RestoreStatisticsIfEmpty(h.usageStats); errRestore != nil {
			warnings = append(warnings, "restore_usage_failed: "+errRestore.Error())
		}
		snapshot = h.usageStats.Snapshot()
	} else {
		warnings = append(warnings, "usage statistics unavailable")
	}
	if filters.UserID != nil {
		authIndex := strconv.FormatInt(*filters.UserID, 10)
		snapshot = filterUsageSnapshotByAuthIndex(snapshot, authIndex)
		warnings = append(warnings, "user_id filter is enforced via auth_index mapping")
	}

	aiResult := h.buildAIOpsQueryResponse(snapshot, aiReq, rangeSpec)

	execSnapshot := textOpsExecutionSnapshot{
		Overview:         aiResult.Overview,
		ModelRows:        aiResult.Models,
		TokensByDay:      aiResult.TokensByDay,
		InputByDay:       aiResult.InputByDay,
		CachedByDay:      aiResult.CachedByDay,
		DailyMetrics:     aiResult.DailyMetrics,
		DailyModelSeries: aiResult.DailyByModel,
		Network:          aiResult.Network,
		Warnings:         warnings,
	}

	priceWarnings := make([]string, 0)
	costsByModel, totalCost := calculateTextOpsCosts(aiResult.Models, &priceWarnings)
	warnings = append(warnings, priceWarnings...)
	execSnapshot.Warnings = warnings

	summary := map[string]any{
		"start_time":       window.Start.Format(time.RFC3339),
		"end_time":         window.End.Format(time.RFC3339),
		"total_requests":   aiResult.Overview.TotalRequests,
		"success_requests": aiResult.Overview.SuccessRequests,
		"failed_requests":  aiResult.Overview.FailedRequests,
		"success_rate":     aiResult.Overview.SuccessRate,
		"total_tokens":     aiResult.Overview.TotalTokens,
		"input_tokens":     aiResult.Overview.InputTokens,
		"output_tokens":    aiResult.Overview.OutputTokens,
		"reasoning_tokens": aiResult.Overview.ReasoningTokens,
		"cached_tokens":    aiResult.Overview.CachedTokens,
		"cache_hit_rate":   aiResult.Overview.CacheHitRate,
		"total_cost":       roundTextOpsCost(totalCost),
		"group_by":         groupBy,
		"requested_intent": intent,
		"generated_at":     now.Format(time.RFC3339),
		"financial_cycle":  "",
		"financial_status": "",
	}
	if filters.ModelName != nil {
		summary["model_filter"] = strings.TrimSpace(*filters.ModelName)
	}
	if filters.Source != nil {
		summary["source_filter"] = strings.TrimSpace(*filters.Source)
	}
	if filters.FinancialCycleID != nil {
		summary["financial_cycle"] = *filters.FinancialCycleID
		summary["financial_status"] = inferFinancialCycleStatus(*filters.FinancialCycleID, window, now)
	}
	summary["data_source"] = "usage_snapshot"

	rows := make([]map[string]any, 0)
	switch intent {
	case textOpsIntentCacheMetrics:
		for _, item := range aiResult.Models {
			row := map[string]any{
				"model_name":      item.Model,
				"total_tokens":    item.TotalTokens,
				"total_requests":  item.TotalRequests,
				"cached_tokens":   item.CachedTokens,
				"input_tokens":    item.InputTokens,
				"cache_hit_rate":  item.CacheHitRate,
				"success_rate":    item.SuccessRate,
				"failed_requests": item.FailedRequests,
				"total_cost":      roundTextOpsCost(costsByModel[item.Model]),
			}
			rows = append(rows, row)
		}
	case textOpsIntentFinancialStatus:
		for _, item := range aiResult.Models {
			row := map[string]any{
				"model_name":      item.Model,
				"total_tokens":    item.TotalTokens,
				"total_requests":  item.TotalRequests,
				"success_rate":    item.SuccessRate,
				"cached_tokens":   item.CachedTokens,
				"cache_hit_rate":  item.CacheHitRate,
				"failed_requests": item.FailedRequests,
				"total_cost":      roundTextOpsCost(costsByModel[item.Model]),
			}
			rows = append(rows, row)
		}
	default:
		for _, item := range aiResult.Models {
			row := map[string]any{
				"model_name":       item.Model,
				"total_tokens":     item.TotalTokens,
				"input_tokens":     item.InputTokens,
				"output_tokens":    item.OutputTokens,
				"reasoning_tokens": item.ReasoningTokens,
				"cached_tokens":    item.CachedTokens,
				"cache_hit_rate":   item.CacheHitRate,
				"total_requests":   item.TotalRequests,
				"success_rate":     item.SuccessRate,
				"total_cost":       roundTextOpsCost(costsByModel[item.Model]),
			}
			rows = append(rows, row)
		}
	}

	raw := map[string]any{
		"tokens_by_day":        aiResult.TokensByDay,
		"input_tokens_by_day":  aiResult.InputByDay,
		"cached_tokens_by_day": aiResult.CachedByDay,
		"daily_metrics":        aiResult.DailyMetrics,
		"daily_model_series":   aiResult.DailyByModel,
		"network":              aiResult.Network,
		"registrations":        aiResult.Registrations,
	}
	return textOpsDataResult{
		Intent:  intent,
		Summary: summary,
		Rows:    rows,
		Raw:     raw,
	}, execSnapshot
}

func filterUsageSnapshotByAuthIndex(snapshot usage.StatisticsSnapshot, authIndex string) usage.StatisticsSnapshot {
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		return snapshot
	}
	result := usage.StatisticsSnapshot{
		APIs:           make(map[string]usage.APISnapshot),
		RequestsByDay:  make(map[string]int64),
		RequestsByHour: make(map[string]int64),
		TokensByDay:    make(map[string]int64),
		TokensByHour:   make(map[string]int64),
	}
	for apiName, apiSnapshot := range snapshot.APIs {
		filteredAPI := usage.APISnapshot{
			Models: make(map[string]usage.ModelSnapshot),
		}
		for modelName, modelSnapshot := range apiSnapshot.Models {
			filteredDetails := make([]usage.RequestDetail, 0, len(modelSnapshot.Details))
			modelTotalTokens := int64(0)
			modelTotalRequests := int64(0)
			for _, detail := range modelSnapshot.Details {
				if strings.TrimSpace(detail.AuthIndex) != authIndex {
					continue
				}
				filteredDetails = append(filteredDetails, detail)
				modelTotalRequests++
				modelTotalTokens += detail.Tokens.TotalTokens
				if detail.Failed {
					result.FailureCount++
				} else {
					result.SuccessCount++
				}
				result.TotalRequests++
				result.TotalTokens += detail.Tokens.TotalTokens
				dayKey := detail.Timestamp.UTC().Format("2006-01-02")
				result.RequestsByDay[dayKey]++
				result.TokensByDay[dayKey] += detail.Tokens.TotalTokens
				hourKey := detail.Timestamp.UTC().Format("15")
				result.RequestsByHour[hourKey]++
				result.TokensByHour[hourKey] += detail.Tokens.TotalTokens
			}
			if len(filteredDetails) == 0 {
				continue
			}
			filteredAPI.Models[modelName] = usage.ModelSnapshot{
				TotalRequests: modelTotalRequests,
				TotalTokens:   modelTotalTokens,
				Details:       filteredDetails,
			}
			filteredAPI.TotalRequests += modelTotalRequests
			filteredAPI.TotalTokens += modelTotalTokens
		}
		if len(filteredAPI.Models) == 0 {
			continue
		}
		result.APIs[apiName] = filteredAPI
	}
	return result
}

func inferFinancialCycleStatus(cycleID string, window textOpsResolvedWindow, now time.Time) string {
	cycle := strings.ToUpper(strings.TrimSpace(cycleID))
	if cycle == "" {
		return "unknown"
	}
	if window.End.Before(now.Add(-24 * time.Hour)) {
		return "closed"
	}
	return "open"
}

func buildTextOpsPresentation(
	data textOpsDataResult,
	filters textOpsFilters,
	groupBy []string,
	now time.Time,
) (string, []textOpsDisplayBlock) {
	modelName := "-"
	if filters.ModelName != nil && strings.TrimSpace(*filters.ModelName) != "" {
		modelName = strings.TrimSpace(*filters.ModelName)
	}
	mdLines := []string{
		fmt.Sprintf("### AI 工作台执行结果（%s）", data.Intent),
		fmt.Sprintf("- 时间范围：`%s` ~ `%s`", filters.StartTime, filters.EndTime),
		fmt.Sprintf("- 模型过滤：`%s`", modelName),
		fmt.Sprintf("- 请求总量：`%v`，成功率：`%v%%`", data.Summary["total_requests"], data.Summary["success_rate"]),
		fmt.Sprintf("- Token 总量：`%v`，缓存命中率：`%v%%`", data.Summary["total_tokens"], data.Summary["cache_hit_rate"]),
		fmt.Sprintf("- 总花费：`$%v`", defaultValue(data.Summary["total_cost"], "0")),
		"",
		"| Model | Requests | Tokens | Cost | Cache Hit % | Success % |",
		"| --- | ---: | ---: | ---: | ---: | ---: |",
	}
	for _, row := range data.Rows {
		mdLines = append(mdLines, fmt.Sprintf(
			"| %v | %v | %v | $%v | %v | %v |",
			row["model_name"],
			defaultValue(row["total_requests"], "-"),
			defaultValue(row["total_tokens"], "-"),
			defaultValue(row["total_cost"], "0"),
			defaultValue(row["cache_hit_rate"], "-"),
			defaultValue(row["success_rate"], "-"),
		))
	}
	markdown := strings.Join(mdLines, "\n")

	blocks := make([]textOpsDisplayBlock, 0)
	blocks = append(blocks, textOpsDisplayBlock{
		Type:  "KPI_CARDS",
		Title: "Metrics",
		Data: []map[string]any{
			{"key": "total_requests", "label": "Requests", "value": data.Summary["total_requests"], "format": "integer"},
			{"key": "success_rate", "label": "Success %", "value": data.Summary["success_rate"], "format": "percent", "status": rateStatus(data.Summary["success_rate"])},
			{"key": "total_tokens", "label": "Tokens", "value": data.Summary["total_tokens"], "format": "compact"},
			{"key": "total_cost", "label": "Cost", "value": data.Summary["total_cost"], "format": "currency"},
			{"key": "cache_hit_rate", "label": "Cache Hit %", "value": data.Summary["cache_hit_rate"], "format": "percent"},
			{"key": "cached_tokens", "label": "Cached Tokens", "value": data.Summary["cached_tokens"], "format": "compact"},
		},
	})
	blocks = append(blocks, textOpsDisplayBlock{
		Type:  "DATA_TABLE",
		Title: "Model Breakdown",
		Data: map[string]any{
			"columns": []map[string]any{
				{"key": "model_name", "label": "Model", "align": "left"},
				{"key": "total_requests", "label": "Requests", "align": "right", "format": "compact"},
				{"key": "total_tokens", "label": "Tokens", "align": "right", "format": "compact"},
				{"key": "total_cost", "label": "Cost", "align": "right", "format": "currency"},
				{"key": "cache_hit_rate", "label": "Cache Hit %", "align": "right", "format": "percent", "progress": true},
				{"key": "success_rate", "label": "Success %", "align": "right", "format": "percent", "warn_below": 90},
				{"key": "trend", "label": "Trend", "align": "center"},
			},
			"rows": data.Rows,
		},
	})

	option := buildTextOpsTrendOption(data, groupBy, now)
	if option != nil {
		blocks = append(blocks, textOpsDisplayBlock{
			Type:  "ECHARTS_OPTION",
			Title: "Token / Success / Cache Trend",
			Data:  option,
		})
	}
	if pieOption := buildTextOpsModelPieOption(data); pieOption != nil {
		blocks = append(blocks, textOpsDisplayBlock{
			Type:  "ECHARTS_OPTION",
			Title: "Model Token Share",
			Data:  pieOption,
		})
	}
	blocks = append(blocks, textOpsDisplayBlock{
		Type:  "MARKDOWN",
		Title: "System Summary",
		Data:  markdown,
	})

	return markdown, blocks
}

func rateStatus(value any) string {
	rate, ok := anyToFloat64(value)
	if !ok {
		return "neutral"
	}
	if rate < 90 {
		return "danger"
	}
	if rate < 98 {
		return "warning"
	}
	return "success"
}

func defaultValue(value any, fallback string) any {
	if value == nil {
		return fallback
	}
	return value
}

func calculateTextOpsCosts(models []aiOpsModelMetric, warnings *[]string) (map[string]float64, float64) {
	result := make(map[string]float64, len(models))
	total := 0.0
	missing := make(map[string]struct{})
	for _, item := range models {
		price, ok := lookupTextOpsModelPrice(item.Model)
		if !ok {
			if item.TotalTokens > 0 {
				missing[item.Model] = struct{}{}
			}
			continue
		}
		cost := calculateTextOpsModelCost(item, price)
		result[item.Model] = cost
		total += cost
	}
	if warnings != nil && len(missing) > 0 {
		names := make([]string, 0, len(missing))
		for name := range missing {
			names = append(names, name)
		}
		sort.Strings(names)
		*warnings = append(*warnings, "model_price_missing: "+strings.Join(names, ","))
	}
	return result, total
}

func lookupTextOpsModelPrice(modelName string) (ModelPrice, bool) {
	key := strings.TrimSpace(modelName)
	if key == "" {
		return ModelPrice{}, false
	}
	canonicalKey := canonicalTextOpsModelName(key)
	modelPricesMutex.RLock()
	defer modelPricesMutex.RUnlock()
	if price, ok := modelPricesData[key]; ok {
		return price, true
	}
	for model, price := range modelPricesData {
		if strings.EqualFold(strings.TrimSpace(model), key) {
			return price, true
		}
	}
	for model, price := range modelPricesData {
		if canonicalTextOpsModelName(model) == canonicalKey {
			return price, true
		}
	}
	return ModelPrice{}, false
}

func canonicalTextOpsModelName(modelName string) string {
	value := strings.ToLower(strings.TrimSpace(modelName))
	if value == "" {
		return ""
	}
	value = strings.Trim(value, "/")
	parts := strings.Split(value, "/")
	value = strings.TrimSpace(parts[len(parts)-1])
	value = strings.TrimPrefix(value, "models/")
	return value
}

func calculateTextOpsModelCost(item aiOpsModelMetric, price ModelPrice) float64 {
	inputTokens := maxInt64(item.InputTokens, 0)
	cachedTokens := maxInt64(item.CachedTokens, 0)
	promptTokens := maxInt64(inputTokens-cachedTokens, 0)
	outputTokens := maxInt64(item.OutputTokens+item.ReasoningTokens, 0)
	total := (float64(promptTokens)/textOpsTokensPerPriceUnit)*price.Input +
		(float64(cachedTokens)/textOpsTokensPerPriceUnit)*price.CachedInput +
		(float64(outputTokens)/textOpsTokensPerPriceUnit)*price.Output
	if total <= 0 {
		return 0
	}
	return total
}

func maxInt64(value int64, minValue int64) int64 {
	if value < minValue {
		return minValue
	}
	return value
}

func roundTextOpsCost(value float64) float64 {
	if value <= 0 {
		return 0
	}
	return float64(int64(value*1_000_000+0.5)) / 1_000_000
}

func buildTextOpsTrendOption(data textOpsDataResult, groupBy []string, _ time.Time) map[string]any {
	raw := data.Raw
	if raw == nil {
		return nil
	}
	dailyMetrics := normalizeTextOpsDailyMetrics(raw["daily_metrics"])
	if len(dailyMetrics) == 0 {
		tokensByDay, ok := raw["tokens_by_day"].(map[string]int64)
		if !ok || len(tokensByDay) == 0 {
			return nil
		}
		inputByDay, _ := raw["input_tokens_by_day"].(map[string]int64)
		cachedByDay, _ := raw["cached_tokens_by_day"].(map[string]int64)
		dailyMetrics = make([]aiOpsDailyMetric, 0, len(tokensByDay))
		for day, tokens := range tokensByDay {
			input := inputByDay[day]
			cached := cachedByDay[day]
			dailyMetrics = append(dailyMetrics, aiOpsDailyMetric{
				Date:         day,
				TotalTokens:  tokens,
				InputTokens:  input,
				CachedTokens: cached,
				CacheHitRate: roundAIOpsRate(ratioPercent(cached, input+cached)),
			})
		}
		sort.Slice(dailyMetrics, func(i, j int) bool {
			return dailyMetrics[i].Date < dailyMetrics[j].Date
		})
	}
	if len(dailyMetrics) == 0 {
		return nil
	}

	labels := make([]string, 0, len(dailyMetrics))
	values := make([]int64, 0, len(labels))
	successRates := make([]float64, 0, len(labels))
	cacheHitRates := make([]float64, 0, len(labels))
	for _, item := range dailyMetrics {
		labels = append(labels, item.Date)
		values = append(values, item.TotalTokens)
		successRates = append(successRates, item.SuccessRate)
		cacheHitRates = append(cacheHitRates, item.CacheHitRate)
	}

	chartType := "line"
	for _, item := range groupBy {
		if strings.EqualFold(item, "model_name") {
			chartType = "bar"
			break
		}
	}

	return map[string]any{
		"legend": map[string]any{
			"top": 0,
		},
		"grid": map[string]any{
			"left":   56,
			"right":  56,
			"top":    44,
			"bottom": 36,
		},
		"tooltip": map[string]any{
			"trigger": "axis",
		},
		"xAxis": map[string]any{
			"type": "category",
			"data": labels,
		},
		"yAxis": []map[string]any{
			{
				"type": "value",
				"name": "Tokens",
			},
			{
				"type":      "value",
				"name":      "%",
				"min":       0,
				"max":       100,
				"axisLabel": map[string]any{"formatter": "{value}%"},
			},
		},
		"series": []map[string]any{
			{
				"name":       "Token Consumption",
				"type":       chartType,
				"smooth":     chartType == "line",
				"showSymbol": false,
				"yAxisIndex": 0,
				"data":       values,
			},
			{
				"name":       "Success %",
				"type":       "line",
				"smooth":     true,
				"showSymbol": false,
				"yAxisIndex": 1,
				"data":       successRates,
			},
			{
				"name":       "Cache Hit %",
				"type":       "line",
				"smooth":     true,
				"showSymbol": false,
				"yAxisIndex": 1,
				"data":       cacheHitRates,
			},
		},
	}
}

func normalizeTextOpsDailyMetrics(raw any) []aiOpsDailyMetric {
	items, ok := raw.([]aiOpsDailyMetric)
	if ok {
		return items
	}
	generic, ok := raw.([]any)
	if !ok {
		return nil
	}
	result := make([]aiOpsDailyMetric, 0, len(generic))
	for _, item := range generic {
		row, ok := item.(map[string]any)
		if !ok {
			continue
		}
		totalTokens, _ := anyToInt64(row["total_tokens"])
		inputTokens, _ := anyToInt64(row["input_tokens"])
		cachedTokens, _ := anyToInt64(row["cached_tokens"])
		successRate, _ := anyToFloat64(row["success_rate"])
		cacheHitRate, _ := anyToFloat64(row["cache_hit_rate"])
		result = append(result, aiOpsDailyMetric{
			Date:         fmt.Sprint(row["date"]),
			TotalTokens:  totalTokens,
			InputTokens:  inputTokens,
			CachedTokens: cachedTokens,
			SuccessRate:  successRate,
			CacheHitRate: cacheHitRate,
		})
	}
	return result
}

func buildTextOpsModelPieOption(data textOpsDataResult) map[string]any {
	if len(data.Rows) == 0 {
		return nil
	}
	pieData := make([]map[string]any, 0, len(data.Rows))
	for _, row := range data.Rows {
		tokens, ok := anyToInt64(row["total_tokens"])
		if !ok || tokens <= 0 {
			continue
		}
		pieData = append(pieData, map[string]any{
			"name":  fmt.Sprint(row["model_name"]),
			"value": tokens,
		})
	}
	if len(pieData) == 0 {
		return nil
	}
	return map[string]any{
		"tooltip": map[string]any{
			"trigger":   "item",
			"formatter": "{b}: {c} ({d}%)",
		},
		"legend": map[string]any{
			"type":   "scroll",
			"bottom": 0,
		},
		"series": []map[string]any{
			{
				"name":   "Token Share",
				"type":   "pie",
				"radius": []string{"42%", "70%"},
				"center": []string{"50%", "45%"},
				"data":   pieData,
			},
		},
	}
}

func anyToInt64(value any) (int64, bool) {
	switch v := value.(type) {
	case int:
		return int64(v), true
	case int64:
		return v, true
	case float64:
		return int64(v), true
	case json.Number:
		parsed, err := v.Int64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func anyToFloat64(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		parsed, err := v.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func (h *Handler) enhanceTextOpsPresentation(ctx context.Context, cfg textOpsLLMConfig, payload textOpsQueryResponse) (textOpsPresentation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	input, errMarshal := json.Marshal(map[string]any{
		"user_query": payload.UserQuery,
		"router":     payload.Router,
		"guardrail":  payload.Guardrail,
		"data":       payload.Data,
		"warnings":   payload.Warnings,
	})
	if errMarshal != nil {
		return textOpsPresentation{}, fmt.Errorf("marshal data for presenter failed: %w", errMarshal)
	}
	systemPrompt := strings.Join([]string{
		"You are a data presenter for an AI Ops console.",
		"Return concise Chinese Markdown only.",
		"Focus on 1) key result 2) risks 3) next actions.",
		"Use router.filters and guardrail.effective_filters when explaining scoped results.",
		"If total_requests is 0 with a model_name filter, say no records matched that model and time range; do not claim the whole account has no API calls.",
		"Do not return JSON.",
		"Do not include markdown code fences.",
		"Do not include <think>, reasoning, analysis, or chain-of-thought.",
	}, "\n")
	userPrompt := "Context JSON:\n" + string(input)

	req := textOpsLLMChatRequest{
		Model: cfg.Model,
		Messages: []aiOpsAICallRequestMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		MaxTokens:       cfg.MaxTokens,
		Temperature:     0,
		ReasoningEffort: resolveManagementLLMReasoningEffort(cfg.Model),
	}
	markdown, err := h.callTextOpsLLM(ctx, cfg, req)
	if err != nil {
		return textOpsPresentation{}, err
	}
	markdown = stripTextOpsThinkBlocks(markdown)
	if markdown == "" {
		return textOpsPresentation{}, fmt.Errorf("presenter markdown is empty")
	}

	presentation := textOpsPresentation{
		Markdown: markdown,
		Blocks: []textOpsDisplayBlock{
			{
				Type:  "MARKDOWN",
				Title: "AI Summary",
				Data:  markdown,
			},
		},
	}
	for _, block := range payload.Presentation.Blocks {
		if strings.EqualFold(block.Type, "MARKDOWN") {
			continue
		}
		presentation.Blocks = append(presentation.Blocks, block)
	}
	return presentation, nil
}

func stripTextOpsThinkBlocks(raw string) string {
	text := strings.TrimSpace(raw)
	for {
		lower := strings.ToLower(text)
		start := strings.Index(lower, "<think>")
		if start < 0 {
			if endOnly := strings.Index(lower, "</think>"); endOnly >= 0 {
				text = strings.TrimSpace(text[endOnly+len("</think>"):])
				continue
			}
			break
		}
		end := strings.Index(lower[start+len("<think>"):], "</think>")
		if end < 0 {
			return strings.TrimSpace(text[:start])
		}
		end = start + len("<think>") + end + len("</think>")
		text = strings.TrimSpace(text[:start] + text[end:])
	}
	return strings.TrimSpace(text)
}

func resolveManagementLLMReasoningEffort(model string) string {
	lowerModel := strings.ToLower(model)
	if strings.Contains(lowerModel, "minimax-m3") {
		return "none"
	}
	return ""
}

func (h *Handler) callTextOpsLLM(ctx context.Context, cfg textOpsLLMConfig, body textOpsLLMChatRequest) (string, error) {
	data, errMarshal := json.Marshal(body)
	if errMarshal != nil {
		return "", fmt.Errorf("marshal llm request failed: %w", errMarshal)
	}
	endpoint := strings.TrimRight(cfg.BaseURL, "/") + "/chat/completions"
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if errRequest != nil {
		return "", fmt.Errorf("build llm request failed: %w", errRequest)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)

	client := &http.Client{
		Transport: h.apiCallTransport(nil),
	}
	resp, errDo := client.Do(req)
	if errDo != nil {
		return "", fmt.Errorf("llm request failed: %w", errDo)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	payload, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return "", fmt.Errorf("read llm response failed: %w", errRead)
	}

	var parsed aiOpsAICallResponse
	if errUnmarshal := json.Unmarshal(payload, &parsed); errUnmarshal != nil {
		return "", fmt.Errorf("parse llm response failed: %w", errUnmarshal)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		msg := strings.TrimSpace(extractAIOpsErrorMessage(parsed, string(payload)))
		if msg == "" {
			msg = "unknown llm upstream error"
		}
		return "", fmt.Errorf("%s", msg)
	}
	if parsed.Error != nil {
		msg := strings.TrimSpace(parsed.Error.Message)
		if msg == "" {
			msg = "llm upstream returned error"
		}
		return "", fmt.Errorf("%s", msg)
	}
	content := strings.TrimSpace(extractAIOpsMessageContent(parsed))
	if content == "" {
		finishReason := ""
		if len(parsed.Choices) > 0 {
			finishReason = strings.TrimSpace(parsed.Choices[0].FinishReason)
		}
		if finishReason != "" {
			return "", fmt.Errorf("empty llm response content, finish_reason=%s", finishReason)
		}
		return "", fmt.Errorf("empty llm response content")
	}
	return content, nil
}
