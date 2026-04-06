package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestLoadConfigOptional_APIKeyEntries_MixedLegacyAndStructured(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte(`api-keys:
  - legacy-key
  - key: team-a-key
    name: Team A
    description: shared key
    models:
      - "gpt-4o*"
      - " Claude-3-7-Sonnet* "
    limits:
      rate:
        rpm: 120
        qps: 5
        burst: 10
      concurrency:
        max: 3
        queue-max: 50
        queue-timeout-ms: 15000
      tokens:
        lifetime:
          limit: 100000
        periodic:
          limit: 5000
          window: day
`)
	if err := os.WriteFile(configPath, content, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}

	if got, want := cfg.APIKeys, []string{"legacy-key", "team-a-key"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cfg.APIKeys = %#v, want %#v", got, want)
	}
	if len(cfg.APIKeyEntries) != 2 {
		t.Fatalf("expected 2 api key entries, got %d", len(cfg.APIKeyEntries))
	}

	entry := cfg.APIKeyEntries[1]
	if entry.Key != "team-a-key" {
		t.Fatalf("entry.Key = %q, want team-a-key", entry.Key)
	}
	if entry.Name != "Team A" {
		t.Fatalf("entry.Name = %q, want Team A", entry.Name)
	}
	if entry.Description != "shared key" {
		t.Fatalf("entry.Description = %q, want shared key", entry.Description)
	}
	if got, want := entry.Models, []string{"gpt-4o*", "claude-3-7-sonnet*"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("entry.Models = %#v, want %#v", got, want)
	}
	if entry.Limits.Rate.RPM != 120 || entry.Limits.Rate.QPS != 5 || entry.Limits.Rate.Burst != 10 {
		t.Fatalf("unexpected rate limits: %#v", entry.Limits.Rate)
	}
	if entry.Limits.Concurrency.Max != 3 || entry.Limits.Concurrency.QueueMax != 50 || entry.Limits.Concurrency.QueueTimeoutMS != 15000 {
		t.Fatalf("unexpected concurrency limits: %#v", entry.Limits.Concurrency)
	}
	if entry.Limits.Tokens.Lifetime.Limit != 100000 {
		t.Fatalf("lifetime limit = %d, want 100000", entry.Limits.Tokens.Lifetime.Limit)
	}
	if entry.Limits.Tokens.Periodic.Limit != 5000 || entry.Limits.Tokens.Periodic.Window != APIKeyTokenWindowDay {
		t.Fatalf("unexpected periodic limit: %#v", entry.Limits.Tokens.Periodic)
	}
}

func TestConfig_SanitizeAPIKeyEntries_DedupesAndDropsEmpty(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		SDKConfig: SDKConfig{
			APIKeyEntries: []APIKeyEntry{
				{Key: "  alpha  "},
				{Key: ""},
				{Key: "alpha"},
				{Key: "beta", Models: []string{" GPT-4O* ", "", "gpt-4o*"}},
			},
		},
	}

	cfg.SanitizeAPIKeyEntries()

	if got, want := cfg.APIKeys, []string{"alpha", "beta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cfg.APIKeys = %#v, want %#v", got, want)
	}
	if len(cfg.APIKeyEntries) != 2 {
		t.Fatalf("expected 2 sanitized entries, got %d", len(cfg.APIKeyEntries))
	}
	if got, want := cfg.APIKeyEntries[1].Models, []string{"gpt-4o*"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cfg.APIKeyEntries[1].Models = %#v, want %#v", got, want)
	}
}

func TestAPIKeyEntry_MarshalYAML_UsesScalarForSimpleEntry(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		SDKConfig: SDKConfig{
			APIKeyEntries: []APIKeyEntry{
				{Key: "simple-key"},
				{Key: "structured-key", Name: "Structured"},
			},
		},
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("yaml.Marshal() error = %v", err)
	}

	text := string(data)
	if !strings.Contains(text, "- simple-key") {
		t.Fatalf("expected simple entry to marshal as scalar, got:\n%s", text)
	}
	if !strings.Contains(text, "key: structured-key") {
		t.Fatalf("expected structured entry to marshal as mapping, got:\n%s", text)
	}
}
