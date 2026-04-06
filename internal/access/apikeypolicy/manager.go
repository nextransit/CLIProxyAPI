package apikeypolicy

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

const (
	DefaultQueueMax     = 128
	DefaultQueueTimeout = 30 * time.Second
)

const (
	errorCodeModelDenied        = "model_not_allowed"
	errorCodeRateLimit          = "rate_limit_exceeded"
	errorCodeConcurrencyQueue   = "concurrency_queue_full"
	errorCodeConcurrencyTimed   = "concurrency_queue_timeout"
	errorCodeTokenQuotaExceeded = "token_quota_exceeded"
)

type PolicyError struct {
	Status  int
	Code    string
	Message string
}

func (e *PolicyError) Error() string {
	if e == nil {
		return ""
	}
	if strings.TrimSpace(e.Message) != "" {
		return e.Message
	}
	return "api key policy rejected"
}

func (e *PolicyError) StatusCode() int {
	if e == nil || e.Status <= 0 {
		return http.StatusTooManyRequests
	}
	return e.Status
}

func (e *PolicyError) Headers() http.Header { return nil }

func newPolicyError(status int, code string, message string) *PolicyError {
	return &PolicyError{
		Status:  status,
		Code:    code,
		Message: strings.TrimSpace(message),
	}
}

type Lease interface {
	Release()
}

type lease struct {
	once  sync.Once
	state *keyState
	sem   chan struct{}
}

func (l *lease) Release() {
	if l == nil || l.state == nil || l.sem == nil {
		return
	}
	l.once.Do(func() {
		select {
		case <-l.sem:
		default:
		}
		l.state.mu.Lock()
		if l.state.inFlight > 0 {
			l.state.inFlight--
		}
		l.state.mu.Unlock()
	})
}

type compiledPolicy struct {
	entry    config.APIKeyEntry
	matchers []wildcardMatcher
}

type keyState struct {
	mu sync.Mutex

	sem    chan struct{}
	semCap int

	inFlight int
	waiters  int

	rpmWindowStart time.Time
	rpmCount       int

	qpsTokens float64
	qpsLast   time.Time

	lifetimeUsed int64
	periodicUsed int64
	periodicFrom time.Time
}

type RuntimeEntry struct {
	Key            string    `json:"key"`
	Name           string    `json:"name,omitempty"`
	Super          bool      `json:"super"`
	Models         []string  `json:"models,omitempty"`
	RPM            int       `json:"rpm,omitempty"`
	QPS            int       `json:"qps,omitempty"`
	Burst          int       `json:"burst,omitempty"`
	ConcurrencyMax int       `json:"concurrency-max,omitempty"`
	QueueMax       int       `json:"queue-max,omitempty"`
	QueueTimeoutMS int       `json:"queue-timeout-ms,omitempty"`
	LifetimeLimit  int64     `json:"lifetime-limit,omitempty"`
	LifetimeUsed   int64     `json:"lifetime-used"`
	PeriodicLimit  int64     `json:"periodic-limit,omitempty"`
	PeriodicWindow string    `json:"periodic-window,omitempty"`
	PeriodicUsed   int64     `json:"periodic-used"`
	PeriodicFrom   time.Time `json:"periodic-from,omitempty"`
	InFlight       int       `json:"in-flight"`
	Queueing       int       `json:"queueing"`
}

type Manager struct {
	mu sync.RWMutex

	policies map[string]*compiledPolicy
	states   map[string]*keyState

	defaultQueueMax     int
	defaultQueueTimeout time.Duration
	now                 func() time.Time
}

func NewManager() *Manager {
	return &Manager{
		policies:            make(map[string]*compiledPolicy),
		states:              make(map[string]*keyState),
		defaultQueueMax:     DefaultQueueMax,
		defaultQueueTimeout: DefaultQueueTimeout,
		now:                 time.Now,
	}
}

var defaultManager = NewManager()

func init() {
	coreusage.RegisterPlugin(defaultManager)
}

func DefaultManager() *Manager { return defaultManager }

