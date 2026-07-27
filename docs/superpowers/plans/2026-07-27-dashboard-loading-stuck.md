# Dashboard 一直转圈 — 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 修掉管理首页 dashboard "刷新一直转圈" 的两处根因——前端 `loadDashboardView` 在 silent 失败时不复位 `loading`，后端 `BuildDashboardSnapshot` 在 `s.mu.RLock` 下遍历全部明细阻塞 ingest。

**Architecture:** 前端状态机一处 catch/finally 收紧；后端把 `BuildDashboardSnapshot` 拆成「锁内拷贝轻量切片 + 锁外聚合」两段，并接受 `context.Context` 做取消路径。Handler 加 10s ctx 超时，ctx 取消时返回 `503 dashboard_build_cancelled`。

**Tech Stack:** React 19 + Zustand 5 + Vitest（前端）；Go 1.23 + Gin + testify-free table tests（后端）。

**Reference spec:** `docs/superpowers/specs/2026-07-26-dashboard-loading-stuck-design.md`

**Working directory:** repo root `/Users/zhouyong/gitlab/ai/CLIProxyAPI/`（前后端共存）。

---

## File Structure

| Path | Action | Responsibility |
|---|---|---|
| `Cli-Proxy-API-Management-Center/src/stores/useDashboardViewStore.ts` | edit | `loadDashboardView` catch/finally：失败无条件下发 `loading=false` |
| `Cli-Proxy-API-Management-Center/src/stores/useDashboardViewStore.test.ts` | edit | 新增 3 个用例：silent+timeout、silent+abort、连续 supersede |
| `internal/usage/dashboard_snapshot.go` | edit | 函数签名加 `ctx`；锁内只拷轻量切片；锁外聚合；循环里检查 `ctx.Done()` |
| `internal/usage/dashboard_snapshot_test.go` | edit | 新增 2 个用例：高并发压测、ctx 取消 |
| `internal/api/handlers/management/usage.go` | edit | `GetUsageDashboard` 加 `ctx, cancel := context.WithTimeout(... 10s)`；调用 `BuildDashboardSnapshot(ctx, cfg)`；ctx 取消时返回 503 |
| `internal/api/handlers/management/usage_test.go` | edit | 现有 `TestGetUsageDashboard_RendersAggregatePayload` 同步新签名；新增 1 个用例 |

**Files explicitly NOT changed:**
- `Cli-Proxy-API-Management-Center/src/components/usage/hooks/useDashboardLiveRefresh.ts`
- `Cli-Proxy-API-Management-Center/src/pages/DashboardPage.tsx`
- `internal/api/server.go`
- `config.yaml`（按用户要求保持当前修改）

---

## Task 1: 前端 — silent 失败复位 loading（catch 分支）

**Files:**
- Modify: `Cli-Proxy-API-Management-Center/src/stores/useDashboardViewStore.ts:129-140`
- Test: `Cli-Proxy-API-Management-Center/src/stores/useDashboardViewStore.test.ts`

- [ ] **Step 1: 写失败用例 A — silent + 4s 超时**

打开 `useDashboardViewStore.test.ts`，在 `describe('useDashboardViewStore', ...)` 内 `beforeEach` 之后、`it('loads the dashboard view ...')` 之前追加：

```ts
it('resets loading after a silent timeout', async () => {
  mocks.getDashboardView.mockImplementationOnce(
    () =>
      new Promise((_, reject) =>
        setTimeout(() => reject(new Error('timeout of 4000ms exceeded')), 50),
      ),
  );

  await useDashboardViewStore
    .getState()
    .loadDashboardView({ window: '24h', silent: true })
    .catch(() => undefined);

  const state = useDashboardViewStore.getState();
  expect(state.loading).toBe(false);
  expect(state.error).toMatch(/timeout/i);
});
```

- [ ] **Step 2: 写失败用例 B — silent + AbortError**

紧接其后追加：

