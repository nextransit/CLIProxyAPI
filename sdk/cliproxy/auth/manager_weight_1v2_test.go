package auth

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// TestAuthScheduler_WeightedRoundRobin_1v2_PicksTwiceAsOftenForHigherWeight
// is the canonical regression for the user report "weight 1 and 2 are not
// actually used in a 1:2 ratio, the higher weight key is always chosen".
//
// We construct a manager with two claude auths (weight=1, weight=2) on the
// same model, then drive the production scheduling path
// (Manager → authScheduler.pickSingle) and count the picks.
func TestAuthScheduler_WeightedRoundRobin_1v2_PicksTwiceAsOftenForHigherWeight(t *testing.T) {
	// Both auths need to be in the model registry so the scheduler can
	// match the model key to these auths.
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("claude:apikey:1-id", "claude",
		[]*registry.ModelInfo{{ID: "claude-opus-4-6"}})
	reg.RegisterClient("claude:apikey:2-id", "claude",
		[]*registry.ModelInfo{{ID: "claude-opus-4-6"}})

	mgr := NewManager(nil, &RoundRobinSelector{}, nil)
	rec := newRecordingExecutor("claude")
	mgr.RegisterExecutor(rec)

	authA := &Auth{
		ID:         "claude:apikey:1-id",
		Provider:   "claude",
		Attributes: map[string]string{"weight": "1"},
		Metadata:   map[string]any{"type": "claude"},
	}
	authB := &Auth{
		ID:         "claude:apikey:2-id",
		Provider:   "claude",
		Attributes: map[string]string{"weight": "2"},
		Metadata:   map[string]any{"type": "claude"},
	}
	if _, err := mgr.Register(context.Background(), authA); err != nil {
		t.Fatalf("register A: %v", err)
	}
	if _, err := mgr.Register(context.Background(), authB); err != nil {
		t.Fatalf("register B: %v", err)
	}

	// Drive Execute repeatedly and count via the recording executor which
	// remembers the most recent auth ID it saw.
	const runs = 60
	for i := 0; i < runs; i++ {
		if _, err := mgr.Execute(context.Background(),
			[]string{"claude"},
			cliproxyexecutor.Request{Model: "claude-opus-4-6"},
			cliproxyexecutor.Options{},
		); err != nil {
			t.Fatalf("execute %d: %v", i, err)
		}
	}

	rec.mu.Lock()
	counts := map[string]int{}
	for k, v := range rec.counts {
		counts[k] = v
	}
	rec.mu.Unlock()
	gotA, gotB := counts["claude:apikey:1-id"], counts["claude:apikey:2-id"]
	if gotA == 0 && gotB == 0 {
		t.Fatalf("recording executor saw no picks: %+v", counts)
	}

	// Weighted 1:2 means B should be roughly 2x A.
	// Allow a generous margin to keep the test stable.
	if gotB < 2*gotA-5 {
		t.Errorf("expected weight=2 key to be picked at least ~2x as often as weight=1, got A=%d B=%d (ratio %.2f)", gotA, gotB, float64(gotB)/float64(max(gotA, 1)))
	}
	if gotA < 10 {
		t.Errorf("expected weight=1 key to be picked at least 10 times in 60 runs, got %d", gotA)
	}
	t.Logf("distribution after %d runs: weight=1 -> %d, weight=2 -> %d (ratio %.2f)", runs, gotA, gotB, float64(gotB)/float64(max(gotA, 1)))
	_ = time.Now()
}
