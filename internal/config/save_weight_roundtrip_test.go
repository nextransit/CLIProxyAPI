package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSaveConfigPreserveComments_OpenAICompatWeight covers the round-trip for
// weight on an openai-compatibility API key entry. This is the regression that
// "editing weight, refreshing the page, weight is back to 1" maps onto.
func TestSaveConfigPreserveComments_OpenAICompatWeight(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	initial := `claude-api-key:
  - api-key: sk-A
    base-url: https://api.minimaxi.com/anthropic
openai-compatibility:
  - name: minimax
    base-url: https://api.minimaxi.com/anthropic
    api-key-entries:
      - api-key: sk-cp-first
        weight: 1
      - api-key: sk-cp-second
        weight: 1
`
	if err := os.WriteFile(configPath, []byte(initial), 0o600); err != nil {
		t.Fatalf("write initial: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	// Edit the second entry's weight.
	cfg.OpenAICompatibility[0].APIKeyEntries[1].Weight = 2

	if err := SaveConfigPreserveComments(configPath, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Reload and confirm.
	reloaded, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := reloaded.OpenAICompatibility[0].APIKeyEntries[1].Weight; got != 2 {
		t.Fatalf("expected weight=2 after round-trip, got %d", got)
	}
}
