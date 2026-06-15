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

func applyResponsesMetadataHeaders(h http.Header, metadata map[string]string) {
	if len(metadata) == 0 {
		return
	}
	if v := strings.TrimSpace(metadata[responsesClientMetadataInstallationID]); v != "" {
		h.Set(responsesClientMetadataInstallationID, v)
	}
	if v := strings.TrimSpace(metadata[responsesClientMetadataSessionID]); v != "" {
		h.Set("session-id", v)
	}
	if v := strings.TrimSpace(metadata[responsesClientMetadataThreadID]); v != "" {
		h.Set("thread-id", v)
		h.Set("x-client-request-id", v)
	}
	if v := strings.TrimSpace(metadata[responsesClientMetadataWindowID]); v != "" {
		h.Set(responsesClientMetadataWindowID, v)
	}
	if v := strings.TrimSpace(metadata[responsesClientMetadataTurnMetadata]); v != "" {
		h.Set(responsesClientMetadataTurnMetadata, v)
	}
}
