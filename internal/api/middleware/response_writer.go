// Package middleware provides Gin HTTP middleware for the CLI Proxy API server.
// It includes a sophisticated response writer wrapper designed to capture and log request and response data,
// including support for streaming responses, without impacting latency.
package middleware

import (
	"bytes"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
	log "github.com/sirupsen/logrus"
)

const requestBodyOverrideContextKey = "REQUEST_BODY_OVERRIDE"
const responseBodyOverrideContextKey = "RESPONSE_BODY_OVERRIDE"
const websocketTimelineOverrideContextKey = "WEBSOCKET_TIMELINE_OVERRIDE"
const responsesProtocolAnomalyContextKey = "RESPONSES_PROTOCOL_ANOMALY"

// maxResponseBodyBytes caps the in-memory response body buffer to 1 MiB
// to prevent unbounded allocation when logging is active (commercial-mode: false).
const maxResponseBodyBytes = 1 << 20

// maxRequestBodyBytes caps the request body captured for logging at 1 MiB.
const maxRequestBodyBytes = 1 << 20

// maxAnomalyResponseBodyBytes caps response text retained only for lightweight anomaly detection.
const maxAnomalyResponseBodyBytes = 64 * 1024

var claudeCodeToolFailureMarkers = []string{
	"no such tool available",
	"tool selection syntax is wrong",
	"those tool calls failed",
	"tool names got corrupted",
}

// RequestInfo holds essential details of an incoming HTTP request for logging purposes.
type RequestInfo struct {
	URL       string              // URL is the request URL.
	Method    string              // Method is the HTTP method (e.g., GET, POST).
	Headers   map[string][]string // Headers contains the request headers.
	Body      []byte              // Body is the raw request body.
	RequestID string              // RequestID is the unique identifier for the request.
	Timestamp time.Time           // Timestamp is when the request was received.
}

// ResponseWriterWrapper wraps the standard gin.ResponseWriter to intercept and log response data.
// It is designed to handle both standard and streaming responses, ensuring that logging operations do not block the client response.
type ResponseWriterWrapper struct {
	gin.ResponseWriter
	body                *bytes.Buffer              // body is a buffer to store the response body for non-streaming responses.
	isStreaming         bool                       // isStreaming indicates whether the response is a streaming type (e.g., text/event-stream).
	streamWriter        logging.StreamingLogWriter // streamWriter is a writer for handling streaming log entries.
	chunkChannel        chan []byte                // chunkChannel is a channel for asynchronously passing response chunks to the logger.
	streamDone          chan struct{}              // streamDone signals when the streaming goroutine completes.
	logger              logging.RequestLogger      // logger is the instance of the request logger service.
	requestInfo         *RequestInfo               // requestInfo holds the details of the original request.
	statusCode          int                        // statusCode stores the HTTP status code of the response.
	headers             map[string][]string        // headers stores the response headers.
	logOnErrorOnly      bool                       // logOnErrorOnly enables logging only when an error response is detected.
	firstChunkTimestamp time.Time                  // firstChunkTimestamp captures TTFB for streaming responses.
	anomalyBody         *bytes.Buffer              // anomalyBody stores a small response prefix for protocol-failure detection.
}

// NewResponseWriterWrapper creates and initializes a new ResponseWriterWrapper.
// It takes the original gin.ResponseWriter, a logger instance, and request information.
//
// Parameters:
//   - w: The original gin.ResponseWriter to wrap.
//   - logger: The logging service to use for recording requests.
//   - requestInfo: The pre-captured information about the incoming request.
//
// Returns:
//   - A pointer to a new ResponseWriterWrapper.
func NewResponseWriterWrapper(w gin.ResponseWriter, logger logging.RequestLogger, requestInfo *RequestInfo) *ResponseWriterWrapper {
	return &ResponseWriterWrapper{
		ResponseWriter: w,
		body:           &bytes.Buffer{},
		logger:         logger,
		requestInfo:    requestInfo,
		headers:        make(map[string][]string),
	}
}

