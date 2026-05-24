package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const responsesHTTPSessionTTL = 30 * time.Minute

var defaultResponsesHTTPSessionCache = newResponsesHTTPSessionCache(0)

type responsesHTTPSessionCache struct {
	mu       sync.Mutex
	ttl      time.Duration
	sessions map[string]*responsesHTTPSessionState
}

type responsesHTTPSessionState struct {
	lastSeen        time.Time
	requestSnapshot []byte
	responseOutput  []byte
	responseID      string
}

type responsesHTTPSessionSnapshot struct {
	RequestSnapshot []byte
	ResponseOutput  []byte
	ResponseID      string
}

func newResponsesHTTPSessionCache(ttl time.Duration) *responsesHTTPSessionCache {
	if ttl <= 0 {
		ttl = responsesHTTPSessionTTL
	}
	return &responsesHTTPSessionCache{
		ttl:      ttl,
		sessions: make(map[string]*responsesHTTPSessionState),
	}
}

func (c *responsesHTTPSessionCache) load(sessionKey string) *responsesHTTPSessionSnapshot {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" || c == nil {
		return nil
	}

	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cleanupLocked(now)

	state, ok := c.sessions[sessionKey]
	if !ok || state == nil {
		return nil
	}
	state.lastSeen = now
	return &responsesHTTPSessionSnapshot{
		RequestSnapshot: cloneResponsesBytes(state.requestSnapshot),
		ResponseOutput:  cloneResponsesBytes(state.responseOutput),
		ResponseID:      state.responseID,
	}
}

func (c *responsesHTTPSessionCache) store(sessionKey string, requestSnapshot []byte, responseOutput []byte, responseID string) {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" || c == nil || len(requestSnapshot) == 0 {
		return
	}

	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cleanupLocked(now)
	c.sessions[sessionKey] = &responsesHTTPSessionState{
		lastSeen:        now,
		requestSnapshot: cloneResponsesBytes(requestSnapshot),
		responseOutput:  cloneResponsesBytes(responseOutput),
		responseID:      strings.TrimSpace(responseID),
	}
}

func (c *responsesHTTPSessionCache) cleanupLocked(now time.Time) {
	if c == nil || c.ttl <= 0 {
		return
	}
	for key, state := range c.sessions {
		if state == nil || now.Sub(state.lastSeen) > c.ttl {
			delete(c.sessions, key)
		}
	}
}

func cloneResponsesBytes(src []byte) []byte {
	if len(src) == 0 {
		return nil
	}
	return bytes.Clone(src)
}

func responsesHTTPSessionKey(req *http.Request) string {
	return websocketDownstreamSessionKey(req)
}

func (h *OpenAIResponsesAPIHandler) prepareResponsesSessionRequest(c *gin.Context, rawJSON []byte) ([]byte, []byte, string, *interfaces.ErrorMessage) {
	if c == nil || c.Request == nil {
		return rawJSON, nil, "", nil
	}

	sessionKey := responsesHTTPSessionKey(c.Request)
	if sessionKey == "" {
		return rawJSON, nil, "", nil
	}

	snapshot := defaultResponsesHTTPSessionCache.load(sessionKey)
	if snapshot != nil {
		currentModel := strings.TrimSpace(gjson.GetBytes(rawJSON, "model").String())
		previousModel := strings.TrimSpace(gjson.GetBytes(snapshot.RequestSnapshot, "model").String())
		if currentModel != "" && previousModel != "" && currentModel != previousModel {
			snapshot = nil
		}
	}

	modelName := strings.TrimSpace(gjson.GetBytes(rawJSON, "model").String())
	if modelName == "" && snapshot != nil {
		modelName = strings.TrimSpace(gjson.GetBytes(snapshot.RequestSnapshot, "model").String())
	}
	allowIncremental := h.websocketUpstreamSupportsIncrementalInputForModel(modelName)
	allowCompactionReplay := h.websocketUpstreamSupportsCompactionReplayForModel(modelName)

	requestJSON, requestSnapshot, errMsg := normalizeResponsesHTTPRequest(rawJSON, snapshot, allowIncremental, allowCompactionReplay)
	if errMsg != nil {
		return nil, nil, sessionKey, errMsg
	}
	return requestJSON, requestSnapshot, sessionKey, nil
}