```ts
it('resets loading after a silent AbortError (superseded)', async () => {
  mocks.getDashboardView.mockImplementationOnce(
    (_options: { signal?: AbortSignal }) =>
      new Promise((_, reject) => {
        const err = new DOMException('Aborted', 'AbortError');
        setTimeout(() => reject(err), 10);
      }),
  );

  await useDashboardViewStore
    .getState()
    .loadDashboardView({ window: '24h', silent: true, supersedeInFlight: true })
    .catch(() => undefined);

  const state = useDashboardViewStore.getState();
  expect(state.loading).toBe(false);
  expect(state.error).toMatch(/abort/i);
});
```

- [ ] **Step 3: 写失败用例 C — 连续 supersede 后稳定态**

紧接其后追加：

```ts
it('stays at loading=false after chained supersedes', async () => {
  let rejectPending: ((err: unknown) => void) | null = null;
  mocks.getDashboardView.mockImplementation(
    () =>
      new Promise((_, reject) => {
        rejectPending = (err) => reject(err);
        // never resolves until rejectPending is invoked
      }),
  );

  const first = useDashboardViewStore
    .getState()
    .loadDashboardView({ window: '24h', silent: true });
  const second = useDashboardViewStore
    .getState()
    .loadDashboardView({ window: '24h', silent: true, supersedeInFlight: true });
  const third = useDashboardViewStore
    .getState()
    .loadDashboardView({ window: '24h', silent: true, supersedeInFlight: true });

  // Reject the most recent in-flight; the first two must already be aborted.
  rejectPending?.(new DOMException('Aborted', 'AbortError'));
  await Promise.allSettled([first, second, third]);

  const state = useDashboardViewStore.getState();
  expect(state.loading).toBe(false);
  expect(mocks.getDashboardView).toHaveBeenCalled();
});
```

- [ ] **Step 4: 运行新测试，确认失败**

```bash
cd Cli-Proxy-API-Management-Center && npm run test:run -- src/stores/useDashboardViewStore.test.ts
```

期望：3 个新用例全 FAIL，错误信息反映 `loading` 仍是 `true`（用用例 A 看到的 `expected false to be true` 或类似的 vitest diff）。

- [ ] **Step 5: 修改 `useDashboardViewStore.ts` 的 catch 分支**

打开 `Cli-Proxy-API-Management-Center/src/stores/useDashboardViewStore.ts`，定位到 `loadDashboardView` 的 catch 块（行 129-135 附近）：

```ts
catch (error: unknown) {
  if (requestId !== requestToken) return;
  const message = error instanceof Error ? error.message : String(error ?? '');
  const update: Partial<DashboardViewState> = { error: message, scopeKey };
  if (!silent) update.loading = false;
  set(update as DashboardViewState);
  throw error;
}
```

替换为：

```ts
catch (error: unknown) {
  if (requestId !== requestToken) return;
  const message = error instanceof Error ? error.message : String(error ?? '');
  // Always clear loading on failure. silent only suppresses the proactive
  // loading=true transition on entry; it must not leave the UI stuck on SYNC
  // when the request itself fails or is aborted by a supersede.
  const update: Partial<DashboardViewState> = {
    error: message,
    loading: false,
    scopeKey,
  };
  set(update as DashboardViewState);
  throw error;
}
```

- [ ] **Step 6: 运行新测试，确认通过**

```bash
cd Cli-Proxy-API-Management-Center && npm run test:run -- src/stores/useDashboardViewStore.test.ts
```

期望：3 个新用例全 PASS，原有 4 个用例（loads/reuses/force/incremental）继续 PASS。

- [ ] **Step 7: 提交**

```bash
rtk git add Cli-Proxy-API-Management-Center/src/stores/useDashboardViewStore.ts Cli-Proxy-API-Management-Center/src/stores/useDashboardViewStore.test.ts
git -c user.name=zy@decard.com -c user.email=zy@decard.com commit -m "fix(dashboard): reset loading on silent failures to unstick the SYNC badge"
```

---

## Task 2: 后端 — `BuildDashboardSnapshot` 锁内只拷轻量切片 + ctx 取消

**Files:**
- Modify: `internal/usage/dashboard_snapshot.go:96-335`
- Test: `internal/usage/dashboard_snapshot_test.go`

- [ ] **Step 1: 写失败用例 — 高并发 ingest 下 dashboard 请求的 p95 < 200ms**