// Write wraps the underlying ResponseWriter's Write method to capture response data.
// For non-streaming responses, it writes to an internal buffer. For streaming responses,
// it sends data chunks to a non-blocking channel for asynchronous logging.
// CRITICAL: This method prioritizes writing to the client to ensure zero latency,
// handling logging operations subsequently.
func (w *ResponseWriterWrapper) Write(data []byte) (int, error) {
	// Ensure headers are captured before first write
	// This is critical because Write() may trigger WriteHeader() internally
	w.ensureHeadersCaptured()

	// CRITICAL: Write to client first (zero latency)
	n, err := w.ResponseWriter.Write(data)

	// THEN: Handle logging based on response type
	w.captureAnomalyResponse(data)
	if w.isStreaming && w.chunkChannel != nil {
		// Capture TTFB on first chunk (synchronous, before async channel send)
		if w.firstChunkTimestamp.IsZero() {
			w.firstChunkTimestamp = time.Now()
		}
		// For streaming responses: Send to async logging channel (non-blocking)
		select {
		case w.chunkChannel <- append([]byte(nil), data...): // Non-blocking send with copy
		default: // Channel full, skip logging to avoid blocking
		}
		return n, err
	}

	if w.shouldBufferResponseBody() {
		if w.body.Len() < maxResponseBodyBytes {
			remaining := maxResponseBodyBytes - w.body.Len()
			if len(data) > remaining {
				w.body.Write(data[:remaining])
			} else {
				w.body.Write(data)
			}
		}
	}

	return n, err
}

func (w *ResponseWriterWrapper) shouldBufferResponseBody() bool {
	if w.logger != nil && w.logger.IsEnabled() {
		return true
	}
	if !w.logOnErrorOnly {
		return false
	}
	status := w.statusCode
	if status == 0 {
		if statusWriter, ok := w.ResponseWriter.(interface{ Status() int }); ok && statusWriter != nil {
			status = statusWriter.Status()
		} else {
			status = http.StatusOK
		}
	}
	return status >= http.StatusBadRequest
}

// WriteString wraps the underlying ResponseWriter's WriteString method to capture response data.
// Some handlers (and fmt/io helpers) write via io.StringWriter; without this override, those writes
// bypass Write() and would be missing from request logs.
func (w *ResponseWriterWrapper) WriteString(data string) (int, error) {
	w.ensureHeadersCaptured()

	// CRITICAL: Write to client first (zero latency)
	n, err := w.ResponseWriter.WriteString(data)

	// THEN: Capture for logging
	w.captureAnomalyResponse([]byte(data))
	if w.isStreaming && w.chunkChannel != nil {
		// Capture TTFB on first chunk (synchronous, before async channel send)
		if w.firstChunkTimestamp.IsZero() {
			w.firstChunkTimestamp = time.Now()
		}
		select {
		case w.chunkChannel <- []byte(data):
		default:
		}
		return n, err
	}

	if w.shouldBufferResponseBody() {
		if w.body.Len() < maxResponseBodyBytes {
			remaining := maxResponseBodyBytes - w.body.Len()
			if len(data) > remaining {
				w.body.WriteString(data[:remaining])
			} else {
				w.body.WriteString(data)
			}
		}
	}
	return n, err
}

// WriteHeader wraps the underlying ResponseWriter's WriteHeader method.
// It captures the status code, detects if the response is streaming based on the Content-Type header,
// and initializes the appropriate logging mechanism (standard or streaming).
func (w *ResponseWriterWrapper) WriteHeader(statusCode int) {
	w.statusCode = statusCode

	// Capture response headers using the new method
	w.captureCurrentHeaders()

	// Detect streaming based on Content-Type
	contentType := w.ResponseWriter.Header().Get("Content-Type")
	w.isStreaming = w.detectStreaming(contentType)

	// If streaming, initialize streaming log writer
	if w.isStreaming && w.logger.IsEnabled() {
		streamWriter, err := w.logger.LogStreamingRequest(
			w.requestInfo.URL,
			w.requestInfo.Method,
			w.requestInfo.Headers,
			w.requestInfo.Body,
			w.requestInfo.RequestID,
		)
		if err == nil {
			w.streamWriter = streamWriter
			w.chunkChannel = make(chan []byte, 100) // Buffered channel for async writes
			doneChan := make(chan struct{})
			w.streamDone = doneChan

			// Start async chunk processor
			go w.processStreamingChunks(doneChan)

			// Write status immediately
			_ = streamWriter.WriteStatus(statusCode, w.headers)
		}
	}

	// Call original WriteHeader
	w.ResponseWriter.WriteHeader(statusCode)
}

