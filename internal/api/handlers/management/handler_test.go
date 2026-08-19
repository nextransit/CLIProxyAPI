package management

import (
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// Localhost callers must never be banned. The threat model for the
// failed-attempts map is an off-host attacker probing the management
// key, not the operator sitting at the machine. After repeated wrong
// keys from 127.0.0.1 the correct key must continue to work.
func TestAuthenticateManagementKey_LocalhostIsNeverBanned(t *testing.T) {
	h := &Handler{
		cfg:            &config.Config{},
		failedAttempts: make(map[string]*attemptInfo),
		envSecret:      "test-secret",
	}

	for i := 0; i < 20; i++ {
		allowed, statusCode, errMsg := h.AuthenticateManagementKey("127.0.0.1", true, "wrong-secret")
		if allowed {
			t.Fatalf("expected auth to be denied at attempt %d", i+1)
		}
		if statusCode != http.StatusUnauthorized || errMsg != "invalid management key" {
			t.Fatalf("unexpected auth failure at attempt %d: status=%d msg=%q", i+1, statusCode, errMsg)
		}
	}

	allowed, statusCode, _ := h.AuthenticateManagementKey("127.0.0.1", true, "test-secret")
	if !allowed {
		t.Fatalf("expected localhost caller with the correct key to be allowed, got status=%d", statusCode)
	}
	if _, present := h.failedAttempts["127.0.0.1"]; present {
		t.Fatalf("localhost caller should not populate failedAttempts")
	}
}

// Remote callers do still get banned after repeated wrong keys so the
// existing brute-force protection stays in place.
func TestAuthenticateManagementKey_RemoteIPBanBlocksCorrectKeyDuringBan(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{
			RemoteManagement: config.RemoteManagement{
				AllowRemote: true,
			},
		},
		failedAttempts: make(map[string]*attemptInfo),
		envSecret:      "test-secret",
	}

	ip := "203.0.113.10"
	for i := 0; i < 5; i++ {
		allowed, statusCode, errMsg := h.AuthenticateManagementKey(ip, false, "wrong-secret")
		if allowed {
			t.Fatalf("expected auth to be denied at attempt %d", i+1)
		}
		if statusCode != http.StatusUnauthorized || errMsg != "invalid management key" {
			t.Fatalf("unexpected auth failure at attempt %d: status=%d msg=%q", i+1, statusCode, errMsg)
		}
	}

	allowed, statusCode, errMsg := h.AuthenticateManagementKey(ip, false, "test-secret")
	if allowed {
		t.Fatalf("expected correct key to be denied while banned")
	}
	if statusCode != http.StatusForbidden {
		t.Fatalf("expected forbidden status while banned, got %d", statusCode)
	}
	if !strings.HasPrefix(errMsg, "IP banned due to too many failed attempts. Try again in") {
		t.Fatalf("unexpected banned message: %q", errMsg)
	}
}
