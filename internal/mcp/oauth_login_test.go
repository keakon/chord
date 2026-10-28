package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
)

func loginFixture(t *testing.T) (*AuthService, Target, *atomic.Int32) {
	t.Helper()
	exchange := new(atomic.Int32)
	client, _, _ := publicOAuthTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource/mcp":
			_, _ = io.WriteString(w, `{"resource":"https://example.com/mcp","authorization_servers":["https://example.com"],"scopes_supported":["read"]}`)
		case "/.well-known/oauth-authorization-server":
			_, _ = io.WriteString(w, `{"issuer":"https://example.com","authorization_endpoint":"https://example.com/authorize","token_endpoint":"https://example.com/token","response_types_supported":["code"],"grant_types_supported":["authorization_code","refresh_token"],"token_endpoint_auth_methods_supported":["none"],"code_challenge_methods_supported":["S256"],"authorization_response_iss_parameter_supported":true}`)
		case "/token":
			exchange.Add(1)
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			if r.Form.Get("resource") != "https://example.com/mcp" || r.Form.Get("code_verifier") == "" {
				t.Error("exchange lost resource or PKCE")
			}
			_, _ = io.WriteString(w, `{"access_token":"sample-access","token_type":"Bearer","expires_in":3600}`)
		default:
			http.NotFound(w, r)
		}
	}))
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	targets, err := ResolveTargets(context.Background(), ResolveTargetOptions{ContentRoot: t.TempDir(), Main: config.MCPConfig{"sample": {URL: "https://example.com/mcp", Auth: config.MCPAuthOAuth, OAuth: &config.MCPOAuthConfig{Issuer: "https://example.com", ClientID: "sample-client", CallbackPort: new(port)}}}})
	if err != nil {
		t.Fatal(err)
	}
	business := &http.Client{Transport: probeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})}
	service := NewAuthService(AuthOptions{StateDir: t.TempDir(), HTTPClient: client, MCPHTTPClient: business})
	t.Cleanup(service.Close)
	return service, targets[0], exchange
}

func deliverLoginCallback(t *testing.T, progress LoginProgress) {
	t.Helper()
	if progress.AuthorizationURL == "" {
		return
	}
	link, err := url.Parse(progress.AuthorizationURL)
	if err != nil {
		t.Error(err)
		return
	}
	query := link.Query()
	callback, err := url.Parse(query.Get("redirect_uri"))
	if err != nil {
		t.Error(err)
		return
	}
	callback.RawQuery = url.Values{"state": {query.Get("state")}, "code": {"sample-code"}, "iss": {"https://example.com"}}.Encode()
	response, err := http.Get(callback.String())
	if err != nil {
		t.Error(err)
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Errorf("callback status=%d", response.StatusCode)
	}
}

