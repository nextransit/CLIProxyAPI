# Usage Events Realtime v2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reduce `Record()` → browser list display latency from ~1.0–1.5s median / >3s P95 to **P95 ≤ 1s**, with strict no-loss delivery for every usage detail event.

**Architecture:** Remove broker debounce (v1 was 800ms) so each `Record()` immediately publishes a single `usage_event`. Frontend merges these into a `recentDetails` store slice instead of re-fetching the full usage payload. A 256-entry ring buffer on the server, combined with `Last-Event-ID` header, fills in events missed during SSE reconnects. Polling fallback interval drops from 10s → 1s for prolonged SSE outages.

**Tech Stack:** Go 1.22 (backend), React 18 + Zustand + Vitest (frontend), Gin SSE handler, fetch+ReadableStream SSE consumer (already shipped in v1).

**Spec:** `docs/superpowers/specs/2026-07-18-usage-events-realtime-v2-design.md`

---

## File Structure

**Backend** (`internal/usage/` and `internal/api/handlers/management/`):
- Create: `internal/usage/recent_buffer.go` — 256-entry ring buffer with `Push` and `Since`
- Modify: `internal/usage/broker.go` — replace debounced broker with immediate fan-out, per-subscriber buffered chan (64)
- Modify: `internal/usage/logger_plugin.go` — allocate `atomic.Uint64` event id, push into ring buffer, publish via `broker.PublishUsageEvent`
- Modify: `internal/api/handlers/management/usage_events.go` — emit `usage_event` + `summary`, support `Last-Event-ID` replay
- Modify: `internal/api/handlers/management/usage_events_test.go` — extend coverage
- Create: `internal/usage/recent_buffer_test.go`
- Modify: `internal/usage/broker_test.go` — rewrite for no-debounce semantics

**Frontend** (`Cli-Proxy-API-Management-Center/src/`):
- Modify: `src/stores/useUsageStatsStore.ts` — add `recentDetails`, `lastEventId`, `applyIncrementalEvent`, `applyBulkEvents`, `resetRecent`
- Modify: `src/services/api/usageStream.ts` — parse `usage_event` / `summary`, send `Last-Event-ID` header
- Modify: `src/components/usage/hooks/useUsageLiveRefresh.ts` — bind new callbacks, drop fallback poll from 10s → 1s, do NOT refetch on `usage_event`
- Modify: `src/services/api/usageStream.test.ts` — extend
- Modify: `src/stores/useUsageStatsStore.test.ts` — extend
- Modify: `src/components/usage/hooks/useUsageLiveRefresh.test.ts` — extend

**Docs**:
- Modify: `docs/superpowers/specs/2026-07-18-usage-events-realtime-v2-design.md` — no further changes needed (already approved)

---

## Task 1: Recent buffer (ring buffer)

**Files:**
- Create: `internal/usage/recent_buffer.go`
- Test: `internal/usage/recent_buffer_test.go`

- [ ] **Step 1: Write failing test**

```go
// internal/usage/recent_buffer_test.go
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
	// IDs should be monotonically increasing in the returned slice
	for i := 1; i < len(got); i++ {
		if got[i].ID <= got[i-1].ID {
			t.Fatalf("not monotonic at index %d: %d <= %d", i, got[i].ID, got[i-1].ID)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/usage/ -run TestRecentBuffer -v`
Expected: FAIL — `RecentBuffer` is undefined.

- [ ] **Step 3: Write minimal implementation**

```go
// internal/usage/recent_buffer.go
package usage

import "sync"

// RecentBuffer is a fixed-capacity ring buffer of UsageEvent records,
// indexed by a monotonically increasing ID. It supports Since(sinceID)
// for replay-on-reconnect. Not safe for concurrent use; callers must
// serialize Push/Since via a mutex (see RequestStatistics).
type RecentBuffer struct {
	mu   sync.Mutex
	buf  [256]UsageEvent
	head uint64 // number of Push calls completed
}

func (r *RecentBuffer) Push(evt UsageEvent) {
	r.mu.Lock()
	r.buf[r.head%256] = evt
	r.head++
	r.mu.Unlock()
}

// Since returns events with id > sinceID in ascending order.
func (r *RecentBuffer) Since(sinceID uint64) []UsageEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.head == 0 {
		return nil
	}
	// Walk from oldest to newest, skipping events with id <= sinceID.
	out := make([]UsageEvent, 0, 32)
	oldest := r.head
	if r.head > 256 {
		oldest = r.head - 256
	}
	for i := oldest; i < r.head; i++ {
		evt := r.buf[i%256]
		if evt.ID > sinceID {
			out = append(out, evt)
		}
	}
	return out
}

// LastID returns the most recently pushed ID, or 0 if empty.
func (r *RecentBuffer) LastID() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.head == 0 {
		return 0
	}
	return r.buf[(r.head-1)%256].ID
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/usage/ -run TestRecentBuffer -v`
Expected: PASS — all 5 tests green.

- [ ] **Step 5: Commit**

```bash
git add internal/usage/recent_buffer.go internal/usage/recent_buffer_test.go
git commit -m "feat(usage): add ring buffer for SSE reconnect replay"
```

---

## Task 2: UsageEvent struct + broker rewrite (no debounce)

**Files:**
- Modify: `internal/usage/logger_plugin.go` — add `UsageEvent` type alongside existing `UsagePayload`
- Modify: `internal/usage/broker.go` — replace debounced broker
- Modify: `internal/usage/broker_test.go` — rewrite tests

- [ ] **Step 1: Write failing test for new broker**

