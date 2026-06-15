package auth

import "testing"

// TestIsModelSupportErrorMessage covers the patterns that should (and should not)
// be recognized as "the model is unsupported by this upstream". The list guards
// against accidental over-matching (e.g. ChatGPT-account-only messages being
// treated as model-not-supported and triggering 12h client suspension).
func TestIsModelSupportErrorMessage(t *testing.T) {
	cases := []struct {
		name    string
		message string
		want    bool
	}{
		// True positives: upstream is explicit about the model being unsupported.
		{
			name:    "model_not_supported code",
			message: `{"code":"model_not_supported","message":"..."}`,
			want:    true,
		},
		{
			name:    "requested model is not supported",
			message: "The requested model is not supported.",
			want:    true,
		},
		{
			name:    "requested model is unsupported",
			message: "The requested model is unsupported.",
			want:    true,
		},
		{
			name:    "requested model is unavailable",
			message: "The requested model is unavailable.",
			want:    true,
		},
		{
			name:    "unsupported model prefix",
			message: "Unsupported model: gpt-99",
			want:    true,
		},
		{
			name:    "model unavailable prefix",
			message: "Model unavailable for this endpoint.",
			want:    true,
		},
		{
			name:    "not available for your plan",
			message: "Model X is not available for your plan.",
			want:    true,
		},
		{
			name:    "not available for your account",
			message: "Model X is not available for your account.",
			want:    true,
		},

		// True negatives: messages that mention "not supported" but are about
		// account type, endpoint, or unrelated capabilities.
		{
			name:    "ChatGPT account restriction",
			message: "The 'gpt-5.3-codex' model is not supported when using Codex with a ChatGPT account.",
			want:    false,
		},
		{
			name:    "claude code endpoint restriction",
			message: "Claude Code is not supported on this endpoint.",
			// Endpoint-restricted model call: still a model-support style failure,
			// routing should fall through to another auth/upstream.
			want: true,
		},
		{
			name:    "dedicated anthropic endpoint",
			message: "This key is bound to a dedicated Anthropic-format endpoint.",
			want:    true,
		},
		{
			name:    "generic 400 unrelated",
			message: `{"detail":"System messages are not allowed"}`,
			want:    false,
		},
		{
			name:    "not_found for model id",
			message: `{"detail":"Item with id rs_0 not found."}`,
			want:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isModelSupportErrorMessage(tc.message)
			if got != tc.want {
				t.Fatalf("isModelSupportErrorMessage(%q) = %v, want %v", tc.message, got, tc.want)
			}
		})
	}
}
