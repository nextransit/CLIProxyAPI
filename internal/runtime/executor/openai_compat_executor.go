package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
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
	translated = removeUnsupportedClaudeBuiltinToolsForOpenAICompat(translated, originalPayload)
	if opts.Alt == "responses/compact" {
		if updated, errDelete := sjson.DeleteBytes(translated, "stream"); errDelete == nil {
			translated = updated
		}
	}

	translated, err = thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}
	reporter.SetThinkingFromPayload(translated)

	translated, err = normalizeOpenAICompatToolMessages(translated)
	if err != nil {
		return resp, err
	}

	if isDeepSeekModel(baseModel) {
		translated = normalizeDeepSeekThinkingRequest(translated, baseModel, e.Identifier())
		translated, err = ensureDeepSeekToolMessageNames(translated)
		if err != nil {
			return resp, err
		}
		translated, err = ensureDeepSeekReasoningContent(translated)
		if err != nil {
			return resp, err
		}
	}
	reporter.SetThinkingFromPayloadIfMissing(translated)
	translated = normalizeMiniMaxM3Request(translated, baseModel)
	translated = clampOpenAICompatMaxTokens(translated, baseModel, e.Identifier())
	reporter.SetThinkingFromPayloadIfMissing(translated)
	if isDeepSeekV4Model(baseModel) {
		reporter.SetThinkingEffortIfMissing(defaultDeepSeekV4ThinkingEffort(e.Identifier()))
	}

	url := strings.TrimSuffix(baseURL, "/") + endpoint
	reporter.SetHTTPRequestMetadata("openai_compat", http.MethodPost, "/v1"+endpoint, "OpenAI Compatible", url)
	reporter.SetModelMetadata(requestedModel, baseModel, "", "")

	debugLogMessageStructure(translated, baseModel)
	if log.IsLevelEnabled(log.DebugLevel) {
		log.Debugf("openai compat executor: sending request upstream, model=%s, stream=%v, url=%s", baseModel, opts.Stream, url)
	}

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
	reporter.SetStatusCode(httpResp.StatusCode)
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := helps.LimitedReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))

		// Handle MiniMax 2013 errors with progressive payload reduction.
		// Strategy: strip tools -> compact messages -> truncate content (last resort).
		if shouldAttemptOpenAICompat2013Recovery(b, httpResp.StatusCode, baseModel, translated) {
			compactLevels := []int{5, 3, 1}
			currentPayload := stripToolsFromPayload(translated)
			if !bytes.Equal(currentPayload, translated) {
				helps.LogWithRequestID(ctx).Debugf("openai compat executor: 2013 retry with tools stripped")
				retryReq, rErr := buildOpenAICompatRetryRequest(ctx, url, apiKey, currentPayload, attrs, false)
				if rErr == nil {
					httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
					if retryResp, rErr := httpClient.Do(retryReq); rErr == nil {
						retryBody, _ := helps.LimitedReadAll(retryResp.Body)
						if errClose := retryResp.Body.Close(); errClose != nil {
							log.Errorf("openai compat executor: close 2013 retry response body error: %v", errClose)
						}
						if retryResp.StatusCode >= 200 && retryResp.StatusCode < 300 {
							helps.LogWithRequestID(ctx).Debugf("openai compat executor: 2013 tools-stripped retry succeeded")
							var param any
							retryBody = sanitizeOpenAICompatThinkingResponse(baseModel, retryBody)
							out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, currentPayload, retryBody, &param)
							resp = cliproxyexecutor.Response{Payload: out, Headers: retryResp.Header.Clone()}
							return resp, nil
						}
						if !shouldAttemptOpenAICompat2013Recovery(retryBody, retryResp.StatusCode, baseModel, currentPayload) {
							helps.LogWithRequestID(ctx).Debugf("openai compat executor: 2013 tools-stripped retry returned non-recoverable status %d", retryResp.StatusCode)
						}
					}
				}
			}

			// Step 1: Try compacting message count progressively
		compactLoop:
			for retryIdx, maxItems := range compactLevels {
				compacted := compactMessagesForRetry(currentPayload, maxItems)
				if bytes.Equal(compacted, currentPayload) {
					break
				}
				helps.LogWithRequestID(ctx).Debugf("openai compat executor: 2013 compact retry %d/%d to %d messages", retryIdx+1, len(compactLevels), maxItems)

				retryReq, rErr := buildOpenAICompatRetryRequest(ctx, url, apiKey, compacted, attrs, false)
				if rErr != nil {
					break
				}

				httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
				retryResp, rErr := httpClient.Do(retryReq)
				if rErr != nil {
					break
				}
				retryBody, _ := helps.LimitedReadAll(retryResp.Body)
				retryResp.Body.Close()

				if retryResp.StatusCode >= 200 && retryResp.StatusCode < 300 {
					helps.LogWithRequestID(ctx).Debugf("openai compat executor: 2013 compact retry %d succeeded", retryIdx+1)
					var param any
					retryBody = sanitizeOpenAICompatThinkingResponse(baseModel, retryBody)
					out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, compacted, retryBody, &param)
					resp = cliproxyexecutor.Response{Payload: out, Headers: retryResp.Header.Clone()}
					return resp, nil
				}

				if shouldAttemptOpenAICompat2013Recovery(retryBody, retryResp.StatusCode, baseModel, compacted) {
					helps.LogWithRequestID(ctx).Debugf("openai compat executor: 2013 compact retry %d still exceeded, trying harder compaction", retryIdx+1)
					currentPayload = compacted
					continue
				}
				break compactLoop
			}

			// Step 2: Last resort - truncate message content
			helps.LogWithRequestID(ctx).Debugf("openai compat executor: 2013 compact retries exhausted, attempting content truncation")
			truncated := truncateMessageContent(currentPayload, 128000)
			if !bytes.Equal(truncated, currentPayload) {
				retryReq, rErr := buildOpenAICompatRetryRequest(ctx, url, apiKey, truncated, attrs, false)
				if rErr == nil {
					httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
					if retryResp, rErr := httpClient.Do(retryReq); rErr == nil {
						retryBody, _ := helps.LimitedReadAll(retryResp.Body)
						retryResp.Body.Close()
						if retryResp.StatusCode >= 200 && retryResp.StatusCode < 300 {
							helps.LogWithRequestID(ctx).Debugf("openai compat executor: 2013 content truncation retry succeeded")
							var param any
							retryBody = sanitizeOpenAICompatThinkingResponse(baseModel, retryBody)
							out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, truncated, retryBody, &param)
							resp = cliproxyexecutor.Response{Payload: out, Headers: retryResp.Header.Clone()}
							return resp, nil
						}
					}
				}
			}
			helps.LogWithRequestID(ctx).Debugf("openai compat executor: 2013 all recovery strategies exhausted")
		}

		err = statusErr{code: httpResp.StatusCode, msg: string(b), retryAfter: helps.ParseRetryAfter(httpResp, b)}
		return resp, err
	}
	body, err := helps.LimitedReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	body = sanitizeOpenAICompatThinkingResponse(baseModel, body)
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
	translated = removeUnsupportedClaudeBuiltinToolsForOpenAICompat(translated, originalPayload)

	translated, err = thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}
	reporter.SetThinkingFromPayload(translated)

	translated, err = normalizeOpenAICompatToolMessages(translated)
	if err != nil {
		return nil, err
	}

	if isDeepSeekModel(baseModel) {
		translated = normalizeDeepSeekThinkingRequest(translated, baseModel, e.Identifier())
		translated, err = ensureDeepSeekToolMessageNames(translated)
		if err != nil {
			return nil, err
		}
		translated, err = ensureDeepSeekReasoningContent(translated)
		if err != nil {
			return nil, err
		}
	}
	reporter.SetThinkingFromPayloadIfMissing(translated)
	translated = normalizeMiniMaxM3Request(translated, baseModel)

	// Request usage data in the final streaming chunk so that token statistics
	// are captured even when the upstream is an OpenAI-compatible provider.
	translated, _ = sjson.SetBytes(translated, "stream_options.include_usage", true)
	translated = clampOpenAICompatMaxTokens(translated, baseModel, e.Identifier())
	reporter.SetThinkingFromPayloadIfMissing(translated)
	if isDeepSeekV4Model(baseModel) {
		reporter.SetThinkingEffortIfMissing(defaultDeepSeekV4ThinkingEffort(e.Identifier()))
	}

	url := strings.TrimSuffix(baseURL, "/") + "/chat/completions"
	reporter.SetHTTPRequestMetadata("openai_compat", http.MethodPost, "/v1/chat/completions", "OpenAI Compatible", url)
	reporter.SetModelMetadata(requestedModel, baseModel, "", "")

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
	reporter.SetStatusCode(httpResp.StatusCode)
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := helps.LimitedReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("openai compat executor: close response body error: %v", errClose)
		}

		// Handle MiniMax 2013 errors with progressive payload reduction for streaming.
		if shouldAttemptOpenAICompat2013Recovery(b, httpResp.StatusCode, baseModel, translated) {
			lastFailureStatus := httpResp.StatusCode
			lastFailureBody := b
			lastFailureRetryAfter := helps.ParseRetryAfter(httpResp, b)
			compactLevels := []int{5, 3, 1}
			currentPayload := stripToolsFromPayload(translated)
			if !bytes.Equal(currentPayload, translated) {
				helps.LogWithRequestID(ctx).Debugf("openai compat executor: 2013 streaming retry with tools stripped")
				retryReq, rErr := buildOpenAICompatRetryRequest(ctx, url, apiKey, currentPayload, attrs, true)
				if rErr == nil {
					httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
					if retryResp, rErr := httpClient.Do(retryReq); rErr == nil {
						if retryResp.StatusCode >= 200 && retryResp.StatusCode < 300 {
							helps.LogWithRequestID(ctx).Debugf("openai compat executor: 2013 streaming tools-stripped retry succeeded")
							httpResp = retryResp
							translated = currentPayload
						} else {
							retryBody, _ := helps.LimitedReadAll(retryResp.Body)
							lastFailureStatus = retryResp.StatusCode
							lastFailureBody = retryBody
							lastFailureRetryAfter = helps.ParseRetryAfter(retryResp, retryBody)
							if errClose := retryResp.Body.Close(); errClose != nil {
								log.Errorf("openai compat executor: close 2013 streaming retry response body error: %v", errClose)
							}
							if !shouldAttemptOpenAICompat2013Recovery(retryBody, retryResp.StatusCode, baseModel, currentPayload) {
								helps.LogWithRequestID(ctx).Debugf("openai compat executor: 2013 streaming tools-stripped retry returned non-recoverable status %d", retryResp.StatusCode)
							}
						}
					}
				}
			}

			// Step 1: Try compacting message count progressively
		compactLoop:
			for retryIdx, maxItems := range compactLevels {
				if httpResp.StatusCode >= 200 && httpResp.StatusCode < 300 {
					break compactLoop
				}
				compacted := compactMessagesForRetry(currentPayload, maxItems)
				if bytes.Equal(compacted, currentPayload) {
					break
				}
				helps.LogWithRequestID(ctx).Debugf("openai compat executor: 2013 compact retry %d/%d to %d messages", retryIdx+1, len(compactLevels), maxItems)

				retryReq, rErr := buildOpenAICompatRetryRequest(ctx, url, apiKey, compacted, attrs, true)
				if rErr != nil {
					break
				}

				httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
				retryResp, rErr := httpClient.Do(retryReq)
				if rErr != nil {
					break
				}

				if retryResp.StatusCode >= 200 && retryResp.StatusCode < 300 {
					helps.LogWithRequestID(ctx).Debugf("openai compat executor: 2013 compact retry %d succeeded", retryIdx+1)
					httpResp = retryResp
					translated = compacted
					break compactLoop
				}

				retryBody, _ := helps.LimitedReadAll(retryResp.Body)
				lastFailureStatus = retryResp.StatusCode
				lastFailureBody = retryBody
				lastFailureRetryAfter = helps.ParseRetryAfter(retryResp, retryBody)
				if shouldAttemptOpenAICompat2013Recovery(retryBody, retryResp.StatusCode, baseModel, compacted) {
					helps.LogWithRequestID(ctx).Debugf("openai compat executor: 2013 compact retry %d still exceeded, trying harder compaction", retryIdx+1)
					currentPayload = compacted
					if errClose := retryResp.Body.Close(); errClose != nil {
						log.Errorf("openai compat executor: close 2013 streaming compact retry response body error: %v", errClose)
					}
					continue
				}
				if errClose := retryResp.Body.Close(); errClose != nil {
					log.Errorf("openai compat executor: close 2013 streaming compact retry response body error: %v", errClose)
				}
				break compactLoop
			}

			// If we still have a non-2xx response after compact retries, check if we should try truncation
			if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
				helps.LogWithRequestID(ctx).Debugf("openai compat executor: 2013 compact retries exhausted, attempting content truncation")
				truncated := truncateMessageContent(currentPayload, 128000)
				if !bytes.Equal(truncated, currentPayload) {
					retryReq, rErr := buildOpenAICompatRetryRequest(ctx, url, apiKey, truncated, attrs, true)
					if rErr == nil {
						httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
						if retryResp, rErr := httpClient.Do(retryReq); rErr == nil {
							if retryResp.StatusCode >= 200 && retryResp.StatusCode < 300 {
								helps.LogWithRequestID(ctx).Debugf("openai compat executor: 2013 content truncation retry succeeded")
								httpResp = retryResp
								translated = truncated
							} else {
								retryBody, _ := helps.LimitedReadAll(retryResp.Body)
								lastFailureStatus = retryResp.StatusCode
								lastFailureBody = retryBody
								lastFailureRetryAfter = helps.ParseRetryAfter(retryResp, retryBody)
								if errClose := retryResp.Body.Close(); errClose != nil {
									log.Errorf("openai compat executor: close 2013 streaming truncation retry response body error: %v", errClose)
								}
							}
						}
					}
				}
			}

			// If still not successful, log and prepare to return error
			if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
				helps.LogWithRequestID(ctx).Debugf("openai compat executor: 2013 all recovery strategies exhausted")
				err = statusErr{code: lastFailureStatus, msg: string(lastFailureBody), retryAfter: lastFailureRetryAfter}
				return nil, err
			}
			reporter.SetStatusCode(httpResp.StatusCode)
		} else {
			err = statusErr{code: httpResp.StatusCode, msg: string(b), retryAfter: helps.ParseRetryAfter(httpResp, b)}
			return nil, err
		}
	}
	// Wrap the response body with an idle timeout to prevent infinite blocking
	// when the upstream stops sending data mid-stream without closing the connection.
	// This causes "Response stalled mid-stream" errors from the client.
	upstreamIdleTimeout := time.Duration(e.cfg.Streaming.UpstreamIdleTimeoutSeconds) * time.Second
	readCloser := helps.NewIdleTimeoutReadCloser(httpResp.Body, upstreamIdleTimeout)

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := readCloser.Close(); errClose != nil {
				log.Errorf("openai compat executor: close response body error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(readCloser)
		scanner.Buffer(nil, 52_428_800) // 50MB
		var param any
		insideThinkBlock := false
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
			chunkLine := sanitizeOpenAICompatThinkingStreamLine(baseModel, bytes.Clone(line), &insideThinkBlock)
			if len(chunkLine) == 0 {
				continue
			}
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, chunkLine, &param)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return
				}
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			reporter.PublishFailure(ctx)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
			case <-ctx.Done():
			}
		} else {
			// In case the upstream close the stream without a terminal [DONE] marker.
			// Feed a synthetic done marker through the translator so pending
			// response.completed events are still emitted exactly once.
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, []byte("data: [DONE]"), &param)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return
				}
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

