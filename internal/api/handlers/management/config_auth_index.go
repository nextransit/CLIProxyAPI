package management

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/watcher/synthesizer"
)

type geminiKeyWithAuthIndex struct {
	config.GeminiKey
	AuthIndex string `json:"auth-index,omitempty"`
}

type claudeKeyWithAuthIndex struct {
	config.ClaudeKey
	AuthIndex string `json:"auth-index,omitempty"`
}

type codexKeyWithAuthIndex struct {
	config.CodexKey
	AuthIndex string `json:"auth-index,omitempty"`
}

type vertexCompatKeyWithAuthIndex struct {
	config.VertexCompatKey
	AuthIndex string `json:"auth-index,omitempty"`
}

type openAICompatibilityAPIKeyWithAuthIndex struct {
	config.OpenAICompatibilityAPIKey
	AuthIndex string `json:"auth-index,omitempty"`
}

type openAICompatibilityWithAuthIndex struct {
	Name                       string                                   `json:"name"`
	Disabled                   bool                                     `json:"disabled"`
	Priority                   int                                      `json:"priority,omitempty"`
	Prefix                     string                                   `json:"prefix,omitempty"`
	BaseURL                    string                                   `json:"base-url"`
	APIKeyEntries              []openAICompatibilityAPIKeyWithAuthIndex `json:"api-key-entries,omitempty"`
	Models                     []config.OpenAICompatibilityModel        `json:"models,omitempty"`
	Headers                    map[string]string                        `json:"headers,omitempty"`
	SessionAffinityMaxRequests *int                                     `json:"session-affinity-max-requests,omitempty"`
	AuthIndex                  string                                   `json:"auth-index,omitempty"`
}

func (h *Handler) liveAuthIndexByID() map[string]string {
	out := map[string]string{}
	if h == nil {
		return out
	}
	h.mu.Lock()
	manager := h.authManager
	h.mu.Unlock()
	if manager == nil {
		return out
	}
	// authManager.List() returns clones, so EnsureIndex only affects these copies.
	for _, auth := range manager.List() {
		if auth == nil {
			continue
		}
		id := strings.TrimSpace(auth.ID)
		if id == "" {
			continue
		}
		idx := strings.TrimSpace(auth.Index)
		if idx == "" {
			idx = auth.EnsureIndex()
		}
		if idx == "" {
			continue
		}
		out[id] = idx
	}
	return out
}

func (h *Handler) geminiKeysWithAuthIndex() []geminiKeyWithAuthIndex {
	if h == nil {
		return nil
	}
	liveIndexByID := h.liveAuthIndexByID()

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return nil
	}

	idGen := synthesizer.NewStableIDGenerator()
	out := make([]geminiKeyWithAuthIndex, len(h.cfg.GeminiKey))
	for i := range h.cfg.GeminiKey {
		entry := h.cfg.GeminiKey[i]
		authIndex := ""
		if key := strings.TrimSpace(entry.APIKey); key != "" {
			id, _ := idGen.Next("gemini:apikey", key, entry.BaseURL)
			authIndex = liveIndexByID[id]
		}
		out[i] = geminiKeyWithAuthIndex{
			GeminiKey: entry,
			AuthIndex: authIndex,
		}
	}
	return out
}

func (h *Handler) claudeKeysWithAuthIndex() []claudeKeyWithAuthIndex {
	if h == nil {
		return nil
	}
	liveIndexByID := h.liveAuthIndexByID()

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return nil
	}

	idGen := synthesizer.NewStableIDGenerator()
	out := make([]claudeKeyWithAuthIndex, len(h.cfg.ClaudeKey))
	for i := range h.cfg.ClaudeKey {
		entry := h.cfg.ClaudeKey[i]
		authIndex := ""
		if key := strings.TrimSpace(entry.APIKey); key != "" {
			id, _ := idGen.Next("claude:apikey", key, entry.BaseURL)
			authIndex = liveIndexByID[id]
		}
		out[i] = claudeKeyWithAuthIndex{
			ClaudeKey: entry,
			AuthIndex: authIndex,
		}
	}
	return out
}

