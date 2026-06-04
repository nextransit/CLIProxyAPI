package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestIsMiniMaxContextWindowError(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			"2013 context window from MiniMax plain JSON",
			`{"type":"error","error":{"type":"bad_request_error","message":"invalid params, context window exceeds limit (2013)","http_code":"400"}}`,
			true,
		},
		{
			"2013 without context window phrase",
			`{"error":{"message":"something (2013)"}}`,
			false,
		},
		{
			"context window without 2013",
			`{"error":{"message":"context window exceeded"}}`,
			false,
		},
		{"empty body", "", false},
		{"unrelated error", `{"error":{"message":"bad request"}}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isMiniMaxContextWindowError([]byte(tt.body))
			if got != tt.want {
				t.Errorf("isMiniMaxContextWindowError(%q) = %v, want %v", tt.body, got, tt.want)
			}
		})
	}
}

func TestCompactMessagesForRetry(t *testing.T) {
	// Build a payload with 30 messages: index 0 = system, indices 1-29 = user/assistant pairs.
	parts := []string{`{"role":"system","content":"sys"}`}
	for i := 1; i < 30; i++ {
		role := "user"
		if i%2 == 0 {
			role = "assistant"
		}
		parts = append(parts, fmt.Sprintf(`{"role":"%s","content":"msg%d"}`, role, i))
	}
	msgs := "[" + parts[0]
	for i := 1; i < len(parts); i++ {
		msgs += "," + parts[i]
	}
	msgs += "]"
	payload := []byte(`{"model":"MiniMax-M2.7-highspeed","messages":` + msgs + `}`)

	// Compact to 20 messages
	compacted := compactMessagesForRetry(payload, 20)

	var m map[string]interface{}
	if err := json.Unmarshal(compacted, &m); err != nil {
		t.Fatalf("unmarshal compacted: %v", err)
	}
	msgSlice, ok := m["messages"].([]interface{})
	if !ok {
		t.Fatal("messages field is not an array")
	}
	if len(msgSlice) != 21 {
		t.Errorf("expected 21 messages after compact (system + last 20), got %d", len(msgSlice))
	}
	// First message should be system
	first, ok := msgSlice[0].(map[string]interface{})
	if !ok {
		t.Fatal("first message is not a map")
	}
	if first["role"] != "system" {
		t.Errorf("expected first role system, got %v", first["role"])
	}
	// Last message should be msg29 (at index 20 since system + last 20 = 21 items)
	last, ok := msgSlice[20].(map[string]interface{})
	if !ok {
		t.Fatal("last message is not a map")
	}
	if last["content"] != "msg29" {
		t.Errorf("expected last content msg29, got %v", last["content"])
	}
}

func TestCompactMessagesForRetry_UnderLimit(t *testing.T) {
	payload := []byte(`{"model":"MiniMax-M2.7-highspeed","messages":[{"role":"system","content":"sys"},{"role":"user","content":"1"},{"role":"assistant","content":"2"},{"role":"user","content":"3"},{"role":"assistant","content":"4"}]}`)
	compacted := compactMessagesForRetry(payload, 20)
	if !bytes.Equal(compacted, payload) {
		t.Error("expected payload unchanged when under message limit")
	}
}

func TestCompactMessagesForRetry_NoMessagesField(t *testing.T) {
	payload := []byte(`{"model":"MiniMax-M2.7-highspeed"}`)
	compacted := compactMessagesForRetry(payload, 20)
	if !bytes.Equal(compacted, payload) {
		t.Error("expected payload unchanged when no messages field")
	}
}

func TestCompactMessagesForRetry_InputField(t *testing.T) {
	// OpenAI Responses API uses "input" field instead of "messages".
	parts := []string{`{"role":"system","content":"sys"}`}
	for i := 1; i < 30; i++ {
		parts = append(parts, fmt.Sprintf(`{"role":"user","content":"msg%d"}`, i))
	}
	input := "[" + parts[0]
	for i := 1; i < len(parts); i++ {
		input += "," + parts[i]
	}
	input += "]"
	payload := []byte(`{"model":"MiniMax-M2.7-highspeed","input":` + input + `,"tools":[]}`)

	compacted := compactMessagesForRetry(payload, 5)

	var m map[string]interface{}
	if err := json.Unmarshal(compacted, &m); err != nil {
		t.Fatalf("unmarshal compacted: %v", err)
	}
	inp, ok := m["input"].([]interface{})
	if !ok {
		t.Fatal("input field is not an array")
	}
	// System (1) + last 5 = 6 items
	if len(inp) != 6 {
		t.Errorf("expected 6 input items after compact (system + last 5), got %d", len(inp))
	}
	first, ok := inp[0].(map[string]interface{})
	if !ok {
		t.Fatal("first input is not a map")
	}
	if first["role"] != "system" {
		t.Errorf("expected first role system, got %v", first["role"])
	}
	last, ok := inp[5].(map[string]interface{})
	if !ok {
		t.Fatal("last input is not a map")
	}
	if last["content"] != "msg29" {
		t.Errorf("expected last content msg29, got %v", last["content"])
	}
}

func TestCompactMessagesForRetry_ExactlyAtLimit(t *testing.T) {
	parts := []string{`{"role":"system","content":"sys"}`}
	for i := 1; i < 10; i++ {
		parts = append(parts, fmt.Sprintf(`{"role":"user","content":"%d"}`, i))
	}
	msgs := "[" + parts[0]
	for i := 1; i < len(parts); i++ {
		msgs += "," + parts[i]
	}
	msgs += "]"
	payload := []byte(`{"model":"MiniMax-M2.7-highspeed","messages":` + msgs + `}`)
	compacted := compactMessagesForRetry(payload, 10)
	if !bytes.Equal(compacted, payload) {
		t.Error("expected payload unchanged when exactly at limit")
	}
}

func TestStripToolsFromPayload(t *testing.T) {
	payload := []byte(`{"model":"MiniMax-M2.7","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","name":"test"}],"tool_choice":"auto"}`)
	stripped := stripToolsFromPayload(payload)

	var m map[string]interface{}
	if err := json.Unmarshal(stripped, &m); err != nil {
		t.Fatalf("unmarshal stripped: %v", err)
	}
	if _, ok := m["tools"]; ok {
		t.Error("expected tools to be removed")
	}
	if _, ok := m["tool_choice"]; ok {
		t.Error("expected tool_choice to be removed")
	}
	if _, ok := m["messages"]; !ok {
		t.Error("expected messages to be preserved")
	}
}

func TestStripToolsFromPayload_NoTools(t *testing.T) {
	payload := []byte(`{"model":"MiniMax-M2.7","messages":[{"role":"user","content":"hi"}]}`)
	stripped := stripToolsFromPayload(payload)
	if !bytes.Equal(stripped, payload) {
		t.Error("expected payload unchanged when no tools present")
	}
}

func TestCompactWithStripTools(t *testing.T) {
	payload := []byte(`{"model":"MiniMax-M2.7","messages":[{"role":"system","content":"sys"},{"role":"user","content":"msg1"},{"role":"assistant","content":"msg2"}],"tools":[{"type":"function","name":"test"}],"tool_choice":"auto"}`)

	stripped := stripToolsFromPayload(payload)
	var m map[string]interface{}
	if err := json.Unmarshal(stripped, &m); err != nil {
		t.Fatalf("unmarshal stripped: %v", err)
	}
	if _, ok := m["tools"]; ok {
		t.Error("expected tools removed after strip")
	}
	if _, ok := m["tool_choice"]; ok {
		t.Error("expected tool_choice removed after strip")
	}

	compacted := compactMessagesForRetry(stripped, 5)
	if !bytes.Equal(compacted, stripped) {
		t.Error("expected compact unchanged when messages under limit")
	}
}

func TestCompactWithStripAndTruncate(t *testing.T) {
	hugeContent := strings.Repeat("x", 500000)
	payload := []byte(`{"model":"MiniMax-M2.7","messages":[{"role":"user","content":"` + hugeContent + `"}],"tools":[{"type":"function","name":"test"}],"tool_choice":"auto"}`)

	stripped := stripToolsFromPayload(payload)
	compacted := compactMessagesForRetry(stripped, 1)
	truncated := truncateMessageContent(compacted, 128000)

	var m map[string]interface{}
	if err := json.Unmarshal(truncated, &m); err != nil {
		t.Fatalf("unmarshal truncated: %v", err)
	}
	msgs, ok := m["messages"].([]interface{})
	if !ok {
		t.Fatal("messages not an array")
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	msg := msgs[0].(map[string]interface{})
	content := msg["content"].(string)
	if len(content) >= len(hugeContent) {
		t.Error("expected content to be truncated")
	}
	if !strings.Contains(content, "[truncated") {
		t.Error("expected truncation marker in content")
	}
}

func TestTruncateMessageContent_SmallContent(t *testing.T) {
	payload := []byte(`{"model":"MiniMax-M2.7","messages":[{"role":"user","content":"hello"}]}`)
	result := truncateMessageContent(payload, 128000)
	if !bytes.Equal(result, payload) {
		t.Error("expected payload unchanged when content is small")
	}
}

func TestTruncateMessageContent_NoMessages(t *testing.T) {
	payload := []byte(`{"model":"MiniMax-M2.7"}`)
	result := truncateMessageContent(payload, 128000)
	if !bytes.Equal(result, payload) {
		t.Error("expected payload unchanged when no messages")
	}
}

func TestStripToolsFromAnthropicPayload(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{
			"has tools",
			`{"model":"claude-sonnet-4-20250514","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"bash","description":"run command"}],"system":"you are helpful"}`,
		},
		{
			"no tools",
			`{"model":"claude-sonnet-4-20250514","messages":[{"role":"user","content":"hi"}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stripped := stripToolsFromAnthropicPayload([]byte(tt.payload))
			var m map[string]interface{}
			if err := json.Unmarshal(stripped, &m); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if _, ok := m["tools"]; ok {
				t.Error("expected tools to be removed")
			}
			if _, ok := m["messages"]; !ok {
				t.Error("expected messages to be preserved")
			}
		})
	}
}

func TestCompactAnthropicPayloadWithStripAndTruncate(t *testing.T) {
	parts := make([]string, 0, 15)
	for i := 0; i < 15; i++ {
		role := "user"
		if i%2 == 0 {
			role = "assistant"
		}
		parts = append(parts, fmt.Sprintf(`{"role":"%s","content":"msg%d"}`, role, i))
	}
	msgs := "[" + parts[0]
	for i := 1; i < len(parts); i++ {
		msgs += "," + parts[i]
	}
	msgs += "]"
	payload := []byte(`{"model":"claude-sonnet-4-20250514","messages":` + msgs + `,"tools":[{"name":"bash"}],"system":"you are helpful"}`)

	e := NewMiniMaxExecutor("minimax", &config.Config{})

	stripped := stripToolsFromAnthropicPayload(payload)
	compacted := e.compactAnthropicPayload(stripped, 5)

	var m map[string]interface{}
	if err := json.Unmarshal(compacted, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := m["tools"]; ok {
		t.Error("expected tools removed")
	}
	msgsArr, ok := m["messages"].([]interface{})
	if !ok {
		t.Fatal("messages not array")
	}
	if len(msgsArr) != 5 {
		t.Errorf("expected 5 messages after compact, got %d", len(msgsArr))
	}
}

func TestIsContextWindowExceeded(t *testing.T) {
	tests := []struct {
		name           string
		body           string
		httpStatusCode int
		want           bool
	}{
		{
			"2013 with HTTP 400",
			`{"error":{"type":"bad_request_error","message":"context window exceeds limit (2013)","http_code":"400"}}`,
			400,
			true,
		},
		{
			"2013 with HTTP 500 but http_code 400 (the fix case)",
			`{"type":"error","error":{"type":"bad_request_error","message":"invalid params, context window exceeds limit (2013)","http_code":"400"}}`,
			500,
			true,
		},
		{
			"2013 without context window phrase",
			`{"error":{"message":"something (2013)"}}`,
			400,
			false,
		},
		{
			"context window without 2013",
			`{"error":{"message":"context window exceeded"}}`,
			400,
			false,
		},
		{
			"empty body",
			"",
			400,
			false,
		},
		{
			"unrelated error with HTTP 500",
			`{"error":{"message":"bad request"}}`,
			500,
			false,
		},
		{
			"error.code 2013 with HTTP 500 but http_code 400",
			`{"error":{"code":2013,"message":"context exceeded"},"http_code":"400"}`,
			500,
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isContextWindowExceeded([]byte(tt.body), tt.httpStatusCode)
			if got != tt.want {
				t.Errorf("isContextWindowExceeded(%q, %d) = %v, want %v", tt.body, tt.httpStatusCode, got, tt.want)
			}
		})
	}
}

func TestShouldAttemptOpenAICompat2013RecoveryMiniMaxAmbiguousLargePayload(t *testing.T) {
	body := []byte(`{"type":"error","error":{"type":"bad_request_error","message":"invalid params, 400 (2013)","http_code":"400"}}`)
	payload := []byte(`{"model":"MiniMax-M3","messages":[{"role":"user","content":"` + strings.Repeat("x", 1<<20) + `"}]}`)

	if !shouldAttemptOpenAICompat2013Recovery(body, http.StatusBadRequest, "MiniMax-M3", payload) {
		t.Fatal("expected MiniMax ambiguous 2013 with large payload to trigger recovery")
	}
	if shouldAttemptOpenAICompat2013Recovery(body, http.StatusBadRequest, "gpt-test", payload) {
		t.Fatal("expected non-MiniMax ambiguous 2013 to stay non-recoverable")
	}
}

func TestOpenAICompatExecutorMiniMaxAmbiguous2013RetriesStrippedTools(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		call := atomic.AddInt32(&calls, 1)
		switch call {
		case 1:
			if !gjson.GetBytes(body, "tools").Exists() {
				t.Fatalf("first request should include tools, body=%s", string(body))
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"bad_request_error","message":"invalid params, 400 (2013)","http_code":"400"}}`))
		case 2:
			if gjson.GetBytes(body, "tools").Exists() {
				t.Fatalf("retry request should strip tools, body=%s", string(body))
			}
			if gjson.GetBytes(body, "tool_choice").Exists() {
				t.Fatalf("retry request should strip tool_choice, body=%s", string(body))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"chatcmpl-x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
		default:
			t.Fatalf("unexpected retry call %d", call)
		}
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model: "MiniMax-M3",
		Payload: []byte(`{
			"model":"MiniMax-M3",
			"messages":[{"role":"user","content":"hi"}],
			"tools":[{"type":"function","function":{"name":"bad_tool","description":"test","parameters":{"type":"object","properties":{}}}}],
			"tool_choice":"auto"
		}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !bytes.Contains(resp.Payload, []byte(`"content":"ok"`)) {
		t.Fatalf("response payload = %s, want assistant content", string(resp.Payload))
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("calls = %d, want 2", got)
	}
}

func TestOpenAICompatExecutorMiniMaxAmbiguous2013StreamRetryKeepsSuccessBody(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		call := atomic.AddInt32(&calls, 1)
		switch call {
		case 1:
			if !gjson.GetBytes(body, "tools").Exists() {
				t.Fatalf("first request should include tools, body=%s", string(body))
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"bad_request_error","message":"invalid params, 400 (2013)","http_code":"400"}}`))
		case 2:
			if gjson.GetBytes(body, "tools").Exists() {
				t.Fatalf("retry request should strip tools, body=%s", string(body))
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":null}]}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		default:
			t.Fatalf("unexpected retry call %d", call)
		}
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}

	stream, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model: "MiniMax-M3",
		Payload: []byte(`{
			"model":"MiniMax-M3",
			"messages":[{"role":"user","content":"hi"}],
			"tools":[{"type":"function","function":{"name":"bad_tool","description":"test","parameters":{"type":"object","properties":{}}}}],
			"tool_choice":"auto"
		}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	var got bytes.Buffer
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		got.Write(chunk.Payload)
	}
	if !strings.Contains(got.String(), `"content":"ok"`) {
		t.Fatalf("stream payload = %s, want content chunk", got.String())
	}
	if gotCalls := atomic.LoadInt32(&calls); gotCalls != 2 {
		t.Fatalf("calls = %d, want 2", gotCalls)
	}
}

func TestOpenAICompatExecutorMiniMaxAmbiguous2013StreamRetryExhaustedKeepsFailureBody(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		call := atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		switch call {
		case 1:
			if !gjson.GetBytes(body, "tools").Exists() {
				t.Fatalf("first request should include tools, body=%s", string(body))
			}
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"bad_request_error","message":"initial invalid params, 400 (2013)","http_code":"400"}}`))
		case 2:
			if gjson.GetBytes(body, "tools").Exists() {
				t.Fatalf("retry request should strip tools, body=%s", string(body))
			}
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"bad_request_error","message":"retry still invalid params, 400 (2013)","http_code":"400"}}`))
		default:
			t.Fatalf("unexpected retry call %d", call)
		}
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}

	_, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model: "MiniMax-M3",
		Payload: []byte(`{
			"model":"MiniMax-M3",
			"messages":[{"role":"user","content":"hi"}],
			"tools":[{"type":"function","function":{"name":"bad_tool","description":"test","parameters":{"type":"object","properties":{}}}}],
			"tool_choice":"auto"
		}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       true,
	})
	if err == nil {
		t.Fatal("ExecuteStream() expected error")
	}
	if !strings.Contains(err.Error(), "retry still invalid params") {
		t.Fatalf("ExecuteStream() error = %v, want retry failure body", err)
	}
	if gotCalls := atomic.LoadInt32(&calls); gotCalls != 2 {
		t.Fatalf("calls = %d, want 2", gotCalls)
	}
}
