package middleware

import (
	"bytes"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
)

func TestExtractRequestBodyPrefersOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	wrapper := &ResponseWriterWrapper{
		requestInfo: &RequestInfo{Body: []byte("original-body")},
	}

	body := wrapper.extractRequestBody(c)
	if string(body) != "original-body" {
		t.Fatalf("request body = %q, want %q", string(body), "original-body")
	}

	c.Set(requestBodyOverrideContextKey, []byte("override-body"))
	body = wrapper.extractRequestBody(c)
	if string(body) != "override-body" {
		t.Fatalf("request body = %q, want %q", string(body), "override-body")
	}
}

func TestExtractRequestBodySupportsStringOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	wrapper := &ResponseWriterWrapper{body: &bytes.Buffer{}}
	c.Set(requestBodyOverrideContextKey, "override-as-string")

	body := wrapper.extractRequestBody(c)
	if string(body) != "override-as-string" {
		t.Fatalf("request body = %q, want %q", string(body), "override-as-string")
	}
}

func TestExtractResponseBodyPrefersOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	wrapper := &ResponseWriterWrapper{body: &bytes.Buffer{}}
	wrapper.body.WriteString("original-response")

	body := wrapper.extractResponseBody(c)
	if string(body) != "original-response" {
		t.Fatalf("response body = %q, want %q", string(body), "original-response")
	}

	c.Set(responseBodyOverrideContextKey, []byte("override-response"))
	body = wrapper.extractResponseBody(c)
	if string(body) != "override-response" {
		t.Fatalf("response body = %q, want %q", string(body), "override-response")
	}

	body[0] = 'X'
	if got := wrapper.extractResponseBody(c); string(got) != "override-response" {
		t.Fatalf("response override should be cloned, got %q", string(got))
	}
}

func TestExtractResponseBodySupportsStringOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	wrapper := &ResponseWriterWrapper{}
	c.Set(responseBodyOverrideContextKey, "override-response-as-string")

	body := wrapper.extractResponseBody(c)
	if string(body) != "override-response-as-string" {
		t.Fatalf("response body = %q, want %q", string(body), "override-response-as-string")
	}
}

func TestExtractBodyOverrideClonesBytes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	override := []byte("body-override")
	c.Set(requestBodyOverrideContextKey, override)

	body := extractBodyOverride(c, requestBodyOverrideContextKey)
	if !bytes.Equal(body, override) {
		t.Fatalf("body override = %q, want %q", string(body), string(override))
	}

	body[0] = 'X'
	if !bytes.Equal(override, []byte("body-override")) {
		t.Fatalf("override mutated: %q", string(override))
	}
}

func TestExtractWebsocketTimelineUsesOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	wrapper := &ResponseWriterWrapper{}
	if got := wrapper.extractWebsocketTimeline(c); got != nil {
		t.Fatalf("expected nil websocket timeline, got %q", string(got))
	}

	c.Set(websocketTimelineOverrideContextKey, []byte("timeline"))
	body := wrapper.extractWebsocketTimeline(c)
	if string(body) != "timeline" {
		t.Fatalf("websocket timeline = %q, want %q", string(body), "timeline")
	}
}

func TestFinalizeStreamingWritesAPIWebsocketTimeline(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	streamWriter := &testStreamingLogWriter{}
	wrapper := &ResponseWriterWrapper{
		ResponseWriter: c.Writer,
		logger:         &testRequestLogger{enabled: true},
		requestInfo: &RequestInfo{
			URL:       "/v1/responses",
			Method:    "POST",
			Headers:   map[string][]string{"Content-Type": {"application/json"}},
			RequestID: "req-1",
			Timestamp: time.Date(2026, time.April, 1, 12, 0, 0, 0, time.UTC),
		},
		isStreaming:  true,
		streamWriter: streamWriter,
	}

	c.Set("API_WEBSOCKET_TIMELINE", []byte("Timestamp: 2026-04-01T12:00:00Z\nEvent: api.websocket.request\n{}"))

	if err := wrapper.Finalize(c); err != nil {
		t.Fatalf("Finalize error: %v", err)
	}
	if string(streamWriter.apiWebsocketTimeline) != "Timestamp: 2026-04-01T12:00:00Z\nEvent: api.websocket.request\n{}" {
		t.Fatalf("stream writer websocket timeline = %q", string(streamWriter.apiWebsocketTimeline))
	}
	if !streamWriter.closed {
		t.Fatal("expected stream writer to be closed")
	}
}

