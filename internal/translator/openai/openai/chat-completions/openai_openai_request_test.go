package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIRequestToOpenAI_ModelReplacement(t *testing.T) {
	input := []byte(`{"model":"old-model","messages":[{"role":"user","content":"hi"}]}`)
	out := ConvertOpenAIRequestToOpenAI("new-model", input, false)
	if gjson.GetBytes(out, "model").String() != "new-model" {
		t.Errorf("expected model 'new-model', got %s", gjson.GetBytes(out, "model").String())
	}
}

func TestSanitizeToolCalls_StripsIncomplete(t *testing.T) {
	input := []byte(`{
		"model": "glm-5.1",
		"messages": [
			{"role": "user", "content": "hello"},
			{"role": "assistant", "content": null, "tool_calls": [{"id": "call_1", "type": "function", "function": {"name": "read", "arguments": "{}"}}, {"id": "call_2", "type": "function"}]},
			{"role": "tool", "tool_call_id": "call_1", "content": "result"}
		]
	}`)
	out := ConvertOpenAIRequestToOpenAI("glm-5.1", input, false)
	tcs := gjson.GetBytes(out, "messages.1.tool_calls")
	if !tcs.IsArray() {
		t.Fatalf("expected tool_calls array, got: %s", tcs.Raw)
	}
	arr := tcs.Array()
	if len(arr) != 1 {
		t.Errorf("expected 1 valid tool_call, got %d", len(arr))
	}
	if arr[0].Get("id").String() != "call_1" {
		t.Errorf("expected call_1, got %s", arr[0].Get("id").String())
	}
}

func TestSanitizeToolCalls_RemovesEntireArrayWhenAllInvalid(t *testing.T) {
	input := []byte(`{
		"model": "glm-5.1",
		"messages": [
			{"role": "user", "content": "hello"},
			{"role": "assistant", "content": null, "tool_calls": [{"id": "call_1", "type": "function"}, {"id": "call_2", "type": "function"}]}
		]
	}`)
	out := ConvertOpenAIRequestToOpenAI("glm-5.1", input, false)
	tcs := gjson.GetBytes(out, "messages.1.tool_calls")
	if tcs.Exists() {
		t.Errorf("expected tool_calls to be removed, got: %s", tcs.Raw)
	}
}

func TestSanitizeToolCalls_PassthroughWhenValid(t *testing.T) {
	input := []byte(`{
		"model": "glm-5.1",
		"messages": [
			{"role": "user", "content": "hello"},
			{"role": "assistant", "content": null, "tool_calls": [{"id": "call_1", "type": "function", "function": {"name": "read", "arguments": "{}"}}]}
		]
	}`)
	out := ConvertOpenAIRequestToOpenAI("glm-5.1", input, false)
	tcs := gjson.GetBytes(out, "messages.1.tool_calls")
	if !tcs.IsArray() || len(tcs.Array()) != 1 {
		t.Errorf("expected 1 tool_call unchanged, got: %s", tcs.Raw)
	}
}

func TestSanitizeToolCalls_NoToolCalls(t *testing.T) {
	input := []byte(`{"model":"glm-5.1","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"}]}`)
	out := ConvertOpenAIRequestToOpenAI("glm-5.1", input, false)
	content := gjson.GetBytes(out, "messages.1.content").String()
	if content != "hello" {
		t.Errorf("expected 'hello', got '%s'", content)
	}
}

func TestSanitizeToolMessages_CopyCallIDToToolCallID(t *testing.T) {
	input := []byte(`{
		"model": "minimax-ai/minimax-m2.7-highspeed",
		"messages": [
			{"role": "user", "content": "hello"},
			{"role": "assistant", "tool_calls": [{"id": "call_1", "type": "function", "function": {"name": "read", "arguments": "{}"}}]},
			{"role": "tool", "call_id": "call_1", "content": "result"}
		]
	}`)
	out := ConvertOpenAIRequestToOpenAI("minimax-ai/minimax-m2.7-highspeed", input, false)
	toolCallID := gjson.GetBytes(out, "messages.2.tool_call_id").String()
	if toolCallID != "call_1" {
		t.Errorf("expected tool_call_id 'call_1', got '%s'", toolCallID)
	}
	// Ensure call_id is still present
	callID := gjson.GetBytes(out, "messages.2.call_id").String()
	if callID != "call_1" {
		t.Errorf("expected call_id 'call_1', got '%s'", callID)
	}
}

func TestSanitizeToolMessages_PreserveExistingToolCallID(t *testing.T) {
	input := []byte(`{
		"model": "minimax-ai/minimax-m2.7-highspeed",
		"messages": [
			{"role": "user", "content": "hello"},
			{"role": "assistant", "tool_calls": [{"id": "call_1", "type": "function", "function": {"name": "read", "arguments": "{}"}}]},
			{"role": "tool", "tool_call_id": "call_1", "call_id": "different", "content": "result"}
		]
	}`)
	out := ConvertOpenAIRequestToOpenAI("minimax-ai/minimax-m2.7-highspeed", input, false)
	toolCallID := gjson.GetBytes(out, "messages.2.tool_call_id").String()
	if toolCallID != "call_1" {
		t.Errorf("expected tool_call_id 'call_1' to be preserved, got '%s'", toolCallID)
	}
}

func TestSanitizeToolMessages_NoOpWhenNoCallID(t *testing.T) {
	input := []byte(`{
		"model": "minimax-ai/minimax-m2.7-highspeed",
		"messages": [
			{"role": "user", "content": "hello"},
			{"role": "assistant", "tool_calls": [{"id": "call_1", "type": "function", "function": {"name": "read", "arguments": "{}"}}]},
			{"role": "tool", "content": "result"}
		]
	}`)
	out := ConvertOpenAIRequestToOpenAI("minimax-ai/minimax-m2.7-highspeed", input, false)
	toolCallID := gjson.GetBytes(out, "messages.2.tool_call_id").String()
	if toolCallID != "" {
		t.Errorf("expected no tool_call_id, got '%s'", toolCallID)
	}
}
