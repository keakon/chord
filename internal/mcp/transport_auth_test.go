package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func transportAuthFixture(t *testing.T, handler http.Handler) (*HTTPTransport, *AuthService, Target, ExpectedIdentity) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	target := storeTestTarget()
	target.Config.URL = server.URL + "/"
	target.Config.Name = target.Ref.ServerName
	target.ConfigGeneration = 1
	target.TargetID = identityHash(struct{ Workspace, Owner, Endpoint string }{target.Ref.WorkspaceID, target.Ref.Owner, target.Config.URL})
	store := NewCredentialStore(t.TempDir())
	expected := seedRefreshTest(t, store, target, time.Now())
	mutateTransportCredential(t, store, target, func(slot *CredentialSlot) {
		slot.Token.ExpiresAt = time.Now().Add(time.Hour).UTC()
		slot.Token.RefreshToken = ""
	})
	service := NewAuthService(AuthOptions{Store: store, Discoverer: NewOAuthDiscoverer(&fixedOAuthMetadata{resource: target.Config.URL, issuer: "https://auth.example.invalid/tenant"})})
	t.Cleanup(service.Close)
	transport := NewHTTPTransport(HTTPTransportOptions{Endpoint: target.Config.URL, AuthService: service, Target: target, ExpectedIdentity: expected, ConnectionGeneration: 9})
	return transport, service, target, expected
}
func mutateTransportCredential(t *testing.T, store *CredentialStore, target Target, mutate func(*CredentialSlot)) {
	t.Helper()
	if err := store.UpdateLocked(t.Context(), target, func(tx *CredentialTransaction) error {
		mutate(tx.File.Slots[target.ProfileID])
		_, err := tx.Commit()
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
func writeTransportRPC(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	var req JSONRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.Error(err)
		return
	}
	_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{}}`, req.ID)
}

func TestHTTPDynamicRevisionKeepsSessionAndIdentityChangeBlocks(t *testing.T) {
	var requests atomic.Int32
	tr, service, target, _ := transportAuthFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		if n == 1 {
			if r.Header.Get("Authorization") != "Bearer access-before" || r.Header.Get(mcpSessionHeader) != "" {
				t.Error("incorrect first credential")
			}
			w.Header().Set(mcpSessionHeader, "session-one")
		} else if r.Header.Get("Authorization") != "Bearer access-after" || r.Header.Get(mcpSessionHeader) != "session-one" {
			t.Error("revision rotation lost session or token")
		}
		writeTransportRPC(t, w, r)
	}))
	req := JSONRPCRequest{ID: 1, Method: "tools/call", Params: map[string]any{"name": "sample", "arguments": map[string]any{}}}
	if _, err := tr.Send(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	mutateTransportCredential(t, service.store, target, func(slot *CredentialSlot) { slot.TokenRevision++; slot.Token.AccessToken = "access-after" })
	if _, err := tr.Send(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	mutateTransportCredential(t, service.store, target, func(slot *CredentialSlot) { slot.AuthGeneration++; slot.TokenRevision = 1 })
	_, err := tr.Send(t.Context(), req)
	failure, ok := errors.AsType[*HTTPError](err)
	if !ok || failure.SendEvidence != SendNotSent || diagnoseAuth(err).ErrorClass != ErrorIdentityChanged || requests.Load() != 2 {
		t.Fatalf("identity change sent a request: %v count=%d", err, requests.Load())
	}
}

func TestHTTPBusinessUnauthorizedHasNoReplayAndCannotPoisonSession(t *testing.T) {
	var requests atomic.Int32
	tr, service, target, _ := transportAuthFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set(mcpSessionHeader, "rejected-session")
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	tr.sessionID = "original-session"
	_, err := tr.Send(t.Context(), JSONRPCRequest{ID: 1, Method: "tools/call", Params: map[string]any{"name": "sample"}})
	e, ok := errors.AsType[*HTTPError](err)
	if !ok || e.StatusCode != 401 || requests.Load() != 1 || tr.sessionID != "original-session" {
		t.Fatalf("rejection result=%v count=%d session=%s", err, requests.Load(), tr.sessionID)
	}
	file, err := service.store.InspectFile(t.Context(), target)
	if err != nil || file.Slots[target.ProfileID].RejectedRevision != 1 {
		t.Fatalf("rejection not persisted: %v", err)
	}
}

func TestHTTPLateRejectionCannotRejectNewRevision(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	tr, service, target, _ := transportAuthFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(http.StatusUnauthorized)
	}))
	done := make(chan error, 1)
	go func() { _, err := tr.Send(t.Context(), JSONRPCRequest{ID: 1, Method: "tools/call"}); done <- err }()
	<-entered
	mutateTransportCredential(t, service.store, target, func(slot *CredentialSlot) { slot.TokenRevision++; slot.Token.AccessToken = "access-after" })
	close(release)
	if err := <-done; err == nil {
		t.Fatal("original request succeeded")
	}
	file, err := service.store.InspectFile(t.Context(), target)
	if err != nil || file.Slots[target.ProfileID].RejectedRevision != 0 {
		t.Fatalf("late response changed current revision: %v", err)
	}
}

func TestHTTPInsufficientScopeDoesNotDisableOtherOperations(t *testing.T) {
	var calls atomic.Int32
	tr, service, target, _ := transportAuthFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", scope="files:write"`)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		writeTransportRPC(t, w, r)
	}))
	if _, err := tr.Send(t.Context(), JSONRPCRequest{ID: 1, Method: "tools/call", Params: map[string]any{"name": "write", "arguments": map[string]any{"private": "ignored"}}}); err == nil {
		t.Fatal("scope failure lost")
	}
	if _, err := tr.Send(t.Context(), JSONRPCRequest{ID: 2, Method: "tools/call", Params: map[string]any{"name": "read"}}); err != nil {
		t.Fatal(err)
	}
	view, err := service.Inspect(t.Context(), target)
	if err != nil || view.Auth != AuthReady || len(view.ScopeRequirements) != 1 || view.ScopeRequirements[0].RemoteTool != "write" {
		t.Fatalf("scope state=%+v err=%v", view, err)
	}
}

