package auth

import (
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
)

// ModelRoutingDiagnostics describes the credentials that can or cannot serve a
// model at the current instant.
type ModelRoutingDiagnostics struct {
	Model                   string                       `json:"model"`
	CanonicalModel          string                       `json:"canonical_model"`
	Providers               []string                     `json:"providers"`
	SessionAffinity         *SessionAffinityDiagnostics  `json:"session_affinity,omitempty"`
	ProviderDiagnostics     []ProviderRoutingDiagnostics `json:"provider_diagnostics"`
	EffectiveCandidateCount int                          `json:"effective_candidate_count"`
}

// SessionAffinityDiagnostics summarizes the active selector policy.
type SessionAffinityDiagnostics struct {
	Enabled     bool   `json:"enabled"`
	Strategy    string `json:"strategy"`
	TTL         string `json:"ttl,omitempty"`
	MaxRequests int    `json:"max_requests,omitempty"`
}

// ProviderRoutingDiagnostics describes candidate credentials for one provider.
type ProviderRoutingDiagnostics struct {
	Provider                string                  `json:"provider"`
	ExecutorRegistered      bool                    `json:"executor_registered"`
	EffectiveCandidateCount int                     `json:"effective_candidate_count"`
	Candidates              []CredentialDiagnostics `json:"candidates"`
}

// CredentialDiagnostics describes one credential's routing state for a model.
type CredentialDiagnostics struct {
	AuthID                  string `json:"auth_id"`
	AuthIndex               string `json:"auth_index,omitempty"`
	Provider                string `json:"provider"`
	Label                   string `json:"label,omitempty"`
	AccountKind             string `json:"account_kind,omitempty"`
	Account                 string `json:"account,omitempty"`
	Status                  Status `json:"status"`
	Disabled                bool   `json:"disabled"`
	Unavailable             bool   `json:"unavailable"`
	SupportsModel           bool   `json:"supports_model"`
	Effective               bool   `json:"effective"`
	BlockedReason           string `json:"blocked_reason,omitempty"`
	SelectionModel          string `json:"selection_model,omitempty"`
	SelectionModelSupported bool   `json:"selection_model_supported,omitempty"`
	Weight                  int    `json:"weight"`
	Priority                int    `json:"priority"`
	SessionBindingScope     string `json:"session_binding_scope,omitempty"`
	SessionBindings         int    `json:"session_bindings,omitempty"`
	BaseURL                 string `json:"base_url,omitempty"`
	ProxyConfigured         bool   `json:"proxy_configured,omitempty"`
	NextRetryAfter          string `json:"next_retry_after,omitempty"`
	StatusMessage           string `json:"status_message,omitempty"`
	LastError               string `json:"last_error,omitempty"`
}

// DiagnoseModelRouting returns route-level visibility without executing an
// upstream request.
func (m *Manager) DiagnoseModelRouting(model string, requestedProviders []string) ModelRoutingDiagnostics {
	model = strings.TrimSpace(model)
	canonical := canonicalModelKey(model)
	if canonical == "" {
		canonical = strings.TrimSpace(thinking.ParseSuffix(model).ModelName)
	}
	providers := m.normalizeDiagnosticProviders(model, requestedProviders)
	out := ModelRoutingDiagnostics{
		Model:           model,
		CanonicalModel:  canonical,
		Providers:       providers,
		SessionAffinity: m.sessionAffinityDiagnostics(),
	}
	now := time.Now()
	type candidateState struct {
		diagnostics        CredentialDiagnostics
		eligibleForCurrent bool
	}
	type providerState struct {
		diagnostics ProviderRoutingDiagnostics
		candidates  []candidateState
	}
	states := make([]providerState, 0, len(providers))
	bestPriority := 0
	hasEligible := false
	for _, provider := range providers {
		pd := ProviderRoutingDiagnostics{
			Provider:           provider,
			ExecutorRegistered: m.executorRegistered(provider),
		}
		state := providerState{diagnostics: pd}
		auths := m.authsForProvider(provider)
		for _, auth := range auths {
			diag, eligible := m.credentialDiagnostics(auth, provider, model, now)
			if eligible && !pd.ExecutorRegistered {
				eligible = false
				diag.BlockedReason = "executor_not_registered"
			}
			if eligible && (!hasEligible || diag.Priority > bestPriority) {
				bestPriority = diag.Priority
				hasEligible = true
			}
			state.candidates = append(state.candidates, candidateState{
				diagnostics:        diag,
				eligibleForCurrent: eligible,
			})
		}
		states = append(states, state)
	}
	for _, state := range states {
		pd := state.diagnostics
		for _, candidate := range state.candidates {
			diag := candidate.diagnostics
			if candidate.eligibleForCurrent && hasEligible && diag.Priority == bestPriority {
				diag.Effective = true
				pd.EffectiveCandidateCount++
				out.EffectiveCandidateCount++
			} else if candidate.eligibleForCurrent && diag.BlockedReason == "" {
				diag.BlockedReason = "lower_priority"
			}
			pd.Candidates = append(pd.Candidates, diag)
		}
		out.ProviderDiagnostics = append(out.ProviderDiagnostics, pd)
	}
	return out
}

