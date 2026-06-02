package executor

import (
	"bytes"
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

func TestSanitizeOpenAICompatThinkingResponseMiniMaxM27(t *testing.T) {
	input := []byte(`{
		"choices":[
			{"message":{"content":"<think>internal reasoning</think>\n{\"ok\":true}"}}
		]
	}`)

	out := sanitizeOpenAICompatThinkingResponse("MiniMax-M2.7-highspeed", input)
	got := gjson.GetBytes(out, "choices.0.message.content").String()
	if got != `{"ok":true}` {
		t.Fatalf("content = %q, want JSON only", got)
	}
}

func TestSanitizeOpenAICompatThinkingResponseMiniMaxM3(t *testing.T) {
	input := []byte(`{
		"choices":[
			{"message":{"content":"<think>internal reasoning</think>\nanswer"}}
		]
	}`)

	out := sanitizeOpenAICompatThinkingResponse("MiniMax-M3", input)
	got := gjson.GetBytes(out, "choices.0.message.content").String()
	if got != "answer" {
		t.Fatalf("content = %q, want answer only", got)
	}
}

func TestSanitizeOpenAICompatThinkingResponseLeavesOtherModels(t *testing.T) {
	input := []byte(`{
		"choices":[
			{"message":{"content":"<think>literal</think>\nanswer"}}
		]
	}`)

	out := sanitizeOpenAICompatThinkingResponse("gpt-test", input)
	got := gjson.GetBytes(out, "choices.0.message.content").String()
	if got != "<think>literal</think>\nanswer" {
		t.Fatalf("content = %q, want original", got)
	}
}

func TestSanitizeOpenAICompatThinkingStreamLine(t *testing.T) {
	input := []byte(`data: {"choices":[{"delta":{"content":"internal reasoning</think>\nanswer"}}]}`)

	out := sanitizeOpenAICompatThinkingStreamLine("minimax/minimax-m2.7", input, nil)
	got := gjson.GetBytes(bytes.TrimSpace(out[len("data: "):]), "choices.0.delta.content").String()
	if got != "answer" {
		t.Fatalf("stream delta content = %q, want answer", got)
	}
}

func TestSanitizeOpenAICompatThinkingStreamLineTracksOpenBlock(t *testing.T) {
	inside := false
	first := []byte(`data: {"choices":[{"delta":{"content":"<think>internal"}}]}`)
	second := []byte(`data: {"choices":[{"delta":{"content":" reasoning"}}]}`)
	third := []byte(`data: {"choices":[{"delta":{"content":"</think>\nanswer"}}]}`)

	firstOut := sanitizeOpenAICompatThinkingStreamLine("minimax/minimax-m2.7", first, &inside)
	secondOut := sanitizeOpenAICompatThinkingStreamLine("minimax/minimax-m2.7", second, &inside)
	thirdOut := sanitizeOpenAICompatThinkingStreamLine("minimax/minimax-m2.7", third, &inside)

	if got := gjson.GetBytes(bytes.TrimSpace(firstOut[len("data: "):]), "choices.0.delta.content").String(); got != "" {
		t.Fatalf("first content = %q, want empty", got)
	}
	if got := gjson.GetBytes(bytes.TrimSpace(secondOut[len("data: "):]), "choices.0.delta.content").String(); got != "" {
		t.Fatalf("second content = %q, want empty", got)
	}
	if got := gjson.GetBytes(bytes.TrimSpace(thirdOut[len("data: "):]), "choices.0.delta.content").String(); got != "answer" {
		t.Fatalf("third content = %q, want answer", got)
	}
	if inside {
		t.Fatalf("inside think state should be reset")
	}
}

func TestNormalizeMiniMaxM3RequestMapsReasoningEffort(t *testing.T) {
	input := []byte(`{"model":"MiniMax-M3","messages":[],"reasoning_effort":"high"}`)

	out := normalizeMiniMaxM3Request(input, "MiniMax-M3")

	if gjson.GetBytes(out, "reasoning_effort").Exists() {
		t.Fatalf("reasoning_effort should be removed, body=%s", string(out))
	}
	if got := gjson.GetBytes(out, "thinking.type").String(); got != "adaptive" {
		t.Fatalf("thinking.type = %q, want adaptive, body=%s", got, string(out))
	}
	if got := gjson.GetBytes(out, "reasoning_split").Bool(); !got {
		t.Fatalf("reasoning_split = %v, want true, body=%s", got, string(out))
	}
}

func TestNormalizeMiniMaxM3RequestDisablesThinking(t *testing.T) {
	input := []byte(`{"model":"MiniMax-M3","messages":[],"reasoning_effort":"none"}`)

	out := normalizeMiniMaxM3Request(input, "MiniMax-M3")

	if got := gjson.GetBytes(out, "thinking.type").String(); got != "disabled" {
		t.Fatalf("thinking.type = %q, want disabled, body=%s", got, string(out))
	}
	if gjson.GetBytes(out, "reasoning_split").Exists() {
		t.Fatalf("reasoning_split should not be set when thinking is disabled, body=%s", string(out))
	}
}

func TestNormalizeMiniMaxM3RequestPreservesReasoningSplit(t *testing.T) {
	input := []byte(`{"model":"MiniMax-M3","messages":[],"thinking":{"type":"adaptive"},"reasoning_split":false}`)

	out := normalizeMiniMaxM3Request(input, "minimax/minimax-m3")

	if got := gjson.GetBytes(out, "reasoning_split").Bool(); got {
		t.Fatalf("reasoning_split = %v, want preserved false, body=%s", got, string(out))
	}
}

func TestNormalizeMiniMaxM3RequestNoReasoningEffortWithThinkingDisabled(t *testing.T) {
	input := []byte(`{"model":"MiniMax-M3","messages":[],"thinking":{"type":"disabled"}}`)

	out := normalizeMiniMaxM3Request(input, "MiniMax-M3")

	if got := gjson.GetBytes(out, "thinking.type").String(); got != "disabled" {
		t.Fatalf("thinking.type = %q, want preserved disabled, body=%s", got, string(out))
	}
	if gjson.GetBytes(out, "reasoning_split").Exists() {
		t.Fatalf("reasoning_split should not be set when thinking is disabled, body=%s", string(out))
	}
}

func TestNormalizeMiniMaxM3RequestNoReasoningEffortWithThinkingAdaptive(t *testing.T) {
	input := []byte(`{"model":"MiniMax-M3","messages":[],"thinking":{"type":"adaptive"}}`)

	out := normalizeMiniMaxM3Request(input, "MiniMax-M3")

	if got := gjson.GetBytes(out, "thinking.type").String(); got != "adaptive" {
		t.Fatalf("thinking.type = %q, want preserved adaptive, body=%s", got, string(out))
	}
	if got := gjson.GetBytes(out, "reasoning_split").Bool(); !got {
		t.Fatalf("reasoning_split = %v, want true when thinking is adaptive, body=%s", got, string(out))
	}
}

func TestNormalizeMiniMaxM3RequestNoReasoningEffortEmptyThinkingDefaultsAdaptive(t *testing.T) {
	input := []byte(`{"model":"MiniMax-M3","messages":[],"thinking":{}}`)

	out := normalizeMiniMaxM3Request(input, "MiniMax-M3")

	if got := gjson.GetBytes(out, "thinking.type").String(); got != "adaptive" {
		t.Fatalf("thinking.type = %q, want adaptive (default), body=%s", got, string(out))
	}
	if got := gjson.GetBytes(out, "reasoning_split").Bool(); !got {
		t.Fatalf("reasoning_split = %v, want true when thinking defaults to adaptive, body=%s", got, string(out))
	}
}