func TestLoginSavesBeforeValidationFailure(t *testing.T) {
	service, target, exchange := loginFixture(t)
	// Cancelling after committing must retain the saved token and report that fact.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err := service.Login(ctx, target, LoginOptions{Reauth: true}, func(p LoginProgress) {
		if p.State == OperationValidating {
			cancel()
		}
		deliverLoginCallback(t, p)
	})
	if err == nil || !result.CredentialSaved || exchange.Load() != 1 {
		t.Fatalf("saved=%t requests=%d error=%v", result.CredentialSaved, exchange.Load(), err)
	}
	file, err := service.store.InspectFile(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	slot := fileSlot(file, target)
	if slot == nil || slot.Token == nil || slot.Token.AccessToken != "sample-access" || slot.Token.RefreshToken != "" || slot.AuthGeneration != 2 || slot.TokenRevision != 1 {
		t.Fatal("successful exchange did not preserve saved token")
	}
	// The safe view must not contain the secret even when encoded by CLI callers.
	view, err := service.Inspect(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(view)
	var fields map[string]any
	if json.Unmarshal(raw, &fields) != nil {
		t.Fatal("invalid safe view")
	}
	if _, ok := fields["token"]; ok {
		t.Fatal("credential leaked to view")
	}
}

func TestReauthCancelledPreservesPreviousGeneration(t *testing.T) {
	service, target, exchange := loginFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	_, err := service.Login(ctx, target, LoginOptions{Reauth: true}, func(p LoginProgress) {
		if p.State == OperationAwaitingCallback {
			cancel()
		}
	})
	if err == nil || exchange.Load() != 0 {
		t.Fatalf("cancelled login exchanged a token: %v", err)
	}
	file, err := service.store.InspectFile(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	slot := fileSlot(file, target)
	if slot.AuthGeneration != 1 || slot.Token != nil {
		t.Fatal("cancelled login changed authentication generation")
	}
}

func TestLoginCancellationAfterCommitAdmissionStillSaves(t *testing.T) {
	service, target, exchange := loginFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	service.now = func() time.Time {
		service.mu.Lock()
		op := service.logins[targetKey(target)]
		committing := op != nil && op.committing
		service.mu.Unlock()
		if committing {
			cancel()
		}
		return time.Now()
	}
	result, err := service.Login(ctx, target, LoginOptions{Reauth: true}, func(p LoginProgress) { deliverLoginCallback(t, p) })
	if !result.CredentialSaved || err == nil || exchange.Load() != 1 {
		t.Fatalf("commit admission: saved=%t exchange=%d err=%v", result.CredentialSaved, exchange.Load(), err)
	}
	file, err := service.store.InspectFile(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	if slot := fileSlot(file, target); slot.Token == nil || slot.AuthGeneration != 2 {
		t.Fatal("admitted commit was rolled back by cancellation")
	}
}

func TestLoginConfigurationChangeRejectsOldOperation(t *testing.T) {
	service, target, exchange := loginFixture(t)
	service.UpdateTargets(target.Ref.Owner, []Target{target})
	result, err := service.Login(t.Context(), target, LoginOptions{Reauth: true}, func(p LoginProgress) {
		if p.State == OperationCommitting {
			replacement := target.Snapshot()
			replacement.ConfigGeneration++
			service.UpdateTargets(target.Ref.Owner, []Target{replacement})
		}
		deliverLoginCallback(t, p)
	})
	if err == nil || result.CredentialSaved || exchange.Load() != 1 {
		t.Fatalf("stale operation: saved=%t exchange=%d err=%v", result.CredentialSaved, exchange.Load(), err)
	}
	file, err := service.store.InspectFile(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	if slot := fileSlot(file, target); slot.Token != nil || slot.AuthGeneration != 1 {
		t.Fatal("old configuration committed credentials")
	}
}

func TestLoginScopeHistoryDoesNotHideCatalogFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params map[string]any  `json:"params"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if len(request.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if request.Method == "tools/list" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"protocolVersion": request.Params["protocolVersion"], "serverInfo": map[string]string{"name": "sample", "version": "test"}, "capabilities": map[string]any{"tools": map[string]any{}}}})
	}))
	defer server.Close()
	service, target, _ := loginFixture(t)
	target.Config.URL = server.URL + "/mcp"
	target.TargetID = identityHash(struct{ Workspace, Owner, Endpoint string }{target.Ref.WorkspaceID, target.Ref.Owner, target.Config.URL})
	p := profileForTarget(target)
	b := &CredentialBinding{Resource: target.Config.URL, Issuer: p.IssuerHint, ClientID: p.ConfiguredClientID, TokenAuthMethod: p.TokenAuthMethod, RedirectURI: p.RedirectURI}
	b.BindingID = credentialBindingID(target.TargetID, target.ProfileID, *b)
	metadata := newMetadataFixture()
	metadata.documents[server.URL+"/.well-known/oauth-protected-resource/mcp"] = `{"resource":"` + target.Config.URL + `","authorization_servers":["https://example.com"]}`
	metadata.documents["https://example.com/.well-known/oauth-authorization-server"] = `{"issuer":"https://example.com","authorization_endpoint":"https://example.com/authorize","token_endpoint":"https://example.com/token","response_types_supported":["code"],"code_challenge_methods_supported":["S256"]}`
	service.discoverer = NewOAuthDiscoverer(metadata)
	service.mcpHTTPClient = server.Client()
	if err := service.store.UpdateLocked(t.Context(), target, func(tx *CredentialTransaction) error {
		tx.File.Slots[target.ProfileID] = &CredentialSlot{Profile: p, Status: CredentialReady, AuthGeneration: 1, TokenRevision: 1, Binding: b, Token: &CredentialToken{AccessToken: "sample-access", IssuedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour).UTC(), LifetimeSeconds: 3600}, ScopeRequirements: []ScopeRequirement{{BindingID: b.BindingID, AuthGeneration: 1, TokenRevision: 1, Scopes: []string{"write"}, ErrorClass: ErrorInsufficientScope, ObservedAt: time.Now().UTC()}}}
		_, err := tx.Commit()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	opened := false
	_, err := service.Login(ctx, target, LoginOptions{}, func(p LoginProgress) {
		if p.State == OperationAwaitingCallback {
			opened = true
			cancel()
		}
	})
	if err == nil || opened {
		t.Fatalf("catalog failure started authorization: opened=%t err=%v", opened, err)
	}
}
