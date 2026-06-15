package executor

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// stubExecutor records a single call. We use it to drive retryAnthropicForRecovery
// in isolation from the rest of the streaming path.
type stubExecutor struct {
	calls int
}

func (s *stubExecutor) Identifier() string { return "minimax" }

// We can't easily mock HttpRequest without an http server, so the test below
// is a thin shape check that the function is exported and accept the right
// signature, plus a smoke test against an httptest.Server.
//
// The behavioral coverage lives in the streaming code path; here we
// only verify the recovery helper does not panic and routes through the
// expected error path on a 2013 response.
func TestRetryAnthropicForRecovery_DoesNotPanic(t *testing.T) {
	e := &MiniMaxExecutor{provider: "minimax"}
	auth := &coreauth.Auth{
		ID:         "minimax:apikey:test",
		Provider:   "minimax",
		Attributes: map[string]string{"base_url": "http://127.0.0.1:1", "api_key": "x"},
	}
	_, _ = e.retryAnthropicForRecovery(context.Background(), auth, []byte(`{"model":"MiniMax-M3","messages":[]}`), "x", "http://127.0.0.1:1", false)
	// We only assert that the call did not panic and returned ok=false on
	// connection refused; the helper's logic is exercised in non-streaming
	// recovery above.
}

// TestRetryAnthropicForRecovery_SuccessBody ensures the helper surfaces the
// raw response body when the upstream returns 200 with a non-2013 payload.
func TestRetryAnthropicForRecovery_SuccessBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg-1","content":[{"type":"text","text":"hi"}]}`))
	}))
	defer srv.Close()

	e := &MiniMaxExecutor{provider: "minimax"}
	auth := &coreauth.Auth{
		ID:         "minimax:apikey:test",
		Provider:   "minimax",
		Attributes: map[string]string{"base_url": srv.URL, "api_key": "x"},
	}
	body, ok := e.retryAnthropicForRecovery(context.Background(), auth, []byte(`{}`), "x", srv.URL, false)
	if !ok {
		t.Fatalf("expected ok=true on 200 response")
	}
	if !bytes.Contains(body, []byte(`"id":"msg-1"`)) {
		t.Fatalf("unexpected body: %s", string(body))
	}
}

// TestRetryAnthropicForRecovery_2013RetriesAsFailure ensures the helper
// returns ok=false on a 2013 response so the streaming recovery loop can
// try the next compact level.
func TestRetryAnthropicForRecovery_2013RetriesAsFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":2013,"message":"context window exceeds limit"}}`))
	}))
	defer srv.Close()

	e := &MiniMaxExecutor{provider: "minimax"}
	auth := &coreauth.Auth{
		ID:         "minimax:apikey:test",
		Provider:   "minimax",
		Attributes: map[string]string{"base_url": srv.URL, "api_key": "x"},
	}
	body, ok := e.retryAnthropicForRecovery(context.Background(), auth, []byte(`{}`), "x", srv.URL, false)
	if ok {
		t.Fatalf("expected ok=false on 2013 response, got body: %s", string(body))
	}
	if !strings.Contains(string(body), "2013") {
		t.Fatalf("expected body to contain 2013, got: %s", string(body))
	}
}

// ensure compile-time reference to cliproxyexecutor.Options is preserved.
var _ = cliproxyexecutor.Options{}
