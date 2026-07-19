package management

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	textOpsQueryKindAnalytics = "analytics"
	textOpsQueryKindGeneral   = "general"
	textOpsQueryKindNews      = "news"
	textOpsQueryKindWeather   = "weather"

	textOpsGeneralMaxTokens     = 800
	textOpsGeneralFallbackModel = "deepseek-v4-flash"
	textOpsWeatherCacheTTL      = 5 * time.Minute
	textOpsWeatherBodyLimit     = 1 << 20
)

var (
	textOpsWeatherGeocodingEndpoint = "https://geocoding-api.open-meteo.com/v1/search"
	textOpsWeatherForecastEndpoint  = "https://api.open-meteo.com/v1/forecast"
	textOpsWeatherEnglishLocation   = regexp.MustCompile(`(?i)\b(?:weather|forecast|temperature)\s+(?:in|for)\s+([a-z][a-z .'-]{1,50})`)
	textOpsWeatherCache             = struct {
		sync.RWMutex
		entries map[string]textOpsWeatherCacheEntry
	}{entries: make(map[string]textOpsWeatherCacheEntry)}
)

type textOpsWeatherCacheEntry struct {
	result    textOpsWeatherResult
	expiresAt time.Time
}

type textOpsWeatherResult struct {
	Location                 string
	Country                  string
	AdminArea                string
	Timezone                 string
	ObservedAt               string
	Condition                string
	Temperature              float64
	ApparentTemperature      float64
	RelativeHumidity         float64
	Precipitation            float64
	WindSpeed                float64
	MinimumTemperature       float64
	MaximumTemperature       float64
	PrecipitationProbability float64
}

type textOpsWeatherGeocodingResponse struct {
	Results []struct {
		Name      string  `json:"name"`
		Country   string  `json:"country"`
		AdminArea string  `json:"admin1"`
		Timezone  string  `json:"timezone"`
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
	} `json:"results"`
}

type textOpsWeatherForecastResponse struct {
	Timezone string `json:"timezone"`
	Current  struct {
		Time                string  `json:"time"`
		Temperature         float64 `json:"temperature_2m"`
		ApparentTemperature float64 `json:"apparent_temperature"`
		RelativeHumidity    float64 `json:"relative_humidity_2m"`
		Precipitation       float64 `json:"precipitation"`
		WeatherCode         int     `json:"weather_code"`
		WindSpeed           float64 `json:"wind_speed_10m"`
	} `json:"current"`
	Daily struct {
		MinimumTemperature       []float64 `json:"temperature_2m_min"`
		MaximumTemperature       []float64 `json:"temperature_2m_max"`
		PrecipitationProbability []float64 `json:"precipitation_probability_max"`
		WeatherCode              []int     `json:"weather_code"`
	} `json:"daily"`
}

func classifyTextOpsQueryKind(userQuery string) string {
	if isTextOpsWeatherQuery(userQuery) {
		return textOpsQueryKindWeather
	}
	if isTextOpsNewsQuery(userQuery) {
		return textOpsQueryKindNews
	}

	normalized := strings.ToLower(strings.TrimSpace(userQuery))
	if normalized == "" {
		return textOpsQueryKindGeneral
	}

	explanationCues := []string{"什么是", "是什么意思", "介绍一下", "解释一下", "what is", "explain "}
	for _, cue := range explanationCues {
		if strings.Contains(normalized, cue) {
			return textOpsQueryKindGeneral
		}
	}

	analyticsCues := []string{
		"token", "缓存", "cache", "财务", "对账", "结算", "账单", "花费", "费用", "成本",
		"模型", "model", "调用", "请求", "request", "流量", "traffic", "命中率", "成功率",
		"失败率", "延迟", "latency", "注册", "registration", "网络", "network", "用量",
		"消耗", "usage", "spend", "billing", "api key", "apikey",
	}
	for _, cue := range analyticsCues {
		if strings.Contains(normalized, cue) {
			return textOpsQueryKindAnalytics
		}
	}
	return textOpsQueryKindGeneral
}