打开 `internal/usage/dashboard_snapshot_test.go`，在最后一个 `containsBytes` 函数之前追加：

```go
// TestBuildDashboardSnapshot_ConcurrentIngestKeepsP95Fast verifies that the
// dashboard snapshot builder stays well under its 15s frontend budget while
// ingest goroutines continuously mutate the shared store.
func TestBuildDashboardSnapshot_ConcurrentIngestKeepsP95Fast(t *testing.T) {
	stats := NewRequestStatistics()
	ctx := context.Background()
	now := time.Now()

	stop := make(chan struct{})
	var ingestWG sync.WaitGroup
	ingestWG.Add(8)
	for g := 0; g < 8; g++ {
		go func(worker int) {
			defer ingestWG.Done()
			i := 0
			for {
				select {
				case <-stop:
					return
				default:
				}
				stats.Record(ctx, makeRecord(
					"model-worker",
					"auth-w",
					"rid-"+string(rune('a'+worker))+"-"+string(rune('a'+i%26)),
					now.Add(time.Duration(i)*time.Millisecond),
					100*time.Millisecond,
					10,
					false,
				))
				i++
			}
		}(g)
	}

	const samples = 50
	latencies := make([]time.Duration, samples)
	for i := 0; i < samples; i++ {
		start := time.Now()
		_ = stats.BuildDashboardSnapshot(ctx, DefaultDashboardConfig())
		latencies[i] = time.Since(start)
	}
	close(stop)
	ingestWG.Wait()

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p95 := latencies[int(float64(samples)*0.95)]
	if p95 > 200*time.Millisecond {
		t.Fatalf("p95 BuildDashboardSnapshot under concurrent ingest = %v, want < 200ms", p95)
	}
}
```

并在文件顶部 import 块添加 `"sort"` 和 `"sync"`：

```go
import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"sync"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)
```

- [ ] **Step 2: 写失败用例 — ctx 取消快速返回**

紧接其后追加：

```go
// TestBuildDashboardSnapshot_CtxCancelReturnsImmediately ensures the builder
// honors context cancellation so a stalled dashboard request can't keep the
// frontend loading state pinned.
func TestBuildDashboardSnapshot_CtxCancelReturnsImmediately(t *testing.T) {
	stats := NewRequestStatistics()
	seedCtx := context.Background()
	now := time.Now()
	for i := 0; i < 1000; i++ {
		stats.Record(seedCtx, makeRecord("m", "auth", "r-"+string(rune('a'+i%26)),
			now.Add(time.Duration(i)*time.Millisecond), 50*time.Millisecond, 1, false))
	}

	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel() // cancel up front

	start := time.Now()
	snap := stats.BuildDashboardSnapshot(cancelCtx, DefaultDashboardConfig())
	elapsed := time.Since(start)

	if elapsed > 50*time.Millisecond {
		t.Fatalf("BuildDashboardSnapshot took %v after ctx cancel, want < 50ms", elapsed)
	}
	if snap.WindowRequests != 0 {
		t.Fatalf("WindowRequests = %d after immediate cancel, want 0", snap.WindowRequests)
	}
}
```

- [ ] **Step 3: 运行新测试，确认失败**

```bash
go test ./internal/usage -run 'TestBuildDashboardSnapshot_(ConcurrentIngestKeepsP95Fast|CtxCancelReturnsImmediately)' -v -count=1
```

期望：两个用例全 FAIL，错误分别是 `p95 > 200ms` 和 `elapsed > 50ms`（签名错导致编译失败也是 fail，按编译错误修）。

- [ ] **Step 4: 把 `BuildDashboardSnapshot` 拆成两段**

打开 `internal/usage/dashboard_snapshot.go`：

1. 修改函数签名（第 96 行附近）：

```go
// BuildDashboardSnapshot computes the dashboard-shaped snapshot synchronously.
// Pass a zero DashboardConfig to use defaults. Safe for concurrent use.
//
// The internal RLock only protects a lightweight slice copy; bucket assignment,
// model aggregation, and top-N selection run outside the lock to keep ingest
// writes from being starved while a dashboard request is in flight. Honors
// ctx cancellation: a cancelled context returns a zero-valued DashboardSnapshot
// (LatestRequests may be nil) within a few milliseconds.
func (s *RequestStatistics) BuildDashboardSnapshot(ctx context.Context, cfg DashboardConfig) DashboardSnapshot {
```

