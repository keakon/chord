package mcp

import "testing"

func TestOAuthDiagnosticPreservesClassAndEvidence(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://proxy.example.invalid:8080")
	for _, class := range []ErrorClass{ErrorNetworkPolicyDenied, ErrorTLS, ErrorProtocol, ErrorEndpointRedirect} {
		got := diagnoseAuth(&OAuthHTTPError{ErrorClass: class, SendEvidence: SendNotSent})
		if got.ErrorClass != class || got.SendEvidence != SendNotSent || got.SuggestedAction == "" || got.ControlNetworkMode != ControlNetworkDirect || !got.ProxyEnvironmentDetected {
			t.Errorf("class=%s diagnostic=%+v", class, got)
		}
	}
	wrapped := newTransportFailure("credential preparation failed", SendNotSent, &OAuthHTTPError{ErrorClass: ErrorProtocol, SendEvidence: SendResponseReceived})
	if got := diagnoseAuth(wrapped); got.ErrorClass != ErrorProtocol || got.SendEvidence != SendNotSent {
		t.Fatalf("business send evidence lost: %+v", got)
	}
}
