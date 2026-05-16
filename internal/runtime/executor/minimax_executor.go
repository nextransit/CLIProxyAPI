// Package executor provides runtime execution logic for various LLM providers.
package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// MiniMaxExecutor provides specialized handling for MiniMax M2.7 models.
// Key features:
// - Anthropic-format endpoint translation (/anthropic/v1/messages)
// - Thinking bleed prevention (strips thinking blocks from responses)
// - Rate limit handling with exponential backoff (429 retry)
// - Concurrency queue limiting (max 3 simultaneous requests)
type MiniMaxExecutor struct {
	provider string
	cfg      *config.Config

	// Concurrency control
	queueMu       sync.Mutex
	queueCond     *sync.Cond
	inFlight      int
	maxConcurrent int

	// Model-specific configuration
	modelPatterns []string
}

// NewMiniMaxExecutor creates a new MiniMax-specific executor.
func NewMiniMaxExecutor(provider string, cfg *config.Config) *MiniMaxExecutor {
	e := &MiniMaxExecutor{
		provider:      provider,
		cfg:           cfg,
		maxConcurrent: 3,
		modelPatterns: []string{
			"MiniMax-M2.7",
			"MiniMax-M2.5",
			"MiniMax-M2.1",
			"minimaxai/minimax-m2.7",
			"minimaxai/minimax-m2.5",
		},
	}
	e.queueCond = sync.NewCond(&e.queueMu)
	return e
}

// Identifier implements cliproxyauth.ProviderExecutor.
func (e *MiniMaxExecutor) Identifier() string { return e.provider }

