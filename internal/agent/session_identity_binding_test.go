package agent

import (
	"testing"

	"github.com/keakon/chord/internal/llm"
)

// The Claude emulation's session UUID is derived from the client's session
// key, so the identity binding must cover both agent kinds: a missing key on
// either side would silently drop the session identity from the wire instead
// of a wrong shape.

func TestMainAgentBindsSessionKeyForDerivedIdentity(t *testing.T) {
	a := &MainAgent{sessionDir: "/sessions/20260102150405123"}
	c := &llm.Client{}
	a.ensureLLMSessionIdentity(c)
	if got := c.SessionKey(); got != "20260102150405123" {
		t.Fatalf("MainAgent session key = %q, want the session directory base", got)
	}
}

func TestSubAgentBindsSubSessionKeyForDerivedIdentity(t *testing.T) {
	s := &SubAgent{sessionDir: "/sessions/20260102150405123", instanceID: "coder-1"}
	c := &llm.Client{}
	s.setSessionIdentity(c)
	if got := c.SessionKey(); got != "20260102150405123:sub:coder-1" {
		t.Fatalf("SubAgent session key = %q, want the base with the instance suffix", got)
	}
}
