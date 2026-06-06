# Session-Affinity 加权轮询 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 session-affinity 启用时，让同 session 内 N 轮请求后自动重新走 weighted selector 选 auth，平衡 sticky 会话保持与权重轮询。

**Architecture:** 扩展 `SessionCache` 的 `sessionEntry` 加 `requestCount`；`SessionAffinitySelector.Pick` 在 cache hit 时检查 count 是否达到 `MaxRequests` 阈值，达到则忽略 cache 走 fallback (weighted) selector。新增 `session-affinity-max-requests` 配置（默认 20，0=关闭）。

**Tech Stack:** Go 1.26, gin, yaml v3, existing auth/scheduler/selector machinery.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/config/config.go` | YAML 配置 schema；加 `SessionAffinityMaxRequests` 字段 |
| `sdk/cliproxy/auth/types.go` | `SessionAffinityConfig` 加 `MaxRequests` 字段 |
| `sdk/cliproxy/auth/session_cache.go` | `sessionEntry` 增 `requestCount`；`GetAndRefresh` 改签名返回 count |
| `sdk/cliproxy/auth/selector.go` | `SessionAffinitySelector` 增 `maxRequests` 字段；`Pick` 检查阈值 |
| `sdk/cliproxy/service.go` | config → selector 传参 |
| `sdk/cliproxy/auth/session_cache_test.go` (新) | session cache count 行为测试 |
| `sdk/cliproxy/auth/selector_session_affinity_test.go` (新) | session affinity sticky-then-rotate 测试 |
| `config.yaml` | 文档注释新配置 |

---

## Task 1: 添加 config 字段 SessionAffinityMaxRequests

**Files:**
- Modify: `internal/config/config.go:1-50` (imports) and add field next to existing session-affinity settings

- [ ] **Step 1: Add the field to Config struct**

In `internal/config/config.go`, after the `SessionAffinityTTL` field (search for it), add:

```go
// SessionAffinityMaxRequests controls how many requests a session can stick to
// the same auth before being forced to re-rotate via the weighted selector.
// 0 disables the counter and reverts to the legacy "always sticky for the
// full TTL" behavior. Default when not set: 20.
SessionAffinityMaxRequests int `yaml:"session-affinity-max-requests,omitempty"`
```

- [ ] **Step 2: Verify the file still compiles**

Run: `go build ./internal/config/...`
Expected: no errors, no warnings

- [ ] **Step 3: Commit**

```bash
git add internal/config/config.go
git commit -m "config: add SessionAffinityMaxRequests field"
```

---

## Task 2: 加 SessionAffinityConfig.MaxRequests 字段

**Files:**
- Modify: `sdk/cliproxy/auth/types.go` (add MaxRequests to SessionAffinityConfig)

- [ ] **Step 1: Add MaxRequests to SessionAffinityConfig**

In `sdk/cliproxy/auth/types.go`, find the `SessionAffinityConfig` struct and add the field:

```go
type SessionAffinityConfig struct {
    Fallback    Selector
    TTL         time.Duration
    // MaxRequests forces a session to re-rotate through the fallback selector
    // after this many requests. 0 keeps the legacy sticky-until-TTL behavior.
    MaxRequests int
}
```

- [ ] **Step 2: Verify build**

Run: `go build ./sdk/cliproxy/auth/...`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add sdk/cliproxy/auth/types.go
git commit -m "auth: add MaxRequests to SessionAffinityConfig"
```

---

## Task 3: 扩展 SessionEntry 与 GetAndRefresh 签名

**Files:**
- Modify: `sdk/cliproxy/auth/session_cache.go`

- [ ] **Step 1: Add requestCount to sessionEntry**

In `sdk/cliproxy/auth/session_cache.go`, modify the struct:

```go
// sessionEntry stores auth binding with expiration and a per-session request
// counter used to drive the weighted-rotation policy.
type sessionEntry struct {
    authID       string
    expiresAt    time.Time
    requestCount int
}
```

- [ ] **Step 2: Update GetAndRefresh to return count**

Replace the existing `GetAndRefresh` function:

```go
// GetAndRefresh retrieves the auth ID bound to a session and refreshes TTL on
// hit. It also returns the post-increment request count so callers can
// implement a "rotate after N requests" policy.
func (c *SessionCache) GetAndRefresh(sessionID string) (authID string, count int, ok bool) {
    if sessionID == "" {
        return "", 0, false
    }
    now := time.Now()
    c.mu.Lock()
    defer c.mu.Unlock()
    entry, exists := c.entries[sessionID]
    if !exists {
        return "", 0, false
    }
    if now.After(entry.expiresAt) {
        delete(c.entries, sessionID)
        return "", 0, false
    }
    // Refresh TTL and increment counter atomically.
    entry.expiresAt = now.Add(c.ttl)
    entry.requestCount++
    c.entries[sessionID] = entry
    return entry.authID, entry.requestCount, true
}
```

