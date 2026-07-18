# Usage 事件明细实时刷新 v2(每条不丢 + 增量推送)

**日期**: 2026-07-18
**状态**: 已批准
**版本**: 2.0(对 `2026-07-12-usage-real-time-refresh-design.md` v1 的迭代)
**影响范围**:
- 后端:`internal/usage`(broker / logger_plugin / 事件 ID)、`internal/api/handlers/management/usage_events.go`
- 前端:`Cli-Proxy-API-Management-Center/src/stores/useUsageStatsStore.ts`、`services/api/usageStream.ts`、`components/usage/hooks/useUsageLiveRefresh.ts`
**不影响**:`assets/usage.html`(独立静态页,沿用 30s 轮询)

---

## 1. 背景与动机

v1 spec(`2026-07-12`)实现了"SSE 推整 snapshot + 800ms 去抖 + 10s 轮询兜底"。用户反馈:**新事件明细仍需数秒才出现在列表,体感"还是不够及时"**。

### v1 延迟链路实测拆解

```
请求进入 → Record() 写入
   ↓ [broker 800ms 去抖窗口]
SSE 推送 snapshot(仅 total_requests / total_tokens 两个整数)
   ↓ [客户端 ~50ms 解析]
前端收到 → 调用 loadUsageStats({ force: true })
   ↓ [HTTP GET /v0/management/usage ~100~300ms]
   ↓ [序列化全量 payload 数十 KB ~50~200ms]
列表 re-render
```

**累计中位数 ~1.0~1.5s,P95 常超过 3s**。延迟源:

| 段 | 耗时 | 可优化点 |
|---|---|---|
| broker 去抖 | ≤800ms | 取消去抖,严格不丢 |
| SSE payload 过小 | 0 | 改推单条明细 + 全量摘要 |
| 前端收到 snapshot 后再 GET 全量 | ~150~500ms | 直接用推送里的明细合并到 store,不再拉 |

### 目标

1. 任何后端 `Record` 写入后 **≤ 1s**(P95)浏览器看到对应明细。
2. **严格不丢**任何 Record 对应的事件(包括高并发场景)。
3. SSE 断开时降级 1s 轮询,恢复后无缝对齐(不重复、不丢)。
4. 不破坏 v1 已有的鉴权、心跳、heartbeat、`MAX_RENDERED_EVENTS`、visibility 暂停。

### 非目标

- 不改 `assets/usage.html`。
- 不引入 WebSocket;仍走 SSE + `fetch` 流(沿用 `usageStream.ts` 的现有实现)。
- 不改 `/v0/management/usage` HTTP 接口语义。

### 与 v1 的差异(决策依据)

| 维度 | v1 | v2(本文) |
|---|---|---|
| broker 去抖 | 800ms 合并窗口 | **移除**,严格不丢 |
| SSE payload | 整 snapshot(仅总数) | `summary` + `usage_event` 两类;`usage_event` 带单条明细 |
| 前端拿到推送后 | 调 `loadUsageStats()` 拉全量 | 直接 `applyIncrementalEvent` 合并,不再拉 |
| 事件 id | 无 | `atomic.Uint64` 单调递增,前端按 id 去重 |
| 重连补偿 | 无 | 服务端 ring buffer(256)按 `Last-Event-ID` 补齐 |
| 轮询兜底间隔 | 10s | **1s**(SSE 长时间不可用时仍尽量实时) |
| 订阅 channel 缓冲 | 1(去抖下足够) | 64(无去抖下避免慢消费者丢) |

---

## 2. 架构

```
                          ┌─────────── ring buffer (cap 256) ──────────┐
                          │  [id, detail, summary] 环形                 │
                          └────────────────────────────────────────────┘
                                          ▲
Request.Record() ─► nextEventID.Add(1) ─► │ 写 detail
   │
   ├─ broker.PublishUsageEvent(detail) ─► 立即扇出
   │     │
   │     ▼
   │  每个订阅者独立 buffered chan(64)
   │     │ (慢消费者 select-default 仅丢弃自己,不阻塞 publisher)
   │     ▼
   │  SSE handler
   │     │ event: usage_event\ndata: {id, ...}\n\n
   │     ▼ flush
   │  浏览器 fetch 流 → usageStream.ts
   │     │
   │     ▼ store.applyIncrementalEvent(detail)
   │  useUsageStatsStore.recentDetails 头部插入
   │     │
   │     ▼ re-render(RequestEventsDetailsCard)
   │  列表显示新明细
```

