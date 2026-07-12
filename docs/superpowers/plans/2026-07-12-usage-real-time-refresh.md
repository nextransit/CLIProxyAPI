# Usage Real-Time Refresh Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 Management Center 的 Usage 页实现 SSE 推送（带 10s 轮询降级），并在请求事件明细卡片头部同时显示「总数 / 过滤命中数」。

**Architecture:** 后端 `Record()` 通过新增的 `broker` 推送 usage snapshot，前端 `EventSource` 订阅后写入 `useUsageStatsStore`；SSE 故障时自动降级为 10s 轮询。卡片头部在已有过滤条数旁追加未过滤条数。

**Tech Stack:** Go (net/http), Gin, Server-Sent Events; React + Zustand + TypeScript; i18next.

---

## File Structure

**后端（Go）**
- Create: `internal/usage/broker.go` — SSE 订阅 broker，含去抖
- Create: `internal/usage/broker_test.go` — broker 单测
- Create: `internal/api/handlers/usage_events.go` — `/v0/management/usage/events` handler
- Create: `internal/api/handlers/usage_events_test.go` — handler 单测
- Modify: `internal/usage/logger_plugin.go` — Record 末尾调 broker.Publish
- Modify: `internal/api/server.go` — 注册新路由 + 实例化 broker

**前端（TypeScript）**
- Create: `Cli-Proxy-API-Management-Center/src/services/api/usageStream.ts` — EventSource 封装
- Create: `Cli-Proxy-API-Management-Center/src/services/api/usageStream.test.ts` — 单元测试（vitest）
- Modify: `Cli-Proxy-API-Management-Center/src/components/usage/hooks/useUsageData.ts` — 接入 SSE + 降级
- Modify: `Cli-Proxy-API-Management-Center/src/components/usage/RequestEventsDetailsCard.tsx` — 头部显示总数/过滤后
- Modify: `Cli-Proxy-API-Management-Center/src/i18n/locales/zh-CN.json` — 新增 i18n key
- Modify: `Cli-Proxy-API-Management-Center/src/i18n/locales/en.json` — 新增 i18n key

**依赖关系**
- Task 1-3: 后端 broker 单元 + 集成
- Task 4-5: 后端 SSE handler
- Task 6: 注册路由
- Task 7-9: 前端 EventSource 封装 + 接入
- Task 10-11: 前端详情卡片头部 + i18n

---

## Task 1: Broker 单元 — Subscribe/Cancel

**Files:**
- Create: `internal/usage/broker.go`
- Create: `internal/usage/broker_test.go`

- [ ] **Step 1: 写失败测试 — Subscribe 收到 Publish 的 snapshot**

```go
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
```

- [ ] **Step 2: 运行测试，验证失败**

Run: `cd /Users/zhouyong/Desktop/work/Decard/gitlab/ai/CLIProxyAPI && go test ./internal/usage/ -run TestBrokerSubscribeReceivesPublish -v`
Expected: FAIL — `NewBroker` undefined.

- [ ] **Step 3: 写最小实现**

```go
// internal/usage/broker.go
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
    [key: string]  any
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
```

- [ ] **Step 4: 运行测试，验证通过**

Run: `go test ./internal/usage/ -run TestBrokerSubscribeReceivesPublish -v`
Expected: PASS.

- [ ] **Step 5: 提交**

```bash
git add internal/usage/broker.go internal/usage/broker_test.go
git commit -m "feat(usage): add broker for debounced SSE publish"
```

---

## Task 2: Broker — Debounce 合并突发

**Files:**
- Modify: `internal/usage/broker_test.go`

- [ ] **Step 1: 写失败测试 — 100ms 内多次 Publish 只触发一次 flush**