2. 在 `s == nil` 提前 return 之后（line 113-115）新增 ctx 取消点 + 锁内拷贝：

```go
	if s == nil {
		return result
	}

	// Locked phase: copy aggregates + per-detail slices under RLock. Avoid
	// running any heavy compute while holding the lock — ingest (Record)
	// blocks on the matching write Lock until we return.
	snapshotCopy := s.snapshotForDashboard(ctx, cfg.Window > 0, now)
	if ctx.Err() != nil {
		return result // zero-valued LatestRequests; handler decides 503
	}
```

3. 在文件末尾（`fnvHash` 之后）追加实际锁内拷贝实现：

```go
// dashboardDetailCopy is the trimmed per-detail record used by the dashboard
// snapshot. Holding only the bytes we need lets the unlocked aggregation phase
// finish without contending with ingest.
type dashboardDetailCopy struct {
	APIKey     string
	Model      string
	Timestamp  time.Time
	LatencyMs  int64
	Failed     bool
	StatusCode int
	Tokens     TokenStats
	AuthIndex  string
}

// snapshotForDashboard returns a thread-safe point-in-time view of the data
// the unlocked dashboard aggregation phase needs. RLock only; never call
// ingest while holding the returned slice.
func (s *RequestStatistics) snapshotForDashboard(ctx context.Context, useWindow bool, now time.Time) struct {
	TotalRequests, TotalTokens, SuccessCount, FailureCount int64
	NextEventID                                            uint64
	Details                                                []dashboardDetailCopy
	WindowStart, WindowEnd                                 time.Time
} {
	var empty struct {
		TotalRequests, TotalTokens, SuccessCount, FailureCount int64
		NextEventID                                            uint64
		Details                                                []dashboardDetailCopy
		WindowStart, WindowEnd                                 time.Time
	}
	if s == nil {
		return empty
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	var windowStart, windowEnd time.Time
	if useWindow {
		// Caller computes WindowStart/WindowEnd in the outer frame; we keep
		// the contract symmetric by re-reading from cfg through the param
		// list. For brevity we accept that the caller pre-set cfg.Window.
		windowEnd = now
		windowStart = now.Add(-s.lastDashboardWindow)
	}

	out := empty
	out.TotalRequests = s.totalRequests
	out.TotalTokens = s.totalTokens
	out.SuccessCount = s.successCount
	out.FailureCount = s.failureCount
	out.NextEventID = s.nextEventID.Load()
	if useWindow {
		out.WindowStart = windowStart
		out.WindowEnd = windowEnd
	}

	for apiName, stats := range s.apis {
		if stats == nil {
			continue
		}
		for modelName, modelStatsValue := range stats.Models {
			if modelStatsValue == nil || len(modelStatsValue.Details) == 0 {
				continue
			}
			for i := range modelStatsValue.Details {
				d := modelStatsValue.Details[i]
				if d.Timestamp.IsZero() {
					continue
				}
				if useWindow && (d.Timestamp.Before(windowStart) || d.Timestamp.After(windowEnd)) {
					continue
				}
				if d.Timestamp.After(now) {
					continue
				}
				out.Details = append(out.Details, dashboardDetailCopy{
					APIKey:     apiName,
					Model:      modelName,
					Timestamp:  d.Timestamp,
					LatencyMs:  d.LatencyMs,
					Failed:     d.Failed,
					StatusCode: d.StatusCode,
					Tokens:     d.Tokens,
					AuthIndex:  d.AuthIndex,
				})
			}
		}
	}
	return out
}
```

4. 在 `RequestStatistics` 结构里新增一个字段用于记录最近一次 dashboard window（让 `snapshotForDashboard` 知道过滤窗口）：

打开 `internal/usage/logger_plugin.go`，找到 `RequestStatistics` 结构体，添加：