**SSE handler 初始化**(连接建立时):

1. 读 `Last-Event-ID` 请求头(若有)
2. 从 ring buffer 取 `id > lastEventId` 的子集,逐条 flush `usage_event`
3. flush 一次 `summary` 携带 `latest_event_id`
4. 切到 live channel 持续读

**重连/断开策略**(前端):

| 状态 | 行为 |
|---|---|
| `open` | 停轮询;收 `usage_event` 增量合并;收 `summary` 批量对齐(仅在重连后首次) |
| `error` / `closed` | 启 1s 轮询拉全量(`loadUsageStats`)作为兜底 |
| `connecting` | 保持上一状态,不重复挂载 |

---

## 3. 数据契约

### 3.1 SSE 端点

`GET /v0/management/usage/events`(沿用 v1)

| 项 | 值 |
|---|---|
| 鉴权 | `Authorization: Bearer <managementKey>`(与 v1 一致) |
| `Content-Type` | `text/event-stream` |
| `Cache-Control` | `no-cache` |
| `Last-Event-ID` 请求头 | 客户端记录的最大 `id`,服务端据此补偿 |

### 3.2 事件类型(SSE `event:` 字段)

| event | 触发时机 | data schema |
|---|---|---|
| `summary` | SSE 连接建立后立即发 1 次,以及重连补偿结束后发 1 次 | 见 §3.3 |
| `usage_event` | 每次 `Request.Record()` 完成 | 见 §3.4 |
| `heartbeat` | 每 15s | `{"ts":"<RFC3339>"}`(沿用 v1) |

### 3.3 `summary` payload

```json
{
  "total_requests": 12345,
  "total_tokens": 678901,
  "success_count": 12000,
  "failure_count": 345,
  "latest_event_id": 12344
}
```

### 3.4 `usage_event` payload

```json
{
  "id": 12345,
  "api_key": "key-xxxx",
  "model": "gpt-4o",
  "failed": false,
  "tokens": { "input": 100, "output": 200, "total": 300 },
  "requested_at": "2026-07-18T10:00:00Z",
  "duration_ms": 1234,
  "status_code": 200
}
```

字段命名遵循现有 `/v0/management/usage` 接口的 snake_case。

### 3.5 后端事件 ID

- `RequestStatistics` 持有 `atomic.Uint64 nextEventID`
- `Record()` 进入时 `id := s.nextEventID.Add(1) - 1`(单调递增,全局唯一)
- ID 仅用于去重与重连补偿;不暴露业务语义

### 3.6 Ring buffer(重连补偿)

- 容量 256,环形存储 `{id, detail}`
- `Record()` 写入尾部;满则覆盖最旧
- 仅在 SSE handler 初始化路径读取(`RecentSince(sinceID uint64) []Detail`)
- 不影响 `Record` 热路径(写 ring buffer 用 mutex,但操作 O(1))

### 3.7 订阅 channel 容量

- 64/订阅者
- 慢消费者:channel 满时 `select default` **仅丢弃该订阅者**的事件,记录 `dropped` 计数(仅日志,暂不暴露 metrics)

### 3.8 内存成本估算

| 项 | 单订阅者 | 100 订阅者 |
|---|---|---|
| 订阅 channel(64 × ~200B) | 12.8KB | 1.28MB |
| Ring buffer(256 × ~200B) | 51.2KB | 共享 |
| 进程总开销 | < 100KB | < 2MB |

可接受。

---

## 4. 后端改动

### 4.1 `internal/usage/broker.go`(重写)

```go
type Broker struct {
    mu      sync.Mutex
    clients map[*subscription]struct{}
}

type subscription struct {
    ch       chan UsageEvent  // 容量 64
    dropped  uint64           // 慢消费者丢弃计数(原子)
}

func NewBroker() *Broker
func (b *Broker) Subscribe() (<-chan UsageEvent, func())  // 返回 cancel
func (b *Broker) Publish(evt UsageEvent)                  // 立即扇出,无去抖
```

