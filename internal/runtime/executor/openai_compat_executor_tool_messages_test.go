package executor

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestNormalizeOpenAICompatToolMessagesRepairsToolSequence(t *testing.T) {
	input := []byte(`{
		"messages":[
			{"role":"user","content":"start"},
			{"role":"assistant","tool_calls":[
				{"id":"call_a","type":"function","function":{"name":"tool_a","arguments":"{}"}},
				{"id":"call_b","type":"function","function":{"name":"tool_b","arguments":{"city":"Paris"}}},
				{"id":"call_c","type":"function","function":{"name":"tool_c","arguments":"plain text"}}
			]},
			{"role":"assistant","content":"queued work"},
			{"role":"user","content":"interleaved"},
			{"role":"tool","tool_call_id":"call_a","content":{"ok":true}},
			{"role":"tool","call_id":"call_b","output":["done"]},
			{"role":"assistant","content":"after tools"}
		]
	}`)

	out, err := normalizeOpenAICompatToolMessages(input)
	if err != nil {
		t.Fatalf("normalizeOpenAICompatToolMessages() error = %v", err)
	}

	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 7 {
		t.Fatalf("messages len = %d, want 7: %s", len(messages), gjson.GetBytes(out, "messages").Raw)
	}

	if got := messages[1].Get("role").String(); got != "assistant" {
		t.Fatalf("messages[1].role = %q, want assistant", got)
	}
	if got := messages[1].Get("content").String(); got != "queued work" {
		t.Fatalf("assistant content = %q, want queued work", got)
	}
	if got := messages[1].Get("tool_calls.1.function.arguments").String(); got != `{"city":"Paris"}` {
		t.Fatalf("call_b arguments = %q, want JSON object string", got)
	}
	if got := messages[1].Get("tool_calls.2.function.arguments").String(); got != `{"input":"plain text"}` {
		t.Fatalf("call_c arguments = %q, want wrapped JSON string", got)
	}
	for idx, callID := range []string{"call_a", "call_b", "call_c"} {
		msg := messages[2+idx]
		if got := msg.Get("role").String(); got != "tool" {
			t.Fatalf("messages[%d].role = %q, want tool", 2+idx, got)
		}
		if got := msg.Get("tool_call_id").String(); got != callID {
			t.Fatalf("messages[%d].tool_call_id = %q, want %q", 2+idx, got, callID)
		}
		if msg.Get("content").Type != gjson.String {
			t.Fatalf("messages[%d].content must be a string: %s", 2+idx, msg.Raw)
		}
	}
	if got := messages[2].Get("content").String(); got != `{"ok":true}` {
		t.Fatalf("call_a content = %q, want JSON string", got)
	}
	if got := messages[3].Get("content").String(); got != `["done"]` {
		t.Fatalf("call_b content = %q, want output JSON string", got)
	}
	if got := messages[4].Get("content").String(); !strings.Contains(got, "tool result missing") {
		t.Fatalf("call_c synthesized content = %q, want missing-tool error", got)
	}
	if got := messages[5].Get("role").String(); got != "user" {
		t.Fatalf("messages[5].role = %q, want user", got)
	}
	if got := messages[6].Get("content").String(); got != "after tools" {
		t.Fatalf("messages[6].content = %q, want after tools", got)
	}
}

func TestNormalizeOpenAICompatToolMessagesPreservesOrphanToolContent(t *testing.T) {
	largeToolOutput := strings.Repeat("tool-output-", 12000)
	input := []byte(`{
		"messages":[
			{"role":"user","content":"start"},
			{"role":"tool","tool_call_id":"orphan_call","content":"` + largeToolOutput + `"},
			{"role":"assistant","content":"after tool"}
		]
	}`)

	out, err := normalizeOpenAICompatToolMessages(input)
	if err != nil {
		t.Fatalf("normalizeOpenAICompatToolMessages() error = %v", err)
	}

	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 3 {
		t.Fatalf("messages len = %d, want 3: %s", len(messages), gjson.GetBytes(out, "messages").Raw)
	}
	if got := messages[1].Get("role").String(); got != "user" {
		t.Fatalf("orphan tool role = %q, want user; message=%s", got, messages[1].Raw)
	}
	content := messages[1].Get("content").String()
	if !strings.Contains(content, "Tool result (orphan_call):") {
		t.Fatalf("orphan tool content missing prefix: %q", content[:min(len(content), 80)])
	}
	if !strings.Contains(content, largeToolOutput) {
		t.Fatalf("large orphan tool output was not preserved")
	}
	if len(out) < len(input)-128 {
		t.Fatalf("normalized body unexpectedly shrank: input=%d output=%d", len(input), len(out))
	}
}