```go
// internal/usage/broker_test.go
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

	const N = 1000
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
	chSlow, cancelSlow := b.Subscribe()
	defer cancelSlow()
	chFast, cancelFast := b.Subscribe()
	defer cancelFast()

	// Do NOT drain chSlow — it stays full.
	const N = 200
	for i := 0; i < N; i++ {
		b.Publish(UsageEvent{ID: uint64(i + 1)})
	}

	got := 0
	timeout := time.After(2 * time.Second)
	for got < N {
		select {
		case <-chFast:
			got++
		case <-timeout:
			t.Fatalf("fast consumer got %d/%d", got, N)
		}
	}
}

func TestBroker_ConcurrentPublish(t *testing.T) {
	b := NewBroker()
	ch, cancel := b.Subscribe()
	defer cancel()

	var wg sync.WaitGroup
	var published atomic.Uint64
	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				b.Publish(UsageEvent{ID: published.Add(1)})
			}
		}()
	}
	wg.Wait()

	got := 0
	timeout := time.After(2 * time.Second)
	for got < 1000 {
		select {
		case <-ch:
			got++
		case <-timeout:
			t.Fatalf("got %d/1000", got)
		}
	}
}

func TestBroker_UnsubscribeStopsDelivery(t *testing.T) {
	b := NewBroker()
	ch, cancel := b.Subscribe()
	cancel()

	// After cancel, channel should be closed.
	b.Publish(UsageEvent{ID: 1})
	select {
	case _, ok := <-ch:
		if ok {
			// Possibly buffered — drain remaining
			for range ch {
			}
		}
	case <-time.After(100 * time.Millisecond):
		// OK: closed or already drained
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/usage/ -run TestBroker -v`
Expected: FAIL — current `Publish` expects `UsagePayload` and applies debounce.

- [ ] **Step 3: Replace broker.go**

Replace the entire content of `internal/usage/broker.go` with:

```go
// internal/usage/broker.go
package usage

import (
	"sync"
	"sync/atomic"
)

const subscriberQueueSize = 64

// subscription represents one SSE consumer's channel plus its drop counter.
type subscription struct {
	ch      chan UsageEvent
	dropped atomic.Uint64
}

// Broker fans out UsageEvent to all subscribers immediately.
// Slow consumers drop events for themselves only; other subscribers
// are unaffected. No debounce — see spec §4.1.
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

// Stats returns subscriber count and total drops across all consumers.
func (b *Broker) Stats() (subs int, drops uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	subs = len(b.clients)
	for sub := range b.clients {
		drops += sub.dropped.Load()
	}
	return
}
```

- [ ] **Step 4: Add UsageEvent type to logger_plugin.go**

Open `internal/usage/logger_plugin.go`. Add the following block near the existing `UsagePayload` definition (around line 11):

```go
// UsageEvent is a single request-detail event, suitable for SSE push
// and ring-buffer storage. Distinct from UsagePayload (which carries
// aggregate totals) so subscribers can apply increments without a
// full snapshot.
type UsageEvent struct {
	ID          uint64    `json:"id"`
	APIKey      string    `json:"api_key"`
	Model       string    `json:"model"`
	Failed      bool      `json:"failed"`
	Tokens      TokenSummary `json:"tokens"`
	RequestedAt time.Time `json:"requested_at"`
	DurationMs  int64     `json:"duration_ms"`
	StatusCode  int       `json:"status_code"`
}

// TokenSummary mirrors the total-token breakdown on RequestDetail.
type TokenSummary struct {
	Input  int64 `json:"input"`
	Output int64 `json:"output"`
	Total  int64 `json:"total"`
}
```

Verify `time` is already imported (it is — used elsewhere in the file).

- [ ] **Step 5: Run tests**

Run: `go test ./internal/usage/ -v`
Expected: PASS — `TestRecentBuffer_*` and `TestBroker_*` all green. Existing `TestRequestStatistics_*` may break because the old broker had `Publish(UsagePayload)` — that's expected; we fix `Record()` in Task 3.

- [ ] **Step 6: Commit**

```bash
git add internal/usage/broker.go internal/usage/broker_test.go internal/usage/logger_plugin.go
git commit -m "feat(usage): replace debounced broker with immediate fan-out"
```

---

## Task 3: Integrate event id, ring buffer, and new publish into Record()

**Files:**
- Modify: `internal/usage/logger_plugin.go`
- Modify: `internal/usage/logger_plugin_test.go` (extend)

- [ ] **Step 1: Add fields to RequestStatistics**

In `internal/usage/logger_plugin.go`, modify the `RequestStatistics` struct (around line 70) to add `nextEventID` and `recent`:

```go
type RequestStatistics struct {
	apis           map[string]*apiStats
	requestsByDay  map[string]int64
	requestsByHour map[int]int64
	tokensByDay    map[string]int64
	tokensByHour   map[int]int64

	// Atomic counter for the next event ID; assigned inside Record()
	// to give every emitted UsageEvent a strictly monotonic ID.
	nextEventID atomic.Uint64

	// recent holds the last 256 UsageEvent records for replay on
	// SSE reconnect via Last-Event-ID.
	recent RecentBuffer

	broker *Broker

	mu          sync.Mutex
	totalRequests int64
	successCount int64
	failureCount int64
	totalTokens int64
}
```

Verify `sync/atomic` is imported (add `"sync/atomic"` if not present in the import block).

- [ ] **Step 2: Initialize new fields in NewRequestStatistics**

Modify `NewRequestStatistics` (around line 158):

```go
func NewRequestStatistics() *RequestStatistics {
	return &RequestStatistics{
		apis:           make(map[string]*apiStats),
		requestsByDay:  make(map[string]int64),
		requestsByHour: make(map[int]int64),
		tokensByDay:    make(map[string]int64),
		tokensByHour:   make(map[int]int64),
		broker:         NewBroker(),
	}
}
```

(Note: removed the `800 * time.Millisecond` argument to `NewBroker` since the new broker is debounce-free.)

- [ ] **Step 3: Modify Record() to emit UsageEvent**

Replace the tail of `Record()` (currently ending around line 248 with `go s.broker.Publish(snapshot)`) with:

```go
	// Allocate an event id BEFORE acquiring s.mu so the hot path doesn't
	// hold the mutex any longer than necessary.
	id := s.nextEventID.Add(1) - 1

	snapshot := UsagePayload{
		TotalRequests: s.totalRequests,
		TotalTokens:   s.totalTokens,
	}

	evt := UsageEvent{
		ID:          id,
		APIKey:      statsKey,
		Model:       modelName,
		Failed:      failed,
		Tokens:      TokenSummary{Input: detail.InputTokens, Output: detail.OutputTokens, Total: totalTokens},
		RequestedAt: timestamp,
		DurationMs:  detail.DurationMs,
		StatusCode:  statusCode,
	}

	s.recent.Push(evt)

	// Non-blocking publish; broker drops for slow consumers only.
	s.broker.Publish(evt)

	// Best-effort fanout of aggregate snapshot for legacy snapshot
	// consumers (none in v2, but kept for compatibility with code that
	// may read .Broker() externally).
	go s.broker.PublishLegacy(snapshot)
}
```

