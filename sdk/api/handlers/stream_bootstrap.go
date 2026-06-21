package handlers

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

type StreamBootstrapOptions struct {
	// KeepAliveInterval overrides the configured streaming keep-alive interval.
	// If nil, the configured default is used. If set to <= 0, keep-alives are disabled.
	KeepAliveInterval *time.Duration

	// Commit writes streaming headers before the first heartbeat is emitted.
	Commit func()

	// WriteKeepAlive writes a single heartbeat. It should not flush.
	WriteKeepAlive func()
}

type StreamBootstrapResult struct {
	Chunk     []byte
	HasChunk  bool
	Closed    bool
	Err       *interfaces.ErrorMessage
	HasError  bool
	Committed bool
	Canceled  bool
}

// AwaitFirstStreamChunk waits for the first payload while keeping SSE clients alive.
// It preserves the pre-stream error path until a heartbeat has committed headers.
func (h *BaseAPIHandler) AwaitFirstStreamChunk(c *gin.Context, flusher http.Flusher, data <-chan []byte, errs <-chan *interfaces.ErrorMessage, opts StreamBootstrapOptions) StreamBootstrapResult {
	var result StreamBootstrapResult
	if c == nil {
		result.Canceled = true
		return result
	}

	commit := opts.Commit
	if commit == nil {
		commit = func() {}
	}
	writeKeepAlive := opts.WriteKeepAlive
	if writeKeepAlive == nil {
		writeKeepAlive = func() {
			_, _ = c.Writer.Write([]byte(": keep-alive\n\n"))
		}
	}

	var cfg *config.SDKConfig
	if h != nil {
		cfg = h.Cfg
	}
	keepAliveInterval := StreamingKeepAliveInterval(cfg)
	if opts.KeepAliveInterval != nil {
		keepAliveInterval = *opts.KeepAliveInterval
	}
	var keepAlive *time.Ticker
	var keepAliveC <-chan time.Time
	if keepAliveInterval > 0 {
		keepAlive = time.NewTicker(keepAliveInterval)
		defer keepAlive.Stop()
		keepAliveC = keepAlive.C
	}

	done := c.Request.Context().Done()
	for {
		select {
		case <-done:
			result.Canceled = true
			return result
		case errMsg, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			result.Err = errMsg
			result.HasError = true
			return result
		case chunk, ok := <-data:
			if !ok {
				result.Closed = true
				return result
			}
			result.Chunk = chunk
			result.HasChunk = true
			return result
		case <-keepAliveC:
			if !result.Committed {
				commit()
				result.Committed = true
			}
			writeKeepAlive()
			flusher.Flush()
		}
	}
}