func isTextOpsWeatherQuery(userQuery string) bool {
	normalized := strings.ToLower(strings.TrimSpace(userQuery))
	weatherCues := []string{"天气", "气温", "温度", "下雨", "降雨", "weather", "forecast", "temperature"}
	for _, cue := range weatherCues {
		if strings.Contains(normalized, cue) {
			return true
		}
	}
	return false
}

func extractTextOpsWeatherLocation(userQuery string) string {
	trimmed := strings.TrimSpace(userQuery)
	if match := textOpsWeatherEnglishLocation.FindStringSubmatch(trimmed); len(match) > 1 {
		location := strings.TrimSpace(match[1])
		lowerLocation := strings.ToLower(location)
		for _, suffix := range []string{" today", " tomorrow", " now", " please"} {
			lowerLocation = strings.TrimSuffix(lowerLocation, suffix)
			location = strings.TrimSpace(location[:len(lowerLocation)])
		}
		if isUsableTextOpsWeatherLocation(location) {
			return location
		}
	}

	replacer := strings.NewReplacer(
		"会不会下雨", "", "是否会下雨", "", "今天会下雨吗", "", "明天会下雨吗", "",
		"天气预报", "", "今天", "", "今日", "", "现在", "", "当前", "", "明天", "", "后天", "",
		"天气", "", "气温", "", "温度", "", "下雨", "", "降雨", "", "怎么样", "", "如何", "",
		"什么", "", "请问", "", "帮我查", "", "查询", "", "查一下", "", "看一下", "", "告诉我", "",
		"会不会", "", "是否", "", "的", "", "呢", "", "吗", "",
	)
	location := replacer.Replace(trimmed)
	location = strings.TrimSpace(strings.Trim(location, "，。！？,.!?;；:：()（）[]【】"))
	if isUsableTextOpsWeatherLocation(location) {
		return location
	}
	return ""
}

func isUsableTextOpsWeatherLocation(location string) bool {
	location = strings.TrimSpace(location)
	if utf8.RuneCountInString(location) < 2 || utf8.RuneCountInString(location) > 50 {
		return false
	}
	switch strings.ToLower(location) {
	case "这里", "本地", "当地", "我这里", "当前位置", "所在城市", "local", "here":
		return false
	default:
		return true
	}
}