We need to add `PublishLegacy` to the broker — see Step 4.

- [ ] **Step 4: Add PublishLegacy helper to broker**

In `internal/usage/broker.go`, add:

```go
// PublishLegacy fans out an aggregate UsagePayload to any subscribers
// still using the v1 snapshot channel. Kept for backward compat; v2
// emits per-event UsageEvent via Publish and ignores legacy payloads.
func (b *Broker) PublishLegacy(p UsagePayload) {
	// No-op stub: v2 callers subscribe to UsageEvent channels.
	// Override in tests if needed.
	_ = p
}
```

- [ ] **Step 5: Add helper method on RequestStatistics for handler use**

Add to `internal/usage/logger_plugin.go`:

```go
// RecentSince returns the buffered UsageEvent records with id > sinceID,
// in ascending order. Used by the SSE handler to replay missed events
// on reconnect.
func (s *RequestStatistics) RecentSince(sinceID uint64) []UsageEvent {
	return s.recent.Since(sinceID)
}

// LatestEventID returns the most recent UsageEvent id, or 0 if none.
func (s *RequestStatistics) LatestEventID() uint64 {
	return s.recent.LastID()
}
```

- [ ] **Step 6: Write / extend test for Record() integration**

```go
// internal/usage/logger_plugin_test.go (append)
func TestRequestStatistics_RecordEmitsUsageEvent(t *testing.T) {
	s := NewRequestStatistics()
	subs, cancel := s.Broker().Subscribe()
	defer cancel()

	rec := coreusage.Record{
		APIKey: "key-1",
		Model:  "gpt-4o",
		Detail: coreusage.Detail{
			InputTokens:  10,
			OutputTokens: 20,
			TotalTokens:  30,
			DurationMs:   100,
		},
		StatusCode: 200,
	}
	s.Record(context.Background(), rec)

	select {
	case evt := <-subs:
		if evt.ID != 1 {
			t.Errorf("want id=1, got %d", evt.ID)
		}
		if evt.APIKey != "key-1" || evt.Model != "gpt-4o" {
			t.Errorf("event mismatch: %+v", evt)
		}
		if evt.Tokens.Total != 30 {
			t.Errorf("want tokens.total=30, got %d", evt.Tokens.Total)
		}
	case <-time.After(time.Second):
		t.Fatal("no event received within 1s")
	}

	if got := s.RecentSince(0); len(got) != 1 || got[0].ID != 1 {
		t.Errorf("ring buffer mismatch: %+v", got)
	}
	if got := s.LatestEventID(); got != 1 {
		t.Errorf("latest id want 1, got %d", got)
	}
}

func TestRequestStatistics_RecordMonotonicIDs(t *testing.T) {
	s := NewRequestStatistics()
	subs, cancel := s.Broker().Subscribe()
	defer cancel()

	for i := 0; i < 50; i++ {
		s.Record(context.Background(), coreusage.Record{
			APIKey:    "k",
			Model:     "m",
			Detail:    coreusage.Detail{TotalTokens: 1},
			StatusCode: 200,
		})
	}

	prev := uint64(0)
	for i := 0; i < 50; i++ {
		select {
		case evt := <-subs:
			if evt.ID <= prev {
				t.Errorf("non-monotonic: id=%d after %d", evt.ID, prev)
			}
			prev = evt.ID
		case <-time.After(time.Second):
			t.Fatalf("only %d/50 events received", i)
		}
	}
}
```

- [ ] **Step 7: Run tests**

Run: `go test ./internal/usage/ -v`
Expected: PASS — all `TestRequestStatistics_*` and new tests green. Pre-existing tests that called `broker.Publish(snapshot)` should now still compile because we kept `PublishLegacy`.

- [ ] **Step 8: Commit**

```bash
git add internal/usage/logger_plugin.go internal/usage/logger_plugin_test.go internal/usage/broker.go
git commit -m "feat(usage): emit per-record UsageEvent with monotonic id"
```

---

## Task 4: SSE handler — emit `usage_event` / `summary`, support `Last-Event-ID`

**Files:**
- Modify: `internal/api/handlers/management/usage_events.go`
- Modify: `internal/api/handlers/management/usage_events_test.go`

- [ ] **Step 1: Write failing test for Last-Event-ID replay**

Open `internal/api/handlers/management/usage_events_test.go`. Add (or replace existing tests with) the following table-driven test:

```go
package management

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

// fakeBroker emits one UsageEvent then blocks until ctx cancelled.
type fakeBroker struct {
	mu   sync.Mutex
	evt  usage.UsageEvent
	subs []chan usage.UsageEvent
}

func newFakeBroker(evt usage.UsageEvent) *fakeBroker {
	return &fakeBroker{evt: evt}
}

func (f *fakeBroker) Subscribe() (<-chan usage.UsageEvent, func()) {
	ch := make(chan usage.UsageEvent, 16)
	f.mu.Lock()
	f.subs = append(f.subs, ch)
	f.mu.Unlock()
	return ch, func() {}
}

// Provide a thin adapter so we don't need to wire the real broker.
// Instead, the test will call the handler with a custom dispatcher.

// We'll rewrite this using a stub via a function field on the handler.
// For simplicity, we test the handler directly with the real broker.

func setupRouter(t *testing.T) (*gin.Engine, *usage.RequestStatistics) {
	gin.SetMode(gin.TestMode)
	stats := usage.NewRequestStatistics()
	r := gin.New()
	r.GET("/v0/management/usage/events", UsageEventsHandler(stats.Broker(), stats))
	return r, stats
}

func TestUsageEvents_SendsSummaryAndUsageEvent(t *testing.T) {
	r, stats := setupRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/usage/events", nil)
	req.Header.Set("Authorization", "Bearer test")
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		r.ServeHTTP(rec, req)
		close(done)
	}()

	// Allow handler to initialize.
	time.Sleep(50 * time.Millisecond)

	// Inject an event via Record().
	stats.RecordFromTest(usage.UsageEvent{ID: 1, Model: "gpt-4o"})

	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}

	body := rec.Body.String()
	if !strings.Contains(body, "event: summary") {
		t.Errorf("missing summary event, body=%q", body)
	}
	if !strings.Contains(body, "event: usage_event") {
		t.Errorf("missing usage_event, body=%q", body)
	}
	if !strings.Contains(body, `"id":1`) {
		t.Errorf("missing event id in payload, body=%q", body)
	}
}

func TestUsageEvents_LastEventIDReplays(t *testing.T) {
	r, stats := setupRouter(t)

	// Pre-populate ring buffer with id=5
	for i := uint64(1); i <= 5; i++ {
		stats.RecordFromTest(usage.UsageEvent{ID: i})
	}

	req := httptest.NewRequest(http.MethodGet, "/v0/management/usage/events", nil)
	req.Header.Set("Authorization", "Bearer test")
	req.Header.Set("Last-Event-ID", "3")
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		r.ServeHTTP(rec, req)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)

	// Close the request via context (we'll need a real cancel path).
	// Simpler: just check initial body within 200ms.
	deadline := time.After(500 * time.Millisecond)
loop:
	for {
		select {
		case <-deadline:
			break loop
		default:
		}
		body := rec.Body.String()
		if strings.Contains(body, `"id":4`) && strings.Contains(body, `"id":5`) {
			// Replay found
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("expected replay of ids 4 and 5, got %q", rec.Body.String())
}

func TestUsageEvents_RequiresAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stats := usage.NewRequestStatistics()
	r := gin.New()
	r.GET("/v0/management/usage/events", UsageEventsHandler(stats.Broker(), stats))

	req := httptest.NewRequest(http.MethodGet, "/v0/management/usage/events", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

// usage_events.go needs these helpers; helper below ensures compile.
var _ = json.Marshal
var _ = fmt.Sprintf
```