func sanitizeOpenAICompatThinkingResponse(model string, body []byte) []byte {
	if !isMiniMaxThinkingTagModel(model) || len(body) == 0 || !gjson.ValidBytes(body) {
		return body
	}
	out := body
	choices := gjson.GetBytes(body, "choices")
	if !choices.IsArray() {
		return body
	}
	for choiceIndex, choice := range choices.Array() {
		out = sanitizeOpenAICompatThinkingStringField(out, choice, fmt.Sprintf("choices.%d.message.content", choiceIndex), "message.content")
		out = sanitizeOpenAICompatThinkingStringField(out, choice, fmt.Sprintf("choices.%d.delta.content", choiceIndex), "delta.content")
	}
	return out
}

func sanitizeOpenAICompatThinkingStreamLine(model string, line []byte, insideThinkBlock *bool) []byte {
	if !isMiniMaxThinkingTagModel(model) || len(line) == 0 {
		return line
	}
	prefix := []byte("data:")
	trimmed := bytes.TrimSpace(line)
	if !bytes.HasPrefix(trimmed, prefix) {
		return line
	}
	data := bytes.TrimSpace(trimmed[len(prefix):])
	if bytes.Equal(data, []byte("[DONE]")) {
		return line
	}
	cleaned := sanitizeOpenAICompatThinkingResponseWithState(model, data, insideThinkBlock)
	if len(cleaned) == 0 || bytes.Equal(cleaned, data) {
		return line
	}
	return append([]byte("data: "), cleaned...)
}

