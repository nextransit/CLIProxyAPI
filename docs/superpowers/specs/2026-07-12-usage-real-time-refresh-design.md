# Usage 统计页面实时刷新 + 过滤总条数

**日期**: 2026-07-12
**状态**: 已批准
**版本**: 1.0
**影响范围**: `Cli-Proxy-API-Management-Center` 前端 + `internal/usage` + `internal/api` 后端
**不影响**: `assets/usage.html`（独立静态页）

---

## 1. 概述

### 背景

当前 Management Center 的 Usage 页（`UsagePage.tsx`）通过前端 `setInterval` 每 **30 秒** 拉取一次 `/v0/management/usage`，用户在关注实时流量时感知延迟较高。

同时 `RequestEventsDetailsCard` 的过滤面板（来源 / 认证索引 / 模型 / 搜索词）已支持按维度过滤，但面板头部只显示「过滤后条数」，用户无法直观知道「在不应用过滤时一共多少事件」。

### 目标

1. 任何后端 `Record` 写入后 **≤ 2 秒** 内浏览器看到新数据。
2. 事件明细面板头部始终同时显示「总数」与「当前过滤命中数」两个数字。
3. 在 SSE 不可用时自动降级为 10 秒轮询，保证可用性。
4. 不破坏现有 visibility-hidden 暂停、`MAX_RENDERED_EVENTS = 500` 限制。

### 非目标

- 不改 `assets/usage.html`（独立静态页）的 30s 轮询逻辑。
- 不引入 WebSocket；统一走浏览器原生 EventSource。
- 不做服务端增量 diff 推送（推送整 snapshot；后端开销 < 1ms）。

---

## 2. 架构

```
┌─────────────────┐  SSE /events   ┌──────────────────┐
│  Management UI  │ ◀────────────  │  Go HTTP server  │
│  (EventSource)  │                │  + broker        │
└────────┬────────┘                └────────┬─────────┘
         │ fallback polling 10s             │
         │ GET /v0/management/usage         ▼
         │                  ┌──────────────────────────┐
         └──────────────────┤ useUsageStatsStore (zust) │
                            └──────────────────────────┘
```

触发链路：

1. `RequestStatistics.Record()` 写入统计
2. `broker.Publish(snapshot)`（800ms 去抖）
3. 所有订阅 SSE 连接 flush 一次最新 snapshot
4. 前端 `useUsageStatsStore` 更新 → React re-render
5. `RequestEventsDetailsCard` 的 `rows` / `filteredRows` 重算
6. 头部的「总数 / 过滤后」自动更新

---

## 3. 数据契约

### 3.1 SSE 端点

`GET /v0/management/usage/events`

| 项 | 值 |
| --- | --- |
| 鉴权 | `Authorization: Bearer <managementKey>`（与现有 `/v0/management/usage` 一致） |
| 响应 Content-Type | `text/event-stream` |
| Cache-Control | `no-cache` |
| 连接保持 | 无限（直到客户端断开或服务端关闭） |

事件类型：

| event | data 形态 | 触发时机 |
| --- | --- | --- |
| `snapshot` | 完整 usage JSON（与 `GET /usage` 相同 schema） | 任何 stats 变化且过 800ms 去抖窗口 |
| `heartbeat` | `{"ts":"<RFC3339>"}` | 每 15 秒 1 次，防止中间代理超时 |

客户端断开时，服务端从 broker 注销订阅并释放 channel。

### 3.2 轮询降级契约

- 前端 EventSource `onerror` → 切 `polling` 模式：启动 `setInterval(loadUsageStats, 10_000)`
- 连续 3 次 `onopen` 成功 → 关闭 polling timer，回到 SSE
- `document.visibilityState === 'hidden'` 时 SSE 与 polling 都暂停
- 后端不感知前端模式，由前端协调

### 3.3 频率与去抖

