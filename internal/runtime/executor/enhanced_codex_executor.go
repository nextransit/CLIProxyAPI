package executor

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type EnhancedCodexExecutor struct {
	*CodexExecutor
	errorHandler *AuthErrorHandler
}

func NewEnhancedCodexExecutor(cfg *config.Config) *EnhancedCodexExecutor {
	base := NewCodexExecutor(cfg)
	return &EnhancedCodexExecutor{
		CodexExecutor: base,
		errorHandler:  NewAuthErrorHandler("codex"),
	}
}

func (e *EnhancedCodexExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	resp, err := e.CodexExecutor.Execute(ctx, auth, req, opts)

	if err != nil && auth != nil {
		if sErr, ok := err.(statusErr); ok {
			e.errorHandler.HandleAuthError(err, sErr.code, []byte(sErr.msg), auth)
		}
	}

	return resp, err
}

func (e *EnhancedCodexExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	result, err := e.CodexExecutor.ExecuteStream(ctx, auth, req, opts)

	if err != nil && auth != nil {
		if sErr, ok := err.(statusErr); ok {
			e.errorHandler.HandleAuthError(err, sErr.code, []byte(sErr.msg), auth)
		}
	}

	return result, err
}

func (e *EnhancedCodexExecutor) ShouldAutoDisable(err error) bool {
	if sErr, ok := err.(statusErr); ok {
		return e.errorHandler.isKeyDisabledError(sErr.code, sErr.msg)
	}
	return false
}

func (e *EnhancedCodexExecutor) GetDisabledKeys() []string {
	return []string{}
}
