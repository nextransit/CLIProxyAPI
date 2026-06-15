package auth

import (
	"sync"
	"time"
)

// sessionEntry stores auth binding with expiration and a per-session request
// counter used to drive the weighted-rotation policy.
type sessionEntry struct {
	authID       string
	expiresAt    time.Time
	requestCount int
}

// SessionCache provides TTL-based session to auth mapping with automatic cleanup.
type SessionCache struct {
	mu      sync.RWMutex
	entries map[string]sessionEntry
	ttl     time.Duration
	stopCh  chan struct{}
}

// NewSessionCache creates a cache with the specified TTL.
// A background goroutine periodically cleans expired entries.
func NewSessionCache(ttl time.Duration) *SessionCache {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	c := &SessionCache{
		entries: make(map[string]sessionEntry),
		ttl:     ttl,
		stopCh:  make(chan struct{}),
	}
	go c.cleanupLoop()
	return c
}

// Get retrieves the auth ID bound to a session, if still valid.
// Does NOT refresh the TTL on access.
func (c *SessionCache) Get(sessionID string) (string, bool) {
	if sessionID == "" {
		return "", false
	}
	c.mu.RLock()
	entry, ok := c.entries[sessionID]
	c.mu.RUnlock()
	if !ok {
		return "", false
	}
	if time.Now().After(entry.expiresAt) {
		c.mu.Lock()
		delete(c.entries, sessionID)
		c.mu.Unlock()
		return "", false
	}
	return entry.authID, true
}

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

// Invalidate removes a specific session binding.
func (c *SessionCache) Invalidate(sessionID string) {
	if sessionID == "" {
		return
	}
	c.mu.Lock()
	delete(c.entries, sessionID)
	c.mu.Unlock()
}

// InvalidateAuth removes all sessions bound to a specific auth ID.
// Used when an auth becomes unavailable.
func (c *SessionCache) InvalidateAuth(authID string) {
	if c == nil || authID == "" {
		return
	}
	c.mu.Lock()
	for sid, entry := range c.entries {
		if entry.authID == authID {
			delete(c.entries, sid)
		}
	}
	c.mu.Unlock()
}

// CountByAuth returns a map of authID to the number of currently active
// sessions bound to it. Expired entries are excluded. Used by the weighted
// session-affinity selector to spread new sessions across auths in proportion
// to each auth's configured weight.
func (c *SessionCache) CountByAuth() map[string]int {
	out := map[string]int{}
	if c == nil {
		return out
	}
	now := time.Now()
	c.mu.RLock()
	entries := make([]sessionEntry, 0, len(c.entries))
	expired := make([]string, 0)
	for sid, entry := range c.entries {
		if now.After(entry.expiresAt) {
			expired = append(expired, sid)
			continue
		}
		entries = append(entries, entry)
	}
	c.mu.RUnlock()
	if len(expired) > 0 {
		c.mu.Lock()
		for _, sid := range expired {
			delete(c.entries, sid)
		}
		c.mu.Unlock()
	}
	for _, entry := range entries {
		out[entry.authID]++
	}
	return out
}

// Stop terminates the background cleanup goroutine.
func (c *SessionCache) Stop() {
	select {
	case <-c.stopCh:
	default:
		close(c.stopCh)
	}
}

func (c *SessionCache) cleanupLoop() {
	ticker := time.NewTicker(c.ttl / 2)
	defer ticker.Stop()
	for {
		select {
		case <-c.stopCh:
			return
		case <-ticker.C:
			c.cleanup()
		}
	}
}

func (c *SessionCache) cleanup() {
	now := time.Now()
	c.mu.Lock()
	for sid, entry := range c.entries {
		if now.After(entry.expiresAt) {
			delete(c.entries, sid)
		}
	}
	c.mu.Unlock()
}
