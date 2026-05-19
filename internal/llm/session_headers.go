package llm

import (
	"net/http"
	"strings"
)

// applySessionIDHeaders attaches the stable Chord session identifier using the
// header names used by Responses/Codex-compatible transports. It is best-effort
// metadata for provider-side routing/cache affinity; request bodies remain the
// authoritative prompt-cache signal where the API supports prompt_cache_key.
func applySessionIDHeaders(h http.Header, sid string) {
	sid = strings.TrimSpace(sid)
	if sid == "" {
		return
	}
	h.Set("X-Session-Id", sid)
	h.Set("session-id", sid)
}
