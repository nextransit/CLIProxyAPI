package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_ToolCallOutput(t *testing.T) {
	// Test case: function_call_output with call_id
	inputWithCallID := []byte(`{
		"model": "MiniMax-M2.7-highspeed",
		"input": [
			{"type": "function_call", "call_id": "call_123", "name": "test_tool", "arguments": "{}"},
			{"type": "function_call_output", "call_id": "call_123", "output": "result"}
		]
	}`)

	result := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("MiniMax-M2.7-highspeed", inputWithCallID, false)

	// Check that tool_call_id is set correctly
	messages := gjson.GetBytes(result, "messages")
	if !messages.IsArray() {
		t.Fatalf("expected messages to be array, got %v", messages.Type)
	}

	// Find the tool message (second message)
	var toolMessage gjson.Result
	for _, msg := range messages.Array() {
		if msg.Get("role").String() == "tool" {
			toolMessage = msg
			break
		}
	}

	if !toolMessage.Exists() {
		t.Fatal("tool message not found")
	}

	toolCallID := toolMessage.Get("tool_call_id").String()
	if toolCallID != "call_123" {
		t.Errorf("expected tool_call_id to be 'call_123', got '%s'", toolCallID)
	}

	t.Logf("Result: %s", string(result))
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_ToolCallOutputWithID(t *testing.T) {
	// Test case: function_call_output with id instead of call_id
	inputWithID := []byte(`{
		"model": "MiniMax-M2.7-highspeed",
		"input": [
			{"type": "function_call", "id": "call_456", "name": "test_tool", "arguments": "{}"},
			{"type": "function_call_output", "id": "call_456", "output": "result"}
		]
	}`)

	result := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("MiniMax-M2.7-highspeed", inputWithID, false)

	// Find the tool message
	var toolMessage gjson.Result
	for _, msg := range gjson.GetBytes(result, "messages").Array() {
		if msg.Get("role").String() == "tool" {
			toolMessage = msg
			break
		}
	}

	if !toolMessage.Exists() {
		t.Fatal("tool message not found")
	}

	toolCallID := toolMessage.Get("tool_call_id").String()
	if toolCallID != "call_456" {
		t.Errorf("expected tool_call_id to be 'call_456', got '%s'", toolCallID)
	}

	t.Logf("Result: %s", string(result))
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_CustomToolCallOutput(t *testing.T) {
	// Test case: custom_tool_call_output
	input := []byte(`{
		"model": "MiniMax-M2.7-highspeed",
		"input": [
			{"type": "custom_tool_call", "call_id": "call_789", "name": "test_tool", "arguments": "{}"},
			{"type": "custom_tool_call_output", "call_id": "call_789", "output": "result"}
		]
	}`)

	result := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("MiniMax-M2.7-highspeed", input, false)

	// Find the tool message
	var toolMessage gjson.Result
	for _, msg := range gjson.GetBytes(result, "messages").Array() {
		if msg.Get("role").String() == "tool" {
			toolMessage = msg
			break
		}
	}

	if !toolMessage.Exists() {
		t.Fatal("tool message not found")
	}

	toolCallID := toolMessage.Get("tool_call_id").String()
	if toolCallID != "call_789" {
		t.Errorf("expected tool_call_id to be 'call_789', got '%s'", toolCallID)
	}

	t.Logf("Result: %s", string(result))
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_NormalizesFunctionArguments(t *testing.T) {
	input := []byte(`{
		"model": "MiniMax-M2.7-highspeed",
		"input": [
			{"type": "function_call", "call_id": "call_object", "name": "object_tool", "arguments": {"city":"Paris"}},
			{"type": "function_call", "call_id": "call_array", "name": "array_tool", "arguments": ["one","two"]},
			{"type": "function_call", "call_id": "call_text", "name": "text_tool", "arguments": "plain text"}
		]
	}`)

	result := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("MiniMax-M2.7-highspeed", input, false)

	if got := gjson.GetBytes(result, "messages.0.tool_calls.0.function.arguments").String(); got != `{"city":"Paris"}` {
		t.Fatalf("object arguments = %q, want JSON object string", got)
	}
	if got := gjson.GetBytes(result, "messages.0.tool_calls.1.function.arguments").String(); got != `["one","two"]` {
		t.Fatalf("array arguments = %q, want JSON array string", got)
	}
	if got := gjson.GetBytes(result, "messages.0.tool_calls.2.function.arguments").String(); got != `{"input":"plain text"}` {
		t.Fatalf("text arguments = %q, want wrapped JSON string", got)
	}
}