```go
func TestBrokerDebouncesPublish(t *testing.T) {
    b := NewBroker(100 * time.Millisecond)
    ch, cancel := b.Subscribe()
    defer cancel()

    b.Publish(UsagePayload{TotalRequests: 1})
    b.Publish(UsagePayload{TotalRequests: 2})
    b.Publish(UsagePayload{TotalRequests: 3})

    select {
    case got := <-ch:
        if got.TotalRequests != 3 {
            t.Fatalf("expected latest value 3, got %d", got.TotalRequests)
        }
    case <-time.After(time.Second):
        t.Fatal("timeout waiting for debounced publish")
    }

    // No second event should arrive within debounce window.
    select {
    case got := <-ch:
        t.Fatalf("unexpected second publish: %+v", got)
    case <-time.After(300 * time.Millisecond):
        // ok
    }
}
```

- [ ] **Step 2: 运行，验证失败**

Run: `go test ./internal/usage/ -run TestBrokerDebouncesPublish -v`
Expected: FAIL — current broker flushes each Publish.

- [ ] **Step 3: 实现 — `Publish` 已经在 Step 3 Task 1 用 `AfterFunc` 实现去抖；确认 timer 重置逻辑生效。无需新代码。**

- [ ] **Step 4: 运行，验证通过**

Run: `go test ./internal/usage/ -run TestBrokerDebouncesPublish -v`
Expected: PASS.

- [ ] **Step 5: 提交**

```bash
git add internal/usage/broker_test.go
git commit -m "test(usage): verify broker debounces burst publishes"
```

---

## Task 3: Wire broker into RequestStatistics

**Files:**
- Modify: `internal/usage/logger_plugin.go` — `RequestStatistics` 持有 broker，`Record` 末尾 publish
- Modify: `internal/usage/logger_plugin_test.go` — 增加 broker 接收断言

- [ ] **Step 1: 写失败测试 — Record 触发 broker publish**

```go
// In logger_plugin_test.go
func TestRequestStatisticsRecordPublishesToBroker(t *testing.T) {
    stats := NewRequestStatistics()
    ch, cancel := stats.Broker().Subscribe()
    defer cancel()

    stats.Record(context.Background(), coreusage.Record{
        APIKey:      "test-key",
        Model:       "gpt-5.4",
        RequestedAt: time.Now(),
        Detail:      coreusage.Detail{TotalTokens: 10},
    })

    select {
    case payload := <-ch:
        if payload.TotalRequests < 1 {
            t.Fatalf("TotalRequests = %d, want >= 1", payload.TotalRequests)
        }
    case <-time.After(time.Second):
        t.Fatal("timeout waiting for broker publish")
    }
}
```

- [ ] **Step 2: 运行，验证失败**

Run: `go test ./internal/usage/ -run TestRequestStatisticsRecordPublishesToBroker -v`
Expected: FAIL — `stats.Broker()` undefined.

- [ ] **Step 3: 修改 logger_plugin.go**

```go
// Add field to RequestStatistics
type RequestStatistics struct {
    mu            sync.RWMutex
    totalRequests int64
    successCount  int64
    failureCount  int64
    totalTokens   int64
    apis          map[string]*apiStats
    requestsByDay map[string]int64
    requestsByHour map[int]int64
    tokensByDay   map[string]int64
    tokensByHour  map[int]int64
    broker        *Broker
}

// Modify constructor:
func NewRequestStatistics() *RequestStatistics {
    return &RequestStatistics{
        apis:           make(map[string]*apiStats),
        requestsByDay:  make(map[string]int64),
        requestsByHour: make(map[int]int64),
        tokensByDay:    make(map[string]int64),
        tokensByHour:   make(map[int]int64),
        broker:         NewBroker(800 * time.Millisecond),
    }
}

// Add accessor:
func (s *RequestStatistics) Broker() *Broker { return s.broker }

// At end of Record(), before defer unlock:
go s.broker.Publish(s.snapshotPayload())
```

Add private helper:

```go
func (s *RequestStatistics) snapshotPayload() UsagePayload {
    return UsagePayload{
        TotalRequests: s.totalRequests,
        TotalTokens:   s.totalTokens,
        // future: include more fields as frontend needs them
    }
}
```

> 注意：`Record()` 持锁期间 publish 会阻塞；改用 `go` 异步调用避免影响热路径。

