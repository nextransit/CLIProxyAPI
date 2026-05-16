package executor

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestShouldRouteWebSearchToOpenRouter(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		want     bool
	}{
		{"openrouter provider", "openrouter", true},
		{"minimax provider", "minimax", false},
		{"other provider", "other", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldRouteWebSearchToOpenRouter(tt.provider)
			if got != tt.want {
				t.Errorf("shouldRouteWebSearchToOpenRouter(%s) = %v, want %v", tt.provider, got, tt.want)
			}
		})
	}
}

func TestContainsWebSearchTool(t *testing.T) {
	tests := []struct {
		name   string
		tools  string
		expect bool
	}{
		{
			name:   "web_search_20250305 type",
			tools:  `{"tools":[{"type":"web_search_20250305","name":"web_search"}]}`,
			expect: true,
		},
		{
			name:   "web_search function name",
			tools:  `{"tools":[{"type":"function","name":"web_search"}]}`,
			expect: true,
		},
		{
			name:   "no web_search",
			tools:  `{"tools":[{"type":"function","name":"get_weather"}]}`,
			expect: false,
		},
		{
			name:   "empty tools",
			tools:  `{"tools":[]}`,
			expect: false,
		},
		{
			name:   "no tools field",
			tools:  `{}`,
			expect: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := containsWebSearchTool([]byte(tt.tools))
			if got != tt.expect {
				t.Errorf("containsWebSearchTool() = %v, want %v", got, tt.expect)
			}
		})
	}
}

func TestRouteWebSearchToOpenRouter(t *testing.T) {
	tests := []struct {
		name          string
		payload       string
		expectRouted  bool
		expectTools   int // expected number of tools after routing
	}{
		{
			name:         "removes web_search tool",
			payload:      `{"tools":[{"name":"web_search"},{"name":"get_weather"}]}`,
			expectRouted: true,
			expectTools:  1,
		},
		{
			name:         "no web_search tool",
			payload:      `{"tools":[{"name":"get_weather"}]}`,
			expectRouted: false,
			expectTools:  1,
		},
		{
			name:         "empty tools",
			payload:      `{"tools":[]}`,
			expectRouted: false,
			expectTools:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, routed := routeWebSearchToOpenRouter([]byte(tt.payload))
			if routed != tt.expectRouted {
				t.Errorf("routeWebSearchToOpenRouter() routed = %v, want %v", routed, tt.expectRouted)
			}
			if tt.expectRouted {
				// Check tool count
				toolCount := 0
				gjson.GetBytes(result, "tools").ForEach(func(_, _ gjson.Result) bool {
					toolCount++
					return true
				})
				if toolCount != tt.expectTools {
					t.Errorf("tool count = %d, want %d", toolCount, tt.expectTools)
				}
				// Verify metadata flag is set
				metaVal := gjson.Get(string(result), "_meta.route_web_search_to_openrouter")
				if !metaVal.Exists() || !metaVal.Bool() {
					t.Errorf("expected _meta.route_web_search_to_openrouter to be true")
				}
			}
		})
	}
}