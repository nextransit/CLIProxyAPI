package auth

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestMiniMaxM3TokenPlanRetryAt(t *testing.T) {
	quotaError := &Error{
		HTTPStatus: http.StatusTooManyRequests,
		Message:    `{"error":{"message":"已达到 Token Plan 用量上限 (2056)"}}`,
	}
	tests := []struct {
		name  string
		model string
		now   time.Time
		want  time.Time
	}{
		{
			name:  "before 05:00",
			model: "MiniMax-M3",
			now:   time.Date(2026, time.July, 20, 4, 59, 59, 0, miniMaxM3QuotaLocation),
			want:  time.Date(2026, time.July, 20, 5, 0, 0, 0, miniMaxM3QuotaLocation),
		},
		{
			name:  "at 05:00",
			model: "minimax-claude/MiniMax-M3",
			now:   time.Date(2026, time.July, 20, 5, 0, 0, 0, miniMaxM3QuotaLocation),
			want:  time.Date(2026, time.July, 20, 10, 0, 0, 0, miniMaxM3QuotaLocation),
		},
		{
			name:  "before 15:00",
			model: "minimax-claude/MiniMax-M3",
			now:   time.Date(2026, time.July, 20, 14, 39, 14, 0, miniMaxM3QuotaLocation),
			want:  time.Date(2026, time.July, 20, 15, 0, 0, 0, miniMaxM3QuotaLocation),
		},
		{
			name:  "after 20:00",
			model: "MiniMax-M3",
			now:   time.Date(2026, time.July, 20, 20, 0, 1, 0, miniMaxM3QuotaLocation),
			want:  time.Date(2026, time.July, 21, 0, 0, 0, 0, miniMaxM3QuotaLocation),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := miniMaxM3TokenPlanRetryAt(tt.model, quotaError, tt.now)
			if !ok {
				t.Fatal("expected fixed MiniMax-M3 quota retry time")
			}
			if !got.Equal(tt.want) {
				t.Fatalf("retry time = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMiniMaxM3TokenPlanRetryAtScope(t *testing.T) {
	now := time.Date(2026, time.July, 20, 14, 39, 14, 0, miniMaxM3QuotaLocation)
	tests := []struct {
		name  string
		model string
		err   *Error
		want  bool
	}{
		{
			name:  "code in error field",
			model: "MiniMax-M3",
			err:   &Error{Code: "2056", HTTPStatus: http.StatusTooManyRequests, Message: "Token Plan exhausted"},
			want:  true,
		},
		{
			name:  "usage limit without code",
			model: "minimax-claude/MiniMax-M3",
			err:   &Error{HTTPStatus: http.StatusTooManyRequests, Message: "Token Plan usage limit exceeded"},
			want:  true,
		},
		{
			name:  "ordinary rate limit",
			model: "MiniMax-M3",
			err:   &Error{HTTPStatus: http.StatusTooManyRequests, Message: "rate limit exceeded"},
		},
		{
			name:  "different model",
			model: "MiniMax-M2.7",
			err:   &Error{HTTPStatus: http.StatusTooManyRequests, Message: "Token Plan usage limit exceeded (2056)"},
		},
		{
			name:  "different status",
			model: "MiniMax-M3",
			err:   &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "Token Plan usage limit exceeded (2056)"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, got := miniMaxM3TokenPlanRetryAt(tt.model, tt.err, now)
			if got != tt.want {
				t.Fatalf("matched = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestManagerMarkResultMiniMaxM3UsesFixedTokenPlanRetryTime(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	for _, model := range []string{"MiniMax-M3", "minimax-claude/MiniMax-M3"} {
		t.Run(model, func(t *testing.T) {
			manager := NewManager(nil, &RoundRobinSelector{}, nil)
			auth := &Auth{ID: model, Provider: "claude", Status: StatusActive}
			if _, err := manager.Register(context.Background(), auth); err != nil {
				t.Fatalf("register auth: %v", err)
			}

			quotaError := &Error{
				HTTPStatus: http.StatusTooManyRequests,
				Message:    `{"error":{"message":"已达到 Token Plan 用量上限 (2056)"}}`,
			}
			retryAfter := 30 * time.Minute
			before := time.Now()
			expectedBefore, _ := miniMaxM3TokenPlanRetryAt(model, quotaError, before)
			manager.MarkResult(context.Background(), Result{
				AuthID:     auth.ID,
				Provider:   auth.Provider,
				Model:      model,
				RetryAfter: &retryAfter,
				Error:      quotaError,
			})
			after := time.Now()
			expectedAfter, _ := miniMaxM3TokenPlanRetryAt(model, quotaError, after)

			updated, ok := manager.GetByID(auth.ID)
			if !ok || updated == nil {
				t.Fatal("missing updated auth")
			}
			state := updated.ModelStates[model]
			if state == nil {
				t.Fatal("missing model state")
			}
			if !state.NextRetryAfter.Equal(expectedBefore) && !state.NextRetryAfter.Equal(expectedAfter) {
				t.Fatalf("NextRetryAfter = %v, want %v or %v", state.NextRetryAfter, expectedBefore, expectedAfter)
			}
			if !state.Quota.NextRecoverAt.Equal(state.NextRetryAfter) {
				t.Fatalf("NextRecoverAt = %v, want %v", state.Quota.NextRecoverAt, state.NextRetryAfter)
			}
			if state.Quota.BackoffLevel != 0 {
				t.Fatalf("BackoffLevel = %d, want 0", state.Quota.BackoffLevel)
			}
		})
	}
}
