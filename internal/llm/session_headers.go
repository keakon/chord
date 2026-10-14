package llm

import (
	"net/http"
	"strings"
)

// applySessionIDHeaders attaches the stable client identity used by Responses
// and compatible gateways for session routing and cache affinity.
func applySessionIDHeaders(h http.Header, sid string) {
	sid = strings.TrimSpace(sid)
	if sid == "" {
		return
	}
	h.Set(headerXSessionID, sid)
	h.Set(headerSessionID, sid)
}

// applyCodexSessionIdentity uses the upstream Codex header names. Chord uses
// one identity for its session and thread; the same value is used for the
// prompt-cache key when enabled. Keep it stable across requests because the
// ChatGPT backend derives cache affinity from session-id.
// Without a caller-provided identity, leave these headers absent.
func applyCodexSessionIdentity(h http.Header, sid string) {
	sid = strings.TrimSpace(sid)
	if sid == "" {
		return
	}
	h.Set(headerSessionID, sid)
	h.Set(headerThreadID, sid)
	h.Set(headerClientRequestID, sid)
}

func applyResponsesMetadataHeaders(h http.Header, metadata map[string]string) {
	if len(metadata) == 0 {
		return
	}
	if v := strings.TrimSpace(metadata[responsesClientMetadataInstallationID]); v != "" {
		h.Set(responsesClientMetadataInstallationID, v)
	}
	if v := strings.TrimSpace(metadata[responsesClientMetadataSessionID]); v != "" {
		h.Set(headerSessionID, v)
	}
	if v := strings.TrimSpace(metadata[responsesClientMetadataThreadID]); v != "" {
		h.Set(headerThreadID, v)
		h.Set(headerClientRequestID, v)
	}
	if v := strings.TrimSpace(metadata[responsesClientMetadataWindowID]); v != "" {
		h.Set(responsesClientMetadataWindowID, v)
	}
	if v := strings.TrimSpace(metadata[responsesClientMetadataTurnMetadata]); v != "" {
		h.Set(responsesClientMetadataTurnMetadata, v)
	}
}
