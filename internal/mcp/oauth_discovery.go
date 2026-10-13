package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/keakon/chord/internal/config"
)

// OAuthMetadataClient separates read-only discovery from registration and token
// exchange. Production callers supply the restricted OAuthHTTPClient.
type OAuthMetadataClient interface {
	GetJSON(context.Context, string, any) error
	ValidateEndpoint(context.Context, string) error
}

type ProtectedResourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported,omitempty"`
}

type AuthorizationServerMetadata struct {
	Issuer                                     string   `json:"issuer"`
	AuthorizationEndpoint                      string   `json:"authorization_endpoint"`
	TokenEndpoint                              string   `json:"token_endpoint"`
	RegistrationEndpoint                       string   `json:"registration_endpoint,omitempty"`
	ResponseTypesSupported                     []string `json:"response_types_supported"`
	GrantTypesSupported                        []string `json:"grant_types_supported,omitempty"`
	TokenEndpointAuthMethodsSupported          []string `json:"token_endpoint_auth_methods_supported,omitempty"`
	CodeChallengeMethodsSupported              []string `json:"code_challenge_methods_supported"`
	ScopesSupported                            []string `json:"scopes_supported,omitempty"`
	ClientIDMetadataDocumentSupported          bool     `json:"client_id_metadata_document_supported,omitempty"`
	AuthorizationResponseIssParameterSupported bool     `json:"authorization_response_iss_parameter_supported,omitempty"`
}

// OAuthDiscovery contains validated protocol metadata, never tokens. It must
// remain inside the authentication implementation rather than diagnostic DTOs.
type OAuthDiscovery struct {
	validUntil          time.Time
	ProtectedResource   ProtectedResourceMetadata
	AuthorizationServer AuthorizationServerMetadata
	Resource            string
	Issuer              string
	Scopes              []string
}

type DiscoveryOptions struct {
	BearerChallenges    []BearerChallenge
	AuthenticateHeaders []string
	VerifiedIssuer      string
	Force               bool
}

type discoveryCacheKey struct{ target, profile string }
type discoveryCacheEntry struct {
	generation                                                        uint64
	endpoint, issuerHint, verifiedIssuer, metadataURI, challengeScope string
	expires                                                           time.Time
	result                                                            OAuthDiscovery
}

type OAuthDiscoverer struct {
	client    OAuthMetadataClient
	challenge func(context.Context, Target) ([]BearerChallenge, error)
	mu        sync.Mutex
	cache     map[discoveryCacheKey]discoveryCacheEntry
	now       func() time.Time
}

func NewOAuthDiscoverer(client OAuthMetadataClient) *OAuthDiscoverer {
	return &OAuthDiscoverer{client: client, cache: make(map[discoveryCacheKey]discoveryCacheEntry), now: time.Now}
}

// DiscoveryError carries a stable class and a safe summary only.
type DiscoveryError struct {
	Class   ErrorClass
	Summary string
}

func (e *DiscoveryError) Error() string { return e.Summary }
func discoveryError(class ErrorClass, summary string) error {
	return &DiscoveryError{Class: class, Summary: summary}
}

// Invalidate removes metadata after rejected or changed identity evidence.
func (d *OAuthDiscoverer) Invalidate(target Target) {
	d.mu.Lock()
	delete(d.cache, discoveryCacheKey{target.TargetID, target.ProfileID})
	d.mu.Unlock()
}

