package apikeypolicy

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestManagerAcquire_ModelWhitelistAndSuper(t *testing.T) {
	t.Parallel()

	manager := NewManager()
	manager.SetPolicies([]config.APIKeyEntry{
		{Key: "restricted", Models: []string{"gpt-4o*", "claude-*"}},
		{Key: "super", Super: true, Models: []string{"gpt-4o"}},
	})

	if _, err := manager.Acquire(context.Background(), "restricted", "claude-3-7-sonnet"); err != nil {
		t.Fatalf("restricted key allowed model: %v", err)
	}
	if _, err := manager.Acquire(context.Background(), "restricted", "kimi-k2"); err == nil {
		t.Fatal("expected restricted key to reject non-whitelisted model")
	}
	if _, err := manager.Acquire(context.Background(), "super", "kimi-k2"); err != nil {
		t.Fatalf("super key should bypass model checks: %v", err)
	}
}

func TestManagerAcquire_RPMLimit(t *testing.T) {
	t.Parallel()

	manager := NewManager()
	current := time.Date(2026, 4, 2, 10, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return current }
	manager.SetPolicies([]config.APIKeyEntry{
		{
			Key: "k1",
			Limits: config.APIKeyLimitSettings{
				Rate: config.APIKeyRateLimits{RPM: 1},
			},
		},
	})

	if _, err := manager.Acquire(context.Background(), "k1", "gpt-4o"); err != nil {
		t.Fatalf("first request should pass: %v", err)
	}
	if _, err := manager.Acquire(context.Background(), "k1", "gpt-4o"); err == nil {
		t.Fatal("second request in same minute should hit rpm limit")
	}

	current = current.Add(time.Minute)
	if _, err := manager.Acquire(context.Background(), "k1", "gpt-4o"); err != nil {
		t.Fatalf("request after minute rollover should pass: %v", err)
	}
}

func TestManagerAcquire_QPSAndBurst(t *testing.T) {
	t.Parallel()

	manager := NewManager()
	current := time.Date(2026, 4, 2, 10, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return current }
	manager.SetPolicies([]config.APIKeyEntry{
		{
			Key: "k1",
			Limits: config.APIKeyLimitSettings{
				Rate: config.APIKeyRateLimits{
					QPS:   1,
					Burst: 2,
				},
			},
		},
	})

	if _, err := manager.Acquire(context.Background(), "k1", "gpt-4o"); err != nil {
		t.Fatalf("first burst request should pass: %v", err)
	}
	if _, err := manager.Acquire(context.Background(), "k1", "gpt-4o"); err != nil {
		t.Fatalf("second burst request should pass: %v", err)
	}
	if _, err := manager.Acquire(context.Background(), "k1", "gpt-4o"); err == nil {
		t.Fatal("third immediate request should hit qps limiter")
	}

	current = current.Add(time.Second)
	if _, err := manager.Acquire(context.Background(), "k1", "gpt-4o"); err != nil {
		t.Fatalf("request after token refill should pass: %v", err)
	}
}

func TestManagerAcquire_ConcurrencyQueueAndTimeout(t *testing.T) {
	manager := NewManager()
	manager.SetPolicies([]config.APIKeyEntry{
		{
			Key: "k1",
			Limits: config.APIKeyLimitSettings{
				Concurrency: config.APIKeyConcurrencyLimits{
					Max:            1,
					QueueMax:       1,
					QueueTimeoutMS: 120,
				},
			},
		},
	})

	firstLease, err := manager.Acquire(context.Background(), "k1", "gpt-4o")
	if err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}
	defer firstLease.Release()

	secondResult := make(chan error, 1)
	var secondLease Lease
	go func() {
		var acquireErr error
		secondLease, acquireErr = manager.Acquire(context.Background(), "k1", "gpt-4o")
		secondResult <- acquireErr
	}()

	waitersReadyDeadline := time.Now().Add(200 * time.Millisecond)
	for {
		state := manager.stateForKey("k1")
		state.mu.Lock()
		waiters := state.waiters
		state.mu.Unlock()
		if waiters >= 1 {
			break
		}
		if time.Now().After(waitersReadyDeadline) {
			t.Fatal("second acquire did not enter queue in time")
		}
		time.Sleep(1 * time.Millisecond)
	}
	if _, err := manager.Acquire(context.Background(), "k1", "gpt-4o"); err == nil {
		t.Fatal("third acquire should fail because queue is full")
	}

	firstLease.Release()
	if err := <-secondResult; err != nil {
		t.Fatalf("second acquire should be released from queue: %v", err)
	}
	if secondLease != nil {
		secondLease.Release()
	}

	blockingLease, err := manager.Acquire(context.Background(), "k1", "gpt-4o")
	if err != nil {
		t.Fatalf("blocking acquire failed: %v", err)
	}
	defer blockingLease.Release()

	_, timeoutErr := manager.Acquire(context.Background(), "k1", "gpt-4o")
	if timeoutErr == nil {
		t.Fatal("expected queue timeout when slot is not released")
	}
	if errors.Is(timeoutErr, context.DeadlineExceeded) {
		t.Fatalf("expected policy timeout error, got context deadline: %v", timeoutErr)
	}
}

