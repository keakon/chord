package config

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	OpenAIOAuthIssuer       = "https://auth.openai.com"
	OpenAIOAuthAuthorizeURL = OpenAIOAuthIssuer + "/oauth/authorize"
	OpenAIOAuthTokenURL     = OpenAIOAuthIssuer + "/oauth/token"
	OpenAIOAuthClientID     = "app_EMoamEEZ73f0CkXaXp7hrann"
	OpenAICodexResponsesURL = "https://chatgpt.com/backend-api/codex/responses"
)

// OAuthCredentialStatus indicates the usability status of an OAuth credential.
// Empty string means the credential is normal/usable.
// "deactivated" means the account has been permanently disabled.
// "expired" means the refresh token has expired and cannot be refreshed.
type OAuthCredentialStatus string

const (
	OAuthStatusNormal      OAuthCredentialStatus = ""
	OAuthStatusDeactivated OAuthCredentialStatus = "deactivated"
	OAuthStatusExpired     OAuthCredentialStatus = "expired"
)

// IsValid returns true if the credential is usable (not deactivated or expired).
func (s OAuthCredentialStatus) IsValid() bool {
	return s == OAuthStatusNormal
}

// OAuthCredential stores OAuth token information.
// expires is a millisecond-precision Unix timestamp.
type OAuthCredential struct {
	Refresh   string                `yaml:"refresh"`
	Access    string                `yaml:"access"`
	Expires   int64                 `yaml:"expires"`
	AccountID string                `yaml:"account_id,omitempty"`
	Email     string                `yaml:"email,omitempty"`
	Status    OAuthCredentialStatus `yaml:"status,omitempty"`
}

type oauthTokenClaims struct {
	ChatGPTAccountID string `json:"chatgpt_account_id,omitempty"`
	Email            string `json:"email,omitempty"`
	Organizations    []struct {
		ID string `json:"id"`
	} `json:"organizations,omitempty"`
	OpenAIAuth *struct {
		ChatGPTAccountID string `json:"chatgpt_account_id,omitempty"`
	} `json:"https://api.openai.com/auth,omitempty"`
	OpenAIProfile *struct {
		Email string `json:"email,omitempty"`
	} `json:"https://api.openai.com/profile,omitempty"`
}

func extractOAuthAccountIDFromClaims(claims oauthTokenClaims) string {
	if claims.ChatGPTAccountID != "" {
		return claims.ChatGPTAccountID
	}
	if claims.OpenAIAuth != nil && claims.OpenAIAuth.ChatGPTAccountID != "" {
		return claims.OpenAIAuth.ChatGPTAccountID
	}
	if len(claims.Organizations) > 0 {
		return claims.Organizations[0].ID
	}
	return ""
}

// extractOAuthClaims decodes the payload of an OpenAI OAuth JWT without
// verifying the signature. Returns zero-value claims on any parse error.
func extractOAuthClaims(token string) oauthTokenClaims {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[1] == "" {
		return oauthTokenClaims{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return oauthTokenClaims{}
	}
	var claims oauthTokenClaims
	_ = json.Unmarshal(payload, &claims)
	return claims
}

// ExtractOAuthAccountIDFromToken extracts the ChatGPT account/workspace ID from
// an OpenAI OAuth JWT without verifying the signature. It is best-effort and
// returns an empty string when the token is not a JWT or does not carry the
// expected claims.
func ExtractOAuthAccountIDFromToken(token string) string {
	return extractOAuthAccountIDFromClaims(extractOAuthClaims(token))
}

// ExtractOAuthEmailFromToken extracts the email claim from an OpenAI OAuth JWT
// without verifying the signature. Returns an empty string when not present.
// Checks the top-level "email" claim first, then the nested
// "https://api.openai.com/profile.email" claim (used by access tokens).
func ExtractOAuthEmailFromToken(token string) string {
	claims := extractOAuthClaims(token)
	if claims.Email != "" {
		return claims.Email
	}
	if claims.OpenAIProfile != nil && claims.OpenAIProfile.Email != "" {
		return claims.OpenAIProfile.Email
	}
	return ""
}

// ProviderCredential is a union type: either an API key or an OAuth token.
// In YAML, a scalar string (including $ENV_VAR) maps to APIKey; a mapping maps to OAuth.
// ExplicitEmpty is true when the YAML value is a literal empty string (not an unset $ENV_VAR).
type ProviderCredential struct {
	APIKey        string
	OAuth         *OAuthCredential
	ExplicitEmpty bool
}

// MarshalYAML implements union serialization.
// OAuth credentials are serialized as a mapping; API keys as a scalar string.
func (c ProviderCredential) MarshalYAML() (interface{}, error) {
	if c.OAuth != nil {
		return c.OAuth, nil
	}
	return c.APIKey, nil
}

// UnmarshalYAML implements union deserialization.
func (c *ProviderCredential) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		raw := value.Value
		if len(raw) > 0 && raw[0] == '$' {
			c.APIKey = os.ExpandEnv(raw)
		} else {
			c.APIKey = raw
			c.ExplicitEmpty = (raw == "")
		}
		return nil
	case yaml.MappingNode:
		var o OAuthCredential
		if err := value.Decode(&o); err != nil {
			return err
		}
		if o.AccountID == "" && o.Access != "" {
			o.AccountID = ExtractOAuthAccountIDFromToken(o.Access)
		}
		c.OAuth = &o
		return nil
	default:
		return fmt.Errorf("unsupported credential type in auth config")
	}
}