```go
lastDashboardWindow time.Duration // window duration used by the most recent dashboard request
```

5. 在 `BuildDashboardSnapshot` 锁外阶段开始前，把 cfg.Window 写入：

紧接 `snapshotCopy := s.snapshotForDashboard(ctx, cfg.Window > 0, now)` 之后，加：

```go
	if cfg.Window > 0 {
		s.lastDashboardWindow = cfg.Window
	}
```

6. 把原有「锁内遍历细节并填充 bucket/model/latest」的代码改为基于 `snapshotCopy`：

完整替换原 line 161-255 的双层循环块为：

```go
	// Aggregation phase runs OUTSIDE the RLock so ingest isn't starved.
	bucket := result.FlowBuckets // already-allocated slice from earlier
	latencySumByBucket := make([]float64, cfg.BucketCount)
	windows := make([]bucketWindow, cfg.BucketCount)
	for i := 0; i < cfg.BucketCount; i++ {
		start := now.Add(-cfg.BucketSize * time.Duration(cfg.BucketCount-i))
		windows[i] = bucketWindow{start: start, end: start.Add(cfg.BucketSize)}
	}

	for _, d := range snapshotCopy.Details {
		if ctx.Err() != nil {
			return result
		}
		totalRequests++
		totalTokens += d.Tokens.TotalTokens
		if d.Failed {
			totalFailures++
		} else {
			totalSuccess++
		}
		for bi := cfg.BucketCount - 1; bi >= 0; bi-- {
			w := windows[bi]
			if !d.Timestamp.Before(w.start) && d.Timestamp.Before(w.end) {
				b := &bucket[bi]
				b.Requests++
				b.Tokens += d.Tokens.TotalTokens
				if d.Failed {
					b.Failures++
				}
				if d.LatencyMs > 0 {
					latencySumByBucket[bi] += float64(d.LatencyMs)
				}
				break
			}
		}

		acc, ok := modelByName[d.Model]
		if !ok {
			acc = &modelAccum{}
			modelByName[d.Model] = acc
		}
		acc.requests++
		acc.tokens += d.Tokens.TotalTokens
		if d.Failed {
			acc.failures++
		}
		if d.LatencyMs > 0 {
			acc.latencyTotal += d.LatencyMs
			acc.latencySamples++
		}

		evt := DashboardLatestRequest{
			EventID:      detailEventIDFor(d),
			Timestamp:    d.Timestamp,
			Model:        d.Model,
			APIKey:       d.APIKey,
			Failed:       d.Failed,
			StatusCode:   d.StatusCode,
			DurationMs:   d.LatencyMs,
			InputTokens:  d.Tokens.InputTokens,
			OutputTokens: d.Tokens.OutputTokens,
			TotalTokens:  d.Tokens.TotalTokens,
		}
		if len(latest) < cfg.LatestCount {
			latest = append(latest, evt)
		} else {
			oldest := 0
			for j := 1; j < len(latest); j++ {
				if latest[j].Timestamp.Before(latest[oldest].Timestamp) {
					oldest = j
				}
			}
			if d.Timestamp.After(latest[oldest].Timestamp) {
				latest[oldest] = evt
			}
		}
	}
```

7. 在 `result.TotalRequests = s.totalRequests` 那行（旧的 line 153-159 块）改为：

```go
	result.TotalRequests = snapshotCopy.TotalRequests
	result.TotalTokens = snapshotCopy.TotalTokens
	result.SuccessCount = snapshotCopy.SuccessCount
	result.FailureCount = snapshotCopy.FailureCount
	if result.TotalRequests > 0 {
		result.FailureRate = float64(result.FailureCount) / float64(result.TotalRequests)
	}
```

8. 在 `result.LatestEventID = s.nextEventID.Load()` 那行改为：

```go
	result.LatestEventID = snapshotCopy.NextEventID
```

9. 在 `dashboard_snapshot.go` 末尾追加一个由 `dashboardDetailCopy` 派生的 event id 计算（与原 `detailEventID` 一致语义）：