func normalizeResponsesHTTPRequest(
	rawJSON []byte,
	snapshot *responsesHTTPSessionSnapshot,
	allowIncrementalInputWithPreviousResponseID bool,
	allowCompactionReplay bool,
) ([]byte, []byte, *interfaces.ErrorMessage) {
	lastRequest := []byte(nil)
	lastResponseOutput := []byte("[]")
	lastResponseID := ""
	if snapshot != nil {
		lastRequest = snapshot.RequestSnapshot
		if len(snapshot.ResponseOutput) > 0 {
			lastResponseOutput = snapshot.ResponseOutput
		}
		lastResponseID = strings.TrimSpace(snapshot.ResponseID)
	}

	requestJSON := ensureResponsesRequestDefaults(rawJSON, lastRequest)
	requestSnapshot := canonicalizeResponsesSnapshotRequest(rawJSON, lastRequest)
	if len(lastRequest) == 0 {
		return requestJSON, requestSnapshot, nil
	}

	nextInput := gjson.GetBytes(rawJSON, "input")
	if !nextInput.Exists() || !nextInput.IsArray() {
		return requestJSON, requestSnapshot, nil
	}

	if prev := strings.TrimSpace(gjson.GetBytes(rawJSON, "previous_response_id").String()); prev != "" {
		if allowIncrementalInputWithPreviousResponseID && lastResponseID != "" {
			nextSnapshot, errSnapshot := buildResponsesCanonicalSnapshot(rawJSON, lastRequest, lastResponseOutput, nextInput.Raw)
			if errSnapshot != nil {
				return nil, nil, badResponsesRequestError("failed to rebuild request snapshot", errSnapshot)
			}
			return requestJSON, nextSnapshot, nil
		}
		return normalizeResponsesHTTPMergedRequest(rawJSON, lastRequest, lastResponseOutput, nextInput.Raw, false)
	}

	if allowIncrementalInputWithPreviousResponseID && lastResponseID != "" {
		historyRaw, errHistory := buildResponsesHistoryInput(lastRequest, lastResponseOutput)
		if errHistory != nil {
			return nil, nil, badResponsesRequestError("failed to rebuild request history", errHistory)
		}

		if tailRaw, ok, errTail := extractResponsesIncrementalTail(nextInput.Raw, historyRaw); errTail != nil {
			return nil, nil, badResponsesRequestError("failed to compare request transcript", errTail)
		} else if ok && isLikelyIncrementalResponsesInput(gjson.Parse(tailRaw)) {
			incremental := ensureResponsesRequestDefaults(rawJSON, lastRequest)
			incremental, _ = sjson.SetBytes(incremental, "previous_response_id", lastResponseID)
			incremental, _ = sjson.SetRawBytes(incremental, "input", []byte(tailRaw))
			return incremental, requestSnapshot, nil
		}

		if isLikelyIncrementalResponsesInput(nextInput) {
			incremental := ensureResponsesRequestDefaults(rawJSON, lastRequest)
			incremental, _ = sjson.SetBytes(incremental, "previous_response_id", lastResponseID)
			nextSnapshot, errSnapshot := buildResponsesCanonicalSnapshot(rawJSON, lastRequest, lastResponseOutput, nextInput.Raw)
			if errSnapshot != nil {
				return nil, nil, badResponsesRequestError("failed to rebuild request snapshot", errSnapshot)
			}
			return incremental, nextSnapshot, nil
		}
	}

	if inputContainsFullTranscript(nextInput) {
		if allowCompactionReplay {
			return requestJSON, requestSnapshot, nil
		}
		return normalizeResponsesHTTPMergedRequest(rawJSON, lastRequest, lastResponseOutput, nextInput.Raw, true)
	}

	if shouldReplaceResponsesTranscript(rawJSON, nextInput) {
		return requestJSON, requestSnapshot, nil
	}

	return normalizeResponsesHTTPMergedRequest(rawJSON, lastRequest, lastResponseOutput, nextInput.Raw, false)
}

