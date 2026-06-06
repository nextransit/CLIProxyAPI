# Session-Affinity 加权轮询设计

**日期**: 2026-06-06
**作者**: Claude Code
**关联分支**: `personal/offline-dev`

## 背景

CLIProxyAPI 提供 `session-affinity` 机制：同一 chat 会话（通过 `user_id._session_{uuid}` 或 `X-Session-ID` header 识别）的所有请求会被 stick 到首次选中的 auth，直到 TTL 过期（默认 4h）或 auth 不可用。

这在客户端视角是好的——claude code 切换消息时不会因 auth 切换导致 conversation state 丢失。但配合加权轮询（weight=1:2 配置），会**让所有同 session 请求都路由到首次选中的 auth**：

- 首次请求走 weighted selector（1:2 概率选 auth A 或 auth B）
- 后续请求 cache hit → 始终用首次选的 auth
- 用户实际看到"总是 weight=2"（或 weight=1），看不到 1:2 轮询

诉求：**不破坏正常 session 行为**（同 session 连续多轮必须 sticky），同时**实现一定轮询**——同 session 内 20 轮请求后切换到 weighted selector 重新选。

## 设计

### 行为

| 场景 | 行为 |
|------|------|
| 新 session 首次请求 | 走 weighted selector 选 auth（按 weight 比例） |
| 同 session 第 2..N-1 轮 | 返回 cache 中的 auth，**严格 sticky** |
| 同 session 第 N 轮（达到 MaxRequests） | **忽略 cache**，走 weighted selector 重新选，count 重置为 1 |
| 当前 auth 不可用（rate limit/error/Disabled） | 走 weighted selector 重新选（已有行为，保留） |
| cache entry TTL 过期 | cache miss，走 weighted selector（已有行为，保留） |

### 默认值与配置

新增配置项：
```yaml
session-affinity: true
session-affinity-ttl: "4h"
session-affinity-max-requests: 20   # 新增
```

- `session-affinity-max-requests: 0` → 关闭计数功能（同当前行为，永远 sticky）
- 默认值 `20`（与用户确认的选择一致）

### 数据结构

`sessionEntry` 增加 `requestCount` 字段：

```go
// session_cache.go
type sessionEntry struct {
    authID       string
    expiresAt    time.Time
    requestCount int  // 新增: 该 session 命中本 cache 的请求计数
}
```

### 关键 API 改动

`SessionCache.GetAndRefresh` 签名扩展为返回 count：

```go
// Before:
func (c *SessionCache) GetAndRefresh(sessionID string) (string, bool)

// After:
func (c *SessionCache) GetAndRefresh(sessionID string) (authID string, count int, ok bool)
```

`SessionAffinityConfig` 增加 MaxRequests：

```go
// selector.go
type SessionAffinityConfig struct {
    Fallback    Selector
    TTL         time.Duration
    MaxRequests int  // 新增: 0 表示禁用计数
}
```

`Config` 增加 SessionAffinityMaxRequests：

```go
// config.go
type Config struct {
    // ... 现有字段
    SessionAffinityMaxRequests int `yaml:"session-affinity-max-requests,omitempty"`
}
```

### 选 auth 流程

```go
func (s *SessionAffinitySelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
    primaryID, fallbackID := extractSessionIDs(...)
    if primaryID == "" {
        return s.fallback.Pick(ctx, provider, model, opts, auths)
    }

    available, err := getAvailableAuths(auths, provider, model, now)
    // ... 现有 logic

    cacheKey := provider + "::" + primaryID + "::" + model
    if cachedAuthID, count, ok := s.cache.GetAndRefresh(cacheKey); ok {
        // 新增: 达到 MaxRequests 阈值则强制重新轮询
        if s.maxRequests > 0 && count >= s.maxRequests {
            // 不返回 cache；走 weighted selector 重新选
        } else {
            // 现有 sticky 行为
            for _, auth := range available {
                if auth.ID == cachedAuthID {
                    return auth, nil
                }
            }
            // Cached auth 不在 available 列表（被 rate limit/Disabled）→ 现有 fallback
        }
    }

    // fallback/first-pick：走 weighted selector
    auth, err := s.fallback.Pick(ctx, provider, model, opts, auths)
    if err != nil {
        return nil, err
    }
    s.cache.Set(cacheKey, auth.ID)  // Set 内部 count 置 1
    return auth, nil
}
```

