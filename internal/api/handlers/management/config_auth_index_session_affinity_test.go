package management

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestOpenAICompatibilityWithAuthIndexPreservesSessionAffinityMaxRequests(t *testing.T) {
	t.Parallel()

	maxRequests := 5
	h := &Handler{
		cfg: &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{
				{
					Name:                       "SenseNova",
					BaseURL:                    "https://token.sensenova.cn/v1",
					SessionAffinityMaxRequests: &maxRequests,
				},
			},
		},
	}

	got := h.openAICompatibilityWithAuthIndex()
	if len(got) != 1 {
		t.Fatalf("providers = %d, want 1", len(got))
	}
	if got[0].SessionAffinityMaxRequests == nil || *got[0].SessionAffinityMaxRequests != 5 {
		t.Fatalf("session affinity max requests = %v, want 5", got[0].SessionAffinityMaxRequests)
	}
}
