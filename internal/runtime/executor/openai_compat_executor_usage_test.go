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
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	cliproxyusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
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

func TestOpenAICompatExecutorFallbackUsageStream(t *testing.T) {
	probe := &usageProbe{records: make(chan cliproxyusage.Record, 16)}
	cliproxyusage.RegisterPlugin(probe)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatalf("response writer does not support flushing")
		}
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
	if record.Detail.InputTokens <= 0 {
		t.Fatalf("input tokens = %d, want > 0", record.Detail.InputTokens)
	}
	if record.Detail.TotalTokens < record.Detail.InputTokens {
		t.Fatalf("total tokens = %d, want >= input tokens %d", record.Detail.TotalTokens, record.Detail.InputTokens)
	}
}