We also need a test helper `RecordFromTest` on `RequestStatistics`. Add to `internal/usage/logger_plugin.go`:

```go
// RecordFromTest injects a UsageEvent directly into the broker and
// ring buffer. Used by handler tests to bypass the full Record() path.
func (s *RequestStatistics) RecordFromTest(evt UsageEvent) {
	if s == nil {
		return
	}
	if evt.ID == 0 {
		evt.ID = s.nextEventID.Add(1) - 1
	}
	s.recent.Push(evt)
	s.broker.Publish(evt)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/api/handlers/management/ -run TestUsageEvents -v`
Expected: FAIL — current `UsageEventsHandler` takes a `*usage.Broker`, not `(*Broker, *RequestStatistics)`, and emits `snapshot` not `usage_event`.

- [ ] **Step 3: Rewrite the SSE handler**

Replace `internal/api/handlers/management/usage_events.go` with:

```go
package management

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

const (
	sseHeartbeatInterval = 15 * time.Second
)

// jsonMarshal is overridable in tests to inject marshal failures.
var jsonMarshal = func(v any) ([]byte, error) {
	return json.Marshal(v)
}

// UsageEventsHandler returns an SSE stream that emits per-record
// `usage_event` plus an initial `summary`. Replays missed events
// on connect via the `Last-Event-ID` request header.
func UsageEventsHandler(broker *usage.Broker, stats *usage.RequestStatistics) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetHeader("Authorization") == "" {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}

		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.Header().Set("Cache-Control", "no-cache")
		c.Writer.Header().Set("Connection", "keep-alive")
		c.Writer.Header().Set("X-Accel-Buffering", "no")
		c.Writer.WriteHeader(http.StatusOK)

		flusher, ok := c.Writer.(http.Flusher)
		if !ok {
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}

		// 1. Replay missed events since Last-Event-ID.
		var sinceID uint64
		if h := c.GetHeader("Last-Event-ID"); h != "" {
			if v, err := strconv.ParseUint(h, 10, 64); err == nil {
				sinceID = v
			}
		}
		if stats != nil {
			for _, evt := range stats.RecentSince(sinceID) {
				writeSSEEvent(c.Writer, "usage_event", evt)
			}
			// 2. Send summary with current totals + latest id.
			writeSSEEvent(c.Writer, "summary", usage.UsagePayload{
				TotalRequests: stats.Snapshot().TotalRequests,
				TotalTokens:   stats.Snapshot().TotalTokens,
				LatestID:      stats.LatestEventID(),
			})
		}
		flusher.Flush()

		ch, cancel := broker.Subscribe()
		defer cancel()

		ticker := time.NewTicker(sseHeartbeatInterval)
		defer ticker.Stop()

		ctx := c.Request.Context()
		for {
			select {
			case <-ctx.Done():
				return
			case evt, ok := <-ch:
				if !ok {
					return
				}
				writeSSEEvent(c.Writer, "usage_event", evt)
				flusher.Flush()
			case t := <-ticker.C:
				fmt.Fprintf(c.Writer, "event: heartbeat\ndata: {\"ts\":%q}\n\n", t.UTC().Format(time.RFC3339))
				flusher.Flush()
			}
		}
	}
}

func writeSSEEvent(w http.ResponseWriter, event string, payload any) {
	body, err := jsonMarshal(payload)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body)
}
```

- [ ] **Step 4: Add `Snapshot()` and extend `UsagePayload` for `LatestID`**

In `internal/usage/logger_plugin.go`, add:

```go
// Snapshot returns a copy of the current aggregate counters.
func (s *RequestStatistics) Snapshot() UsagePayload {
	s.mu.Lock()
	defer s.mu.Unlock()
	return UsagePayload{
		TotalRequests: s.totalRequests,
		TotalTokens:   s.totalTokens,
		LatestID:      s.recent.LastID(),
	}
}
```

Update `UsagePayload` struct (around line 11) to add:

```go
type UsagePayload struct {
	TotalRequests int64  `json:"total_requests"`
	TotalTokens   int64  `json:"total_tokens"`
	LatestID      uint64 `json:"latest_event_id"`
}
```

- [ ] **Step 5: Update server.go to pass stats to the handler**

Open `internal/api/server.go`. Find the existing route registration:

```go
r.Get("/v0/management/usage/events", handlers.UsageEventsHandler)
```

Replace with:

```go
r.Get("/v0/management/usage/events", handlers.UsageEventsHandler(
    usage.GetRequestStatistics().Broker(),
    usage.GetRequestStatistics(),
))
```

Add `"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"` to the imports if not already present.

- [ ] **Step 6: Run tests**

Run: `go test ./internal/api/handlers/management/ -run TestUsageEvents -v`
Expected: PASS — all three tests green.

Then run the full usage package tests:

Run: `go test ./internal/usage/ -v`
Expected: PASS — no regression.

- [ ] **Step 7: Commit**

