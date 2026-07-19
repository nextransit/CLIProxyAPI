package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

func TestClassifyTextOpsQueryKind(t *testing.T) {
	tests := []struct {
		query string
		want  string
	}{
		{query: "今天什么天气", want: textOpsQueryKindWeather},
		{query: "上海今天会下雨吗", want: textOpsQueryKindWeather},
		{query: "今天有什么AI新闻", want: textOpsQueryKindNews},
		{query: "最新 OpenAI 资讯", want: textOpsQueryKindNews},
		{query: "查询今日账单", want: textOpsQueryKindAnalytics},
		{query: "近24小时缓存命中率趋势", want: textOpsQueryKindAnalytics},
		{query: "什么是 Token", want: textOpsQueryKindGeneral},
		{query: "帮我写一句发布公告", want: textOpsQueryKindGeneral},
	}
	for _, test := range tests {
		t.Run(test.query, func(t *testing.T) {
			if got := classifyTextOpsQueryKind(test.query); got != test.want {
				t.Fatalf("classifyTextOpsQueryKind(%q) = %q, want %q", test.query, got, test.want)
			}
		})
	}
}

func TestExtractTextOpsWeatherLocation(t *testing.T) {
	tests := []struct {
		query string
		want  string
	}{
		{query: "今天什么天气", want: ""},
		{query: "上海今天什么天气", want: "上海"},
		{query: "杭州今天会下雨吗", want: "杭州"},
		{query: "weather in New York today", want: "New York"},
		{query: "我这里今天天气怎么样", want: ""},
	}
	for _, test := range tests {
		t.Run(test.query, func(t *testing.T) {
			if got := extractTextOpsWeatherLocation(test.query); got != test.want {
				t.Fatalf("extractTextOpsWeatherLocation(%q) = %q, want %q", test.query, got, test.want)
			}
		})
	}
}

func TestQueryTextOps_WeatherWithoutLocationSkipsLLM(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var llmCalls atomic.Int32
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		llmCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"should not be called"}}]}`))
	}))
	defer llmServer.Close()

	handler := NewHandler(&config.Config{}, "", nil)
	response := executeTextOpsTestRequest(t, handler, `{
		"user_query":"今天什么天气",
		"fast_mode":true,
		"operator_context":{"role":"admin","user_id":1},
		"router":{"enabled":true,"api_key":"test","base_url":"`+llmServer.URL+`","model":"MiniMax-M3"},
		"presenter":{"enabled":true,"api_key":"test","base_url":"`+llmServer.URL+`","model":"MiniMax-M3"}
	}`)

	if got := llmCalls.Load(); got != 0 {
		t.Fatalf("llm calls = %d, want 0", got)
	}
	if response.Router.Intent != textOpsIntentGeneralAssistant {
		t.Fatalf("intent = %q, want %q", response.Router.Intent, textOpsIntentGeneralAssistant)
	}
	if response.Router.Route != "weather_clarification" {
		t.Fatalf("route = %q, want weather_clarification", response.Router.Route)
	}
	if !strings.Contains(response.Presentation.Markdown, "城市") {
		t.Fatalf("presentation should ask for a city, got %q", response.Presentation.Markdown)
	}
}

func TestQueryTextOps_GeneralQuestionUsesSingleLLMCall(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var llmCalls atomic.Int32
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		llmCalls.Add(1)
		if request.URL.Path != "/v1/chat/completions" {
			t.Errorf("request path = %q, want /v1/chat/completions", request.URL.Path)
		}
		var payload textOpsLLMChatRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode llm request: %v", err)
		}
		if payload.ReasoningEffort != "" {
			t.Errorf("reasoning_effort = %q, want empty", payload.ReasoningEffort)
		}
		if payload.MaxTokens > textOpsGeneralMaxTokens {
			t.Errorf("max_tokens = %d, want <= %d", payload.MaxTokens, textOpsGeneralMaxTokens)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"发布完成，请关注错误率。"}}]}`))
	}))
	defer llmServer.Close()

	handler := NewHandler(&config.Config{}, "", nil)
	response := executeTextOpsTestRequest(t, handler, `{
		"user_query":"帮我写一句发布公告",
		"fast_mode":true,
		"operator_context":{"role":"admin","user_id":1},
		"router":{"enabled":true,"api_key":"test","base_url":"`+llmServer.URL+`","model":"custom-fast-model","max_tokens":2000},
		"presenter":{"enabled":true,"api_key":"test","base_url":"`+llmServer.URL+`","model":"custom-fast-model","max_tokens":4096}
	}`)

	if got := llmCalls.Load(); got != 1 {
		t.Fatalf("llm calls = %d, want 1", got)
	}
	if response.Router.Route != "general_llm" {
		t.Fatalf("route = %q, want general_llm", response.Router.Route)
	}
	if response.Presentation.Markdown != "发布完成，请关注错误率。" {
		t.Fatalf("presentation markdown = %q", response.Presentation.Markdown)
	}
}

