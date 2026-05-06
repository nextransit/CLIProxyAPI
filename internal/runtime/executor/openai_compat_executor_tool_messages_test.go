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