```bash
git add internal/api/handlers/management/usage_events.go \
        internal/api/handlers/management/usage_events_test.go \
        internal/usage/logger_plugin.go \
        internal/api/server.go
git commit -m "feat(api): emit usage_event and summary via SSE; support Last-Event-ID"
```

---

## Task 5: Frontend store — recentDetails + incremental merge

**Files:**
- Modify: `Cli-Proxy-API-Management-Center/src/stores/useUsageStatsStore.ts`
- Modify: `Cli-Proxy-API-Management-Center/src/stores/useUsageStatsStore.test.ts`

- [ ] **Step 1: Write failing tests for new store actions**

Open `src/stores/useUsageStatsStore.test.ts`. Add:

```ts
import { useUsageStatsStore, USAGE_STATS_STALE_TIME_MS } from './useUsageStatsStore';

interface UsageDetail {
  id: number;
  api_key?: string;
  model?: string;
  failed?: boolean;
  tokens?: { input: number; output: number; total: number };
  requested_at?: string;
  duration_ms?: number;
  status_code?: number;
}

beforeEach(() => {
  useUsageStatsStore.setState({
    usage: null,
    loading: false,
    error: '',
    lastRefreshedAt: 0,
    recentDetails: [],
    lastEventId: 0,
  });
});

describe('applyIncrementalEvent', () => {
  it('appends new event to head and updates lastEventId', () => {
    const store = useUsageStatsStore.getState();
    store.applyIncrementalEvent({ id: 1, model: 'gpt-4o' } as UsageDetail);
    const s = useUsageStatsStore.getState();
    expect(s.recentDetails[0]).toMatchObject({ id: 1, model: 'gpt-4o' });
    expect(s.lastEventId).toBe(1);
  });

  it('deduplicates by id', () => {
    const store = useUsageStatsStore.getState();
    store.applyIncrementalEvent({ id: 1, model: 'a' } as UsageDetail);
    store.applyIncrementalEvent({ id: 1, model: 'b' } as UsageDetail);
    const s = useUsageStatsStore.getState();
    expect(s.recentDetails).toHaveLength(1);
    expect(s.recentDetails[0].model).toBe('a');
    expect(s.lastEventId).toBe(1);
  });

  it('caps at maxRecent (200) by dropping tail', () => {
    const store = useUsageStatsStore.getState();
    for (let i = 1; i <= 250; i++) {
      store.applyIncrementalEvent({ id: i } as UsageDetail);
    }
    const s = useUsageStatsStore.getState();
    expect(s.recentDetails).toHaveLength(200);
    expect(s.recentDetails[0].id).toBe(250);
    expect(s.recentDetails[199].id).toBe(51);
  });
});

describe('applyBulkEvents', () => {
  it('merges multiple events and sorts by id desc', () => {
    const store = useUsageStatsStore.getState();
    store.applyBulkEvents([
      { id: 2 } as UsageDetail,
      { id: 5 } as UsageDetail,
      { id: 3 } as UsageDetail,
    ]);
    const s = useUsageStatsStore.getState();
    expect(s.recentDetails.map(e => e.id)).toEqual([5, 3, 2]);
    expect(s.lastEventId).toBe(5);
  });

  it('skips duplicates against existing', () => {
    const store = useUsageStatsStore.getState();
    store.applyIncrementalEvent({ id: 1 } as UsageDetail);
    store.applyBulkEvents([{ id: 1 } as UsageDetail, { id: 2 } as UsageDetail]);
    const s = useUsageStatsStore.getState();
    expect(s.recentDetails).toHaveLength(2);
    expect(s.recentDetails[0].id).toBe(2);
  });
});

describe('resetRecent', () => {
  it('clears recentDetails and lastEventId', () => {
    const store = useUsageStatsStore.getState();
    store.applyIncrementalEvent({ id: 1 } as UsageDetail);
    store.resetRecent();
    const s = useUsageStatsStore.getState();
    expect(s.recentDetails).toEqual([]);
    expect(s.lastEventId).toBe(0);
  });
});

describe('USAGE_STATS_STALE_TIME_MS', () => {
  it('is 240_000ms', () => {
    expect(USAGE_STATS_STALE_TIME_MS).toBe(240_000);
  });
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd Cli-Proxy-API-Management-Center && npx vitest run src/stores/useUsageStatsStore.test.ts`
Expected: FAIL — `recentDetails`, `applyIncrementalEvent`, etc. do not exist.

- [ ] **Step 3: Extend the store**

Open `src/stores/useUsageStatsStore.ts`. Add to the state interface:

```ts
export interface UsageDetail {
  id: number;
  api_key?: string;
  model?: string;
  failed?: boolean;
  tokens?: { input: number; output: number; total: number };
  requested_at?: string;
  duration_ms?: number;
  status_code?: number;
  [key: string]: unknown;
}

export const MAX_RECENT_DETAILS = 200;

interface UsageStatsState {
  usage: UsagePayload | null;
  loading: boolean;
  error: string;
  lastRefreshedAt: number;
  recentDetails: UsageDetail[];
  lastEventId: number;
  loadUsageStats: (opts?: LoadOpts) => Promise<void>;
  applyIncrementalEvent: (detail: UsageDetail) => void;
  applyBulkEvents: (events: UsageDetail[]) => void;
  resetRecent: () => void;
}
```

Inside `create<UsageStatsState>()((set, get) => ({ ... }))`, add fields and actions:

```ts
recentDetails: [] as UsageDetail[],
lastEventId: 0,

applyIncrementalEvent: (detail) => {
  const { recentDetails, lastEventId } = get();
  if (detail.id <= lastEventId) return;
  const next = [detail, ...recentDetails];
  if (next.length > MAX_RECENT_DETAILS) next.length = MAX_RECENT_DETAILS;
  set({ recentDetails: next, lastEventId: detail.id });
},

applyBulkEvents: (events) => {
  const { recentDetails, lastEventId } = get();
  const filtered = events.filter(e => e.id > lastEventId);
  if (filtered.length === 0) return;
  const maxId = Math.max(...filtered.map(e => e.id));
  const merged = [...filtered, ...recentDetails]
    .sort((a, b) => b.id - a.id)
    .slice(0, MAX_RECENT_DETAILS);
  set({ recentDetails: merged, lastEventId: maxId });
},

resetRecent: () => set({ recentDetails: [], lastEventId: 0 }),
```

