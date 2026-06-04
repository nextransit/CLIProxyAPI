package executor

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/tidwall/gjson"
)

func TestMiniMaxExecutor_IsMiniMaxModel(t *testing.T) {
	cfg := &config.Config{}
	e := NewMiniMaxExecutor("minimax", cfg)

	tests := []struct {
		model     string
		isMiniMax bool
	}{
		{"MiniMax-M3", true},
		{"MiniMax-M2.7", true},
		{"MiniMax-M2.5", true},
		{"MiniMax-M2.1", true},
		{"minimaxai/minimax-m3", true},
		{"minimaxai/minimax-m2.7", true},
		{"minimaxai/minimax-m2.5", true},
		{"MiniMax-M2.7-highspeed", true},
		{"gpt-5.4", false},
		{"claude-4.6", false},
		{"deepseek-v3", false},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got := e.isMiniMaxModel(tt.model)
			if got != tt.isMiniMax {
				t.Errorf("isMiniMaxModel(%q) = %v, want %v", tt.model, got, tt.isMiniMax)
			}
		})
	}
}

func TestMiniMaxExecutor_BuildAnthropicURL(t *testing.T) {
	cfg := &config.Config{}
	e := NewMiniMaxExecutor("minimax", cfg)

	tests := []struct {
		baseURL string
		want    string
	}{
		{"https://api.minimax.io/v1", "https://api.minimax.io/v1/anthropic/v1/messages"},
		{"https://api.minimax.io/v1/", "https://api.minimax.io/v1/anthropic/v1/messages"},
		{"https://api.minimaxi.com/v1", "https://api.minimaxi.com/v1/anthropic/v1/messages"},
	}

	for _, tt := range tests {
		t.Run(tt.baseURL, func(t *testing.T) {
			got := e.buildAnthropicURL(tt.baseURL)
			if got != tt.want {
				t.Errorf("buildAnthropicURL(%q) = %v, want %v", tt.baseURL, got, tt.want)
			}
		})
	}
}

func TestMiniMaxExecutor_FilterThinkingBlocks(t *testing.T) {
	cfg := &config.Config{}
	e := NewMiniMaxExecutor("minimax", cfg)

	tests := []struct {
		name             string
		payload          string
		wantContains     string
		dontWantContains string
	}{
		{
			name:             "remove thinking block",
			payload:          `{"content":[{"type":"thinking","thinking":"let me think..."},{"type":"text","text":"final answer"}]}`,
			dontWantContains: `"type":"thinking"`,
			wantContains:     `"type":"text"`,
		},
		{
			name:         "keep only text block",
			payload:      `{"content":[{"type":"text","text":"hello"}]}`,
			wantContains: `"type":"text"`,
		},
		{
			name:             "empty payload",
			payload:          "",
			wantContains:     "",
			dontWantContains: "",
		},
		{
			name:             "invalid json",
			payload:          `{invalid`,
			wantContains:     `{invalid`,
			dontWantContains: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := e.filterThinkingBlocks([]byte(tt.payload))

			if tt.dontWantContains != "" && strings.Contains(string(got), tt.dontWantContains) {
				t.Errorf("filterThinkingBlocks() should not contain %q, got %s", tt.dontWantContains, got)
			}
			if tt.wantContains != "" && !strings.Contains(string(got), tt.wantContains) && strings.Contains(tt.payload, tt.wantContains) {
				t.Errorf("filterThinkingBlocks() should contain %q", tt.wantContains)
			}
		})
	}
}

func TestMiniMaxExecutor_CompactAnthropicPayload(t *testing.T) {
	cfg := &config.Config{}
	e := NewMiniMaxExecutor("minimax", cfg)

	tests := []struct {
		name     string
		payload  string
		maxItems int
		wantLen  int
	}{
		{
			name:     "compact 10 messages to 5",
			payload:  `{"messages":[{"role":"user","content":"msg1"},{"role":"assistant","content":"msg2"},{"role":"user","content":"msg3"},{"role":"assistant","content":"msg4"},{"role":"user","content":"msg5"},{"role":"assistant","content":"msg6"},{"role":"user","content":"msg7"},{"role":"assistant","content":"msg8"},{"role":"user","content":"msg9"},{"role":"assistant","content":"msg10"}]}`,
			maxItems: 5,
			wantLen:  5,
		},
		{
			name:     "compact 10 messages to 3",
			payload:  `{"messages":[{"role":"user","content":"msg1"},{"role":"assistant","content":"msg2"},{"role":"user","content":"msg3"},{"role":"assistant","content":"msg4"},{"role":"user","content":"msg5"},{"role":"assistant","content":"msg6"},{"role":"user","content":"msg7"},{"role":"assistant","content":"msg8"},{"role":"user","content":"msg9"},{"role":"assistant","content":"msg10"}]}`,
			maxItems: 3,
			wantLen:  3,
		},
		{
			name:     "already less than maxItems",
			payload:  `{"messages":[{"role":"user","content":"msg1"},{"role":"assistant","content":"msg2"}]}`,
			maxItems: 5,
			wantLen:  2,
		},
		{
			name:     "empty messages",
			payload:  `{"messages":[]}`,
			maxItems: 5,
			wantLen:  0,
		},
		{
			name:     "invalid json",
			payload:  `{invalid`,
			maxItems: 5,
			wantLen:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := e.compactAnthropicPayload([]byte(tt.payload), tt.maxItems)

			msgs := gjson.GetBytes(got, "messages")
			if msgs.IsArray() && tt.wantLen > 0 {
				if len(msgs.Array()) != tt.wantLen {
					t.Errorf("compactAnthropicPayload() returned %d messages, want %d", len(msgs.Array()), tt.wantLen)
				}
			}
		})
	}
}