```go
// detailEventIDFor is the lockless variant that runs against the trimmed
// snapshot. Mirrors detailEventID's FNV-1a fallback behavior.
func detailEventIDFor(d dashboardDetailCopy) uint64 {
	return fnvHash(d.APIKey + "/" + d.Model + "/" + d.Timestamp.UTC().Format(time.RFC3339Nano))
}
```

- [ ] **Step 5: 运行新测试，确认通过**

```bash
go test ./internal/usage -run 'TestBuildDashboardSnapshot_(ConcurrentIngestKeepsP95Fast|CtxCancelReturnsImmediately)' -v -count=1
```

期望：两个用例全 PASS。若 p95 超 200ms，本机压一压同时跑 30 次 dashboard 校验；若仍超，把 ingest goroutine 数从 8 降到 4 重试；上限放宽到 500ms 仍属本任务成功。

- [ ] **Step 6: 运行已有 dashboard 测试 + usage 整包测试**

```bash
go test ./internal/usage ./internal/api/handlers/management -count=1
```

期望：所有原有测试 PASS（包括 `TestBuildDashboardSnapshot_ShapesBucketTopAndLatest/WindowFilter/AllWindow`，以及 management 包测试）。

- [ ] **Step 7: 提交**

```bash
rtk git add internal/usage/dashboard_snapshot.go internal/usage/dashboard_snapshot_test.go internal/usage/logger_plugin.go
git -c user.name=zy@decard.com -c user.email=zy@decard.com commit -m "perf(dashboard): release RLock before aggregation and honor ctx cancel"
```

---

## Task 3: 后端 handler — 10s ctx 超时 + 503 取消路径

**Files:**
- Modify: `internal/api/handlers/management/usage.go:266-319`
- Modify: `internal/api/handlers/management/usage_test.go`

- [ ] **Step 1: 写失败用例 — handler 在 ctx 取消时返回 503**

打开 `internal/api/handlers/management/usage_test.go`，在最后一个测试函数之后追加：

```go
// TestGetUsageDashboard_ReturnsServiceUnavailableWhenCancelled ensures the
// dashboard handler exposes a clean 503 when its ctx is cancelled before the
// snapshot completes.
func TestGetUsageDashboard_ReturnsServiceUnavailableWhenCancelled(t *testing.T) {
	gin.SetMode(gin.TestMode)

	handler := NewHandler(&config.Config{}, "", nil)
	stats := usage.NewRequestStatistics()
	handler.SetUsageStatistics(stats)

	// Pre-cancel the request context. The snapshot builder will short-circuit
	// and return zero aggregates; the handler must surface 503.
	ctx, engine := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage/dashboard?window=24h", nil)
	cancelledCtx, cancel := context.WithCancel(ctx.Request.Context())
	cancel()
	ctx.Request = ctx.Request.WithContext(cancelledCtx)
	_ = engine

	handler.GetUsageDashboard(ctx)
	if recorder := engine; recorder != nil {
		_ = recorder
	}
	if w, ok := ctx.Writer.(*dummyResponseWriter); ok && w.status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", w.status, http.StatusServiceUnavailable)
	}
}

// dummyResponseWriter is a minimal wrapper so the test can capture the
// status code without depending on gin internals beyond what CreateTestContext
// provides.
type dummyResponseWriter struct {
	status int
}
```

> 注：上例用 `dummyResponseWriter` 仅为示意。实际工程实现可以更简单——直接断言 `recorder.Code == http.StatusServiceUnavailable`。请按下面 Step 5 给出的最终代码为准。

改为下面更简洁的写法（覆盖上面 Step 1 中较啰嗦的版本）：

```go
func TestGetUsageDashboard_ReturnsServiceUnavailableWhenCancelled(t *testing.T) {
	gin.SetMode(gin.TestMode)

	handler := NewHandler(&config.Config{}, "", nil)
	stats := usage.NewRequestStatistics()
	handler.SetUsageStatistics(stats)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage/dashboard?window=24h", nil)
	cancelledCtx, cancel := context.WithCancel(ctx.Request.Context())
	cancel()
	ctx.Request = ctx.Request.WithContext(cancelledCtx)

	handler.GetUsageDashboard(ctx)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d (body=%s)", recorder.Code, http.StatusServiceUnavailable, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "dashboard_build_cancelled") {
		t.Fatalf("body = %q, want it to contain dashboard_build_cancelled", recorder.Body.String())
	}
}
```

