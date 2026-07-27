# Dashboard 刷新一直转圈 — 修复设计

日期：2026-07-26
状态：待 review
范围：前后端双修，仅触动 1 个前端 .ts + 1 个前端测试 + 1 个后端 .go + 1 个后端测试 + 1 个 handler 调用点。

## 1. 问题陈述

管理页首页 dashboard 触发“刷新”后，UI 文字徽标长期停留在 `SYNC`，用户感知为“转圈”。后端 `/v0/management/usage/dashboard` 接口在持续写入的服务器上，最坏情况下 15s 前端超时后才返回。前端 silent 路径不做 loading 复位，导致状态机永远停在 `loading: true`。两条因素的叠加是症状来源。

## 2. 根因

### 2.1 前端 — silent 失败不重置 loading

文件 `Cli-Proxy-API-Management-Center/src/stores/useDashboardViewStore.ts`，方法 `loadDashboardView`：

```ts
catch (error: unknown) {
  if (requestId !== requestToken) return;
  const message = error instanceof Error ? error.message : String(error ?? '');
  const update: Partial<DashboardViewState> = { error: message, scopeKey };
  if (!silent) update.loading = false;       // ← 罪魁
  set(update as DashboardViewState);
  throw error;
}
```

- `useDashboardLiveRefresh.ts` SSE 回调始终传入 `silent: true, supersedeInFlight: true`。
- 失败（网络、超时、被 supersede abort）时 `loading` 永远保持 `true`。
- `DashboardPage.tsx:632` 显示 `dashboardLoading ? 'SYNC' : '24H'`，呈现"一直转圈"。

### 2.2 后端 — 长读锁链阻塞

文件 `internal/usage/dashboard_snapshot.go`，`BuildDashboardSnapshot`：

```go
s.mu.RLock()
defer s.mu.RUnlock()
for apiName, stats := range s.apis {
  for modelName, modelStatsValue := range stats.Models {
    for i := range details = modelStatsValue.Details { ... }
  }
}
```

- `internal/usage/logger_plugin.go:239` `Record` 走 `s.mu.Lock()`，必须等所有 RLock 释放。
- 任意一次 `BuildDashboardSnapshot` 都要遍历所有 API × Model × Detail 整个切片。
- 没有 ctx 取消路径，无法在已被抢占时立即返回。

handler `internal/api/handlers/management/usage.go:266-319` 同步调用 `BuildDashboardSnapshot`，也没有 ctx 取消。

## 3. 修复设计

### 3.1 前端 — 让 loading 永远收敛

文件 `Cli-Proxy-API-Management-Center/src/stores/useDashboardViewStore.ts`，仅修改 `loadDashboardView` 的 catch 与 finally。

**变更：**

- catch 分支：把 `if (!silent) update.loading = false` 改为 `update.loading = false`，无论是否 silent 都复位。
- catch 分支：保留 `throw error`（*.catch 调用方仍吃掉）。
- finally 分支：除了清空 `inFlight`，确保 `loading` 在最终失败态下也是 `false`。如果仍然 `loading=true`（仅当 block 中所有 set 都被 `requestToken` 短路），再补一次 `set({ loading: false, scopeKey })`。

**不变：**

- `useDashboardLiveRefresh.ts` 的 `.catch(() => {})` 不变（异常已在 store 内消化）。
- `DashboardPage.tsx` 不变。

### 3.2 后端 — 锁内只拷贝轻量切片 + ctx 取消

**`internal/usage/dashboard_snapshot.go`**：把 `BuildDashboardSnapshot` 拆为两段。

- 新签名：`func (s *RequestStatistics) BuildDashboardSnapshot(ctx context.Context, cfg DashboardConfig) DashboardSnapshot`
- 锁内阶段（`s.mu.RLock` 范围）：
  1. 拷贝 `s.totalRequests/successCount/failureCount/totalTokens/nextEventID`。
  2. 一次遍历 `apis[*].Models[*].Details`，构造 `[]dashboardDetail{timestamp, latencyMs, failed, tokens, apiName, modelName, statusCode}`，释放锁。
