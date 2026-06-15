package auth

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// TestManager_ResumeAuthModels_ClearsCooldownAndSuspension verifies that
// ResumeAuthModels restores a credential that was previously pinned out of
// rotation by a per-model 404-style failure, without forcing the operator to
// restart the service.
func TestManager_ResumeAuthModels_ClearsCooldownAndSuspension(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	m := NewManager(nil, nil, nil)

	auth := &Auth{
		ID:       "auth-resume",
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
			Message:    `{"detail":"模型已下架"}`,
		},
	})

	// Pre-condition: the model is now blocked for 30 minutes.
	before, ok := m.GetByID(auth.ID)
	if !ok || before == nil {
		t.Fatalf("expected auth to be present")
	}
	state := before.ModelStates[model]
	if state == nil || !state.Unavailable {
		t.Fatalf("expected per-model Unavailable=true before resume, got %#v", state)
	}
	if !state.NextRetryAfter.After(time.Now()) {
		t.Fatalf("expected NextRetryAfter in the future before resume, got %v", state.NextRetryAfter)
	}

	// Action.
	cleared, err := m.ResumeAuthModels(context.Background(), auth.ID, nil)
	if err != nil {
		t.Fatalf("ResumeAuthModels: %v", err)
	}
	if cleared != 1 {
		t.Fatalf("expected 1 cleared model state, got %d", cleared)
	}

	// Post-condition: the model is back to active and the auth has no cooldown.
	after, ok := m.GetByID(auth.ID)
	if !ok || after == nil {
		t.Fatalf("expected auth to be present after resume")
	}
	resumed := after.ModelStates[model]
	if resumed == nil {
		t.Fatalf("expected model state to remain in the map (just reset), got nil")
	}
	if resumed.Unavailable {
		t.Fatalf("expected Unavailable=false after resume, got true")
	}
	if !resumed.NextRetryAfter.IsZero() {
		t.Fatalf("expected NextRetryAfter to be cleared, got %v", resumed.NextRetryAfter)
	}
	if after.Status == StatusError {
		t.Fatalf("expected auth.Status to be reset to active, got %v", after.Status)
	}
	if after.Unavailable {
		t.Fatalf("expected auth.Unavailable=false after resume, got true")
	}
	if !after.NextRetryAfter.IsZero() {
		t.Fatalf("expected auth.NextRetryAfter zero after resume, got %v", after.NextRetryAfter)
	}
}

// TestManager_ResumeAuthModels_FilterByModel ensures the optional model list
// only resets the requested models and leaves other model cooldowns alone.
func TestManager_ResumeAuthModels_FilterByModel(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	m := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-filter", Provider: "codex"}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	// Force two independent per-model states by tweaking the auth map directly
	// via the manager's internal sync points. We rely on MarkResult having
	// already set one model and then patch the second in via a follow-up call
	// with a different model id; the model_states map is append-only here so
	// the easiest path is to drive two MarkResult calls.
	for _, model := range []string{"gpt-5.3-codex", "gpt-5.4"} {
		m.MarkResult(context.Background(), Result{
			AuthID:   auth.ID,
			Provider: auth.Provider,
			Model:    model,
			Success:  false,
			Error: &Error{
				HTTPStatus: http.StatusNotFound,
				Message:    `{"detail":"模型已下架"}`,
			},
		})
	}

	cleared, err := m.ResumeAuthModels(context.Background(), auth.ID, []string{"gpt-5.3-codex"})
	if err != nil {
		t.Fatalf("ResumeAuthModels: %v", err)
	}
	if cleared != 1 {
		t.Fatalf("expected 1 cleared, got %d", cleared)
	}

	updated, _ := m.GetByID(auth.ID)
	if got := updated.ModelStates["gpt-5.3-codex"]; got == nil || got.Unavailable {
		t.Fatalf("expected gpt-5.3-codex to be reset, got %#v", got)
	}
	if got := updated.ModelStates["gpt-5.4"]; got == nil || !got.Unavailable {
		t.Fatalf("expected gpt-5.4 to remain Unavailable, got %#v", got)
	}
}