// ensureHeadersCaptured is a helper function to make sure response headers are captured.
// It is safe to call this method multiple times; it will always refresh the headers
// with the latest state from the underlying ResponseWriter.
func (w *ResponseWriterWrapper) ensureHeadersCaptured() {
	// Always capture the current headers to ensure we have the latest state
	w.captureCurrentHeaders()
}

// captureCurrentHeaders reads all headers from the underlying ResponseWriter and stores them
// in the wrapper's headers map. It creates copies of the header values to prevent race conditions.
func (w *ResponseWriterWrapper) captureCurrentHeaders() {
	// Initialize headers map if needed
	if w.headers == nil {
		w.headers = make(map[string][]string)
	}

	// Capture all current headers from the underlying ResponseWriter
	for key, values := range w.ResponseWriter.Header() {
		// Make a copy of the values slice to avoid reference issues
		headerValues := make([]string, len(values))
		copy(headerValues, values)
		w.headers[key] = headerValues
	}
}

// detectStreaming determines if a response should be treated as a streaming response.
// It checks for a "text/event-stream" Content-Type or a '"stream": true'
// field in the original request body.
func (w *ResponseWriterWrapper) detectStreaming(contentType string) bool {
	// Check Content-Type for Server-Sent Events
	if strings.Contains(contentType, "text/event-stream") {
		return true
	}

	// If a concrete Content-Type is already set (e.g., application/json for error responses),
	// treat it as non-streaming instead of inferring from the request payload.
	if strings.TrimSpace(contentType) != "" {
		return false
	}

	// Only fall back to request payload hints when Content-Type is not set yet.
	if w.requestInfo != nil && len(w.requestInfo.Body) > 0 {
		return bytes.Contains(w.requestInfo.Body, []byte(`"stream": true`)) ||
			bytes.Contains(w.requestInfo.Body, []byte(`"stream":true`))
	}

	return false
}

// processStreamingChunks runs in a separate goroutine to process response chunks from the chunkChannel.
// It asynchronously writes each chunk to the streaming log writer.
func (w *ResponseWriterWrapper) processStreamingChunks(done chan struct{}) {
	if done == nil {
		return
	}

	defer close(done)

	if w.streamWriter == nil || w.chunkChannel == nil {
		return
	}

	for chunk := range w.chunkChannel {
		w.streamWriter.WriteChunkAsync(chunk)
	}
}