func (h *Handler) codexKeysWithAuthIndex() []codexKeyWithAuthIndex {
	if h == nil {
		return nil
	}
	liveIndexByID := h.liveAuthIndexByID()

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return nil
	}

	idGen := synthesizer.NewStableIDGenerator()
	out := make([]codexKeyWithAuthIndex, len(h.cfg.CodexKey))
	for i := range h.cfg.CodexKey {
		entry := h.cfg.CodexKey[i]
		authIndex := ""
		if key := strings.TrimSpace(entry.APIKey); key != "" {
			id, _ := idGen.Next("codex:apikey", key, entry.BaseURL)
			authIndex = liveIndexByID[id]
		}
		out[i] = codexKeyWithAuthIndex{
			CodexKey:  entry,
			AuthIndex: authIndex,
		}
	}
	return out
}

func (h *Handler) vertexCompatKeysWithAuthIndex() []vertexCompatKeyWithAuthIndex {
	if h == nil {
		return nil
	}
	liveIndexByID := h.liveAuthIndexByID()

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return nil
	}

	idGen := synthesizer.NewStableIDGenerator()
	out := make([]vertexCompatKeyWithAuthIndex, len(h.cfg.VertexCompatAPIKey))
	for i := range h.cfg.VertexCompatAPIKey {
		entry := h.cfg.VertexCompatAPIKey[i]
		id, _ := idGen.Next("vertex:apikey", entry.APIKey, entry.BaseURL, entry.ProxyURL)
		authIndex := liveIndexByID[id]
		out[i] = vertexCompatKeyWithAuthIndex{
			VertexCompatKey: entry,
			AuthIndex:       authIndex,
		}
	}
	return out
}

func (h *Handler) openAICompatibilityWithAuthIndex() []openAICompatibilityWithAuthIndex {
	if h == nil {
		return nil
	}
	liveIndexByID := h.liveAuthIndexByID()

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return nil
	}

	normalized := normalizedOpenAICompatibilityEntries(h.cfg.OpenAICompatibility)
	out := make([]openAICompatibilityWithAuthIndex, len(normalized))
	idGen := synthesizer.NewStableIDGenerator()
	for i := range normalized {
		entry := normalized[i]
		providerName := strings.ToLower(strings.TrimSpace(entry.Name))
		if providerName == "" {
			providerName = "openai-compatibility"
		}
		idKind := fmt.Sprintf("openai-compatibility:%s", providerName)

		response := openAICompatibilityWithAuthIndex{
			Name:                       entry.Name,
			Disabled:                   entry.Disabled,
			Priority:                   entry.Priority,
			Prefix:                     entry.Prefix,
			BaseURL:                    entry.BaseURL,
			Models:                     entry.Models,
			Headers:                    entry.Headers,
			SessionAffinityMaxRequests: entry.SessionAffinityMaxRequests,
			AuthIndex:                  "",
		}
		if len(entry.APIKeyEntries) == 0 {
			id, _ := idGen.Next(idKind, entry.BaseURL)
			response.AuthIndex = liveIndexByID[id]
		} else {
			response.APIKeyEntries = make([]openAICompatibilityAPIKeyWithAuthIndex, len(entry.APIKeyEntries))
			for j := range entry.APIKeyEntries {
				apiKeyEntry := entry.APIKeyEntries[j]
				id, _ := idGen.Next(idKind, apiKeyEntry.APIKey, entry.BaseURL, apiKeyEntry.ProxyURL)
				response.APIKeyEntries[j] = openAICompatibilityAPIKeyWithAuthIndex{
					OpenAICompatibilityAPIKey: apiKeyEntry,
					AuthIndex:                 liveIndexByID[id],
				}
			}
		}
		out[i] = response
	}
	return out
}

