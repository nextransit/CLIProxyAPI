package executor

import (
	"strconv"
	"testing"
)

func TestToolCallLoopDetection_CountsConsecutiveToolCalls(t *testing.T) {
	// Build 25 consecutive tool call exchanges.
	messages := []string{
		`{"role":"user","content":"find weather"}`,
	}
	for i := 1; i <= 25; i++ {
		id := strconv.Itoa(i)
		messages = append(messages,
			`{"role":"assistant","content":"","tool_calls":[{"id":"call_`+id+`","function":{"name":"get_weather","arguments":"{}"}}]}`,
			`{"role":"tool","tool_call_id":"call_`+id+`","content":"sunny"}`,
		)
	}

	count := countConsecutiveToolCalls(messages)
	if count < 20 {
		t.Errorf("expected circuit breaker to trigger at 20, got %d", count)
	}
}

func TestToolCallLoopDetection_ResetsOnTextResponse(t *testing.T) {
	// A text response resets the tool-call chain.
	messages := []string{
		`{"role":"assistant","content":"","tool_calls":[{"id":"call_1","function":{"name":"get_weather","arguments":"{}"}}]}`,
		`{"role":"tool","tool_call_id":"call_1","content":"sunny"}`,
		`{"role":"assistant","content":"Let me check another source"}`,
		`{"role":"assistant","content":"","tool_calls":[{"id":"call_2","function":{"name":"get_weather","arguments":"{}"}}]}`,
	}

	count := countConsecutiveToolCalls(messages)
	if count != 1 {
		t.Errorf("expected count to reset to 1 after text response, got %d", count)
	}
}

func TestToolCallLoopDetection_EmptyOrFewMessages(t *testing.T) {
	// A short non-tool exchange should not trigger the circuit breaker.
	messages := []string{
		`{"role":"user","content":"hello"}`,
		`{"role":"assistant","content":"Hi!"}`,
	}

	count := countConsecutiveToolCalls(messages)
	if count != 0 {
		t.Errorf("expected 0 for messages without tool calls, got %d", count)
	}
}