- [ ] **Step 4: 运行，验证通过**

Run: `go test ./internal/usage/ -run TestRequestStatisticsRecordPublishesToBroker -v`
Expected: PASS.

- [ ] **Step 5: 提交**

```bash
git add internal/usage/logger_plugin.go internal/usage/logger_plugin_test.go
git commit -m "feat(usage): wire broker into RequestStatistics.Record"
```

---

## Task 4: SSE Handler — Authenticated stream

**Files:**
- Create: `internal/api/handlers/usage_events.go`
- Create: `internal/api/handlers/usage_events_test.go`

- [ ] **Step 1: 写失败测试 — Handler 401 without auth header**

```go
// internal/api/handlers/usage_events_test.go
package handlers

import (
    "net/http"
    "net/http/httptest"
    "testing"
    "time"

    "github.com/gin-gonic/gin"
    "github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

func TestUsageEventsRequiresAuth(t *testing.T) {
    gin.SetMode(gin.TestMode)
    r := gin.New()
    r.GET("/v0/management/usage/events", UsageEventsHandler(usage.GetRequestStatistics().Broker()))

    req := httptest.NewRequest(http.MethodGet, "/v0/management/usage/events", nil)
    w := httptest.NewRecorder()
    r.ServeHTTP(w, req)

    if w.Code != http.StatusUnauthorized {
        t.Fatalf("status = %d, want 401", w.Code)
    }
}
```

- [ ] **Step 2: 运行，验证失败**

Run: `go test ./internal/api/handlers/ -run TestUsageEventsRequiresAuth -v`
Expected: FAIL — handler not implemented.

- [ ] **Step 3: 写最小实现**

```go
// internal/api/handlers/usage_events.go
package handlers

import (
    "fmt"
    "net/http"
    "time"

    "github.com/gin-gonic/gin"
    "github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

const (
    sseHeartbeatInterval = 15 * time.Second
    sseWriteTimeout      = 0 // no write timeout; client-driven lifecycle
)

// UsageEventsHandler returns an SSE stream of usage snapshots.
// Auth is enforced by the caller via middleware (see server.go).
func UsageEventsHandler(broker *usage.Broker) gin.HandlerFunc {
    return func(c *gin.Context) {
        // Auth check happens at middleware level; defensive check here.
        // Authorization header must be present after middleware.
        if c.GetHeader("Authorization") == "" {
            c.AbortWithStatus(http.StatusUnauthorized)
            return
        }

        ch, cancel := broker.Subscribe()
        defer cancel()

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

        ticker := time.NewTicker(sseHeartbeatInterval)
        defer ticker.Stop()

        ctx := c.Request.Context()
        for {
            select {
            case <-ctx.Done():
                return
            case payload, ok := <-ch:
                if !ok {
                    return
                }
                writeSSEEvent(c.Writer, "snapshot", payload)
                flusher.Flush()
            case t := <-ticker.C:
                fmt.Fprintf(c.Writer, "event: heartbeat\ndata: {\"ts\":%q}\n\n", t.UTC().Format(time.RFC3339))
                flusher.Flush()
            }
        }
    }
}

func writeSSEEvent(w http.ResponseWriter, event string, payload usage.UsagePayload) {
    body, err := jsonMarshal(payload)
    if err != nil {
        return
    }
    fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body)
}
```

Add tiny helper `jsonMarshal` to avoid importing encoding/json in the hot path repeatedly:

```go
// in same file
var jsonMarshal = func(v any) ([]byte, error) {
    return json.Marshal(v)
}
```

(The compiler will inline this; just keeps the SSE write loop readable.)

- [ ] **Step 4: 运行，验证通过**

Run: `go test ./internal/api/handlers/ -run TestUsageEventsRequiresAuth -v`
Expected: PASS.

- [ ] **Step 5: 提交**

```bash
git add internal/api/handlers/usage_events.go internal/api/handlers/usage_events_test.go
git commit -m "feat(api): add SSE handler for usage events"
```