func (h *Handler) executeTextOpsGeneralQuery(
	ctx context.Context,
	req textOpsQueryRequest,
	currentTime time.Time,
	response textOpsQueryResponse,
	queryKind string,
) textOpsQueryResponse {
	filters := textOpsFilters{
		StartTime: currentTime.Format(time.RFC3339),
		EndTime:   currentTime.Format(time.RFC3339),
	}
	response.Router = textOpsRouterResult{
		Intent:  textOpsIntentGeneralAssistant,
		Filters: filters,
		GroupBy: []string{},
		Route:   "general_local",
	}
	response.Guardrail = applyTextOpsGuardrail(req.UserQuery, filters, req.OperatorContext)
	if response.Guardrail.Blocked {
		return response
	}
	response.Router.Filters = response.Guardrail.EffectiveFilters

	var markdown string
	summary := map[string]any{
		"response_kind": queryKind,
		"generated_at":  currentTime.Format(time.RFC3339),
	}

	switch queryKind {
	case textOpsQueryKindWeather:
		location := extractTextOpsWeatherLocation(req.UserQuery)
		if location == "" {
			response.Router.Route = "weather_clarification"
			summary["needs_clarification"] = true
			summary["required_field"] = "location"
			markdown = "请告诉我需要查询的城市或地区，例如：`上海今天什么天气`。天气属于实时数据，必须先确定位置。"
		} else {
			weather, errWeather := h.fetchTextOpsWeather(ctx, location)
			if errWeather != nil {
				response.Router.Route = "weather_tool_failed"
				response.Warnings = append(response.Warnings, "weather_tool_failed: "+errWeather.Error())
				summary["location"] = location
				summary["data_source"] = "open_meteo"
				markdown = fmt.Sprintf("暂时无法获取 `%s` 的实时天气，请稍后重试。为避免误导，本次没有使用模型猜测天气。", location)
			} else {
				response.Router.Route = "weather_tool"
				summary = buildTextOpsWeatherSummary(weather, currentTime)
				markdown = buildTextOpsWeatherMarkdown(weather)
			}
		}
	case textOpsQueryKindNews:
		topic := extractTextOpsNewsTopic(req.UserQuery)
		news, errNews := h.fetchTextOpsNews(ctx, topic, currentTime)
		if errNews != nil {
			response.Router.Route = "news_tool_failed"
			response.Warnings = append(response.Warnings, "news_tool_failed: "+errNews.Error())
			summary["topic"] = topic
			summary["data_source"] = "news_rss"
			markdown = fmt.Sprintf("暂时无法获取 `%s` 的实时新闻，请稍后重试。为避免误导，本次没有使用模型编造新闻。", topic)
		} else {
			response.Router.Route = "news_tool"
			summary = buildTextOpsNewsSummary(news, currentTime)
			markdown = buildTextOpsNewsMarkdown(news, currentTime)
		}
	default:
		cfg := resolveTextOpsGeneralLLMConfig(req, h)
		if !cfg.Enabled {
			response.Router.Route = "general_unavailable"
			summary["needs_llm"] = true
			markdown = "这是一个通用问答，但当前没有可用的 CPA AI 模型配置。请在 AI Copilot 配置中启用模型后重试。"
		} else {
			answer, usedModel, fallbackUsed, answerWarnings, errAnswer := h.answerTextOpsGeneralQueryWithFallback(
				ctx,
				cfg,
				req.UserQuery,
				currentTime,
			)
			response.Warnings = append(response.Warnings, answerWarnings...)
			if errAnswer != nil {
				response.Router.Route = "general_llm_failed"
				response.Warnings = append(response.Warnings, "general_llm_failed: "+errAnswer.Error())
				markdown = "通用模型暂时没有返回可用结果，请稍后重试。"
			} else {
				response.Router.Route = "general_llm"
				if fallbackUsed {
					response.Router.Route = "general_llm_fallback"
					summary["primary_model"] = cfg.Model
					summary["fallback_reason"] = "latency_or_primary_failure"
				}
				summary["model"] = usedModel
				markdown = answer
			}
		}
	}

	response.Data = textOpsDataResult{
		Intent:  textOpsIntentGeneralAssistant,
		Summary: summary,
		Rows:    []map[string]any{},
	}
	response.Presentation = textOpsPresentation{
		Markdown: markdown,
		Blocks: []textOpsDisplayBlock{
			{Type: "MARKDOWN", Title: "Assistant", Data: markdown},
		},
	}
	return response
}

func resolveTextOpsGeneralLLMConfig(req textOpsQueryRequest, h *Handler) textOpsLLMConfig {
	cfg := req.Presenter
	if !cfg.Enabled {
		cfg = req.Router
	}
	cfg = resolveTextOpsLLMConfig(cfg, h, textOpsDefaultPresenterModel, textOpsGeneralMaxTokens)
	if cfg.MaxTokens > textOpsGeneralMaxTokens {
		cfg.MaxTokens = textOpsGeneralMaxTokens
	}
	return cfg
}

type textOpsGeneralAnswerResult struct {
	answer string
	model  string
	err    error
}

