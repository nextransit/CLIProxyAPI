package apikeypolicy

import "testing"

func TestWildcardMatcher_Match(t *testing.T) {
	t.Parallel()

	matcher, ok := newWildcardMatcher(" claude-3-7-* ")
	if !ok {
		t.Fatal("expected matcher to be created")
	}

	if !matcher.Match("claude-3-7-sonnet") {
		t.Fatal("expected wildcard to match sonnet model")
	}
	if !matcher.Match("CLAUDE-3-7-opus") {
		t.Fatal("expected wildcard to be case-insensitive")
	}
	if matcher.Match("gpt-4o") {
		t.Fatal("expected wildcard not to match unrelated model")
	}
}
