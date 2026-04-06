package configaccess

import (
	"net/http/httptest"
	"testing"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v6/sdk/access"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestRegister_UsesStructuredAPIKeyEntries(t *testing.T) {
	t.Cleanup(func() {
		sdkaccess.UnregisterProvider(sdkaccess.AccessProviderTypeConfigAPIKey)
	})

	Register(&sdkconfig.SDKConfig{
		APIKeyEntries: []sdkconfig.APIKeyEntry{
			{Key: "structured-key"},
		},
	})

	providers := sdkaccess.RegisteredProviders()
	if len(providers) != 1 {
		t.Fatalf("expected 1 registered provider, got %d", len(providers))
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer structured-key")

	result, authErr := providers[0].Authenticate(req.Context(), req)
	if authErr != nil {
		t.Fatalf("Authenticate() error = %v", authErr)
	}
	if result == nil {
		t.Fatal("Authenticate() returned nil result")
	}
	if result.Principal != "structured-key" {
		t.Fatalf("result.Principal = %q, want structured-key", result.Principal)
	}
}