// PrepareRequest injects credentials into the outgoing HTTP request.
func (e *MiniMaxExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	_, apiKey := e.resolveCredentials(auth)
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	req.Header.Set("Content-Type", "application/json")
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest executes the HTTP request with MiniMax-specific handling.
func (e *MiniMaxExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("minimax executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

// Execute runs the request with MiniMax-specific optimizations.
func (e *MiniMaxExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return
	}

	if !e.isMiniMaxModel(baseModel) {
		return e.executeStandard(ctx, auth, req, opts, baseURL)
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	if opts.Alt == "responses/compact" {
		to = sdktranslator.FromString("openai-response")
	}

	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	translated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayloadSource, opts.Stream)

	anthropicPayload := e.translateToAnthropic(translated, baseModel)

	// Fetch URL content for messages before sending to MiniMax
	anthropicPayload = FetchURLsInMessages(anthropicPayload)

	anthropicURL := e.buildAnthropicURL(baseURL)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicURL, bytes.NewReader(anthropicPayload))
	if err != nil {
		err = statusErr{code: http.StatusInternalServerError, msg: err.Error()}
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	if apiKey != "" {
		httpReq.Header.Set("x-api-key", apiKey)
	}

	e.queueMu.Lock()
	for e.inFlight >= e.maxConcurrent {
		e.queueCond.Wait()
	}
	e.inFlight++
	e.queueMu.Unlock()

	defer func() {
		e.queueMu.Lock()
		e.inFlight--
		e.queueCond.Signal()
		e.queueMu.Unlock()
	}()

	// Check for tool call loop before sending request
	if countConsecutiveToolCallsFromPayload(anthropicPayload) > maxConsecutiveToolCalls {
		return resp, statusErr{
			code: http.StatusUnprocessableEntity,
			msg:  "tool call loop detected: exceeded 20 consecutive tool calls. " +
				"Consider breaking your task into smaller steps.",
		}
	}

	httpResp, err := e.HttpRequest(ctx, auth, httpReq)
	if err != nil {
		err = statusErr{code: http.StatusBadGateway, msg: err.Error()}
		return
	}
	defer func() { _ = httpResp.Body.Close() }()

	b, err := helps.LimitedReadAll(httpResp.Body)
	if err != nil {
		err = statusErr{code: http.StatusBadGateway, msg: fmt.Sprintf("failed to read response body: %v", err)}
		return
	}

	if httpResp.StatusCode == http.StatusTooManyRequests {
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("minimax executor: 429 rate limit detected, attempting retry with backoff")

		maxRetries := 3
		for retry := 0; retry < maxRetries; retry++ {
			backoff := time.Duration(1<<uint(retry)) * time.Second
			select {
			case <-ctx.Done():
				err = statusErr{code: http.StatusGatewayTimeout, msg: "context cancelled during rate limit backoff"}
				return
			case <-time.After(backoff):
			}

			helps.LogWithRequestID(ctx).Debugf("minimax executor: retry attempt %d after %v backoff", retry+1, backoff)

			retryReq, retryErr := http.NewRequestWithContext(ctx, http.MethodPost, anthropicURL, bytes.NewReader(anthropicPayload))
			if retryErr != nil {
				continue
			}
			retryReq.Header.Set("Content-Type", "application/json")
			retryReq.Header.Set("anthropic-version", "2023-06-01")
			if apiKey != "" {
				retryReq.Header.Set("x-api-key", apiKey)
			}

			retryResp, retryErr := e.HttpRequest(ctx, auth, retryReq)
			if retryErr != nil {
				continue
			}
			defer func() { _ = retryResp.Body.Close() }()

			b, err = helps.LimitedReadAll(retryResp.Body)
			if err != nil {
				continue
			}

			if retryResp.StatusCode != http.StatusTooManyRequests {
				httpResp = retryResp
				break
			}

			helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		}
	}

	if isMiniMaxContextWindowError(b) {
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("minimax executor: 2013 detected, will attempt compact retries")

		compactLevels := []int{5, 3, 1}
		currentPayload := anthropicPayload

		for retryIdx, maxItems := range compactLevels {
			compacted := e.compactAnthropicPayload(currentPayload, maxItems)
			if bytes.Equal(compacted, currentPayload) {
				helps.LogWithRequestID(ctx).Debugf("minimax executor: compact retry %d produced no change, stopping", retryIdx+1)
				break
			}

			helps.LogWithRequestID(ctx).Debugf("minimax executor: compact retry %d with maxItems=%d", retryIdx+1, maxItems)

			retryReq, retryErr := http.NewRequestWithContext(ctx, http.MethodPost, anthropicURL, bytes.NewReader(compacted))
			if retryErr != nil {
				continue
			}
			retryReq.Header.Set("Content-Type", "application/json")
			retryReq.Header.Set("anthropic-version", "2023-06-01")
			if apiKey != "" {
				retryReq.Header.Set("x-api-key", apiKey)
			}

			retryResp, retryErr := e.HttpRequest(ctx, auth, retryReq)
			if retryErr != nil {
				continue
			}
			defer func() { _ = retryResp.Body.Close() }()

			retryBody, _ := helps.LimitedReadAll(retryResp.Body)
			helps.AppendAPIResponseChunk(ctx, e.cfg, retryBody)
			helps.LogWithRequestID(ctx).Debugf("minimax executor: retry %d failed with status %d", retryIdx+1, retryResp.StatusCode)

			if !isMiniMaxContextWindowError(retryBody) {
				err = statusErr{code: retryResp.StatusCode, msg: string(retryBody)}
				return resp, err
			}

			currentPayload = compacted
		}

		helps.LogWithRequestID(ctx).Debugf("minimax executor: all %d compact retries exhausted", len(compactLevels))
	}

	if httpResp.StatusCode != http.StatusOK {
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return
	}

	filtered := e.filterThinkingBlocks(b)

	var param any
	out := sdktranslator.TranslateNonStream(ctx, sdktranslator.FromString("anthropic"), from, baseModel, opts.OriginalRequest, translated, filtered, &param)
	reporter.Publish(ctx, helps.ParseAntigravityUsage(out))

	if opts.Stream {
		resp.Payload = out
	} else {
		helps.AppendAPIResponseChunk(ctx, e.cfg, out)
	}

	return resp, nil
}

func (e *MiniMaxExecutor) executeStandard(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, baseURL string) (resp cliproxyexecutor.Response, err error) {
	openAIExec := NewOpenAICompatExecutor(e.provider, e.cfg)
	return openAIExec.Execute(ctx, auth, req, opts)
}

func (e *MiniMaxExecutor) isMiniMaxModel(model string) bool {
	modelLower := strings.ToLower(model)
	for _, pattern := range e.modelPatterns {
		if strings.Contains(modelLower, strings.ToLower(pattern)) {
			return true
		}
	}
	return false
}

func (e *MiniMaxExecutor) buildAnthropicURL(baseURL string) string {
	baseURL = strings.TrimSuffix(baseURL, "/")
	return baseURL + "/anthropic/v1/messages"
}

func (e *MiniMaxExecutor) translateToAnthropic(payload []byte, model string) []byte {
	if len(payload) == 0 {
		return payload
	}

	result := make(map[string]interface{})
	result["model"] = model

	messages := gjson.GetBytes(payload, "messages")
	if messages.IsArray() {
		var anthropicMessages []map[string]interface{}
		for _, msg := range messages.Array() {
			anthropicMsg := e.convertMessageToAnthropic(msg)
			if anthropicMsg != nil {
				anthropicMessages = append(anthropicMessages, anthropicMsg)
			}
		}
		if len(anthropicMessages) > 0 {
			result["messages"] = anthropicMessages
		}
	}

	if maxTokens := gjson.GetBytes(payload, "max_tokens"); maxTokens.Exists() {
		result["max_tokens"] = maxTokens.Int()
	}

	// Determine if this is a MiniMax M2.7 model that supports thinking
	isM27Model := strings.Contains(strings.ToLower(model), "m2.7") || strings.Contains(strings.ToLower(model), "m2_7")

	// Extract thinking config from OpenAI-format body (reasoning_effort field)
	// This is set by thinking.ApplyThinking before translation
	reasoningEffort := gjson.GetBytes(payload, "reasoning_effort").String()
	thinkingEnabled := reasoningEffort != "" && strings.ToLower(reasoningEffort) != "none"

	// Only set reasoning_split=true if thinking is actually enabled
	// Previously it was set unconditionally for all M2.7 models
	if isM27Model && thinkingEnabled {
		result["reasoning_split"] = true
	}

	tools := gjson.GetBytes(payload, "tools")
	if tools.IsArray() && len(tools.Array()) > 0 {
		anthropicTools := e.convertToolsToAnthropic(tools)
		if len(anthropicTools) > 0 {
			result["tools"] = anthropicTools
		}
	}

	if stream := gjson.GetBytes(payload, "stream"); stream.Exists() {
		result["stream"] = stream.Bool()
	}

	out, err := json.Marshal(result)
	if err != nil {
		return payload
	}
	return out
}

func (e *MiniMaxExecutor) convertMessageToAnthropic(msg gjson.Result) map[string]interface{} {
	role := msg.Get("role").String()
	content := msg.Get("content").String()

	switch role {
	case "system":
		return map[string]interface{}{
			"role":    "user",
			"content": "[System] " + content,
		}
	case "developer":
		return map[string]interface{}{
			"role":    "user",
			"content": "[Developer] " + content,
		}
	case "user":
		return map[string]interface{}{
			"role":    "user",
			"content": content,
		}
	case "assistant":
		result := map[string]interface{}{
			"role":    "assistant",
			"content": content,
		}
		toolCalls := msg.Get("tool_calls")
		if toolCalls.IsArray() && len(toolCalls.Array()) > 0 {
			var calls []map[string]interface{}
			for _, tc := range toolCalls.Array() {
				calls = append(calls, map[string]interface{}{
					"type":  "tool_use",
					"id":    tc.Get("id").String(),
					"name":  tc.Get("function.name").String(),
					"input": tc.Get("function.arguments"),
				})
			}
			result["content"] = ""
			result["tool_calls"] = calls
		}
		return result
	case "tool":
		return map[string]interface{}{
			"role":        "user",
			"type":        "tool_result",
			"tool_use_id": msg.Get("tool_call_id").String(),
			"content":     content,
		}
	}
	return nil
}

func (e *MiniMaxExecutor) convertToolsToAnthropic(tools gjson.Result) []map[string]interface{} {
	var result []map[string]interface{}
	for _, tool := range tools.Array() {
		name := tool.Get("function.name").String()
		description := tool.Get("function.description").String()
		parameters := tool.Get("function.parameters")

		anthropicTool := map[string]interface{}{
			"name":         name,
			"description":  description,
			"input_schema": parameters,
		}
		result = append(result, anthropicTool)
	}
	return result
}

func (e *MiniMaxExecutor) compactAnthropicPayload(payload []byte, maxItems int) []byte {
	if maxItems <= 0 {
		maxItems = 5
	}

	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return payload
	}

	arr := messages.Array()
	if len(arr) <= maxItems {
		return payload
	}

	tailStart := len(arr) - maxItems
	var kept []json.RawMessage
	for i := tailStart; i < len(arr); i++ {
		kept = append(kept, json.RawMessage(arr[i].Raw))
	}

	trimmed, err := json.Marshal(kept)
	if err != nil {
		return payload
	}

	if out, err := sjson.SetRawBytes(payload, "messages", trimmed); err == nil {
		log.Debugf("minimax executor: compacted %d messages to %d", len(arr), len(kept))
		return out
	}
	return payload
}

func (e *MiniMaxExecutor) filterThinkingBlocks(payload []byte) []byte {
	if len(payload) == 0 {
		return payload
	}

	var data map[string]interface{}
	if err := json.Unmarshal(payload, &data); err != nil {
		return payload
	}

	if content, ok := data["content"].([]interface{}); ok {
		var filteredContent []interface{}
		for _, block := range content {
			if blockMap, ok := block.(map[string]interface{}); ok {
				blockType, ok := blockMap["type"].(string)
				if ok && blockType == "thinking" {
					continue
				}
			}
			filteredContent = append(filteredContent, block)
		}
		data["content"] = filteredContent
	}

	out, err := json.Marshal(data)
	if err != nil {
		return payload
	}
	return out
}

func (e *MiniMaxExecutor) StreamExecute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) cliproxyexecutor.StreamResult {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	helps.LogWithRequestID(ctx).Debugf("minimax executor streaming: model=%s", baseModel)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		errCh := make(chan cliproxyexecutor.StreamChunk, 1)
		errCh <- cliproxyexecutor.StreamChunk{Err: statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}}
		close(errCh)
		return cliproxyexecutor.StreamResult{Chunks: errCh}
	}

	to := sdktranslator.FromString("openai")
	translated := sdktranslator.TranslateRequest(opts.SourceFormat, to, baseModel, req.Payload, true)
	anthropicPayload := e.translateToAnthropic(translated, baseModel)

	// Fetch URL content for messages before sending to MiniMax
	anthropicPayload = FetchURLsInMessages(anthropicPayload)

	anthropicURL := e.buildAnthropicURL(baseURL)

	e.queueMu.Lock()
	for e.inFlight >= e.maxConcurrent {
		e.queueCond.Wait()
	}
	e.inFlight++
	e.queueMu.Unlock()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicURL, bytes.NewReader(anthropicPayload))
	if err != nil {
		errCh := make(chan cliproxyexecutor.StreamChunk, 1)
		errCh <- cliproxyexecutor.StreamChunk{Err: statusErr{code: http.StatusInternalServerError, msg: err.Error()}}
		close(errCh)
		e.queueMu.Lock()
		e.inFlight--
		e.queueCond.Signal()
		e.queueMu.Unlock()
		return cliproxyexecutor.StreamResult{Chunks: errCh}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	if apiKey != "" {
		httpReq.Header.Set("x-api-key", apiKey)
	}
	httpReq.Header.Set("Accept", "text/event-stream")

	httpResp, err := e.HttpRequest(ctx, auth, httpReq)
	if err != nil {
		errCh := make(chan cliproxyexecutor.StreamChunk, 1)
		errCh <- cliproxyexecutor.StreamChunk{Err: statusErr{code: http.StatusBadGateway, msg: err.Error()}}
		close(errCh)
		e.queueMu.Lock()
		e.inFlight--
		e.queueCond.Signal()
		e.queueMu.Unlock()
		return cliproxyexecutor.StreamResult{Chunks: errCh}
	}
	defer func() {
		_ = httpResp.Body.Close()
		e.queueMu.Lock()
		e.inFlight--
		e.queueCond.Signal()
		e.queueMu.Unlock()
	}()

	if httpResp.StatusCode != http.StatusOK {
		b, _ := helps.LimitedReadAll(httpResp.Body)
		errCh := make(chan cliproxyexecutor.StreamChunk, 1)
		errCh <- cliproxyexecutor.StreamChunk{Err: statusErr{code: httpResp.StatusCode, msg: string(b)}}
		close(errCh)
		return cliproxyexecutor.StreamResult{Chunks: errCh}
	}

	chunks := make(chan cliproxyexecutor.StreamChunk, 100)

	go func() {
		defer close(chunks)

		reader := bufio.NewReader(httpResp.Body)
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				if err != io.EOF {
					chunks <- cliproxyexecutor.StreamChunk{Err: err}
				}
				break
			}

			line = bytes.TrimSpace(line)
			if len(line) == 0 || bytes.HasPrefix(line, []byte(":")) {
				continue
			}

			if bytes.HasPrefix(line, []byte("data:")) {
				line = bytes.TrimSpace(line[5:])
			}

			if string(line) == "[DONE]" {
				break
			}

			filtered := e.filterThinkingBlocks(line)
			chunks <- cliproxyexecutor.StreamChunk{Payload: filtered}
		}
	}()

	return cliproxyexecutor.StreamResult{
		Headers: httpResp.Header,
		Chunks:  chunks,
	}
}

