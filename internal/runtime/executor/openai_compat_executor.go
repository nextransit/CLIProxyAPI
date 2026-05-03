package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	cliproxyusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// OpenAICompatExecutor implements a stateless executor for OpenAI-compatible providers.
// It performs request/response translation and executes against the provider base URL
// using per-auth credentials (API key) and per-auth HTTP transport (proxy) from context.
type OpenAICompatExecutor struct {
	provider string
	cfg      *config.Config
}

// NewOpenAICompatExecutor creates an executor bound to a provider key (e.g., "openrouter").
func NewOpenAICompatExecutor(provider string, cfg *config.Config) *OpenAICompatExecutor {
	return &OpenAICompatExecutor{provider: provider, cfg: cfg}
}

// Identifier implements cliproxyauth.ProviderExecutor.
func (e *OpenAICompatExecutor) Identifier() string { return e.provider }

// PrepareRequest injects OpenAI-compatible credentials into the outgoing HTTP request.
func (e *OpenAICompatExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	_, apiKey := e.resolveCredentials(auth)
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest injects OpenAI-compatible credentials into the request and executes it.
func (e *OpenAICompatExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("openai compat executor: request is nil")
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

func (e *OpenAICompatExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	endpoint := "/chat/completions"
	if opts.Alt == "responses/compact" {
		to = sdktranslator.FromString("openai-response")
		endpoint = "/responses/compact"
	}
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, opts.Stream)
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, opts.Stream)
	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	translated = helps.ApplyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", translated, originalTranslated, requestedModel)
	if opts.Alt == "responses/compact" {
		if updated, errDelete := sjson.DeleteBytes(translated, "stream"); errDelete == nil {
			translated = updated
		}
	}

	translated, err = thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	if isDeepSeekModel(baseModel) {
		translated, err = ensureDeepSeekReasoningContent(translated)
		if err != nil {
			return resp, err
		}
	}

	url := strings.TrimSuffix(baseURL, "/") + endpoint

	// Debug: log detailed message structure for tool call analysis
	debugLogMessageStructure(translated, baseModel)

	// Debug: log translated payload
	log.Printf("DEBUG executor: sending to upstream, model=%s, stream=%v, url=%s", baseModel, opts.Stream, url)
	log.Printf("DEBUG executor: translated payload (first 2000 chars): %s", string(translated[:min(2000, len(translated))]))

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(translated))
	if err != nil {
		return resp, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	httpReq.Header.Set("User-Agent", "cli-proxy-openai-compat")
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      translated,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("openai compat executor: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := helps.LimitedReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return resp, err
	}
	body, err := helps.LimitedReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, body)
	if detail, hasUsage := helps.ParseOpenAIUsageWithPresence(body); hasUsage {
		reporter.Publish(ctx, detail)
	} else {
		// Some OpenAI-compatible providers return successful responses without usage fields.
		// Publish a best-effort prompt-token estimate so dashboard metrics don't collapse to zero.
		reporter.Publish(ctx, estimateOpenAICompatPromptUsage(baseModel, translated))
	}
	// Translate response back to source format when needed
	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, body, &param)
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
	return resp, nil
}

