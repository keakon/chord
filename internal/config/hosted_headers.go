package config

import (
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"golang.org/x/net/http/httpguts"
)

// ValidateHostedToolHeaders permits declaration-specific headers without
// replacing credentials, transport framing or runtime session identity.
func ValidateHostedToolHeaders(headers map[string]string) error {
	seen := make(map[string]bool, len(headers))
	for _, name := range slices.Sorted(maps.Keys(headers)) {
		if !httpguts.ValidHeaderFieldName(name) || !httpguts.ValidHeaderFieldValue(headers[name]) {
			return fmt.Errorf("invalid hosted tool header %q", name)
		}
		canonical := http.CanonicalHeaderKey(name)
		if seen[canonical] {
			return fmt.Errorf("duplicate hosted tool header %q", name)
		}
		seen[canonical] = true
		switch strings.ToLower(name) {
		case "authorization", "proxy-authorization", "x-api-key", "api-key",
			"host", "content-type", "accept", "content-encoding", "accept-encoding",
			"content-length", "transfer-encoding", "connection", "trailer", "te", "upgrade",
			"anthropic-version", "chatgpt-account-id", "session_id", "session-id",
			"x-session-id", "x-codex-turn-state", "thread-id", "x-client-request-id",
			"x-codex-installation-id", "x-codex-window-id", "x-codex-turn-metadata",
			"x-claude-code-session-id",
			// The Claude Code client identity headers describe the machine and
			// client build; a per-request retry counter and timeout budget are
			// transport facts a declaration may legitimately tune.
			"x-stainless-lang", "x-stainless-runtime", "x-stainless-os", "x-stainless-arch",
			"x-stainless-package-version", "x-stainless-runtime-version",
			// The direct-browser-access opt-in is part of the same client
			// identity: the emulation always sends it, so a declaration may not
			// drop or rewrite it.
			"anthropic-dangerous-direct-browser-access":
			return fmt.Errorf("hosted tool header %q is managed by the provider transport", name)
		}
	}
	return nil
}
