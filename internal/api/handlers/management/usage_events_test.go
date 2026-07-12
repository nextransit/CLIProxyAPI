package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

func TestUsageEventsRequiresAuthHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/v0/management/usage/events", UsageEventsHandler(usage.GetRequestStatistics().Broker()))

	req := httptest.NewRequest(http.MethodGet, "/v0/management/usage/events", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

// flushRecorder wraps httptest.ResponseRecorder and adds Flush() so
// http.Flusher assertions succeed in SSE handlers.
type flushRecorder struct {
	*httptest.ResponseRecorder
}

func (f *flushRecorder) Flush() {}

func TestUsageEventsDeliversSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/v0/management/usage/events", UsageEventsHandler(usage.GetRequestStatistics().Broker()))

	ctx, cancelCtx := context.WithCancel(context.Background())
	defer cancelCtx()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/v0/management/usage/events", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}

	done := make(chan struct{})
	go func() {
		r.ServeHTTP(w, req)
		close(done)
	}()

	// Wait for handler to subscribe.
	time.Sleep(50 * time.Millisecond)
	usage.GetRequestStatistics().Broker().Publish(usage.UsagePayload{TotalRequests: 7})

	// Production broker debounces 800ms; wait long enough for flush,
	// then cancel the request context to unblock the handler.
	time.Sleep(time.Second)
	cancelCtx()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not exit within timeout")
	}

	body := w.Body.String()
	if !strings.Contains(body, "event: snapshot") {
		t.Fatalf("body missing snapshot event:\n%s", body)
	}
	if !strings.Contains(body, `"total_requests":7`) {
		t.Fatalf("body missing payload value:\n%s", body)
	}
}
