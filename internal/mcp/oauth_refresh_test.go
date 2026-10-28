package mcp

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keakon/chord/internal/atomicfile"
)

func refreshTestTarget() Target {
	t := storeTestTarget()
	t.Config.URL = "https://resource.example.invalid/mcp"
	t.TargetID = identityHash(struct{ Workspace, Owner, Endpoint string }{t.Ref.WorkspaceID, t.Ref.Owner, t.Config.URL})
	return t
}

func refreshTestDiscovery() *OAuthDiscoverer {
	f := newMetadataFixture()
	f.documents["https://auth.example.invalid/.well-known/oauth-authorization-server/tenant"] = `{"issuer":"https://auth.example.invalid/tenant","authorization_endpoint":"https://auth.example.invalid/authorize","token_endpoint":"https://example.com/token","response_types_supported":["code"],"grant_types_supported":["authorization_code","refresh_token"],"token_endpoint_auth_methods_supported":["none"],"code_challenge_methods_supported":["S256"]}`
	return NewOAuthDiscoverer(f)
}

func seedRefreshTest(t *testing.T, store *CredentialStore, target Target, now time.Time) ExpectedIdentity {
	t.Helper()
	profile := profileForTarget(target)
	binding := &CredentialBinding{Resource: target.Config.URL, Issuer: "https://auth.example.invalid/tenant", ClientID: "sample-client", TokenAuthMethod: profile.TokenAuthMethod, RedirectURI: profile.RedirectURI}
	binding.BindingID = credentialBindingID(target.TargetID, target.ProfileID, *binding)
	err := store.UpdateLocked(t.Context(), target, func(tx *CredentialTransaction) error {
		tx.File.Slots[target.ProfileID] = &CredentialSlot{Profile: profile, AuthGeneration: 1, TokenRevision: 1, Status: CredentialReady, Binding: binding, Token: &CredentialToken{AccessToken: "access-before", RefreshToken: "refresh-before", IssuedAt: now.Add(-time.Hour).UTC(), ExpiresAt: now.Add(-time.Second).UTC(), LifetimeSeconds: 3600, RequestedScopes: []string{"files:read"}, GrantedScopes: []string{"files:read"}}}
		_, err := tx.Commit()
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return ExpectedIdentity{BindingID: binding.BindingID, AuthGeneration: 1}
}

func TestOAuthRefreshSingleflight(t *testing.T) {
	var requests atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	client, _, _ := publicOAuthTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			close(entered)
		}
		<-release
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("resource") != "https://resource.example.invalid/mcp" || r.Form.Get("refresh_token") != "refresh-before" {
			t.Error("incorrect refresh binding")
		}
		_, _ = io.WriteString(w, `{"access_token":"access-after","token_type":"Bearer","expires_in":3600}`)
	}))
	store := NewCredentialStore(t.TempDir())
	target := refreshTestTarget()
	expected := seedRefreshTest(t, store, target, time.Now())
	s := NewAuthService(AuthOptions{Store: store, HTTPClient: client, Discoverer: refreshTestDiscovery()})
	defer s.Close()
	ctx, cancel := context.WithCancel(t.Context())
	first := make(chan error, 1)
	go func() { _, err := s.Acquire(ctx, target, expected); first <- err }()
	<-entered
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter: %v", err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			lease, err := s.Acquire(t.Context(), target, expected)
			if err != nil || lease.token != "access-after" || lease.version.TokenRevision != 2 {
				t.Errorf("acquire result: %v", err)
			}
		})
	}
	close(release)
	wg.Wait()
	if requests.Load() != 1 {
		t.Fatalf("refresh POSTs: %d", requests.Load())
	}
	file, err := store.InspectFile(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	if file.Slots[target.ProfileID].Token.RefreshToken != "refresh-before" {
		t.Fatal("omitted refresh token discarded existing token")
	}
}

