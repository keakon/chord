package mcp

import (
	"context"
	"fmt"
)

// fixedOAuthMetadata isolates transport and credential tests from public DNS.
// Network-policy behavior is covered by the dedicated HTTP client tests.
type fixedOAuthMetadata struct {
	resource string
	issuer   string
}

func (f *fixedOAuthMetadata) ValidateEndpoint(context.Context, string) error { return nil }
func (f *fixedOAuthMetadata) GetJSON(_ context.Context, _ string, out any) error {
	switch value := out.(type) {
	case *ProtectedResourceMetadata:
		*value = ProtectedResourceMetadata{Resource: f.resource, AuthorizationServers: []string{f.issuer}, ScopesSupported: []string{"files:read"}}
	case *AuthorizationServerMetadata:
		*value = AuthorizationServerMetadata{Issuer: f.issuer, AuthorizationEndpoint: f.issuer + "/authorize", TokenEndpoint: "https://example.com/token", ResponseTypesSupported: []string{"code"}, GrantTypesSupported: []string{"authorization_code", "refresh_token"}, TokenEndpointAuthMethodsSupported: []string{"none"}, CodeChallengeMethodsSupported: []string{"S256"}}
	default:
		return fmt.Errorf("unexpected metadata document type")
	}
	return nil
}