func normalizeResponsesHTTPMergedRequest(
	rawJSON []byte,
	lastRequest []byte,
	lastResponseOutput []byte,
	nextInputRaw string,
	stripCompaction bool,
) ([]byte, []byte, *interfaces.ErrorMessage) {
	mergedInput, errMerge := mergeResponsesInputWithHistory(lastRequest, lastResponseOutput, nextInputRaw, stripCompaction)
	if errMerge != nil {
		return nil, nil, badResponsesRequestError("failed to merge responses input", errMerge)
	}

	normalized := canonicalizeResponsesSnapshotRequest(rawJSON, lastRequest)
	normalized, errSet := sjson.SetRawBytes(normalized, "input", []byte(mergedInput))
	if errSet != nil {
		return nil, nil, badResponsesRequestError("failed to update responses input", errSet)
	}
	return normalized, bytes.Clone(normalized), nil
}

func buildResponsesCanonicalSnapshot(rawJSON []byte, lastRequest []byte, lastResponseOutput []byte, nextInputRaw string) ([]byte, error) {
	mergedInput, errMerge := mergeResponsesInputWithHistory(lastRequest, lastResponseOutput, nextInputRaw, false)
	if errMerge != nil {
		return nil, errMerge
	}
	snapshot := canonicalizeResponsesSnapshotRequest(rawJSON, lastRequest)
	return sjson.SetRawBytes(snapshot, "input", []byte(mergedInput))
}

func mergeResponsesInputWithHistory(lastRequest []byte, lastResponseOutput []byte, nextInputRaw string, stripCompaction bool) (string, error) {
	historyRaw, errHistory := buildResponsesHistoryInput(lastRequest, lastResponseOutput)
	if errHistory != nil {
		return "", errHistory
	}

	appendInput := strings.TrimSpace(nextInputRaw)
	if stripCompaction {
		appendInput, errHistory = stripCompactionItemsFromJSONArrayRaw(appendInput)
		if errHistory != nil {
			return "", errHistory
		}
	}

	mergedInput, errMerge := mergeJSONArrayRaw(historyRaw, appendInput)
	if errMerge != nil {
		return "", errMerge
	}
	if deduped, errDedupe := dedupeFunctionCallsByCallID(mergedInput); errDedupe == nil {
		mergedInput = deduped
	}
	return mergedInput, nil
}

func buildResponsesHistoryInput(lastRequest []byte, lastResponseOutput []byte) (string, error) {
	historyRaw, errMerge := mergeJSONArrayRaw(gjson.GetBytes(lastRequest, "input").Raw, normalizeJSONArrayRaw(lastResponseOutput))
	if errMerge != nil {
		return "", errMerge
	}
	if deduped, errDedupe := dedupeFunctionCallsByCallID(historyRaw); errDedupe == nil {
		historyRaw = deduped
	}
	return historyRaw, nil
}

func ensureResponsesRequestDefaults(rawJSON []byte, lastRequest []byte) []byte {
	normalized := bytes.Clone(rawJSON)
	if len(lastRequest) == 0 {
		return normalized
	}
	if !gjson.GetBytes(normalized, "model").Exists() {
		modelName := strings.TrimSpace(gjson.GetBytes(lastRequest, "model").String())
		if modelName != "" {
			normalized, _ = sjson.SetBytes(normalized, "model", modelName)
		}
	}
	if !gjson.GetBytes(normalized, "instructions").Exists() {
		instructions := gjson.GetBytes(lastRequest, "instructions")
		if instructions.Exists() {
			normalized, _ = sjson.SetRawBytes(normalized, "instructions", []byte(instructions.Raw))
		}
	}
	return normalized
}

func canonicalizeResponsesSnapshotRequest(rawJSON []byte, lastRequest []byte) []byte {
	normalized := ensureResponsesRequestDefaults(rawJSON, lastRequest)
	normalized, _ = sjson.DeleteBytes(normalized, "previous_response_id")
	return normalized
}

func inputContainsFullTranscript(input gjson.Result) bool {
	if !input.Exists() || !input.IsArray() {
		return false
	}
	for _, item := range input.Array() {
		switch strings.TrimSpace(item.Get("type").String()) {
		case "compaction", "compaction_summary":
			return true
		}
	}
	return false
}