func (e *OpenAICompatExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return nil, err
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, true)
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, true)
	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	translated = helps.ApplyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", translated, originalTranslated, requestedModel)

	translated, err = thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}

	if isDeepSeekModel(baseModel) {
		translated, err = ensureDeepSeekReasoningContent(translated)
		if err != nil {
			return nil, err
		}
	}

	// Request usage data in the final streaming chunk so that token statistics
	// are captured even when the upstream is an OpenAI-compatible provider.
	translated, _ = sjson.SetBytes(translated, "stream_options.include_usage", true)

	url := strings.TrimSuffix(baseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(translated))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	httpReq.Header.Set("User-Agent", "cli-proxy-openai-compat")
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      translated,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := helps.LimitedReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("openai compat executor: close response body error: %v", errClose)
		}
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return nil, err
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("openai compat executor: close response body error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800) // 50MB
		var param any
		estimatedUsage := estimateOpenAICompatPromptUsage(baseModel, translated)
		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			if detail, ok := helps.ParseOpenAIStreamUsage(line); ok {
				reporter.Publish(ctx, detail)
			}
			if len(line) == 0 {
				continue
			}

			if !bytes.HasPrefix(line, []byte("data:")) {
				continue
			}

			// OpenAI-compatible streams are SSE: lines typically prefixed with "data: ".
			// Pass through translator; it yields one or more chunks for the target schema.
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, bytes.Clone(line), &param)
			for i := range chunks {
				out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			reporter.PublishFailure(ctx)
			out <- cliproxyexecutor.StreamChunk{Err: errScan}
		} else {
			// In case the upstream close the stream without a terminal [DONE] marker.
			// Feed a synthetic done marker through the translator so pending
			// response.completed events are still emitted exactly once.
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, []byte("data: [DONE]"), &param)
			for i := range chunks {
				out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}
			}
		}
		// Ensure we record the request if no usage chunk was ever seen.
		reporter.EnsurePublishedWithDetail(ctx, estimatedUsage)
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func (e *OpenAICompatExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, false)

	modelForCounting := baseModel

	translated, err := thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	enc, err := helps.TokenizerForModel(modelForCounting)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("openai compat executor: tokenizer init failed: %w", err)
	}

	count, err := helps.CountOpenAIChatTokens(enc, translated)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("openai compat executor: token counting failed: %w", err)
	}

	usageJSON := helps.BuildOpenAIUsageJSON(count)
	translatedUsage := sdktranslator.TranslateTokenCount(ctx, to, from, count, usageJSON)
	return cliproxyexecutor.Response{Payload: translatedUsage}, nil
}

func estimateOpenAICompatPromptUsage(model string, translated []byte) cliproxyusage.Detail {
	enc, err := helps.TokenizerForModel(model)
	if err != nil {
		return cliproxyusage.Detail{}
	}
	count, err := helps.CountOpenAIChatTokens(enc, translated)
	if err != nil || count <= 0 {
		return cliproxyusage.Detail{}
	}
	return cliproxyusage.Detail{
		InputTokens:  count,
		TotalTokens:  count,
		OutputTokens: 0,
	}
}

// Refresh is a no-op for API-key based compatibility providers.
func (e *OpenAICompatExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("openai compat executor: refresh called")
	_ = ctx
	return auth, nil
}

func (e *OpenAICompatExecutor) resolveCredentials(auth *cliproxyauth.Auth) (baseURL, apiKey string) {
	if auth == nil {
		return "", ""
	}
	if auth.Attributes != nil {
		baseURL = strings.TrimSpace(auth.Attributes["base_url"])
		apiKey = strings.TrimSpace(auth.Attributes["api_key"])
	}
	return
}

func (e *OpenAICompatExecutor) resolveCompatConfig(auth *cliproxyauth.Auth) *config.OpenAICompatibility {
	if auth == nil || e.cfg == nil {
		return nil
	}
	candidates := make([]string, 0, 3)
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["compat_name"]); v != "" {
			candidates = append(candidates, v)
		}
		if v := strings.TrimSpace(auth.Attributes["provider_key"]); v != "" {
			candidates = append(candidates, v)
		}
	}
	if v := strings.TrimSpace(auth.Provider); v != "" {
		candidates = append(candidates, v)
	}
	for i := range e.cfg.OpenAICompatibility {
		compat := &e.cfg.OpenAICompatibility[i]
		if compat.Disabled {
			continue
		}
		for _, candidate := range candidates {
			if candidate != "" && strings.EqualFold(strings.TrimSpace(candidate), compat.Name) {
				return compat
			}
		}
	}
	return nil
}

func (e *OpenAICompatExecutor) overrideModel(payload []byte, model string) []byte {
	if len(payload) == 0 || model == "" {
		return payload
	}
	payload, _ = sjson.SetBytes(payload, "model", model)
	return payload
}

