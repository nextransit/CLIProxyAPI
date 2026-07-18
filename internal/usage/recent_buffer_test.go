package usage

import (
	"sync"
	"testing"
)

func TestRecentBuffer_PushSince_Basic(t *testing.T) {
	rb := &RecentBuffer{}
	for i := uint64(1); i <= 5; i++ {
		rb.Push(UsageEvent{ID: i, Model: "gpt-4o"})
	}
	got := rb.Since(2)
	if len(got) != 3 {
		t.Fatalf("want 3 events (3,4,5), got %d", len(got))
	}
	if got[0].ID != 3 || got[2].ID != 5 {
		t.Fatalf("want ids [3,4,5], got [%d,%d,%d]", got[0].ID, got[1].ID, got[2].ID)
	}
}

func TestRecentBuffer_Since_Empty(t *testing.T) {
	rb := &RecentBuffer{}
	if got := rb.Since(0); len(got) != 0 {
		t.Fatalf("want empty, got %d", len(got))
	}
}

func TestRecentBuffer_Since_LargeSince(t *testing.T) {
	rb := &RecentBuffer{}
	rb.Push(UsageEvent{ID: 1})
	if got := rb.Since(100); len(got) != 0 {
		t.Fatalf("want empty (since > all ids), got %d", len(got))
	}
}

func TestRecentBuffer_CapacityWraparound(t *testing.T) {
	rb := &RecentBuffer{}
	// Push more than capacity (256) to test wraparound
	for i := uint64(1); i <= 300; i++ {
		rb.Push(UsageEvent{ID: i})
	}
	// Should only retain last 256 events (ids 45..300)
	got := rb.Since(44)
	if len(got) != 256 {
		t.Fatalf("want 256 events after wraparound, got %d", len(got))
	}
	if got[0].ID != 45 {
		t.Fatalf("want first id=45 after wraparound, got %d", got[0].ID)
	}
	if got[len(got)-1].ID != 300 {
		t.Fatalf("want last id=300, got %d", got[len(got)-1].ID)
	}
}

func TestRecentBuffer_ConcurrentPush(t *testing.T) {
	rb := &RecentBuffer{}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(base int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				id := uint64(base*100 + j + 1)
				rb.Push(UsageEvent{ID: id})
			}
		}(i)
	}
	wg.Wait()
	got := rb.Since(0)
	// Should retain at most 256 events
	if len(got) > 256 {
		t.Fatalf("ring buffer exceeded capacity: got %d", len(got))
	}

}