func TestMiniMaxExecutor_TranslateToAnthropic(t *testing.T) {
	cfg := &config.Config{}
	e := NewMiniMaxExecutor("minimax", cfg)

	tests := []struct {
		name  string
		model string
	}{
		{"MiniMax M2.7", "MiniMax-M2.7"},
		{"MiniMax M2.5", "MiniMax-M2.5"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := []byte(`{
				"model": "test-model",
				"messages": [
					{"role": "user", "content": "hello"}
				],
				"max_tokens": 1000,
				"reasoning_effort": "high"
			}`)

			got := e.translateToAnthropic(payload, tt.model)

			if !gjson.ValidBytes(got) {
				t.Errorf("translateToAnthropic() returned invalid JSON")
			}

			model := gjson.GetBytes(got, "model")
			if model.String() != tt.model {
				t.Errorf("translateToAnthropic() model = %q, want %q", model.String(), tt.model)
			}

			messages := gjson.GetBytes(got, "messages")
			if !messages.IsArray() {
				t.Errorf("translateToAnthropic() messages is not an array")
			}

			maxTokens := gjson.GetBytes(got, "max_tokens")
			if maxTokens.Int() != 1000 {
				t.Errorf("translateToAnthropic() max_tokens = %d, want 1000", maxTokens.Int())
			}

			reasoningSplit := gjson.GetBytes(got, "reasoning_split")
			if tt.model == "MiniMax-M2.7" && !reasoningSplit.Exists() {
				t.Errorf("translateToAnthropic() should set reasoning_split for M2.7")
			}
		})
	}
}

func TestMiniMaxExecutor_TranslateToAnthropic_ThinkingConfig(t *testing.T) {
	cfg := &config.Config{}
	e := NewMiniMaxExecutor("minimax", cfg)

	tests := []struct {
		name           string
		model          string
		payload        string
		wantReasoning  bool // whether reasoning_split should be set to true
		reasoningGiven bool // whether reasoning_split key should exist at all
	}{
		{
			name:           "M2.7 with thinking enabled",
			model:          "MiniMax-M2.7",
			payload:        `{"model":"test","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`,
			wantReasoning:  true,
			reasoningGiven: true,
		},
		{
			name:           "M2.7 with thinking disabled",
			model:          "MiniMax-M2.7",
			payload:        `{"model":"test","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"none"}`,
			wantReasoning:  false,
			reasoningGiven: false,
		},
		{
			name:           "M2.7 without reasoning_effort",
			model:          "MiniMax-M2.7",
			payload:        `{"model":"test","messages":[{"role":"user","content":"hi"}]}`,
			wantReasoning:  false,
			reasoningGiven: false,
		},
		{
			name:           "M2.5 without reasoning_effort",
			model:          "MiniMax-M2.5",
			payload:        `{"model":"test","messages":[{"role":"user","content":"hi"}]}`,
			wantReasoning:  false,
			reasoningGiven: false,
		},
		{
			name:           "M2.7 with low thinking",
			model:          "MiniMax-M2.7",
			payload:        `{"model":"test","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"low"}`,
			wantReasoning:  true,
			reasoningGiven: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := e.translateToAnthropic([]byte(tt.payload), tt.model)

			reasoningSplit := gjson.GetBytes(got, "reasoning_split")

			if tt.reasoningGiven {
				if !reasoningSplit.Exists() {
					t.Errorf("translateToAnthropic() reasoning_split should exist, got nothing")
				} else if reasoningSplit.Bool() != tt.wantReasoning {
					t.Errorf("translateToAnthropic() reasoning_split = %v, want %v", reasoningSplit.Bool(), tt.wantReasoning)
				}
			} else {
				if reasoningSplit.Exists() {
					t.Errorf("translateToAnthropic() reasoning_split should NOT exist, got %v", reasoningSplit.Bool())
				}
			}
		})
	}
}
