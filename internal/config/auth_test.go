package config

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func testJWT(payload string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	body := base64.RawURLEncoding.EncodeToString([]byte(payload))
	return header + "." + body + ".sig"
}

func TestLoadAuthConfig_APIKey(t *testing.T) {
	f, err := os.CreateTemp("", "auth-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())

	_, _ = f.WriteString(`anthropic:
  - sk-ant-test123
`)
	f.Close()

	auth, err := LoadAuthConfig(f.Name())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	creds := auth["anthropic"]
	if len(creds) != 1 {
		t.Fatalf("expected 1 credential, got %d", len(creds))
	}
	if creds[0].APIKey != "sk-ant-test123" {
		t.Errorf("expected APIKey=sk-ant-test123, got %q", creds[0].APIKey)
	}
	if creds[0].OAuth != nil {
		t.Error("expected OAuth to be nil")
	}
}

func TestLoadAuthConfig_EnvVarSet(t *testing.T) {
	t.Setenv("TEST_API_KEY", "env-key-value")

	f, err := os.CreateTemp("", "auth-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())

	_, _ = f.WriteString(`openai:
  - $TEST_API_KEY
`)
	f.Close()

	auth, err := LoadAuthConfig(f.Name())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	creds := auth["openai"]
	if len(creds) != 1 {
		t.Fatalf("expected 1 credential, got %d", len(creds))
	}
	if creds[0].APIKey != "env-key-value" {
		t.Errorf("expected APIKey=env-key-value, got %q", creds[0].APIKey)
	}
}

func TestLoadAuthConfig_EnvVarUnset(t *testing.T) {
	os.Unsetenv("UNSET_VAR_XYZ")

	f, err := os.CreateTemp("", "auth-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())

	_, _ = f.WriteString(`openai:
  - $UNSET_VAR_XYZ
`)
	f.Close()

	auth, err := LoadAuthConfig(f.Name())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// unset env vars should be filtered out
	if _, ok := auth["openai"]; ok {
		t.Error("expected openai provider to be absent for unset env var")
	}
}

func TestLoadAuthConfig_OAuth(t *testing.T) {
	f, err := os.CreateTemp("", "auth-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())

	_, _ = f.WriteString(`codex:
  - refresh: refresh-token
    access: access-token
    expires: 1774009702606
    account_id: acc-123
`)
	f.Close()

	auth, err := LoadAuthConfig(f.Name())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	creds := auth["codex"]
	if len(creds) != 1 {
		t.Fatalf("expected 1 credential, got %d", len(creds))
	}
	if creds[0].APIKey != "" {
		t.Errorf("expected APIKey to be empty, got %q", creds[0].APIKey)
	}
	if creds[0].OAuth == nil {
		t.Fatal("expected OAuth to be non-nil")
	}
	if creds[0].OAuth.Refresh != "refresh-token" {
		t.Errorf("expected Refresh=refresh-token, got %q", creds[0].OAuth.Refresh)
	}
	if creds[0].OAuth.Access != "access-token" {
		t.Errorf("expected Access=access-token, got %q", creds[0].OAuth.Access)
	}
	if creds[0].OAuth.Expires != 1774009702606 {
		t.Errorf("expected Expires=1774009702606, got %d", creds[0].OAuth.Expires)
	}
	if creds[0].OAuth.AccountID != "acc-123" {
		t.Errorf("expected AccountID=acc-123, got %q", creds[0].OAuth.AccountID)
	}
}

func TestLoadAuthConfig_IgnoresOAuthStatusInAuthYAML(t *testing.T) {
	f, err := os.CreateTemp("", "auth-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())

	_, _ = f.WriteString(`codex:
  - refresh: refresh-token
    access: access-token
    expires: 1774009702606
    account_id: acc-123
    status: deactivated
`)
	f.Close()

	auth, err := LoadAuthConfig(f.Name())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	creds := auth["codex"]
	if len(creds) != 1 || creds[0].OAuth == nil {
		t.Fatalf("expected one OAuth credential, got %#v", creds)
	}
	if creds[0].OAuth.Status != OAuthStatusNormal {
		t.Fatalf("auth.yaml status should be ignored, got %q", creds[0].OAuth.Status)
	}
}

func TestLoadAuthConfig_OAuthDerivesAccountIDFromAccessToken(t *testing.T) {
	f, err := os.CreateTemp("", "auth-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())

	accessToken := testJWT(`{"https://api.openai.com/auth":{"chatgpt_account_id":"acc-derived"}}`)
	_, _ = f.WriteString("codex:\n  - refresh: refresh-token\n    access: " + accessToken + "\n    expires: 1774009702606\n")
	f.Close()

	auth, err := LoadAuthConfig(f.Name())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	creds := auth["codex"]
	if len(creds) != 1 || creds[0].OAuth == nil {
		t.Fatalf("expected one OAuth credential, got %#v", creds)
	}
	if creds[0].OAuth.AccountID != "acc-derived" {
		t.Fatalf("expected derived AccountID=acc-derived, got %q", creds[0].OAuth.AccountID)
	}
}

func TestExtractOAuthAccountIDFromToken(t *testing.T) {
	tests := []struct {
		name  string
		token string
		want  string
	}{
		{
			name:  "top level claim",
			token: testJWT(`{"chatgpt_account_id":"acc-top"}`),
			want:  "acc-top",
		},
		{
			name:  "nested openai auth claim",
			token: testJWT(`{"https://api.openai.com/auth":{"chatgpt_account_id":"acc-nested"}}`),
			want:  "acc-nested",
		},
		{
			name:  "organizations fallback",
			token: testJWT(`{"organizations":[{"id":"org-123"}]}`),
			want:  "org-123",
		},
		{
			name:  "invalid token",
			token: "not-a-jwt",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtractOAuthAccountIDFromToken(tt.token); got != tt.want {
				t.Fatalf("ExtractOAuthAccountIDFromToken() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractOAuthEmailFromToken(t *testing.T) {
	tests := []struct {
		name  string
		token string
		want  string
	}{
		{
			name:  "top level email",
			token: testJWT(`{"email":"user@example.com"}`),
			want:  "user@example.com",
		},
		{
			name:  "nested profile email",
			token: testJWT(`{"https://api.openai.com/profile":{"email":"user@example.com","email_verified":true}}`),
			want:  "user@example.com",
		},
		{
			name:  "top level preferred over nested",
			token: testJWT(`{"email":"top@example.com","https://api.openai.com/profile":{"email":"nested@example.com"}}`),
			want:  "top@example.com",
		},
		{
			name:  "no email claim",
			token: testJWT(`{"chatgpt_account_id":"acc-1"}`),
			want:  "",
		},
		{
			name:  "invalid token",
			token: "not-a-jwt",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtractOAuthEmailFromToken(tt.token); got != tt.want {
				t.Fatalf("ExtractOAuthEmailFromToken() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLoadAuthConfig_Mixed(t *testing.T) {
	f, err := os.CreateTemp("", "auth-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())

	_, _ = f.WriteString(`anthropic:
  - sk-api-key
  - refresh: r-token
    access: a-token
    expires: 9999999999
`)
	f.Close()

	auth, err := LoadAuthConfig(f.Name())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	creds := auth["anthropic"]
	if len(creds) != 2 {
		t.Fatalf("expected 2 credentials, got %d", len(creds))
	}
	if creds[0].APIKey != "sk-api-key" {
		t.Errorf("expected first cred APIKey=sk-api-key, got %q", creds[0].APIKey)
	}
	if creds[1].OAuth == nil {
		t.Error("expected second cred to have OAuth")
	}
}

func TestExtractAPIKeys(t *testing.T) {
	creds := []ProviderCredential{
		{APIKey: "key1"},
		{OAuth: &OAuthCredential{Refresh: "r", Access: testJWT(`{"chatgpt_account_id":"acc-1"}`), Expires: 123}},
		{APIKey: "key2"},
		{APIKey: ""},
	}
	keys := ExtractAPIKeys(creds)
	// OAuth access token "a" is now included
	if len(keys) != 3 {
		t.Fatalf("expected 3 keys, got %d: %v", len(keys), keys)
	}
	if keys[0] != "key1" || keys[1] == "" || keys[2] != "key2" {
		t.Errorf("unexpected keys: %v", keys)
	}
}

func TestExtractAPIKeys_OAuth(t *testing.T) {
	creds := []ProviderCredential{
		{OAuth: &OAuthCredential{Refresh: "refresh-tok", Access: testJWT(`{"chatgpt_account_id":"acc-1"}`), Expires: 9999999999000}},
		{APIKey: "plain-key"},
	}
	keys := ExtractAPIKeys(creds)
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d: %v", len(keys), keys)
	}
	if keys[0] == "" {
		t.Errorf("expected keys[0] to be OAuth access token, got empty")
	}
	if keys[1] != "plain-key" {
		t.Errorf("expected keys[1]=plain-key, got %q", keys[1])
	}
}

func TestExtractAPIKeys_OAuthDeactivatedStillIncludedForDeferredFiltering(t *testing.T) {
	creds := []ProviderCredential{
		{OAuth: &OAuthCredential{Refresh: "refresh-tok", Access: testJWT(`{"chatgpt_account_id":"acc-1"}`), Expires: 9999999999000, Status: OAuthStatusDeactivated}},
	}
	keys := ExtractAPIKeys(creds)
	if len(keys) != 1 || keys[0] == "" {
		t.Fatalf("expected deactivated OAuth access token to remain in extracted keys for runtime filtering, got %v", keys)
	}
}

func TestExtractAPIKeys_OAuthWithoutAccountIDSkipped(t *testing.T) {
	creds := []ProviderCredential{
		{OAuth: &OAuthCredential{Access: testJWT(`{"exp":4102444800}`), Expires: 9999999999000, Email: "user@example.com"}},
	}
	keys := ExtractAPIKeys(creds)
	if len(keys) != 0 {
		t.Fatalf("expected OAuth access token without account_id to be skipped, got %v", keys)
	}
}

func TestExtractAPIKeys_OAuthMismatchedAccountIDSkipped(t *testing.T) {
	creds := []ProviderCredential{
		{OAuth: &OAuthCredential{Access: testJWT(`{"chatgpt_account_id":"acc-token"}`), AccountID: "acc-other", Expires: 9999999999000}},
	}
	keys := ExtractAPIKeys(creds)
	if len(keys) != 0 {
		t.Fatalf("expected mismatched OAuth access token to be skipped, got %v", keys)
	}
}

func TestExtractAPIKeys_OAuthEmptyAccess(t *testing.T) {
	creds := []ProviderCredential{
		{OAuth: &OAuthCredential{Refresh: "refresh-tok", Access: "", Expires: 0}},
		{APIKey: "plain-key"},
	}
	keys := ExtractAPIKeys(creds)
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys (refresh-only OAuth plus plain key), got %d: %v", len(keys), keys)
	}
	if keys[0] != "" || keys[1] != "plain-key" {
		t.Errorf("unexpected keys: %v", keys)
	}
}

func TestOAuthCredential_IsExpired(t *testing.T) {
	t.Run("already expired", func(t *testing.T) {
		// Expires set to a timestamp in the distant past (milliseconds)
		cred := &OAuthCredential{Expires: 1000} // 1 second since epoch
		if !cred.IsExpired() {
			t.Error("expected IsExpired()=true for past timestamp")
		}
	})

	t.Run("not yet expired", func(t *testing.T) {
		futureExpires := time.Now().Add(1*time.Hour).Unix() * 1000
		cred := &OAuthCredential{Expires: futureExpires}
		if cred.IsExpired() {
			t.Error("expected IsExpired()=false for future timestamp (1 hour ahead)")
		}
	})

	t.Run("expires zero means never expired", func(t *testing.T) {
		cred := &OAuthCredential{Expires: 0}
		if cred.IsExpired() {
			t.Error("expected IsExpired()=false when Expires=0")
		}
	})
}

func TestSaveAndLoadAuthConfig_RoundTrip(t *testing.T) {
	f, err := os.CreateTemp("", "auth-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	f.Close()

	original := AuthConfig{
		"myprovider": {
			{APIKey: "plain-key"},
			{OAuth: &OAuthCredential{
				Refresh:   "r-token",
				Access:    "a-token",
				Expires:   9999999999000,
				AccountID: "acc-1",
				Status:    OAuthStatusDeactivated,
			}},
		},
	}

	if err := SaveAuthConfig(f.Name(), original); err != nil {
		t.Fatalf("SaveAuthConfig: %v", err)
	}

	loaded, err := LoadAuthConfig(f.Name())
	if err != nil {
		t.Fatalf("LoadAuthConfig after save: %v", err)
	}

	creds := loaded["myprovider"]
	if len(creds) != 2 {
		t.Fatalf("expected 2 creds, got %d", len(creds))
	}
	if creds[0].APIKey != "plain-key" {
		t.Errorf("expected APIKey=plain-key, got %q", creds[0].APIKey)
	}
	if creds[1].OAuth == nil {
		t.Fatal("expected OAuth credential, got nil")
	}
	if creds[1].OAuth.Refresh != "r-token" {
		t.Errorf("expected Refresh=r-token, got %q", creds[1].OAuth.Refresh)
	}
	if creds[1].OAuth.Access != "a-token" {
		t.Errorf("expected Access=a-token, got %q", creds[1].OAuth.Access)
	}
	if creds[1].OAuth.Expires != 9999999999000 {
		t.Errorf("expected Expires=9999999999000, got %d", creds[1].OAuth.Expires)
	}
	if creds[1].OAuth.AccountID != "acc-1" {
		t.Errorf("expected AccountID=acc-1, got %q", creds[1].OAuth.AccountID)
	}
	if creds[1].OAuth.Status != OAuthStatusNormal {
		t.Errorf("expected auth.yaml status to be omitted on round trip, got %q", creds[1].OAuth.Status)
	}
	data, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(data), "status:") {
		t.Fatalf("auth.yaml should not contain status, got:\n%s", string(data))
	}
}

func TestIsRefreshTokenInvalid(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "refresh_token_reused",
			err:  &OAuthRefreshError{StatusCode: 401, Code: "refresh_token_reused", Message: "Your refresh token has already been used to generate a new access token."},
			want: true,
		},
		{
			name: "invalid_grant",
			err:  &OAuthRefreshError{StatusCode: 400, Code: "invalid_grant", Message: "refresh token expired"},
			want: true,
		},
		{
			name: "app_session_terminated",
			err:  &OAuthRefreshError{StatusCode: 400, Code: "app_session_terminated", Message: "Your session has ended. Please log in again."},
			want: true,
		},
		{
			name: "token_invalidated",
			err:  &OAuthRefreshError{StatusCode: 401, Code: "token_invalidated", Message: "token invalidated"},
			want: true,
		},
		{
			name: "unknown_unauthorized",
			err:  &OAuthRefreshError{StatusCode: 401, Body: `{"detail":"unauthorized"}`},
			want: true,
		},
		{
			name: "sign_in_again_message",
			err:  &OAuthRefreshError{StatusCode: 400, Message: "Please try signing in again."},
			want: true,
		},
		{
			name: "missing_refresh_token",
			err:  &OAuthRefreshError{Code: "missing_refresh_token", Message: "refresh token is empty"},
			want: false,
		},
		{
			name: "other oauth error",
			err:  &OAuthRefreshError{StatusCode: 403, Code: "access_denied", Message: "country not supported"},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRefreshTokenInvalid(tt.err); got != tt.want {
				t.Fatalf("IsRefreshTokenInvalid() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRefreshOAuthToken_ReusesExistingRefreshWhenResponseOmitsRefreshToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/x-www-form-urlencoded") {
			t.Fatalf("Content-Type = %q, want application/x-www-form-urlencoded", got)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if got := r.Form.Get("grant_type"); got != "refresh_token" {
			t.Fatalf("grant_type = %q, want refresh_token", got)
		}
		if got := r.Form.Get("refresh_token"); got != "old-refresh" {
			t.Fatalf("refresh_token = %q, want old-refresh", got)
		}
		if got := r.Form.Get("client_id"); got != "client-123" {
			t.Fatalf("client_id = %q, want client-123", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new-access-token","expires_in":3600}`))
	}))
	defer server.Close()

	cred := &OAuthCredential{
		Refresh:   "old-refresh",
		Access:    "old-access",
		Expires:   123,
		AccountID: "acc-1",
		Email:     "old@example.com",
	}
	updated, err := RefreshOAuthToken(context.Background(), server.Client(), server.URL, "client-123", cred)
	if err != nil {
		t.Fatalf("RefreshOAuthToken: %v", err)
	}
	if updated.Refresh != "old-refresh" {
		t.Fatalf("Refresh = %q, want old-refresh", updated.Refresh)
	}
	if updated.Access != "new-access-token" {
		t.Fatalf("Access = %q, want new-access-token", updated.Access)
	}
}

func TestRefreshOAuthToken_EmptyRefreshTokenDoesNotCallEndpoint(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	_, err := RefreshOAuthToken(context.Background(), server.Client(), server.URL, "client-123", &OAuthCredential{Access: "old-access"})
	var refreshErr *OAuthRefreshError
	if !errors.As(err, &refreshErr) || refreshErr.Code != "missing_refresh_token" {
		t.Fatalf("RefreshOAuthToken err = %v, want missing_refresh_token error", err)
	}
	if IsRefreshTokenInvalid(err) {
		t.Fatalf("missing_refresh_token should not be classified as invalid refresh token")
	}
	if called {
		t.Fatal("token endpoint was called despite empty refresh token")
	}
}

func TestRefreshOpenAICodexOAuthToken_UsesJSONRequestAndPartialUpdate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
			t.Fatalf("Content-Type = %q, want application/json", got)
		}
		var body struct {
			GrantType    string `json:"grant_type"`
			RefreshToken string `json:"refresh_token"`
			ClientID     string `json:"client_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("Decode body: %v", err)
		}
		if body.GrantType != "refresh_token" {
			t.Fatalf("grant_type = %q, want refresh_token", body.GrantType)
		}
		if body.RefreshToken != "old-refresh" {
			t.Fatalf("refresh_token = %q, want old-refresh", body.RefreshToken)
		}
		if body.ClientID != "client-123" {
			t.Fatalf("client_id = %q, want client-123", body.ClientID)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new-access-token","expires_in":3600}`))
	}))
	defer server.Close()

	cred := &OAuthCredential{
		Refresh:   "old-refresh",
		Access:    "old-access",
		Expires:   123,
		AccountID: "acc-1",
		Email:     "old@example.com",
	}
	updated, err := RefreshOpenAICodexOAuthToken(context.Background(), server.Client(), server.URL, "client-123", cred)
	if err != nil {
		t.Fatalf("RefreshOpenAICodexOAuthToken: %v", err)
	}
	if updated.Refresh != "old-refresh" {
		t.Fatalf("Refresh = %q, want old-refresh", updated.Refresh)
	}
	if updated.Access != "new-access-token" {
		t.Fatalf("Access = %q, want new-access-token", updated.Access)
	}
}

func TestRefreshOAuthToken_ReusesExistingAccessWhenResponseOmitsAccessToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"refresh_token":"new-refresh-token","expires_in":3600}`))
	}))
	defer server.Close()

	cred := &OAuthCredential{
		Refresh:   "old-refresh",
		Access:    "old-access",
		Expires:   123,
		AccountID: "acc-1",
		Email:     "old@example.com",
	}
	updated, err := RefreshOAuthToken(context.Background(), server.Client(), server.URL, "client-123", cred)
	if err != nil {
		t.Fatalf("RefreshOAuthToken: %v", err)
	}
	if updated.Access != "old-access" {
		t.Fatalf("Access = %q, want old-access", updated.Access)
	}
	if updated.Refresh != "new-refresh-token" {
		t.Fatalf("Refresh = %q, want new-refresh-token", updated.Refresh)
	}
	if updated.Expires == cred.Expires {
		t.Fatalf("Expires = %d, want updated value different from %d", updated.Expires, cred.Expires)
	}
}

func TestLoadAuthFromEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "ant-key")
	t.Setenv("OPENAI_API_KEY", "oai-key")

	auth := LoadAuthFromEnv()

	antCreds := auth["anthropic"]
	if len(antCreds) != 1 || antCreds[0].APIKey != "ant-key" {
		t.Errorf("unexpected anthropic creds: %v", antCreds)
	}
	oaiCreds := auth["openai"]
	if len(oaiCreds) != 1 || oaiCreds[0].APIKey != "oai-key" {
		t.Errorf("unexpected openai creds: %v", oaiCreds)
	}
}