// Finalize completes the logging process for the request and response.
// For streaming responses, it closes the chunk channel and the stream writer.
// For non-streaming responses, it logs the complete request and response details,
// including any API-specific request/response data stored in the Gin context.
func (w *ResponseWriterWrapper) Finalize(c *gin.Context) error {
	if w.logger == nil {
		return nil
	}

	finalStatusCode := w.statusCode
	if finalStatusCode == 0 {
		if statusWriter, ok := w.ResponseWriter.(interface{ Status() int }); ok {
			finalStatusCode = statusWriter.Status()
		} else {
			finalStatusCode = 200
		}
	}

	var slicesAPIResponseError []*interfaces.ErrorMessage
	apiResponseError, isExist := c.Get("API_RESPONSE_ERROR")
	if isExist {
		if apiErrors, ok := apiResponseError.([]*interfaces.ErrorMessage); ok {
			slicesAPIResponseError = apiErrors
		}
	}

	responsesProtocolAnomalyReason := extractResponsesProtocolAnomaly(c)
	hasAPIError := len(slicesAPIResponseError) > 0 || finalStatusCode >= http.StatusBadRequest || responsesProtocolAnomalyReason != ""
	w.logAPIErrorSummary(finalStatusCode, slicesAPIResponseError)
	w.logResponsesProtocolAnomalySummary(finalStatusCode, responsesProtocolAnomalyReason)
	w.logClaudeCodeToolFailureSummary(finalStatusCode)
	forceLog := w.logOnErrorOnly && hasAPIError && !w.logger.IsEnabled()
	if !w.logger.IsEnabled() && !forceLog {
		return nil
	}

	if w.isStreaming && w.streamWriter != nil {
		if w.chunkChannel != nil {
			close(w.chunkChannel)
			w.chunkChannel = nil
		}

		if w.streamDone != nil {
			<-w.streamDone
			w.streamDone = nil
		}

		w.streamWriter.SetFirstChunkTimestamp(w.firstChunkTimestamp)

		// Write API Request and Response to the streaming log before closing
		apiRequest := w.extractAPIRequest(c)
		if len(apiRequest) > 0 {
			_ = w.streamWriter.WriteAPIRequest(apiRequest)
		}
		apiResponse := w.extractAPIResponse(c)
		if len(apiResponse) > 0 {
			_ = w.streamWriter.WriteAPIResponse(apiResponse)
		}
		apiWebsocketTimeline := w.extractAPIWebsocketTimeline(c)
		if len(apiWebsocketTimeline) > 0 {
			_ = w.streamWriter.WriteAPIWebsocketTimeline(apiWebsocketTimeline)
		}
		if err := w.streamWriter.Close(); err != nil {
			w.streamWriter = nil
			return err
		}
		w.streamWriter = nil
		return nil
	}

	return w.logRequest(w.extractRequestBody(c), finalStatusCode, w.cloneHeaders(), w.extractResponseBody(c), w.extractWebsocketTimeline(c), w.extractAPIRequest(c), w.extractAPIResponse(c), w.extractAPIWebsocketTimeline(c), w.extractAPIResponseTimestamp(c), slicesAPIResponseError, forceLog)
}

func (w *ResponseWriterWrapper) captureAnomalyResponse(data []byte) {
	if len(data) == 0 || !w.shouldInspectResponseAnomalies() {
		return
	}
	if w.anomalyBody == nil {
		w.anomalyBody = &bytes.Buffer{}
	}
	if w.anomalyBody.Len() >= maxAnomalyResponseBodyBytes {
		return
	}
	remaining := maxAnomalyResponseBodyBytes - w.anomalyBody.Len()
	if len(data) > remaining {
		data = data[:remaining]
	}
	_, _ = w.anomalyBody.Write(data)
}

func (w *ResponseWriterWrapper) shouldInspectResponseAnomalies() bool {
	if w == nil || w.requestInfo == nil {
		return false
	}
	path := requestInfoPath(w.requestInfo.URL)
	if strings.HasPrefix(path, "/v1/responses") && w.logOnErrorOnly {
		return true
	}
	if !isAnomalyInspectablePath(path) {
		return false
	}
	body := w.requestInfo.Body
	if len(body) == 0 {
		return false
	}
	return bytes.Contains(body, []byte(`"tools"`)) || bytes.Contains(body, []byte(`"tool_choice"`))
}

