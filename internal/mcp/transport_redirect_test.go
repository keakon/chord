package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// redirectRequests builds the arguments the http.Client hands CheckRedirect:
// via[0] is the original request and req is the next hop, already carrying the
// copied headers.
func redirectRequests(t *testing.T, from, to string, headers map[string]string) (*http.Request, []*http.Request) {
	t.Helper()
	orig, err := http.NewRequest(http.MethodPost, from, nil)
	if err != nil {
		t.Fatalf("build original request: %v", err)
	}
	next, err := http.NewRequest(http.MethodPost, to, nil)
	if err != nil {
		t.Fatalf("build redirect request: %v", err)
	}
	for k, v := range headers {
		orig.Header.Set(k, v)
		next.Header.Set(k, v)
	}
	return next, []*http.Request{orig}
}

func TestHTTPTransportStripsConfiguredHeadersOffOrigin(t *testing.T) {
	headers := map[string]string{"x-api-key": "secret", "X-Tenant": "acme"}
	transport := NewHTTPTransport("https://mcp.example.com/rpc", headers)

	for _, target := range []string{
		"https://evil.example.net/rpc", // unrelated host
		"https://example.com.evil.net/rpc",
		"http://mcp.example.com/rpc", // same host, transport downgrade
	} {
		req, via := redirectRequests(t, "https://mcp.example.com/rpc", target, headers)
		if err := transport.checkRedirect(req, via); err != nil {
			t.Fatalf("checkRedirect(%s) = %v, want nil", target, err)
		}
		for k := range headers {
			if got := req.Header.Get(k); got != "" {
				t.Errorf("redirect to %s kept %s = %q, want it stripped", target, k, got)
			}
		}
	}
}

func TestHTTPTransportKeepsConfiguredHeadersOnSameOrigin(t *testing.T) {
	headers := map[string]string{"x-api-key": "secret"}
	transport := NewHTTPTransport("https://mcp.example.com/rpc", headers)

	for _, target := range []string{
		"https://mcp.example.com/rpc/v2", // same host, different path
		"https://eu.mcp.example.com/rpc", // subdomain
	} {
		req, via := redirectRequests(t, "https://mcp.example.com/rpc", target, headers)
		if err := transport.checkRedirect(req, via); err != nil {
			t.Fatalf("checkRedirect(%s) = %v, want nil", target, err)
		}
		if got := req.Header.Get("x-api-key"); got != "secret" {
			t.Errorf("redirect to %s dropped x-api-key = %q, want secret", target, got)
		}
	}
}

func TestHTTPTransportRejectsRedirectLoopAndScheme(t *testing.T) {
	transport := NewHTTPTransport("https://mcp.example.com/rpc", nil)

	req, via := redirectRequests(t, "https://mcp.example.com/rpc", "https://mcp.example.com/rpc", nil)
	for len(via) < mcpMaxRedirects {
		via = append(via, via[0])
	}
	if err := transport.checkRedirect(req, via); err == nil || !strings.Contains(err.Error(), "stopped after") {
		t.Fatalf("checkRedirect over the hop limit = %v, want a stop error", err)
	}

	bad, err := http.NewRequest(http.MethodPost, "https://mcp.example.com/rpc", nil)
	if err != nil {
		t.Fatal(err)
	}
	bad.URL = &url.URL{Scheme: "file", Path: "/etc/passwd"}
	_, via = redirectRequests(t, "https://mcp.example.com/rpc", "https://mcp.example.com/rpc", nil)
	if err := transport.checkRedirect(bad, via); err == nil || !strings.Contains(err.Error(), "non-http scheme") {
		t.Fatalf("checkRedirect to a file:// target = %v, want a scheme error", err)
	}
}

// End-to-end cover for the kept case: both httptest servers listen on
// 127.0.0.1, so a hop between them is same-origin and must carry the header.
func TestHTTPTransportFollowsSameOriginRedirectWithHeaders(t *testing.T) {
	var gotKey string
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		var req JSONRPCRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(JSONRPCResponse{JSONRPC: "2.0", ID: req.ID})
	}))
	defer final.Close()

	entry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL, http.StatusTemporaryRedirect)
	}))
	defer entry.Close()

	transport := NewHTTPTransport(entry.URL, map[string]string{"x-api-key": "secret"})
	if _, err := transport.Send(context.Background(), JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "test"}); err != nil {
		t.Fatalf("Send across a same-origin redirect: %v", err)
	}
	if gotKey != "secret" {
		t.Fatalf("redirect target saw x-api-key = %q, want secret", gotKey)
	}
}