func (m *Manager) SetPolicies(entries []config.APIKeyEntry) {
	if m == nil {
		return
	}

	compiled := make(map[string]*compiledPolicy, len(entries))
	for _, entry := range entries {
		key := strings.TrimSpace(entry.Key)
		if key == "" {
			continue
		}
		policy := &compiledPolicy{
			entry: config.APIKeyEntry{
				Key:         key,
				Name:        strings.TrimSpace(entry.Name),
				Description: strings.TrimSpace(entry.Description),
				Super:       entry.Super,
				Models:      append([]string(nil), entry.Models...),
				Limits:      entry.Limits,
			},
		}
		for _, pattern := range entry.Models {
			matcher, ok := newWildcardMatcher(pattern)
			if !ok {
				continue
			}
			policy.matchers = append(policy.matchers, matcher)
		}
		compiled[key] = policy
	}

	m.mu.Lock()
	m.policies = compiled
	for key := range compiled {
		if _, ok := m.states[key]; ok {
			continue
		}
		m.states[key] = &keyState{}
	}
	m.mu.Unlock()
}

func (m *Manager) Acquire(ctx context.Context, apiKey, requestedModel string) (Lease, error) {
	if m == nil {
		return nil, nil
	}
	key := strings.TrimSpace(apiKey)
	if key == "" {
		return nil, nil
	}

	policy := m.policyForKey(key)
	if policy == nil {
		return nil, nil
	}
	if policy.entry.Super {
		return nil, nil
	}

	model := normalizeModelToken(requestedModel)
	if !policy.allowsModel(model) {
		return nil, newPolicyError(http.StatusForbidden, errorCodeModelDenied, fmt.Sprintf("api key is not allowed to use model %q", requestedModel))
	}

	state := m.stateForKey(key)
	var acquired Lease
	if policy.entry.Limits.Concurrency.Max > 0 {
		concurrencyLease, err := m.acquireConcurrency(ctx, state, policy)
		if err != nil {
			return nil, err
		}
		acquired = concurrencyLease
	}

	if err := m.consumeAdmission(state, policy); err != nil {
		if acquired != nil {
			acquired.Release()
		}
		return nil, err
	}

	return acquired, nil
}

func (m *Manager) HandleUsage(_ context.Context, record coreusage.Record) {
	if m == nil {
		return
	}
	key := strings.TrimSpace(record.APIKey)
	if key == "" {
		return
	}
	tokens := record.Detail.TotalTokens
	if tokens <= 0 {
		tokens = record.Detail.InputTokens + record.Detail.OutputTokens + record.Detail.ReasoningTokens
	}
	if tokens <= 0 {
		return
	}
	state := m.stateForKey(key)
	policy := m.policyForKey(key)
	now := record.RequestedAt
	if now.IsZero() {
		now = m.nowUTC()
	} else {
		now = now.UTC()
	}

	state.mu.Lock()
	state.lifetimeUsed += tokens
	if policy != nil {
		m.refreshPeriodicLocked(state, policy, now)
		if policy.entry.Limits.Tokens.Periodic.Limit > 0 {
			state.periodicUsed += tokens
		}
	}
	state.mu.Unlock()
}