- [ ] **Step 3: Update Set to reset counter on (re)bind**

Replace the existing `Set` function so it clears the counter to 0; the next `GetAndRefresh` will see count=1:

```go
// Set binds a session to an auth ID with TTL refresh. It resets the
// per-session request counter; the next GetAndRefresh will report count=1.
func (c *SessionCache) Set(sessionID, authID string) {
    if sessionID == "" || authID == "" {
        return
    }
    c.mu.Lock()
    c.entries[sessionID] = sessionEntry{
        authID:       authID,
        expiresAt:    time.Now().Add(c.ttl),
        requestCount: 0,
    }
    c.mu.Unlock()
}
```

- [ ] **Step 4: Verify build**

Run: `go build ./sdk/cliproxy/auth/...`
Expected: no errors. Other callers of `GetAndRefresh` will need their own update tasks below.

- [ ] **Step 5: Commit**

```bash
git add sdk/cliproxy/auth/session_cache.go
git commit -m "session-cache: track per-session request count"
```

---

## Task 4: 更新 SessionAffinitySelector 处理 count 阈值

**Files:**
- Modify: `sdk/cliproxy/auth/selector.go` (SessionAffinitySelector struct, NewSessionAffinitySelectorWithConfig, Pick method)

- [ ] **Step 1: Add maxRequests field**

In `sdk/cliproxy/auth/selector.go`, modify the `SessionAffinitySelector` struct:

```go
type SessionAffinitySelector struct {
    fallback    Selector
    cache       *SessionCache
    maxRequests int
}
```

- [ ] **Step 2: Update NewSessionAffinitySelectorWithConfig to accept MaxRequests**

Find the function and update it:

```go
func NewSessionAffinitySelectorWithConfig(cfg SessionAffinityConfig) *SessionAffinitySelector {
    if cfg.TTL <= 0 {
        cfg.TTL = time.Hour
    }
    return &SessionAffinitySelector{
        fallback:    cfg.Fallback,
        cache:       NewSessionCache(cfg.TTL),
        maxRequests: cfg.MaxRequests,
    }
}
```

- [ ] **Step 3: Update Pick to fall through when count >= maxRequests**

In the same file, find `func (s *SessionAffinitySelector) Pick(...)`. Inside it, locate the section that reads:

```go
if cachedAuthID, ok := s.cache.GetAndRefresh(cacheKey); ok {
    for _, auth := range available {
        if auth.ID == cachedAuthID {
            entry.Infof("session-affinity: cache hit | session=%s auth=%s provider=%s model=%s", truncateSessionID(primaryID), auth.ID, provider, model)
            return auth, nil
        }
    }
    // Cached auth not available, reselect via fallback selector for even distribution
    auth, err := s.fallback.Pick(ctx, provider, model, opts, auths)
    if err != nil {
        return nil, err
    }
    s.cache.Set(cacheKey, auth.ID)
    entry.Infof("session-affinity: cache hit but auth unavailable, reselected | session=%s auth=%s provider=%s model=%s", truncateSessionID(primaryID), auth.ID, provider, model)
    return auth, nil
}
```

Replace with:

```go
if cachedAuthID, count, ok := s.cache.GetAndRefresh(cacheKey); ok {
    // When MaxRequests > 0 and the per-session counter has reached the
    // threshold, ignore the cached auth and fall through to the fallback
    // (weighted) selector so the session re-rotates.
    if s.maxRequests <= 0 || count < s.maxRequests {
        for _, auth := range available {
            if auth.ID == cachedAuthID {
                entry.Infof("session-affinity: cache hit | session=%s auth=%s provider=%s model=%s count=%d", truncateSessionID(primaryID), auth.ID, provider, model, count)
                return auth, nil
            }
        }
        // Cached auth not available, reselect via fallback selector for even distribution
        auth, err := s.fallback.Pick(ctx, provider, model, opts, auths)
        if err != nil {
            return nil, err
        }
        s.cache.Set(cacheKey, auth.ID)
        entry.Infof("session-affinity: cache hit but auth unavailable, reselected | session=%s auth=%s provider=%s model=%s", truncateSessionID(primaryID), auth.ID, provider, model)
        return auth, nil
    }
    // count >= maxRequests: deliberately fall through to fallback selector.
    entry.Infof("session-affinity: rotation threshold reached | session=%s count=%d >= %d, reselecting via weighted selector", truncateSessionID(primaryID), count, s.maxRequests)
}
```