func sanitizeOpenAICompatThinkingResponseWithState(model string, body []byte, insideThinkBlock *bool) []byte {
	if !isMiniMaxThinkingTagModel(model) || len(body) == 0 || !gjson.ValidBytes(body) {
		return body
	}
	out := body
	choices := gjson.GetBytes(body, "choices")
	if !choices.IsArray() {
		return body
	}
	for choiceIndex, choice := range choices.Array() {
		out = sanitizeOpenAICompatThinkingStringFieldWithState(out, choice, fmt.Sprintf("choices.%d.message.content", choiceIndex), "message.content", insideThinkBlock)
		out = sanitizeOpenAICompatThinkingStringFieldWithState(out, choice, fmt.Sprintf("choices.%d.delta.content", choiceIndex), "delta.content", insideThinkBlock)
	}
	return out
}

func sanitizeOpenAICompatThinkingStringField(out []byte, choice gjson.Result, path string, relativePath string) []byte {
	return sanitizeOpenAICompatThinkingStringFieldWithState(out, choice, path, relativePath, nil)
}

func sanitizeOpenAICompatThinkingStringFieldWithState(out []byte, choice gjson.Result, path string, relativePath string, insideThinkBlock *bool) []byte {
	field := choice.Get(relativePath)
	if !field.Exists() || field.Type != gjson.String {
		return out
	}
	raw := field.String()
	cleaned := stripThinkTagTextWithState(raw, insideThinkBlock)
	if cleaned == raw {
		return out
	}
	updated, errSet := sjson.SetBytes(out, path, cleaned)
	if errSet != nil {
		return out
	}
	return updated
}

func isMiniMaxThinkingTagModel(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	return strings.Contains(normalized, "minimax") &&
		(strings.Contains(normalized, "m2.7") || strings.Contains(normalized, "m2_7") || strings.Contains(normalized, "m3"))
}

func stripThinkTagText(raw string) string {
	return stripThinkTagTextWithState(raw, nil)
}

func stripThinkTagTextWithState(raw string, insideThinkBlock *bool) string {
	text := strings.TrimSpace(raw)
	var out strings.Builder
	for {
		lower := strings.ToLower(text)
		if insideThinkBlock != nil && *insideThinkBlock {
			endOnly := strings.Index(lower, "</think>")
			if endOnly < 0 {
				return strings.TrimSpace(out.String())
			}
			*insideThinkBlock = false
			text = strings.TrimSpace(text[endOnly+len("</think>"):])
			continue
		}
		start := strings.Index(lower, "<think>")
		if start < 0 {
			if endOnly := strings.Index(lower, "</think>"); endOnly >= 0 {
				text = strings.TrimSpace(text[endOnly+len("</think>"):])
				continue
			}
			out.WriteString(text)
			break
		}
		out.WriteString(text[:start])
		end := strings.Index(lower[start+len("<think>"):], "</think>")
		if end < 0 {
			if insideThinkBlock != nil {
				*insideThinkBlock = true
			}
			return strings.TrimSpace(out.String())
		}
		end = start + len("<think>") + end + len("</think>")
		text = strings.TrimSpace(text[end:])
	}
	return strings.TrimSpace(out.String())
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

func clampOpenAICompatMaxTokens(payload []byte, modelID string, provider string) []byte {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload
	}
	maxTokens := gjson.GetBytes(payload, "max_tokens")
	if !maxTokens.Exists() || maxTokens.Type != gjson.Number {
		return payload
	}

	value := maxTokens.Int()
	if value < 1 {
		payload, _ = sjson.SetBytes(payload, "max_tokens", 1)
		value = 1
	}

	if limit := openAICompatMaxTokensLimit(modelID, provider); limit > 0 && value > int64(limit) {
		payload, _ = sjson.SetBytes(payload, "max_tokens", limit)
	}
	return payload
}

func openAICompatMaxTokensLimit(modelID string, provider string) int {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return 0
	}
	deepSeekV4Limit := 0
	if isDeepSeekV4Model(modelID) {
		deepSeekV4Limit = 65536
	}
	if info := registry.LookupModelInfo(modelID, provider); info != nil && info.MaxCompletionTokens > 0 {
		if deepSeekV4Limit > 0 && info.MaxCompletionTokens > deepSeekV4Limit {
			return deepSeekV4Limit
		}
		return info.MaxCompletionTokens
	}
	if info := registry.LookupModelInfo(modelID); info != nil && info.MaxCompletionTokens > 0 {
		if deepSeekV4Limit > 0 && info.MaxCompletionTokens > deepSeekV4Limit {
			return deepSeekV4Limit
		}
		return info.MaxCompletionTokens
	}
	return deepSeekV4Limit
}

func isDeepSeekV4Model(model string) bool {
	lowered := strings.ToLower(strings.TrimSpace(model))
	return strings.Contains(lowered, "deepseek") && strings.Contains(lowered, "v4")
}

func normalizeDeepSeekThinkingRequest(payload []byte, modelID, provider string) []byte {
	if len(payload) == 0 || !gjson.ValidBytes(payload) || !isDeepSeekV4Model(modelID) {
		return payload
	}

	out := payload
	openRouter := isOpenRouterProvider(provider)
	sensenova := isSensenovaProvider(provider)
	reasoningEffortOnly := openRouter || sensenova
	thinkingType := firstDeepSeekThinkingType(out)
	if effort := strings.ToLower(strings.TrimSpace(gjson.GetBytes(out, "reasoning_effort").String())); effort != "" {
		normalizedEffort := normalizeDeepSeekReasoningEffort(effort, provider)
		if normalizedEffort == "" {
			if updated, errDelete := sjson.DeleteBytes(out, "reasoning_effort"); errDelete == nil {
				out = updated
			}
			thinkingType = "disabled"
		} else {
			if updated, errSet := sjson.SetBytes(out, "reasoning_effort", normalizedEffort); errSet == nil {
				out = updated
			}
			if thinkingType == "" && !reasoningEffortOnly {
				thinkingType = "enabled"
			}
		}
	} else {
		switch thinkingType {
		case "disabled":
			if updated, errSet := sjson.SetBytes(out, "reasoning_effort", "none"); errSet == nil {
				out = updated
			}
		default:
			if updated, errSet := sjson.SetBytes(out, "reasoning_effort", defaultDeepSeekV4ThinkingEffort(provider)); errSet == nil {
				out = updated
			}
		}
	}

	// Delete stale extra_body.thinking before writing our canonical value.
	if gjson.GetBytes(out, "extra_body.thinking").Exists() {
		if updated, errDelete := sjson.DeleteBytes(out, "extra_body.thinking"); errDelete == nil {
			out = updated
		}
		if extraBody := gjson.GetBytes(out, "extra_body"); extraBody.Exists() && extraBody.IsObject() && len(extraBody.Map()) == 0 {
			if updated, errDelete := sjson.DeleteBytes(out, "extra_body"); errDelete == nil {
				out = updated
			}
		}
	}
	if thinkingType == "" && !reasoningEffortOnly {
		thinkingType = "enabled"
	}
	if thinkingType != "" && !reasoningEffortOnly {
		if updated, errSet := sjson.SetBytes(out, "extra_body.thinking.type", thinkingType); errSet == nil {
			out = updated
		}
	}
	return out
}

