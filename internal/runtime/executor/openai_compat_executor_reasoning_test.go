package executor

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestEnsureDeepSeekReasoningContentPatchesAssistantMessages(t *testing.T) {
	input := []byte(`{
		"messages":[
			{"role":"assistant","content":"initial summary"},
			{"role":"assistant","content":"tool planning","tool_calls":[{"id":"call_1","type":"function","function":{"name":"list","arguments":"{}"}}]},
			{"role":"assistant","content":"post tool summary"},
			{"role":"user","content":"continue"},
			{"role":"assistant","content":"already has reasoning","reasoning_content":"keep me"}
		]
	}`)

	out, err := ensureDeepSeekReasoningContent(input)
	if err != nil {
		t.Fatalf("ensureDeepSeekReasoningContent() error = %v", err)
	}

	if got := gjson.GetBytes(out, "messages.0.reasoning_content").String(); got != "initial summary" {
		t.Fatalf("messages.0.reasoning_content = %q, want %q", got, "initial summary")
	}
	if got := gjson.GetBytes(out, "messages.1.reasoning_content").String(); got != "tool planning" {
		t.Fatalf("messages.1.reasoning_content = %q, want %q", got, "tool planning")
	}
	if got := gjson.GetBytes(out, "messages.2.reasoning_content").String(); got != "post tool summary" {
		t.Fatalf("messages.2.reasoning_content = %q, want %q", got, "post tool summary")
	}
	if got := gjson.GetBytes(out, "messages.4.reasoning_content").String(); got != "keep me" {
		t.Fatalf("messages.4.reasoning_content = %q, want %q", got, "keep me")
	}
}

func TestEnsureDeepSeekReasoningContentFallbackPlaceholder(t *testing.T) {
	input := []byte(`{
		"messages":[
			{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}}]}
		]
	}`)

	out, err := ensureDeepSeekReasoningContent(input)
	if err != nil {
		t.Fatalf("ensureDeepSeekReasoningContent() error = %v", err)
	}

	if got := gjson.GetBytes(out, "messages.0.reasoning_content").String(); got != "[reasoning unavailable]" {
		t.Fatalf("messages.0.reasoning_content = %q, want %q", got, "[reasoning unavailable]")
	}
}