func TestQueryTextOps_GeneralQuestionHedgesToDeepSeekFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var primaryCalls atomic.Int32
	var fallbackCalls atomic.Int32
	primaryStarted := make(chan struct{})
	var primaryStartedOnce sync.Once

	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var payload textOpsLLMChatRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode llm request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		switch payload.Model {
		case "MiniMax-M3":
			primaryCalls.Add(1)
			primaryStartedOnce.Do(func() { close(primaryStarted) })
			<-request.Context().Done()
		case textOpsGeneralFallbackModel:
			<-primaryStarted
			fallbackCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"fallback response"}}]}`))
		default:
			t.Errorf("unexpected model %q", payload.Model)
			http.Error(w, "unexpected model", http.StatusBadRequest)
		}
	}))
	defer llmServer.Close()

	handler := NewHandler(&config.Config{}, "", nil)
	response := executeTextOpsTestRequest(t, handler, `{
		"user_query":"帮我写一句发布公告",
		"fast_mode":true,
		"operator_context":{"role":"admin","user_id":1},
		"router":{"enabled":true,"api_key":"test","base_url":"`+llmServer.URL+`","model":"MiniMax-M3","max_tokens":800},
		"presenter":{"enabled":true,"api_key":"test","base_url":"`+llmServer.URL+`","model":"MiniMax-M3","max_tokens":1200}
	}`)

	if got := primaryCalls.Load(); got != 1 {
		t.Fatalf("primary calls = %d, want 1", got)
	}
	if got := fallbackCalls.Load(); got != 1 {
		t.Fatalf("fallback calls = %d, want 1", got)
	}
	if response.Router.Route != "general_llm_fallback" {
		t.Fatalf("route = %q, want general_llm_fallback", response.Router.Route)
	}
	if response.Data.Summary["model"] != textOpsGeneralFallbackModel {
		t.Fatalf("model = %#v, want %q", response.Data.Summary["model"], textOpsGeneralFallbackModel)
	}
	if response.Data.Summary["primary_model"] != "MiniMax-M3" {
		t.Fatalf("primary_model = %#v, want MiniMax-M3", response.Data.Summary["primary_model"])
	}
	if response.Presentation.Markdown != "fallback response" {
		t.Fatalf("presentation markdown = %q", response.Presentation.Markdown)
	}
}

func TestQueryTextOps_FastAnalyticsSkipsRouterAndPresenterLLM(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var llmCalls atomic.Int32
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		llmCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"should not be called"}}]}`))
	}))
	defer llmServer.Close()

	handler := NewHandler(&config.Config{}, "", nil)
	handler.SetUsageStatistics(usage.NewRequestStatistics())
	response := executeTextOpsTestRequest(t, handler, `{
		"user_query":"查询今日账单",
		"current_time":"2026-07-17T09:30:00Z",
		"fast_mode":true,
		"operator_context":{"role":"admin","user_id":1},
		"router":{"enabled":true,"api_key":"test","base_url":"`+llmServer.URL+`","model":"MiniMax-M3"},
		"presenter":{"enabled":true,"api_key":"test","base_url":"`+llmServer.URL+`","model":"MiniMax-M3"}
	}`)

	if got := llmCalls.Load(); got != 0 {
		t.Fatalf("llm calls = %d, want 0", got)
	}
	if response.Router.Route != "heuristic_fast" {
		t.Fatalf("route = %q, want heuristic_fast", response.Router.Route)
	}
	if response.Router.Intent != textOpsIntentFinancialStatus {
		t.Fatalf("intent = %q, want %q", response.Router.Intent, textOpsIntentFinancialStatus)
	}
}

