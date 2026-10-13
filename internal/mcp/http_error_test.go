package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseBearerChallenges(t *testing.T) {
	for _, tc := range []struct {
		name            string
		headers         []string
		want            int
		metadata, scope string
		bad             bool
	}{
		{name: "absent"},
		{name: "other scheme", headers: []string{`Basic realm="sample", resource_metadata="https://example.invalid/ignored"`}},
		{name: "multiple schemes", headers: []string{`Basic realm="sample", Bearer resource_metadata="https://example.invalid/meta", scope="read write"`}, want: 1, metadata: "https://example.invalid/meta", scope: "read write"},
		{name: "multiple fields", headers: []string{`Negotiate abc==`, `Bearer error="invalid_token"`}, want: 1},
		{name: "escaped quoted string", headers: []string{`Bearer realm="sample, \"realm\"", scope="read"`}, want: 1, scope: "read"},
		{name: "case insensitive", headers: []string{`bearer SCOPE=read`}, want: 1, scope: "read"},
		{name: "duplicate", headers: []string{`Bearer scope="read", Scope="write"`}, bad: true},
		{name: "conflict", headers: []string{`Bearer resource_metadata="https://example.invalid/a"`, `Bearer resource_metadata="https://example.invalid/b"`}, bad: true},
		{name: "malformed non bearer", headers: []string{`Basic realm="unterminated`}, bad: true},
		{name: "control", headers: []string{"Bearer scope=\"read\x1bwrite\""}, bad: true},
		{name: "missing comma", headers: []string{`Bearer scope="read" realm="sample"`}, bad: true},
		{name: "trailing comma", headers: []string{`Bearer scope="read",`}, bad: true},
		{name: "budget", headers: []string{strings.Repeat("a", maxAuthenticateHeaderBytes+1)}, bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseBearerChallenges(tc.headers)
			if (err != nil) != tc.bad {
				t.Fatalf("error=%v, want bad=%v", err, tc.bad)
			}
			if tc.bad {
				return
			}
			if len(got) != tc.want {
				t.Fatalf("count=%d want %d", len(got), tc.want)
			}
			if tc.want > 0 && (got[0].ResourceMetadata != tc.metadata || got[0].Scope != tc.scope) {
				t.Fatalf("unexpected challenge: %+v", got[0])
			}
		})
	}
}

func TestHTTPErrorProtectsResponseSecrets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", scope="read", error_description="private-description"`)
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("private-response-secret"))
	}))
	defer srv.Close()
	tr := NewHTTPTransport(HTTPTransportOptions{Endpoint: srv.URL, Headers: nil})
	for _, notify := range []bool{false, true} {
		var err error
		if notify {
			err = tr.Notify(context.Background(), JSONRPCNotification{Method: "test"})
		} else {
			_, err = tr.Send(context.Background(), JSONRPCRequest{ID: 1, Method: "test"})
		}
		e, ok := errors.AsType[*HTTPError](err)
		if !ok || e.StatusCode != 403 || e.SendEvidence != SendResponseReceived {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(err.Error(), "private") {
			t.Fatal("secret leaked")
		}
		if got := DiagnoseHTTPError(err); got.ErrorClass != ErrorInsufficientScope {
			t.Fatalf("diagnostic=%+v", got)
		}
	}
}

func TestHTTPTransportSendEvidence(t *testing.T) {
	tr := NewHTTPTransport(HTTPTransportOptions{Endpoint: "://private-url", Headers: nil})
	_, err := tr.Send(context.Background(), JSONRPCRequest{})
	e, ok := errors.AsType[*HTTPError](err)
	if !ok || e.SendEvidence != SendNotSent || strings.Contains(err.Error(), "private") {
		t.Fatalf("error=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tr = NewHTTPTransport(HTTPTransportOptions{Endpoint: "https://example.invalid/private?token=secret", Headers: nil})
	_, err = tr.Send(ctx, JSONRPCRequest{})
	e, ok = errors.AsType[*HTTPError](err)
	if !ok || e.SendEvidence != SendOutcomeUnknown || !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("error=%v", err)
	}
}