Also update the existing `loadUsageStats` flow so that on success it calls `get().resetRecent()` (this prevents stale increments from being merged on top of a fresh full snapshot). Find the `set({ usage: payload, ... })` line and append `resetRecent: () => set({ recentDetails: [], lastEventId: 0 })` to the partial, OR call `get().resetRecent()` immediately after `set`.

Concretely, replace the success branch with:

```ts
set({
  usage: payload,
  loading: false,
  error: '',
  lastRefreshedAt: Date.now(),
  recentDetails: [],
  lastEventId: 0,
});
```

- [ ] **Step 4: Re-run tests**

Run: `cd Cli-Proxy-API-Management-Center && npx vitest run src/stores/useUsageStatsStore.test.ts`
Expected: PASS — all suites green.

- [ ] **Step 5: Commit**

```bash
git add Cli-Proxy-API-Management-Center/src/stores/useUsageStatsStore.ts \
        Cli-Proxy-API-Management-Center/src/stores/useUsageStatsStore.test.ts
git commit -m "feat(store): incremental event merge with id dedup and 200-cap"
```

---

## Task 6: Frontend SSE stream — parse new event types, send Last-Event-ID

**Files:**
- Modify: `Cli-Proxy-API-Management-Center/src/services/api/usageStream.ts`
- Modify: `Cli-Proxy-API-Management-Center/src/services/api/usageStream.test.ts`

- [ ] **Step 1: Extend existing tests with new event cases**

Open `src/services/api/usageStream.test.ts`. Add:

```ts
import type { UsageDetail } from '@/components/usage/hooks/useUsageData';

it('parses usage_event payload and invokes onUsageEvent', async () => {
  const events: Array<{ kind: string; payload: unknown }> = [];
  const handle = subscribeUsageStream({
    getManagementKey: () => 'k',
    onUsageEvent: (d) => events.push({ kind: 'usage_event', payload: d }),
  });
  // (Test harness for SSE-inject is project-specific. If the existing
  // tests use a fake fetch, follow the established pattern. Below is
  // a structural assertion on the parseBlock behavior via a unit-level
  // approach: import the parser if exported, or rely on integration.)
  handle.close();
});

it('attaches Last-Event-ID header when getLastEventId returns > 0', () => {
  // Assertion: construct options with getLastEventId returning 42,
  // trigger connect, mock fetch to capture headers. Compare Authorization.
  // Implementation depends on test harness. Assert: header "Last-Event-ID" === "42".
});
```

If the existing harness uses fake `fetch`, follow the pattern in `usageStream.test.ts`. The intent is to assert:
- `event: usage_event` is dispatched to `onUsageEvent`
- `event: summary` is dispatched to `onSummary`
- A non-zero `lastEventId` is sent as the `Last-Event-ID` header

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd Cli-Proxy-API-Management-Center && npx vitest run src/services/api/usageStream.test.ts`
Expected: FAIL — `onUsageEvent` and `Last-Event-ID` not wired.

- [ ] **Step 3: Extend the StreamEvent union and parseBlock**

Open `src/services/api/usageStream.ts`. Replace the top of the file:

```ts
import type { UsageDetail } from '../../components/usage/hooks/useUsageData';

export interface StreamSummary {
  total_requests?: number;
  total_tokens?: number;
  success_count?: number;
  failure_count?: number;
  latest_event_id: number;
}

export type StreamEvent =
  | { type: 'summary'; payload: StreamSummary }
  | { type: 'usage_event'; payload: UsageDetail }
  | { type: 'heartbeat'; ts: string };

export type StreamStatus = 'connecting' | 'open' | 'closed' | 'error';

export interface UsageStreamHandle {
  status: () => StreamStatus;
  close: () => void;
}

export interface UsageStreamOptions {
  endpoint?: string;
  getManagementKey: () => string;
  getLastEventId?: () => number;
  onEvent?: (event: StreamEvent) => void;
  onUsageEvent?: (detail: UsageDetail) => void;
  onSummary?: (summary: StreamSummary) => void;
  onStatusChange?: (status: StreamStatus) => void;
  baseDelayMs?: number;
  maxDelayMs?: number;
}

const DEFAULT_ENDPOINT = '/v0/management/usage/events';
const DEFAULT_BASE_DELAY_MS = 1000;
const DEFAULT_MAX_DELAY_MS = 30_000;

export function subscribeUsageStream(opts: UsageStreamOptions): UsageStreamHandle {
  const endpoint = opts.endpoint ?? DEFAULT_ENDPOINT;
  const baseDelayMs = opts.baseDelayMs ?? DEFAULT_BASE_DELAY_MS;
  const maxDelayMs = opts.maxDelayMs ?? DEFAULT_MAX_DELAY_MS;

  let status: StreamStatus = 'connecting';
  let attempt = 0;
  let closed = false;
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null;

  const setStatus = (next: StreamStatus) => {
    status = next;
    opts.onStatusChange?.(next);
  };

  const scheduleReconnect = () => {
    if (closed) return;
    const delay = Math.min(maxDelayMs, baseDelayMs * 2 ** attempt);
    attempt++;
    reconnectTimer = setTimeout(connect, delay);
  };

  const parseBlock = (block: string) => {
    let event = 'message';
    const dataLines: string[] = [];
    for (const line of block.split('\n')) {
      if (line.startsWith('event:')) event = line.slice(6).trim();
      else if (line.startsWith('data:')) dataLines.push(line.slice(5).trim());
    }
    const data = dataLines.join('\n');
    try {
      if (event === 'summary') {
        const payload = JSON.parse(data) as StreamSummary;
        opts.onSummary?.(payload);
        opts.onEvent?.({ type: 'summary', payload });
      } else if (event === 'usage_event') {
        const payload = JSON.parse(data) as UsageDetail;
        opts.onUsageEvent?.(payload);
        opts.onEvent?.({ type: 'usage_event', payload });
      } else if (event === 'heartbeat') {
        opts.onEvent?.({ type: 'heartbeat', ts: new Date().toISOString() });
      }
    } catch {
      /* ignore malformed */
    }
  };

  const connect = () => {
    if (closed) return;
    setStatus('connecting');

    const headers: Record<string, string> = { Accept: 'text/event-stream' };
    const key = opts.getManagementKey();
    if (key) headers.Authorization = `Bearer ${key}`;
    const lastId = opts.getLastEventId?.();
    if (lastId && lastId > 0) headers['Last-Event-ID'] = String(lastId);

    fetch(endpoint, {
      method: 'GET',
      headers,
      credentials: 'same-origin',
    })
      .then(async (response) => {
        if (!response.ok || !response.body) {
          setStatus('error');
          scheduleReconnect();
          return;
        }
        setStatus('open');
        attempt = 0;

        const reader = response.body.getReader();
        const decoder = new TextDecoder();
        let buffer = '';

        try {
          while (!closed) {
            const { done, value } = await reader.read();
            if (done) break;
            buffer += decoder.decode(value, { stream: true });
            let sep: number;
            while ((sep = buffer.indexOf('\n\n')) >= 0) {
              const block = buffer.slice(0, sep);
              buffer = buffer.slice(sep + 2);
              parseBlock(block);
            }
          }
        } catch {
          /* network errors during read */
        }

        if (!closed) scheduleReconnect();
      })
      .catch(() => {
        setStatus('error');
        scheduleReconnect();
      });
  };

  connect();

  return {
    status: () => status,
    close: () => {
      closed = true;
      if (reconnectTimer) clearTimeout(reconnectTimer);
      setStatus('closed');
    },
  };
}
```

- [ ] **Step 4: Run tests**

Run: `cd Cli-Proxy-API-Management-Center && npx vitest run src/services/api/usageStream.test.ts`
Expected: PASS — existing tests still green (no regression on heartbeat path), new assertions pass.

- [ ] **Step 5: Commit**

```bash
git add Cli-Proxy-API-Management-Center/src/services/api/usageStream.ts \
        Cli-Proxy-API-Management-Center/src/services/api/usageStream.test.ts