| 项 | 值 | 说明 |
| --- | --- | --- |
| 后端 Publish 去抖窗口 | 800 ms | 高并发场景合并突发 Record；可由配置覆盖 |
| 心跳间隔 | 15 s | 防中间代理 idle timeout |
| Polling 降级间隔 | 10 s | 比 SSE 略慢以省流量 |
| 前端可见性暂停 | 立即 | `document.hidden` 时不发起请求 |

---

## 4. 前端改动

### 4.1 新文件 `src/services/api/usageStream.ts`

封装 EventSource：
- `subscribeUsageStream({ onSnapshot, onError, onOpen }): () => void`
- 内部处理重连退避：1s, 2s, 4s, max 30s
- 单例：同一时间只维护一个 EventSource

### 4.2 修改 `src/components/usage/hooks/useUsageData.ts`

- 新增状态：`connectionMode: 'sse' | 'polling' | 'idle'`
- 启动 SSE 订阅；若进入 polling，挂 setInterval
- 保留原有 `AUTO_REFRESH_INTERVAL_MS` 改为 **fallback**（10_000ms）
- 删除原 line 86-97 的 30s 自动轮询（被 SSE + fallback 取代）
- 暴露 `connectionMode` 给 UI 展示（可选 badge）

### 4.3 修改 `src/components/usage/RequestEventsDetailsCard.tsx`

- 在 `requestEventsMeta` 区域（line 905-916）显示「总数 / 过滤后」
- 文案：`共 {total} 条 · 当前过滤命中 {filtered} 条`
- 语义锁定：
  - `total` = `rows.length`（明细面板中所有 detail 事件条数，不含 API 总请求统计）
  - `filtered` = `filteredRows.length`（应用过滤 + 搜索词后命中条数）
- 当 `filtered > MAX_RENDERED_EVENTS` 时，原有 `request_events_limit_hint` 文案追加在两数之后（不替换）
- 新增 i18n key：
  - `usage_stats.request_events_total_count`
  - `usage_stats.request_events_filtered_count`
  - `usage_stats.request_events_meta_summary`：`{total} / {filtered}`
- `MAX_RENDERED_EVENTS` 超出提示逻辑保留不变

### 4.4 不改动

- `src/services/api/usage.ts`（HTTP 调用保持原样）
- `useUsageStatsStore`（Zustand store）：SSE handler 直接调用 `setUsage(snapshot)`
- 过滤逻辑（line 561-599）：语义不变，仅在头部追加「总数」显示

---

## 5. 后端改动

### 5.1 新文件 `internal/usage/broker.go`

```go
type Broker struct {
    mu      sync.RWMutex
    clients map[chan UsagePayload]struct{}
}

func NewBroker() *Broker
func (b *Broker) Subscribe() (<-chan UsagePayload, func())  // 返回 cancel
func (b *Broker) Publish(snapshot UsagePayload)              // 去抖 800ms
```

- 每个订阅获得独立 buffered channel（容量 1）
- `Publish` 用 singleflight / timer 合并突发写
- 慢消费者处理：channel 满时丢弃旧值而非阻塞 publisher
- 可选：导出 metrics（订阅者数、丢弃率）供后续监控

### 5.2 修改 `internal/usage/logger_plugin.go`

- `Record()` 末尾调 `broker.Publish(currentSnapshot)`（浅拷贝 snapshot）
- broker 通过 `RequestStatistics` 持有，或注册到 `GetRequestStatistics()`

### 5.3 新文件 `internal/api/handlers/usage_events.go`

- `GET /v0/management/usage/events`
- 复用现有 management 鉴权中间件
- `w.Header().Set("Content-Type","text/event-stream")`
- `w.Header().Set("Cache-Control","no-cache")`
- `flusher, _ := w.(http.Flusher)`
- 主循环：
  ```go
  ch, cancel := broker.Subscribe()
  defer cancel()
  ticker := time.NewTicker(15 * time.Second)
  defer ticker.Stop()
  for {
      select {
      case <-r.Context().Done():
          return
      case s := <-ch:
          fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", marshal(s))
          flusher.Flush()
      case <-ticker.C:
          fmt.Fprintf(w, "event: heartbeat\ndata: {\"ts\":%q}\n\n", time.Now().Format(time.RFC3339))
          flusher.Flush()
      }
  }
  ```

