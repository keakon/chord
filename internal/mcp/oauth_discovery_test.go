package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"sync"
	"testing"

	"github.com/keakon/chord/internal/config"
)

type metadataFixture struct {
	mu        sync.Mutex
	documents map[string]string
	failures  map[string]error
	calls     []string
	validated []string
}

func (f *metadataFixture) GetJSON(_ context.Context, endpoint string, out any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, endpoint)
	if err := f.failures[endpoint]; err != nil {
		return err
	}
	raw, ok := f.documents[endpoint]
	if !ok {
		return &OAuthHTTPError{StatusCode: http.StatusNotFound}
	}
	return json.Unmarshal([]byte(raw), out)
}
func (f *metadataFixture) ValidateEndpoint(_ context.Context, endpoint string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.validated = append(f.validated, endpoint)
	return nil
}
func newMetadataFixture() *metadataFixture {
	return &metadataFixture{documents: map[string]string{
		"https://resource.example.invalid/.well-known/oauth-protected-resource/mcp":  `{"resource":"https://resource.example.invalid/mcp","authorization_servers":["https://auth.example.invalid/tenant"],"scopes_supported":["files:read"]}`,
		"https://auth.example.invalid/.well-known/oauth-authorization-server/tenant": `{"issuer":"https://auth.example.invalid/tenant","authorization_endpoint":"https://auth.example.invalid/authorize","token_endpoint":"https://auth.example.invalid/token","response_types_supported":["code"],"code_challenge_methods_supported":["S256"]}`,
	}, failures: make(map[string]error)}
}
func discoveryTarget() Target {
	return Target{TargetID: "sample-target", ProfileID: "sample-profile", ConfigGeneration: 1, Config: ServerConfig{URL: "https://resource.example.invalid/mcp", Auth: config.MCPAuthAuto}}
}

