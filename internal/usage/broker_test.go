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