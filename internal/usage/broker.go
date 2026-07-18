package usage

import (
	"sync"
	"sync/atomic"
)

const subscriberQueueSize = 64

type subscription struct {
	ch      chan UsageEvent
	dropped atomic.Uint64
}

type Broker struct {
	mu      sync.Mutex
	clients map[*subscription]struct{}
}

func NewBroker() *Broker {
	return &Broker{clients: make(map[*subscription]struct{})}
}

func (b *Broker) Subscribe() (<-chan UsageEvent, func()) {
	sub := &subscription{ch: make(chan UsageEvent, subscriberQueueSize)}
	b.mu.Lock()
	b.clients[sub] = struct{}{}
	b.mu.Unlock()
	cancel := func() {
		b.mu.Lock()
		if _, ok := b.clients[sub]; ok {
			delete(b.clients, sub)
			close(sub.ch)
		}
		b.mu.Unlock()
	}
	return sub.ch, cancel
}

func (b *Broker) Publish(evt UsageEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for sub := range b.clients {
		select {
		case sub.ch <- evt:
		default:
			sub.dropped.Add(1)
		}
	}
}

func (b *Broker) Stats() (subs int, drops uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	subs = len(b.clients)
	for sub := range b.clients {
		drops += sub.dropped.Load()
	}
	return
}