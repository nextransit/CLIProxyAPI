package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
)

func TestAwaitFirstStreamChunk_EmitsKeepAliveBeforeFirstChunk(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/stream", nil)

	data := make(chan []byte, 1)
	errs := make(chan *interfaces.ErrorMessage)
	go func() {
		time.Sleep(30 * time.Millisecond)
		data <- []byte("ok")
	}()

	interval := 5 * time.Millisecond
	handler := &BaseAPIHandler{}
	result := handler.AwaitFirstStreamChunk(c, w, data, errs, StreamBootstrapOptions{
		KeepAliveInterval: &interval,
		Commit: func() {
			c.Header("Content-Type", "text/event-stream")
		},
	})

	if !result.Committed {
		t.Fatal("expected heartbeat to commit SSE headers before first chunk")
	}
	if !result.HasChunk || string(result.Chunk) != "ok" {
		t.Fatalf("expected first chunk ok, got has=%v chunk=%q", result.HasChunk, string(result.Chunk))
	}
	if got := w.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
	if body := w.Body.String(); !strings.Contains(body, ": keep-alive\n\n") {
		t.Fatalf("expected keep-alive heartbeat in body, got %q", body)
	}
}
