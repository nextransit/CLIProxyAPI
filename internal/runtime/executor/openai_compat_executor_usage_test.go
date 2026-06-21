package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	cliproxyusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

type usageProbe struct {
	records chan cliproxyusage.Record
}

func (p *usageProbe) HandleUsage(_ context.Context, record cliproxyusage.Record) {
	select {
	case p.records <- record:
	default:
	}
}

func waitUsageRecord(t *testing.T, ch <-chan cliproxyusage.Record, provider, model string, since time.Time) cliproxyusage.Record {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case record := <-ch:
			if record.Provider != provider {
				continue
			}
			if record.Model != model {
				continue
			}
			if record.RequestedAt.Before(since) {
				continue
			}
			return record
		case <-deadline:
			t.Fatalf("timed out waiting for usage record: provider=%s model=%s", provider, model)
		}
	}
}

func TestOpenAICompatExecutorFallbackUsageNonStream(t *testing.T) {
	probe := &usageProbe{records: make(chan cliproxyusage.Record, 16)}
	cliproxyusage.RegisterPlugin(probe)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if len(body) == 0 {
			t.Fatalf("expected request payload")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	model := "minimax-usage-fallback-nonstream"
	started := time.Now()
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   model,
		Payload: []byte(fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"hello"}]}`, model)),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	record := waitUsageRecord(t, probe.records, "openai-compatibility", model, started)
	if record.Failed {
		t.Fatalf("usage record marked failed unexpectedly")
	}
	if record.Detail.InputTokens <= 0 {
		t.Fatalf("input tokens = %d, want > 0", record.Detail.InputTokens)
	}
	if record.Detail.TotalTokens < record.Detail.InputTokens {
		t.Fatalf("total tokens = %d, want >= input tokens %d", record.Detail.TotalTokens, record.Detail.InputTokens)
	}
}

func TestOpenAICompatExecutorMiniMaxM3UsageThinkingFromClaudeRequest(t *testing.T) {
	probe := &usageProbe{records: make(chan cliproxyusage.Record, 16)}
	cliproxyusage.RegisterPlugin(probe)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if gjson.GetBytes(body, "reasoning_effort").Exists() {
			t.Fatalf("reasoning_effort should be normalized away for MiniMax-M3, body=%s", string(body))
		}
		if got := gjson.GetBytes(body, "extra_body.thinking.type").String(); got != "adaptive" {
			t.Fatalf("thinking.type = %q, want adaptive, body=%s", got, string(body))
		}
		if got := gjson.GetBytes(body, "reasoning_split").Bool(); !got {
			t.Fatalf("reasoning_split = %v, want true, body=%s", got, string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-x","object":"chat.completion","usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3},"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	model := "MiniMax-M3"
	started := time.Now()
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model: model,
		Payload: []byte(`{
			"model":"MiniMax-M3",
			"max_tokens":128,
			"thinking":{"type":"adaptive"},
			"output_config":{"effort":"high"},
			"messages":[{"role":"user","content":"hello"}]
		}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	record := waitUsageRecord(t, probe.records, "openai-compatibility", model, started)
	if record.Detail.Thinking == nil {
		t.Fatal("thinking should not be nil")
	}
	if got := record.Detail.Thinking.Intensity; got != "high" {
		t.Fatalf("thinking intensity = %q, want high", got)
	}
	if got := record.Detail.Thinking.Mode; got != "high" {
		t.Fatalf("thinking mode = %q, want auto", got)
	}
	if got := record.Detail.Thinking.Level; got != "high" {
		t.Fatalf("thinking level = %q, want auto", got)
	}
}

