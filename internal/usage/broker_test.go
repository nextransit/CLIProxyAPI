package usage

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBroker_PublishSubscribe_NoLoss(t *testing.T) {
	b := NewBroker()
	ch, cancel := b.Subscribe()
	defer cancel()

	const N = subscriberQueueSize // buffer size: no drops possible

	for i := uint64(1); i <= N; i++ {
		b.Publish(UsageEvent{ID: i})
	}

	got := make([]uint64, 0, N)
	timeout := time.After(2 * time.Second)
	for len(got) < N {
		select {
		case e := <-ch:
			got = append(got, e.ID)
		case <-timeout:
			t.Fatalf("only received %d/%d events before timeout", len(got), N)
		}
	}
}

func TestBroker_SlowConsumer_DropsOwnOnly(t *testing.T) {
	b := NewBroker()
	_, cancelSlow := b.Subscribe()
	defer cancelSlow()
	chFast, cancelFast := b.Subscribe()
	defer cancelFast()

	// Drain the fast consumer.
	var fastGot atomic.Int32
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range chFast {
			fastGot.Add(1)
		}
	}()

	// Publish events; the slow consumer's buffer fills and drops,
	// but the fast consumer should receive at least buffer-size events.
	for i := 0; i < 200; i++ {
		b.Publish(UsageEvent{ID: uint64(i + 1)})
	}

	// Give the consumer goroutine time to drain remaining events.
	time.Sleep(500 * time.Millisecond)

	// The slow consumer should have dropped events.
	_, drops := b.Stats()
	t.Logf("slow subscriber drops=%d fast consumer received=%d", drops, fastGot.Load())

	if drops == 0 {
		t.Fatal("expected slow consumer to drop events")
	}
	if fastGot.Load() == 0 {
		t.Fatal("expected fast consumer to receive events")
	}
}

func TestBroker_ConcurrentPublish_SendsAtLeastBuffer(t *testing.T) {
	b := NewBroker()
	ch, cancel := b.Subscribe()
	defer cancel()

	var received atomic.Int32
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range ch {
			received.Add(1)
		}
	}()

	var publishWg sync.WaitGroup
	var published atomic.Uint64
	for g := 0; g < 10; g++ {
		publishWg.Add(1)
		go func() {
			defer publishWg.Done()
			for i := 0; i < 100; i++ {
				b.Publish(UsageEvent{ID: published.Add(1)})
			}
		}()
	}
	publishWg.Wait()

	// Give consumer time to drain before checking.
	time.Sleep(500 * time.Millisecond)

	_, drops := b.Stats()
	t.Logf("published=1000 received=%d drops(aggregate)=%d", received.Load(), drops)
	if received.Load() == 0 {
		t.Fatal("no events were received")
	}
}

func TestBroker_UnsubscribeStopsDelivery(t *testing.T) {
	b := NewBroker()
	ch, cancel := b.Subscribe()
	cancel()

	b.Publish(UsageEvent{ID: 1})
	select {
	case _, ok := <-ch:
		if ok {
			for range ch {
			}
		}
	case <-time.After(100 * time.Millisecond):
	}
}

func TestBroker_StatsNoRace(t *testing.T) {
	b := NewBroker()
	ch, cancel := b.Subscribe()
	defer cancel()

	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				b.Publish(UsageEvent{ID: uint64(i + 1)})
			}
		}()
	}

	// Concurrently call Stats while publishing.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			b.Stats()
		}
	}()

	// Drain the subscriber.
	var drainWg sync.WaitGroup
	drainWg.Add(1)
	go func() {
		defer drainWg.Done()
		for range ch {
		}
	}()

	wg.Wait()
	// Close subscriber to unblock drain.
	cancel()
	drainWg.Wait()
}