package llm

import "testing"

func TestDefaultLLMUserAgentUsesChordVersion(t *testing.T) {
	got := defaultLLMUserAgent()
	if got == "" || got == "Go-http-client/1.1" {
		t.Fatalf("defaultLLMUserAgent = %q, want Chord product token", got)
	}
	if wantPrefix := "chord/"; len(got) <= len(wantPrefix) || got[:len(wantPrefix)] != wantPrefix {
		t.Fatalf("defaultLLMUserAgent = %q, want prefix %q", got, wantPrefix)
	}
}

func TestSanitizeUserAgentProductVersion(t *testing.T) {
	if got := sanitizeUserAgentProductVersion(" v1.2.3 dirty/build "); got != "v1.2.3-dirty-build" {
		t.Fatalf("sanitizeUserAgentProductVersion = %q", got)
	}
}