func isAnomalyInspectablePath(path string) bool {
	path = strings.SplitN(path, "?", 2)[0]
	for _, prefix := range []string{"/v1/messages", "/v1/chat/completions", "/v1/responses"} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func (w *ResponseWriterWrapper) logAPIErrorSummary(statusCode int, apiResponseErrors []*interfaces.ErrorMessage) {
	if w == nil || w.requestInfo == nil {
		return
	}
	if statusCode < http.StatusBadRequest && len(apiResponseErrors) == 0 {
		return
	}
	method := strings.TrimSpace(w.requestInfo.Method)
	path := requestInfoPath(w.requestInfo.URL)
	if method == "" {
		method = "UNKNOWN"
	}
	if path == "" {
		path = "/"
	}
	log.WithField("request_id", w.requestInfo.RequestID).
		Errorf("request_failed | %s %s %d | request_log_id=%s%s",
			method,
			path,
			statusCode,
			w.requestInfo.RequestID,
			apiErrorSummarySuffix(apiResponseErrors),
		)
}

func (w *ResponseWriterWrapper) logResponsesProtocolAnomalySummary(statusCode int, reason string) {
	if w == nil || w.requestInfo == nil || strings.TrimSpace(reason) == "" {
		return
	}
	method := strings.TrimSpace(w.requestInfo.Method)
	path := requestInfoPath(w.requestInfo.URL)
	if method == "" {
		method = "UNKNOWN"
	}
	if path == "" {
		path = "/"
	}
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	log.WithField("request_id", w.requestInfo.RequestID).
		Errorf("response_incomplete | %s %s %d | reason=%s | request_log_id=%s",
			method,
			path,
			statusCode,
			reason,
			w.requestInfo.RequestID,
		)
}

func (w *ResponseWriterWrapper) logClaudeCodeToolFailureSummary(statusCode int) {
	if w == nil || w.requestInfo == nil || w.anomalyBody == nil || w.anomalyBody.Len() == 0 {
		return
	}
	reason := detectClaudeCodeToolFailure(w.anomalyBody.Bytes())
	if reason == "" {
		return
	}
	method := strings.TrimSpace(w.requestInfo.Method)
	path := requestInfoPath(w.requestInfo.URL)
	if method == "" {
		method = "UNKNOWN"
	}
	if path == "" {
		path = "/"
	}
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	model := extractModelFromRequestBody(w.requestInfo.Body)
	modelPart := ""
	if model != "" {
		modelPart = " | model=" + sanitizeLogSummaryToken(model, 96)
	}
	log.WithField("request_id", w.requestInfo.RequestID).
		Errorf("claude_code_tool_call_failure | %s %s %d%s | reason=%s | request_log_id=%s",
			method,
			path,
			statusCode,
			modelPart,
			reason,
			w.requestInfo.RequestID,
		)
}

func detectClaudeCodeToolFailure(response []byte) string {
	if len(response) == 0 {
		return ""
	}
	lowered := strings.ToLower(string(response))
	for _, marker := range claudeCodeToolFailureMarkers {
		if strings.Contains(lowered, marker) {
			return strings.ReplaceAll(marker, " ", "_")
		}
	}
	return ""
}

func apiErrorSummarySuffix(apiResponseErrors []*interfaces.ErrorMessage) string {
	for _, errMsg := range apiResponseErrors {
		if errMsg == nil {
			continue
		}
		if errMsg.StatusCode > 0 {
			return " | upstream_status=" + strconv.Itoa(errMsg.StatusCode)
		}
	}
	return ""
}

func requestInfoPath(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	return strings.SplitN(value, "?", 2)[0]
}

func extractModelFromRequestBody(body []byte) string {
	const key = `"model"`
	idx := bytes.Index(body, []byte(key))
	if idx < 0 {
		return ""
	}
	rest := body[idx+len(key):]
	colon := bytes.IndexByte(rest, ':')
	if colon < 0 {
		return ""
	}
	rest = bytes.TrimSpace(rest[colon+1:])
	if len(rest) == 0 || rest[0] != '"' {
		return ""
	}
	rest = rest[1:]
	end := bytes.IndexByte(rest, '"')
	if end < 0 {
		return ""
	}
	return string(rest[:end])
}

func sanitizeLogSummaryToken(value string, maxLen int) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.Map(func(r rune) rune {
		switch r {
		case '\r', '\n', '\t', '|':
			return ' '
		default:
			return r
		}
	}, value)
	value = strings.Join(strings.Fields(value), "_")
	if maxLen > 0 && len(value) > maxLen {
		return value[:maxLen]
	}
	return value
}

func (w *ResponseWriterWrapper) cloneHeaders() map[string][]string {
	w.ensureHeadersCaptured()

	finalHeaders := make(map[string][]string, len(w.headers))
	for key, values := range w.headers {
		headerValues := make([]string, len(values))
		copy(headerValues, values)
		finalHeaders[key] = headerValues
	}

	return finalHeaders
}

func (w *ResponseWriterWrapper) extractAPIRequest(c *gin.Context) []byte {
	apiRequest, isExist := c.Get("API_REQUEST")
	if !isExist {
		return nil
	}
	data, ok := apiRequest.([]byte)
	if !ok || len(data) == 0 {
		return nil
	}
	return data
}

func (w *ResponseWriterWrapper) extractAPIResponse(c *gin.Context) []byte {
	apiResponse, isExist := c.Get("API_RESPONSE")
	if !isExist {
		return nil
	}
	data, ok := apiResponse.([]byte)
	if !ok || len(data) == 0 {
		return nil
	}
	return data
}