### 错误处理

- **当前 auth 不可用**：保留 `InvalidateAuth` 机制，selector 走 fallback
- **fallback selector 报错**（无 auth 候选）：返回原错误（已有行为）
- **N 轮到了强制切**：`SessionCache.Set` 内部 count 置 1，下次请求重新从 1 开始
- **`SessionAffinityMaxRequests=0`**：立即 fall through，行为同关闭此功能
- **`SessionAffinityMaxRequests=1`**：每个请求都重新轮询（不 sticky）

### 边界情况

- **不同 model 同 session**：cache key 包含 model，所以不同 model 各自计数
- **新 auth 加入**（`Register`）：旧 session 的 cache 命中不存在的 auth → 走 fallback 重新选（已有行为）
- **fallback 报错**（如 `getAvailableAuths` 返回 `auth_unavailable`）：错误上抛，不写 cache
- **SessionID 解析失败**：fallback 到默认 selector（已有行为）

## 实施

### 文件改动

1. `internal/config/config.go`
   - `Config` 加 `SessionAffinityMaxRequests int` 字段，yaml tag `session-affinity-max-requests,omitempty`

2. `sdk/cliproxy/auth/session_cache.go`
   - `sessionEntry` 加 `requestCount int`
   - `GetAndRefresh` 返回签名改为 `(authID, count, ok)`
   - `Get` 保持原签名（不返回 count）
   - `Set` 不变（count 由 GetAndRefresh + Set 后的下一次 GetAndRefresh 隐式重置为 1）
   - **关键**：cache hit 命中时 count++ 并写回 entry；cache miss 时 entry 由 Set 重置为 0/1

3. `sdk/cliproxy/auth/selector.go`
   - `SessionAffinityConfig` 加 `MaxRequests int`
   - `SessionAffinitySelector` 加 `maxRequests int` 字段
   - `NewSessionAffinitySelectorWithConfig` 接受 MaxRequests
   - `Pick` 在 cache hit 时检查 count >= maxRequests

4. `sdk/cliproxy/builder.go` / `service.go`
   - 把 `cfg.SessionAffinityMaxRequests` 传给 `SessionAffinityConfig`

### 测试

新增 3 个测试到 `sdk/cliproxy/auth/`：

1. `TestSessionAffinity_StickyForNRequests`：
   - 构造 2 个 claude auth (weight=1, weight=2)
   - 同 session 连发 19 次 → 全是同 auth（sticky）
   - 第 20 次 → 走 weighted selector，可能选不同 auth
   - 重新 count=1，后续 19 次再次 sticky

2. `TestSessionAffinity_ResetsCountAfterRotation`：
   - 验证 cache hit 次数到 N 后切换，新 auth 重新从 count=1 开始

3. `TestSessionAffinity_ZeroMaxRequests_DisablesRotation`：
   - MaxRequests=0 时，永不触发轮询切换

更新现有测试：
- `TestSessionCache_GetAndRefresh`：验证 count 自增正确
- 任何调用 `GetAndRefresh` 的测试需要更新签名

### 部署

1. 实施 + 测试
2. 跑 go test 全部
3. 重 build 容器 binary
4. docker restart cli-proxy-api
5. 用户在管理 UI 验证：
   - 同 session 前 20 轮 stick 到同 auth
   - 第 20 轮后切到 weighted selector，可能换 auth
   - 修改 `session-affinity-max-requests` 配置可调

### 风险

- **会话上下文破坏**：claude code 切换消息时若 auth 不同，可能丢 conversation state。但只在 20 轮后切换，单次 chat 会话通常 < 20 轮，影响小
- **滚动更新**：cache entry 没有持久化（重启清空），重启后所有 session 重新走 weighted selector
- **权重突变**：weighted selector 用 `auth.Attributes["weight"]`；重启后所有 auth 重新注册权重一致，无问题
- **向后兼容**：`MaxRequests=0` 退化为原行为，老用户无感知

## 文档更新

- 同步更新 `config.yaml` 注释，加 `session-affinity-max-requests` 说明
- 同步更新 README（如果存在 session-affinity 文档）
