package auth

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// TestManager_MarkResult_GenericNotFoundUsesShortPerModelCooldown covers the
// regression where a 404 from an upstream that simply does not expose the
// requested model pinned the entire auth out of rotation for 12h. The fix is:
//   - per-model state is still marked Unavailable so the routing layer can
//     avoid retrying the same upstream for the same model,
//   - the auth itself is NOT marked Unavailable, so other models keep working,
//   - the per-model retry window is short (30m) instead of 12h.
func TestManager_MarkResult_GenericNotFoundUsesShortPerModelCooldown(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	m := NewManager(nil, nil, nil)

	auth := &Auth{
		ID:       "auth-404",
		Provider: "codex",
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	model := "gpt-5.3-codex"
	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    model,
		Success:  false,
		Error: &Error{
			HTTPStatus: http.StatusNotFound,
			Message:    `{"detail":"不支持的模型/模型已下架，请更换模型!"}`,
		},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}

	// Auth should remain available — a missing-model 404 on a single model
	// must not knock the credential out for every other model too.
	if updated.Unavailable {
		t.Fatalf("expected auth to stay available after per-model 404, got Unavailable=true")
	}
	if !updated.NextRetryAfter.IsZero() {
		t.Fatalf("expected auth-level NextRetryAfter to stay zero, got %v", updated.NextRetryAfter)
	}

	state := updated.ModelStates[model]
	if state == nil {
		t.Fatalf("expected per-model state to be recorded for %s", model)
	}
	if !state.Unavailable {
		t.Fatalf("expected per-model Unavailable=true for %s", model)
	}
	if state.NextRetryAfter.IsZero() {
		t.Fatalf("expected per-model NextRetryAfter to be set")
	}
	// Allow a generous window so the test isn't flaky under load, but fail
	// loudly if we ever regress to the 12h pin.
	if d := state.NextRetryAfter.Sub(time.Now()); d > 2*time.Hour {
		t.Fatalf("per-model 404 cooldown too long: %v (>2h)", d)
	}
}
