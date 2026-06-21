package claude

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponseToClaudeRestoresNonStreamingToolNameVariant(t *testing.T) {
	originalRequest := []byte(`{
		"model": "deepseek-v4-flash",
		"stream": false,
		"tools": [{"name": "AskUserQuestion", "input_schema": {"type": "object"}}],
		"messages": []
	}`)
	response := []byte(`data: {
		"id": "chatcmpl-1",
		"model": "deepseek-v4-flash",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": null,
				"tool_calls": [{
					"id": "call_123",
					"type": "function",
					"function": {
						"name": "ask_user_question",
						"arguments": "{\"questions\":[{\"question\":\"Continue?\"}]}"
					}
				}]
			},
			"finish_reason": "tool_calls"
		}],
		"usage": {"prompt_tokens": 10, "completion_tokens": 5}
	}`)

	var param any
	chunks := ConvertOpenAIResponseToClaude(
		context.Background(),
		"deepseek-v4-flash",
		originalRequest,
		nil,
		response,
		&param,
	)

	if len(chunks) != 1 {
		t.Fatalf("len(chunks) = %d, want 1", len(chunks))
	}
	root := gjson.ParseBytes(chunks[0])
	if got := root.Get("content.0.name").String(); got != "AskUserQuestion" {
		t.Fatalf("content.0.name = %q, want %q; response=%s", got, "AskUserQuestion", string(chunks[0]))
	}
	if got := root.Get("content.0.input.questions.0.question").String(); got != "Continue?" {
		t.Fatalf("content.0.input.questions.0.question = %q, want %q", got, "Continue?")
	}
	if got := root.Get("stop_reason").String(); got != "tool_use" {
		t.Fatalf("stop_reason = %q, want %q", got, "tool_use")
	}
}

func TestConvertOpenAIResponseToClaudeRestoresStreamingToolNameVariant(t *testing.T) {
	originalRequest := []byte(`{
		"model": "deepseek-v4-flash",
		"stream": true,
		"tools": [{"name": "ScheduleWakeup", "input_schema": {"type": "object"}}],
		"messages": []
	}`)
	response := []byte(`data: {
		"id": "chatcmpl-1",
		"model": "deepseek-v4-flash",
		"created": 1,
		"choices": [{
			"index": 0,
			"delta": {
				"tool_calls": [{
					"index": 0,
					"id": "call_456",
					"type": "function",
					"function": {
						"name": "schedule_wakeup",
						"arguments": "{\"delaySeconds\":60}"
					}
				}]
			},
			"finish_reason": null
		}]
	}`)

	var param any
	chunks := ConvertOpenAIResponseToClaude(
		context.Background(),
		"deepseek-v4-flash",
		originalRequest,
		nil,
		response,
		&param,
	)

	payload := findClaudeSSEPayload(t, chunks, "content_block_start")
	if got := payload.Get("content_block.name").String(); got != "ScheduleWakeup" {
		t.Fatalf("content_block.name = %q, want %q; payload=%s", got, "ScheduleWakeup", payload.Raw)
	}
}

func TestConvertOpenAIResponseToClaudeDefersStreamingToolStartUntilName(t *testing.T) {
	originalRequest := []byte(`{
		"model": "deepseek-v4-flash",
		"stream": true,
		"tools": [{"name": "Bash", "input_schema": {"type": "object"}}],
		"messages": []
	}`)

	var param any
	firstChunks := ConvertOpenAIResponseToClaude(
		context.Background(),
		"deepseek-v4-flash",
		originalRequest,
		nil,
		[]byte(`data: {"id":"chatcmpl-1","model":"deepseek-v4-flash","created":1,"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_789","type":"function","function":{"name":"","arguments":""}}]},"finish_reason":null}]}`),
		&param,
	)
	if hasClaudeSSEEvent(firstChunks, "content_block_start") {
		t.Fatalf("empty upstream function.name must not emit content_block_start: %q", string(firstChunks[0]))
	}

	secondChunks := ConvertOpenAIResponseToClaude(
		context.Background(),
		"deepseek-v4-flash",
		originalRequest,
		nil,
		[]byte(`data: {"id":"chatcmpl-1","model":"deepseek-v4-flash","created":1,"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"bash","arguments":"{\"command\":\"pwd\"}"}}]},"finish_reason":null}]}`),
		&param,
	)

	payload := findClaudeSSEPayload(t, secondChunks, "content_block_start")
	if got := payload.Get("content_block.name").String(); got != "Bash" {
		t.Fatalf("content_block.name = %q, want %q; payload=%s", got, "Bash", payload.Raw)
	}
}

func findClaudeSSEPayload(t *testing.T, chunks [][]byte, event string) gjson.Result {
	t.Helper()
	prefix := "event: " + event + "\n"
	for _, chunk := range chunks {
		text := string(chunk)
		if !strings.HasPrefix(text, prefix) {
			continue
		}
		dataPrefix := "data: "
		dataIndex := strings.Index(text, dataPrefix)
		if dataIndex < 0 {
			t.Fatalf("SSE chunk for event %q has no data line: %q", event, text)
		}
		return gjson.Parse(strings.TrimSpace(text[dataIndex+len(dataPrefix):]))
	}
	t.Fatalf("event %q not found in %d chunks", event, len(chunks))
	return gjson.Result{}
}

func hasClaudeSSEEvent(chunks [][]byte, event string) bool {
	prefix := "event: " + event + "\n"
	for _, chunk := range chunks {
		if strings.HasPrefix(string(chunk), prefix) {
			return true
		}
	}
	return false
}
