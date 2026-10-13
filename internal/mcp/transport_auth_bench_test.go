package mcp

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
)

// BenchmarkHTTPToolRequest measures credential preparation together with the
// actual local MCP POST. Fixture construction and explicit cache invalidation
// are outside the timed interval; persistence required by the request is inside.
func BenchmarkHTTPToolRequest(b *testing.B) {
	for _, scenario := range []string{"anonymous", "valid_credential", "first_confirmation", "refresh"} {
		b.Run(scenario, func(b *testing.B) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request JSONRPCRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					b.Error(err)
					return
				}
				_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"content":[]}}`, request.ID)
			}))
			defer server.Close()
			target := Target{Ref: ServerRef{WorkspaceID: "benchmark-workspace", Owner: MainOwner, ServerName: "sample"}, Config: ServerConfig{Name: "sample", URL: server.URL + "/mcp", Auth: config.MCPAuthNone}, ConfigGeneration: 1}
			target.TargetID = identityHash(struct{ Workspace, Owner, Endpoint string }{target.Ref.WorkspaceID, target.Ref.Owner, target.Config.URL})
			target.ProfileID = identityHash(profileForTarget(target))
			options := HTTPTransportOptions{Endpoint: target.Config.URL, Target: target}
			var store *CredentialStore
			clock := time.Now().UTC()
			if scenario != "anonymous" {
				target.Config.Auth = config.MCPAuthOAuth
				options.Target = target
				store = NewCredentialStore(b.TempDir())
				profile := profileForTarget(target)
				binding := &CredentialBinding{Resource: target.Config.URL, Issuer: "https://auth.example.invalid/tenant", ClientID: "sample-client", TokenAuthMethod: profile.TokenAuthMethod, RedirectURI: profile.RedirectURI}
				binding.BindingID = credentialBindingID(target.TargetID, target.ProfileID, *binding)
				if err := store.UpdateLocked(b.Context(), target, func(tx *CredentialTransaction) error {
					tx.File.Slots[target.ProfileID] = &CredentialSlot{Profile: profile, AuthGeneration: 1, TokenRevision: 1, Status: CredentialReady, Binding: binding, Token: &CredentialToken{AccessToken: "sample-access", RefreshToken: "sample-refresh", IssuedAt: clock, ExpiresAt: clock.Add(time.Hour), LifetimeSeconds: 3600, RequestedScopes: []string{}, GrantedScopes: []string{}}}
					_, err := tx.Commit()
					return err
				}); err != nil {
					b.Fatal(err)
				}
				control := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.WriteString(w, `{"access_token":"sample-after","refresh_token":"sample-refresh","token_type":"Bearer","expires_in":3600}`)
				}))
				defer control.Close()
				roots := x509.NewCertPool()
				roots.AddCert(control.Certificate())
				controlClient := NewOAuthHTTPClient(OAuthHTTPOptions{RootCAs: roots,
					LookupIP: func(context.Context, string) ([]net.IPAddr, error) {
						return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
					},
					DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
						return (&net.Dialer{}).DialContext(ctx, network, control.Listener.Addr().String())
					},
				})
				service := NewAuthService(AuthOptions{Store: store, Clock: func() time.Time { return clock }, HTTPClient: controlClient, Discoverer: NewOAuthDiscoverer(&fixedOAuthMetadata{resource: target.Config.URL, issuer: binding.Issuer})})
				defer service.Close()
				options.AuthService = service
				options.ExpectedIdentity = ExpectedIdentity{BindingID: binding.BindingID, AuthGeneration: 1}
			}
			transport := NewHTTPTransport(options)
			request := JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: map[string]any{"name": "sample", "arguments": map[string]any{}}}
			b.ReportAllocs()
			for b.Loop() {
				b.StopTimer()
				switch scenario {
				case "first_confirmation":
					store.forget(target.TargetID)
				case "refresh":
					clock = clock.Add(2 * time.Hour)
				}
				b.StartTimer()
				if _, err := transport.Send(b.Context(), request); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