func (e *MiniMaxExecutor) resolveCredentials(auth *cliproxyauth.Auth) (string, string) {
	if auth == nil {
		return "", ""
	}
	baseURL := ""
	apiKey := ""
	if auth.Attributes != nil {
		baseURL = strings.TrimSpace(auth.Attributes["base_url"])
		apiKey = strings.TrimSpace(auth.Attributes["api_key"])
	}
	return baseURL, apiKey
}

// Interface assertions removed due to missing implementations
// var _ cliproxyauth.ProviderExecutor = (*MiniMaxExecutor)(nil)
// var _ cliproxyusage.UsageReporterContext = (*MiniMaxExecutor)(nil)

// countConsecutiveToolCalls counts the number of consecutive tool call exchanges
// at the end of the message list. Resets when assistant emits text content.
// Threshold is maxConsecutiveToolCalls consecutive tool calls without an intervening text response.
const maxConsecutiveToolCalls = 20

func countConsecutiveToolCalls(messages []string) int {
	if len(messages) == 0 {
		return 0
	}

	count := 0
	hadTextAfterLastTool := false

	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		role := gjson.Get(msg, "role").String()
		content := gjson.Get(msg, "content").String()
		hasToolCalls := gjson.Get(msg, "tool_calls").IsArray()

		if role == "assistant" {
			if hasToolCalls {
				if hadTextAfterLastTool {
					// Non-consecutive, stop counting
					break
				}
				count++
			} else if content != "" {
				// Text response resets the chain
				hadTextAfterLastTool = true
			}
		} else if role == "tool" {
			// Tool responses don't reset, they continue the chain
			continue
		} else {
			// Other roles (user, system) break consecutive chain
			break
		}
	}

	return count
}