func isOpenRouterProvider(provider string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(provider)), "openrouter")
}

func isSensenovaProvider(provider string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(provider)), "sensenova")
}

func defaultDeepSeekV4ThinkingEffort(provider string) string {
	return "high"
}

func firstDeepSeekThinkingType(payload []byte) string {
	for _, path := range []string{"thinking.type", "extra_body.thinking.type"} {
		value := strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, path).String()))
		switch value {
		case "enabled", "adaptive", "auto":
			return "enabled"
		case "disabled", "none", "0":
			return "disabled"
		}
	}
	return ""
}

func normalizeDeepSeekReasoningEffort(effort, provider string) string {
	openRouter := isOpenRouterProvider(provider)
	sensenova := isSensenovaProvider(provider)
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "none", "disabled", "0":
		if sensenova {
			return "none"
		}
		return ""
	case "max":
		if openRouter || sensenova {
			return "high"
		}
		return "max"
	case "xhigh":
		if openRouter || sensenova {
			return "high"
		}
		return "max"
	case "high":
		return "high"
	case "medium", "low":
		if sensenova {
			return effort
		}
		if openRouter {
			return effort
		}
		return "high"
	case "minimal":
		if sensenova {
			return "low"
		}
		if openRouter {
			return effort
		}
		return "high"
	case "auto":
		if openRouter || sensenova {
			return "high"
		}
		return "high"
	default:
		return effort
	}
}

func normalizeMiniMaxM3Request(payload []byte, model string) []byte {
	if !isMiniMaxM3Model(model) || len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload
	}

	out := payload
	out = removeMiniMaxM3UnsupportedThinkingFields(out)
	out = normalizeMiniMaxM3ToolChoice(out)
	if effort := gjson.GetBytes(out, "reasoning_effort"); effort.Exists() {
		thinkingType := "adaptive"
		value := strings.ToLower(strings.TrimSpace(effort.String()))
		if value == "none" || value == "disabled" || value == "0" {
			thinkingType = "disabled"
		}
		if updated, errSet := sjson.SetBytes(out, "extra_body.thinking.type", thinkingType); errSet == nil {
			out = updated
		}
		if updated, errDelete := sjson.DeleteBytes(out, "reasoning_effort"); errDelete == nil {
			out = updated
		}
	}

	thinking := gjson.GetBytes(out, "thinking")
	if !thinking.Exists() || !thinking.IsObject() || gjson.GetBytes(out, "reasoning_split").Exists() {
		return out
	}
	thinkingType := strings.ToLower(strings.TrimSpace(thinking.Get("type").String()))
	if thinkingType == "" {
		thinkingType = "adaptive"
		if updated, errSet := sjson.SetBytes(out, "extra_body.thinking.type", thinkingType); errSet == nil {
			out = updated
		}
	} else if thinkingType == "none" || thinkingType == "0" {
		thinkingType = "disabled"
		if updated, errSet := sjson.SetBytes(out, "extra_body.thinking.type", thinkingType); errSet == nil {
			out = updated
		}
	} else if thinkingType != "disabled" && thinkingType != "adaptive" {
		thinkingType = "adaptive"
		if updated, errSet := sjson.SetBytes(out, "extra_body.thinking.type", thinkingType); errSet == nil {
			out = updated
		}
	}
	if thinkingType != "disabled" {
		if updated, errSet := sjson.SetBytes(out, "reasoning_split", true); errSet == nil {
			out = updated
		}
	}
	return out
}

func normalizeMiniMaxM3ToolChoice(payload []byte) []byte {
	toolChoiceType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "tool_choice.type").String()))
	if toolChoiceType != "any" && toolChoiceType != "tool" {
		return payload
	}
	out, errSet := sjson.SetBytes(payload, "tool_choice.type", "auto")
	if errSet != nil {
		return payload
	}
	return out
}

func removeMiniMaxM3UnsupportedThinkingFields(payload []byte) []byte {
	out := payload
	for _, path := range []string{"output_config.effort", "thinking.budget_tokens"} {
		if !gjson.GetBytes(out, path).Exists() {
			continue
		}
		updated, errDelete := sjson.DeleteBytes(out, path)
		if errDelete != nil {
			continue
		}
		out = updated
	}
	if outputConfig := gjson.GetBytes(out, "output_config"); outputConfig.Exists() && outputConfig.IsObject() && len(outputConfig.Map()) == 0 {
		if updated, errDeleteOutputConfig := sjson.DeleteBytes(out, "output_config"); errDeleteOutputConfig == nil {
			out = updated
		}
	}
	return out
}

func isMiniMaxM3Model(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	return strings.Contains(normalized, "minimax") && strings.Contains(normalized, "m3")
}

func isMiniMaxCompatModel(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	return strings.Contains(normalized, "minimax")
}

func buildOpenAICompatRetryRequest(ctx context.Context, url string, apiKey string, payload []byte, attrs map[string]string, stream bool) (*http.Request, error) {
	retryReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	retryReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		retryReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	retryReq.Header.Set("User-Agent", "cli-proxy-openai-compat")
	util.ApplyCustomHeadersFromAttrs(retryReq, attrs)
	if stream {
		retryReq.Header.Set("Accept", "text/event-stream")
		retryReq.Header.Set("Cache-Control", "no-cache")
	}
	return retryReq, nil
}

func shouldAttemptOpenAICompat2013Recovery(body []byte, httpStatusCode int, model string, payload []byte) bool {
	if isContextWindowExceeded(body, httpStatusCode) {
		return true
	}
	if !isMiniMaxCompatModel(model) || !isMiniMaxAmbiguous2013Error(body, httpStatusCode) {
		return false
	}
	return len(payload) > 1<<20 || hasOpenAICompatTools(payload)
}

func isMiniMaxAmbiguous2013Error(body []byte, httpStatusCode int) bool {
	if len(body) == 0 {
		return false
	}
	if httpCode := gjson.GetBytes(body, "http_code"); httpCode.Exists() && httpCode.Int() == 400 {
		httpStatusCode = 400
	} else if httpCode := gjson.GetBytes(body, "error.http_code"); httpCode.Exists() && httpCode.Int() == 400 {
		httpStatusCode = 400
	}
	if httpStatusCode != http.StatusBadRequest {
		return false
	}
	for _, path := range []string{"error.code", "code"} {
		if code := gjson.GetBytes(body, path); code.Exists() && code.Int() == 2013 {
			return true
		}
	}
	msg := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "error.message").String()))
	if msg == "" {
		msg = strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "message").String()))
	}
	return strings.Contains(msg, "2013") && strings.Contains(msg, "invalid params")
}

func hasOpenAICompatTools(payload []byte) bool {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return false
	}
	tools := gjson.GetBytes(payload, "tools")
	if tools.IsArray() && len(tools.Array()) > 0 {
		return true
	}
	return gjson.GetBytes(payload, "tool_choice").Exists() || gjson.GetBytes(payload, "tool_functions").Exists()
}