- 锁外阶段（不再持锁）：
  1. 流式分配 bucket。
  2. 累计 `byModel`，挑选 top-N。
  3. 维护 latest top-N（已有 7 槽）。
  4. 排序。
- 锁外阶段每 4096 条循环里检查一次 `ctx.Done()`，立即返回当前已计算结果（零值兜底）。这是 ctx 取消路径。

**`internal/api/handlers/management/usage.go`**：

- `GetUsageDashboard` 增加 `ctx, cancel := context.WithTimeout(c.Request.Context(), 10s); defer cancel()`。
- 调 `BuildDashboardSnapshot(ctx, cfg)`。
- 若 `ctx.Err() != nil` 且结果 `WindowRequests == 0` 且 `LatestRequests == nil`，返回 `503 {"error": "dashboard_build_cancelled"}`。

**`internal/api/server.go`**：路由注册不变。

### 3.3 同步点

- `BuildDashboardSnapshot` 签名变 → 现有调用方：
  - `internal/api/handlers/management/usage.go:298` 改为 `h.usageStats.BuildDashboardSnapshot(ctx, cfg)`。
  - `internal/api/handlers/management/usage_test.go:292` 同步测试调用。
- `internal/usage/dashboard_snapshot_test.go` 同步测试调用。

## 4. 测试

### 4.1 前端 — `useDashboardViewStore.test.ts`

新增 3 个用例：

1. `silent: true` 请求 4s 后超时，`loading` 必为 `false`，`error` 反映超时。
2. `silent: true` 请求被 supersede abort（用 `mockRejectedValueOnce(AbortError)`），`loading` 必为 `false`。
3. 连续三次 supersede（同一 scopeKey）→ 最终稳定态 `loading=false, scopeKey` 仍对应最后一次成功。

### 4.2 后端 — `internal/usage/dashboard_snapshot_test.go`

新增 2 个用例：

1. 高并发 ingest：50 个 goroutine 持续 `Record` 1000 条，主线程同时调 `BuildDashboardSnapshot` 30 次。断言：p95 延迟 < 200ms（CI 弱保证，本地检查）。
2. ctx 取消：传 `context.WithCancel` 立即 cancel，断言函数返回 < 50ms，`LatestRequests == nil` 或空。

### 4.3 后端 — `internal/api/handlers/management/usage_test.go`

现有 `TestGetUsageDashboard_RendersAggregatePayload` 同步更新为新签名。

新增 1 个用例：

- 注入一个会让 `BuildDashboardSnapshot` 阻塞的 stats（用 mocked context cancel），断言 handler 返回 503。

## 5. 验证

- 后端：`go test ./internal/usage ./internal/api/handlers/management` 全部通过。
- 前端：`cd Cli-Proxy-API-Management-Center && npm run test:run` 全部通过。
- 压测：本地 `go run ./cmd/server -config config.yaml` 启动，`secret-key` 占位开 `allow-remote: true`；用 `hey`/`xargs` 持续压测 `/v0/management/usage/dashboard`（同级 ingest 50 req/s），`p95 < 2s`，且 15s 超时数为 0。
- 浏览器 Console：`loading:true` 持续不超过 16s。

## 6. 风险与权衡

- 锁内拷贝切片本身仍 O(total records)，但只是轻量结构体拷贝，相对原版少 90% 时间（预测）。如发现仍慢，备份方案：是给 `models` 加 `latestTimestamp` 增量索引，丢弃过期 detail。
- 503 状态码前端未特殊处理，按现有 catch 路径正常转为 `error` 字符串。
- `config.yaml` 不动。

## 7. 文件清单

- `Cli-Proxy-API-Management-Center/src/stores/useDashboardViewStore.ts`
- `Cli-Proxy-API-Management-Center/src/stores/useDashboardViewStore.test.ts`
- `internal/usage/dashboard_snapshot.go`
- `internal/usage/dashboard_snapshot_test.go`
- `internal/api/handlers/management/usage.go`
- `internal/api/handlers/management/usage_test.go`
- `docs/superpowers/specs/2026-07-26-dashboard-loading-stuck-design.md`