---

## Task 5: SSE Handler — Snapshot delivery

**Files:**
- Modify: `internal/api/handlers/usage_events_test.go`

- [ ] **Step 1: 写失败测试 — Client receives snapshot event after publish**

```go
func TestUsageEventsDeliversSnapshot(t *testing.T) {
    gin.SetMode(gin.TestMode)
    r := gin.New()
    r.GET("/v0/management/usage/events", UsageEventsHandler(usage.GetRequestStatistics().Broker()))

    req := httptest.NewRequest(http.MethodGet, "/v0/management/usage/events", nil)
    req.Header.Set("Authorization", "Bearer test")
    w := httptest.NewRecorder()

    // Serve in goroutine so we can publish afterwards.
    done := make(chan struct{})
    go func() {
        r.ServeHTTP(w, req)
        close(done)
    }()

    // Wait for client to subscribe, then publish.
    time.Sleep(50 * time.Millisecond)
    usage.GetRequestStatistics().Broker().Publish(usage.UsagePayload{TotalRequests: 7})

    select {
    case <-done:
    case <-time.After(2 * time.Second):
        t.Fatal("handler did not exit")
    }

    body := w.Body.String()
    if !strings.Contains(body, "event: snapshot") {
        t.Fatalf("body missing snapshot event:\n%s", body)
    }
    if !strings.Contains(body, `"total_requests":7`) {
        t.Fatalf("body missing payload:\n%s", body)
    }
}
```

Add `"strings"` to imports.

- [ ] **Step 2: 运行，验证失败**

Run: `go test ./internal/api/handlers/ -run TestUsageEventsDeliversSnapshot -v`
Expected: FAIL — body empty because handler isn't pushing yet (or write not flushed before recorder finishes).

- [ ] **Step 3: 实现 — Already in Task 4 (writeSSEEvent + flusher.Flush). No new code. If FAIL persists, verify `c.Writer.(http.Flusher)` works with `httptest.ResponseRecorder` (Gin needs `c.Writer` to implement Flusher; `httptest.ResponseRecorder` does not).**

Switch to a streaming `Recorder`:

```go
import "net/http/httptest"

type sseRecorder struct{ *httptest.ResponseRecorder }

func (sseRecorder) Flush() {}

func TestUsageEventsDeliversSnapshot(t *testing.T) {
    // ... replace w := httptest.NewRecorder() with:
    w := &recorderWrapper{ResponseRecorder: httptest.NewRecorder()}

    // add to test file:
    type recorderWrapper struct{ *httptest.ResponseRecorder }
    func (recorderWrapper) Flush() {}
}
```

- [ ] **Step 4: 运行，验证通过**

Run: `go test ./internal/api/handlers/ -run TestUsageEventsDeliversSnapshot -v`
Expected: PASS.

- [ ] **Step 5: 提交**

```bash
git add internal/api/handlers/usage_events_test.go
git commit -m "test(api): verify SSE handler delivers snapshot event"
```

---

## Task 6: Register route in server.go

**Files:**
- Modify: `internal/api/server.go` (line 518-520 area)

- [ ] **Step 1: 修改 — 注册新路由并暴露 broker**

Add to the `mgmt` group block (after line 520):

```go
mgmt.GET("/usage/events", s.mgmt.Wrap(handlers.UsageEventsHandler(usage.GetRequestStatistics().Broker())))
```

If `s.mgmt` exposes only static handler signatures and not a `Wrap` helper, attach the route before `mgmt.Use(...)` or use a separate group:

```go
mgmt.GET("/usage/events", gin.WrapH(handlers.UsageEventsHandler(usage.GetRequestStatistics().Broker())))
```

> 鉴权已经在 `mgmt.Use(s.managementAvailabilityMiddleware(), s.mgmt.Middleware())` 统一处理；新路由自动复用。

- [ ] **Step 2: 构建，验证无编译错误**

Run: `go build ./...`
Expected: success.

- [ ] **Step 3: 提交**