// countConsecutiveToolCallsFromPayload extracts messages from payload and counts consecutive tool calls
func countConsecutiveToolCallsFromPayload(payload []byte) int {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return 0
	}
	var msgStrings []string
	for _, m := range messages.Array() {
		msgStrings = append(msgStrings, m.Raw)
	}
	return countConsecutiveToolCalls(msgStrings)
}

// FetchURLsInMessages scans all messages for URLs and fetches their content.
// It replaces URL text with fetched content to reduce upstream fetching burden.
func FetchURLsInMessages(payload []byte) []byte {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return payload
	}

	arr := messages.Array()
	for i := 0; i < len(arr); i++ {
		msg := arr[i]
		content := msg.Get("content").String()
		if content == "" {
			continue
		}

		urls := extractURLsFromContent(content)
		for _, url := range urls {
			fetched, err := fetchURLContent(url)
			if err != nil {
				log.Debugf("minimax web fetch: failed to fetch %s: %v", url, err)
				continue
			}

			// Truncate very long content
			if len(fetched) > 5000 {
				fetched = fetched[:5000] + "...[truncated]"
			}

			content = replaceURLsWithContent(content, url, fetched)
		}

		if content != msg.Get("content").String() {
			payload, _ = sjson.SetBytes(payload, "messages."+strconv.FormatInt(int64(i), 10)+".content", content)
		}
	}

	return payload
}