// Discover performs no registration, token exchange, or credential writes.
func (d *OAuthDiscoverer) Discover(ctx context.Context, target Target, opts DiscoveryOptions) (result OAuthDiscovery, err error) {
	started := time.Now()
	defer func() {
		traceFromContext(ctx).record(StageDiscovery, started, err)
		if err != nil {
			d.Invalidate(target)
		}
	}()
	if target.Config.Command != "" || target.Config.URL == "" || target.Config.Auth == config.MCPAuthNone {
		return result, discoveryError(ErrorDiscoveryFailed, "managed OAuth is disabled for this server")
	}
	for key := range target.Config.Headers {
		if strings.EqualFold(key, "Authorization") {
			return result, discoveryError(ErrorDiscoveryFailed, "static Authorization does not use managed OAuth discovery")
		}
	}
	if d.client == nil {
		return result, discoveryError(ErrorDiscoveryFailed, "OAuth metadata client is unavailable")
	}
	// Without a new challenge, reuse the validated discovery for this binding.
	// Expiry or a new process obtains the resource's current challenge again;
	// custom metadata addresses are never persisted as trusted endpoints.
	if len(opts.AuthenticateHeaders) == 0 && len(opts.BearerChallenges) == 0 {
		hint := ""
		if target.Config.OAuth != nil {
			hint = target.Config.OAuth.Issuer
		}
		d.mu.Lock()
		cached, ok := d.cache[discoveryCacheKey{target.TargetID, target.ProfileID}]
		d.mu.Unlock()
		if !opts.Force && ok && target.TargetID != "" && cached.generation == target.ConfigGeneration && cached.endpoint == target.Config.URL && cached.issuerHint == hint && (opts.VerifiedIssuer == "" || cached.result.Issuer == opts.VerifiedIssuer) && d.now().Before(cached.expires) {
			return cloneDiscovery(cached.result), nil
		}
		if d.challenge != nil {
			challengeCtx, cancel := context.WithTimeout(ctx, connectAttemptTimeout)
			challenges, challengeErr := d.challenge(challengeCtx, target)
			cancel()
			if challengeErr != nil {
				return result, challengeErr
			}
			opts.BearerChallenges = challenges
		}
	}
	challenges, err := ParseBearerChallenges(opts.AuthenticateHeaders)
	if err != nil {
		return result, discoveryError(ErrorProtocol, "invalid authentication challenge")
	}
	challenges = append(challenges, opts.BearerChallenges...)
	metadataURI, scope := "", ""
	hasErrorChallenge := false
	for _, challenge := range challenges {
		hasErrorChallenge = hasErrorChallenge || challenge.Error != ""
		if challenge.ResourceMetadata != "" {
			if metadataURI != "" && metadataURI != challenge.ResourceMetadata {
				return result, discoveryError(ErrorProtocol, "conflicting resource metadata challenges")
			}
			metadataURI = challenge.ResourceMetadata
		}
		if challenge.Scope != "" {
			if scope != "" {
				scope += " "
			}
			scope += challenge.Scope
		}
	}
	issuerHint := ""
	if target.Config.OAuth != nil {
		issuerHint = target.Config.OAuth.Issuer
	}
	key := discoveryCacheKey{target.TargetID, target.ProfileID}
	d.mu.Lock()
	cached, ok := d.cache[key]
	d.mu.Unlock()
	if !opts.Force && !hasErrorChallenge && ok && target.TargetID != "" && cached.generation == target.ConfigGeneration && cached.endpoint == target.Config.URL && cached.issuerHint == issuerHint && cached.verifiedIssuer == opts.VerifiedIssuer && cached.metadataURI == metadataURI && cached.challengeScope == scope && d.now().Before(cached.expires) {
		return cloneDiscovery(cached.result), nil
	}
	client := d.client
	if restricted, ok := client.(*OAuthHTTPClient); ok {
		client = restricted.ForResource(target.Config.URL)
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	candidates, err := protectedResourceCandidates(target.Config.URL, metadataURI)
	if err != nil {
		return result, err
	}
	if err = d.fetchCandidates(ctx, client, candidates, &result.ProtectedResource); err != nil {
		return result, err
	}
	prm := result.ProtectedResource
	if ValidateResourceBinding(target.Config.URL, prm.Resource) != nil {
		return result, discoveryError(ErrorResourceMismatch, "protected resource does not match the MCP endpoint")
	}
	if len(prm.AuthorizationServers) == 0 || len(prm.AuthorizationServers) > 8 {
		return result, discoveryError(ErrorDiscoveryFailed, "protected resource must declare between one and eight authorization servers")
	}
	issuer := issuerHint
	if issuer != "" && !slices.Contains(prm.AuthorizationServers, issuer) {
		return result, discoveryError(ErrorIssuerMismatch, "configured issuer is not declared by the protected resource")
	}
	if issuer == "" && slices.Contains(prm.AuthorizationServers, opts.VerifiedIssuer) {
		issuer = opts.VerifiedIssuer
	}
	if issuer == "" {
		issuer = prm.AuthorizationServers[0]
	}
	for _, declared := range prm.AuthorizationServers {
		if err = validateIssuerURL(declared); err != nil {
			return result, err
		}
	}
	if err = client.ValidateEndpoint(ctx, issuer); err != nil {
		return result, err
	}
	candidates, err = authorizationServerCandidates(issuer)
	if err != nil {
		return result, err
	}
	if err = d.fetchCandidates(ctx, client, candidates, &result.AuthorizationServer); err != nil {
		return result, err
	}
	as := &result.AuthorizationServer
	if as.Issuer != issuer {
		return result, discoveryError(ErrorIssuerMismatch, "authorization server issuer does not match discovery")
	}
	if !slices.Contains(as.ResponseTypesSupported, "code") {
		return result, discoveryError(ErrorDiscoveryFailed, "authorization server does not support authorization code responses")
	}
	if as.GrantTypesSupported == nil {
		as.GrantTypesSupported = []string{"authorization_code", "implicit"}
	}
	if !slices.Contains(as.GrantTypesSupported, "authorization_code") {
		return result, discoveryError(ErrorDiscoveryFailed, "authorization server does not support the authorization code grant")
	}
	if as.TokenEndpointAuthMethodsSupported == nil {
		as.TokenEndpointAuthMethodsSupported = []string{config.MCPOAuthAuthMethodClientSecretBasic}
	}
	if !slices.Contains(as.CodeChallengeMethodsSupported, "S256") {
		return result, discoveryError(ErrorPKCEUnsupported, "authorization server must explicitly support PKCE S256")
	}
	if as.AuthorizationEndpoint == "" || as.TokenEndpoint == "" {
		return result, discoveryError(ErrorDiscoveryFailed, "authorization server endpoints are missing")
	}
	for _, endpoint := range []string{as.AuthorizationEndpoint, as.TokenEndpoint, as.RegistrationEndpoint} {
		if endpoint != "" {
			if err = client.ValidateEndpoint(ctx, endpoint); err != nil {
				return result, err
			}
		}
	}
	result.Resource, err = CanonicalEndpoint(prm.Resource)
	if err != nil {
		return result, discoveryError(ErrorResourceMismatch, "invalid protected resource")
	}
	result.Issuer = issuer
	requested := prm.ScopesSupported
	if scope != "" {
		if strings.IndexFunc(scope, func(r rune) bool { return unicode.IsControl(r) || (unicode.IsSpace(r) && r != ' ') }) >= 0 {
			return OAuthDiscovery{}, discoveryError(ErrorProtocol, "invalid challenge scope")
		}
		requested = strings.Fields(scope)
	}
	result.Scopes = slices.Clone(requested)
	if target.Config.OAuth != nil {
		result.Scopes = append(result.Scopes, target.Config.OAuth.Scopes...)
	}
	for _, s := range result.Scopes {
		if s == "" || strings.IndexFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
			return OAuthDiscovery{}, discoveryError(ErrorProtocol, "invalid scope in discovery metadata")
		}
	}
	slices.Sort(result.Scopes)
	result.Scopes = slices.Compact(result.Scopes)
	result.validUntil = d.now().Add(5 * time.Minute)
	if target.TargetID != "" {
		d.mu.Lock()
		if len(d.cache) >= 128 {
			for k, entry := range d.cache {
				if !d.now().Before(entry.expires) {
					delete(d.cache, k)
				}
			}
		}
		if len(d.cache) < 128 {
			d.cache[key] = discoveryCacheEntry{generation: target.ConfigGeneration, endpoint: target.Config.URL, issuerHint: issuerHint, verifiedIssuer: opts.VerifiedIssuer, metadataURI: metadataURI, challengeScope: scope, expires: result.validUntil, result: cloneDiscovery(result)}
		}
		d.mu.Unlock()
	}
	return result, nil
}

