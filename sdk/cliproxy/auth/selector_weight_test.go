package auth

import (
	"context"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

func TestRoundRobinSelectorPick_WeightedRoundRobin(t *testing.T) {
	t.Parallel()

	selector := &RoundRobinSelector{}
	auths := []*Auth{
		{ID: "key-a", Provider: "openai", Attributes: map[string]string{"weight": "1"}},
		{ID: "key-b", Provider: "openai", Attributes: map[string]string{"weight": "3"}},
	}

	counts := map[string]int{}
	for i := 0; i < 100; i++ {
		got, err := selector.Pick(
			context.Background(),
			"openai",
			"",
			cliproxyexecutor.Options{},
			auths,
		)
		if err != nil {
			t.Fatalf("Pick() #%d error = %v", i, err)
		}
		if got == nil {
			t.Fatalf("Pick() #%d auth = nil", i)
		}
		counts[got.ID]++
	}

	if counts["key-a"] != 25 {
		t.Errorf("key-a selected %d times, want 25", counts["key-a"])
	}
	if counts["key-b"] != 75 {
		t.Errorf("key-b selected %d times, want 75", counts["key-b"])
	}
}
