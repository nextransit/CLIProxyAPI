package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
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
	if len(msgSlice) != 20 {
		t.Errorf("expected 20 messages after compact, got %d", len(msgSlice))
	}
	// First message should be system
	first, ok := msgSlice[0].(map[string]interface{})
	if !ok {
		t.Fatal("first message is not a map")
	}
	if first["role"] != "system" {
		t.Errorf("expected first role system, got %v", first["role"])
	}
	// Last message should be msg29
	last, ok := msgSlice[19].(map[string]interface{})
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
	// System (1) + last 4 = 5 items
	if len(inp) != 5 {
		t.Errorf("expected 5 input items after compact, got %d", len(inp))
	}
	first, ok := inp[0].(map[string]interface{})
	if !ok {
		t.Fatal("first input is not a map")
	}
	if first["role"] != "system" {
		t.Errorf("expected first role system, got %v", first["role"])
	}
	last, ok := inp[4].(map[string]interface{})
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