func TestOAuthDiscoveryDefaultsAndCache(t *testing.T) {
	f := newMetadataFixture()
	d := NewOAuthDiscoverer(f)
	target := discoveryTarget()
	result, err := d.Discover(context.Background(), target, DiscoveryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.AuthorizationServer.GrantTypesSupported, []string{"authorization_code", "implicit"}) || !reflect.DeepEqual(result.AuthorizationServer.TokenEndpointAuthMethodsSupported, []string{"client_secret_basic"}) {
		t.Fatal("metadata defaults differ from RFC semantics")
	}
	if result.Resource != target.Config.URL || !reflect.DeepEqual(result.Scopes, []string{"files:read"}) {
		t.Fatalf("unexpected result: %#v", result)
	}
	if len(f.validated) != 3 {
		t.Fatalf("endpoint policy checks = %v", f.validated)
	}
	result.Scopes[0] = "changed"
	result.AuthorizationServer.GrantTypesSupported[0] = "changed"
	cached, err := d.Discover(context.Background(), target, DiscoveryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 2 || cached.Scopes[0] != "files:read" || cached.AuthorizationServer.GrantTypesSupported[0] != "authorization_code" {
		t.Fatal("cache aliases output or performed network calls")
	}
	target.ConfigGeneration++
	if _, err = d.Discover(context.Background(), target, DiscoveryOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 4 {
		t.Fatal("configuration generation did not invalidate cache")
	}
}

func TestOAuthDiscoveryChallengeFallback(t *testing.T) {
	for _, headers := range [][]string{nil, {`Basic realm="sample"`}, {`Bearer realm="sample"`}} {
		f := newMetadataFixture()
		if _, err := NewOAuthDiscoverer(f).Discover(context.Background(), discoveryTarget(), DiscoveryOptions{AuthenticateHeaders: headers}); err != nil {
			t.Fatal(err)
		}
	}
	f := newMetadataFixture()
	f.documents["https://metadata.example.invalid/resource"] = f.documents["https://resource.example.invalid/.well-known/oauth-protected-resource/mcp"]
	if _, err := NewOAuthDiscoverer(f).Discover(context.Background(), discoveryTarget(), DiscoveryOptions{AuthenticateHeaders: []string{`Bearer resource_metadata="https://metadata.example.invalid/resource", scope="write read"`}}); err != nil {
		t.Fatal(err)
	}
	if f.calls[0] != "https://metadata.example.invalid/resource" {
		t.Fatal("explicit metadata URI not selected")
	}
	f = newMetadataFixture()
	if _, err := NewOAuthDiscoverer(f).Discover(context.Background(), discoveryTarget(), DiscoveryOptions{AuthenticateHeaders: []string{`Bearer resource_metadata="https://metadata.example.invalid/missing"`}}); err == nil {
		t.Fatal("missing explicit metadata accepted")
	}
	if len(f.calls) != 1 {
		t.Fatal("explicit metadata failure fell back to well-known")
	}
}

func TestOAuthDiscoveryCandidateOrder(t *testing.T) {
	f := newMetadataFixture()
	delete(f.documents, "https://resource.example.invalid/.well-known/oauth-protected-resource/mcp")
	f.documents["https://resource.example.invalid/.well-known/oauth-protected-resource"] = `{"resource":"https://resource.example.invalid/","authorization_servers":["https://auth.example.invalid/tenant"]}`
	as := f.documents["https://auth.example.invalid/.well-known/oauth-authorization-server/tenant"]
	delete(f.documents, "https://auth.example.invalid/.well-known/oauth-authorization-server/tenant")
	f.documents["https://auth.example.invalid/tenant/.well-known/openid-configuration"] = as
	_, err := NewOAuthDiscoverer(f).Discover(context.Background(), discoveryTarget(), DiscoveryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"https://resource.example.invalid/.well-known/oauth-protected-resource/mcp", "https://resource.example.invalid/.well-known/oauth-protected-resource", "https://auth.example.invalid/.well-known/oauth-authorization-server/tenant", "https://auth.example.invalid/.well-known/openid-configuration/tenant", "https://auth.example.invalid/tenant/.well-known/openid-configuration"}
	if !reflect.DeepEqual(f.calls, expected) {
		t.Fatalf("calls = %v", f.calls)
	}
	candidates, err := protectedResourceCandidates("https://resource.example.invalid/", "")
	if err != nil || len(candidates) != 1 {
		t.Fatalf("root candidates = %v, %v", candidates, err)
	}
	candidates, err = authorizationServerCandidates("https://auth.example.invalid")
	if err != nil || len(candidates) != 2 {
		t.Fatalf("root AS candidates = %v, %v", candidates, err)
	}
}

func TestOAuthDiscoveryRejectsMetadata(t *testing.T) {
	tests := []struct {
		name, field string
		value       any
		class       ErrorClass
	}{
		{"issuer mismatch", "issuer", "https://other.example.invalid", ErrorIssuerMismatch},
		{"missing pkce", "code_challenge_methods_supported", nil, ErrorPKCEUnsupported},
		{"unsupported pkce", "code_challenge_methods_supported", []string{"plain"}, ErrorPKCEUnsupported},
		{"response type", "response_types_supported", []string{"token"}, ErrorDiscoveryFailed},
		{"grant type", "grant_types_supported", []string{"refresh_token"}, ErrorDiscoveryFailed},
		{"missing endpoint", "token_endpoint", "", ErrorDiscoveryFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newMetadataFixture()
			uri := "https://auth.example.invalid/.well-known/oauth-authorization-server/tenant"
			var fields map[string]any
			if err := json.Unmarshal([]byte(f.documents[uri]), &fields); err != nil {
				t.Fatal(err)
			}
			if tt.value == nil {
				delete(fields, tt.field)
			} else {
				fields[tt.field] = tt.value
			}
			raw, _ := json.Marshal(fields)
			f.documents[uri] = string(raw)
			_, err := NewOAuthDiscoverer(f).Discover(context.Background(), discoveryTarget(), DiscoveryOptions{})
			classified, ok := errors.AsType[*DiscoveryError](err)
			if !ok || classified.Class != tt.class {
				t.Fatalf("error = %v, want %s", err, tt.class)
			}
		})
	}
	for _, resource := range []string{"https://other.example.invalid/mcp", "https://resource.example.invalid/other", "https://resource.example.invalid/a%2fb"} {
		f := newMetadataFixture()
		raw, _ := json.Marshal(ProtectedResourceMetadata{Resource: resource, AuthorizationServers: []string{"https://auth.example.invalid/tenant"}})
		f.documents["https://resource.example.invalid/.well-known/oauth-protected-resource/mcp"] = string(raw)
		_, err := NewOAuthDiscoverer(f).Discover(context.Background(), discoveryTarget(), DiscoveryOptions{})
		classified, ok := errors.AsType[*DiscoveryError](err)
		if !ok || classified.Class != ErrorResourceMismatch || len(f.calls) != 1 {
			t.Fatalf("resource failure = %v, requests %v", err, f.calls)
		}
	}
}

func TestOAuthDiscoveryFailsWithoutFallback(t *testing.T) {
	for _, failure := range []error{&OAuthHTTPError{StatusCode: 403}, errors.New("invalid JSON")} {
		f := newMetadataFixture()
		f.failures["https://resource.example.invalid/.well-known/oauth-protected-resource/mcp"] = failure
		if _, err := NewOAuthDiscoverer(f).Discover(context.Background(), discoveryTarget(), DiscoveryOptions{}); err == nil || len(f.calls) != 1 {
			t.Fatalf("err = %v, calls = %v", err, f.calls)
		}
	}
	for _, mode := range []string{"static", "none", "stdio"} {
		f := newMetadataFixture()
		target := discoveryTarget()
		switch mode {
		case "static":
			target.Config.Headers = map[string]string{"authorization": "sample"}
		case "none":
			target.Config.Auth = config.MCPAuthNone
		case "stdio":
			target.Config.Command = "sample"
		}
		if _, err := NewOAuthDiscoverer(f).Discover(context.Background(), target, DiscoveryOptions{}); err == nil || len(f.calls) != 0 {
			t.Fatal("unsupported auth mode performed discovery")
		}
	}
	f := newMetadataFixture()
	_, err := NewOAuthDiscoverer(f).Discover(context.Background(), discoveryTarget(), DiscoveryOptions{AuthenticateHeaders: []string{`Bearer scope="unterminated`}})
	if classified, ok := errors.AsType[*DiscoveryError](err); !ok || classified.Class != ErrorProtocol || len(f.calls) != 0 {
		t.Fatal("malformed challenge treated as absent")
	}
}

func TestOAuthDiscoveryIssuerSelectionAndInvalidation(t *testing.T) {
	f := newMetadataFixture()
	target := discoveryTarget()
	target.Config.OAuth = &config.MCPOAuthConfig{Issuer: "https://different.example.invalid"}
	if _, err := NewOAuthDiscoverer(f).Discover(context.Background(), target, DiscoveryOptions{}); err == nil || len(f.calls) != 1 {
		t.Fatal("undeclared configured issuer accepted")
	}
	f = newMetadataFixture()
	d := NewOAuthDiscoverer(f)
	target = discoveryTarget()
	if _, err := d.Discover(context.Background(), target, DiscoveryOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Discover(context.Background(), target, DiscoveryOptions{AuthenticateHeaders: []string{`Bearer error="invalid_token"`}}); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 4 {
		t.Fatal("error challenge reused metadata cache")
	}
	candidates, err := authorizationServerCandidates("https://auth.example.invalid/tenant/")
	if err != nil || candidates[0] != "https://auth.example.invalid/.well-known/oauth-authorization-server/tenant/" || candidates[2] != "https://auth.example.invalid/tenant/.well-known/openid-configuration" {
		t.Fatalf("trailing slash discovery candidates = %v, %v", candidates, err)
	}
}

func TestOAuthDiscoveryRejectsDuplicateAndNullFields(t *testing.T) {
	for _, raw := range []string{
		`{"issuer":"https://auth.example.invalid","issuer":"https://other.example.invalid"}`,
		`{"grant_types_supported":null}`,
		`{"token_endpoint_auth_methods_supported":null}`,
		`{"code_challenge_methods_supported":7}`,
	} {
		var result AuthorizationServerMetadata
		if json.Unmarshal([]byte(raw), &result) == nil {
			t.Fatalf("invalid metadata accepted: %s", raw)
		}
	}
}

func TestOAuthDiscoveryMetadataRetryBound(t *testing.T) {
	f := newMetadataFixture()
	endpoint := "https://resource.example.invalid/.well-known/oauth-protected-resource/mcp"
	f.failures[endpoint] = &OAuthHTTPError{StatusCode: http.StatusServiceUnavailable}
	_, err := NewOAuthDiscoverer(f).Discover(context.Background(), discoveryTarget(), DiscoveryOptions{})
	if err == nil || len(f.calls) != 2 {
		t.Fatalf("retry result %v, calls %v", err, f.calls)
	}
	f.calls = nil
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = NewOAuthDiscoverer(f).Discover(ctx, discoveryTarget(), DiscoveryOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled retry = %v", err)
	}
	if len(f.calls) > 1 {
		t.Fatal("cancelled metadata retried")
	}
}
