# API Key Policy Controls Design

**Date:** 2026-04-01

**Status:** Approved

**Goal:** Add structured API key policies for model whitelist control, traffic control, token quotas, and concurrency control, with matching management UI support.

## Summary

The current top-level `api-keys` configuration is a plain string list used only for client authentication. This design upgrades `api-keys` to support mixed `string | object` entries so existing deployments remain valid while new deployments can attach per-key policy rules.

The new policy system will support:

- Model whitelist control per API key
- Wildcard model matching
- Super API keys that bypass all policy restrictions
- RPM and QPS/burst traffic limits
- Concurrency limits with per-key waiting queues
- Lifetime token quotas
- Periodic token quotas with `day` and `month` windows
- Runtime inspection and token counter reset through management APIs

## Design Goals

- Preserve backward compatibility for existing `api-keys: ["k1", "k2"]` deployments
- Enforce API key policies in one shared runtime path for all supported request handlers
- Avoid scattering policy logic across provider executors
- Keep management UX aligned with actual runtime semantics
- Prevent the visual YAML editor from destroying structured API key policy data

## Non-Goals

- Replacing the top-level `api-keys` configuration with `auth.providers`
- Adding blacklist-based model control
- Adding weekly or custom cron-like quota reset windows in the first version
- Persisting transient queue or in-flight concurrency state across process restarts

## Configuration Model

### Backward-Compatible Shape

`api-keys` will accept mixed string and object entries.

```yaml
api-keys:
  - "legacy-key-1"

  - key: "team-a-key"
    name: "Team A"
    description: "shared internal client key"
    super: false
    models:
      - "gpt-4o*"
      - "claude-3-7-sonnet*"
      - "gemini-2.5-*"
    limits:
      rate:
        rpm: 120
        qps: 5
        burst: 10
      concurrency:
        max: 3
        queue-max: 50
        queue-timeout-ms: 15000
      tokens:
        lifetime:
          limit: 100000000
        periodic:
          limit: 5000000
          window: "day"
```

### Effective Semantics

- String entry: treated as a basic non-super key with no extra restrictions
- `super: true`: bypasses model, rate, concurrency, and token quota restrictions
- `models`: whitelist only; supports wildcard patterns
- Empty `models`: means unrestricted model access for compatibility
- `limits.rate.rpm`: rolling request count cap
- `limits.rate.qps` and `limits.rate.burst`: token-bucket style rate limit
- `limits.concurrency.max`: max in-flight requests for the key
- `limits.concurrency.queue-max`: max waiting requests for the key
- `limits.concurrency.queue-timeout-ms`: max wait time before queue timeout
- `limits.tokens.lifetime.limit`: cumulative token cap until reset
- `limits.tokens.periodic.limit`: periodic token cap
- `limits.tokens.periodic.window`: `day` or `month`

## Backend Architecture

### New Internal Runtime Component

Add a shared runtime component, tentatively `APIKeyPolicyManager`, responsible for:

- Resolving the authenticated API key to its normalized policy entry
- Checking model access
- Checking and updating rate limits
- Managing per-key concurrency queues
- Reading and updating token usage counters
- Exposing runtime state for management APIs

### Enforcement Point

Policy enforcement will happen after request authentication succeeds and before the request is executed against upstream providers.

Recommended integration point:

- `internal/api/server.go` `AuthMiddleware` keeps authenticating the client key
- `sdk/api/handlers/handlers.go` `BaseAPIHandler.ExecuteWithAuthManager`
- `sdk/api/handlers/handlers.go` `BaseAPIHandler.ExecuteCountWithAuthManager`
- `sdk/api/handlers/handlers.go` `BaseAPIHandler.ExecuteStreamWithAuthManager`

This keeps the policy system shared by OpenAI, Claude, Gemini, response APIs, and count-token flows.

### Request Flow

1. `AuthMiddleware` authenticates the client API key
2. Middleware stores authenticated key identity plus normalized policy metadata in `gin.Context`
3. Base handler extracts model and calls `APIKeyPolicyManager.Enter(...)`
4. Policy manager performs checks in this order:
   - Super-key short circuit
   - Model whitelist validation
   - RPM validation
   - QPS/burst validation
   - Token quota pre-check
   - Concurrency admission or queueing
5. Request proceeds through existing auth-manager execution flow
6. On request completion, usage reporter updates token counters
7. Deferred cleanup releases concurrency permits and wakes queued requests

### Model Matching

Model whitelist matching is performed against the normalized request model name after existing auto-model resolution, but before any executor-specific upstream remapping.

This keeps user-visible configuration consistent with what the request handlers accept.

### Concurrency Queueing

Each API key receives an isolated in-memory queue with:

- `max`: in-flight slot count
- `queue-max`: waiting capacity
- `queue-timeout-ms`: per-request wait timeout

