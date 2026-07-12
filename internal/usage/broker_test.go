package usage

import (
	"testing"
	"time"
)

func TestBrokerSubscribeReceivesPublish(t *testing.T) {
	b := NewBroker(100 * time.Millisecond)
	ch, cancel := b.Subscribe()
	defer cancel()

	b.Publish(UsagePayload{TotalRequests: 42})

	select {
	case got := <-ch:
		if got.TotalRequests != 42 {
			t.Fatalf("TotalRequests = %d, want 42", got.TotalRequests)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for publish")
	}
}

func TestBrokerCancelIsIdempotent(t *testing.T) {
	b := NewBroker(50 * time.Millisecond)
	_, cancel := b.Subscribe()
	cancel()
	cancel() // must not panic / double-close
}

func TestBrokerSlowConsumerDrops(t *testing.T) {
	b := NewBroker(50 * time.Millisecond)
	_, cancel := b.Subscribe()
	defer cancel()

	// First publish fills the buffer; we deliberately do NOT drain.
	b.Publish(UsagePayload{TotalRequests: 1})
	// Wait for the debounce + flush to fire so ch is full.
	time.Sleep(120 * time.Millisecond)

	// Second publish should still complete promptly; flush drops stale value.
	done := make(chan struct{})
	go func() {
		b.Publish(UsagePayload{TotalRequests: 2})
		close(done)
	}()

	select {
	case <-done:
		// ok — Publish did not block on slow consumer
	case <-time.After(time.Second):
		t.Fatal("Publish blocked on slow consumer")
	}
}

func TestBrokerBroadcastsToAllSubscribers(t *testing.T) {
	b := NewBroker(50 * time.Millisecond)
	ch1, cancel1 := b.Subscribe()
	defer cancel1()
	ch2, cancel2 := b.Subscribe()
	defer cancel2()

	b.Publish(UsagePayload{TotalRequests: 9})

	for i, ch := range []<-chan UsagePayload{ch1, ch2} {
		select {
		case got := <-ch:
			if got.TotalRequests != 9 {
				t.Fatalf("subscriber %d: TotalRequests = %d, want 9", i, got.TotalRequests)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d timed out", i)
		}
	}
}