func shouldReplaceResponsesTranscript(rawJSON []byte, nextInput gjson.Result) bool {
	if strings.TrimSpace(gjson.GetBytes(rawJSON, "previous_response_id").String()) != "" {
		return false
	}
	if !nextInput.Exists() || !nextInput.IsArray() {
		return false
	}
	for _, item := range nextInput.Array() {
		switch strings.TrimSpace(item.Get("type").String()) {
		case "function_call", "custom_tool_call":
			return true
		case "message":
			if strings.TrimSpace(item.Get("role").String()) == "assistant" {
				return true
			}
		}
	}
	return false
}

func stripCompactionItemsFromJSONArrayRaw(rawArray string) (string, error) {
	rawArray = strings.TrimSpace(rawArray)
	if rawArray == "" {
		return "[]", nil
	}

	var items []json.RawMessage
	if errUnmarshal := json.Unmarshal([]byte(rawArray), &items); errUnmarshal != nil {
		return "", errUnmarshal
	}

	filtered := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		itemType := strings.TrimSpace(gjson.GetBytes(item, "type").String())
		if itemType == "compaction" || itemType == "compaction_summary" {
			continue
		}
		filtered = append(filtered, item)
	}

	out, errMarshal := json.Marshal(filtered)
	if errMarshal != nil {
		return "", errMarshal
	}
	return string(out), nil
}

func extractResponsesIncrementalTail(fullInputRaw string, historyRaw string) (string, bool, error) {
	var fullItems []json.RawMessage
	if errUnmarshal := json.Unmarshal([]byte(strings.TrimSpace(fullInputRaw)), &fullItems); errUnmarshal != nil {
		return "", false, errUnmarshal
	}

	var historyItems []json.RawMessage
	if errUnmarshal := json.Unmarshal([]byte(strings.TrimSpace(historyRaw)), &historyItems); errUnmarshal != nil {
		return "", false, errUnmarshal
	}

	if len(fullItems) <= len(historyItems) {
		return "", false, nil
	}
	for i := range historyItems {
		same, errEqual := equalResponsesJSONItem(fullItems[i], historyItems[i])
		if errEqual != nil {
			return "", false, errEqual
		}
		if !same {
			return "", false, nil
		}
	}

	out, errMarshal := json.Marshal(fullItems[len(historyItems):])
	if errMarshal != nil {
		return "", false, errMarshal
	}
	return string(out), true, nil
}

func equalResponsesJSONItem(left []byte, right []byte) (bool, error) {
	leftCompact, errCompact := compactResponsesJSON(left)
	if errCompact != nil {
		return false, errCompact
	}
	rightCompact, errCompact := compactResponsesJSON(right)
	if errCompact != nil {
		return false, errCompact
	}
	return bytes.Equal(leftCompact, rightCompact), nil
}

func compactResponsesJSON(raw []byte) ([]byte, error) {
	var value any
	if errUnmarshal := json.Unmarshal(raw, &value); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	return json.Marshal(value)
}

func isLikelyIncrementalResponsesInput(input gjson.Result) bool {
	if !input.Exists() || !input.IsArray() {
		return false
	}
	for _, item := range input.Array() {
		switch strings.TrimSpace(item.Get("type").String()) {
		case "function_call_output", "custom_tool_call_output":
			continue
		case "message":
			role := strings.TrimSpace(item.Get("role").String())
			if role == "" || role == "user" || role == "developer" || role == "system" {
				continue
			}
			return false
		default:
			return false
		}
	}
	return true
}

func responsesResponseOutputFromPayload(payload []byte) []byte {
	if output := gjson.GetBytes(payload, "response.output"); output.Exists() && output.IsArray() {
		return cloneResponsesBytes([]byte(output.Raw))
	}
	if output := gjson.GetBytes(payload, "output"); output.Exists() && output.IsArray() {
		return cloneResponsesBytes([]byte(output.Raw))
	}
	return []byte("[]")
}

func responsesResponseIDFromPayload(payload []byte) string {
	if responseID := strings.TrimSpace(gjson.GetBytes(payload, "response.id").String()); responseID != "" {
		return responseID
	}
	return strings.TrimSpace(gjson.GetBytes(payload, "id").String())
}

func badResponsesRequestError(message string, err error) *interfaces.ErrorMessage {
	return &interfaces.ErrorMessage{
		StatusCode: http.StatusBadRequest,
		Error:      fmt.Errorf("%s: %w", message, err),
	}
}
