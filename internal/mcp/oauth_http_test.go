package mcp

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOAuthHTTPNetworkPolicy(t *testing.T) {
	c := NewOAuthHTTPClient(OAuthHTTPOptions{LookupIP: func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP("127.0.0.1")}}, nil
	}})
	for _, endpoint := range []string{"http://example.invalid/meta", "https://127.0.0.1/meta", "https://10.0.0.1/meta", "https://169.254.169.254/meta", "https://[::1]/meta", "https://[::ffff:127.0.0.1]/meta", "https://[fe80::1%25en0]/meta", "https://user:password@example.invalid/meta", "https://example.invalid/meta#fragment", "https://example.invalid/meta", "https://0.0.0.0/meta", "https://224.0.0.1/meta"} {
		err := c.ValidateEndpoint(context.Background(), endpoint)
		e, ok := errors.AsType[*OAuthHTTPError](err)
		if !ok || e.ErrorClass != ErrorNetworkPolicyDenied {
			t.Errorf("%s: error=%v", endpoint, err)
		}
	}
}

func TestOAuthHTTPResourceLocalException(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{"ok":true}`) }))
	defer s.Close()
	c := NewOAuthHTTPClient(OAuthHTTPOptions{ResourceEndpoint: s.URL + "/mcp"})
	var result map[string]bool
	if err := c.GetJSON(context.Background(), s.URL+"/.well-known/oauth-protected-resource", &result); err != nil || !result["ok"] {
		t.Fatalf("result=%v err=%v", result, err)
	}
	if err := c.ValidateEndpoint(context.Background(), s.URL); err == nil {
		t.Fatal("authorization endpoint borrowed local exception")
	}
	if err := c.PostForm(context.Background(), s.URL, url.Values{}, &result); err == nil {
		t.Fatal("POST borrowed local exception")
	}
}

func publicOAuthTestClient(t *testing.T, handler http.Handler) (*OAuthHTTPClient, string, *atomic.Int32) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	calls := new(atomic.Int32)
	c := NewOAuthHTTPClient(OAuthHTTPOptions{
		RootCAs: roots,
		LookupIP: func(ctx context.Context, host string) ([]net.IPAddr, error) {
			calls.Add(1)
			if host != "example.com" {
				return nil, errors.New("unexpected host")
			}
			return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
		},
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if address != "8.8.8.8:443" {
				t.Errorf("dial did not use pinned address: %s", address)
			}
			return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
		},
	})
	return c, "https://example.com", calls
}

func TestOAuthHTTPPinnedDNSAndNoProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	c, base, lookups := publicOAuthTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{"ok":true}`) }))
	var result map[string]bool
	if err := c.GetJSON(context.Background(), base+"/metadata", &result); err != nil || !result["ok"] {
		t.Fatalf("result=%v err=%v", result, err)
	}
	if lookups.Load() != 1 {
		t.Fatalf("DNS lookups=%d, want one pinned lookup", lookups.Load())
	}
}

func TestOAuthHTTPRedirectPolicy(t *testing.T) {
	var requests atomic.Int32
	c, base, _ := publicOAuthTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Location", "/next")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	var out json.RawMessage
	err := c.GetJSON(context.Background(), base+"/metadata", &out)
	e, ok := errors.AsType[*OAuthHTTPError](err)
	if !ok || e.ErrorClass != ErrorEndpointRedirect || requests.Load() != 4 {
		t.Fatalf("GET requests=%d err=%v", requests.Load(), err)
	}
	requests.Store(0)
	err = c.PostForm(context.Background(), base+"/token", url.Values{"code": {"private-code"}}, &out)
	e, ok = errors.AsType[*OAuthHTTPError](err)
	if !ok || e.SendEvidence != SendResponseReceived || requests.Load() != 1 {
		t.Fatalf("POST requests=%d err=%v", requests.Load(), err)
	}
}

func TestOAuthHTTPRejectsPrivateRedirect(t *testing.T) {
	c, base, _ := publicOAuthTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://127.0.0.1/private")
		w.WriteHeader(http.StatusFound)
	}))
	var out json.RawMessage
	err := c.GetJSON(context.Background(), base+"/metadata", &out)
	e, ok := errors.AsType[*OAuthHTTPError](err)
	if !ok || e.ErrorClass != ErrorNetworkPolicyDenied {
		t.Fatalf("error=%v", err)
	}
}

func TestOAuthHTTPSecretSafeErrorsAndLimits(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		wantCode   string
	}{
		{name: "known error", body: `{"error":"invalid_grant","error_description":"private-secret"}`, status: 400, wantCode: "invalid_grant"},
		{name: "unknown error", body: `{"error":"private-secret"}`, status: 400},
		{name: "error overflow", body: strings.Repeat("s", 4097), status: 400},
		{name: "success overflow", body: strings.Repeat("s", oauthMaxBodyBytes+1), status: 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, base, _ := publicOAuthTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			var out json.RawMessage
			err := c.GetJSON(context.Background(), base+"/metadata?secret=private-secret", &out)
			e, ok := errors.AsType[*OAuthHTTPError](err)
			if !ok || e.OAuthError != tc.wantCode || strings.Contains(err.Error(), "private-secret") {
				t.Fatalf("error=%v", err)
			}
			if tc.wantCode != "" && e.RetryAfter != 2*time.Second {
				t.Fatalf("retry-after=%v", e.RetryAfter)
			}
		})
	}
}

func TestOAuthHTTPBasicAndSingleAttempt(t *testing.T) {
	var requests atomic.Int32
	c, base, _ := publicOAuthTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		user, password, ok := r.BasicAuth()
		if !ok || user != "client%3Aid" || password != "secret+value" {
			t.Error("incorrect OAuth Basic encoding")
		}
		if r.Header.Get("Idempotency-Key") != "" {
			t.Error("control POST must not be retryable")
		}
		h := w.(http.Hijacker)
		conn, _, err := h.Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	}))
	var out json.RawMessage
	err := c.PostFormWithBasic(context.Background(), base+"/token", url.Values{"grant_type": {"refresh_token"}}, "client:id", "secret value", &out)
	e, ok := errors.AsType[*OAuthHTTPError](err)
	if !ok || e.SendEvidence != SendOutcomeUnknown || requests.Load() != 1 {
		t.Fatalf("requests=%d err=%v", requests.Load(), err)
	}
}