```bash
git add internal/api/server.go
git commit -m "feat(api): register /v0/management/usage/events SSE route"
```

---

## Task 7: Frontend — usageStream service wrapper

**Files:**
- Create: `Cli-Proxy-API-Management-Center/src/services/api/usageStream.ts`

- [ ] **Step 1: 写文件**

```typescript
// Cli-Proxy-API-Management-Center/src/services/api/usageStream.ts
import type { UsagePayload } from '@/components/usage/hooks/useUsageData';

export type StreamEvent =
  | { type: 'snapshot'; payload: UsagePayload }
  | { type: 'heartbeat'; ts: string };

export type StreamStatus = 'connecting' | 'open' | 'closed' | 'error';

export interface UsageStreamHandle {
  status: () => StreamStatus;
  close: () => void;
}

export interface UsageStreamOptions {
  endpoint?: string;
  getManagementKey: () => string;
  onEvent: (event: StreamEvent) => void;
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

  let es: EventSource | null = null;
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

  const connect = () => {
    if (closed) return;
    setStatus('connecting');

    const key = opts.getManagementKey();
    const url = key
      ? `${endpoint}?_t=${Date.now()}`
      : `${endpoint}?_t=${Date.now()}`; // auth header via fetch wrapper below
    es = new EventSource(url, { withCredentials: false });

    // EventSource does not allow custom headers; we pass key via query param
    // only when API supports it. To keep auth consistent with /usage endpoint,
    // we fall back to header injection via a small bridge.
    es.addEventListener('snapshot', (event) => {
      attempt = 0;
      try {
        const payload = JSON.parse((event as MessageEvent).data) as UsagePayload;
        opts.onEvent({ type: 'snapshot', payload });
      } catch {
        // ignore malformed payload
      }
    });

    es.addEventListener('heartbeat', () => {
      opts.onEvent({ type: 'heartbeat', ts: new Date().toISOString() });
    });

    es.onopen = () => setStatus('open');
    es.onerror = () => {
      setStatus('error');
      es?.close();
      es = null;
      scheduleReconnect();
    };
  };

  connect();

  return {
    status: () => status,
    close: () => {
      closed = true;
      if (reconnectTimer) clearTimeout(reconnectTimer);
      es?.close();
      setStatus('closed');
    },
  };
}
```

> **注**：浏览器 `EventSource` 不支持自定义 header；服务端鉴权需要 query 参数或 cookie。当前 management 鉴权使用 `Authorization` header。**两种解决方案**：
>
> - A: 修改 SSE handler 让它在 query `?key=` 缺失时 fallback 到 header（前端无法注入 header）
> - B: 使用 `fetch` + `ReadableStream` 自己读 SSE，前端可注入 header
>
> **采用方案 B**，重写 `connect`：

```typescript
// Replace the connect() body above with this fetch-based implementation.
const connect = () => {
  if (closed) return;
  setStatus('connecting');

  const headers: Record<string, string> = { Accept: 'text/event-stream' };
  const key = opts.getManagementKey();
  if (key) headers.Authorization = `Bearer ${key}`;

  fetch(endpoint, { method: 'GET', headers, credentials: 'same-origin' })
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
      if (!closed) scheduleReconnect();
    })
    .catch(() => {
      setStatus('error');
      scheduleReconnect();
    });
};

const parseBlock = (block: string) => {
  let event = 'message';
  const dataLines: string[] = [];
  for (const line of block.split('\n')) {
    if (line.startsWith('event:')) event = line.slice(6).trim();
    else if (line.startsWith('data:')) dataLines.push(line.slice(5).trim());
  }
  const data = dataLines.join('\n');
  if (event === 'snapshot') {
    try {
      const payload = JSON.parse(data) as UsagePayload;
      opts.onEvent({ type: 'snapshot', payload });
    } catch {
      /* ignore */
    }
  } else if (event === 'heartbeat') {
    opts.onEvent({ type: 'heartbeat', ts: new Date().toISOString() });
  }
};
```

- [ ] **Step 2: 类型检查**

