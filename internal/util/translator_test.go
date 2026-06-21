package util

import "testing"

func TestToolNameMapFromClaudeRequestMapsClaudeCodeVariants(t *testing.T) {
	raw := []byte(`{"tools":[
		{"name":"Agent","input_schema":{"type":"object"}},
		{"name":"AskUserQuestion","input_schema":{"type":"object"}},
		{"name":"Bash","input_schema":{"type":"object"}},
		{"name":"Edit","input_schema":{"type":"object"}},
		{"name":"Read","input_schema":{"type":"object"}},
		{"name":"ScheduleWakeup","input_schema":{"type":"object"}},
		{"name":"Skill","input_schema":{"type":"object"}},
		{"name":"ToolSearch","input_schema":{"type":"object"}},
		{"name":"Workflow","input_schema":{"type":"object"}},
		{"name":"Write","input_schema":{"type":"object"}}
	]}`)

	toolNameMap := ToolNameMapFromClaudeRequest(raw)
	tests := map[string]string{
		"agent":             "Agent",
		"ask_user_question": "AskUserQuestion",
		"bash":              "Bash",
		"edit":              "Edit",
		"read":              "Read",
		"schedule_wakeup":   "ScheduleWakeup",
		"skill":             "Skill",
		"tool_search":       "ToolSearch",
		"workflow":          "Workflow",
		"write":             "Write",
	}

	for upstreamName, wantName := range tests {
		if gotName := MapToolName(toolNameMap, upstreamName); gotName != wantName {
			t.Fatalf("MapToolName(%q) = %q, want %q", upstreamName, gotName, wantName)
		}
	}
}

func TestToolNameMapPrefersExactCanonicalName(t *testing.T) {
	raw := []byte(`{"tools":[
		{"name":"ReadFile","input_schema":{"type":"object"}},
		{"name":"read_file","input_schema":{"type":"object"}}
	]}`)

	toolNameMap := ToolNameMapFromClaudeRequest(raw)
	if gotName := MapToolName(toolNameMap, "read_file"); gotName != "read_file" {
		t.Fatalf("MapToolName(%q) = %q, want %q", "read_file", gotName, "read_file")
	}
	if gotName := MapToolName(toolNameMap, "readfile"); gotName != "ReadFile" {
		t.Fatalf("MapToolName(%q) = %q, want %q", "readfile", gotName, "ReadFile")
	}
}