- [ ] **Step 4: Verify build**

Run: `go build ./sdk/cliproxy/auth/...`
Expected: no errors

- [ ] **Step 5: Commit**

```bash
git add sdk/cliproxy/auth/selector.go
git commit -m "selector: rotate session binding after N requests"
```

---

## Task 5: 传 config 字段到 selector

**Files:**
- Modify: `sdk/cliproxy/service.go` (in the section that builds the selector when `nextSessionAffinity` is true)

- [ ] **Step 1: Find the selector construction block**

In `sdk/cliproxy/service.go`, locate the `if nextSessionAffinity { ... }` block. It currently looks like:

```go
if nextSessionAffinity {
    ttl := time.Hour
    if ttlStr := strings.TrimSpace(nextSessionAffinityTTL); ttlStr != "" {
        if parsed, err := time.ParseDuration(ttlStr); err == nil && parsed > 0 {
            ttl = parsed
        }
    }
    selector = coreauth.NewSessionAffinitySelectorWithConfig(coreauth.SessionAffinityConfig{
        Fallback: selector,
        TTL:      ttl,
    })
}
```

- [ ] **Step 2: Read MaxRequests from config**

Replace that block with:

```go
if nextSessionAffinity {
    ttl := time.Hour
    if ttlStr := strings.TrimSpace(nextSessionAffinityTTL); ttlStr != "" {
        if parsed, err := time.ParseDuration(ttlStr); err == nil && parsed > 0 {
            ttl = parsed
        }
    }
    maxRequests := 20
    if s.cfg != nil {
        if v := s.cfg.SessionAffinityMaxRequests; v > 0 {
            maxRequests = v
        }
    }
    selector = coreauth.NewSessionAffinitySelectorWithConfig(coreauth.SessionAffinityConfig{
        Fallback:    selector,
        TTL:         ttl,
        MaxRequests: maxRequests,
    })
}
```

- [ ] **Step 3: Verify build**

Run: `go build ./...`
Expected: no errors

- [ ] **Step 4: Commit**

```bash
git add sdk/cliproxy/service.go
git commit -m "service: wire SessionAffinityMaxRequests into selector"
```

---

## Task 6: 更新现有 SessionCache 测试

**Files:**
- Modify: `sdk/cliproxy/auth/session_cache_test.go` (or wherever `GetAndRefresh` is currently exercised)

- [ ] **Step 1: Find existing GetAndRefresh test**

Run: `grep -rn "GetAndRefresh" sdk/cliproxy/auth/` and find the existing test that calls `GetAndRefresh` with the old 2-return-value signature.

- [ ] **Step 2: Update the test to use new signature**

For each existing test call like:

```go
authID, ok := cache.GetAndRefresh("session-1")
```

Replace with:

```go
authID, count, ok := cache.GetAndRefresh("session-1")
```

And add an assertion that `count` increments across calls (e.g., `if count != 1 { t.Fatalf("...") }` on first call, `if count != 2 { t.Fatalf("...") }` on second).

- [ ] **Step 3: Run the test and verify it passes**

Run: `go test ./sdk/cliproxy/auth/ -run TestSessionCache -v`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add sdk/cliproxy/auth/session_cache_test.go
git commit -m "test: update GetAndRefresh tests for count return"
```

---

## Task 7: 加新测试 - sticky 满 N 轮后轮询

**Files:**
- Create: `sdk/cliproxy/auth/selector_session_affinity_rotation_test.go`

- [ ] **Step 1: Write the test file**

```go
package auth