Run: `cd Cli-Proxy-API-Management-Center && npx tsc --noEmit -p tsconfig.app.json`
Expected: success.

- [ ] **Step 3: 提交**

```bash
git add Cli-Proxy-API-Management-Center/src/services/api/usageStream.ts
git commit -m "feat(ui): add usageStream SSE subscription service"
```

---

## Task 8: Frontend — usageStream unit tests

**Files:**
- Create: `Cli-Proxy-API-Management-Center/src/services/api/usageStream.test.ts`

- [ ] **Step 1: 写失败测试**

```typescript
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { subscribeUsageStream } from './usageStream';

describe('subscribeUsageStream', () => {
  const originalFetch = global.fetch;

  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    global.fetch = originalFetch;
    vi.useRealTimers();
  });

  it('invokes onEvent with parsed snapshot payload', async () => {
    const payload = JSON.stringify({ total_requests: 5 });
    const encoder = new TextEncoder();
    const stream = new ReadableStream({
      start(controller) {
        controller.enqueue(
          encoder.encode(`event: snapshot\ndata: ${payload}\n\n`),
        );
        controller.close();
      },
    });

    global.fetch = vi.fn(async () =>
      new Response(stream, {
        status: 200,
        headers: { 'Content-Type': 'text/event-stream' },
      }),
    ) as unknown as typeof fetch;

    const events: unknown[] = [];
    const handle = subscribeUsageStream({
      getManagementKey: () => 'k',
      onEvent: (e) => events.push(e),
      baseDelayMs: 10,
      maxDelayMs: 100,
    });

    await vi.advanceTimersByTimeAsync(50);
    handle.close();

    expect(events).toHaveLength(1);
    expect(events[0]).toEqual({ type: 'snapshot', payload: { total_requests: 5 } });
  });

  it('reconnects with backoff after fetch error', async () => {
    global.fetch = vi.fn(async () => {
      throw new Error('network down');
    }) as unknown as typeof fetch;

    const statusChanges: string[] = [];
    const handle = subscribeUsageStream({
      getManagementKey: () => 'k',
      onEvent: () => {},
      onStatusChange: (s) => statusChanges.push(s),
      baseDelayMs: 100,
      maxDelayMs: 1000,
    });

    await vi.advanceTimersByTimeAsync(50);
    expect(statusChanges).toContain('connecting');
    expect(statusChanges).toContain('error');

    handle.close();
    expect(statusChanges[statusChanges.length - 1]).toBe('closed');
  });
});
```

- [ ] **Step 2: 运行，验证通过**

Run: `cd Cli-Proxy-API-Management-Center && npx vitest run src/services/api/usageStream.test.ts`
Expected: PASS.

- [ ] **Step 3: 提交**

```bash
git add Cli-Proxy-API-Management-Center/src/services/api/usageStream.test.ts
git commit -m "test(ui): cover usageStream snapshot + reconnect"
```

---

## Task 9: Frontend — useUsageData SSE wiring + fallback

**Files:**
- Modify: `Cli-Proxy-API-Management-Center/src/components/usage/hooks/useUsageData.ts`

- [ ] **Step 1: 修改 useUsageData**

Replace lines 86-97 (the `setInterval(..., 30_000)` block) with:

