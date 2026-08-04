package openai

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	"github.com/tidwall/gjson"
)

type responsesHTTPCaptureExecutor struct {
	payloads  [][]byte
	replyIDs  []string
	errByCall map[int]error
}

func (e *responsesHTTPCaptureExecutor) Identifier() string { return "codex" }

func (e *responsesHTTPCaptureExecutor) Execute(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	e.payloads = append(e.payloads, append([]byte(nil), req.Payload...))
	call := len(e.payloads)
	if err := e.errByCall[call]; err != nil {
		return coreexecutor.Response{}, err
	}
	replyID := "resp-http"
	if len(e.replyIDs) >= call {
		replyID = e.replyIDs[call-1]
	} else if len(e.replyIDs) > 0 {
		replyID = e.replyIDs[len(e.replyIDs)-1]
	}
	return coreexecutor.Response{
		Payload: []byte(`{"id":"` + replyID + `","output":[{"type":"message","id":"assistant-out","role":"assistant","content":[{"type":"output_text","text":"done"}]}]}`),
	}, nil
}

func (e *responsesHTTPCaptureExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (e *responsesHTTPCaptureExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *responsesHTTPCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *responsesHTTPCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestResponsesHTTPContinueUsesPreviousResponseIDForFullTranscriptReplay(t *testing.T) {
	gin.SetMode(gin.TestMode)
	defaultResponsesHTTPSessionCache = newResponsesHTTPSessionCache(0)

	executor := &responsesHTTPCaptureExecutor{replyIDs: []string{"resp-1", "resp-2"}}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{
		ID:       "auth-codex-http",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"websockets": true},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses", h.Responses)

	firstBody := `{"model":"test-model","input":[{"type":"message","role":"user","id":"msg-1","content":"hello"}]}`
	firstReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(firstBody))
	firstReq.Header.Set("X-Client-Request-Id", "sess-http-1")
	firstResp := httptest.NewRecorder()
	router.ServeHTTP(firstResp, firstReq)
	if firstResp.Code != http.StatusOK {
		t.Fatalf("first status = %d, want 200, body=%s", firstResp.Code, firstResp.Body.String())
	}

	secondBody := `{"model":"test-model","input":[{"type":"message","role":"user","id":"msg-1","content":"hello"},{"type":"message","id":"assistant-out","role":"assistant","content":[{"type":"output_text","text":"done"}]},{"type":"message","role":"user","id":"msg-2","content":"继续"}]}`
	secondReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(secondBody))
	secondReq.Header.Set("X-Client-Request-Id", "sess-http-1")
	secondResp := httptest.NewRecorder()
	router.ServeHTTP(secondResp, secondReq)
	if secondResp.Code != http.StatusOK {
		t.Fatalf("second status = %d, want 200, body=%s", secondResp.Code, secondResp.Body.String())
	}

	if len(executor.payloads) != 2 {
		t.Fatalf("payload count = %d, want 2", len(executor.payloads))
	}
	forwarded := executor.payloads[1]
	if got := gjson.GetBytes(forwarded, "previous_response_id").String(); got != "resp-1" {
		t.Fatalf("previous_response_id = %q, want resp-1; payload=%s", got, forwarded)
	}
	input := gjson.GetBytes(forwarded, "input").Array()
	if len(input) != 1 {
		t.Fatalf("input len = %d, want 1; payload=%s", len(input), forwarded)
	}
	if got := input[0].Get("id").String(); got != "msg-2" {
		t.Fatalf("incremental input id = %q, want msg-2; payload=%s", got, forwarded)
	}
}

func TestResponsesHTTPFailedTurnDoesNotAdvanceSessionSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	defaultResponsesHTTPSessionCache = newResponsesHTTPSessionCache(0)

	executor := &responsesHTTPCaptureExecutor{
		replyIDs: []string{"resp-1", "resp-2", "resp-3"},
		errByCall: map[int]error{
			2: responsesHTTPStatusError{status: http.StatusBadRequest, msg: `{"error":{"message":"context window exceeds limit (2013)"}}`},
		},
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{
		ID:       "auth-codex-http-retry",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"websockets": true},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses", h.Responses)

	requests := []string{
		`{"model":"test-model","input":[{"type":"message","role":"user","id":"msg-1","content":"hello"}]}`,
		`{"model":"test-model","input":[{"type":"message","role":"user","id":"msg-2","content":"继续"}]}`,
		`{"model":"test-model","input":[{"type":"message","role":"user","id":"msg-3","content":"再继续"}]}`,
	}

	for i, body := range requests {
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
		req.Header.Set("X-Client-Request-Id", "sess-http-retry")
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		if i == 1 {
			if resp.Code != http.StatusBadRequest {
				t.Fatalf("failed turn status = %d, want 400, body=%s", resp.Code, resp.Body.String())
			}
			continue
		}
		if resp.Code != http.StatusOK {
			t.Fatalf("turn %d status = %d, want 200, body=%s", i+1, resp.Code, resp.Body.String())
		}
	}

	if len(executor.payloads) != 3 {
		t.Fatalf("payload count = %d, want 3", len(executor.payloads))
	}
	thirdPayload := executor.payloads[2]
	if got := gjson.GetBytes(thirdPayload, "previous_response_id").String(); got != "resp-1" {
		t.Fatalf("third previous_response_id = %q, want resp-1; payload=%s", got, thirdPayload)
	}
	input := gjson.GetBytes(thirdPayload, "input").Array()
	if len(input) != 1 || input[0].Get("id").String() != "msg-3" {
		t.Fatalf("third incremental input mismatch: %s", thirdPayload)
	}
}