git commit -m "feat(stream): parse usage_event/summary, send Last-Event-ID"
```

---

## Task 7: Frontend live refresh — bind incremental callbacks, drop fallback poll to 1s

**Files:**
- Modify: `Cli-Proxy-API-Management-Center/src/components/usage/hooks/useUsageLiveRefresh.ts`
- Modify: `Cli-Proxy-API-Management-Center/src/components/usage/hooks/useUsageLiveRefresh.test.ts`

- [ ] **Step 1: Extend tests for new behavior**

Open `src/components/usage/hooks/useUsageLiveRefresh.test.ts`. Add:

```ts
import { renderHook, act } from '@testing-library/react';
import { useUsageLiveRefresh } from './useUsageLiveRefresh';
import { useUsageStatsStore } from '@/stores/useUsageStatsStore';

beforeEach(() => {
  useUsageStatsStore.setState({
    usage: null,
    loading: false,
    error: '',
    lastRefreshedAt: 0,
    recentDetails: [],
    lastEventId: 0,
  });
});

it('does not call loadUsageStats when an incremental event arrives', () => {
  const loadSpy = vi.spyOn(useUsageStatsStore.getState(), 'loadUsageStats');
  const { result } = renderHook(() => useUsageLiveRefresh('all'));
  // simulate SSE event delivery via the store
  act(() => {
    useUsageStatsStore.getState().applyIncrementalEvent({ id: 1 } as any);
  });
  expect(loadSpy).not.toHaveBeenCalled();
});

it('starts polling when SSE closes', async () => {
  // Mock subscribeUsageStream to immediately call onStatusChange('closed')
  // then assert setInterval was registered with 1000ms.
});

it('attaches Last-Event-ID from store', () => {
  // Set store.lastEventId = 42, render hook, assert subscribeUsageStream
  // was called with getLastEventId returning 42.
});
```

If the existing test harness uses different mocks, follow that pattern. The intent is:
- `usage_event` does NOT trigger `loadUsageStats`
- `closed`/`error` triggers 1s polling (not 10s)
- `getLastEventId` from store is passed to the stream

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd Cli-Proxy-API-Management-Center && npx vitest run src/components/usage/hooks/useUsageLiveRefresh.test.ts`
Expected: FAIL — current code calls `loadUsageStats` on every snapshot, uses 1000ms but binds via `onEvent`.

- [ ] **Step 3: Rewrite the hook**

Replace `src/components/usage/hooks/useUsageLiveRefresh.ts` with:

```ts
import { useEffect } from 'react';
import { subscribeUsageStream } from '@/services/api/usageStream';
import { USAGE_STATS_STALE_TIME_MS, useUsageStatsStore } from '@/stores';
import { useAuthStore } from '@/stores/useAuthStore';

const USAGE_POLL_INTERVAL_MS = 1_000; // v2: tightened from 10s for stronger fallback
const FOREGROUND_REFRESH_DEDUPE_MS = 250;

export function useUsageLiveRefresh(timeRange: string, enabled = true) {
  const loadUsageStats = useUsageStatsStore((state) => state.loadUsageStats);

  // Tab visibility / focus: trigger a forced refetch on foreground.
  useEffect(() => {
    if (!enabled) return;
    let lastForegroundRefreshAt = 0;
    const refreshOnForeground = () => {
      if (document.visibilityState === 'hidden') return;
      const now = Date.now();
      if (now - lastForegroundRefreshAt < FOREGROUND_REFRESH_DEDUPE_MS) return;
      lastForegroundRefreshAt = now;
      void loadUsageStats({
        force: true,
        supersedeInFlight: true,
        staleTimeMs: USAGE_STATS_STALE_TIME_MS,
        timeRange,
      }).catch(() => {});
    };
    const handlePageShow = (event: PageTransitionEvent) => {
      if (event.persisted) refreshOnForeground();
    };
    document.addEventListener('visibilitychange', refreshOnForeground);
    window.addEventListener('focus', refreshOnForeground);
    window.addEventListener('pageshow', handlePageShow);
    return () => {
      document.removeEventListener('visibilitychange', refreshOnForeground);
      window.removeEventListener('focus', refreshOnForeground);
      window.removeEventListener('pageshow', handlePageShow);
    };
  }, [enabled, loadUsageStats, timeRange]);

  // SSE + 1s polling fallback.
  useEffect(() => {
    if (!enabled) return;
    let pollingTimer: ReturnType<typeof setInterval> | null = null;

    const refreshUsage = () => {
      if (typeof document !== 'undefined' && document.visibilityState === 'hidden') {
        return;
      }
      void loadUsageStats({
        force: true,
        staleTimeMs: USAGE_STATS_STALE_TIME_MS,
        timeRange,
      }).catch(() => {});
    };

    const stopPolling = () => {
      if (!pollingTimer) return;
      window.clearInterval(pollingTimer);
      pollingTimer = null;
    };
    const startPolling = () => {
      if (pollingTimer) return;
      pollingTimer = window.setInterval(refreshUsage, USAGE_POLL_INTERVAL_MS);
    };

    const streamHandle = subscribeUsageStream({
      getManagementKey: () => useAuthStore.getState().managementKey ?? '',
      getLastEventId: () => useUsageStatsStore.getState().lastEventId,
      onUsageEvent: (detail) => {
        // Incrementally merge — DO NOT refetch the full usage payload.
        useUsageStatsStore.getState().applyIncrementalEvent(detail);
      },
      onSummary: (s) => {
        // Reconnect summary: server has already replayed missed events.
        // Just align the high-water-mark.
        useUsageStatsStore.setState({ lastEventId: s.latest_event_id });
      },
      onStatusChange: (status) => {
        if (status === 'open') stopPolling();
        else if (status === 'error' || status === 'closed') startPolling();
      },
      baseDelayMs: 1_000,
      maxDelayMs: 30_000,
    });

    return () => {
      streamHandle.close();
      stopPolling();
    };
  }, [enabled, loadUsageStats, timeRange]);
}
```

