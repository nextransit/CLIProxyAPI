package management

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestExtractTextOpsNewsTopic(t *testing.T) {
	tests := []struct {
		query string
		want  string
	}{
		{query: "今天有什么AI新闻", want: "AI"},
		{query: "最新 OpenAI 资讯", want: "OpenAI"},
		{query: "今天有什么新闻", want: "人工智能"},
	}
	for _, test := range tests {
		t.Run(test.query, func(t *testing.T) {
			if got := extractTextOpsNewsTopic(test.query); got != test.want {
				t.Fatalf("extractTextOpsNewsTopic(%q) = %q, want %q", test.query, got, test.want)
			}
		})
	}
}

func TestQueryTextOps_NewsToolSkipsLLMAndUsesCache(t *testing.T) {
	gin.SetMode(gin.TestMode)
	clearTextOpsNewsCache()

	var newsCalls atomic.Int32
	var llmCalls atomic.Int32
	newsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/rss":
			newsCalls.Add(1)
			w.Header().Set("Content-Type", "application/rss+xml")
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel>
<item><title>AI Agent release improves tool reliability</title><link>https://example.com/agent-release</link><pubDate>Fri, 17 Jul 2026 04:00:00 GMT</pubDate><source>Example Tech</source></item>
<item><title>New open model published</title><link>https://example.com/open-model</link><pubDate>Fri, 17 Jul 2026 03:00:00 GMT</pubDate><source>Model News</source></item>
<item><title>Old AI story</title><link>https://example.com/old-story</link><pubDate>Mon, 15 Jun 2026 03:00:00 GMT</pubDate><source>Old News</source></item>
</channel></rss>`))
		default:
			llmCalls.Add(1)
			http.Error(w, "LLM should not be called", http.StatusInternalServerError)
		}
	}))
	defer newsServer.Close()

	oldSources := textOpsNewsFeedSources
	textOpsNewsFeedSources = []textOpsNewsFeedSource{{Name: "Test News", Endpoint: newsServer.URL + "/rss"}}
	t.Cleanup(func() {
		textOpsNewsFeedSources = oldSources
		clearTextOpsNewsCache()
	})

	handler := NewHandler(&config.Config{}, "", nil)
	requestBody := `{
		"user_query":"今天有什么AI新闻",
		"current_time":"2026-07-17T13:30:00+08:00",
		"fast_mode":true,
		"operator_context":{"role":"admin","user_id":1},
		"router":{"enabled":true,"api_key":"test","base_url":"` + newsServer.URL + `","model":"MiniMax-M3"},
		"presenter":{"enabled":true,"api_key":"test","base_url":"` + newsServer.URL + `","model":"MiniMax-M3"}
	}`
	first := executeTextOpsTestRequest(t, handler, requestBody)
	second := executeTextOpsTestRequest(t, handler, requestBody)

	if got := llmCalls.Load(); got != 0 {
		t.Fatalf("llm calls = %d, want 0", got)
	}
	if got := newsCalls.Load(); got != 1 {
		t.Fatalf("news calls = %d, want 1", got)
	}
	if first.Router.Route != "news_tool" || second.Router.Route != "news_tool" {
		t.Fatalf("routes = %q, %q, want news_tool", first.Router.Route, second.Router.Route)
	}
	if first.Data.Summary["data_source"] != "Test News" {
		t.Fatalf("data_source = %#v, want Test News", first.Data.Summary["data_source"])
	}
	if first.Data.Summary["item_count"] != float64(2) {
		t.Fatalf("item_count = %#v, want 2", first.Data.Summary["item_count"])
	}
	if !strings.Contains(first.Presentation.Markdown, "AI Agent release") ||
		!strings.Contains(first.Presentation.Markdown, "Example Tech") {
		t.Fatalf("presentation should include live news items, got %q", first.Presentation.Markdown)
	}
	if strings.Contains(first.Presentation.Markdown, "Old AI story") {
		t.Fatalf("presentation should exclude stale news, got %q", first.Presentation.Markdown)
	}
}

func TestQueryTextOps_NewsToolUsesAvailableFeed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	clearTextOpsNewsCache()

	newsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/failed":
			http.Error(w, "unavailable", http.StatusBadGateway)
		case "/working":
			w.Header().Set("Content-Type", "application/rss+xml")
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel>
<item><title>Fallback feed item</title><link>https://example.com/fallback</link><pubDate>Fri, 17 Jul 2026 05:00:00 GMT</pubDate><source>Backup Source</source></item>
</channel></rss>`))
		default:
			http.NotFound(w, request)
		}
	}))
	defer newsServer.Close()

	oldSources := textOpsNewsFeedSources
	textOpsNewsFeedSources = []textOpsNewsFeedSource{
		{Name: "Failed News", Endpoint: newsServer.URL + "/failed"},
		{Name: "Backup News", Endpoint: newsServer.URL + "/working"},
	}
	t.Cleanup(func() {
		textOpsNewsFeedSources = oldSources
		clearTextOpsNewsCache()
	})

	handler := NewHandler(&config.Config{}, "", nil)
	response := executeTextOpsTestRequest(t, handler, `{
		"user_query":"今天有什么AI新闻",
		"current_time":"2026-07-17T13:30:00+08:00",
		"fast_mode":true,
		"operator_context":{"role":"admin","user_id":1}
	}`)

	if response.Router.Route != "news_tool" {
		t.Fatalf("route = %q, want news_tool", response.Router.Route)
	}
	if response.Data.Summary["data_source"] != "Backup News" {
		t.Fatalf("data_source = %#v, want Backup News", response.Data.Summary["data_source"])
	}
}