- 移除 v1 的 `debounce` / `pending timer` / `last` 字段
- `Publish` 不再等待,直接遍历 `clients`,慢消费者 `select default` 丢弃
- `dropped` 用 `atomic.AddUint64` 累加;`Stats()` 方法返回订阅者数 / 总丢弃数(便于测试断言)

### 4.2 `internal/usage/logger_plugin.go`

- 新增 `UsageEvent` 结构(等同 §3.4 JSON 字段)
- `RequestStatistics` 新增字段:
  - `nextEventID atomic.Uint64`
  - `recentMu sync.Mutex`
  - `recent [256]UsageEvent` + `recentHead uint64`(环形索引)
- `Record()` 末尾:
  1. `id := s.nextEventID.Add(1) - 1`
  2. 组装 `evt := UsageEvent{ID: id, ...}`,在 `s.mu` 内已完成所有聚合,可直接构造
  3. `recentMu.Lock(); recent[recentHead%256] = evt; recentHead++; recentMu.Unlock()`
  4. `broker.Publish(evt)`(无去抖)

### 4.3 `internal/usage/recent_buffer.go`(新文件)

提取 ring buffer 读写逻辑,便于单测:

```go
type RecentBuffer struct {
    mu    sync.Mutex
    buf   [256]UsageEvent
    head  uint64  // 已写入总数
}

func (r *RecentBuffer) Push(evt UsageEvent)
func (r *RecentBuffer) Since(sinceID uint64) []UsageEvent  // 返回 id > sinceID 的子集,按 id 升序
```

### 4.4 `internal/api/handlers/management/usage_events.go`

- 替换 v1 的 `snapshot` 事件为 `summary` + `usage_event`
- 连接初始化:
  1. 读 `Last-Event-ID` header(若有,解析为 uint64)
  2. `recomp := recentBuffer.Since(lastID)`;遍历 `recomp` 逐条 flush `usage_event`
  3. flush 一次 `summary`(包含 `latest_event_id = nextEventID - 1`)
  4. 切到 live channel
- 主循环保持 v1 结构,仅 `case payload, ok := <-ch` 改为 `case evt := <-ch` 并写 `usage_event`
- 800ms 去抖代码已在 `broker.go` §4.1 重写时移除

### 4.5 `internal/api/server.go`

- 不变(v1 注册的路由仍生效)

### 4.6 配置(可选,沿用 v1)

```yaml
usage:
  sse_enabled: true          # 默认 true
  recent_buffer_size: 256    # 默认 256;v1 未暴露此项
  subscriber_queue_size: 64  # 默认 64
```

不配置时使用默认值,向后兼容。

---

## 5. 前端改动

### 5.1 `src/stores/useUsageStatsStore.ts`

新增 state:

```ts
recentDetails: UsageDetail[]   // 按 id 降序,头部最新
maxRecent: 200                  // 上限(独立于 MAX_RENDERED_EVENTS=500)
lastEventId: number             // 已收到的最大 event id
```

新增 actions:

```ts
applyIncrementalEvent(detail: UsageDetail)
  // 1. if detail.id <= lastEventId → return (去重)
  // 2. lastEventId = detail.id
  // 3. recentDetails.unshift(detail); if length > maxRecent → pop tail
applyBulkEvents(events: UsageDetail[])
  // 重连补偿 / summary 后批量合并:同上去重,最后按 id 降序排序
resetRecent()
  // loadUsageStats 全量拉取后调用,清空 recentDetails,避免重复
```

保留 `setUsage(payload)` 与 `loadUsageStats` 不变。

### 5.2 `src/services/api/usageStream.ts`

`StreamEvent` 联合类型扩展:

```ts
export type StreamEvent =
  | { type: 'summary'; payload: { total_requests: number; total_tokens: number; success_count?: number; failure_count?: number; latest_event_id: number } }
  | { type: 'usage_event'; payload: UsageDetail }
  | { type: 'heartbeat'; ts: string };
```

新增 `subscribeUsageStream` 选项:

```ts
interface UsageStreamOptions {
  ...
  onUsageEvent?: (detail: UsageDetail) => void;  // 新增
  onSummary?: (summary: SummaryPayload) => void;  // 新增
  getLastEventId?: () => number;                  // 新增,用于 Last-Event-ID 头
}
```

`connect()` 中:

```ts
const headers: Record<string, string> = { Accept: 'text/event-stream' };
const lastId = opts.getLastEventId?.();
if (lastId && lastId > 0) headers['Last-Event-ID'] = String(lastId);
```

`parseBlock()` 中:

```ts
if (event === 'usage_event') opts.onUsageEvent?.(JSON.parse(data));
else if (event === 'summary') opts.onSummary?.(JSON.parse(data));
// 移除 v1 的 snapshot 分支
```

### 5.3 `src/components/usage/hooks/useUsageLiveRefresh.ts`

调整回调绑定:

```ts
const streamHandle = subscribeUsageStream({
  getManagementKey: () => useAuthStore.getState().managementKey ?? '',
  getLastEventId:  () => useUsageStatsStore.getState().lastEventId,
  onUsageEvent:    (d) => useUsageStatsStore.getState().applyIncrementalEvent(d),
  onSummary:       (s) => {
    // 重连后首次 summary:仅对齐 lastEventId;实际补偿事件已在 SSE 初始化阶段逐条推送
    useUsageStatsStore.setState({ lastEventId: s.latest_event_id });
  },
  onStatusChange: (status) => {
    if (status === 'open')  stopPolling();
    else if (status === 'error' || status === 'closed') startPolling();
  },
  baseDelayMs: 1_000,
  maxDelayMs: 30_000,
});
```

- **关键变更**:收到 `usage_event` **不再** 触发 `loadUsageStats`(避免覆盖增量)
- SSE 断开时 1s 轮询拉全量,`setUsage` 内调用 `resetRecent()` 清空增量

轮询间隔由 v1 的 10s 改为 1s;`USAGE_POLL_INTERVAL_MS` 常量同步调整。

### 5.4 不改动

- `useUsageData.ts`:仅暴露 `recentDetails` 给 UI(若 UI 已用 `rows.length` 显示总数,则无需改)
- `RequestEventsDetailsCard.tsx`:v1 的「总数 / 过滤命中数」显示保持
- `/v0/management/usage` HTTP 接口语义不变

---

## 6. 测试

### 6.1 后端(Go)

| 文件 | 覆盖 |
|---|---|
| `internal/usage/broker_test.go`(重写) | 同步 Publish 不丢;100 并发 publish × 10 subscriber 不丢;慢消费者 select-default 仅丢自己;Stats() 计数正确 |
| `internal/usage/recent_buffer_test.go`(新) | 容量 256 边界覆盖;Since 单调性;并发读写安全 |
| `internal/usage/logger_plugin_test.go`(扩展) | `Record` 路径 id 单调;Record→Publish 同步触发;ring buffer 写入 |
| `internal/api/handlers/management/usage_events_test.go`(扩展) | `Last-Event-ID` 重连补偿输出顺序;`summary` + `usage_event` schema;401 鉴权;ctx cancel 退出;heartbeat 15s |

### 6.2 前端(Vitest)

| 文件 | 覆盖 |
|---|---|
| `src/services/api/usageStream.test.ts`(扩展) | 解析 `usage_event` / `summary`;透传 `Last-Event-ID` header;重连退避 |
| `src/stores/useUsageStatsStore.test.ts`(扩展) | 增量去重(同 id 不重复);上限截尾(maxRecent);批量合并排序;`resetRecent` |
| `src/components/usage/hooks/useUsageLiveRefresh.test.ts`(扩展) | `usage_event` 不触发 `loadUsageStats`;`open` 停轮询;`closed` 启轮询;`Last-Event-ID` 绑定到 store |

### 6.3 手测脚本