func removeUnsupportedClaudeBuiltinToolsForOpenAICompat(payload []byte, original []byte) []byte {
	builtinNames := claudeBuiltinToolNamesFromOriginalRequest(original)
	if len(builtinNames) == 0 || len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload
	}
	tools := gjson.GetBytes(payload, "tools")
	if !tools.IsArray() || len(tools.Array()) == 0 {
		return payload
	}

	out := payload
	keptTools := make([]any, 0, len(tools.Array()))
	removed := 0
	for _, tool := range tools.Array() {
		name := strings.TrimSpace(tool.Get("function.name").String())
		if name == "" {
			name = strings.TrimSpace(tool.Get("name").String())
		}
		if _, ok := builtinNames[name]; ok {
			removed++
			continue
		}
		keptTools = append(keptTools, tool.Value())
	}
	if removed == 0 {
		return payload
	}

	var errSet error
	if len(keptTools) == 0 {
		if next, errDelete := sjson.DeleteBytes(out, "tools"); errDelete == nil {
			out = next
		}
		if next, errDelete := sjson.DeleteBytes(out, "tool_choice"); errDelete == nil {
			out = next
		}
	} else {
		out, errSet = sjson.SetBytes(out, "tools", keptTools)
		if errSet != nil {
			return payload
		}
		if choiceName := strings.TrimSpace(gjson.GetBytes(out, "tool_choice.function.name").String()); choiceName != "" {
			if _, ok := builtinNames[choiceName]; ok {
				if next, errDelete := sjson.DeleteBytes(out, "tool_choice"); errDelete == nil {
					out = next
				}
			}
		}
	}

	log.WithField("removed_builtin_tools", removed).
		Debug("openai compat executor: removed unsupported Claude built-in tools")
	return out
}

