package executor

import (
	"testing"
)

func TestToolCallLoopDetection_CountsConsecutiveToolCalls(t *testing.T) {
	// 模拟连续 25 次 tool_call 场景
	messages := []string{
		`{"role":"user","content":"find weather"}`,
		`{"role":"assistant","content":"","tool_calls":[{"id":"call_1","function":{"name":"get_weather","arguments":"{}"}}]}`,
		`{"role":"tool","tool_call_id":"call_1","content":"sunny"}`,
		`{"role":"assistant","content":"","tool_calls":[{"id":"call_2","function":{"name":"get_weather","arguments":"{}"}}]}`,
		`{"role":"tool","tool_call_id":"call_2","content":"sunny"}`,
		`{"role":"assistant","content":"","tool_calls":[{"id":"call_3","function":{"name":"get_weather","arguments":"{}"}}]}`,
		`{"role":"tool","tool_call_id":"call_3","content":"sunny"}`,
		`{"role":"assistant","content":"","tool_calls":[{"id":"call_4","function":{"name":"get_weather","arguments":"{}"}}]}`,
		`{"role":"tool","tool_call_id":"call_4","content":"sunny"}`,
		`{"role":"assistant","content":"","tool_calls":[{"id":"call_5","function":{"name":"get_weather","arguments":"{}"}}]}`,
		`{"role":"tool","tool_call_id":"call_5","content":"sunny"}`,
		`{"role":"assistant","content":"","tool_calls":[{"id":"call_6","function":{"name":"get_weather","arguments":"{}"}}]}`,
		`{"role":"tool","tool_call_id":"call_6","content":"sunny"}`,
		`{"role":"assistant","content":"","tool_calls":[{"id":"call_7","function":{"name":"get_weather","arguments":"{}"}}]}`,
		`{"role":"tool","tool_call_id":"call_7","content":"sunny"}`,
		`{"role":"assistant","content":"","tool_calls":[{"id":"call_8","function":{"name":"get_weather","arguments":"{}"}}]}`,
		`{"role":"tool","tool_call_id":"call_8","content":"sunny"}`,
		`{"role":"assistant","content":"","tool_calls":[{"id":"call_9","function":{"name":"get_weather","arguments":"{}"}}]}`,
		`{"role":"tool","tool_call_id":"call_9","content":"sunny"}`,
		`{"role":"assistant","content":"","tool_calls":[{"id":"call_10","function":{"name":"get_weather","arguments":"{}"}}]}`,
		`{"role":"tool","tool_call_id":"call_10","content":"sunny"}`,
		`{"role":"assistant","content":"","tool_calls":[{"id":"call_11","function":{"name":"get_weather","arguments":"{}"}}]}`,
		`{"role":"tool","tool_call_id":"call_11","content":"sunny"}`,
		`{"role":"assistant","content":"","tool_calls":[{"id":"call_12","function":{"name":"get_weather","arguments":"{}"}}]}`,
		`{"role":"tool","tool_call_id":"call_12","content":"sunny"}`,
	}

	// 应该触发 circuit breaker
	count := countConsecutiveToolCalls(messages)
	if count < 20 {
		t.Errorf("expected circuit breaker to trigger at 20, got %d", count)
	}
}

func TestToolCallLoopDetection_ResetsOnTextResponse(t *testing.T) {
	// 中间有文本响应，应该重置计数
	messages := []string{
		`{"role":"assistant","content":"","tool_calls":[{"id":"call_1","function":{"name":"get_weather","arguments":"{}"}}]}`,
		`{"role":"tool","tool_call_id":"call_1","content":"sunny"}`,
		`{"role":"assistant","content":"Let me check another source"},`, // 重置
		`{"role":"assistant","content":"","tool_calls":[{"id":"call_2","function":{"name":"get_weather","arguments":"{}"}}]}`,
	}

	count := countConsecutiveToolCalls(messages)
	if count != 1 {
		t.Errorf("expected count to reset to 1 after text response, got %d", count)
	}
}

func TestToolCallLoopDetection_EmptyOrFewMessages(t *testing.T) {
	// 消息太少不应该触发
	messages := []string{
		`{"role":"user","content":"hello"}`,
		`{"role":"assistant","content":"Hi!"}`,
	}

	count := countConsecutiveToolCalls(messages)
	if count != 0 {
		t.Errorf("expected 0 for messages without tool calls, got %d", count)
	}
}