// isDeepSeekModel checks whether the model name indicates a DeepSeek model.
// DeepSeek API requires reasoning_content to be passed back in multi-turn
// thinking-mode conversations, otherwise it returns 400.
func isDeepSeekModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "deepseek")
}

// ensureDeepSeekReasoningContent ensures assistant messages have reasoning_content present.
// DeepSeek's API requires reasoning_content to be passed back in multi-turn
// thinking-mode conversations; missing it causes 400 errors.
// This mirrors the same logic in kimi_executor.go normalizeKimiToolMessageLinks.
func ensureDeepSeekReasoningContent(payload []byte) ([]byte, error) {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload, nil
	}

	messages := gjson.GetBytes(payload, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return payload, nil
	}

	out := payload
	latestReasoning := ""
	hasLatestReasoning := false
	patched := 0

	for msgIdx, msg := range messages.Array() {
		role := strings.TrimSpace(msg.Get("role").String())
		if role != "assistant" {
			continue
		}

		reasoning := msg.Get("reasoning_content")
		if reasoning.Exists() && strings.TrimSpace(reasoning.String()) != "" {
			latestReasoning = reasoning.String()
			hasLatestReasoning = true
			continue
		}

		reasoningText := fallbackDeepSeekTextReasoning(msg, hasLatestReasoning, latestReasoning)
		path := fmt.Sprintf("messages.%d.reasoning_content", msgIdx)
		next, err := sjson.SetBytes(out, path, reasoningText)
		if err != nil {
			return payload, fmt.Errorf("openai compat executor: failed to set reasoning_content for deepseek: %w", err)
		}
		out = next
		patched++
	}

	if patched > 0 {
		log.WithField("patched_reasoning_messages", patched).
			Debug("openai compat executor: ensured reasoning_content for deepseek model")
	}

	return out, nil
}

// fallbackDeepSeekReasoningForAssistant determines the best reasoning text to inject
// when an assistant message is missing reasoning_content.
// For tool-call messages, prefer previous reasoning context first.
// For text-only assistant messages, prefer current content first.
func fallbackDeepSeekReasoningForAssistant(msg gjson.Result, hasToolCalls bool, hasLatest bool, latest string) string {
	if hasToolCalls {
		return fallbackDeepSeekToolReasoning(msg, hasLatest, latest)
	}
	return fallbackDeepSeekTextReasoning(msg, hasLatest, latest)
}

// fallbackDeepSeekToolReasoning determines reasoning text for assistant tool-call messages.
// It tries: 1) latest reasoning from prior messages, 2) current message content, 3) placeholder.
func fallbackDeepSeekToolReasoning(msg gjson.Result, hasLatest bool, latest string) string {
	if hasLatest && strings.TrimSpace(latest) != "" {
		return latest
	}
	if text := extractDeepSeekReasoningContentText(msg); text != "" {
		return text
	}
	return "[reasoning unavailable]"
}

// fallbackDeepSeekTextReasoning determines reasoning text for assistant text-only messages.
// It tries: 1) current message content, 2) latest reasoning from prior messages, 3) placeholder.
func fallbackDeepSeekTextReasoning(msg gjson.Result, hasLatest bool, latest string) string {
	if text := extractDeepSeekReasoningContentText(msg); text != "" {
		return text
	}
	if hasLatest && strings.TrimSpace(latest) != "" {
		return latest
	}
	return "[reasoning unavailable]"
}

