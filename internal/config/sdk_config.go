// Package config provides configuration management for the CLI Proxy API server.
// It handles loading and parsing YAML configuration files, and provides structured
// access to application settings including server port, authentication directory,
// debug settings, proxy configuration, and API keys.
package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	APIKeyTokenWindowDay   = "day"
	APIKeyTokenWindowMonth = "month"
)

// APIKeyEntry defines a client API key and its optional policy controls.
// It supports both simple scalar YAML entries and structured mapping entries.
type APIKeyEntry struct {
	Key         string              `yaml:"key" json:"key"`
	Name        string              `yaml:"name,omitempty" json:"name,omitempty"`
	Description string              `yaml:"description,omitempty" json:"description,omitempty"`
	Super       bool                `yaml:"super,omitempty" json:"super,omitempty"`
	Models      []string            `yaml:"models,omitempty" json:"models,omitempty"`
	Limits      APIKeyLimitSettings `yaml:"limits,omitempty" json:"limits,omitempty"`
}

// APIKeyLimitSettings groups the supported policy controls for a client API key.
type APIKeyLimitSettings struct {
	Rate        APIKeyRateLimits        `yaml:"rate,omitempty" json:"rate,omitempty"`
	Concurrency APIKeyConcurrencyLimits `yaml:"concurrency,omitempty" json:"concurrency,omitempty"`
	Tokens      APIKeyTokenLimits       `yaml:"tokens,omitempty" json:"tokens,omitempty"`
}

// APIKeyRateLimits describes request frequency limits.
type APIKeyRateLimits struct {
	RPM   int `yaml:"rpm,omitempty" json:"rpm,omitempty"`
	QPS   int `yaml:"qps,omitempty" json:"qps,omitempty"`
	Burst int `yaml:"burst,omitempty" json:"burst,omitempty"`
}

// APIKeyConcurrencyLimits describes in-flight and queueing limits.
type APIKeyConcurrencyLimits struct {
	Max            int `yaml:"max,omitempty" json:"max,omitempty"`
	QueueMax       int `yaml:"queue-max,omitempty" json:"queue-max,omitempty"`
	QueueTimeoutMS int `yaml:"queue-timeout-ms,omitempty" json:"queue-timeout-ms,omitempty"`
}

// APIKeyTokenLimits describes cumulative and periodic token quotas.
type APIKeyTokenLimits struct {
	Lifetime APIKeyLifetimeTokenLimit `yaml:"lifetime,omitempty" json:"lifetime,omitempty"`
	Periodic APIKeyPeriodicTokenLimit `yaml:"periodic,omitempty" json:"periodic,omitempty"`
}

// APIKeyLifetimeTokenLimit describes a cumulative token cap.
type APIKeyLifetimeTokenLimit struct {
	Limit int64 `yaml:"limit,omitempty" json:"limit,omitempty"`
}

// APIKeyPeriodicTokenLimit describes a rolling day/month token cap.
type APIKeyPeriodicTokenLimit struct {
	Limit  int64  `yaml:"limit,omitempty" json:"limit,omitempty"`
	Window string `yaml:"window,omitempty" json:"window,omitempty"`
}

// UnmarshalYAML accepts either a scalar string or a mapping for API key entries.
func (e *APIKeyEntry) UnmarshalYAML(value *yaml.Node) error {
	if e == nil {
		return nil
	}
	switch value.Kind {
	case yaml.ScalarNode:
		e.Key = strings.TrimSpace(value.Value)
		return nil
	case yaml.MappingNode:
		type apiKeyEntryAlias APIKeyEntry
		var alias apiKeyEntryAlias
		if err := value.Decode(&alias); err != nil {
			return err
		}
		*e = APIKeyEntry(alias)
		e.Key = strings.TrimSpace(e.Key)
		return nil
	default:
		return fmt.Errorf("api key entry must be a scalar or mapping")
	}
}

// MarshalYAML emits simple entries as scalars and structured entries as mappings.
func (e APIKeyEntry) MarshalYAML() (any, error) {
	if e.Name == "" &&
		e.Description == "" &&
		!e.Super &&
		len(e.Models) == 0 &&
		e.Limits.Rate == (APIKeyRateLimits{}) &&
		e.Limits.Concurrency == (APIKeyConcurrencyLimits{}) &&
		e.Limits.Tokens == (APIKeyTokenLimits{}) {
		return e.Key, nil
	}

	type apiKeyEntryAlias APIKeyEntry
	return apiKeyEntryAlias(e), nil
}

// SDKConfig represents the application's configuration, loaded from a YAML file.
type SDKConfig struct {
	// ProxyURL is the URL of an optional proxy server to use for outbound requests.
	ProxyURL string `yaml:"proxy-url" json:"proxy-url"`

	// EnableGeminiCLIEndpoint controls whether Gemini CLI internal endpoints (/v1internal:*) are enabled.
	// Default is false for safety; when false, /v1internal:* requests are rejected.
	EnableGeminiCLIEndpoint bool `yaml:"enable-gemini-cli-endpoint" json:"enable-gemini-cli-endpoint"`

	// ForceModelPrefix requires explicit model prefixes (e.g., "teamA/gemini-3-pro-preview")
	// to target prefixed credentials. When false, unprefixed model requests may use prefixed
	// credentials as well.
	ForceModelPrefix bool `yaml:"force-model-prefix" json:"force-model-prefix"`

	// RequestLog enables or disables detailed request logging functionality.
	RequestLog bool `yaml:"request-log" json:"request-log"`

	// APIKeyEntries stores the configured client API keys and their optional policy rules.
	APIKeyEntries []APIKeyEntry `yaml:"api-keys" json:"api-keys"`

	// APIKeys is the normalized plain-string view derived from APIKeyEntries and used by
	// the existing inline authentication provider path.
	APIKeys []string `yaml:"-" json:"-"`

	// PassthroughHeaders controls whether upstream response headers are forwarded to downstream clients.
	// Default is false (disabled).
	PassthroughHeaders bool `yaml:"passthrough-headers" json:"passthrough-headers"`

	// Streaming configures server-side streaming behavior (keep-alives and safe bootstrap retries).
	Streaming StreamingConfig `yaml:"streaming" json:"streaming"`

	// NonStreamKeepAliveInterval controls how often blank lines are emitted for non-streaming responses.
	// <= 0 disables keep-alives. Value is in seconds.
	NonStreamKeepAliveInterval int `yaml:"nonstream-keepalive-interval,omitempty" json:"nonstream-keepalive-interval,omitempty"`
}

// StreamingConfig holds server streaming behavior configuration.
type StreamingConfig struct {
	// KeepAliveSeconds controls how often the server emits SSE heartbeats (": keep-alive\n\n").
	// <= 0 disables keep-alives. Default is 0.
	KeepAliveSeconds int `yaml:"keepalive-seconds,omitempty" json:"keepalive-seconds,omitempty"`

	// BootstrapRetries controls how many times the server may retry a streaming request before any bytes are sent,
	// to allow auth rotation / transient recovery.
	// <= 0 disables bootstrap retries. Default is 0.
	BootstrapRetries int `yaml:"bootstrap-retries,omitempty" json:"bootstrap-retries,omitempty"`
}