func (m *Manager) Snapshot() []RuntimeEntry {
	if m == nil {
		return nil
	}

	m.mu.RLock()
	keys := make([]string, 0, len(m.policies))
	for key := range m.policies {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	policies := make(map[string]*compiledPolicy, len(m.policies))
	for key, policy := range m.policies {
		policies[key] = policy
	}
	states := make(map[string]*keyState, len(m.states))
	for key, state := range m.states {
		states[key] = state
	}
	m.mu.RUnlock()

	now := m.nowUTC()
	result := make([]RuntimeEntry, 0, len(keys))
	for _, key := range keys {
		policy := policies[key]
		if policy == nil {
			continue
		}
		state := states[key]
		entry := RuntimeEntry{
			Key:            policy.entry.Key,
			Name:           policy.entry.Name,
			Super:          policy.entry.Super,
			Models:         append([]string(nil), policy.entry.Models...),
			RPM:            policy.entry.Limits.Rate.RPM,
			QPS:            policy.entry.Limits.Rate.QPS,
			Burst:          policy.entry.Limits.Rate.Burst,
			ConcurrencyMax: policy.entry.Limits.Concurrency.Max,
			QueueMax:       effectiveQueueMax(policy.entry.Limits.Concurrency.QueueMax, m.defaultQueueMax),
			QueueTimeoutMS: int(effectiveQueueTimeout(policy.entry.Limits.Concurrency.QueueTimeoutMS, m.defaultQueueTimeout).Milliseconds()),
			LifetimeLimit:  policy.entry.Limits.Tokens.Lifetime.Limit,
			PeriodicLimit:  policy.entry.Limits.Tokens.Periodic.Limit,
			PeriodicWindow: policy.entry.Limits.Tokens.Periodic.Window,
		}
		if state != nil {
			state.mu.Lock()
			m.refreshPeriodicLocked(state, policy, now)
			entry.InFlight = state.inFlight
			entry.Queueing = state.waiters
			entry.LifetimeUsed = state.lifetimeUsed
			entry.PeriodicUsed = state.periodicUsed
			entry.PeriodicFrom = state.periodicFrom
			state.mu.Unlock()
		}
		result = append(result, entry)
	}

	return result
}

func (m *Manager) ResetTokenUsage(apiKey string) {
	if m == nil {
		return
	}
	trimmed := strings.TrimSpace(apiKey)
	if trimmed != "" {
		state := m.stateForKey(trimmed)
		state.mu.Lock()
		state.lifetimeUsed = 0
		state.periodicUsed = 0
		state.periodicFrom = time.Time{}
		state.mu.Unlock()
		return
	}

	m.mu.RLock()
	states := make([]*keyState, 0, len(m.states))
	for _, state := range m.states {
		states = append(states, state)
	}
	m.mu.RUnlock()
	for _, state := range states {
		state.mu.Lock()
		state.lifetimeUsed = 0
		state.periodicUsed = 0
		state.periodicFrom = time.Time{}
		state.mu.Unlock()
	}
}

func (m *Manager) policyForKey(key string) *compiledPolicy {
	m.mu.RLock()
	policy := m.policies[key]
	m.mu.RUnlock()
	return policy
}

func (m *Manager) stateForKey(key string) *keyState {
	m.mu.RLock()
	state := m.states[key]
	m.mu.RUnlock()
	if state != nil {
		return state
	}

	m.mu.Lock()
	state = m.states[key]
	if state == nil {
		state = &keyState{}
		m.states[key] = state
	}
	m.mu.Unlock()
	return state
}

func (m *Manager) nowUTC() time.Time {
	if m.now == nil {
		return time.Now().UTC()
	}
	return m.now().UTC()
}

func (m *Manager) consumeAdmission(state *keyState, policy *compiledPolicy) error {
	now := m.nowUTC()

	state.mu.Lock()
	defer state.mu.Unlock()

	if policy.entry.Limits.Tokens.Lifetime.Limit > 0 && state.lifetimeUsed >= policy.entry.Limits.Tokens.Lifetime.Limit {
		return newPolicyError(http.StatusTooManyRequests, errorCodeTokenQuotaExceeded, "lifetime token quota exceeded")
	}
	m.refreshPeriodicLocked(state, policy, now)
	if policy.entry.Limits.Tokens.Periodic.Limit > 0 && state.periodicUsed >= policy.entry.Limits.Tokens.Periodic.Limit {
		return newPolicyError(http.StatusTooManyRequests, errorCodeTokenQuotaExceeded, "periodic token quota exceeded")
	}

	rpm := policy.entry.Limits.Rate.RPM
	if rpm > 0 {
		windowStart := now.Truncate(time.Minute)
		if state.rpmWindowStart.IsZero() || !state.rpmWindowStart.Equal(windowStart) {
			state.rpmWindowStart = windowStart
			state.rpmCount = 0
		}
		if state.rpmCount >= rpm {
			return newPolicyError(http.StatusTooManyRequests, errorCodeRateLimit, "rpm limit exceeded")
		}
	}

	qps := policy.entry.Limits.Rate.QPS
	if qps > 0 {
		burst := policy.entry.Limits.Rate.Burst
		if burst <= 0 {
			burst = qps
		}
		if state.qpsLast.IsZero() {
			state.qpsTokens = float64(burst)
			state.qpsLast = now
		} else {
			elapsed := now.Sub(state.qpsLast).Seconds()
			if elapsed > 0 {
				state.qpsTokens = minFloat64(float64(burst), state.qpsTokens+elapsed*float64(qps))
			}
			state.qpsLast = now
		}
		if state.qpsTokens < 1 {
			return newPolicyError(http.StatusTooManyRequests, errorCodeRateLimit, "qps limit exceeded")
		}
	}

	if rpm > 0 {
		state.rpmCount++
	}
	if qps > 0 {
		state.qpsTokens -= 1
	}
	return nil
}

func (m *Manager) acquireConcurrency(ctx context.Context, state *keyState, policy *compiledPolicy) (Lease, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	queueMax := effectiveQueueMax(policy.entry.Limits.Concurrency.QueueMax, m.defaultQueueMax)
	queueTimeout := effectiveQueueTimeout(policy.entry.Limits.Concurrency.QueueTimeoutMS, m.defaultQueueTimeout)

	var sem chan struct{}
	state.mu.Lock()
	max := policy.entry.Limits.Concurrency.Max
	if max <= 0 {
		state.mu.Unlock()
		return nil, nil
	}
	state.ensureSemaphoreLocked(max)
	if queueMax >= 0 && state.waiters >= queueMax {
		state.mu.Unlock()
		return nil, newPolicyError(http.StatusTooManyRequests, errorCodeConcurrencyQueue, "concurrency queue is full")
	}
	state.waiters++
	sem = state.sem
	state.mu.Unlock()

	acquired := false
	timer := time.NewTimer(queueTimeout)
	defer timer.Stop()
	select {
	case sem <- struct{}{}:
		acquired = true
	case <-ctx.Done():
	case <-timer.C:
	}

	state.mu.Lock()
	state.waiters--
	if acquired {
		state.inFlight++
	}
	state.mu.Unlock()

	if !acquired {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, newPolicyError(http.StatusTooManyRequests, errorCodeConcurrencyTimed, "concurrency queue timeout")
	}

	return &lease{state: state, sem: sem}, nil
}

func (s *keyState) ensureSemaphoreLocked(max int) {
	if max <= 0 {
		s.sem = nil
		s.semCap = 0
		return
	}
	if s.sem == nil {
		s.sem = make(chan struct{}, max)
		s.semCap = max
		return
	}
	if s.semCap == max {
		return
	}
	// Resize only when idle to avoid dropping in-flight acquisitions.
	if s.inFlight == 0 && s.waiters == 0 {
		s.sem = make(chan struct{}, max)
		s.semCap = max
	}
}

func effectiveQueueMax(configured int, defaults int) int {
	if configured > 0 {
		return configured
	}
	if defaults > 0 {
		return defaults
	}
	return 0
}

func effectiveQueueTimeout(configuredMS int, defaults time.Duration) time.Duration {
	if configuredMS > 0 {
		return time.Duration(configuredMS) * time.Millisecond
	}
	if defaults > 0 {
		return defaults
	}
	return 30 * time.Second
}

func (m *Manager) refreshPeriodicLocked(state *keyState, policy *compiledPolicy, now time.Time) {
	if policy == nil {
		return
	}
	window := policy.entry.Limits.Tokens.Periodic.Window
	if window != config.APIKeyTokenWindowDay && window != config.APIKeyTokenWindowMonth {
		return
	}
	windowStart := periodicWindowStart(now, window)
	if state.periodicFrom.IsZero() || !state.periodicFrom.Equal(windowStart) {
		state.periodicFrom = windowStart
		state.periodicUsed = 0
	}
}

func periodicWindowStart(now time.Time, window string) time.Time {
	utc := now.UTC()
	switch window {
	case config.APIKeyTokenWindowMonth:
		return time.Date(utc.Year(), utc.Month(), 1, 0, 0, 0, 0, time.UTC)
	default:
		return time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
	}
}

func (p *compiledPolicy) allowsModel(model string) bool {
	if p == nil {
		return true
	}
	if p.entry.Super {
		return true
	}
	if len(p.matchers) == 0 {
		return true
	}
	for _, matcher := range p.matchers {
		if matcher.Match(model) {
			return true
		}
	}
	return false
}

func minFloat64(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