func extractDeepSeekReasoningContentText(msg gjson.Result) string {
	content := msg.Get("content")
	if content.Type == gjson.String {
		if text := strings.TrimSpace(content.String()); text != "" {
			return text
		}
	}
	if content.IsArray() {
		parts := make([]string, 0, len(content.Array()))
		for _, item := range content.Array() {
			text := strings.TrimSpace(item.Get("text").String())
			if text == "" {
				continue
			}
			parts = append(parts, text)
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}
	return ""
}

type statusErr struct {
	code       int
	msg        string
	retryAfter *time.Duration
}

func (e statusErr) Error() string {
	if e.msg != "" {
		return e.msg
	}
	return fmt.Sprintf("status %d", e.code)
}
func (e statusErr) StatusCode() int            { return e.code }
func (e statusErr) RetryAfter() *time.Duration { return e.retryAfter }

// debugLogMessageStructure logs detailed message structure for diagnosing tool call issues.
// It prints each message's role, content preview, and tool_call_id (if present) to help
// identify "tool call result does not follow tool call" errors.
func debugLogMessageStructure(payload []byte, model string) {
	if !log.IsLevelEnabled(log.DebugLevel) {
		return
	}

	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		log.Debugf("DEBUG tool_call: model=%s, messages is not an array", model)
		return
	}

	msgCount := len(messages.Array())
	toolCallIDs := make(map[string]int) // track which message index has which tool_call_id
	assistantWithToolCalls := make(map[int]bool)

	// First pass: identify all assistant messages with tool_calls and all tool messages
	for i, msg := range messages.Array() {
		role := msg.Get("role").String()
		if role == "assistant" {
			tcs := msg.Get("tool_calls")
			if tcs.IsArray() && len(tcs.Array()) > 0 {
				assistantWithToolCalls[i] = true
				for j, tc := range tcs.Array() {
					tcID := tc.Get("id").String()
					if tcID != "" {
						toolCallIDs[tcID] = i*1000+j // encode assistant index in upper digits
					}
				}
			}
		}
	}

	// Second pass: log detailed structure
	for i, msg := range messages.Array() {
		role := msg.Get("role").String()

		switch role {
		case "assistant":
			tcs := msg.Get("tool_calls")
			if tcs.IsArray() && len(tcs.Array()) > 0 {
				var tcIDs []string
				for _, tc := range tcs.Array() {
					tcIDs = append(tcIDs, tc.Get("id").String())
				}
				log.Debugf("DEBUG tool_call: [%d] assistant with tool_calls=%v", i, tcIDs)
			} else {
				content := msg.Get("content").String()
				if len(content) > 60 {
					content = content[:60] + "..."
				}
				log.Debugf("DEBUG tool_call: [%d] assistant content=%q", i, content)
			}
		case "tool":
			toolCallID := msg.Get("tool_call_id").String()
			if toolCallID == "" {
				toolCallID = "(empty)"
			}
			content := msg.Get("content").String()
			if len(content) > 40 {
				content = content[:40] + "..."
			}
			// Check if tool_call_id matches a known assistant tool call
			if origIdx, ok := toolCallIDs[toolCallID]; ok {
				assistantIdx := origIdx / 1000
				log.Debugf("DEBUG tool_call: [%d] tool tool_call_id=%s matches assistant[%d]", i, toolCallID[:min(8, len(toolCallID))], assistantIdx)
			} else {
				log.Debugf("DEBUG tool_call: [%d] tool tool_call_id=%s UNMATCHED", i, toolCallID[:min(8, len(toolCallID))])
			}
			_ = content
		case "system":
			log.Debugf("DEBUG tool_call: [%d] system", i)
		case "user":
			content := msg.Get("content").String()
			if len(content) > 60 {
				content = content[:60] + "..."
			}
			log.Debugf("DEBUG tool_call: [%d] user content=%q", i, content)
		default:
			log.Debugf("DEBUG tool_call: [%d] %s", i, role)
		}
	}

	// Log sequence integrity check
	var lastAssistantWithTC, lastToolMsg int = -1, -1
	for i, msg := range messages.Array() {
		role := msg.Get("role").String()
		if role == "assistant" && assistantWithToolCalls[i] {
			lastAssistantWithTC = i
		}
		if role == "tool" {
			lastToolMsg = i
			if lastAssistantWithTC == -1 {
				log.Warnf("DEBUG tool_call: tool message at [%d] has no preceding assistant with tool_calls", i)
			} else if lastToolMsg < lastAssistantWithTC {
				log.Warnf("DEBUG tool_call: ORDER ISSUE: tool at [%d] after assistant at [%d]", i, lastAssistantWithTC)
			}
		}
	}
}
