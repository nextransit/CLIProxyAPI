package tui

import "testing"

func TestLogsTabLineMatchesFiltersIncludesSearch(t *testing.T) {
	model := newLogsTabModel(nil, nil)
	model.search = "token_invalidated"
	model.filter = "error"

	matchLine := `[2026-03-27 10:01:00] [error] {"code":"token_invalidated"}`
	if !model.lineMatchesFilters(matchLine) {
		t.Fatalf("lineMatchesFilters(%q) = false, want true", matchLine)
	}

	infoLine := `[2026-03-27 10:01:00] [info] {"code":"token_invalidated"}`
	if model.lineMatchesFilters(infoLine) {
		t.Fatalf("lineMatchesFilters(%q) = true, want false", infoLine)
	}

	otherError := `[2026-03-27 10:01:00] [error] {"code":"other_error"}`
	if model.lineMatchesFilters(otherError) {
		t.Fatalf("lineMatchesFilters(%q) = true, want false", otherError)
	}
}