1. 启服务 → 浏览器打开 Usage 页
2. DevTools Network 看到 `/v0/management/usage/events` 长连接
3. 触发 1 次 LLM 请求 → DevTools 看到 1 条 `usage_event` payload → 列表 ≤ 1s 显示新行
4. 并发脚本 100 req/s 持续 60s → 列表事件数与后端 `Record` 数差 < 1%;无前端报错
5. `kill` 后端 → 前端 1s 内切轮询;`loadUsageStats` 频率变为 1s;恢复后端 → SSE 自动重连,`Last-Event-ID` 补偿期间未推送的事件
6. 切到其他标签页 1min → 切回时不爆发请求(v1 visibility 暂停逻辑保留)
7. 切过滤维度 → 头部的「总数 / 过滤命中数」仍正确(v1 行为保留)

---

## 7. 验收标准(AC)

- [ ] **AC1**: 任何 `Record` 后 **P95 ≤ 1s** 浏览器列表显示对应明细行
- [ ] **AC2**: 100 req/s × 60s 并发压测下,前端可见事件数与后端 `Record` 数差异 < 1%
- [ ] **AC3**: SSE 断开 → 1s 内切轮询;恢复后 ≤ 2s 通过 `Last-Event-ID` 完成补偿
- [ ] **AC4**: SSE `open` 时停止轮询(仅一条活跃通道)
- [ ] **AC5**: 标签页隐藏时不发起 SSE 重连或轮询
- [ ] **AC6**: v1 的「总数 / 过滤命中数」头部显示、心跳 15s、auth 401 处理不受影响
- [ ] **AC7**: `assets/usage.html` 行为不变

---

## 8. 风险与权衡

| 风险 | 缓解 |
|---|---|
| 高频 Record 推送打爆 SSE 通道 | 订阅 channel 容量 64;慢消费者仅丢自己;1000 req/s 单连接仍可承受 |
| 慢消费者持续丢事件 | dropped 计数日志告警;未来可暴露 Prometheus |
| 重连期间 ring buffer 被覆盖(>256 条) | 256 条窗口 ≈ 256ms@1000req/s,足够覆盖典型重连耗时(1~5s 重连时 `Last-Event-ID` 之外的丢失由轮询兜底补齐) |
| `Last-Event-ID` 解析失败(非数字) | 服务端 fallback 到 0,正常推送 |
| atomic.Uint64 与 ring buffer mutex 顺序 | id 在 `Record()` 入口先取(无锁),构造 detail 在 `s.mu` 内;ring buffer 写在 `s.mu` 外但 `recentMu` 内;并发安全已论证 |
| 增量与全量不一致(loadUsageStats 后 recentDetails 残留) | `setUsage` / `loadUsageStats` 完成后调用 `resetRecent()` 清空 |

### 关键设计决策

1. **移除去抖而非压缩**:v1 用 800ms 合并高频请求;v2 严格不丢更符合用户对"明细实时"的预期。后端 CPU 开销可控。
2. **推明细而非整 snapshot**:v1 推整 snapshot 让前端再拉一次,是延迟主因。v2 直接推明细让前端立即合并。
3. **ring buffer 而非 DB 查询补偿**:避免重连补偿路径触 DB;容量 256 是延迟与内存的折中。
4. **轮询间隔 10s → 1s**:SSE 长时间不可用时仍尽量实时;`loadUsageStats` 内部已有 staleTime 逻辑,1s 不会触发额外开销。

### 兼容性

- `assets/usage.html` 不变。
- 既有 `/v0/management/usage` 接口不变。
- 既有 `useUsageStatsStore` 接口扩展(新增字段/actions),不删除任何字段。
- v1 spec 在 git 历史中保留;本文档通过 §1 的差异表明确替代关系。

---

## 9. 实施顺序(将由 writing-plans 展开)

1. `internal/usage/recent_buffer.go` + 单测
2. `internal/usage/broker.go` 重写 + 单测
3. `internal/usage/logger_plugin.go` 集成(id / ring buffer / PublishUsageEvent)
4. `internal/api/handlers/management/usage_events.go` 事件类型与重连补偿
5. 前端 `useUsageStatsStore` 增量模型 + 单测
6. 前端 `usageStream.ts` 扩展 + 单测
7. 前端 `useUsageLiveRefresh.ts` 切换 + 单测
8. 手测脚本 + AC 验证

每步独立可测、独立提交。