func TestOpenAICompatExecutorMiniMaxM3AcceptsXHighReasoningEffort(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	clientID := "test-minimax-m3-stale-levels-" + t.Name()
	reg.RegisterClient(clientID, "minimax", []*registry.ModelInfo{{
		ID:       "MiniMax-M3",
		OwnedBy:  "minimax",
		Type:     "minimax",
		Thinking: &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}},
	}})
	t.Cleanup(func() {
		reg.UnregisterClient(clientID)
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if gjson.GetBytes(body, "reasoning_effort").Exists() {
			t.Fatalf("reasoning_effort should be normalized away for MiniMax-M3, body=%s", string(body))
		}
		if got := gjson.GetBytes(body, "extra_body.thinking.type").String(); got != "adaptive" {
			t.Fatalf("thinking.type = %q, want adaptive, body=%s", got, string(body))
		}
		if got := gjson.GetBytes(body, "reasoning_split").Bool(); !got {
			t.Fatalf("reasoning_split = %v, want true, body=%s", got, string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-x","object":"chat.completion","usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3},"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "MiniMax-M3",
		Payload: []byte(`{"model":"MiniMax-M3","messages":[{"role":"user","content":"hello"}],"reasoning_effort":"xhigh"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
}

func TestOpenAICompatExecutorSensenovaDeepSeekV4DefaultsHighThinking(t *testing.T) {
	probe := &usageProbe{records: make(chan cliproxyusage.Record, 16)}
	cliproxyusage.RegisterPlugin(probe)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if got := gjson.GetBytes(body, "reasoning_effort").String(); got != "high" {
			t.Fatalf("reasoning_effort = %q, want high; body=%s", got, string(body))
		}
		if gjson.GetBytes(body, "extra_body.thinking.type").Exists() {
			t.Fatalf("thinking.type should not be sent to Sensenova; body=%s", string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-x","object":"chat.completion","usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3},"choices":[{"index":0,"message":{"role":"assistant","content":"ok","reasoning_content":"reasoned"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("sensenova", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	model := "deepseek-v4-flash"
	started := time.Now()
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   model,
		Payload: []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	record := waitUsageRecord(t, probe.records, "sensenova", model, started)
	if record.Detail.Thinking == nil {
		t.Fatal("thinking should not be nil")
	}
	if got := record.Detail.Thinking.Intensity; got != "high" {
		t.Fatalf("thinking intensity = %q, want high", got)
	}
	if got := record.Detail.Thinking.Mode; got != "level" {
		t.Fatalf("thinking mode = %q, want level", got)
	}
	if got := record.Detail.Thinking.Level; got != "high" {
		t.Fatalf("thinking level = %q, want high", got)
	}
}

func TestOpenAICompatExecutorDeepSeekV4UsagePreservesRequestedXHigh(t *testing.T) {
	probe := &usageProbe{records: make(chan cliproxyusage.Record, 16)}
	cliproxyusage.RegisterPlugin(probe)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if got := gjson.GetBytes(body, "reasoning_effort").String(); got != "high" {
			t.Fatalf("reasoning_effort = %q, want high; body=%s", got, string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-x","object":"chat.completion","usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3},"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("sensenova", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	model := "deepseek-v4-flash"
	started := time.Now()
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   model,
		Payload: []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hello"}],"reasoning_effort":"xhigh"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	record := waitUsageRecord(t, probe.records, "sensenova", model, started)
	if record.Detail.Thinking == nil {
		t.Fatal("thinking should not be nil")
	}
	if got := record.Detail.Thinking.Intensity; got != "xhigh" {
		t.Fatalf("thinking intensity = %q, want xhigh", got)
	}
	if got := record.Detail.Thinking.Level; got != "xhigh" {
		t.Fatalf("thinking level = %q, want xhigh", got)
	}
}

func TestOpenAICompatExecutorOpenRouterDeepSeekV4DefaultThinkingIsRecorded(t *testing.T) {
	probe := &usageProbe{records: make(chan cliproxyusage.Record, 16)}
	cliproxyusage.RegisterPlugin(probe)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if got := gjson.GetBytes(body, "reasoning_effort").String(); got != "high" {
			t.Fatalf("reasoning_effort = %q, want high; body=%s", got, string(body))
		}
		if gjson.GetBytes(body, "extra_body.thinking.type").Exists() {
			t.Fatalf("thinking.type should not be sent to OpenRouter; body=%s", string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-x","object":"chat.completion","usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3},"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openrouter", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	model := "deepseek/deepseek-v4-flash"
	started := time.Now()
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   model,
		Payload: []byte(`{"model":"deepseek/deepseek-v4-flash","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	record := waitUsageRecord(t, probe.records, "openrouter", model, started)
	if record.Detail.Thinking == nil {
		t.Fatal("thinking should not be nil")
	}
	if got := record.Detail.Thinking.Intensity; got != "high" {
		t.Fatalf("thinking intensity = %q, want high", got)
	}
	if got := record.Detail.Thinking.Mode; got != "level" {
		t.Fatalf("thinking mode = %q, want level", got)
	}
	if got := record.Detail.Thinking.Level; got != "high" {
		t.Fatalf("thinking level = %q, want high", got)
	}
}

func TestOpenAICompatExecutorFallbackUsageStream(t *testing.T) {
	probe := &usageProbe{records: make(chan cliproxyusage.Record, 16)}
	cliproxyusage.RegisterPlugin(probe)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !gjson.GetBytes(body, "stream_options.include_usage").Bool() {
			t.Fatalf("stream_options.include_usage should be true, body=%s", string(body))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatalf("response writer does not support flushing")
		}
		_, _ = io.WriteString(w, "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"choices\":[],\"usage\":{\"prompt_tokens\":0,\"completion_tokens\":0,\"total_tokens\":0}}\n\n")
		flusher.Flush()
		_, _ = io.WriteString(w, "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":null}]}\n\n")
		flusher.Flush()
		_, _ = io.WriteString(w, "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		flusher.Flush()
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	model := "minimax-usage-fallback-stream"
	started := time.Now()
	stream, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   model,
		Payload: []byte(fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"hello"}]}`, model)),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
	}

	record := waitUsageRecord(t, probe.records, "openai-compatibility", model, started)
	if record.Failed {
		t.Fatalf("usage record marked failed unexpectedly")
	}
	if record.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", record.StatusCode, http.StatusOK)
	}
	if record.Detail.InputTokens <= 0 {
		t.Fatalf("input tokens = %d, want > 0", record.Detail.InputTokens)
	}
	if record.Detail.TotalTokens < record.Detail.InputTokens {
		t.Fatalf("total tokens = %d, want >= input tokens %d", record.Detail.TotalTokens, record.Detail.InputTokens)
	}
}