func TestManagerAcquire_TokenLimits(t *testing.T) {
	t.Parallel()

	manager := NewManager()
	current := time.Date(2026, 4, 2, 10, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return current }
	manager.SetPolicies([]config.APIKeyEntry{
		{
			Key: "k1",
			Limits: config.APIKeyLimitSettings{
				Tokens: config.APIKeyTokenLimits{
					Lifetime: config.APIKeyLifetimeTokenLimit{Limit: 100},
					Periodic: config.APIKeyPeriodicTokenLimit{Limit: 50, Window: config.APIKeyTokenWindowDay},
				},
			},
		},
	})

	manager.HandleUsage(context.Background(), coreusage.Record{
		APIKey: "k1",
		Detail: coreusage.Detail{TotalTokens: 40},
	})
	if _, err := manager.Acquire(context.Background(), "k1", "gpt-4o"); err != nil {
		t.Fatalf("expected request to pass before periodic limit: %v", err)
	}

	manager.HandleUsage(context.Background(), coreusage.Record{
		APIKey: "k1",
		Detail: coreusage.Detail{TotalTokens: 10},
	})
	if _, err := manager.Acquire(context.Background(), "k1", "gpt-4o"); err == nil {
		t.Fatal("expected request to fail after periodic limit is consumed")
	}

	current = current.Add(24 * time.Hour)
	if _, err := manager.Acquire(context.Background(), "k1", "gpt-4o"); err != nil {
		t.Fatalf("expected periodic quota reset next day: %v", err)
	}

	manager.HandleUsage(context.Background(), coreusage.Record{
		APIKey: "k1",
		Detail: coreusage.Detail{TotalTokens: 50},
	})
	if _, err := manager.Acquire(context.Background(), "k1", "gpt-4o"); err == nil {
		t.Fatal("expected request to fail once lifetime quota is consumed")
	}
}

func TestManagerResetTokenUsage(t *testing.T) {
	t.Parallel()

	manager := NewManager()
	manager.SetPolicies([]config.APIKeyEntry{
		{Key: "k1"},
		{Key: "k2"},
	})

	manager.HandleUsage(context.Background(), coreusage.Record{
		APIKey: "k1",
		Detail: coreusage.Detail{TotalTokens: 10},
	})
	manager.HandleUsage(context.Background(), coreusage.Record{
		APIKey: "k2",
		Detail: coreusage.Detail{TotalTokens: 20},
	})

	manager.ResetTokenUsage("k1")
	snapshot := manager.Snapshot()
	var used1 int64
	var used2 int64
	for _, item := range snapshot {
		switch item.Key {
		case "k1":
			used1 = item.LifetimeUsed
		case "k2":
			used2 = item.LifetimeUsed
		}
	}
	if used1 != 0 || used2 != 20 {
		t.Fatalf("unexpected usage after single reset: k1=%d k2=%d", used1, used2)
	}

	manager.ResetTokenUsage("")
	snapshot = manager.Snapshot()
	for _, item := range snapshot {
		if item.LifetimeUsed != 0 {
			t.Fatalf("expected all token usage reset, key=%s used=%d", item.Key, item.LifetimeUsed)
		}
	}
}

func TestLeaseRelease_IsIdempotent(t *testing.T) {
	t.Parallel()

	manager := NewManager()
	manager.SetPolicies([]config.APIKeyEntry{
		{
			Key: "k1",
			Limits: config.APIKeyLimitSettings{
				Concurrency: config.APIKeyConcurrencyLimits{Max: 1, QueueMax: 1, QueueTimeoutMS: 1000},
			},
		},
	})

	acquired, err := manager.Acquire(context.Background(), "k1", "gpt-4o")
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if acquired == nil {
		t.Fatal("expected non-nil lease for concurrency-limited key")
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		acquired.Release()
	}()
	go func() {
		defer wg.Done()
		acquired.Release()
	}()
	wg.Wait()

	next, err := manager.Acquire(context.Background(), "k1", "gpt-4o")
	if err != nil {
		t.Fatalf("expected second acquire to pass after release: %v", err)
	}
	if next != nil {
		next.Release()
	}
}
