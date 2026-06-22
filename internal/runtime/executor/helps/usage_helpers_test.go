package helps

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
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

func TestParseClaudeUsageIncludesCacheTokensInTotal(t *testing.T) {
	data := []byte(`{"usage":{"input_tokens":13,"output_tokens":4,"cache_read_input_tokens":22000,"cache_creation_input_tokens":31}}`)
	detail := ParseClaudeUsage(data)
	if detail.InputTokens != 13 {
		t.Fatalf("input tokens = %d, want 13", detail.InputTokens)
	}
	if detail.OutputTokens != 4 {
		t.Fatalf("output tokens = %d, want 4", detail.OutputTokens)
	}
	if detail.CachedTokens != 22000 {
		t.Fatalf("cached tokens = %d, want 22000", detail.CachedTokens)
	}
	if detail.TotalTokens != 22048 {
		t.Fatalf("total tokens = %d, want 22048", detail.TotalTokens)
	}
}

func TestParseClaudeStreamUsageIncludesCacheTokensInTotal(t *testing.T) {
	line := []byte(`data: {"type":"message_delta","usage":{"input_tokens":13,"output_tokens":4,"cache_read_input_tokens":22000,"cache_creation_input_tokens":31}}`)
	detail, ok := ParseClaudeStreamUsage(line)
	if !ok {
		t.Fatal("expected usage detail")
	}
	if detail.InputTokens != 13 {
		t.Fatalf("input tokens = %d, want 13", detail.InputTokens)
	}
	if detail.OutputTokens != 4 {
		t.Fatalf("output tokens = %d, want 4", detail.OutputTokens)
	}
	if detail.CachedTokens != 22000 {
		t.Fatalf("cached tokens = %d, want 22000", detail.CachedTokens)
	}
	if detail.TotalTokens != 22048 {
		t.Fatalf("total tokens = %d, want 22048", detail.TotalTokens)
	}
}

func TestParseClaudeStreamUsageReadsMessageStartUsage(t *testing.T) {
	line := []byte(`data: {"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":0,"output_tokens":0,"cache_read_input_tokens":1366,"cache_creation_input_tokens":0}}}`)
	detail, ok := ParseClaudeStreamUsage(line)
	if !ok {
		t.Fatal("expected usage detail")
	}
	if detail.CachedTokens != 1366 {
		t.Fatalf("cached tokens = %d, want 1366", detail.CachedTokens)
	}
	if detail.TotalTokens != 1366 {
		t.Fatalf("total tokens = %d, want 1366", detail.TotalTokens)
	}
}