func TestNormalizeOpenAICompatToolMessagesKeepsFutureToolMatch(t *testing.T) {
	input := []byte(`{
		"messages":[
			{"role":"tool","tool_call_id":"call_late","content":"late result"},
			{"role":"assistant","tool_calls":[{"id":"call_late","type":"function","function":{"name":"tool","arguments":"{}"}}]}
		]
	}`)

	out, err := normalizeOpenAICompatToolMessages(input)
	if err != nil {
		t.Fatalf("normalizeOpenAICompatToolMessages() error = %v", err)
	}

	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 2 {
		t.Fatalf("messages len = %d, want 2: %s", len(messages), gjson.GetBytes(out, "messages").Raw)
	}
	if got := messages[0].Get("role").String(); got != "assistant" {
		t.Fatalf("messages[0].role = %q, want assistant", got)
	}
	if got := messages[1].Get("role").String(); got != "tool" {
		t.Fatalf("messages[1].role = %q, want tool", got)
	}
	if got := messages[1].Get("tool_call_id").String(); got != "call_late" {
		t.Fatalf("messages[1].tool_call_id = %q, want call_late", got)
	}
}

// TestNormalizeOpenAICompatToolMessagesBackfillsEmptyFunctionName ensures the
// tool_call normalization patches assistant tool_calls whose function.name is
// empty or missing. Upstream providers such as DeepSeek v4 reject such
// payloads with 400 "invalid tool_call function, function/name cannot be
// empty"; this test guards against that regression for every deepseek-v4
// variant (deepseek-v4-pro, deepseek-v4-pro-unknown, deepseek-v4-flash,
// deepseek-v4-flash-unknown, deepseek-ai/deepseek-v4-pro, ...).
func TestNormalizeOpenAICompatToolMessagesBackfillsEmptyFunctionName(t *testing.T) {
	input := []byte(`{
		"messages":[
			{"role":"user","content":"start"},
			{"role":"assistant","tool_calls":[
				{"id":"call_1","type":"function","function":{"name":"","arguments":"{}"}},
				{"id":"call_2","type":"function","function":{"arguments":"{}"}},
				{"id":"call_3","type":"function","function":{"name":"   ","arguments":"{}"}},
				{"id":"call_4","type":"function","function":{"name":"valid_tool","arguments":"{}"}}
			]},
			{"role":"assistant","content":"queued work"},
			{"role":"user","content":"interleaved"}
		]
	}`)

	out, err := normalizeOpenAICompatToolMessages(input)
	if err != nil {
		t.Fatalf("normalizeOpenAICompatToolMessages() error = %v", err)
	}

	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) < 1 {
		t.Fatalf("messages missing: %s", gjson.GetBytes(out, "messages").Raw)
	}
	var toolCalls []gjson.Result
	for _, msg := range messages {
		if msg.Get("role").String() == "assistant" && msg.Get("tool_calls").IsArray() {
			toolCalls = msg.Get("tool_calls").Array()
			break
		}
	}
	if len(toolCalls) != 4 {
		t.Fatalf("tool_calls len = %d, want 4: %s", len(toolCalls), gjson.GetBytes(out, "messages").Raw)
	}

	// Empty / missing / whitespace-only names must be backfilled with a
	// placeholder so DeepSeek v4 / v4-flash accepts the request.
	if got := strings.TrimSpace(toolCalls[0].Get("function.name").String()); got == "" {
		t.Fatalf("tool_calls[0].function.name should be backfilled, got empty")
	}
	if got := strings.TrimSpace(toolCalls[1].Get("function.name").String()); got == "" {
		t.Fatalf("tool_calls[1].function.name should be backfilled, got empty")
	}
	if got := strings.TrimSpace(toolCalls[2].Get("function.name").String()); got == "" {
		t.Fatalf("tool_calls[2].function.name should be backfilled, got empty")
	}

	// Already-valid names must be preserved unchanged.
	if got := toolCalls[3].Get("function.name").String(); got != "valid_tool" {
		t.Fatalf("tool_calls[3].function.name = %q, want valid_tool", got)
	}

	// Backfilled names should remain distinct (per call) so they don't
	// collide when the same request is forwarded to multiple v4 variants.
	name0 := toolCalls[0].Get("function.name").String()
	name1 := toolCalls[1].Get("function.name").String()
	name2 := toolCalls[2].Get("function.name").String()
	if name0 == name1 || name0 == name2 || name1 == name2 {
		t.Fatalf("backfilled names should be distinct, got %q, %q, %q", name0, name1, name2)
	}

	// arguments must remain valid JSON strings ("{}") — DeepSeek also
	// rejects empty function.arguments.
	for i, tc := range toolCalls {
		if got := tc.Get("function.arguments").String(); got != "{}" {
			t.Fatalf("tool_calls[%d].function.arguments = %q, want {}", i, got)
		}
	}
}