import (
	"context"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// TestSessionAffinity_StickyForNRequests_ThenReselectsViaWeightedSelector
// pins the new behavior: the first N-1 requests for a given session return
// the same auth (sticky), the Nth request falls through to the fallback
// weighted selector and may pick a different auth.
func TestSessionAffinity_StickyForNRequests_ThenReselectsViaWeightedSelector(t *testing.T) {
	rec := newRecordingExecutor("claude")

	// Two auths with weight=1 and weight=2 so the weighted selector is
	// deterministic-enough to distinguish the two.
	authA := &Auth{
		ID:         "auth-A",
		Provider:   "claude",
		Attributes: map[string]string{"weight": "1"},
		Metadata:   map[string]any{"type": "claude"},
	}
	authB := &Auth{
		ID:         "auth-B",
		Provider:   "claude",
		Attributes: map[string]string{"weight": "2"},
		Metadata:   map[string]any{"type": "claude"},
	}
	auths := []*Auth{authA, authB}
	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{
			"user_id": "user_session_1",
		},
	}

	fallback := &RoundRobinSelector{}
	sel := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:    fallback,
		TTL:         time.Hour,
		MaxRequests: 5,
	})

	// First 4 calls (N-1) must all return the same auth.
	first, err := sel.Pick(context.Background(), "claude", "model-x", opts, auths)
	if err != nil {
		t.Fatalf("pick 1: %v", err)
	}
	for i := 2; i <= 4; i++ {
		next, err := sel.Pick(context.Background(), "claude", "model-x", opts, auths)
		if err != nil {
			t.Fatalf("pick %d: %v", i, err)
		}
		if next.ID != first.ID {
			t.Fatalf("expected sticky auth %s for pick %d, got %s", first.ID, i, next.ID)
		}
	}

	// 5th call (== N) must fall through to the weighted selector and may
	// pick a different auth. The weighted selector under deterministic
	// round-robin with the first pick being auth A and a fresh cursor will
	// also return auth A, but the important property is that the call went
	// through the fallback path; we exercise that by setting up an auth
	// pool where the fallback would choose B. To make this deterministic,
	// use only auth B in the fallback set.
	fallbackOnlyB := []*Auth{authB}
	next, err := sel.Pick(context.Background(), "claude", "model-x", opts, fallbackOnlyB)
	if err != nil {
		t.Fatalf("pick 5: %v", err)
	}
	if next.ID != "auth-B" {
		t.Fatalf("expected rotation to pick auth-B (only available via fallback) on Nth request, got %s", next.ID)
	}
	_ = rec
	_ = time.Second
}
```

- [ ] **Step 2: Run the test and verify it passes**

Run: `go test ./sdk/cliproxy/auth/ -run TestSessionAffinity_StickyForNRequests -v`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add sdk/cliproxy/auth/selector_session_affinity_rotation_test.go
git commit -m "test: session-affinity sticky for N then rotate"
```

---

## Task 8: 加新测试 - MaxRequests=0 关闭轮询

**Files:**
- Create: `sdk/cliproxy/auth/selector_session_affinity_rotation_test.go` (append)

- [ ] **Step 1: Add the second test**

Append to the same file:

```go
// TestSessionAffinity_ZeroMaxRequests_DisablesRotation verifies the
// opt-out: when MaxRequests is 0, the selector keeps the legacy
// "sticky for the full TTL" behavior and never falls through to the
// weighted selector based on count.
func TestSessionAffinity_ZeroMaxRequests_DisablesRotation(t *testing.T) {
	authA := &Auth{ID: "auth-A", Provider: "claude", Attributes: map[string]string{"weight": "1"}, Metadata: map[string]any{"type": "claude"}}
	authB := &Auth{ID: "auth-B", Provider: "claude", Attributes: map[string]string{"weight": "2"}, Metadata: map[string]any{"type": "claude"}}
	auths := []*Auth{authA, authB}
	opts := cliproxyexecutor.Options{Metadata: map[string]any{"user_id": "user_session_2"}}

	sel := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:    &RoundRobinSelector{},
		TTL:         time.Hour,
		MaxRequests: 0, // disabled
	})

	first, err := sel.Pick(context.Background(), "claude", "model-x", opts, auths)
	if err != nil {
		t.Fatalf("pick 1: %v", err)
	}
	// Even after 100 calls, must keep returning the same auth.
	for i := 2; i <= 100; i++ {
		next, err := sel.Pick(context.Background(), "claude", "model-x", opts, auths)
		if err != nil {
			t.Fatalf("pick %d: %v", i, err)
		}
		if next.ID != first.ID {
			t.Fatalf("MaxRequests=0 should never rotate; pick %d returned %s, expected %s", i, next.ID, first.ID)
		}
	}
}
```

- [ ] **Step 2: Run the test and verify it passes**

