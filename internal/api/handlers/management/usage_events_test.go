package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

type synchronizedRecorder struct {
	mu sync.Mutex
	*httptest.ResponseRecorder
}

func newSynchronizedRecorder() *synchronizedRecorder {
	return &synchronizedRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (r *synchronizedRecorder) WriteHeader(statusCode int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ResponseRecorder.WriteHeader(statusCode)
}

func (r *synchronizedRecorder) Write(data []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ResponseRecorder.Write(data)
}

func (r *synchronizedRecorder) Flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ResponseRecorder.Flush()
}

func (r *synchronizedRecorder) BodyString() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Body.String()
}

func setupRouter(t *testing.T) (*gin.Engine, *usage.RequestStatistics) {
	gin.SetMode(gin.TestMode)
	stats := usage.NewRequestStatistics()
	r := gin.New()
	r.GET("/v0/management/usage/events", UsageEventsHandler(stats.Broker(), stats))
	return r, stats
}

func TestUsageEvents_SendsSummaryAndUsageEvent(t *testing.T) {
	r, stats := setupRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/usage/events", nil)
	req.Header.Set("Authorization", "Bearer test")
	requestCtx, cancelRequest := context.WithCancel(req.Context())
	req = req.WithContext(requestCtx)
	rec := newSynchronizedRecorder()

	done := make(chan struct{})
	go func() {
		r.ServeHTTP(rec, req)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)

	// Inject an event via RecordFromTest.
	stats.RecordFromTest(usage.UsageEvent{ID: 1, Model: "gpt-4o"})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		body := rec.BodyString()
		if strings.Contains(body, "event: usage_event") && strings.Contains(body, `"id":1`) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancelRequest()
	<-done

	body := rec.BodyString()
	if !strings.Contains(body, "event: summary") {
		t.Errorf("missing summary event, body:\n%s", body)
	}
	if !strings.Contains(body, "event: usage_event") {
		t.Errorf("missing usage_event, body:\n%s", body)
	}
	if !strings.Contains(body, `"id":1`) {
		t.Errorf("missing event id in payload, body:\n%s", body)
	}
}

func TestUsageEvents_LastEventIDReplays(t *testing.T) {
	r, stats := setupRouter(t)

	for i := uint64(1); i <= 5; i++ {
		stats.RecordFromTest(usage.UsageEvent{ID: i})
	}

	req := httptest.NewRequest(http.MethodGet, "/v0/management/usage/events", nil)
	req.Header.Set("Authorization", "Bearer test")
	req.Header.Set("Last-Event-ID", "3")
	requestCtx, cancelRequest := context.WithCancel(req.Context())
	req = req.WithContext(requestCtx)
	rec := newSynchronizedRecorder()

	done := make(chan struct{})
	go func() {
		r.ServeHTTP(rec, req)
		close(done)
	}()

	deadline := time.After(500 * time.Millisecond)
loop:
	for {
		select {
		case <-deadline:
			break loop
		default:
		}
		body := rec.BodyString()
		if strings.Contains(body, `"id":4`) && strings.Contains(body, `"id":5`) {
			// Also verify summary contains latest_event_id
			if strings.Contains(body, "latest_event_id") {
				cancelRequest()
				<-done
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancelRequest()
	<-done
	t.Errorf("expected replay of ids 4 and 5, got %q", rec.BodyString())
}

func TestUsageEvents_RequiresAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stats := usage.NewRequestStatistics()
	r := gin.New()
	r.GET("/v0/management/usage/events", UsageEventsHandler(stats.Broker(), stats))

	req := httptest.NewRequest(http.MethodGet, "/v0/management/usage/events", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}
