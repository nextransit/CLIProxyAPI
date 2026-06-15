package auth

import (
	"context"
	"net/http"
	"sync"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// recordingExecutor records the most recent auth ID handed to it on each
// Execute call. Used by weighted-round-robin tests to confirm that the
// scheduler actually distributes picks by weight.
type recordingExecutor struct {
	id     string
	mu     sync.Mutex
	counts map[string]int
}

func newRecordingExecutor(id string) *recordingExecutor {
	return &recordingExecutor{id: id, counts: map[string]int{}}
}

func (e *recordingExecutor) Identifier() string { return e.id }
func (e *recordingExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if auth != nil {
		e.counts[auth.ID]++
	}
	return cliproxyexecutor.Response{}, nil
}
func (e *recordingExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return &cliproxyexecutor.StreamResult{}, nil
}
func (e *recordingExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}
func (e *recordingExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *recordingExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}