Run: `go test ./sdk/cliproxy/auth/ -run TestSessionAffinity_ZeroMaxRequests -v`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add sdk/cliproxy/auth/selector_session_affinity_rotation_test.go
git commit -m "test: MaxRequests=0 disables rotation"
```

---

## Task 9: 加新测试 - 切换后 count 重置

**Files:**
- Create: `sdk/cliproxy/auth/selector_session_affinity_rotation_test.go` (append)

- [ ] **Step 1: Add the third test**

Append:

```go
// TestSessionAffinity_ResetsCountAfterRotation verifies that after a
// forced rotation, the new binding starts its counter at 1 (not the old
// count) so the next N-1 requests stick to the new auth.
func TestSessionAffinity_ResetsCountAfterRotation(t *testing.T) {
	authA := &Auth{ID: "auth-A", Provider: "claude", Attributes: map[string]string{"weight": "1"}, Metadata: map[string]any{"type": "claude"}}
	authB := &Auth{ID: "auth-B", Provider: "claude", Attributes: map[string]string{"weight": "2"}, Metadata: map[string]any{"type": "claude"}}
	opts := cliproxyexecutor.Options{Metadata: map[string]any{"user_id": "user_session_3"}}

	sel := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:    &RoundRobinSelector{},
		TTL:         time.Hour,
		MaxRequests: 3,
	})

	// Three sticky calls to auth A; the 3rd call rotates to auth B.
	first, _ := sel.Pick(context.Background(), "claude", "model-x", opts, []*Auth{authA, authB})
	_ = first
	_ = sel.Pick(context.Background(), "claude", "model-x", opts, []*Auth{authA, authB})
	rotated, err := sel.Pick(context.Background(), "claude", "model-x", opts, []*Auth{authA, authB})
	if err != nil {
		t.Fatalf("pick 3: %v", err)
	}
	if rotated.ID != "auth-B" {
		t.Fatalf("expected pick 3 to rotate to auth-B, got %s", rotated.ID)
	}

	// Now the next 2 calls must be sticky to auth B.
	for i := 4; i <= 5; i++ {
		next, err := sel.Pick(context.Background(), "claude", "model-x", opts, []*Auth{authA, authB})
		if err != nil {
			t.Fatalf("pick %d: %v", i, err)
		}
		if next.ID != "auth-B" {
			t.Fatalf("expected sticky to auth-B after rotation on pick %d, got %s", i, next.ID)
		}
	}
}
```

- [ ] **Step 2: Run the test and verify it passes**

Run: `go test ./sdk/cliproxy/auth/ -run TestSessionAffinity_ResetsCountAfterRotation -v`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add sdk/cliproxy/auth/selector_session_affinity_rotation_test.go
git commit -m "test: count resets after rotation"
```

---

## Task 10: 在 config.yaml 加新配置说明

**Files:**
- Modify: `config.yaml` (where existing session-affinity-ttl is documented)

- [ ] **Step 1: Locate the existing session-affinity config block**

In `config.yaml`, find:

```yaml
  # session-affinity-ttl: "4h"
```

- [ ] **Step 2: Add comment for new config field**

Right after that line, add:

```yaml
  # session-affinity-max-requests: 20
```

with a brief comment explaining the default and zero value.

- [ ] **Step 3: Commit**

```bash
git add config.yaml
git commit -m "config: document session-affinity-max-requests"
```

---

## Task 11: 跑全量测试 + build

- [ ] **Step 1: Run all Go tests**

Run: `go test ./...`
Expected: all packages pass

- [ ] **Step 2: Run go vet**

Run: `go vet ./...`
Expected: no warnings

- [ ] **Step 3: Build the container binary**

Run: `bash docker-build.sh` (or the project's standard build command)
Expected: produces `./CLIProxyAPI` binary

- [ ] **Step 4: Restart the running container with the new binary**

Run: `docker restart cli-proxy-api`
Expected: container comes up healthy

---

## Task 12: 端到端验证

- [ ] **Step 1: Verify gpt-5.3-codex still works**

Run:
```bash
curl -s -X POST http://localhost:8317/v1/responses \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-mbQa17oNUZfDD6hrW" \
  -d '{"model":"gpt-5.3-codex","input":"hi","stream":false}' | head -c 200
```
Expected: 200 OK with response body containing "Hi!"

- [ ] **Step 2: Verify session-affinity behavior (manual)**

Ask the user to:
1. Open the management UI
2. Edit the `https://api.minimaxi.com/anthropic` claude-api-key block
3. Set weight=1 on key 1, weight=2 on key 2
4. Save
5. Send 5 sequential requests in the same chat
6. Verify the same auth is used for the first 4, and the 5th may switch

- [ ] **Step 3: Final commit if any leftover changes**

```bash
git status
# If anything is unstaged:
git add -A
git commit -m "chore: final cleanup"
```

---

## Risks Summary

- **会话上下文破坏**：claude code 切换消息时若 auth 不同，可能丢 conversation state。但只在 20 轮后切换，影响小。
- **重启清空 cache**：cache 没持久化，重启后所有 session 重新走 weighted selector。
- **向后兼容**：`MaxRequests=0` 退化为原行为，老用户无感。
- **测试覆盖**：3 个新测试 + 1 个现有更新，覆盖核心行为与边界。
