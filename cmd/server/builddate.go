package main

import (
	"time"
)

// parseBuildDate parses a build date string in RFC3339 format, normalizing it to UTC.
// It returns the parsed time and a boolean indicating success.
func parseBuildDate(value string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// resolveBuildDate resolves the build date. If the input is "unknown", none, dev, or empty,
// it returns the current time formatted in RFC3339. Otherwise, if it's a valid date, it normalizes it.
func resolveBuildDate(value string) string {
	if t, ok := parseBuildDate(value); ok {
		return t.Format(time.RFC3339)
	}
	// Fallback to current time if it's unknown/invalid
	return time.Now().UTC().Format(time.RFC3339)
}
