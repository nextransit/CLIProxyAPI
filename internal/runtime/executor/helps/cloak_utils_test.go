package helps

import "testing"

func TestShouldCloakTreatsClaudeCodeUserAgentsAsOfficial(t *testing.T) {
	for _, userAgent := range []string{
		"claude-cli/2.1.98 (external, cli)",
		"claude-code/1.0",
		" Claude-Code/1.0 ",
	} {
		if ShouldCloak("auto", userAgent) {
			t.Fatalf("ShouldCloak(auto, %q) = true, want false", userAgent)
		}
	}
}