func TestOAuthRefreshFailureStates(t *testing.T) {
	for _, tc := range []struct {
		name, body      string
		status          int
		class           ErrorClass
		marker, blocked bool
	}{
		{"invalid grant", `{"error":"invalid_grant"}`, 400, ErrorRefreshInvalid, false, false},
		{"invalid client", `{"error":"invalid_client"}`, 401, ErrorRefreshBlocked, false, true},
		{"invalid scope", `{"error":"invalid_scope"}`, 400, ErrorRefreshBlocked, false, true},
		{"server failure", `{"error":"server_error"}`, 500, ErrorRefreshOutcomeUnknown, true, false},
		{"malformed success", `{"access_token":"value","token_type":"unknown"}`, 200, ErrorRefreshOutcomeUnknown, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			client, _, _ := publicOAuthTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			store := NewCredentialStore(t.TempDir())
			target := refreshTestTarget()
			expected := seedRefreshTest(t, store, target, time.Now())
			s := NewAuthService(AuthOptions{Store: store, HTTPClient: client, Discoverer: refreshTestDiscovery()})
			defer s.Close()
			if _, err := s.Acquire(t.Context(), target, expected); diagnoseAuth(err).ErrorClass != tc.class {
				t.Fatalf("class: %v", err)
			}
			file, err := store.InspectFile(t.Context(), target)
			if err != nil {
				t.Fatal(err)
			}
			slot := file.Slots[target.ProfileID]
			if (slot.RefreshInflight != nil) != tc.marker || (slot.RefreshBlocked != nil) != tc.blocked {
				t.Fatal("incorrect durable refresh state")
			}
			if _, err := s.Acquire(t.Context(), target, expected); err == nil {
				t.Fatal("unusable token returned")
			}
			if requests.Load() != 1 {
				t.Fatalf("unsafe refresh replay: %d", requests.Load())
			}
			if tc.blocked {
				old := BlockedRefreshStamp{CredentialVersion: slotVersion(slot), BlockID: slot.RefreshBlocked.BlockID}
				if changed, err := s.RetryBlockedRefresh(t.Context(), target, old); err != nil || !changed {
					t.Fatalf("explicit retry: %v", err)
				}
				_, _ = s.Acquire(t.Context(), target, expected)
				if changed, err := s.RetryBlockedRefresh(t.Context(), target, old); err != nil || changed {
					t.Fatalf("old stamp cleared new block: %v", err)
				}
			}
		})
	}
}

func TestOAuthRejectionCASAndLogout(t *testing.T) {
	store := NewCredentialStore(t.TempDir())
	target := refreshTestTarget()
	expected := seedRefreshTest(t, store, target, time.Now())
	s := NewAuthService(AuthOptions{Store: store})
	defer s.Close()
	stamp := RequestStamp{BindingID: expected.BindingID, AuthGeneration: expected.AuthGeneration, TokenRevision: 1}
	if _, err := s.Logout(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	if err := s.ObserveRejection(t.Context(), target, stamp, Challenge{StatusCode: 401}); err != nil {
		t.Fatal(err)
	}
	file, err := store.InspectFile(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	if slot := file.Slots[target.ProfileID]; slot.Status != CredentialLoggedOut || slot.RejectedRevision != 0 || slot.AuthGeneration != 2 {
		t.Fatal("old request mutated logged-out generation")
	}
	if _, err := s.Acquire(t.Context(), target, expected); diagnoseAuth(err).ErrorClass != ErrorIdentityChanged {
		t.Fatalf("old identity: %v", err)
	}
}

func TestOAuthAcquireRechecksAfterSharedRefresh(t *testing.T) {
	store := NewCredentialStore(t.TempDir())
	target := refreshTestTarget()
	expected := seedRefreshTest(t, store, target, time.Now())
	s := NewAuthService(AuthOptions{Store: store})
	defer s.Close()
	read := make(chan struct{})
	var once sync.Once
	store.forget(target.TargetID)
	store.replace = func(path string, data []byte, mode os.FileMode) (atomicfile.ReplaceOutcome, error) {
		outcome, err := atomicfile.ReplaceDurable(path, data, mode)
		once.Do(func() { close(read) })
		return outcome, err
	}
	finished := make(chan struct{})
	close(finished)
	s.mu.Lock()
	s.flights[targetKey(target)] = &refreshFlight{done: finished}
	result := make(chan error, 1)
	go func() { _, err := s.Acquire(t.Context(), target, expected); result <- err }()
	<-read
	err := store.UpdateLocked(t.Context(), target, func(tx *CredentialTransaction) error {
		slot := tx.File.Slots[target.ProfileID]
		slot.TokenRevision = 2
		slot.RejectedRevision = 2
		slot.RefreshBlocked = &CredentialRefreshBlocked{BlockID: "sample-block", BindingID: expected.BindingID, AuthGeneration: 1, TokenRevision: 2, OAuthError: "invalid_client", ErrorClass: ErrorRefreshBlocked, BlockedAt: time.Now().UTC()}
		_, err := tx.Commit()
		return err
	})
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := <-result; diagnoseAuth(err).ErrorClass != ErrorRefreshBlocked {
		t.Fatalf("blocked revision returned after shared refresh: %v", err)
	}
}

func TestOAuthRefreshProcessTransactions(t *testing.T) {
	if home := os.Getenv("CHORD_REFRESH_CHILD_HOME"); home != "" {
		roots := x509.NewCertPool()
		der, err := base64.StdEncoding.DecodeString(os.Getenv("CHORD_REFRESH_TEST_CERT"))
		if err != nil {
			os.Exit(2)
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			os.Exit(2)
		}
		roots.AddCert(cert)
		client := NewOAuthHTTPClient(OAuthHTTPOptions{RootCAs: roots, LookupIP: func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
		}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, os.Getenv("CHORD_REFRESH_TEST_ADDRESS"))
		}})
		store := NewCredentialStore(home)
		target := refreshTestTarget()
		file, err := store.InspectFile(context.Background(), target)
		if err != nil {
			os.Exit(3)
		}
		slot := file.Slots[target.ProfileID]
		s := NewAuthService(AuthOptions{Store: store, HTTPClient: client, Discoverer: refreshTestDiscovery()})
		lease, err := s.Acquire(context.Background(), target, ExpectedIdentity{BindingID: slot.Binding.BindingID, AuthGeneration: slot.AuthGeneration})
		s.Close()
		if err != nil || lease.token != "access-after" || lease.version.TokenRevision != 2 {
			os.Exit(4)
		}
		os.Exit(0)
	}
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, `{"access_token":"access-after","token_type":"Bearer","expires_in":3600}`)
	}))
	defer server.Close()
	store := NewCredentialStore(t.TempDir())
	target := refreshTestTarget()
	seedRefreshTest(t, store, target, time.Now())
	var children []*exec.Cmd
	for range 3 {
		cmd := exec.Command(os.Args[0], "-test.run=^TestOAuthRefreshProcessTransactions$")
		cmd.Env = append(os.Environ(), "CHORD_REFRESH_CHILD_HOME="+store.stateDir, "CHORD_REFRESH_TEST_CERT="+base64.StdEncoding.EncodeToString(server.Certificate().Raw), "CHORD_REFRESH_TEST_ADDRESS="+server.Listener.Addr().String())
		cmd.Stdout = new(bytes.Buffer)
		cmd.Stderr = cmd.Stdout
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		children = append(children, cmd)
	}
	for _, cmd := range children {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("child: %v, %s", err, cmd.Stdout)
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("cross-process refresh count: %d", requests.Load())
	}
}

