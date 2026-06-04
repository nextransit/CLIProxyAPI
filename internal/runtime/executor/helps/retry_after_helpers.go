package helps

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	resetAtRFC3339Pattern              = regexp.MustCompile(`(?i)\breset(?:s)?\s+at\s+([0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]+)?(?:Z|[+-][0-9]{2}:[0-9]{2}))`)
	resetAtParenthesizedSecondsPattern = regexp.MustCompile(`(?i)\breset(?:s)?\s+at\b[^\r\n()]*\(([0-9]{1,8})\)`)
	resetInSecondsPattern              = regexp.MustCompile(`(?i)\breset(?:s)?\s+in\s+([0-9]{1,8})\s*(?:s|sec|secs|second|seconds)?\b`)
	retryAfterTextSecondsPattern       = regexp.MustCompile(`(?i)\bretry[- ]?after\s+([0-9]{1,8})\s*(?:s|sec|secs|second|seconds)?\b`)
)

// ParseRetryAfter extracts a Retry-After hint from an HTTP response. It honors
// the standard `Retry-After` header (delta-seconds or HTTP-date) and falls back
// to provider-specific retry hints in the response body. Returns nil if no
// usable hint is present.
func ParseRetryAfter(resp *http.Response, body []byte) *time.Duration {
	now := time.Now()
	if resp != nil {
		if h := strings.TrimSpace(resp.Header.Get("Retry-After")); h != "" {
			if d, ok := parseRetryAfterValue(h, now); ok {
				return &d
			}
		}
	}
	if len(body) == 0 {
		return nil
	}
	return parseRetryAfterBody(body, now)
}

func parseRetryAfterBody(body []byte, now time.Time) *time.Duration {
	var env struct {
		Error struct {
			RetryAfter int    `json:"retry_after"`
			RetryDelay string `json:"retryDelay"`
			Message    string `json:"message"`
		} `json:"error"`
		RetryAfter int    `json:"retry_after"`
		RetryDelay string `json:"retryDelay"`
		Message    string `json:"message"`
	}
	if err := json.Unmarshal(body, &env); err == nil {
		if env.Error.RetryAfter > 0 {
			d := time.Duration(env.Error.RetryAfter) * time.Second
			return &d
		}
		if env.RetryAfter > 0 {
			d := time.Duration(env.RetryAfter) * time.Second
			return &d
		}
		for _, s := range []string{env.Error.RetryDelay, env.RetryDelay} {
			if d := parseRetryAfterDurationString(s, now); d != nil {
				return d
			}
		}
		for _, s := range []string{env.Error.Message, env.Message} {
			if d := parseRetryAfterText(s, now); d != nil {
				return d
			}
		}
	}
	return parseRetryAfterText(string(body), now)
}

func parseRetryAfterDurationString(s string, now time.Time) *time.Duration {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if parsed, err := time.ParseDuration(s); err == nil && parsed > 0 {
		return &parsed
	}
	if d, ok := parseRetryAfterValue(s, now); ok {
		return &d
	}
	return nil
}

func parseRetryAfterText(text string, now time.Time) *time.Duration {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if match := resetAtRFC3339Pattern.FindStringSubmatch(text); len(match) == 2 {
		if t, err := time.Parse(time.RFC3339Nano, match[1]); err == nil {
			if d := t.Sub(now); d > 0 {
				return &d
			}
		}
	}
	for _, pattern := range []*regexp.Regexp{
		resetAtParenthesizedSecondsPattern,
		resetInSecondsPattern,
		retryAfterTextSecondsPattern,
	} {
		if match := pattern.FindStringSubmatch(text); len(match) == 2 {
			if d := durationFromSeconds(match[1]); d != nil {
				return d
			}
		}
	}
	return nil
}

func durationFromSeconds(value string) *time.Duration {
	secs, err := strconv.Atoi(value)
	if err != nil || secs <= 0 {
		return nil
	}
	d := time.Duration(secs) * time.Second
	return &d
}

func parseRetryAfterValue(v string, now time.Time) (time.Duration, bool) {
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0, false
		}
		return time.Duration(secs) * time.Second, true
	}
	for _, layout := range []string{
		time.RFC1123,
		time.RFC1123Z,
		time.RFC850,
		time.ANSIC,
	} {
		if t, err := time.Parse(layout, v); err == nil {
			d := t.Sub(now)
			if d <= 0 {
				return 0, false
			}
			return d, true
		}
	}
	return 0, false
}