func TestDetectClaudeCodeToolFailure(t *testing.T) {
	tests := []struct {
		name string
		body []byte
		want string
	}{
		{
			name: "no such tool",
			body: []byte(`{"content":"No such tool available: bash"}`),
			want: "no_such_tool_available",
		},
		{
			name: "tool selection syntax",
			body: []byte(`Those tool calls failed — the tool selection syntax is wrong.`),
			want: "tool_selection_syntax_is_wrong",
		},
		{
			name: "unrelated",
			body: []byte(`{"content":"done"}`),
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectClaudeCodeToolFailure(tt.body); got != tt.want {
				t.Fatalf("detectClaudeCodeToolFailure() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractModelFromRequestBody(t *testing.T) {
	body := []byte(`{"messages":[],"model":"deepseek-v4-flash","tools":[{"name":"Bash"}]}`)
	if got := extractModelFromRequestBody(body); got != "deepseek-v4-flash" {
		t.Fatalf("extractModelFromRequestBody() = %q, want %q", got, "deepseek-v4-flash")
	}

	if got := extractModelFromRequestBody([]byte(`{"messages":[]}`)); got != "" {
		t.Fatalf("extractModelFromRequestBody() = %q, want empty", got)
	}
}

func TestFinalizeForcesRequestLogForResponsesProtocolAnomaly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	logger := &testRequestLogger{enabled: false}
	wrapper := NewResponseWriterWrapper(c.Writer, logger, &RequestInfo{
		URL:       "/v1/responses",
		Method:    "POST",
		Headers:   map[string][]string{"Content-Type": {"application/json"}},
		Body:      []byte(`{"model":"gpt-5","input":"hello"}`),
		RequestID: "deadbeef",
		Timestamp: time.Date(2026, time.June, 20, 11, 26, 2, 0, time.UTC),
	})
	wrapper.logOnErrorOnly = true
	c.Writer = wrapper

	c.Set(responsesProtocolAnomalyContextKey, "max_output_tokens")
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Status(200)
	_, _ = c.Writer.Write([]byte(`data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","id":"msg-1"}],"incomplete_details":null}}`))

	if err := wrapper.Finalize(c); err != nil {
		t.Fatalf("Finalize error: %v", err)
	}
	if logger.logRequestCalls != 1 {
		t.Fatalf("log request calls = %d, want 1", logger.logRequestCalls)
	}
	if !logger.force {
		t.Fatal("expected forced request log")
	}
	if logger.statusCode != 200 {
		t.Fatalf("status code = %d, want 200", logger.statusCode)
	}
	if !bytes.Contains(logger.requestBody, []byte(`"model":"gpt-5"`)) {
		t.Fatalf("request body not captured: %s", logger.requestBody)
	}
	if !bytes.Contains(logger.responseBody, []byte(`response.completed`)) {
		t.Fatalf("response body not captured: %s", logger.responseBody)
	}
}

type testRequestLogger struct {
	enabled         bool
	force           bool
	requestBody     []byte
	responseBody    []byte
	statusCode      int
	logRequestCalls int
}

func (l *testRequestLogger) LogRequest(string, string, map[string][]string, []byte, int, map[string][]string, []byte, []byte, []byte, []byte, []byte, []*interfaces.ErrorMessage, string, time.Time, time.Time) error {
	l.logRequestCalls++
	return nil
}

func (l *testRequestLogger) LogRequestWithOptions(_ string, _ string, _ map[string][]string, requestBody []byte, statusCode int, _ map[string][]string, response []byte, _ []byte, _ []byte, _ []byte, _ []byte, _ []*interfaces.ErrorMessage, force bool, _ string, _ time.Time, _ time.Time) error {
	l.logRequestCalls++
	l.force = force
	l.requestBody = bytes.Clone(requestBody)
	l.responseBody = bytes.Clone(response)
	l.statusCode = statusCode
	return nil
}

func (l *testRequestLogger) LogStreamingRequest(string, string, map[string][]string, []byte, string) (logging.StreamingLogWriter, error) {
	return &testStreamingLogWriter{}, nil
}

func (l *testRequestLogger) IsEnabled() bool {
	return l.enabled
}

type testStreamingLogWriter struct {
	apiWebsocketTimeline []byte
	closed               bool
}

func (w *testStreamingLogWriter) WriteChunkAsync([]byte) {}

func (w *testStreamingLogWriter) WriteStatus(int, map[string][]string) error {
	return nil
}

func (w *testStreamingLogWriter) WriteAPIRequest([]byte) error {
	return nil
}

func (w *testStreamingLogWriter) WriteAPIResponse([]byte) error {
	return nil
}

func (w *testStreamingLogWriter) WriteAPIWebsocketTimeline(apiWebsocketTimeline []byte) error {
	w.apiWebsocketTimeline = bytes.Clone(apiWebsocketTimeline)
	return nil
}

func (w *testStreamingLogWriter) SetFirstChunkTimestamp(time.Time) {}

func (w *testStreamingLogWriter) Close() error {
	w.closed = true
	return nil
}