func TestClaudeStreamUsageAccumulatorCombinesStartAndDeltaUsage(t *testing.T) {
	var accumulator ClaudeStreamUsageAccumulator
	accumulator.AddLine([]byte(`data: {"type":"message_start","message":{"usage":{"input_tokens":0,"output_tokens":0,"cache_read_input_tokens":1366,"cache_creation_input_tokens":0}}}`))
	accumulator.AddLine([]byte(`data: {"type":"message_delta","usage":{"input_tokens":1252,"output_tokens":213,"cache_read_input_tokens":114,"cache_creation_input_tokens":31}}`))

	detail, ok := accumulator.Detail()
	if !ok {
		t.Fatal("expected usage detail")
	}
	if detail.InputTokens != 1252 {
		t.Fatalf("input tokens = %d, want 1252", detail.InputTokens)
	}
	if detail.OutputTokens != 213 {
		t.Fatalf("output tokens = %d, want 213", detail.OutputTokens)
	}
	if detail.CachedTokens != 1480 {
		t.Fatalf("cached tokens = %d, want 1480", detail.CachedTokens)
	}
	if detail.TotalTokens != 2976 {
		t.Fatalf("total tokens = %d, want 2976", detail.TotalTokens)
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

func TestParseOpenAIUsageWithPresenceReturnsFalseForZeroUsagePlaceholder(t *testing.T) {
	data := []byte(`{"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`)
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

func TestParseOpenAIStreamUsageIgnoresZeroUsagePlaceholder(t *testing.T) {
	line := []byte(`data: {"id":"x","usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`)
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

	record := reporter.buildRecord(context.Background(), usage.Detail{TotalTokens: 3}, false)
	if record.Latency < time.Second {
		t.Fatalf("latency = %v, want >= 1s", record.Latency)
	}
	if record.Latency > 3*time.Second {
		t.Fatalf("latency = %v, want <= 3s", record.Latency)
	}
}

func TestUsageReporterBuildRecordIncludesRequestID(t *testing.T) {
	reporter := &UsageReporter{
		provider: "openai",
		model:    "gpt-5.4",
	}
	ctx := logging.WithRequestID(context.Background(), "deadbeef")

	record := reporter.buildRecord(ctx, usage.Detail{TotalTokens: 3}, false)
	if record.RequestID != "deadbeef" {
		t.Fatalf("request id = %q, want deadbeef", record.RequestID)
	}
}

func TestUsageReporterSetThinkingFromPayload_OpenAIReasoningEffort(t *testing.T) {
	reporter := &UsageReporter{}
	reporter.SetThinkingFromPayload([]byte(`{"reasoning_effort":"high"}`))

	record := reporter.buildRecord(context.Background(), usage.Detail{}, false)
	if record.Detail.Thinking == nil {
		t.Fatal("thinking should not be nil")
	}
	if got := record.Detail.Thinking.Intensity; got != "high" {
		t.Fatalf("intensity = %q, want %q", got, "high")
	}
	if got := record.Detail.Thinking.Mode; got != "level" {
		t.Fatalf("mode = %q, want %q", got, "level")
	}
	if got := record.Detail.Thinking.Level; got != "high" {
		t.Fatalf("level = %q, want %q", got, "high")
	}
}

func TestUsageReporterSetThinkingFromPayload_DisabledReasoningEffort(t *testing.T) {
	reporter := &UsageReporter{}
	reporter.SetThinkingFromPayload([]byte(`{"reasoning_effort":"disabled"}`))

	record := reporter.buildRecord(context.Background(), usage.Detail{}, false)
	if record.Detail.Thinking == nil {
		t.Fatal("thinking should not be nil")
	}
	if got := record.Detail.Thinking.Intensity; got != "none" {
		t.Fatalf("intensity = %q, want %q", got, "none")
	}
	if got := record.Detail.Thinking.Mode; got != "none" {
		t.Fatalf("mode = %q, want %q", got, "none")
	}
	if got := record.Detail.Thinking.Level; got != "none" {
		t.Fatalf("level = %q, want %q", got, "none")
	}
}

func TestUsageReporterSetThinkingFromPayload_GeminiBudget(t *testing.T) {
	reporter := &UsageReporter{}
	reporter.SetThinkingFromPayload([]byte(`{"generationConfig":{"thinkingConfig":{"thinkingBudget":8192}}}`))

	record := reporter.buildRecord(context.Background(), usage.Detail{}, false)
	if record.Detail.Thinking == nil {
		t.Fatal("thinking should not be nil")
	}
	if got := record.Detail.Thinking.Mode; got != "budget" {
		t.Fatalf("mode = %q, want %q", got, "budget")
	}
	if record.Detail.Thinking.Budget == nil || *record.Detail.Thinking.Budget != 8192 {
		t.Fatalf("budget = %v, want 8192", record.Detail.Thinking.Budget)
	}
}

func TestUsageReporterSetThinkingFromPayload_ClaudeAdaptive(t *testing.T) {
	reporter := &UsageReporter{}
	reporter.SetThinkingFromPayload([]byte(`{"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}`))

	record := reporter.buildRecord(context.Background(), usage.Detail{}, false)
	if record.Detail.Thinking == nil {
		t.Fatal("thinking should not be nil")
	}
	if got := record.Detail.Thinking.Level; got != "max" {
		t.Fatalf("level = %q, want %q", got, "max")
	}
}

func TestParseOpenAIUsage_DeepSeekCustomCache(t *testing.T) {
	data := []byte(`{"usage":{"prompt_tokens":1000,"completion_tokens":200,"total_tokens":1200,"prompt_cache_hit_tokens":800,"prompt_cache_miss_tokens":200}}`)
	detail := ParseOpenAIUsage(data)
	if detail.InputTokens != 1000 {
		t.Fatalf("input tokens = %d, want 1000", detail.InputTokens)
	}
	if detail.CachedTokens != 800 {
		t.Fatalf("cached tokens = %d, want 800", detail.CachedTokens)
	}
}

func TestParseOpenAIStreamUsage_DeepSeekCustomCache(t *testing.T) {
	line := []byte(`data: {"id":"chatcmpl-test","usage":{"prompt_tokens":1000,"completion_tokens":200,"total_tokens":1200,"prompt_cache_hit_tokens":800,"prompt_cache_miss_tokens":200}}`)
	detail, ok := ParseOpenAIStreamUsage(line)
	if !ok {
		t.Fatal("expected successful stream usage parse")
	}
	if detail.InputTokens != 1000 {
		t.Fatalf("input tokens = %d, want 1000", detail.InputTokens)
	}
	if detail.CachedTokens != 800 {
		t.Fatalf("cached tokens = %d, want 800", detail.CachedTokens)
	}
}
