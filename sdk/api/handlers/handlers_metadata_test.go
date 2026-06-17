package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	"golang.org/x/net/context"
)

func TestRequestExecutionMetadataIncludesExecutionSessionWithoutIdempotencyKey(t *testing.T) {
	ctx := WithExecutionSessionID(context.Background(), "session-1")

	meta := requestExecutionMetadata(ctx)
	if got := meta[coreexecutor.ExecutionSessionMetadataKey]; got != "session-1" {
		t.Fatalf("ExecutionSessionMetadataKey = %v, want %q", got, "session-1")
	}
	if _, ok := meta[idempotencyKeyMetadataKey]; ok {
		t.Fatalf("unexpected idempotency key in metadata: %v", meta[idempotencyKeyMetadataKey])
	}
}

func TestRequestExecutionHeadersClonesGinRequestHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("X-Session-ID", "session-123")
	ginCtx.Request = req

	headers := requestExecutionHeaders(context.WithValue(context.Background(), "gin", ginCtx))
	if got := headers.Get("X-Session-ID"); got != "session-123" {
		t.Fatalf("X-Session-ID = %q, want session-123", got)
	}
	req.Header.Set("X-Session-ID", "mutated")
	if got := headers.Get("X-Session-ID"); got != "session-123" {
		t.Fatalf("headers were not cloned, got %q", got)
	}
}