func (h *Handler) answerTextOpsGeneralQueryWithFallback(
	ctx context.Context,
	primaryCfg textOpsLLMConfig,
	userQuery string,
	currentTime time.Time,
) (string, string, bool, []string, error) {
	if strings.EqualFold(primaryCfg.Model, textOpsGeneralFallbackModel) {
		answer, err := h.answerTextOpsGeneralQuery(ctx, primaryCfg, userQuery, currentTime)
		return answer, primaryCfg.Model, false, nil, err
	}

	fallbackCfg := primaryCfg
	fallbackCfg.Model = textOpsGeneralFallbackModel
	fallbackCfg.MaxTokens = textOpsGeneralMaxTokens

	if shouldHedgeTextOpsGeneralModel(primaryCfg.Model) {
		hedgeCtx, cancel := context.WithCancel(ctx)
		defer cancel()

		results := make(chan textOpsGeneralAnswerResult, 2)
		run := func(cfg textOpsLLMConfig) {
			answer, err := h.answerTextOpsGeneralQuery(hedgeCtx, cfg, userQuery, currentTime)
			results <- textOpsGeneralAnswerResult{answer: answer, model: cfg.Model, err: err}
		}
		go run(primaryCfg)
		go run(fallbackCfg)

		errorsByModel := make(map[string]error, 2)
		for range 2 {
			result := <-results
			if result.err == nil {
				cancel()
				return result.answer, result.model, !strings.EqualFold(result.model, primaryCfg.Model), nil, nil
			}
			errorsByModel[result.model] = result.err
		}
		return "", "", false, buildTextOpsGeneralFailureWarnings(primaryCfg.Model, errorsByModel), fmt.Errorf(
			"primary and fallback models failed",
		)
	}

	answer, errPrimary := h.answerTextOpsGeneralQuery(ctx, primaryCfg, userQuery, currentTime)
	if errPrimary == nil {
		return answer, primaryCfg.Model, false, nil, nil
	}
	answer, errFallback := h.answerTextOpsGeneralQuery(ctx, fallbackCfg, userQuery, currentTime)
	if errFallback == nil {
		return answer, fallbackCfg.Model, true, []string{"general_primary_failed: " + errPrimary.Error()}, nil
	}
	return "", "", false, []string{
		"general_primary_failed: " + errPrimary.Error(),
		"general_fallback_failed: " + errFallback.Error(),
	}, fmt.Errorf("primary and fallback models failed")
}

func shouldHedgeTextOpsGeneralModel(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	return strings.Contains(normalized, "minimax-m3") || strings.Contains(normalized, "minimax-m2.7")
}

func buildTextOpsGeneralFailureWarnings(primaryModel string, errorsByModel map[string]error) []string {
	warnings := make([]string, 0, 2)
	if errPrimary := errorsByModel[primaryModel]; errPrimary != nil {
		warnings = append(warnings, "general_primary_failed: "+errPrimary.Error())
	}
	if errFallback := errorsByModel[textOpsGeneralFallbackModel]; errFallback != nil {
		warnings = append(warnings, "general_fallback_failed: "+errFallback.Error())
	}
	return warnings
}

func (h *Handler) answerTextOpsGeneralQuery(
	ctx context.Context,
	cfg textOpsLLMConfig,
	userQuery string,
	currentTime time.Time,
) (string, error) {
	systemPrompt := strings.Join([]string{
		"You are the general assistant inside an AI operations workspace.",
		"Answer in concise Chinese unless the user requests another language.",
		"Current time is " + currentTime.Format(time.RFC3339) + ".",
		"Do not invent real-time weather, news, prices, or other live data.",
		"If live data is required and no tool result is available, state the limitation clearly.",
		"Do not include <think>, reasoning, analysis, or chain-of-thought.",
	}, "\n")
	request := textOpsLLMChatRequest{
		Model: cfg.Model,
		Messages: []aiOpsAICallRequestMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: strings.TrimSpace(userQuery)},
		},
		MaxTokens:       cfg.MaxTokens,
		Temperature:     0.2,
		ReasoningEffort: resolveManagementLLMReasoningEffort(cfg.Model),
	}
	answer, err := h.callTextOpsLLM(ctx, cfg, request)
	if err != nil {
		return "", err
	}
	answer = stripTextOpsThinkBlocks(answer)
	if answer == "" {
		return "", fmt.Errorf("general assistant response is empty")
	}
	return answer, nil
}