func (w *ResponseWriterWrapper) extractAPIWebsocketTimeline(c *gin.Context) []byte {
	apiTimeline, isExist := c.Get("API_WEBSOCKET_TIMELINE")
	if !isExist {
		return nil
	}
	data, ok := apiTimeline.([]byte)
	if !ok || len(data) == 0 {
		return nil
	}
	return bytes.Clone(data)
}

func (w *ResponseWriterWrapper) extractAPIResponseTimestamp(c *gin.Context) time.Time {
	ts, isExist := c.Get("API_RESPONSE_TIMESTAMP")
	if !isExist {
		return time.Time{}
	}
	if t, ok := ts.(time.Time); ok {
		return t
	}
	return time.Time{}
}

func (w *ResponseWriterWrapper) extractRequestBody(c *gin.Context) []byte {
	if body := extractBodyOverride(c, requestBodyOverrideContextKey); len(body) > 0 {
		return body
	}
	if w.requestInfo != nil && len(w.requestInfo.Body) > 0 {
		return w.requestInfo.Body
	}
	return nil
}

func (w *ResponseWriterWrapper) extractResponseBody(c *gin.Context) []byte {
	if body := extractBodyOverride(c, responseBodyOverrideContextKey); len(body) > 0 {
		return body
	}
	if w.body == nil || w.body.Len() == 0 {
		if w.anomalyBody != nil && w.anomalyBody.Len() > 0 {
			return bytes.Clone(w.anomalyBody.Bytes())
		}
		return nil
	}
	return bytes.Clone(w.body.Bytes())
}

func (w *ResponseWriterWrapper) extractWebsocketTimeline(c *gin.Context) []byte {
	return extractBodyOverride(c, websocketTimelineOverrideContextKey)
}

func extractBodyOverride(c *gin.Context, key string) []byte {
	if c == nil {
		return nil
	}
	bodyOverride, isExist := c.Get(key)
	if !isExist {
		return nil
	}
	switch value := bodyOverride.(type) {
	case []byte:
		if len(value) > 0 {
			return bytes.Clone(value)
		}
	case string:
		if strings.TrimSpace(value) != "" {
			return []byte(value)
		}
	}
	return nil
}

func extractResponsesProtocolAnomaly(c *gin.Context) string {
	if c == nil {
		return ""
	}
	value, exists := c.Get(responsesProtocolAnomalyContextKey)
	if !exists {
		return ""
	}
	reason, ok := value.(string)
	if !ok {
		return ""
	}
	return sanitizeLogSummaryToken(reason, 96)
}

func (w *ResponseWriterWrapper) logRequest(requestBody []byte, statusCode int, headers map[string][]string, body, websocketTimeline, apiRequestBody, apiResponseBody, apiWebsocketTimeline []byte, apiResponseTimestamp time.Time, apiResponseErrors []*interfaces.ErrorMessage, forceLog bool) error {
	if w.requestInfo == nil {
		return nil
	}

	if loggerWithOptions, ok := w.logger.(interface {
		LogRequestWithOptions(string, string, map[string][]string, []byte, int, map[string][]string, []byte, []byte, []byte, []byte, []byte, []*interfaces.ErrorMessage, bool, string, time.Time, time.Time) error
	}); ok {
		return loggerWithOptions.LogRequestWithOptions(
			w.requestInfo.URL,
			w.requestInfo.Method,
			w.requestInfo.Headers,
			requestBody,
			statusCode,
			headers,
			body,
			websocketTimeline,
			apiRequestBody,
			apiResponseBody,
			apiWebsocketTimeline,
			apiResponseErrors,
			forceLog,
			w.requestInfo.RequestID,
			w.requestInfo.Timestamp,
			apiResponseTimestamp,
		)
	}

	return w.logger.LogRequest(
		w.requestInfo.URL,
		w.requestInfo.Method,
		w.requestInfo.Headers,
		requestBody,
		statusCode,
		headers,
		body,
		websocketTimeline,
		apiRequestBody,
		apiResponseBody,
		apiWebsocketTimeline,
		apiResponseErrors,
		w.requestInfo.RequestID,
		w.requestInfo.Timestamp,
		apiResponseTimestamp,
	)
}
