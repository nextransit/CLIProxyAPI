package tui

import "testing"

func TestNextAuthPageSizeCyclesWithinSupportedRange(t *testing.T) {
	if got := nextAuthPageSize(20); got != 50 {
		t.Fatalf("nextAuthPageSize(20) = %d, want 50", got)
	}
	if got := nextAuthPageSize(50); got != 100 {
		t.Fatalf("nextAuthPageSize(50) = %d, want 100", got)
	}
	if got := nextAuthPageSize(100); got != 20 {
		t.Fatalf("nextAuthPageSize(100) = %d, want 20", got)
	}
	if got := nextAuthPageSize(999); got != 20 {
		t.Fatalf("nextAuthPageSize(999) = %d, want 20", got)
	}
}