func TestOAuthRefreshProcessCrash(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			close(entered)
		}
		<-release
		_, _ = io.WriteString(w, `{"access_token":"access-after","token_type":"Bearer","expires_in":3600}`)
	}))
	defer server.Close()
	store := NewCredentialStore(t.TempDir())
	target := refreshTestTarget()
	expected := seedRefreshTest(t, store, target, time.Now())
	cmd := exec.Command(os.Args[0], "-test.run=^TestOAuthRefreshProcessTransactions$")
	cmd.Env = append(os.Environ(), "CHORD_REFRESH_CHILD_HOME="+store.stateDir, "CHORD_REFRESH_TEST_CERT="+base64.StdEncoding.EncodeToString(server.Certificate().Raw), "CHORD_REFRESH_TEST_ADDRESS="+server.Listener.Addr().String())
	if err := cmd.Start(); err != nil {
		close(release)
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		close(release)
		t.Fatal("child did not reach token endpoint")
	}
	if err := cmd.Process.Kill(); err != nil {
		close(release)
		t.Fatal(err)
	}
	_ = cmd.Wait()
	close(release)
	s := NewAuthService(AuthOptions{Store: store})
	defer s.Close()
	if _, err := s.Acquire(t.Context(), target, expected); diagnoseAuth(err).ErrorClass != ErrorRefreshOutcomeUnknown {
		t.Fatalf("crash recovery: %v", err)
	}
	file, err := store.InspectFile(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	slot := file.Slots[target.ProfileID]
	if slot.RefreshInflight == nil || slot.Status != CredentialRefreshOutcomeUnknown || requests.Load() != 1 {
		t.Fatal("interrupted refresh replayed or marker lost")
	}
}