func (h *Handler) fetchTextOpsWeather(ctx context.Context, location string) (textOpsWeatherResult, error) {
	cacheKey := strings.ToLower(strings.TrimSpace(location))
	if cached, ok := getCachedTextOpsWeather(cacheKey, time.Now()); ok {
		return cached, nil
	}

	transport := http.DefaultTransport
	if h != nil {
		transport = h.apiCallTransport(nil)
	}
	client := &http.Client{Transport: transport}

	geocodingURL, errURL := url.Parse(textOpsWeatherGeocodingEndpoint)
	if errURL != nil {
		return textOpsWeatherResult{}, fmt.Errorf("invalid weather geocoding endpoint: %w", errURL)
	}
	query := geocodingURL.Query()
	query.Set("name", location)
	query.Set("count", "1")
	query.Set("language", "zh")
	query.Set("format", "json")
	geocodingURL.RawQuery = query.Encode()

	var geocoding textOpsWeatherGeocodingResponse
	if errFetch := fetchTextOpsJSON(ctx, client, geocodingURL.String(), &geocoding); errFetch != nil {
		return textOpsWeatherResult{}, fmt.Errorf("geocoding failed: %w", errFetch)
	}
	if len(geocoding.Results) == 0 {
		return textOpsWeatherResult{}, fmt.Errorf("location not found")
	}
	resolved := geocoding.Results[0]

	forecastURL, errForecastURL := url.Parse(textOpsWeatherForecastEndpoint)
	if errForecastURL != nil {
		return textOpsWeatherResult{}, fmt.Errorf("invalid weather forecast endpoint: %w", errForecastURL)
	}
	forecastQuery := forecastURL.Query()
	forecastQuery.Set("latitude", fmt.Sprintf("%.6f", resolved.Latitude))
	forecastQuery.Set("longitude", fmt.Sprintf("%.6f", resolved.Longitude))
	forecastQuery.Set("current", "temperature_2m,apparent_temperature,relative_humidity_2m,precipitation,weather_code,wind_speed_10m")
	forecastQuery.Set("daily", "temperature_2m_max,temperature_2m_min,precipitation_probability_max,weather_code")
	forecastQuery.Set("forecast_days", "1")
	forecastQuery.Set("timezone", "auto")
	forecastURL.RawQuery = forecastQuery.Encode()

	var forecast textOpsWeatherForecastResponse
	if errFetch := fetchTextOpsJSON(ctx, client, forecastURL.String(), &forecast); errFetch != nil {
		return textOpsWeatherResult{}, fmt.Errorf("forecast failed: %w", errFetch)
	}

	result := textOpsWeatherResult{
		Location:            resolved.Name,
		Country:             resolved.Country,
		AdminArea:           resolved.AdminArea,
		Timezone:            firstNonEmptyTextOpsValue(forecast.Timezone, resolved.Timezone),
		ObservedAt:          forecast.Current.Time,
		Condition:           describeTextOpsWeatherCode(forecast.Current.WeatherCode),
		Temperature:         forecast.Current.Temperature,
		ApparentTemperature: forecast.Current.ApparentTemperature,
		RelativeHumidity:    forecast.Current.RelativeHumidity,
		Precipitation:       forecast.Current.Precipitation,
		WindSpeed:           forecast.Current.WindSpeed,
	}
	if len(forecast.Daily.MinimumTemperature) > 0 {
		result.MinimumTemperature = forecast.Daily.MinimumTemperature[0]
	}
	if len(forecast.Daily.MaximumTemperature) > 0 {
		result.MaximumTemperature = forecast.Daily.MaximumTemperature[0]
	}
	if len(forecast.Daily.PrecipitationProbability) > 0 {
		result.PrecipitationProbability = forecast.Daily.PrecipitationProbability[0]
	}
	if result.Condition == "未知" && len(forecast.Daily.WeatherCode) > 0 {
		result.Condition = describeTextOpsWeatherCode(forecast.Daily.WeatherCode[0])
	}
	cacheTextOpsWeather(cacheKey, result, time.Now().Add(textOpsWeatherCacheTTL))
	return result, nil
}

func fetchTextOpsJSON(ctx context.Context, client *http.Client, endpoint string, target any) error {
	request, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if errRequest != nil {
		return errRequest
	}
	request.Header.Set("Accept", "application/json")

	response, errDo := client.Do(request)
	if errDo != nil {
		return errDo
	}
	defer func() {
		_ = response.Body.Close()
	}()

	payload, errRead := io.ReadAll(io.LimitReader(response.Body, textOpsWeatherBodyLimit))
	if errRead != nil {
		return errRead
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("upstream status %d", response.StatusCode)
	}
	if errDecode := json.Unmarshal(payload, target); errDecode != nil {
		return errDecode
	}
	return nil
}

