package openai

import (
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

var codexClientAllowedReasoningLevels = map[string]struct{}{
	"none":   {},
	"low":    {},
	"medium": {},
	"high":   {},
	"xhigh":  {},
}

func (h *OpenAIAPIHandler) codexClientModelsResponse() map[string]any {
	return CodexClientModelsResponse(h.Models())
}

func CodexClientModelsResponse(models []map[string]any) map[string]any {
	return map[string]any{
		"models": buildCodexClientModels(models),
	}
}

func buildCodexClientModels(models []map[string]any) []map[string]any {
	result := make([]map[string]any, 0, len(models))
	for _, model := range models {
		id := strings.TrimSpace(stringModelValue(model, "id"))
		if id == "" {
			continue
		}

		entry := map[string]any{
			"slug":                           id,
			"display_name":                   displayNameForCodexClient(id, model),
			"description":                    descriptionForCodexClient(id, model),
			"default_reasoning_level":        "medium",
			"supported_reasoning_levels":     defaultCodexClientReasoningLevels(),
			"shell_type":                     "shell_command",
			"visibility":                     "list",
			"supported_in_api":               true,
			"priority":                       codexClientModelPriority(id),
			"supports_reasoning_summaries":   true,
			"default_reasoning_summary":      "none",
			"support_verbosity":              true,
			"default_verbosity":              "low",
			"apply_patch_tool_type":          "freeform",
			"supports_parallel_tool_calls":   true,
			"supports_image_detail_original": true,
			"supports_search_tool":           true,
			"input_modalities":               []string{"text", "image"},
		}

		if contextWindow := intModelValue(model, "context_length"); contextWindow > 0 {
			entry["context_window"] = contextWindow
			entry["max_context_window"] = contextWindow
		}

		if info := registry.LookupModelInfo(id); info != nil {
			applyCodexClientRegistryMetadata(entry, info)
		}

		sanitizeCodexClientReasoningMetadata(entry)
		applyCodexClientVisibilityOverride(entry, id)
		result = append(result, entry)
	}

	sort.SliceStable(result, func(i, j int) bool {
		return modelPriorityValue(result[i]) < modelPriorityValue(result[j])
	})

	return result
}

func applyCodexClientRegistryMetadata(entry map[string]any, info *registry.ModelInfo) {
	if info == nil {
		return
	}

	if info.DisplayName != "" {
		entry["display_name"] = info.DisplayName
	}
	if info.Description != "" {
		entry["description"] = info.Description
	}
	if info.ContextLength > 0 {
		entry["context_window"] = info.ContextLength
		entry["max_context_window"] = info.ContextLength
	}
	if looksLikeImageModel(info) {
		entry["visibility"] = "hide"
		entry["input_modalities"] = []string{"text"}
		entry["supports_search_tool"] = false
	}
	applyCodexClientThinkingMetadata(entry, info.Thinking)
}

func displayNameForCodexClient(id string, model map[string]any) string {
	if displayName := stringModelValue(model, "display_name"); displayName != "" {
		return displayName
	}
	return id
}

func descriptionForCodexClient(id string, model map[string]any) string {
	if description := stringModelValue(model, "description"); description != "" {
		return description
	}
	if displayName := stringModelValue(model, "display_name"); displayName != "" {
		return displayName
	}
	return id
}

func defaultCodexClientReasoningLevels() []any {
	return []any{
		map[string]any{"effort": "low", "description": codexClientReasoningDescription("low")},
		map[string]any{"effort": "medium", "description": codexClientReasoningDescription("medium")},
		map[string]any{"effort": "high", "description": codexClientReasoningDescription("high")},
		map[string]any{"effort": "xhigh", "description": codexClientReasoningDescription("xhigh")},
	}
}

func applyCodexClientVisibilityOverride(entry map[string]any, id string) {
	switch strings.TrimSpace(id) {
	case "grok-imagine-image-quality", "gpt-image-2", "grok-imagine-image", "grok-imagine-video", "grok-imagine-video-1.5-preview":
		entry["visibility"] = "hide"
		entry["supports_search_tool"] = false
		entry["input_modalities"] = []string{"text"}
	}
}

func applyCodexClientThinkingMetadata(entry map[string]any, thinking *registry.ThinkingSupport) {
	if thinking == nil || len(thinking.Levels) == 0 {
		return
	}

	levels := make([]any, 0, len(thinking.Levels))
	defaultLevel := ""
	for _, rawLevel := range thinking.Levels {
		level := normalizeCodexClientReasoningLevel(rawLevel)
		if level == "" {
			continue
		}
		levels = append(levels, map[string]any{
			"effort":      level,
			"description": codexClientReasoningDescription(level),
		})
		if defaultLevel == "" && level != "none" {
			defaultLevel = level
		}
		if level == "medium" {
			defaultLevel = level
		}
	}
	if len(levels) == 0 {
		return
	}
	if defaultLevel == "" {
		defaultLevel = "none"
	}

	entry["supported_reasoning_levels"] = levels
	entry["default_reasoning_level"] = defaultLevel
}

func sanitizeCodexClientReasoningMetadata(entry map[string]any) {
	rawLevels, ok := entry["supported_reasoning_levels"].([]any)
	if !ok {
		return
	}

	levels := make([]any, 0, len(rawLevels))
	allowedDefaults := make(map[string]struct{}, len(rawLevels))
	for _, rawLevel := range rawLevels {
		levelEntry, ok := rawLevel.(map[string]any)
		if !ok {
			continue
		}
		level := normalizeCodexClientReasoningLevel(stringModelValue(levelEntry, "effort"))
		if level == "" {
			continue
		}
		levels = append(levels, map[string]any{
			"effort":      level,
			"description": codexClientReasoningDescription(level),
		})
		allowedDefaults[level] = struct{}{}
	}

	if len(levels) == 0 {
		entry["supported_reasoning_levels"] = defaultCodexClientReasoningLevels()
		entry["default_reasoning_level"] = "medium"
		return
	}

	defaultLevel := normalizeCodexClientReasoningLevel(stringModelValue(entry, "default_reasoning_level"))
	if _, ok := allowedDefaults[defaultLevel]; !ok {
		defaultLevel = stringModelValue(levels[0].(map[string]any), "effort")
	}

	entry["supported_reasoning_levels"] = levels
	entry["default_reasoning_level"] = defaultLevel
}

func normalizeCodexClientReasoningLevel(rawLevel string) string {
	level := strings.ToLower(strings.TrimSpace(rawLevel))
	if _, ok := codexClientAllowedReasoningLevels[level]; !ok {
		return ""
	}
	return level
}

func codexClientReasoningDescription(level string) string {
	switch level {
	case "none":
		return "No reasoning"
	case "low":
		return "Fast responses with lighter reasoning"
	case "medium":
		return "Balances speed and reasoning depth for everyday tasks"
	case "high":
		return "Greater reasoning depth for complex problems"
	case "xhigh":
		return "Extra high reasoning depth for complex problems"
	default:
		return level
	}
}

func codexClientModelPriority(id string) int {
	switch strings.TrimSpace(strings.ToLower(id)) {
	case "gpt-5.6-sol":
		return 0
	case "gpt-5.5":
		return 1
	case "deepseek-v4-pro":
		return 2
	case "deepseek-v4-flash":
		return 3
	case "minimax-m3", "minimaxai/minimax-m3", "minimax/minimax-m3", "minimax-claude/minimax-m3":
		return 4
	default:
		return 100
	}
}

func modelPriorityValue(model map[string]any) int {
	switch priority := model["priority"].(type) {
	case int:
		return priority
	case int64:
		return int(priority)
	case float64:
		return int(priority)
	default:
		return 100
	}
}

func looksLikeImageModel(info *registry.ModelInfo) bool {
	if info == nil {
		return false
	}
	normalizedType := strings.ToLower(strings.TrimSpace(info.Type))
	if normalizedType == "image" || normalizedType == "openai-image" {
		return true
	}
	normalizedID := strings.ToLower(strings.TrimSpace(info.ID))
	return strings.Contains(normalizedID, "image") || strings.Contains(normalizedID, "video")
}

func stringModelValue(model map[string]any, key string) string {
	if model == nil {
		return ""
	}
	value, ok := model[key]
	if !ok {
		return ""
	}
	if s, ok := value.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func intModelValue(model map[string]any, key string) int {
	if model == nil {
		return 0
	}
	switch value := model[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}