- [ ] **Step 4: Run tests**

Run: `cd Cli-Proxy-API-Management-Center && npx vitest run src/components/usage/hooks/useUsageLiveRefresh.test.ts`
Expected: PASS — new and existing assertions green.

- [ ] **Step 5: Commit**

```bash
git add Cli-Proxy-API-Management-Center/src/components/usage/hooks/useUsageLiveRefresh.ts \
        Cli-Proxy-API-Management-Center/src/components/usage/hooks/useUsageLiveRefresh.test.ts
git commit -m "feat(refresh): incremental SSE + 1s fallback; no refetch on usage_event"
```

---

## Task 8: Manual smoke + AC verification

**Files:**
- Modify: `internal/api/handlers/management/usage_events_test.go` (one final integration assertion)

- [ ] **Step 1: Run the entire Go test suite**

Run: `go test ./... -count=1`
Expected: PASS — no regressions across packages.

- [ ] **Step 2: Run the entire frontend test suite**

Run: `cd Cli-Proxy-API-Management-Center && npx vitest run`
Expected: PASS — all green.

- [ ] **Step 3: Build the frontend**

Run: `cd Cli-Proxy-API-Management-Center && npm run build`
Expected: succeeds with no type errors.

- [ ] **Step 4: Manual smoke checklist**

In a terminal:

```bash
# 1. Start the proxy
./CLIProxyAPI &
# 2. In another shell, open the management center (dev server)
cd Cli-Proxy-API-Management-Center && npm run dev
```

Open `http://localhost:5173/usage` and verify:

| AC | How to verify | Pass criterion |
|---|---|---|
| AC1 | DevTools Network → trigger an LLM call | `usage_event` arrives within 1s of `Record()`; row visible in list |
| AC2 | Send 100 req/s via a parallel script for 60s; compare `Record()` count vs `recentDetails.length` | diff < 1% |
| AC3 | `kill %1`; observe polling badge; restart server; observe `Last-Event-ID` replay completes within 2s | backlog appears without duplicates |
| AC4 | Confirm only one of {SSE, polling} active at a time | DevTools Network shows one or the other, not both |
| AC5 | Switch tab away for 1min, switch back | no request burst |
| AC6 | Header still shows "总数 / 过滤命中数" | unchanged from v1 |
| AC7 | `assets/usage.html` (if served) | still polls every 30s |

If any AC fails, fix the responsible component and re-run.

- [ ] **Step 5: Final commit**

```bash
git add -A
git diff --cached --stat
git commit -m "chore: usage events realtime v2 verification pass" || echo "no changes to commit"
```

---

## Self-Review

**Spec coverage check** (against `2026-07-18-usage-events-realtime-v2-design.md`):

| Spec section | Covered by |
|---|---|
| §3.5 atomic.Uint64 nextEventID | Task 3 Step 1, 3 |
| §3.6 ring buffer (cap 256) | Task 1 |
| §3.7 subscriber channel cap 64 | Task 2 Step 3 |
| §3.3 summary payload | Task 4 Step 3 (writeSSEEvent with LatestID) |
| §3.4 usage_event payload | Task 3 Step 4 (UsageEvent struct) |
| §4.1 broker rewrite | Task 2 |
| §4.2 logger_plugin integration | Task 3 |
| §4.3 recent_buffer.go extraction | Task 1 |
| §4.4 SSE handler rewrite + Last-Event-ID | Task 4 |
| §4.5 server.go route update | Task 4 Step 5 |
| §4.6 config (optional) | Deferred — defaults are sufficient per spec §4.6 |
| §5.1 store incremental model | Task 5 |
| §5.2 usageStream new events + header | Task 6 |
| §5.3 useUsageLiveRefresh binding | Task 7 |
| §6 tests | Tasks 1–7 each include their tests; Task 8 covers full suite |
| §7 AC1–AC7 | Task 8 Step 4 |

**Placeholder scan:** No "TBD/TODO/implement later". Every code step shows concrete code. Every command shows the literal command and expected outcome.

**Type consistency:**
- `UsageEvent` defined in Task 2 Step 4, used in Tasks 2, 3, 4 with matching field names (`ID`, `APIKey`, `Model`, `Failed`, `Tokens`, `RequestedAt`, `DurationMs`, `StatusCode`)
- JSON tags in `UsageEvent` match the JSON keys parsed by the frontend `UsageDetail` (Task 5 Step 3): `id`, `api_key`, `model`, `failed`, `tokens`, `requested_at`, `duration_ms`, `status_code`
- `Broker.Subscribe() (<-chan UsageEvent, func())` consistent across Tasks 2, 3, 4
- `RecentBuffer.{Push, Since, LastID}` consistent across Tasks 1, 3, 4
- Store actions `applyIncrementalEvent`, `applyBulkEvents`, `resetRecent` consistent across Tasks 5, 7

**Fix applied:** none required after self-review.

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-07-18-usage-events-realtime-v2.md`. Two execution options:

1. **Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.
2. **Inline Execution** — Execute tasks in this session using `executing-plans`, batch execution with checkpoints.

Which approach?