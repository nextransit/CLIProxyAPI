package auth

import (
	"context"
	"net/http"
	"testing"
)

func TestManagerMarkResultSenseNovaTokenPlanExhaustedKeepsTemporaryCooldown(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	auth := &Auth{ID: "sensenova-exhausted", Provider: "sensenova", Status: StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "sensenova",
		Model:    "deepseek-v4-flash",
		Error: &Error{
			HTTPStatus: http.StatusTooManyRequests,
			Message:    `{"error":{"message":"token plan limit exhausted"}}`,
		},
	})

	updated := manager.List()[0]
	state := updated.ModelStates["deepseek-v4-flash"]
	if state == nil {
		t.Fatal("missing model state")
	}
	if state.Status == StatusDisabled {
		t.Fatal("a single token-plan 429 must not permanently disable the model")
	}
	if state.NextRetryAfter.IsZero() {
		t.Fatal("token-plan exhaustion should set a retry time")
	}
	if state.Quota.Reason != "quota" {
		t.Fatalf("quota reason = %q, want quota", state.Quota.Reason)
	}
}

func TestManagerMarkResultSenseNovaRPMExhaustedKeepsTemporaryCooldown(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	auth := &Auth{ID: "sensenova-rpm", Provider: "sensenova", Status: StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "sensenova",
		Model:    "deepseek-v4-flash",
		Error: &Error{
			HTTPStatus: http.StatusTooManyRequests,
			Message:    `{"error":{"message":"rpm exhausted"}}`,
		},
	})

	updated := manager.List()[0]
	state := updated.ModelStates["deepseek-v4-flash"]
	if state == nil {
		t.Fatal("missing model state")
	}
	if state.Status == StatusDisabled {
		t.Fatal("RPM exhaustion should remain a temporary cooldown")
	}
	if state.NextRetryAfter.IsZero() {
		t.Fatal("RPM exhaustion should set a retry time")
	}
}
