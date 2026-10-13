package mcp

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/keakon/chord/internal/config"
)

const OAuthClientMetadataURL = "https://keakon.github.io/chord/oauth/client-metadata.json"

type OAuthRegistrationClient interface {
	PostJSON(context.Context, string, any, any) error
}

type OAuthRegistrar struct{ client OAuthRegistrationClient }

func NewOAuthRegistrar(client OAuthRegistrationClient) *OAuthRegistrar {
	return &OAuthRegistrar{client: client}
}

type dynamicRegistrationRequest struct {
	ClientName              string   `json:"client_name"`
	ApplicationType         string   `json:"application_type"`
	RedirectURIs            []string `json:"redirect_uris"`
	ResponseTypes           []string `json:"response_types"`
	GrantTypes              []string `json:"grant_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

type dynamicRegistrationResponse struct {
	ClientID                string   `json:"client_id"`
	ClientSecret            string   `json:"client_secret"`
	ClientSecretExpiresAt   *int64   `json:"client_secret_expires_at"`
	RedirectURIs            []string `json:"redirect_uris"`
	ResponseTypes           []string `json:"response_types"`
	GrantTypes              []string `json:"grant_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

// Register selects exactly one mechanism. The caller must bind the callback
// listener first and durably save DCR results before beginning authorization.
// No failure falls through to another registration mechanism.
func (r *OAuthRegistrar) Register(ctx context.Context, target Target, metadata OAuthDiscovery) (CredentialRegistration, error) {
	if err := ctx.Err(); err != nil {
		return CredentialRegistration{}, err
	}
	profile := profileForTarget(target)
	as := metadata.AuthorizationServer
	fail := func(message string) (CredentialRegistration, error) {
		return CredentialRegistration{}, discoveryError(ErrorRegistrationFailed, message)
	}
	if metadata.Issuer == "" || metadata.Issuer != as.Issuer {
		return fail("registration requires validated issuer metadata")
	}
	registration := CredentialRegistration{Issuer: metadata.Issuer, RedirectURI: profile.RedirectURI, TokenAuthMethod: profile.TokenAuthMethod, GrantTypes: []string{"authorization_code"}, ResponseTypes: []string{"code"}}
	if slices.Contains(as.GrantTypesSupported, "refresh_token") {
		registration.GrantTypes = append(registration.GrantTypes, "refresh_token")
	}
	if profile.ConfiguredClientID != "" {
		if profile.IssuerHint != metadata.Issuer {
			return fail("pre-registered client issuer does not match discovery")
		}
		if !slices.Contains(as.TokenEndpointAuthMethodsSupported, profile.TokenAuthMethod) {
			return fail("authorization server does not support the configured client authentication method")
		}
		registration.ClientID = profile.ConfiguredClientID
		return registration, nil
	}
	if !slices.Contains(as.TokenEndpointAuthMethodsSupported, config.MCPOAuthAuthMethodNone) {
		return fail("authorization server does not declare support for public clients; configure a pre-registered client")
	}
	registration.TokenAuthMethod = config.MCPOAuthAuthMethodNone
	if as.ClientIDMetadataDocumentSupported {
		expected := "http://127.0.0.1:41937/oauth/callback"
		if profile.RedirectURI != expected {
			return fail("client metadata registration requires callback port 41937; configure that port or a pre-registered client")
		}
		registration.ClientID = OAuthClientMetadataURL
		return registration, nil
	}
	if as.RegistrationEndpoint == "" {
		return fail("authorization server offers no automatic registration; configure client_id and issuer")
	}
	if r.client == nil {
		return fail("OAuth registration client is unavailable")
	}
	request := dynamicRegistrationRequest{ClientName: "Chord", ApplicationType: "native", RedirectURIs: []string{profile.RedirectURI}, ResponseTypes: registration.ResponseTypes, GrantTypes: registration.GrantTypes, TokenEndpointAuthMethod: config.MCPOAuthAuthMethodNone}
	var response dynamicRegistrationResponse
	if err := r.client.PostJSON(ctx, as.RegistrationEndpoint, request, &response); err != nil {
		return CredentialRegistration{}, err
	}
	if strings.TrimSpace(response.ClientID) == "" {
		return fail("registration response is missing client_id")
	}
	// RFC 7591 defaults an omitted method to client_secret_basic; it cannot be
	// interpreted as the public-client method requested by Chord.
	if response.TokenEndpointAuthMethod != config.MCPOAuthAuthMethodNone {
		return fail("registration response changed the public-client authentication method")
	}
	if len(response.RedirectURIs) != 1 || response.RedirectURIs[0] != profile.RedirectURI {
		return fail("registration response changed the callback URI")
	}
	if response.ResponseTypes == nil {
		response.ResponseTypes = []string{"code"}
	}
	if response.GrantTypes == nil {
		response.GrantTypes = []string{"authorization_code"}
	}
	if !sameStringSet(response.ResponseTypes, request.ResponseTypes) || !sameStringSet(response.GrantTypes, request.GrantTypes) {
		return fail("registration response changed the requested grant or response types")
	}
	if response.ClientSecretExpiresAt != nil {
		expiration := *response.ClientSecretExpiresAt
		if expiration < 0 || expiration > 253402300799 {
			return fail("registration response has invalid secret expiry")
		}
		if expiration > 0 {
			registration.SecretExpiresAt = time.Unix(expiration, 0).UTC()
		}
	}
	registration.ClientID = response.ClientID
	registration.ClientSecret = response.ClientSecret
	return registration, nil
}

func sameStringSet(a, b []string) bool {
	a = slices.Clone(a)
	b = slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(slices.Compact(a), slices.Compact(b))
}

func (m *dynamicRegistrationResponse) UnmarshalJSON(raw []byte) error {
	type registration dynamicRegistrationResponse
	var value registration
	if err := decodeOAuthMetadata(raw, []string{"client_id", "client_secret", "client_secret_expires_at", "redirect_uris", "response_types", "grant_types", "token_endpoint_auth_method"}, &value); err != nil {
		return err
	}
	*m = dynamicRegistrationResponse(value)
	return nil
}
