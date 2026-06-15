package auth

import (
	"context"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// TestRoundRobinSelectorPick_WeightOneVsTwo exercises the exact scenario the
// user reported: two claude-api-key entries, one with weight=1 and one with
// weight=2. Over a long enough window, keyB should be picked roughly twice
// as often as keyA.
func TestRoundRobinSelectorPick_WeightOneVsTwo(t *testing.T) {
	selector := &RoundRobinSelector{}

	authA := &Auth{
		ID:         "claude-key-A",
		Provider:   "claude",
		Attributes: map[string]string{"weight": "1"},
	}
	authB := &Auth{
		ID:         "claude-key-B",
		Provider:   "claude",
		Attributes: map[string]string{"weight": "2"},
	}
	auths := []*Auth{authA, authB}

	const picks = 300
	counts := map[string]int{}
	for i := 0; i < picks; i++ {
		picked, err := selector.Pick(context.Background(), "claude", "claude-opus-4-6", cliproxyexecutor.Options{}, auths)
		if err != nil {
			t.Fatalf("Pick %d: %v", i, err)
		}
		counts[picked.ID]++
	}

	// Weighted round-robin: 1:2 → keyA:keyB:keyB, repeated.
	// With 300 picks at 1:2 ratio we expect keyA≈100, keyB≈200.
	// Allow a wide margin to keep the test non-flaky, but tight enough to
	// catch a regression to plain 1:1.
	if got := counts["claude-key-A"]; got < 70 || got > 130 {
		t.Errorf("keyA picked %d times (want ~100, range 70-130)", got)
	}
	if got := counts["claude-key-B"]; got < 170 || got > 230 {
		t.Errorf("keyB picked %d times (want ~200, range 170-230)", got)
	}
	// Sanity: B should be picked approximately 2x as often as A.
	if counts["claude-key-B"] < 2*counts["claude-key-A"]-20 {
		t.Errorf("expected keyB at least ~2x keyA picks, got A=%d B=%d", counts["claude-key-A"], counts["claude-key-B"])
	}
	_ = time.Second
}