Behavior:

- If slots are available, request runs immediately
- If full and queue has capacity, request waits
- If queue is full, request is rejected with `429`
- If queue wait exceeds timeout, request is rejected with `429`
- If the client request context is canceled, the queued request is removed

### Token Quota Accounting

Two counters are maintained per API key:

- Lifetime consumed tokens
- Period-window consumed tokens

Checks happen before execution using persisted counters. Final counters are incremented after actual usage is known from the existing usage reporting path.

This approach is simple and robust, though the very last concurrent requests may slightly overshoot the configured quota before subsequent requests are blocked.

## Persistence

Add a dedicated policy state persistence file separate from usage statistics.

Persisted fields per key:

- Lifetime tokens used
- Periodic window anchor
- Periodic tokens used

Do not persist:

- Current in-flight concurrency
- Current waiting queue
- Transient rate limiter token bucket state

On restart:

- Lifetime and periodic counters are restored
- Concurrency and queues start empty
- Rate limiter buckets restart empty

## Management API Design

### Existing Endpoint Upgrade

Upgrade `/api-keys` management endpoints to operate on normalized object entries while still accepting legacy string-list payloads.

- `GET /api-keys`
- `PUT /api-keys`
- `PATCH /api-keys`
- `DELETE /api-keys`

### New Endpoints

- `GET /api-keys/runtime`
  - Returns current per-key runtime state
- `POST /api-keys/reset-tokens`
  - Resets lifetime, periodic, or both counters for a target key

### Runtime State Payload

Per key runtime payload should include at least:

- Key identifier
- Name
- Super flag
- Current in-flight count
- Current queue length
- Lifetime tokens used
- Periodic tokens used
- Active periodic window
- Recent limiter rejection counters if available

## Frontend Design

### Primary UI Surface

Upgrade the System page API key management UI into a structured list editor.

Each list row should display:

- Key name or fallback label
- Masked key value
- Super badge
- Model whitelist count
- RPM summary
- QPS/burst summary
- Concurrency summary
- Token quota summary

### Editing UI

Use a modal or side panel grouped into four sections:

1. Basic info
   - key
   - name
   - description
   - super toggle
2. Model control
   - whitelist list
   - wildcard usage hint
3. Traffic and concurrency
   - RPM
   - QPS
   - burst
   - max concurrency
   - queue max
   - queue timeout
4. Token quotas
   - lifetime limit
   - periodic limit
   - periodic window
   - runtime usage display
   - reset actions

Super API key mode should visually disable non-applicable controls.

### Visual YAML Editor Compatibility

The visual config editor currently treats `api-keys` as plain text lines. That must not destroy structured entries.

Version 1 behavior:

- If `api-keys` contains only strings, keep the existing simple editor
- If any structured object is detected, the visual editor shows the block as advanced/managed elsewhere and does not flatten it back into strings
- Source mode remains the escape hatch for direct YAML edits

## Error Behavior

Suggested API errors:

- Model not allowed: `403` with code `model_not_allowed`
- RPM limit exceeded: `429` with code `rate_limit_exceeded`
- QPS/burst exceeded: `429` with code `rate_limit_exceeded`
- Concurrency queue full: `429` with code `rate_limit_exceeded`
- Concurrency queue timeout: `429` with code `rate_limit_exceeded`
- Token quota exceeded: `403` with code `insufficient_quota`

The response body should remain OpenAI-compatible using the existing error response conventions.

## Testing Strategy

### Backend

- Config parsing for string, object, and mixed entries
- Middleware context propagation of normalized policy data
- Model whitelist matching including wildcard rules
- RPM checks
- QPS/burst checks
- Concurrency queue admission, queue full, timeout, and cancel removal
- Lifetime quota enforcement
- Periodic quota reset for day and month windows
- Runtime endpoint payload correctness
- Token reset endpoint behavior
- Regression coverage for legacy `api-keys` auth behavior

### Frontend

- Structured API key list rendering
- Add/edit/delete flows
- Super-key toggle behavior
- Wildcard model editor UX
- Runtime status rendering
- Reset token actions
- Visual editor compatibility guard for structured `api-keys`

## Implementation Sequence

1. Add normalized backend config types and compatibility parsing
2. Extend auth middleware/context propagation
3. Add policy runtime manager and enforcement hooks
4. Add persistent token counter state
5. Add runtime and reset management endpoints
6. Upgrade frontend API key management UI
7. Guard visual config editor against flattening structured API keys
8. Add backend and frontend tests

## Recommended Outcome

Use the backward-compatible top-level `api-keys` upgrade with centralized runtime enforcement. This delivers the requested controls without forcing users to migrate to a new authentication model and keeps the operational surface understandable for both YAML users and management UI users.
