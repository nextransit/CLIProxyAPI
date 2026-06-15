package synthesizer

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// TestConfigSynthesizer_ClaudeKeys_PropagatesWeight ensures the Claude key
// synthesizer copies the user-configured Weight into auth.Attributes so the
// selector can use it for weighted round-robin.
func TestConfigSynthesizer_ClaudeKeys_PropagatesWeight(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
			ClaudeKey: []config.ClaudeKey{
				{APIKey: "sk-A", BaseURL: "https://example.com/a", Weight: 1},
				{APIKey: "sk-B", BaseURL: "https://example.com/b", Weight: 3},
				{APIKey: "sk-C", BaseURL: "https://example.com/c", Weight: 0},
			},
		},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}
	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 3 {
		t.Fatalf("expected 3 auths, got %d", len(auths))
	}
	wantWeights := map[string]string{"sk-A": "1", "sk-B": "3", "sk-C": ""}
	for _, auth := range auths {
		key := auth.Attributes["api_key"]
		want, ok := wantWeights[key]
		if !ok {
			t.Fatalf("unexpected auth with api_key=%q", key)
		}
		got := auth.Attributes["weight"]
		if got != want {
			t.Errorf("auth %q: Attributes[weight] = %q, want %q (weight 0 must be omitted)", key, got, want)
		}
	}
}

// TestConfigSynthesizer_CodexKeys_PropagatesWeight is the Codex-side counterpart.
func TestConfigSynthesizer_CodexKeys_PropagatesWeight(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
			CodexKey: []config.CodexKey{
				{APIKey: "codex-A", BaseURL: "https://example.com/a", Weight: 1},
				{APIKey: "codex-B", BaseURL: "https://example.com/b", Weight: 3},
			},
		},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}
	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 2 {
		t.Fatalf("expected 2 auths, got %d", len(auths))
	}
	wantWeights := map[string]string{"codex-A": "1", "codex-B": "3"}
	for _, auth := range auths {
		key := auth.Attributes["api_key"]
		want, ok := wantWeights[key]
		if !ok {
			t.Fatalf("unexpected auth with api_key=%q", key)
		}
		if got := auth.Attributes["weight"]; got != want {
			t.Errorf("auth %q: Attributes[weight] = %q, want %q", key, got, want)
		}
	}
}

// TestConfigSynthesizer_GeminiKeys_PropagatesWeight covers the Gemini path,
// which already had a Weight field in the config struct but was missing the
// synthesizer plumbing.
func TestConfigSynthesizer_GeminiKeys_PropagatesWeight(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
			GeminiKey: []config.GeminiKey{
				{APIKey: "gem-A", Weight: 1},
				{APIKey: "gem-B", Weight: 3},
			},
		},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}
	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 2 {
		t.Fatalf("expected 2 auths, got %d", len(auths))
	}
	wantWeights := map[string]string{"gem-A": "1", "gem-B": "3"}
	for _, auth := range auths {
		key := auth.Attributes["api_key"]
		want, ok := wantWeights[key]
		if !ok {
			t.Fatalf("unexpected auth with api_key=%q", key)
		}
		if got := auth.Attributes["weight"]; got != want {
			t.Errorf("auth %q: Attributes[weight] = %q, want %q", key, got, want)
		}
	}
}