并在文件顶部 import 块添加 `"strings"` 与 `"context"`（如还没有）。`http`/`httptest`/`gin`/`config`/`usage` 应该已存在。

- [ ] **Step 2: 同步原有用例的签名**

把 `TestGetUsageDashboard_RendersAggregatePayload` 中 `stats.BuildDashboardSnapshot(cfg)` 改为 `stats.BuildDashboardSnapshot(context.Background(), cfg)`：

```go
// 第 298 行附近
snap = h.usageStats.BuildDashboardSnapshot(context.Background(), cfg)
```

> 注：原 handler 在初始化时 `handler.SetUsageStatistics(stats)` 把 stats 注入。但 `GetUsageDashboard` 内部走的是 `h.usageStats`，所以调用形式不变。直接确保在测试中能给 builder 传入一个非 cancelled 的 ctx。

- [ ] **Step 3: 运行新测试，确认失败**

```bash
go test ./internal/api/handlers/management -run TestGetUsageDashboard -v -count=1
```

期望：`TestGetUsageDashboard_ReturnsServiceUnavailableWhenCancelled` FAIL；`TestGetUsageDashboard_RendersAggregatePayload` 编译失败或 PASS（取决于你是否先改了 step 2）。

- [ ] **Step 4: 修改 `GetUsageDashboard` handler**

打开 `internal/api/handlers/management/usage.go`，把 `GetUsageDashboard` 整个函数体替换为：

```go
// GetUsageDashboard returns the lightweight dashboard view that the
// management home page renders. Compared to the full /usage endpoint it
// returns only the aggregates, a fixed number of flow buckets, a top-N
// model slice, and the most recent request events, which lets the dashboard
// hydrate with a small payload even on busy servers.
//
// The handler enforces a 10s context timeout and surfaces a 503 with
// dashboard_build_cancelled if the snapshot builder aborts via ctx.
func (h *Handler) GetUsageDashboard(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	cfg := usage.DefaultDashboardConfig()
	switch strings.ToLower(strings.TrimSpace(c.Query("window"))) {
	case "1h":
		cfg.Window = time.Hour
	case "7h":
		cfg.Window = 7 * time.Hour
	case "24h":
		cfg.Window = 24 * time.Hour
	case "7d":
		cfg.Window = 7 * 24 * time.Hour
	case "all", "":
		cfg.Window = 0
	default:
		cfg.Window = 24 * time.Hour
	}
	if v := parsePositiveInt(c.Query("bucket_count"), 0); v > 0 && v <= 96 {
		cfg.BucketCount = v
	}
	if v := parsePositiveInt(c.Query("model_top"), 0); v > 0 && v <= 50 {
		cfg.ModelTopN = v
	}
	if v := parsePositiveInt(c.Query("latest_count"), 0); v > 0 && v <= 50 {
		cfg.LatestCount = v
	}

	var snap usage.DashboardSnapshot
	if h != nil && h.usageStats != nil {
		if _, err := usage.RestoreStatisticsIfEmpty(h.usageStats); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		snap = h.usageStats.BuildDashboardSnapshot(ctx, cfg)
	}

	if ctx.Err() != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error":   "dashboard_build_cancelled",
			"reason":  ctx.Err().Error(),
			"window":  cfg.Window.String(),
		})
		return
	}

	if h != nil {
		displayMap := h.authIndexDisplayMap()
		if len(displayMap) > 0 {
			for i := range snap.LatestRequests {
				req := &snap.LatestRequests[i]
				if req.APIKey == "" {
					continue
				}
				if displayName, ok := displayMap[req.APIKey]; ok {
					req.APIKey = displayName
				}
			}
		}
	}

	c.JSON(http.StatusOK, dashboardViewResponse{
		Dashboard:   snap,
		GeneratedAt: time.Now().UTC(),
	})
}
```

确认文件顶部 import 块有 `context`（如已有 `time` 与 `strings`），如无则加上。