func TestQueryTextOps_WeatherToolUsesCache(t *testing.T) {
	gin.SetMode(gin.TestMode)
	clearTextOpsWeatherCache()

	var geocodingCalls atomic.Int32
	var forecastCalls atomic.Int32
	weatherServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/geocoding":
			geocodingCalls.Add(1)
			_, _ = w.Write([]byte(`{"results":[{"name":"上海","country":"中国","admin1":"上海市","timezone":"Asia/Shanghai","latitude":31.23,"longitude":121.47}]}`))
		case "/forecast":
			forecastCalls.Add(1)
			_, _ = w.Write([]byte(`{
				"timezone":"Asia/Shanghai",
				"current":{"time":"2026-07-17T17:30","temperature_2m":31.2,"apparent_temperature":35.1,"relative_humidity_2m":67,"precipitation":0,"weather_code":1,"wind_speed_10m":12.4},
				"daily":{"temperature_2m_min":[27.1],"temperature_2m_max":[34.5],"precipitation_probability_max":[35],"weather_code":[1]}
			}`))
		default:
			http.NotFound(w, request)
		}
	}))
	defer weatherServer.Close()

	oldGeocodingEndpoint := textOpsWeatherGeocodingEndpoint
	oldForecastEndpoint := textOpsWeatherForecastEndpoint
	textOpsWeatherGeocodingEndpoint = weatherServer.URL + "/geocoding"
	textOpsWeatherForecastEndpoint = weatherServer.URL + "/forecast"
	t.Cleanup(func() {
		textOpsWeatherGeocodingEndpoint = oldGeocodingEndpoint
		textOpsWeatherForecastEndpoint = oldForecastEndpoint
		clearTextOpsWeatherCache()
	})

	handler := NewHandler(&config.Config{}, "", nil)
	requestBody := `{
		"user_query":"上海今天什么天气",
		"current_time":"2026-07-17T09:30:00Z",
		"fast_mode":true,
		"operator_context":{"role":"admin","user_id":1}
	}`
	first := executeTextOpsTestRequest(t, handler, requestBody)
	second := executeTextOpsTestRequest(t, handler, requestBody)

	if first.Router.Route != "weather_tool" || second.Router.Route != "weather_tool" {
		t.Fatalf("routes = %q, %q, want weather_tool", first.Router.Route, second.Router.Route)
	}
	if got := geocodingCalls.Load(); got != 1 {
		t.Fatalf("geocoding calls = %d, want 1", got)
	}
	if got := forecastCalls.Load(); got != 1 {
		t.Fatalf("forecast calls = %d, want 1", got)
	}
	if first.Data.Summary["temperature_c"] != float64(31.2) {
		t.Fatalf("temperature_c = %#v, want 31.2", first.Data.Summary["temperature_c"])
	}
	if !strings.Contains(first.Presentation.Markdown, "Open-Meteo") {
		t.Fatalf("presentation should include data source, got %q", first.Presentation.Markdown)
	}
}

func executeTextOpsTestRequest(t *testing.T, handler *Handler, body string) textOpsQueryResponse {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(
		http.MethodPost,
		"/v0/management/text-ops/query",
		strings.NewReader(body),
	)
	ctx.Request.Header.Set("Content-Type", "application/json")

	startedAt := time.Now()
	handler.QueryTextOps(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200, body=%s", recorder.Code, recorder.Body.String())
	}
	if time.Since(startedAt) > 5*time.Second {
		t.Fatalf("query took longer than 5 seconds in a local test")
	}

	var response textOpsQueryResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return response
}