func TestOAuthRefreshSaveFailureRecovery(t *testing.T) {
	for _, afterReplacement := range []bool{false, true} {
		t.Run(map[bool]string{false: "not replaced", true: "replacement unknown"}[afterReplacement], func(t *testing.T) {
			var requests atomic.Int32
			client, _, _ := publicOAuthTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				_, _ = io.WriteString(w, `{"access_token":"access-after","token_type":"Bearer","expires_in":3600}`)
			}))
			store := NewCredentialStore(t.TempDir())
			target := refreshTestTarget()
			expected := seedRefreshTest(t, store, target, time.Now())
			store.replace = func(path string, raw []byte, mode os.FileMode) (atomicfile.ReplaceOutcome, error) {
				var file CredentialFile
				if err := json.Unmarshal(raw, &file); err != nil {
					t.Fatal(err)
				}
				if file.Slots[target.ProfileID].TokenRevision == 2 {
					if afterReplacement {
						if _, err := atomicfile.ReplaceDurable(path, raw, mode); err != nil {
							return atomicfile.NotReplaced, err
						}
						return atomicfile.ReplacementUnknown, errors.New("injected persistence failure")
					}
					return atomicfile.NotReplaced, errors.New("injected persistence failure")
				}
				return atomicfile.ReplaceDurable(path, raw, mode)
			}
			s := NewAuthService(AuthOptions{Store: store, HTTPClient: client, Discoverer: refreshTestDiscovery()})
			defer s.Close()
			if _, err := s.Acquire(t.Context(), target, expected); diagnoseAuth(err).ErrorClass != ErrorCredentialStoreFailed {
				t.Fatalf("save failure: %v", err)
			}
			store.replace = atomicfile.ReplaceDurable
			lease, err := s.Acquire(t.Context(), target, expected)
			if afterReplacement {
				if err != nil || lease.token != "access-after" {
					t.Fatalf("new token recovery: %v", err)
				}
			} else if diagnoseAuth(err).ErrorClass != ErrorRefreshOutcomeUnknown {
				t.Fatalf("marker recovery: %v", err)
			}
			if requests.Load() != 1 {
				t.Fatal("refresh replayed after unknown persistence")
			}
		})
	}
}

func TestOAuthInspectInflightRequiresLockConfirmation(t *testing.T) {
	store := NewCredentialStore(t.TempDir())
	target := refreshTestTarget()
	expected := seedRefreshTest(t, store, target, time.Now())
	if err := store.UpdateLocked(t.Context(), target, func(tx *CredentialTransaction) error {
		slot := fileSlot(tx.File, target)
		slot.RefreshInflight = &CredentialRefreshInflight{OperationID: "sample-operation", AuthGeneration: 1, TokenRevision: 1, StartedAt: time.Now().UTC()}
		_, err := tx.Commit()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	service := NewAuthService(AuthOptions{Store: store})
	defer service.Close()
	view, err := service.Inspect(t.Context(), target)
	if err != nil || view.Auth != AuthRefreshRequired || view.Diagnostic.ErrorClass != "" {
		t.Fatalf("inspect prematurely classified interruption: %+v %v", view, err)
	}
	if _, err := service.Acquire(t.Context(), target, expected); diagnoseAuth(err).ErrorClass != ErrorRefreshOutcomeUnknown {
		t.Fatalf("lock confirmation: %v", err)
	}
	view, err = service.Inspect(t.Context(), target)
	if err != nil || view.Diagnostic.ErrorClass != ErrorRefreshOutcomeUnknown {
		t.Fatalf("confirmed interruption: %+v %v", view, err)
	}
}

func TestOAuthRefreshScopeDefaultsToGrantedAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name, scope string
		want        []string
	}{
		{"omitted", "", []string{"files:read"}},
		{"explicit", `,"scope":"files:read files:list"`, []string{"files:list", "files:read"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _, _ := publicOAuthTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				if r.Form.Has("scope") {
					t.Error("refresh unexpectedly requests scopes")
				}
				_, _ = io.WriteString(w, `{"access_token":"access-after","token_type":"Bearer","expires_in":3600`+tc.scope+`}`)
			}))
			store := NewCredentialStore(t.TempDir())
			target := refreshTestTarget()
			expected := seedRefreshTest(t, store, target, time.Now())
			requested := []string{"files:read", "files:write"}
			if err := store.UpdateLocked(t.Context(), target, func(tx *CredentialTransaction) error {
				tx.File.Slots[target.ProfileID].Token.RequestedScopes = requested
				_, err := tx.Commit()
				return err
			}); err != nil {
				t.Fatal(err)
			}
			service := NewAuthService(AuthOptions{Store: store, HTTPClient: client, Discoverer: refreshTestDiscovery()})
			defer service.Close()
			if _, err := service.Acquire(t.Context(), target, expected); err != nil {
				t.Fatal(err)
			}
			file, err := store.InspectFile(t.Context(), target)
			if err != nil {
				t.Fatal(err)
			}
			token := file.Slots[target.ProfileID].Token
			if !slices.Equal(token.GrantedScopes, tc.want) || !slices.Equal(token.RequestedScopes, requested) {
				t.Fatalf("scopes: granted=%v requested=%v", token.GrantedScopes, token.RequestedScopes)
			}
		})
	}
}