func TestOAuthBusinessRedirectNeverFollows(t *testing.T) {
	var calls atomic.Int32
	tr, _, _, _ := transportAuthFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Location", "/next")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	_, err := tr.Send(context.Background(), JSONRPCRequest{ID: 1, Method: "tools/call"})
	if DiagnoseHTTPError(err).ErrorClass != ErrorEndpointRedirect || calls.Load() != 1 {
		t.Fatalf("redirect calls=%d err=%v", calls.Load(), err)
	}
}

func TestHTTPValidCredentialMetadataChangeBlocksBeforeSend(t *testing.T) {
	for _, issuerChange := range []bool{false, true} {
		t.Run(map[bool]string{false: "resource", true: "issuer"}[issuerChange], func(t *testing.T) {
			var calls atomic.Int32
			transport, service, target, _ := transportAuthFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); writeTransportRPC(t, w, r) }))
			if _, err := transport.Send(t.Context(), JSONRPCRequest{ID: 1, Method: "tools/call"}); err != nil {
				t.Fatal(err)
			}
			metadata := service.discoverer.client.(*fixedOAuthMetadata)
			want := ErrorResourceMismatch
			if issuerChange {
				metadata.issuer = "https://another.example.invalid"
				want = ErrorIssuerMismatch
			} else {
				metadata.resource = target.Config.URL + "different"
			}
			service.discoverer.Invalidate(target)
			_, err := transport.Send(t.Context(), JSONRPCRequest{ID: 2, Method: "tools/call"})
			detail, ok := errors.AsType[*HTTPError](err)
			if !ok || detail.SendEvidence != SendNotSent || calls.Load() != 1 || diagnoseAuth(err).ErrorClass != want {
				t.Fatalf("metadata change sent credentials: calls=%d error=%v", calls.Load(), err)
			}
		})
	}
}
