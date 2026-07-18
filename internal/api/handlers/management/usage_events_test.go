package management

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

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
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		r.ServeHTTP(rec, req)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)

	// Inject an event via RecordFromTest.
	stats.RecordFromTest(usage.UsageEvent{ID: 1, Model: "gpt-4o"})

	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}

	body := rec.Body.String()
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
	rec := httptest.NewRecorder()

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
		body := rec.Body.String()
		if strings.Contains(body, `"id":4`) && strings.Contains(body, `"id":5`) {
			// Also verify summary contains latest_event_id
			if strings.Contains(body, "latest_event_id") {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("expected replay of ids 4 and 5, got %q", rec.Body.String())
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