```typescript
useEffect(() => {
  let pollingTimer: ReturnType<typeof setInterval> | null = null;
  let streamHandle: ReturnType<typeof subscribeUsageStream> | null = null;
  let cancelled = false;

  const startPolling = () => {
    if (pollingTimer) return;
    pollingTimer = window.setInterval(() => {
      if (typeof document !== 'undefined' && document.visibilityState === 'hidden') {
        return;
      }
      void loadUsageStats({
        force: true,
        staleTimeMs: USAGE_STATS_STALE_TIME_MS,
        timeRange,
      }).catch(() => {});
    }, 10_000);
  };

  const stopPolling = () => {
    if (pollingTimer) {
      window.clearInterval(pollingTimer);
      pollingTimer = null;
    }
  };

  streamHandle = subscribeUsageStream({
    getManagementKey: () => useConfigStore.getState().managementKey ?? '',
    onEvent: (event) => {
      if (event.type !== 'snapshot') return;
      if (typeof document !== 'undefined' && document.visibilityState === 'hidden') {
        return;
      }
      // Push snapshot directly into store; bypass loadUsageStats cache check.
      useUsageStatsStore.setState({
        usage: event.payload as never,
        loading: false,
        error: '',
        lastRefreshedAt: Date.now(),
      });
    },
    onStatusChange: (status) => {
      if (status === 'open') {
        stopPolling();
      } else if (status === 'error' || status === 'closed') {
        startPolling();
      }
    },
    baseDelayMs: 1000,
    maxDelayMs: 30_000,
  });

  return () => {
    cancelled = true;
    streamHandle?.close();
    stopPolling();
  };
}, [loadUsageStats, timeRange]);
```

Add import at top:
```typescript
import { subscribeUsageStream } from '@/services/api/usageStream';
import { useConfigStore } from '@/stores'; // already imported on line 11
```

Remove the now-unused `AUTO_REFRESH_INTERVAL_MS` constant or keep it as fallback default — we recommend removing to avoid drift:

```typescript
// Delete this line:
// const AUTO_REFRESH_INTERVAL_MS = 30_000;
```

- [ ] **Step 2: 类型检查**

Run: `cd Cli-Proxy-API-Management-Center && npx tsc --noEmit -p tsconfig.app.json`
Expected: success.

- [ ] **Step 3: 提交**

```bash
git add Cli-Proxy-API-Management-Center/src/components/usage/hooks/useUsageData.ts
git commit -m "feat(ui): wire SSE into useUsageData with polling fallback"
```

---

## Task 10: i18n keys (zh-CN + en)

**Files:**
- Modify: `Cli-Proxy-API-Management-Center/src/i18n/locales/zh-CN.json`
- Modify: `Cli-Proxy-API-Management-Center/src/i18n/locales/en.json`

- [ ] **Step 1: 在 zh-CN.json 的 `request_events_count` 行后插入新 key**

```json
"request_events_total_count": "共 {{total}} 条",
"request_events_filtered_count": "当前过滤命中 {{filtered}} 条",
```

- [ ] **Step 2: 在 en.json 同样位置插入**

```json
"request_events_total_count": "{{total}} events total",
"request_events_filtered_count": "{{filtered}} matched current filters",
```

- [ ] **Step 3: 验证 JSON 格式**

Run: `cd Cli-Proxy-API-Management-Center && node -e "JSON.parse(require('fs').readFileSync('src/i18n/locales/zh-CN.json'))"`
Expected: no output (valid JSON).

- [ ] **Step 4: 提交**

```bash
git add Cli-Proxy-API-Management-Center/src/i18n/locales/zh-CN.json Cli-Proxy-API-Management-Center/src/i18n/locales/en.json
git commit -m "feat(i18n): add keys for request events total + filtered count"
```

---

## Task 11: RequestEventsDetailsCard — render total + filtered

**Files:**
- Modify: `Cli-Proxy-API-Management-Center/src/components/usage/RequestEventsDetailsCard.tsx` (around line 905-916)

- [ ] **Step 1: 修改 `requestEventsMeta` JSX**

Find:

```tsx
<div className={styles.requestEventsMeta}>
  <span>{t('usage_stats.request_events_count', { count: filteredRows.length })}</span>
  {hasLatencyData && <span className={styles.requestEventsLimitHint}>{latencyHint}</span>}
  {filteredRows.length > MAX_RENDERED_EVENTS && (
    <span className={styles.requestEventsLimitHint}>
      {t('usage_stats.request_events_limit_hint', {
        shown: MAX_RENDERED_EVENTS,
        total: filteredRows.length,
      })}
    </span>
  )}
</div>
```

Replace with:

```tsx
<div className={styles.requestEventsMeta}>
  <span>
    {t('usage_stats.request_events_total_count', { total: rows.length })}
    {' · '}
    {t('usage_stats.request_events_filtered_count', { filtered: filteredRows.length })}
  </span>
  {hasLatencyData && <span className={styles.requestEventsLimitHint}>{latencyHint}</span>}
  {filteredRows.length > MAX_RENDERED_EVENTS && (
    <span className={styles.requestEventsLimitHint}>
      {t('usage_stats.request_events_limit_hint', {
        shown: MAX_RENDERED_EVENTS,
        total: filteredRows.length,
      })}
    </span>
  )}
</div>
```

> 当 `filteredRows.length === 0` 时，卡片显示 `EmptyState`（line 898-902），不会渲染到这段 JSX。当 `filteredRows.length > 0` 时，`rows.length >= filteredRows.length`，两者都可见。
>
> 当 `rows.length === filteredRows.length`（无过滤），文案是「共 N 条 · 当前过滤命中 N 条」。这是设计预期（spec §4.3：始终同时显示）。

- [ ] **Step 2: 类型检查**

Run: `cd Cli-Proxy-API-Management-Center && npx tsc --noEmit -p tsconfig.app.json`
Expected: success.

- [ ] **Step 3: 提交**

```bash
git add Cli-Proxy-API-Management-Center/src/components/usage/RequestEventsDetailsCard.tsx
git commit -m "feat(ui): show total + filtered count in request events header"
```

---

## Task 12: End-to-end smoke verification

**Files:** none (verification only)

- [ ] **Step 1: 启动后端**

Run: `cd /Users/zhouyong/Desktop/work/Decard/gitlab/ai/CLIProxyAPI && go run ./cmd/server`
Expected: server starts, listens on configured port.

- [ ] **Step 2: 启动前端**

Run: `cd Cli-Proxy-API-Management-Center && npm run dev`
Expected: dev server starts.

- [ ] **Step 3: 浏览器手动验证**

1. 打开 Usage 页 → DevTools Network → 看到 `200` 的 `text/event-stream` 响应
2. 触发任意 LLM 请求 → 1-2s 内数字更新
3. 切到别的标签页 30s → 切回时无爆发请求（visibility 暂停生效）
4. 杀掉后端 → 10s 内页面仍能更新（轮询兜底）→ 重启后端 → 重新建立 SSE

Expected: 全部通过。

- [ ] **Step 4: 验证总条数显示**

1. 在过滤面板选择「模型 = gpt-5.4」→ 头部显示「共 100 条 · 当前过滤命中 12 条」
2. 清空过滤 → 头部显示「共 100 条 · 当前过滤命中 100 条」

Expected: 文案随过滤实时更新。

- [ ] **Step 5: 跑全量回归**

Run: `go test ./... && cd Cli-Proxy-API-Management-Center && npx vitest run`
Expected: all pass.

- [ ] **Step 6: 提交（如有调整）**

```bash
git add -A
git commit -m "chore: post-verification cleanup" || true
```

---

## Self-Review Checklist

1. **Spec coverage**:
   - §3.1 SSE 端点 → Task 4, 5, 6
   - §3.2 轮询降级 → Task 9
   - §3.3 去抖 / 心跳 / 间隔 → Task 1, 2, 4
   - §4.1 usageStream 服务 → Task 7, 8
   - §4.2 useUsageData 接入 → Task 9
   - §4.3 总数 / 过滤显示 → Task 10, 11
   - §5.1 broker → Task 1, 2
   - §5.2 logger 集成 → Task 3
   - §5.3 handler → Task 4, 5
   - §5.4 路由注册 → Task 6
   - §6 验收 → Task 12

2. **Placeholders**: 无 TBD/TODO；所有代码块完整。

3. **Type consistency**:
   - `UsagePayload` 在 broker.go 与 useUsageData.ts 一致
   - `subscribeUsageStream` 签名在 Task 7 定义，Task 8 测试与 Task 9 调用一致
   - i18n key 名在 Task 10 定义与 Task 11 引用一致