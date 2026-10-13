package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/keakon/chord/internal/config"
)

type registrationFixture struct {
	response string
	err      error
	calls    int
	request  dynamicRegistrationRequest
}

func (f *registrationFixture) PostJSON(_ context.Context, _ string, in, out any) error {
	f.calls++
	f.request = in.(dynamicRegistrationRequest)
	if f.err != nil {
		return f.err
	}
	return json.Unmarshal([]byte(f.response), out)
}
func registrationMetadata() OAuthDiscovery {
	return OAuthDiscovery{Issuer: "https://auth.example.invalid", AuthorizationServer: AuthorizationServerMetadata{Issuer: "https://auth.example.invalid", RegistrationEndpoint: "https://auth.example.invalid/register", TokenEndpointAuthMethodsSupported: []string{config.MCPOAuthAuthMethodNone, config.MCPOAuthAuthMethodClientSecretBasic}, GrantTypesSupported: []string{"authorization_code", "refresh_token"}}}
}

func TestOAuthRegistrationMechanismSelection(t *testing.T) {
	t.Run("pre registered", func(t *testing.T) {
		f := &registrationFixture{}
		target := discoveryTarget()
		target.Config.OAuth = &config.MCPOAuthConfig{Issuer: "https://auth.example.invalid", ClientID: "sample-client", TokenEndpointAuthMethod: config.MCPOAuthAuthMethodClientSecretBasic, ClientSecretEnv: "CHORD_SAMPLE_CLIENT_SECRET"}
		metadata := registrationMetadata()
		metadata.AuthorizationServer.ClientIDMetadataDocumentSupported = true
		got, err := NewOAuthRegistrar(f).Register(context.Background(), target, metadata)
		if err != nil || got.ClientID != "sample-client" || got.ClientSecret != "" || f.calls != 0 {
			t.Fatalf("registration = %#v, err %v, calls %d", got, err, f.calls)
		}
	})
	t.Run("client metadata", func(t *testing.T) {
		f := &registrationFixture{}
		metadata := registrationMetadata()
		metadata.AuthorizationServer.ClientIDMetadataDocumentSupported = true
		got, err := NewOAuthRegistrar(f).Register(context.Background(), discoveryTarget(), metadata)
		if err != nil || got.ClientID != OAuthClientMetadataURL || f.calls != 0 {
			t.Fatalf("registration = %#v, err %v", got, err)
		}
		target := discoveryTarget()
		target.Config.OAuth = &config.MCPOAuthConfig{CallbackPort: new(41938)}
		if _, err := NewOAuthRegistrar(f).Register(context.Background(), target, metadata); err == nil || f.calls != 0 {
			t.Fatal("invalid CIMD callback fell through to DCR")
		}
	})
	t.Run("dynamic", func(t *testing.T) {
		f := &registrationFixture{response: `{"client_id":"sample-client","client_secret":"sample-secret","client_secret_expires_at":0,"redirect_uris":["http://127.0.0.1:41937/oauth/callback"],"token_endpoint_auth_method":"none","grant_types":["refresh_token","authorization_code"],"response_types":["code"]}`}
		got, err := NewOAuthRegistrar(f).Register(context.Background(), discoveryTarget(), registrationMetadata())
		if err != nil || got.ClientID != "sample-client" || got.ClientSecret != "sample-secret" || !got.SecretExpiresAt.IsZero() || f.calls != 1 {
			t.Fatalf("unexpected dynamic result, err %v", err)
		}
		if f.request.ApplicationType != "native" || f.request.TokenEndpointAuthMethod != "none" || !reflect.DeepEqual(f.request.GrantTypes, []string{"authorization_code", "refresh_token"}) {
			t.Fatalf("request = %#v", f.request)
		}
	})
}

func TestOAuthRegistrationRejectsChangedContract(t *testing.T) {
	for _, response := range []string{
		`{"client_id":"sample-client","redirect_uris":["http://127.0.0.1:41937/oauth/callback"]}`,
		`{"client_id":"sample-client","redirect_uris":["http://127.0.0.1:41938/oauth/callback"],"token_endpoint_auth_method":"none"}`,
		`{"client_id":"sample-client","redirect_uris":["http://127.0.0.1:41937/oauth/callback"],"token_endpoint_auth_method":"client_secret_basic"}`,
		`{"client_id":"","redirect_uris":["http://127.0.0.1:41937/oauth/callback"],"token_endpoint_auth_method":"none"}`,
		`{"client_id":"sample-client","redirect_uris":["http://127.0.0.1:41937/oauth/callback"],"token_endpoint_auth_method":"none","response_types":["token"]}`,
		`{"client_id":"sample-client","redirect_uris":["http://127.0.0.1:41937/oauth/callback"],"token_endpoint_auth_method":"none","client_secret_expires_at":-1}`,
	} {
		f := &registrationFixture{response: response}
		metadata := registrationMetadata()
		metadata.AuthorizationServer.GrantTypesSupported = []string{"authorization_code"}
		if _, err := NewOAuthRegistrar(f).Register(context.Background(), discoveryTarget(), metadata); err == nil || f.calls != 1 {
			t.Fatalf("invalid response accepted: %s", response)
		}
	}
}

func TestOAuthRegistrationDoesNotRetryUnknownOutcome(t *testing.T) {
	failure := &OAuthHTTPError{ErrorClass: ErrorNetwork, SendEvidence: SendOutcomeUnknown}
	f := &registrationFixture{err: failure}
	_, err := NewOAuthRegistrar(f).Register(context.Background(), discoveryTarget(), registrationMetadata())
	if !errors.Is(err, failure) || f.calls != 1 {
		t.Fatalf("unknown outcome retried or replaced: %v, %d", err, f.calls)
	}
}

func TestOAuthRegistrationMetadataDefaultsDoNotImplyPublicClient(t *testing.T) {
	f := &registrationFixture{}
	metadata := registrationMetadata()
	metadata.AuthorizationServer.TokenEndpointAuthMethodsSupported = []string{config.MCPOAuthAuthMethodClientSecretBasic}
	if _, err := NewOAuthRegistrar(f).Register(context.Background(), discoveryTarget(), metadata); err == nil || f.calls != 0 {
		t.Fatal("public client support inferred from missing declaration")
	}
	metadata = registrationMetadata()
	metadata.AuthorizationServer.GrantTypesSupported = []string{"authorization_code"}
	f.response = `{"client_id":"sample-client","redirect_uris":["http://127.0.0.1:41937/oauth/callback"],"token_endpoint_auth_method":"none"}`
	got, err := NewOAuthRegistrar(f).Register(context.Background(), discoveryTarget(), metadata)
	if err != nil || !reflect.DeepEqual(got.GrantTypes, []string{"authorization_code"}) {
		t.Fatalf("registration defaults = %#v, %v", got, err)
	}
}
