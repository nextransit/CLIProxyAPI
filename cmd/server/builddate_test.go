package main

import (
	"testing"
	"time"
)

func TestParseBuildDateRejectsInvalidDefaults(t *testing.T) {
	for _, value := range []string{"", "unknown", "none", "dev", "not-a-date"} {
		if _, ok := parseBuildDate(value); ok {
			t.Fatalf("parseBuildDate(%q) accepted an invalid build date", value)
		}
	}
}

func TestParseBuildDateNormalizesRFC3339(t *testing.T) {
	got, ok := parseBuildDate("2026-05-12T23:40:01+08:00")
	if !ok {
		t.Fatal("parseBuildDate rejected a valid build date")
	}

	want := "2026-05-12T15:40:01Z"
	if got.Format(time.RFC3339) != want {
		t.Fatalf("parseBuildDate normalized to %q, want %q", got.Format(time.RFC3339), want)
	}
}

func TestResolveBuildDateNeverReturnsUnknown(t *testing.T) {
	got := resolveBuildDate("unknown")
	if got == "" || got == "unknown" {
		t.Fatalf("resolveBuildDate returned %q, want a valid RFC3339 timestamp", got)
	}

	if _, err := time.Parse(time.RFC3339, got); err != nil {
		t.Fatalf("resolveBuildDate returned %q, not RFC3339: %v", got, err)
	}
}
