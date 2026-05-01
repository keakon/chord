package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/keakon/chord/internal/message"
)

// SetSessionID sets the persistent session identifier used as prompt_cache_key
// for OpenAI's prompt caching. When the session changes, any existing Codex
// WebSocket chain is dropped so incremental reuse cannot cross session bounds.
func (r *ResponsesProvider) SetSessionID(sid string) {
	if r == nil {
		return
	}
	sid = strings.TrimSpace(sid)
	if r.sessionID == sid {
		return
	}
	if r.sessionID != "" || sid != "" {
		r.resetCodexWebSocketChain("session_id_changed")
	}
	r.sessionID = sid
}

// responsesInputSignature returns a SHA-256 hash of the serialized input items.
func responsesInputSignature(items []responsesInputItem) string {
	b, err := json.Marshal(items)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// responsesInputPrefixSignature returns the signature of the first n items.
func responsesInputPrefixSignature(items []responsesInputItem, n int) string {
	if n <= 0 || n > len(items) {
		return ""
	}
	return responsesInputSignature(items[:n])
}

// responsesRequestSignature returns a stable signature for incremental
// compatibility checks. It intentionally excludes input and previous_response_id
// so callers can compare whether non-input request properties stayed the same.
func responsesRequestSignature(req *responsesRequest) string {
	if req == nil {
		return ""
	}
	cpy := *req
	cpy.Input = nil
	cpy.PreviousResponseID = ""
	b, err := json.Marshal(cpy)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func responsesItemKey(item responsesInputItem) string {
	switch item.Type {
	case "function_call":
		return "function_call|" + item.CallID + "|" + item.Name + "|" + item.Arguments
	case "message":
		var b strings.Builder
		b.WriteString("message|")
		b.WriteString(item.Role)
		b.WriteString("|")
		content, _ := item.Content.([]responsesContentBlock)
		for _, c := range content {
			b.WriteString(c.Type)
			b.WriteString(":")
			b.WriteString(c.Text)
			b.WriteString(";")
		}
		return b.String()
	default:
		raw, _ := json.Marshal(item)
		return item.Type + "|" + string(raw)
	}
}

func responsesMergeOutputItems(primary, fallback []responsesInputItem) []responsesInputItem {
	if len(primary) == 0 {
		return append([]responsesInputItem(nil), fallback...)
	}
	out := append([]responsesInputItem(nil), primary...)
	seen := make(map[string]struct{}, len(out))
	for _, item := range out {
		seen[responsesItemKey(item)] = struct{}{}
	}
	for _, item := range fallback {
		key := responsesItemKey(item)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	return out
}

func responsesFinalizeIncrementalOutputItems(payloadItems []responsesInputItem, resp *message.Response) []responsesInputItem {
	return responsesMergeOutputItems(payloadItems, responsesResponseToInputItems(resp))
}