func buildTextOpsWeatherSummary(weather textOpsWeatherResult, currentTime time.Time) map[string]any {
	return map[string]any{
		"response_kind":             textOpsQueryKindWeather,
		"data_source":               "open_meteo",
		"location":                  weather.Location,
		"country":                   weather.Country,
		"admin_area":                weather.AdminArea,
		"timezone":                  weather.Timezone,
		"observed_at":               weather.ObservedAt,
		"condition":                 weather.Condition,
		"temperature_c":             weather.Temperature,
		"apparent_temperature_c":    weather.ApparentTemperature,
		"relative_humidity_percent": weather.RelativeHumidity,
		"precipitation_mm":          weather.Precipitation,
		"wind_speed_kmh":            weather.WindSpeed,
		"minimum_temperature_c":     weather.MinimumTemperature,
		"maximum_temperature_c":     weather.MaximumTemperature,
		"precipitation_probability": weather.PrecipitationProbability,
		"generated_at":              currentTime.Format(time.RFC3339),
	}
}

func buildTextOpsWeatherMarkdown(weather textOpsWeatherResult) string {
	locationParts := []string{weather.Location}
	if weather.AdminArea != "" && !strings.EqualFold(weather.AdminArea, weather.Location) {
		locationParts = append(locationParts, weather.AdminArea)
	}
	if weather.Country != "" {
		locationParts = append(locationParts, weather.Country)
	}
	return fmt.Sprintf(
		"### %s今天天气\n\n- 当前：**%s，%.1f°C**，体感 %.1f°C\n- 今日：最低 %.1f°C，最高 %.1f°C\n- 降水：%.1f mm，最高降水概率 %.0f%%\n- 湿度：%.0f%%，风速 %.1f km/h\n- 数据时间：`%s`（%s）\n\n数据源：Open-Meteo",
		strings.Join(locationParts, " / "),
		weather.Condition,
		weather.Temperature,
		weather.ApparentTemperature,
		weather.MinimumTemperature,
		weather.MaximumTemperature,
		weather.Precipitation,
		weather.PrecipitationProbability,
		weather.RelativeHumidity,
		weather.WindSpeed,
		weather.ObservedAt,
		weather.Timezone,
	)
}

func describeTextOpsWeatherCode(code int) string {
	switch {
	case code == 0:
		return "晴"
	case code >= 1 && code <= 3:
		return "多云"
	case code == 45 || code == 48:
		return "雾"
	case code >= 51 && code <= 57:
		return "毛毛雨"
	case code >= 61 && code <= 67:
		return "雨"
	case code >= 71 && code <= 77:
		return "雪"
	case code >= 80 && code <= 82:
		return "阵雨"
	case code == 85 || code == 86:
		return "阵雪"
	case code >= 95 && code <= 99:
		return "雷暴"
	default:
		return "未知"
	}
}

func getCachedTextOpsWeather(key string, now time.Time) (textOpsWeatherResult, bool) {
	textOpsWeatherCache.RLock()
	entry, ok := textOpsWeatherCache.entries[key]
	textOpsWeatherCache.RUnlock()
	if !ok || !entry.expiresAt.After(now) {
		return textOpsWeatherResult{}, false
	}
	return entry.result, true
}

func cacheTextOpsWeather(key string, result textOpsWeatherResult, expiresAt time.Time) {
	textOpsWeatherCache.Lock()
	textOpsWeatherCache.entries[key] = textOpsWeatherCacheEntry{result: result, expiresAt: expiresAt}
	textOpsWeatherCache.Unlock()
}

func clearTextOpsWeatherCache() {
	textOpsWeatherCache.Lock()
	textOpsWeatherCache.entries = make(map[string]textOpsWeatherCacheEntry)
	textOpsWeatherCache.Unlock()
}

func firstNonEmptyTextOpsValue(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