### 5.4 修改 `internal/api/server.go`

注册路由：
```go
r.Get("/v0/management/usage/events", handlers.UsageEventsHandler)
```
（沿用现有 management 鉴权中间件组）

### 5.5 配置项（`config.yaml` 可选）

```yaml
usage:
  sse_enabled: true        # 默认 true；false 时前端不会尝试 SSE，仅轮询
  sse_debounce_ms: 800     # 默认 800；可调小提升实时性，加大省 CPU
```

不配置时使用默认值，向后兼容。

---

## 6. 测试与验证

### 6.1 单元测试

| 文件 | 覆盖 |
| --- | --- |
| `internal/usage/broker_test.go` | Subscribe/Unsubscribe 配对；去抖窗口合并；慢消费者不阻塞 publisher；并发 Publish 安全 |
| `internal/api/handlers/usage_events_test.go` | httptest：验证 SSE 输出格式；鉴权失败 401；context cancel 立即退出；heartbeat 15s 触发 |

### 6.2 手测脚本

1. 启服务 → 浏览器打开 Usage 页
2. DevTools Network 看到 `EventSource /events`
3. 触发一次 LLM 请求 → 列表数字 ≤ 2s 内变化
4. `kill` 后端进程 → 5s 内前端 badge 切到「轮询模式」；恢复后端 → 自动重连 SSE
5. 切到其他标签页 1min → 切回时不爆发请求（visibility 暂停生效）
6. 在事件明细面板切换过滤 → 头部的「总数 / 过滤命中数」同时正确显示

### 6.3 验收标准

- [ ] **AC1**: 任何 `Record` 后 ≤ 2s 内浏览器看到新数据
- [ ] **AC2**: 服务中断下页面仍能 ≤ 10s 内更新（轮询兜底）
- [ ] **AC3**: 详情面板头始终显示「总数 / 过滤命中数」两个数字
- [ ] **AC4**: 标签页隐藏时不发起请求
- [ ] **AC5**: 既有过滤、导出 CSV/JSON、Trace Drawer、Toast 功能不受影响

---

## 7. 风险与权衡

### 7.1 已知风险

| 风险 | 缓解 |
| --- | --- |
| 中间代理（Nginx/CDN）切断 SSE | 每 15s heartbeat；客户端 1s/2s/4s/max 30s 退避重连 |
| 高频 Record 推爆 SSE 通道 | 后端 800ms 去抖；慢消费者 channel 容量 1，新值覆盖旧值 |
| 标签页隐藏后回到前台爆发重连 | visibility 暂停 SSE 重连；恢复时仅触发一次 `loadUsage` 校对 |
| 管理密钥泄漏放大推送面 | 鉴权复用现有中间件；SSE 端点不暴露增量 diff，仅 snapshot |

### 7.2 关键设计决策

1. **去抖 800ms**：token 级高频突发下，800ms 内合并为 1 次推送。若需更实时可降到 200ms，但后端 snapshot 拷贝开销线性增加。
2. **后端推整 snapshot**：避免设计增量 diff 协议；snapshot 是已聚合数据，浅拷贝 < 1ms。
3. **不引入 WebSocket**：浏览器原生 EventSource 足够，省掉握手鉴权复杂度。
4. **轮询降级而非纯 SSE**：增强在弱网络环境下的可用性。

### 7.3 兼容性

- `assets/usage.html` 不变：继续 30s 轮询。
- 既有 `/v0/management/usage` 接口不变：仍可被任何外部脚本使用。
- 既有 `useUsageStatsStore` 接口不变：仅增加新事件订阅方式。