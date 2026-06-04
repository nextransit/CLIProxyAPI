package helps

import (
	"net/http"
	"testing"
	"time"
)

func TestParseRetryAfter_HeaderDeltaSeconds(t *testing.T) {
	hdr := http.Header{}
	hdr.Set("Retry-After", "120")
	resp := &http.Response{Header: hdr}
	got := ParseRetryAfter(resp, nil)
	if got == nil || *got != 120*time.Second {
		t.Fatalf("expected 120s, got %v", got)
	}
}

func TestParseRetryAfter_HeaderHTTPDate(t *testing.T) {
	hdr := http.Header{}
	hdr.Set("Retry-After", time.Now().Add(45*time.Second).UTC().Format(time.RFC1123))
	resp := &http.Response{Header: hdr}
	got := ParseRetryAfter(resp, nil)
	if got == nil || *got < 30*time.Second || *got > 60*time.Second {
		t.Fatalf("expected ~45s, got %v", got)
	}
}

func TestParseRetryAfter_AnthropicBody(t *testing.T) {
	body := []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"...","retry_after":33,"retryDelay":"33s"}}`)
	got := ParseRetryAfter(nil, body)
	if got == nil || *got != 33*time.Second {
		t.Fatalf("expected 33s, got %v", got)
	}
}

func TestParseRetryAfter_MiniMaxUsageLimitMessage(t *testing.T) {
	now := time.Date(2026, 6, 4, 14, 25, 44, 0, time.FixedZone("CST", 8*60*60))
	body := []byte(`{"error":{"message":"usage limit exceeded, 5-hour usage limit reached for Token Plan Max (19834000/19834000 used), resets at 2026-06-04T15:00:00+08:00 (2056)"}}`)

	got := parseRetryAfterBody(body, now)
	if got == nil || *got != 2056*time.Second {
		t.Fatalf("expected 2056s, got %v", got)
	}
}

func TestParseRetryAfter_NoHint(t *testing.T) {
	if got := ParseRetryAfter(nil, []byte(`{"error":{"code":"x"}}`)); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
	if got := ParseRetryAfter(&http.Response{Header: http.Header{}}, nil); got != nil {
		t.Fatalf("expected nil for empty header, got %v", got)
	}
}
