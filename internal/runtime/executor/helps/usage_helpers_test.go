package helps

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestParseOpenAIUsageChatCompletions(t *testing.T) {
	data := []byte(`{"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":5}}}`)
	detail := ParseOpenAIUsage(data)
	if detail.InputTokens != 1 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 1)
	}
	if detail.OutputTokens != 2 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 2)
	}
	if detail.TotalTokens != 3 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 3)
	}
	if detail.CachedTokens != 4 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 4)
	}
	if detail.ReasoningTokens != 5 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 5)
	}
}

func TestParseOpenAIUsageResponses(t *testing.T) {
	data := []byte(`{"usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30,"input_tokens_details":{"cached_tokens":7},"output_tokens_details":{"reasoning_tokens":9}}}`)
	detail := ParseOpenAIUsage(data)
	if detail.InputTokens != 10 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 10)
	}
	if detail.OutputTokens != 20 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 20)
	}
	if detail.TotalTokens != 30 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 30)
	}
	if detail.CachedTokens != 7 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 7)
	}
	if detail.ReasoningTokens != 9 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 9)
	}
}

func TestParseOpenAIUsageWithPresenceReturnsFalseForMissingUsage(t *testing.T) {
	data := []byte(`{"id":"resp_1","choices":[{"message":{"content":"hi"}}]}`)
	detail, ok := ParseOpenAIUsageWithPresence(data)
	if ok {
		t.Fatalf("expected no usage detail, got %+v", detail)
	}
}

func TestParseOpenAIUsageWithPresenceReturnsFalseForNullUsage(t *testing.T) {
	data := []byte(`{"usage":null}`)
	detail, ok := ParseOpenAIUsageWithPresence(data)
	if ok {
		t.Fatalf("expected no usage detail, got %+v", detail)
	}
}

func TestParseOpenAIUsageWithPresenceReturnsFalseForEmptyUsageObject(t *testing.T) {
	data := []byte(`{"usage":{}}`)
	detail, ok := ParseOpenAIUsageWithPresence(data)
	if ok {
		t.Fatalf("expected no usage detail, got %+v", detail)
	}
}

func TestParseOpenAIUsageWithPresenceSupportsResponsesStyleFields(t *testing.T) {
	data := []byte(`{"usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30,"input_tokens_details":{"cached_tokens":7},"output_tokens_details":{"reasoning_tokens":9}}}`)
	detail, ok := ParseOpenAIUsageWithPresence(data)
	if !ok {
		t.Fatalf("expected usage detail")
	}
	if detail.InputTokens != 10 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 10)
	}
	if detail.OutputTokens != 20 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 20)
	}
	if detail.TotalTokens != 30 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 30)
	}
	if detail.CachedTokens != 7 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 7)
	}
	if detail.ReasoningTokens != 9 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 9)
	}
}

func TestParseOpenAIStreamUsageIgnoresNullUsage(t *testing.T) {
	line := []byte(`data: {"id":"x","usage":null}`)
	if detail, ok := ParseOpenAIStreamUsage(line); ok {
		t.Fatalf("expected no usage detail, got %+v", detail)
	}
}

func TestParseOpenAIStreamUsageIgnoresEmptyUsageObject(t *testing.T) {
	line := []byte(`data: {"id":"x","usage":{}}`)
	if detail, ok := ParseOpenAIStreamUsage(line); ok {
		t.Fatalf("expected no usage detail, got %+v", detail)
	}
}

func TestParseOpenAIStreamUsageSupportsResponsesStyleFields(t *testing.T) {
	line := []byte(`data: {"id":"x","usage":{"input_tokens":11,"output_tokens":13,"total_tokens":24,"input_tokens_details":{"cached_tokens":2},"output_tokens_details":{"reasoning_tokens":3}}}`)
	detail, ok := ParseOpenAIStreamUsage(line)
	if !ok {
		t.Fatalf("expected usage detail")
	}
	if detail.InputTokens != 11 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 11)
	}
	if detail.OutputTokens != 13 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 13)
	}
	if detail.TotalTokens != 24 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 24)
	}
	if detail.CachedTokens != 2 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 2)
	}
	if detail.ReasoningTokens != 3 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 3)
	}
}

func TestUsageReporterBuildRecordIncludesLatency(t *testing.T) {
	reporter := &UsageReporter{
		provider:    "openai",
		model:       "gpt-5.4",
		requestedAt: time.Now().Add(-1500 * time.Millisecond),
	}

	record := reporter.buildRecord(usage.Detail{TotalTokens: 3}, false)
	if record.Latency < time.Second {
		t.Fatalf("latency = %v, want >= 1s", record.Latency)
	}
	if record.Latency > 3*time.Second {
		t.Fatalf("latency = %v, want <= 3s", record.Latency)
	}
}