func claudeBuiltinToolNamesFromOriginalRequest(original []byte) map[string]struct{} {
	if len(original) == 0 || !gjson.ValidBytes(original) {
		return nil
	}
	tools := gjson.GetBytes(original, "tools")
	if !tools.IsArray() {
		return nil
	}
	out := make(map[string]struct{})
	for _, tool := range tools.Array() {
		if tool.Get("input_schema").Exists() {
			continue
		}
		if name := claudeBuiltinToolName(tool); name != "" {
			out[name] = struct{}{}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func claudeBuiltinToolName(tool gjson.Result) string {
	toolType := strings.ToLower(strings.TrimSpace(tool.Get("type").String()))
	if toolType == "" {
		return ""
	}
	prefixName := claudeBuiltinToolNameFromType(toolType)
	if prefixName == "" {
		return ""
	}
	name := strings.TrimSpace(tool.Get("name").String())
	if name != "" {
		return name
	}
	return prefixName
}

func claudeBuiltinToolNameFromType(toolType string) string {
	for _, prefix := range []string{"web_search", "code_execution", "text_editor", "computer"} {
		if strings.HasPrefix(toolType, prefix) {
			return prefix
		}
	}
	return ""
}

// ensureDeepSeekToolMessageNames fills the legacy tool message name field.
// DeepSeek v4 currently rejects some OpenAI-compatible tool result messages
// without name even when tool_call_id is present.
func ensureDeepSeekToolMessageNames(payload []byte) ([]byte, error) {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload, nil
	}

	messages := gjson.GetBytes(payload, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return payload, nil
	}

	toolNames := make(map[string]string)
	for _, msg := range messages.Array() {
		if strings.TrimSpace(msg.Get("role").String()) != "assistant" {
			continue
		}
		toolCalls := msg.Get("tool_calls")
		if !toolCalls.IsArray() {
			continue
		}
		for _, toolCall := range toolCalls.Array() {
			toolCallID := strings.TrimSpace(toolCall.Get("id").String())
			if toolCallID == "" {
				toolCallID = strings.TrimSpace(toolCall.Get("call_id").String())
			}
			if toolCallID == "" {
				continue
			}
			name := strings.TrimSpace(toolCall.Get("function.name").String())
			if name == "" {
				name = openAICompatFallbackToolName(toolCallID)
			}
			toolNames[toolCallID] = name
		}
	}

	out := payload
	patched := 0
	for msgIdx, msg := range messages.Array() {
		if strings.TrimSpace(msg.Get("role").String()) != "tool" {
			continue
		}
		if strings.TrimSpace(msg.Get("name").String()) != "" {
			continue
		}
		toolCallID := strings.TrimSpace(msg.Get("tool_call_id").String())
		if toolCallID == "" {
			toolCallID = strings.TrimSpace(msg.Get("call_id").String())
		}
		if toolCallID == "" {
			continue
		}
		name := toolNames[toolCallID]
		if name == "" {
			name = openAICompatFallbackToolName(toolCallID)
		}
		next, err := sjson.SetBytes(out, fmt.Sprintf("messages.%d.name", msgIdx), name)
		if err != nil {
			return payload, fmt.Errorf("openai compat executor: failed to set deepseek tool message name: %w", err)
		}
		out = next
		patched++
	}

	if patched > 0 {
		log.WithField("patched_tool_message_names", patched).
			Debug("openai compat executor: ensured tool message names for deepseek model")
	}

	return out, nil
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

		hasToolCalls := msg.Get("tool_calls").Exists() && msg.Get("tool_calls").IsArray() && len(msg.Get("tool_calls").Array()) > 0
		reasoningText := fallbackDeepSeekReasoningForAssistant(msg, hasToolCalls, hasLatestReasoning, latestReasoning)
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

type openAICompatToolRepairStats struct {
	MovedToolMessages             int
	SynthesizedToolMessages       int
	DroppedOrphanToolMessages     int
	PreservedOrphanToolMessages   int
	NormalizedToolContents        int
	NormalizedToolArguments       int
	PatchedToolMessageIDs         int
	MergedAssistantMessages       int
	PatchedAssistantToolCallIDs   int
	PatchedAssistantToolCallNames int
	NormalizedAssistantContent    int
}

func (s openAICompatToolRepairStats) changed() bool {
	return s.MovedToolMessages > 0 ||
		s.SynthesizedToolMessages > 0 ||
		s.DroppedOrphanToolMessages > 0 ||
		s.PreservedOrphanToolMessages > 0 ||
		s.NormalizedToolContents > 0 ||
		s.NormalizedToolArguments > 0 ||
		s.PatchedToolMessageIDs > 0 ||
		s.MergedAssistantMessages > 0 ||
		s.PatchedAssistantToolCallIDs > 0 ||
		s.PatchedAssistantToolCallNames > 0 ||
		s.NormalizedAssistantContent > 0
}

type openAICompatToolMessageRef struct {
	index int
	raw   json.RawMessage
}

func normalizeOpenAICompatToolMessages(payload []byte) ([]byte, error) {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload, nil
	}

	messages := gjson.GetBytes(payload, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return payload, nil
	}

	var rawMessages []json.RawMessage
	if errUnmarshal := json.Unmarshal([]byte(messages.Raw), &rawMessages); errUnmarshal != nil {
		return payload, fmt.Errorf("openai compat executor: failed to parse messages for tool-call normalization: %w", errUnmarshal)
	}

	normalizedMessages, stats, errNormalize := normalizeOpenAICompatMessageSequence(rawMessages)
	if errNormalize != nil {
		return payload, errNormalize
	}
	if !stats.changed() {
		return payload, nil
	}

	messagesRaw, errMarshal := json.Marshal(normalizedMessages)
	if errMarshal != nil {
		return payload, fmt.Errorf("openai compat executor: failed to marshal normalized messages: %w", errMarshal)
	}
	out, errSet := sjson.SetRawBytes(payload, "messages", messagesRaw)
	if errSet != nil {
		return payload, fmt.Errorf("openai compat executor: failed to set normalized messages: %w", errSet)
	}

	log.WithFields(log.Fields{
		"moved_tool_messages":               stats.MovedToolMessages,
		"synthesized_tool_messages":         stats.SynthesizedToolMessages,
		"dropped_orphan_tool_messages":      stats.DroppedOrphanToolMessages,
		"preserved_orphan_tool_messages":    stats.PreservedOrphanToolMessages,
		"normalized_tool_contents":          stats.NormalizedToolContents,
		"normalized_tool_arguments":         stats.NormalizedToolArguments,
		"patched_tool_message_ids":          stats.PatchedToolMessageIDs,
		"merged_assistant_messages":         stats.MergedAssistantMessages,
		"patched_assistant_tool_call_ids":   stats.PatchedAssistantToolCallIDs,
		"patched_assistant_tool_call_names": stats.PatchedAssistantToolCallNames,
		"normalized_assistant_content":      stats.NormalizedAssistantContent,
	}).Debug("openai compat executor: normalized tool-call message sequence")

	return out, nil
}

func normalizeOpenAICompatMessageSequence(rawMessages []json.RawMessage) ([]json.RawMessage, openAICompatToolRepairStats, error) {
	stats := openAICompatToolRepairStats{}
	if len(rawMessages) == 0 {
		return rawMessages, stats, nil
	}

	toolRefs := collectOpenAICompatToolMessageRefs(rawMessages)
	usedTools := make(map[int]bool)
	preservedTools := make(map[int]bool)
	skippedMessages := make(map[int]bool)
	normalized := make([]json.RawMessage, 0, len(rawMessages))

	for msgIdx, raw := range rawMessages {
		if skippedMessages[msgIdx] {
			continue
		}

		role := openAICompatMessageRole(raw)
		if role == "tool" {
			if openAICompatToolMessageReferencedByFutureAssistant(rawMessages, msgIdx, raw) {
				continue
			}
			orphanMessage, ok := convertOpenAICompatOrphanToolMessage(raw)
			if ok {
				normalized = append(normalized, orphanMessage)
				preservedTools[msgIdx] = true
				stats.PreservedOrphanToolMessages++
				continue
			}
			continue
		}

		if role != "assistant" || !openAICompatHasToolCalls(raw) {
			normalized = append(normalized, append(json.RawMessage(nil), raw...))
			continue
		}

		assistant, toolCallIDs, assistantStats, errNormalize := normalizeOpenAICompatAssistantToolCalls(raw, msgIdx)
		if errNormalize != nil {
			return rawMessages, stats, errNormalize
		}
		stats.PatchedAssistantToolCallIDs += assistantStats.PatchedAssistantToolCallIDs
		stats.PatchedAssistantToolCallNames += assistantStats.PatchedAssistantToolCallNames
		stats.NormalizedAssistantContent += assistantStats.NormalizedAssistantContent
		stats.NormalizedToolArguments += assistantStats.NormalizedToolArguments

		if len(toolCallIDs) == 0 {
			if openAICompatMessageContentString(assistant) != "" {
				normalized = append(normalized, assistant)
			}
			continue
		}

		var merged int
		assistant, merged, errNormalize = mergeOpenAICompatAssistantMessagesBeforeToolOutput(assistant, rawMessages, skippedMessages, toolRefs, toolCallIDs, msgIdx)
		if errNormalize != nil {
			return rawMessages, stats, errNormalize
		}
		stats.MergedAssistantMessages += merged
		normalized = append(normalized, assistant)

		for toolPos, toolCallID := range toolCallIDs {
			ref, ok := firstUnusedOpenAICompatToolRef(toolRefs[toolCallID], usedTools)
			if !ok {
				normalized = append(normalized, synthesizeOpenAICompatToolMessage(toolCallID))
				stats.SynthesizedToolMessages++
				continue
			}

			toolMessage, toolStats, errTool := normalizeOpenAICompatToolMessage(ref.raw, toolCallID)
			if errTool != nil {
				return rawMessages, stats, errTool
			}
			stats.NormalizedToolContents += toolStats.NormalizedToolContents
			stats.PatchedToolMessageIDs += toolStats.PatchedToolMessageIDs
			if ref.index != msgIdx+1+toolPos {
				stats.MovedToolMessages++
			}
			usedTools[ref.index] = true
			skippedMessages[ref.index] = true
			normalized = append(normalized, toolMessage)
		}
	}

	for msgIdx, raw := range rawMessages {
		if openAICompatMessageRole(raw) == "tool" && !usedTools[msgIdx] && !preservedTools[msgIdx] {
			stats.DroppedOrphanToolMessages++
		}
	}

	return normalized, stats, nil
}

func openAICompatToolMessageReferencedByFutureAssistant(rawMessages []json.RawMessage, msgIdx int, raw json.RawMessage) bool {
	ids := openAICompatToolMessageIDs(raw)
	if len(ids) == 0 {
		return false
	}
	for idx := msgIdx + 1; idx < len(rawMessages); idx++ {
		if openAICompatMessageRole(rawMessages[idx]) != "assistant" {
			continue
		}
		toolCalls := gjson.GetBytes(rawMessages[idx], "tool_calls")
		if !toolCalls.IsArray() {
			continue
		}
		for _, toolCall := range toolCalls.Array() {
			toolCallID := strings.TrimSpace(toolCall.Get("id").String())
			if toolCallID == "" {
				toolCallID = strings.TrimSpace(toolCall.Get("call_id").String())
			}
			if toolCallID != "" && ids[toolCallID] {
				return true
			}
		}
	}
	return false
}

func openAICompatToolMessageIDs(raw json.RawMessage) map[string]bool {
	ids := make(map[string]bool)
	for _, id := range []string{
		strings.TrimSpace(gjson.GetBytes(raw, "tool_call_id").String()),
		strings.TrimSpace(gjson.GetBytes(raw, "call_id").String()),
	} {
		if id != "" {
			ids[id] = true
		}
	}
	return ids
}

func convertOpenAICompatOrphanToolMessage(raw json.RawMessage) (json.RawMessage, bool) {
	content := openAICompatToolMessageContentString(raw)
	if strings.TrimSpace(content) == "" {
		return nil, false
	}
	toolCallID := strings.TrimSpace(gjson.GetBytes(raw, "tool_call_id").String())
	if toolCallID == "" {
		toolCallID = strings.TrimSpace(gjson.GetBytes(raw, "call_id").String())
	}
	prefix := "Tool result"
	if toolCallID != "" {
		prefix = fmt.Sprintf("Tool result (%s)", toolCallID)
	}
	msg := []byte(`{"role":"user","content":""}`)
	msg, _ = sjson.SetBytes(msg, "content", prefix+":\n"+content)
	return json.RawMessage(msg), true
}

func collectOpenAICompatToolMessageRefs(rawMessages []json.RawMessage) map[string][]openAICompatToolMessageRef {
	refs := make(map[string][]openAICompatToolMessageRef)
	for msgIdx, raw := range rawMessages {
		if openAICompatMessageRole(raw) != "tool" {
			continue
		}
		toolCallID := strings.TrimSpace(gjson.GetBytes(raw, "tool_call_id").String())
		callID := strings.TrimSpace(gjson.GetBytes(raw, "call_id").String())
		if toolCallID != "" {
			refs[toolCallID] = append(refs[toolCallID], openAICompatToolMessageRef{index: msgIdx, raw: raw})
		}
		if callID != "" && callID != toolCallID {
			refs[callID] = append(refs[callID], openAICompatToolMessageRef{index: msgIdx, raw: raw})
		}
	}
	return refs
}

func normalizeOpenAICompatAssistantToolCalls(raw json.RawMessage, msgIdx int) (json.RawMessage, []string, openAICompatToolRepairStats, error) {
	stats := openAICompatToolRepairStats{}
	out := append([]byte(nil), raw...)
	toolCalls := gjson.GetBytes(raw, "tool_calls")
	if !toolCalls.IsArray() {
		return json.RawMessage(out), nil, stats, nil
	}

	wrapper := []byte(`{"tool_calls":[]}`)
	toolCallIDs := make([]string, 0, len(toolCalls.Array()))
	for toolIdx, toolCall := range toolCalls.Array() {
		toolCallRaw := []byte(toolCall.Raw)
		toolCallID := strings.TrimSpace(toolCall.Get("id").String())
		if toolCallID == "" {
			toolCallID = strings.TrimSpace(toolCall.Get("call_id").String())
		}
		if toolCallID == "" {
			toolCallID = fmt.Sprintf("call_repaired_%d_%d", msgIdx, toolIdx)
		}
		var errSet error
		toolCallRaw, errSet = sjson.SetBytes(toolCallRaw, "id", toolCallID)
		if errSet != nil {
			return nil, nil, stats, fmt.Errorf("openai compat executor: failed to repair empty tool_call id: %w", errSet)
		}
		if strings.TrimSpace(toolCall.Get("id").String()) != toolCallID {
			stats.PatchedAssistantToolCallIDs++
		}
		toolCallRaw, errSet = sjson.SetBytes(toolCallRaw, "type", "function")
		if errSet != nil {
			return nil, nil, stats, fmt.Errorf("openai compat executor: failed to normalize tool_call type: %w", errSet)
		}
		if gjson.GetBytes(toolCallRaw, "call_id").Exists() {
			if next, errDelete := sjson.DeleteBytes(toolCallRaw, "call_id"); errDelete == nil {
				toolCallRaw = next
			}
		}

		arguments := toolCall.Get("function.arguments")
		normalizedArgs := normalizeOpenAICompatFunctionArguments(arguments)
		if !arguments.Exists() || arguments.Type != gjson.String || arguments.String() != normalizedArgs {
			toolCallRaw, errSet = sjson.SetBytes(toolCallRaw, "function.arguments", normalizedArgs)
			if errSet != nil {
				return nil, nil, stats, fmt.Errorf("openai compat executor: failed to normalize tool_call arguments: %w", errSet)
			}
			stats.NormalizedToolArguments++
		}

		functionName := strings.TrimSpace(toolCall.Get("function.name").String())
		if functionName == "" {
			// Some clients (notably those sending Claude- or Gemini-style
			// tool calls through the OpenAI-compat executor) emit assistant
			// tool_calls with an empty function.name. Upstream providers
			// (e.g. DeepSeek v4) reject such requests with 400 "invalid
			// tool_call function, function/name cannot be empty". Backfill
			// with a placeholder so the request is accepted; the upstream
			// still pairs the call with the matching tool result by id.
			placeholder := openAICompatFallbackToolName(toolCallID)
			if next, errSetName := sjson.SetBytes(toolCallRaw, "function.name", placeholder); errSetName == nil {
				toolCallRaw = next
				stats.PatchedAssistantToolCallNames++
			}
		}
		toolCallIDs = append(toolCallIDs, toolCallID)
		wrapper, errSet = sjson.SetRawBytes(wrapper, "tool_calls.-1", toolCallRaw)
		if errSet != nil {
			return nil, nil, stats, fmt.Errorf("openai compat executor: failed to append repaired tool_call: %w", errSet)
		}
	}

	var errSet error
	out, errSet = sjson.SetRawBytes(out, "tool_calls", []byte(gjson.GetBytes(wrapper, "tool_calls").Raw))
	if errSet != nil {
		return nil, nil, stats, fmt.Errorf("openai compat executor: failed to set repaired assistant tool_calls: %w", errSet)
	}

	content := gjson.GetBytes(out, "content")
	if !content.Exists() || content.Type == gjson.Null {
		out, errSet = sjson.SetBytes(out, "content", "")
		if errSet != nil {
			return nil, nil, stats, fmt.Errorf("openai compat executor: failed to normalize assistant content: %w", errSet)
		}
		stats.NormalizedAssistantContent++
	} else if content.Type != gjson.String {
		out, errSet = sjson.SetBytes(out, "content", openAICompatMessageContentString(json.RawMessage(out)))
		if errSet != nil {
			return nil, nil, stats, fmt.Errorf("openai compat executor: failed to stringify assistant content: %w", errSet)
		}
		stats.NormalizedAssistantContent++
	}

	return json.RawMessage(out), toolCallIDs, stats, nil
}

func mergeOpenAICompatAssistantMessagesBeforeToolOutput(assistant json.RawMessage, rawMessages []json.RawMessage, skipped map[int]bool, toolRefs map[string][]openAICompatToolMessageRef, toolCallIDs []string, msgIdx int) (json.RawMessage, int, error) {
	limit := len(rawMessages)
	for _, toolCallID := range toolCallIDs {
		for _, ref := range toolRefs[toolCallID] {
			if ref.index > msgIdx && ref.index < limit {
				limit = ref.index
			}
		}
	}
	if limit == len(rawMessages) {
		limit = msgIdx + 1
		for limit < len(rawMessages) {
			if openAICompatMessageRole(rawMessages[limit]) != "assistant" || openAICompatHasToolCalls(rawMessages[limit]) {
				break
			}
			limit++
		}
	}

	out := append(json.RawMessage(nil), assistant...)
	merged := 0
	for idx := msgIdx + 1; idx < limit; idx++ {
		if skipped[idx] {
			continue
		}
		if openAICompatMessageRole(rawMessages[idx]) != "assistant" || openAICompatHasToolCalls(rawMessages[idx]) {
			continue
		}
		next, changed, errMerge := appendOpenAICompatAssistantContent(out, rawMessages[idx])
		if errMerge != nil {
			return assistant, merged, errMerge
		}
		if changed {
			out = next
			merged++
		}
		skipped[idx] = true
	}
	return out, merged, nil
}

func appendOpenAICompatAssistantContent(base json.RawMessage, extra json.RawMessage) (json.RawMessage, bool, error) {
	extraText := openAICompatMessageContentString(extra)
	if extraText == "" {
		return base, false, nil
	}

	baseText := openAICompatMessageContentString(base)
	combined := extraText
	if baseText != "" {
		combined = baseText + "\n" + extraText
	}

	out, errSet := sjson.SetBytes([]byte(base), "content", combined)
	if errSet != nil {
		return base, false, fmt.Errorf("openai compat executor: failed to merge assistant content: %w", errSet)
	}
	return json.RawMessage(out), true, nil
}

func firstUnusedOpenAICompatToolRef(refs []openAICompatToolMessageRef, used map[int]bool) (openAICompatToolMessageRef, bool) {
	for _, ref := range refs {
		if used[ref.index] {
			continue
		}
		return ref, true
	}
	return openAICompatToolMessageRef{}, false
}

func normalizeOpenAICompatToolMessage(raw json.RawMessage, expectedToolCallID string) (json.RawMessage, openAICompatToolRepairStats, error) {
	stats := openAICompatToolRepairStats{}
	out := append([]byte(nil), raw...)
	currentID := strings.TrimSpace(gjson.GetBytes(out, "tool_call_id").String())
	if currentID != expectedToolCallID {
		var errSet error
		out, errSet = sjson.SetBytes(out, "tool_call_id", expectedToolCallID)
		if errSet != nil {
			return nil, stats, fmt.Errorf("openai compat executor: failed to set tool_call_id: %w", errSet)
		}
		stats.PatchedToolMessageIDs++
	}

	content := gjson.GetBytes(out, "content")
	if !content.Exists() {
		if output := gjson.GetBytes(out, "output"); output.Exists() {
			var errSet error
			out, errSet = sjson.SetBytes(out, "content", openAICompatJSONResultString(output))
			if errSet != nil {
				return nil, stats, fmt.Errorf("openai compat executor: failed to set tool content from output: %w", errSet)
			}
		} else {
			var errSet error
			out, errSet = sjson.SetBytes(out, "content", "")
			if errSet != nil {
				return nil, stats, fmt.Errorf("openai compat executor: failed to set empty tool content: %w", errSet)
			}
		}
		stats.NormalizedToolContents++
	} else if content.Type != gjson.String {
		var errSet error
		out, errSet = sjson.SetBytes(out, "content", openAICompatJSONResultString(content))
		if errSet != nil {
			return nil, stats, fmt.Errorf("openai compat executor: failed to stringify tool content: %w", errSet)
		}
		stats.NormalizedToolContents++
	}
	if gjson.GetBytes(out, "call_id").Exists() {
		if next, errDelete := sjson.DeleteBytes(out, "call_id"); errDelete == nil {
			out = next
		}
	}
	if gjson.GetBytes(out, "output").Exists() {
		if next, errDelete := sjson.DeleteBytes(out, "output"); errDelete == nil {
			out = next
		}
	}

	return json.RawMessage(out), stats, nil
}

func synthesizeOpenAICompatToolMessage(toolCallID string) json.RawMessage {
	msg := []byte(`{"role":"tool","tool_call_id":"","content":""}`)
	msg, _ = sjson.SetBytes(msg, "tool_call_id", toolCallID)
	content, errMarshal := json.Marshal(map[string]string{
		"error": fmt.Sprintf("tool result missing for tool_call_id %q", toolCallID),
	})
	if errMarshal != nil {
		msg, _ = sjson.SetBytes(msg, "content", "tool result missing")
		return json.RawMessage(msg)
	}
	msg, _ = sjson.SetBytes(msg, "content", string(content))
	return json.RawMessage(msg)
}

func openAICompatMessageRole(raw json.RawMessage) string {
	return strings.TrimSpace(gjson.GetBytes(raw, "role").String())
}

func openAICompatHasToolCalls(raw json.RawMessage) bool {
	toolCalls := gjson.GetBytes(raw, "tool_calls")
	return toolCalls.Exists() && toolCalls.IsArray() && len(toolCalls.Array()) > 0
}

func openAICompatMessageContentString(raw json.RawMessage) string {
	content := gjson.GetBytes(raw, "content")
	if !content.Exists() || content.Type == gjson.Null {
		return ""
	}
	if content.Type == gjson.String {
		return content.String()
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
		return strings.Join(parts, "\n")
	}
	return openAICompatJSONResultString(content)
}

func openAICompatToolMessageContentString(raw json.RawMessage) string {
	if content := gjson.GetBytes(raw, "content"); content.Exists() {
		return openAICompatMessageContentString(raw)
	}
	return openAICompatJSONResultString(gjson.GetBytes(raw, "output"))
}

func openAICompatJSONResultString(value gjson.Result) string {
	if !value.Exists() || value.Type == gjson.Null {
		return ""
	}
	if value.Type == gjson.String {
		return value.String()
	}
	return value.Raw
}

func normalizeOpenAICompatFunctionArguments(arguments gjson.Result) string {
	if !arguments.Exists() || arguments.Type == gjson.Null {
		return "{}"
	}

	raw := strings.TrimSpace(openAICompatJSONResultString(arguments))
	if raw == "" {
		return "{}"
	}
	if json.Valid([]byte(raw)) {
		return raw
	}

	wrapped, errMarshal := json.Marshal(map[string]string{"input": raw})
	if errMarshal != nil {
		return "{}"
	}
	return string(wrapped)
}

func openAICompatFallbackToolName(toolCallID string) string {
	const prefix = "tool_call_"
	trimmed := strings.TrimSpace(toolCallID)
	if trimmed == "" {
		return "tool_call"
	}

	var b strings.Builder
	b.Grow(len(prefix) + len(trimmed))
	b.WriteString(prefix)
	for i := 0; i < len(trimmed); i++ {
		c := trimmed[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('_')
	}
	name := b.String()
	if len(name) > 64 {
		return name[:64]
	}
	return name
}

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
						toolCallIDs[tcID] = i*1000 + j // encode assistant index in upper digits
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

// isContextWindowExceeded checks if the response body indicates a context
// window exceeded error (code 2013 or message containing "context window").
// Also checks httpStatusCode for robustness against upstream inconsistency.
func isContextWindowExceeded(body []byte, httpStatusCode int) bool {
	if len(body) == 0 {
		return false
	}

	// Check http_code field in body (One-API/decard.cc format may return 500 HTTP
	// status while body contains http_code=400). Check both "http_code" and "error.http_code".
	if httpCode := gjson.GetBytes(body, "http_code"); httpCode.Exists() && httpCode.Int() == 400 {
		httpStatusCode = 400
	} else if httpCode := gjson.GetBytes(body, "error.http_code"); httpCode.Exists() && httpCode.Int() == 400 {
		httpStatusCode = 400
	}

	// Must have either HTTP 400 status or 2013 error code
	if httpStatusCode != 400 {
		return false
	}

	// MiniMax native format: {"error":{"code":2013,...}}
	if code := gjson.GetBytes(body, "error.code"); code.Exists() && code.Int() == 2013 {
		return true
	}
	// One-API / decard.cc format: message contains "context window exceeds limit (2013)"
	msg := strings.ToLower(gjson.GetBytes(body, "error.message").String())
	if strings.Contains(msg, "context window") && strings.Contains(msg, "2013") {
		return true
	}
	return false
}

// compactMessagesForRetry keeps only the last maxItems messages from the
// payload's messages array to reduce context window usage.
func compactMessagesForRetry(payload []byte, maxItems int) []byte {
	// Try "messages" field first (chat completions format), then "input" (responses API format)
	msgField := "messages"
	messages := gjson.GetBytes(payload, msgField)
	if !messages.Exists() || !messages.IsArray() {
		msgField = "input"
		messages = gjson.GetBytes(payload, msgField)
		if !messages.Exists() || !messages.IsArray() {
			return payload
		}
	}
	arr := messages.Array()
	if len(arr) <= maxItems {
		return payload
	}
	// Keep system message if present, plus the last maxItems items
	var kept []gjson.Result
	for _, msg := range arr {
		role := strings.ToLower(strings.TrimSpace(msg.Get("role").String()))
		if role == "system" {
			kept = append(kept, msg)
		}
	}
	start := len(arr) - maxItems
	if start < 0 {
		start = 0
	}
	kept = append(kept, arr[start:]...)

	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, item := range kept {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.WriteString(item.Raw)
	}
	buf.WriteByte(']')
	out, err := sjson.SetRawBytes(payload, msgField, buf.Bytes())
	if err != nil {
		return payload
	}
	return out
}

// stripToolsFromPayload removes tools and tool_choice fields from a payload
// to reduce context window usage. This is useful as a first step before
// compacting messages when the context window is exceeded.
func stripToolsFromPayload(payload []byte) []byte {
	result := payload
	var err error
	result, err = sjson.DeleteBytes(result, "tools")
	if err != nil {
		return payload
	}
	result, err = sjson.DeleteBytes(result, "tool_choice")
	if err != nil {
		return payload
	}
	result, err = sjson.DeleteBytes(result, "tool_functions")
	if err != nil {
		return payload
	}
	return result
}

// truncateMessageContent truncates the content of the last message
// in the payload when it exceeds maxChars. This is a last-resort fallback
// when even keeping a single message exceeds the context window.
// It truncates the content and appends a truncation marker.
func truncateMessageContent(payload []byte, maxChars int) []byte {
	// Try "messages" field first (chat completions format)
	messages := gjson.GetBytes(payload, "messages")
	if messages.IsArray() {
		return truncateMessagesArray(payload, "messages", messages.Array(), maxChars)
	}
	// Also try "input" field (responses API format)
	input := gjson.GetBytes(payload, "input")
	if input.IsArray() {
		return truncateMessagesArray(payload, "input", input.Array(), maxChars)
	}
	return payload
}

func truncateMessagesArray(payload []byte, field string, msgs []gjson.Result, maxChars int) []byte {
	if len(msgs) == 0 {
		return payload
	}

	modified := false
	for i := range msgs {
		content := msgs[i].Get("content")
		if !content.Exists() {
			continue
		}
		contentStr := content.String()
		if len(contentStr) <= maxChars {
			continue
		}
		// Truncate: keep first 40% and last 40% to preserve context at both ends
		headSize := maxChars * 40 / 100
		tailSize := maxChars - headSize - 50 // reserve space for truncation marker
		if tailSize < 100 {
			headSize = maxChars - 150
			tailSize = 100
		}
		marker := fmt.Sprintf("\n\n...[truncated %d chars, context window limit]...\n\n", len(contentStr)-maxChars)
		truncated := contentStr[:headSize] + marker + contentStr[len(contentStr)-tailSize:]
		payload, _ = sjson.SetBytes(payload, field+"."+fmt.Sprint(i)+".content", truncated)
		modified = true
	}
	if !modified {
		return payload
	}
	return payload
}