func (m *Manager) normalizeDiagnosticProviders(model string, requestedProviders []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(requestedProviders))
	add := func(provider string) {
		provider = strings.ToLower(strings.TrimSpace(provider))
		if provider == "" {
			return
		}
		if _, ok := seen[provider]; ok {
			return
		}
		seen[provider] = struct{}{}
		out = append(out, provider)
	}
	for _, provider := range requestedProviders {
		add(provider)
	}
	if len(out) == 0 && model != "" {
		for _, provider := range registry.GetGlobalRegistry().GetModelProviders(canonicalModelKey(model)) {
			add(provider)
		}
	}
	if len(out) == 0 {
		m.mu.RLock()
		for _, auth := range m.auths {
			if auth == nil {
				continue
			}
			add(auth.Provider)
		}
		m.mu.RUnlock()
	}
	return out
}

func (m *Manager) sessionAffinityDiagnostics() *SessionAffinityDiagnostics {
	if m == nil {
		return nil
	}
	sel, ok := m.selector.(*SessionAffinitySelector)
	if !ok || sel == nil {
		return &SessionAffinityDiagnostics{Enabled: false, Strategy: selectorStrategyName(m.selector)}
	}
	return &SessionAffinityDiagnostics{
		Enabled:     true,
		Strategy:    selectorStrategyName(sel.fallback),
		TTL:         sel.cacheTTLString(),
		MaxRequests: sel.maxRequests,
	}
}

func selectorStrategyName(selector Selector) string {
	switch selector.(type) {
	case *FillFirstSelector:
		return "fill-first"
	case *RoundRobinSelector, nil:
		return "round-robin"
	default:
		return "custom"
	}
}

func (s *SessionAffinitySelector) cacheTTLString() string {
	if s == nil || s.cache == nil {
		return ""
	}
	return s.cache.ttl.String()
}

func (m *Manager) executorRegistered(provider string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.executors[strings.ToLower(strings.TrimSpace(provider))]
	return ok
}

func (m *Manager) authsForProvider(provider string) []*Auth {
	provider = strings.ToLower(strings.TrimSpace(provider))
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Auth, 0)
	for _, auth := range m.auths {
		if auth == nil {
			continue
		}
		if strings.ToLower(strings.TrimSpace(auth.Provider)) != provider {
			continue
		}
		out = append(out, auth.Clone())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (m *Manager) credentialDiagnostics(auth *Auth, sessionProvider, model string, now time.Time) (CredentialDiagnostics, bool) {
	kind, account := auth.AccountInfo()
	selectionModel := m.selectionModelForAuth(auth, model)
	reg := registry.GetGlobalRegistry()
	routeSupported := m.authSupportsRouteModel(reg, auth, model)
	selectionSupported := selectionModel != "" && reg.ClientSupportsModel(auth.ID, canonicalModelKey(selectionModel))
	blocked, reason, next := isAuthBlockedForModel(auth, selectionModel, now)
	blockedReason := diagnosticBlockedReason(blocked, reason, routeSupported, auth)
	authIndex := auth.EnsureIndex()
	bindings := 0
	if sel, ok := m.selector.(*SessionAffinitySelector); ok && sel != nil && sel.cache != nil {
		bindings = sel.cache.CountByAuthFor(sessionProvider, selectionArgForSelector(m.selector, model))[auth.ID]
	}
	diag := CredentialDiagnostics{
		AuthID:                  auth.ID,
		AuthIndex:               authIndex,
		Provider:                auth.Provider,
		Label:                   auth.Label,
		AccountKind:             kind,
		Account:                 account,
		Status:                  auth.Status,
		Disabled:                auth.Disabled || auth.Status == StatusDisabled,
		Unavailable:             auth.Unavailable,
		SupportsModel:           routeSupported,
		BlockedReason:           blockedReason,
		SelectionModel:          selectionModel,
		SelectionModelSupported: selectionSupported,
		Weight:                  authWeight(auth),
		Priority:                authPriority(auth),
		SessionBindingScope:     sessionProvider,
		SessionBindings:         bindings,
		BaseURL:                 maskedAttribute(auth, "base_url"),
		ProxyConfigured:         strings.TrimSpace(auth.ProxyURL) != "",
		StatusMessage:           auth.StatusMessage,
	}
	if !next.IsZero() && next.After(now) {
		diag.NextRetryAfter = next.UTC().Format(time.RFC3339)
	}
	if auth.LastError != nil {
		diag.LastError = auth.LastError.Error()
	}
	return diag, routeSupported && !blocked
}

func diagnosticBlockedReason(blocked bool, reason blockReason, supportsModel bool, auth *Auth) string {
	if !supportsModel {
		return "unsupported_model"
	}
	if auth == nil {
		return "invalid_auth"
	}
	if auth.Disabled || auth.Status == StatusDisabled {
		return "disabled"
	}
	if !blocked {
		return ""
	}
	switch reason {
	case blockReasonCooldown:
		return "cooldown"
	case blockReasonDisabled:
		return "disabled"
	default:
		return "unavailable"
	}
}

func maskedAttribute(auth *Auth, key string) string {
	if auth == nil || len(auth.Attributes) == 0 {
		return ""
	}
	return maskDiagnosticValue(auth.Attributes[key])
}

func maskDiagnosticValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) <= 12 {
		return value
	}
	return value[:8] + "..." + value[len(value)-4:]
}
