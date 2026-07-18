package management

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

const (
	sseHeartbeatInterval = 15 * time.Second
)

// jsonMarshal is overridable in tests to inject marshal failures.
var jsonMarshal = func(v any) ([]byte, error) {
	return json.Marshal(v)
}

// UsageEventsHandler returns an SSE stream of usage snapshots.
// Auth is enforced by checking the Authorization header (the real management
// middleware in server.go does the full check; this is a defensive fallback
// so the handler is safe to register directly in tests or behind a different
// auth scheme).
func UsageEventsHandler(broker *usage.Broker) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetHeader("Authorization") == "" {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}

		ch, cancel := broker.Subscribe()
		defer cancel()

		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.Header().Set("Cache-Control", "no-cache")
		c.Writer.Header().Set("Connection", "keep-alive")
		c.Writer.Header().Set("X-Accel-Buffering", "no")
		c.Writer.WriteHeader(http.StatusOK)

		flusher, ok := c.Writer.(http.Flusher)
		if !ok {
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}

		ticker := time.NewTicker(sseHeartbeatInterval)
		defer ticker.Stop()

		ctx := c.Request.Context()
		for {
			select {
			case <-ctx.Done():
				return
			case payload, ok := <-ch:
				if !ok {
					return
				}
				writeSSEEvent(c.Writer, "snapshot", payload)
				flusher.Flush()
			case t := <-ticker.C:
				fmt.Fprintf(c.Writer, "event: heartbeat\ndata: {\"ts\":%q}\n\n", t.UTC().Format(time.RFC3339))
				flusher.Flush()
			}
		}
	}
}

func writeSSEEvent(w http.ResponseWriter, event string, payload usage.UsageEvent) {
	body, err := jsonMarshal(payload)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body)
}