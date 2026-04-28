package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"

	"github.com/keakon/chord/internal/message"
)

// responsesSessionState holds legacy HTTP previous_response_id bookkeeping.
// Current Codex behavior no longer uses HTTP incremental transmission, so only
// reset/log fields remain; connection-scoped incremental state lives in the
// WebSocket fields on ResponsesProvider.
type responsesSessionState struct {
	mu sync.Mutex

	lastResponseID   string
	lastModelID      string
	lastFullInputLen int
	lastFullInputSig string

	// for logging only
	lastKeyHint string
}

func (s *responsesSessionState) reset(reason string, attrs ...any) {
	attrs = append([]any{"reason", reason, "prev_response_id", s.lastResponseID, "prev_model", s.lastModelID}, attrs...)
	slog.Info("responses: session reset", attrs...)
	s.lastResponseID = ""
	s.lastModelID = ""
	s.lastFullInputLen = 0
	s.lastFullInputSig = ""
	s.lastKeyHint = ""
}

// ResetResponsesSession clears cached previous_response_id session state.
func (r *ResponsesProvider) ResetResponsesSession(reason string) {
	r.session.mu.Lock()
	r.session.reset(reason)
	r.session.mu.Unlock()
	r.resetCodexWebSocketChain(reason)
}

// SetSessionID sets the persistent session identifier used as prompt_cache_key
// for OpenAI's prompt caching. Should be called on session creation/switch.
func (r *ResponsesProvider) SetSessionID(sid string) {
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