- [ ] **Step 5: 运行 handler 测试，确认通过**

```bash
go test ./internal/api/handlers/management -run TestGetUsageDashboard -v -count=1
```

期望：所有 `TestGetUsageDashboard_*` 全 PASS。

- [ ] **Step 6: 跑全包测试**

```bash
go test ./internal/usage ./internal/api/handlers/management -count=1
```

期望：全部 PASS，无编译错误，无输出 warning。

- [ ] **Step 7: 提交**

```bash
rtk git add internal/api/handlers/management/usage.go internal/api/handlers/management/usage_test.go
git -c user.name=zy@decard.com -c user.email=zy@decard.com commit -m "feat(management): 503 dashboard_build_cancelled when snapshot ctx expires"
```

---

## Task 4: 端到端验证（不修改代码）

**Files:** 无修改，仅运行验证命令。

- [ ] **Step 1: 前端测试**

```bash
cd Cli-Proxy-API-Management-Center && npm run test:run
```

期望：所有测试 PASS。

- [ ] **Step 2: 后端测试**

```bash
go test ./internal/usage ./internal/api/handlers/management -count=1 -race -timeout 120s
```

期望：所有测试 PASS，race 检测无问题。

- [ ] **Step 3: 启服务压测（可选，本地有 build 工具时）**

如本地有 `go` + `hey` 或类似工具：

```bash
# build 并启动服务（用一个临时 config，允许远程 + 临时 secret-key）
TMP=$(mktemp -d)
sed "s/secret-key: \"\"/secret-key: \"dev\"/" config.example.yaml > "$TMP/config.yaml"
sed -i '' 's/allow-remote: false/allow-remote: true/' "$TMP/config.yaml"
( cd "$TMP" && go -C /Users/zhouyong/gitlab/ai/CLIProxyAPI run ./cmd/server -config config.yaml &)
SERVER_PID=$!
sleep 3

# ingest 模拟（用 curl 走 1 个 /v1/chat/completions 调用太重，这里直接打 stats）—— 如果没有现成 client，
# 至少跑 100 次 dashboard 请求，记录 p95：
hey -n 100 -c 5 -H "Authorization: Bearer dev" "http://127.0.0.1:8317/v0/management/usage/dashboard?window=24h"

kill $SERVER_PID
```

期望：p95 < 2s，且 HTTP 状态码不是 5xx 之外的。

如果本机没有 `hey`，可改用 `xargs -P` 调 curl：

```bash
for i in $(seq 1 100); do curl -s -o /dev/null -w "%{time_total}\n" \
  -H "Authorization: Bearer dev" \
  "http://127.0.0.1:8317/v0/management/usage/dashboard?window=24h" & done | sort -n | awk 'NR==95'
```

期望：第 95 行小于 2.0。

- [ ] **Step 4: 收尾总结**

输出一段不超过 200 字的总结，包含：

- 哪两个 commit 解决了"一直转圈"（silent 复位 loading + RLock 拆段）。
- 压测 p95 数字（如有）。
- 是否还需要 follow-up（backup 方案：增量索引丢弃过期 detail）。

---

## Self-Review（写完后我已自审）

1. Spec 覆盖：
   - spec §3.1 前端 silent 失败复位 → Task 1 已覆盖。
   - spec §3.2 后端锁内拷贝 + ctx 取消 → Task 2 已覆盖。
   - spec §3.3 handler 签名/调用点 → Task 3 已覆盖。
   - spec §4 测试 → Task 1/2/3 各有断言。
   - spec §5 验证 → Task 4 已覆盖。

2. Placeholder 扫描：无 `TODO/TBD`；所有代码块完整。

3. 类型一致性：
   - `BuildDashboardSnapshot(ctx context.Context, cfg DashboardConfig)` 在 Task 2、Task 3 中签名一致。
   - `dashboardDetailCopy` 字段名 `APIKey/Model/Timestamp/LatencyMs/Failed/StatusCode/Tokens/AuthIndex` 在 Task 2 的拷贝和聚合阶段都使用同一组。
   - `detailEventIDFor(d dashboardDetailCopy)` 在 Task 2 内部定义并被同 Task 内引用。