func protectedResourceCandidates(endpoint, metadataURI string) ([]string, error) {
	if metadataURI != "" {
		return []string{metadataURI}, nil
	}
	canonical, err := CanonicalEndpoint(endpoint)
	if err != nil {
		return nil, discoveryError(ErrorResourceMismatch, "invalid MCP endpoint")
	}
	u, _ := url.Parse(canonical)
	base := u.Scheme + "://" + u.Host + "/.well-known/oauth-protected-resource"
	if u.EscapedPath() == "/" {
		return []string{base}, nil
	}
	return []string{base + u.EscapedPath(), base}, nil
}

func validateIssuerURL(issuer string) error {
	u, err := url.Parse(issuer)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.ForceQuery || strings.Contains(issuer, "#") || u.Opaque != "" {
		return discoveryError(ErrorIssuerMismatch, "issuer must be an absolute HTTPS URL without userinfo, query, or fragment")
	}
	return nil
}

func authorizationServerCandidates(issuer string) ([]string, error) {
	if err := validateIssuerURL(issuer); err != nil {
		return nil, err
	}
	u, _ := url.Parse(issuer)
	origin := u.Scheme + "://" + u.Host
	path := u.EscapedPath()
	if path == "" || path == "/" {
		return []string{origin + "/.well-known/oauth-authorization-server", origin + "/.well-known/openid-configuration"}, nil
	}
	return []string{origin + "/.well-known/oauth-authorization-server" + path, origin + "/.well-known/openid-configuration" + path, origin + strings.TrimSuffix(path, "/") + "/.well-known/openid-configuration"}, nil
}

