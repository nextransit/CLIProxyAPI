package executor

import (
	"context"
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
	"github.com/tidwall/gjson"
)

func TestCodexExecutorUsagePreservesXHighBeforePayloadOverride(t *testing.T) {
	probe := &usageProbe{records: make(chan cliproxyusage.Record, 16)}
	cliproxyusage.RegisterPlugin(probe)

	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":0,\"status\":\"completed\",\"background\":false,\"error\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":2,\"total_tokens\":3}}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{{
				Models: []config.PayloadModelRule{{Name: "gpt-5.5", Protocol: "codex"}},
				Params: map[string]any{"reasoning.effort": "high"},
			}},
		},
	})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	started := time.Now()
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5(xhigh)",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if got := gjson.GetBytes(gotBody, "reasoning.effort").String(); got != "high" {
		t.Fatalf("upstream reasoning.effort = %q, want high; body=%s", got, string(gotBody))
	}

	record := waitUsageRecord(t, probe.records, "codex", "gpt-5.5", started)
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
