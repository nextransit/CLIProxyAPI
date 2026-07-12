package usage

import (
    "sync"
    "time"
)

// UsagePayload is a minimal subset of the snapshot used for SSE push.
// Full snapshot type lives in logger_plugin.go; we keep this decoupled
// to avoid importing the whole stats package from tests.
type UsagePayload struct {
    TotalRequests int64 `json:"total_requests"`
    TotalTokens   int64 `json:"total_tokens"`
}

type Broker struct {
    debounce time.Duration

    mu      sync.Mutex
    clients map[chan UsagePayload]struct{}

    timerMu sync.Mutex
    pending *time.Timer
    last    UsagePayload
}

func NewBroker(debounce time.Duration) *Broker {
    return &Broker{
        debounce: debounce,
        clients:  make(map[chan UsagePayload]struct{}),
    }
}

func (b *Broker) Subscribe() (<-chan UsagePayload, func()) {
    ch := make(chan UsagePayload, 1)
    b.mu.Lock()
    b.clients[ch] = struct{}{}
    b.mu.Unlock()
    cancel := func() {
        b.mu.Lock()
        if _, ok := b.clients[ch]; ok {
            delete(b.clients, ch)
            close(ch)
        }
        b.mu.Unlock()
    }
    return ch, cancel
}

func (b *Broker) Publish(p UsagePayload) {
    b.timerMu.Lock()
    b.last = p
    if b.pending != nil {
        b.pending.Stop()
    }
    b.pending = time.AfterFunc(b.debounce, b.flush)
    b.timerMu.Unlock()
}

func (b *Broker) flush() {
    b.timerMu.Lock()
    payload := b.last
    b.pending = nil
    b.timerMu.Unlock()

    b.mu.Lock()
    defer b.mu.Unlock()
    for ch := range b.clients {
        select {
        case ch <- payload:
        default:
            // drop stale value; slow consumer skips to newest
        }
    }
}