func (d *OAuthDiscoverer) fetchCandidates(ctx context.Context, client OAuthMetadataClient, candidates []string, out any) error {
	for _, endpoint := range candidates {
		err := d.getMetadata(ctx, client, endpoint, out)
		if err == nil {
			return nil
		}
		if e, ok := errors.AsType[*OAuthHTTPError](err); ok && (e.StatusCode == http.StatusNotFound || e.StatusCode == http.StatusMethodNotAllowed) {
			continue
		}
		return err
	}
	return discoveryError(ErrorDiscoveryFailed, "OAuth metadata was not found")
}

func (d *OAuthDiscoverer) getMetadata(ctx context.Context, client OAuthMetadataClient, endpoint string, out any) error {
	err := client.GetJSON(ctx, endpoint, out)
	if err == nil {
		return nil
	}
	e, ok := errors.AsType[*OAuthHTTPError](err)
	if !ok || !(e.ErrorClass == ErrorNetwork || e.StatusCode == 429 || e.StatusCode == 502 || e.StatusCode == 503 || e.StatusCode == 504) {
		return err
	}
	delay := 250 * time.Millisecond
	if e.RetryAfter > delay {
		delay = min(e.RetryAfter, 2*time.Second)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}
	return client.GetJSON(ctx, endpoint, out)
}

func cloneDiscovery(d OAuthDiscovery) OAuthDiscovery {
	d.ProtectedResource.AuthorizationServers = slices.Clone(d.ProtectedResource.AuthorizationServers)
	d.ProtectedResource.ScopesSupported = slices.Clone(d.ProtectedResource.ScopesSupported)
	d.AuthorizationServer.ResponseTypesSupported = slices.Clone(d.AuthorizationServer.ResponseTypesSupported)
	d.AuthorizationServer.GrantTypesSupported = slices.Clone(d.AuthorizationServer.GrantTypesSupported)
	d.AuthorizationServer.TokenEndpointAuthMethodsSupported = slices.Clone(d.AuthorizationServer.TokenEndpointAuthMethodsSupported)
	d.AuthorizationServer.CodeChallengeMethodsSupported = slices.Clone(d.AuthorizationServer.CodeChallengeMethodsSupported)
	d.AuthorizationServer.ScopesSupported = slices.Clone(d.AuthorizationServer.ScopesSupported)
	d.Scopes = slices.Clone(d.Scopes)
	return d
}

// decodeOAuthMetadata preserves defaults only for absent fields, not explicit
// nulls, and rejects duplicate keys before the standard decoder can overwrite.
func decodeOAuthMetadata(raw []byte, known []string, out any) error {
	if rejectDuplicateJSON(raw) != nil {
		return discoveryError(ErrorProtocol, "invalid OAuth metadata JSON")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return discoveryError(ErrorProtocol, "OAuth metadata must be an object")
	}
	for _, key := range known {
		if value, ok := fields[key]; ok && strings.TrimSpace(string(value)) == "null" {
			return discoveryError(ErrorProtocol, "OAuth metadata contains a null protocol field")
		}
	}
	if json.Unmarshal(raw, out) != nil {
		return discoveryError(ErrorProtocol, "OAuth metadata contains an invalid protocol field")
	}
	return nil
}

func (m *ProtectedResourceMetadata) UnmarshalJSON(raw []byte) error {
	type metadata ProtectedResourceMetadata
	var value metadata
	if err := decodeOAuthMetadata(raw, []string{"resource", "authorization_servers", "scopes_supported"}, &value); err != nil {
		return err
	}
	*m = ProtectedResourceMetadata(value)
	return nil
}

func (m *AuthorizationServerMetadata) UnmarshalJSON(raw []byte) error {
	type metadata AuthorizationServerMetadata
	var value metadata
	if err := decodeOAuthMetadata(raw, []string{"issuer", "authorization_endpoint", "token_endpoint", "registration_endpoint", "response_types_supported", "grant_types_supported", "token_endpoint_auth_methods_supported", "code_challenge_methods_supported", "scopes_supported", "client_id_metadata_document_supported", "authorization_response_iss_parameter_supported"}, &value); err != nil {
		return err
	}
	*m = AuthorizationServerMetadata(value)
	return nil
}

func (d *OAuthDiscoverer) valid(result OAuthDiscovery) bool { return d.now().Before(result.validUntil) }