// authIndexDisplayMap returns a map from AuthIndex to display name (e.g., "Claude #1").
// This is used to format usage statistics for display in the dashboard.
func (h *Handler) authIndexDisplayMap() map[string]string {
	out := map[string]string{}
	if h == nil {
		return out
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return out
	}

	// Build id -> AuthIndex mapping from authManager (inlined from liveAuthIndexByID)
	authIndexByID := map[string]string{}
	if manager := h.authManager; manager != nil {
		for _, auth := range manager.List() {
			if auth == nil {
				continue
			}
			id := strings.TrimSpace(auth.ID)
			if id == "" {
				continue
			}
			idx := strings.TrimSpace(auth.Index)
			if idx == "" {
				idx = auth.EnsureIndex()
			}
			if idx == "" {
				continue
			}
			authIndexByID[id] = idx
		}
	}

	idGen := synthesizer.NewStableIDGenerator()

	// Claude keys
	for i := range h.cfg.ClaudeKey {
		entry := h.cfg.ClaudeKey[i]
		if key := strings.TrimSpace(entry.APIKey); key != "" {
			id, _ := idGen.Next("claude:apikey", key, entry.BaseURL)
			if authIndex := authIndexByID[id]; authIndex != "" {
				out[authIndex] = fmt.Sprintf("Claude #%d", i+1)
			}
		}
	}

	// Gemini keys
	for i := range h.cfg.GeminiKey {
		entry := h.cfg.GeminiKey[i]
		if key := strings.TrimSpace(entry.APIKey); key != "" {
			id, _ := idGen.Next("gemini:apikey", key, entry.BaseURL)
			if authIndex := authIndexByID[id]; authIndex != "" {
				out[authIndex] = fmt.Sprintf("Gemini #%d", i+1)
			}
		}
	}

	// Codex keys
	for i := range h.cfg.CodexKey {
		entry := h.cfg.CodexKey[i]
		if key := strings.TrimSpace(entry.APIKey); key != "" {
			id, _ := idGen.Next("codex:apikey", key, entry.BaseURL)
			if authIndex := authIndexByID[id]; authIndex != "" {
				out[authIndex] = fmt.Sprintf("Codex #%d", i+1)
			}
		}
	}

	// Vertex keys
	for i := range h.cfg.VertexCompatAPIKey {
		entry := h.cfg.VertexCompatAPIKey[i]
		id, _ := idGen.Next("vertex:apikey", entry.APIKey, entry.BaseURL, entry.ProxyURL)
		if authIndex := authIndexByID[id]; authIndex != "" {
			out[authIndex] = fmt.Sprintf("Vertex #%d", i+1)
		}
	}

	// OpenAI Compatibility
	normalized := normalizedOpenAICompatibilityEntries(h.cfg.OpenAICompatibility)
	for i, entry := range normalized {
		providerName := strings.ToLower(strings.TrimSpace(entry.Name))
		if providerName == "" {
			providerName = "openai"
		}
		idKind := fmt.Sprintf("openai-compatibility:%s", providerName)
		if len(entry.APIKeyEntries) == 0 {
			id, _ := idGen.Next(idKind, entry.BaseURL)
			if authIndex := authIndexByID[id]; authIndex != "" {
				out[authIndex] = fmt.Sprintf("%s #%d", providerName, i+1)
			}
		} else {
			for j := range entry.APIKeyEntries {
				apiKeyEntry := entry.APIKeyEntries[j]
				id, _ := idGen.Next(idKind, apiKeyEntry.APIKey, entry.BaseURL, apiKeyEntry.ProxyURL)
				if authIndex := authIndexByID[id]; authIndex != "" {
					out[authIndex] = fmt.Sprintf("%s #%d.%d", providerName, i+1, j+1)
				}
			}
		}
	}

	return out
}
