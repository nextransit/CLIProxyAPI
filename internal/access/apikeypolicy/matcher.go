package apikeypolicy

import (
	"regexp"
	"strings"
)

type wildcardMatcher struct {
	pattern string
	re      *regexp.Regexp
}

func newWildcardMatcher(pattern string) (wildcardMatcher, bool) {
	normalized := normalizeModelToken(pattern)
	if normalized == "" {
		return wildcardMatcher{}, false
	}
	escaped := regexp.QuoteMeta(normalized)
	expr := "^" + strings.ReplaceAll(escaped, `\*`, ".*") + "$"
	re, err := regexp.Compile(expr)
	if err != nil {
		return wildcardMatcher{}, false
	}
	return wildcardMatcher{
		pattern: normalized,
		re:      re,
	}, true
}

func (m wildcardMatcher) Match(model string) bool {
	if m.re == nil {
		return false
	}
	return m.re.MatchString(normalizeModelToken(model))
}

func normalizeModelToken(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