func TestNormalizeResponsesHTTPCompactionFallbackMergesWhenReplayUnsupported(t *testing.T) {
	snapshot := &responsesHTTPSessionSnapshot{
		RequestSnapshot: []byte(`{"model":"test-model","input":[
			{"type":"message","role":"user","id":"msg-1","content":"original prompt"},
			{"type":"message","role":"assistant","id":"msg-2","content":"original reply"},
			{"type":"function_call","id":"fc-1","call_id":"call-1","name":"bash","arguments":"{}"},
			{"type":"function_call_output","id":"fco-1","call_id":"call-1","output":"old result"}
		]}`),
		ResponseOutput: []byte(`[
			{"type":"message","role":"assistant","id":"msg-3","content":"next reply"}
		]`),
		ResponseID: "resp-1",
	}

	raw := []byte(`{"model":"test-model","input":[
		{"type":"message","role":"user","id":"msg-1c","content":"compacted user msg"},
		{"type":"compaction","encrypted_content":"conversation summary"}
	]}`)

	normalized, next, errMsg := normalizeResponsesHTTPRequest(raw, snapshot, false, false)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	if !bytesEqual(next, normalized) {
		t.Fatalf("snapshot must match normalized request")
	}

	input := gjson.GetBytes(normalized, "input").Array()
	if len(input) != 6 {
		t.Fatalf("input len = %d, want 6; payload=%s", len(input), normalized)
	}
	wantIDs := []string{"msg-1", "msg-2", "fc-1", "fco-1", "msg-3", "msg-1c"}
	for i, want := range wantIDs {
		if got := input[i].Get("id").String(); got != want {
			t.Fatalf("input[%d].id = %q, want %q; payload=%s", i, got, want, normalized)
		}
	}
	for _, item := range input {
		if got := item.Get("type").String(); got == "compaction" || got == "compaction_summary" {
			t.Fatalf("compaction item must be stripped in fallback merge: %s", item.Raw)
		}
	}
}

func TestNormalizeResponsesHTTPReplacesCodexLocalCompactionTranscript(t *testing.T) {
	snapshot := &responsesHTTPSessionSnapshot{
		RequestSnapshot: []byte(`{"model":"test-model","input":[
			{"type":"message","role":"user","id":"old-user","content":"old prompt"}
		]}`),
		ResponseOutput: []byte(`[
			{"type":"image_generation_call","id":"ig_stale","status":"completed","output_format":"png"}
		]`),
		ResponseID: "resp-1",
	}
	raw := []byte(fmt.Sprintf(`{"model":"test-model","input":[
		{"type":"additional_tools","role":"developer","tools":[]},
		{"type":"message","role":"developer","content":[{"type":"input_text","text":"current instructions"}]},
		{"type":"message","role":"user","content":[{"type":"input_text","text":%q}]},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}
	]}`, codexLocalCompactionSummaryPrefix+"\nCompacted summary."))

	normalized, next, errMsg := normalizeResponsesHTTPRequest(raw, snapshot, true, true)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	if got, want := gjson.GetBytes(normalized, "input").Raw, gjson.GetBytes(raw, "input").Raw; got != want {
		t.Fatalf("compacted input was not preserved:\n got: %s\nwant: %s", got, want)
	}
	if bytes.Contains(normalized, []byte("ig_stale")) || bytes.Contains(normalized, []byte("old-user")) {
		t.Fatalf("compacted request contains stale session history: %s", normalized)
	}
	if !bytes.Equal(next, canonicalizeResponsesSnapshotRequest(raw, snapshot.RequestSnapshot)) {
		t.Fatalf("next snapshot must contain only the compacted transcript: %s", next)
	}
}

func TestCodexLocalCompactionSummaryReplacementSemantics(t *testing.T) {
	compactedInput := gjson.Parse(fmt.Sprintf(`[
		{"type":"additional_tools","role":"developer","tools":[{"type":"custom","name":"exec"}]},
		{"type":"message","role":"developer","content":[{"type":"input_text","text":"current instructions"}]},
		{"type":"message","role":"user","content":[{"type":"input_text","text":%q}]},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}
	]`, codexLocalCompactionSummaryPrefix+"\nCompacted summary."))

	if !inputHasCodexLocalCompactionSummary(compactedInput) {
		t.Fatal("Codex local compaction summary was not detected")
	}
	if !shouldReplaceWebsocketTranscript([]byte(`{"type":"response.create"}`), compactedInput) {
		t.Fatal("response.create with local compaction summary must replace websocket history")
	}
	for _, raw := range []string{
		`{"type":"response.append"}`,
		`{"type":"response.create","previous_response_id":""}`,
		`{"type":"response.create","previous_response_id":null}`,
		`{"type":"response.create","previous_response_id":"resp-1"}`,
	} {
		if shouldReplaceWebsocketTranscript([]byte(raw), compactedInput) {
			t.Fatalf("request must not use the local compaction replacement rule: %s", raw)
		}
	}

	ordinaryInput := gjson.Parse(`[
		{"type":"message","role":"developer","content":"Please summarize future messages."},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"Please create a compacted summary."}]}
	]`)
	if inputHasCodexLocalCompactionSummary(ordinaryInput) {
		t.Fatal("ordinary user/developer input must not match local compaction")
	}
}

func bytesEqual(left []byte, right []byte) bool { return bytes.Equal(left, right) }

type responsesHTTPStatusError struct {
	status int
	msg    string
}

func (e responsesHTTPStatusError) Error() string { return e.msg }

func (e responsesHTTPStatusError) StatusCode() int { return e.status }
