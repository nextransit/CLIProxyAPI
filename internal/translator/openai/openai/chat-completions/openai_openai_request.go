// Package chat_completions provides request translation for OpenAI-to-OpenAI passthrough.
package chat_completions

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertOpenAIRequestToOpenAI passes through an OpenAI Chat Completions request,
// replacing the model name and sanitising incomplete tool_calls entries.
//
// Some clients (e.g. Crush) emit assistant messages where tool_calls entries lack
// the required "function" sub-object.  Strict upstream providers (e.g. GLM) reject
// such requests.  This function strips any tool_calls entry that is missing the
// "function" field and removes the entire tool_calls array when all entries are
// invalid.
func ConvertOpenAIRequestToOpenAI(modelName string, inputRawJSON []byte, _ bool) []byte {
	out, err := sjson.SetBytes(inputRawJSON, "model", modelName)
	if err != nil {
		return inputRawJSON
	}
	out = sanitizeToolCalls(out)
	out = sanitizeToolMessages(out)
	return out
}

// sanitizeToolCalls removes incomplete tool_calls entries from assistant messages.
// An entry is considered incomplete when its "function" field is missing or not an object.
func sanitizeToolCalls(raw []byte) []byte {
	messages := gjson.GetBytes(raw, "messages")
	if !messages.IsArray() {
		return raw
	}

	out := raw
	dirty := false

	for i, msg := range messages.Array() {
		if msg.Get("role").String() != "assistant" {
			continue
		}
		tcs := msg.Get("tool_calls")
		if !tcs.IsArray() || len(tcs.Array()) == 0 {
			continue
		}

		var valid []gjson.Result
		for _, tc := range tcs.Array() {
			fn := tc.Get("function")
			if fn.Exists() && fn.IsObject() {
				valid = append(valid, tc)
			}
		}

		if len(valid) == len(tcs.Array()) {
			continue
		}
		dirty = true
		path := fmt.Sprintf("messages.%d.tool_calls", i)
		if len(valid) == 0 {
			out, _ = sjson.DeleteBytes(out, path)
		} else {
			arr := make([]byte, 0, len(valid)*128)
			arr = append(arr, '[')
			for j, v := range valid {
				if j > 0 {
					arr = append(arr, ',')
				}
				arr = append(arr, v.Raw...)
			}
			arr = append(arr, ']')
			out, _ = sjson.SetRawBytes(out, path, arr)
		}
	}

	if dirty {
		return out
	}
	return raw
}

// sanitizeToolMessages normalizes tool messages by ensuring they have tool_call_id.
// Some clients emit tool messages with call_id instead of tool_call_id.
// Strict upstream providers (e.g. MiniMax) require tool_call_id.
// This function copies call_id to tool_call_id when tool_call_id is missing.
func sanitizeToolMessages(raw []byte) []byte {
	messages := gjson.GetBytes(raw, "messages")
	if !messages.IsArray() {
		return raw
	}

	out := raw
	dirty := false

	for i, msg := range messages.Array() {
		if msg.Get("role").String() != "tool" {
			continue
		}
		toolCallID := strings.TrimSpace(msg.Get("tool_call_id").String())
		if toolCallID != "" {
			continue
		}
		callID := strings.TrimSpace(msg.Get("call_id").String())
		if callID == "" {
			continue
		}
		path := fmt.Sprintf("messages.%d.tool_call_id", i)
		next, err := sjson.SetBytes(out, path, callID)
		if err != nil {
			continue
		}
		out = next
		dirty = true
	}

	if dirty {
		return out
	}
	return raw
}