func TestEnsureDeepSeekToolMessageNamesBackfillsFromAssistantToolCalls(t *testing.T) {
	input := []byte(`{
		"messages":[
			{"role":"assistant","tool_calls":[
				{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{}"}},
				{"id":"call_2","type":"function","function":{"name":"","arguments":"{}"}},
				{"id":"call_3","type":"function","function":{"name":"valid_existing","arguments":"{}"}}
			]},
			{"role":"tool","tool_call_id":"call_1","content":"ok"},
			{"role":"tool","tool_call_id":"call_2","content":"ok"},
			{"role":"tool","tool_call_id":"call_3","name":"keep_me","content":"ok"}
		]
	}`)

	out, err := ensureDeepSeekToolMessageNames(input)
	if err != nil {
		t.Fatalf("ensureDeepSeekToolMessageNames() error = %v", err)
	}

	if got := gjson.GetBytes(out, "messages.1.name").String(); got != "read_file" {
		t.Fatalf("messages.1.name = %q, want read_file", got)
	}
	if got := gjson.GetBytes(out, "messages.2.name").String(); got != "tool_call_call_2" {
		t.Fatalf("messages.2.name = %q, want tool_call_call_2", got)
	}
	if got := gjson.GetBytes(out, "messages.3.name").String(); got != "keep_me" {
		t.Fatalf("messages.3.name = %q, want keep_me", got)
	}
}

func TestOpenAICompatToolNormalizationForDeepSeekV4(t *testing.T) {
	input := []byte(`{
		"messages":[
			{"role":"assistant","tool_calls":[
				{"id":"list_directory:1","type":"function","function":{"name":"","arguments":""}}
			]},
			{"role":"tool","tool_call_id":"list_directory:1","content":{"ok":true}}
		]
	}`)

	out, err := normalizeOpenAICompatToolMessages(input)
	if err != nil {
		t.Fatalf("normalizeOpenAICompatToolMessages() error = %v", err)
	}
	out, err = ensureDeepSeekToolMessageNames(out)
	if err != nil {
		t.Fatalf("ensureDeepSeekToolMessageNames() error = %v", err)
	}

	assistantName := gjson.GetBytes(out, "messages.0.tool_calls.0.function.name").String()
	if assistantName != "tool_call_list_directory_1" {
		t.Fatalf("assistant tool_call function.name = %q, want tool_call_list_directory_1", assistantName)
	}
	if got := gjson.GetBytes(out, "messages.0.tool_calls.0.function.arguments").String(); got != "{}" {
		t.Fatalf("assistant tool_call function.arguments = %q, want {}", got)
	}
	if got := gjson.GetBytes(out, "messages.1.name").String(); got != assistantName {
		t.Fatalf("tool message name = %q, want %q", got, assistantName)
	}
	if got := gjson.GetBytes(out, "messages.1.content").String(); got != `{"ok":true}` {
		t.Fatalf("tool message content = %q, want JSON string", got)
	}
}
