package llm

import (
	"net/http"
	"testing"
)

// Session headers must agree with the caller's prompt-cache identity.
func TestApplyCodexSessionIdentityCarriesOneStableValue(t *testing.T) {
	h := http.Header{}
	applyCodexSessionIdentity(h, "session-abc")

	// The exact header set upstream Codex CLI sends for a root agent.
	for _, name := range []string{"session-id", "thread-id", "x-client-request-id"} {
		if got := h.Get(name); got != "session-abc" {
			t.Fatalf("%s = %q, want session-abc", name, got)
		}
	}
	if got := h.Get("session_id"); got != "" {
		t.Fatalf("session_id = %q, want only the canonical session header", got)
	}
}

// An absent caller identity must not produce a random session header.
func TestApplyCodexSessionIdentitySkipsEmptyIdentity(t *testing.T) {
	h := http.Header{}
	applyCodexSessionIdentity(h, "   ")

	if len(h) != 0 {
		t.Fatalf("expected no headers for an empty session identity, got %#v", h)
	}
}