// AuthConfig maps provider names to their list of credentials.
type AuthConfig map[string][]ProviderCredential

// ExtractAPIKeys extracts all API keys from a credential list.
// OAuth credentials' access tokens are included as API keys.
// Explicit empty strings are included as valid keys.
func ExtractAPIKeys(creds []ProviderCredential) []string {
	keys := make([]string, 0, len(creds))
	for _, c := range creds {
		// OAuth branch: extract access token as API key
		if c.OAuth != nil && c.OAuth.Access != "" {
			keys = append(keys, c.OAuth.Access)
			continue
		}
		if c.APIKey != "" || c.ExplicitEmpty {
			keys = append(keys, c.APIKey)
		}
	}
	return keys
}

// LoadAuthConfig loads authentication configuration from a YAML file.
// Credentials with an empty APIKey are filtered out unless ExplicitEmpty is true
// (i.e., the YAML value was a literal "" rather than an unset $ENV_VAR).
func LoadAuthConfig(path string) (AuthConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(AuthConfig), nil
		}
		return nil, err
	}

	var raw map[string][]ProviderCredential
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	return normalizeAuthConfig(raw), nil
}

// IsExpired reports whether the OAuth credential has expired (or is about to expire within 60 seconds).
// If Expires is 0, the credential is treated as never expiring.
func (o *OAuthCredential) IsExpired() bool {
	if o.Expires == 0 {
		return false
	}
	return time.Now().Unix() >= o.Expires/1000-60
}

// SaveAuthConfig serializes auth and writes it to path with permission 0600.
func SaveAuthConfig(path string, auth AuthConfig) error {
	data, err := yaml.Marshal(auth)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// tokenResponse is the JSON response from an OAuth token endpoint.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	IDToken      string `json:"id_token"`
}

type oauthErrorEnvelope struct {
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

// OAuthRefreshError captures a structured token refresh failure.
type OAuthRefreshError struct {
	StatusCode int
	Code       string
	Message    string
	Body       string
}

func (e *OAuthRefreshError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Code != "" || e.Message != "" {
		return fmt.Sprintf("token endpoint returned %d: %s (%s)", e.StatusCode, e.Message, e.Code)
	}
	return fmt.Sprintf("token endpoint returned %d: %s", e.StatusCode, e.Body)
}

// IsRefreshTokenInvalid reports whether the refresh failed because the refresh
// token itself is no longer usable and the credential should be marked expired.
func IsRefreshTokenInvalid(err error) bool {
	var refreshErr *OAuthRefreshError
	if !errors.As(err, &refreshErr) {
		return false
	}
	if refreshErr.Code == "refresh_token_reused" || refreshErr.Code == "invalid_grant" {
		return true
	}
	msg := strings.ToLower(refreshErr.Message)
	return strings.Contains(msg, "refresh token has already been used") ||
		strings.Contains(msg, "refresh token") && strings.Contains(msg, "expired") ||
		strings.Contains(msg, "refresh token") && strings.Contains(msg, "invalid")
}

// RefreshOAuthToken refreshes an OAuth credential using the refresh_token grant.
// If the response omits refresh_token, the original refresh token is reused.
// expires is stored as a millisecond-precision Unix timestamp.
func RefreshOAuthToken(ctx context.Context, httpClient *http.Client, tokenURL, clientID string, cred *OAuthCredential) (*OAuthCredential, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", cred.Refresh)
	if clientID != "" {
		form.Set("client_id", clientID)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := httpClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var env oauthErrorEnvelope
		if err := json.Unmarshal(body, &env); err == nil && env.Error != nil {
			return nil, &OAuthRefreshError{
				StatusCode: resp.StatusCode,
				Code:       env.Error.Code,
				Message:    env.Error.Message,
				Body:       string(body),
			}
		}
		return nil, &OAuthRefreshError{StatusCode: resp.StatusCode, Body: string(body)}
	}

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("parse token response: %w", err)
	}

	newRefresh := tr.RefreshToken
	if newRefresh == "" {
		newRefresh = cred.Refresh
	}

	var expires int64
	if tr.ExpiresIn > 0 {
		expires = (time.Now().Unix() + tr.ExpiresIn) * 1000
	}

	idClaims := extractOAuthClaims(tr.IDToken)
	accessClaims := extractOAuthClaims(tr.AccessToken)

	accountID := extractOAuthAccountIDFromClaims(idClaims)
	if accountID == "" {
		accountID = extractOAuthAccountIDFromClaims(accessClaims)
	}
	if accountID == "" {
		accountID = cred.AccountID
	}

	email := idClaims.Email
	if email == "" && idClaims.OpenAIProfile != nil {
		email = idClaims.OpenAIProfile.Email
	}
	if email == "" {
		email = accessClaims.Email
	}
	if email == "" && accessClaims.OpenAIProfile != nil {
		email = accessClaims.OpenAIProfile.Email
	}
	if email == "" {
		email = cred.Email
	}

	return &OAuthCredential{
		Access:    tr.AccessToken,
		Refresh:   newRefresh,
		Expires:   expires,
		AccountID: accountID,
		Email:     email,
		// Status is cleared on successful refresh (credential is now valid)
	}, nil
}

// LoadAuthFromEnv loads authentication configuration from environment variables.
func LoadAuthFromEnv() AuthConfig {
	auth := make(AuthConfig)
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		auth["anthropic"] = append(auth["anthropic"], ProviderCredential{APIKey: key})
	}
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		auth["openai"] = append(auth["openai"], ProviderCredential{APIKey: key})
	}
	return auth
}
