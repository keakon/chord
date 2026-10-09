package agent

import (
	"path/filepath"
	"testing"
)

// The Responses prompt_cache_key / client_metadata pair is part of the Codex
// client contract for several relays, and the identity behind it lives on the
// llm.Client. Every path that installs a client must therefore re-apply it:
// a freshly built client would otherwise send the first request of a session
// without the pair and be rejected with a 400 no key rotation can fix.
func TestEnsureLLMSessionIDPinsIdentityOnFreshClient(t *testing.T) {
	t.Parallel()
	a := &MainAgent{sessionDir: filepath.Join("/sessions", "proj", "session-a")}
	client := newTestLLMClient()
	a.ensureLLMSessionID(client)
	if got := client.SessionKey(); got != "session-a" {
		t.Fatalf("SessionKey = %q, want the session directory base", got)
	}
}

// A session directory that is not usable as an identity must leave the client
// untouched rather than pinning a placeholder like "." on it.
func TestEnsureLLMSessionIDIgnoresUnusableSessionDir(t *testing.T) {
	t.Parallel()
	a := &MainAgent{}
	client := newTestLLMClient()
	a.ensureLLMSessionID(client)
	if got := client.SessionKey(); got != "" {
		t.Fatalf("SessionKey = %q, want empty without a session directory", got)
	}
	a.ensureLLMSessionID(nil)
}
