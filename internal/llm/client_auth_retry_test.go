package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/ratelimit"
)

func TestMarkKeyCooldown429CodexOAuthQuotaExhaustedUsesResetWindow(t *testing.T) {
	ctx := context.Background()
	resetPrimary := time.Now().Add(2 * time.Hour)
	resetSecondary := time.Now().Add(24 * time.Hour)
	p := NewProviderConfig("p", config.ProviderConfig{
		Type:         config.ProviderTypeResponses,
		Preset:       config.ProviderPresetCodex,
		RetryBackoff: config.RetryBackoffNone,
	}, []string{"oauth-key"})
	p.mu.Lock()
	p.keyStates[0].OAuthInfo = &OAuthKeyInfo{Expires: time.Now().Add(time.Hour).UnixMilli()}
	p.mu.Unlock()
	p.UpdateKeySnapshot("oauth-key", &ratelimit.KeyRateLimitSnapshot{
		Primary:   &ratelimit.RateLimitWindow{UsedPct: 100, ResetsAt: resetPrimary},
		Secondary: &ratelimit.RateLimitWindow{UsedPct: 100, ResetsAt: resetSecondary},
	})
	res := markKeyCooldown(ctx, p, "oauth-key", "test-model", &APIError{StatusCode: 429, Message: "quota exhausted"})
	if !res.cooldownApplied {
		t.Fatal("expected cooldownApplied=true for quota exhaustion")
	}
	p.mu.Lock()
	ks := p.keyStates[0]
	exhaustedUntil := ks.ExhaustedUntil
	cooldownEnd := ks.CooldownEnd
	p.mu.Unlock()
	if exhaustedUntil.Before(resetSecondary.Add(-2*time.Second)) || exhaustedUntil.After(resetSecondary.Add(2*time.Second)) {
		t.Fatalf("ExhaustedUntil = %v, want ~%v", exhaustedUntil, resetSecondary)
	}
	if !cooldownEnd.IsZero() {
		t.Fatalf("CooldownEnd = %v, want zero when quota exhaustion uses hard reset window", cooldownEnd)
	}
}

func TestMarkKeyCooldown429CodexOAuthRetryHintOutranksExplicitPacingWithoutExhaustedSnapshot(t *testing.T) {
	ctx := context.Background()
	p := NewProviderConfig("p", config.ProviderConfig{
		Type:         config.ProviderTypeResponses,
		Preset:       config.ProviderPresetCodex,
		RetryBackoff: config.RetryBackoffNone,
	}, []string{"oauth-key"})
	p.mu.Lock()
	p.keyStates[0].OAuthInfo = &OAuthKeyInfo{Expires: time.Now().Add(time.Hour).UnixMilli()}
	p.mu.Unlock()

	res := markKeyCooldown(ctx, p, "oauth-key", "test-model", &APIError{
		StatusCode: 429,
		Code:       "usage_limit_reached",
		Message:    "usage limit reached",
		RetryAfter: 2 * time.Minute,
	})
	if !res.cooldownApplied {
		t.Fatal("server-directed Retry-After must apply even under retry_backoff=none")
	}
	p.mu.Lock()
	ks := *p.keyStates[0]
	p.mu.Unlock()
	if !ks.ExhaustedUntil.IsZero() {
		t.Fatalf("exhausted_until = %v, want zero without a confirmed quota snapshot", ks.ExhaustedUntil)
	}
	if remain := time.Until(ks.CooldownEnd); remain < 115*time.Second || remain > 125*time.Second {
		t.Fatalf("cooldown remaining = %v, want verbatim ~2m Retry-After", remain)
	}
}

func TestMarkKeyCooldown401OAuthRefreshReturnsRefreshedKey(t *testing.T) {
	ctx := context.Background()
	oldAccess := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-1","chatgpt_user_id":"user-1"}`)
	newAccess := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-1","chatgpt_user_id":"user-1","exp":4102444800}`)
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"`+newAccess+`","refresh_token":"new-refresh-token","expires_in":3600}`)
	}))
	defer refreshServer.Close()

	creds := []config.ProviderCredential{{
		OAuth: &config.OAuthCredential{
			Access:  oldAccess,
			Refresh: "old-refresh-token",
			Expires: time.Now().Add(time.Hour).UnixMilli(),
		},
	}}
	auth := config.AuthConfig{"openai": creds}
	var authMu sync.Mutex
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, config.ExtractAPIKeys(creds))
	p.SetOAuthRefresher(refreshServer.URL, "client-id", "", "", &auth, &authMu, map[string]OAuthKeySetup{creds[0].OAuth.Access: {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Expires: creds[0].OAuth.Expires}}, "")

	result := markKeyCooldown(ctx, p, oldAccess, "test-model", &APIError{StatusCode: 401, Message: "unauthorized"})
	if !result.oauthRefreshed {
		t.Fatal("expected oauthRefreshed=true")
	}
	if result.cooldownApplied {
		t.Fatal("expected cooldownApplied=false after successful refresh")
	}
	if result.refreshedKey != newAccess {
		t.Fatalf("refreshedKey = %q, want refreshed access", result.refreshedKey)
	}

	key, _, err := p.SelectKeyWithContext(ctx)
	if err != nil {
		t.Fatalf("SelectKeyWithContext: %v", err)
	}
	if key != newAccess {
		t.Fatalf("selected key = %q, want refreshed key", key)
	}
}

func TestMarkKeyCooldown403OAuthRefreshReturnsRefreshedKey(t *testing.T) {
	ctx := context.Background()
	oldAccess := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-1","chatgpt_user_id":"user-1"}`)
	newAccess := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-1","chatgpt_user_id":"user-1","exp":4102444800}`)
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"`+newAccess+`","refresh_token":"new-refresh-token","expires_in":3600}`)
	}))
	defer refreshServer.Close()

	creds := []config.ProviderCredential{{
		OAuth: &config.OAuthCredential{
			Access:  oldAccess,
			Refresh: "old-refresh-token",
			Expires: time.Now().Add(time.Hour).UnixMilli(),
		},
	}}
	auth := config.AuthConfig{"openai": creds}
	var authMu sync.Mutex
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, config.ExtractAPIKeys(creds))
	p.SetOAuthRefresher(refreshServer.URL, "client-id", "", "", &auth, &authMu, map[string]OAuthKeySetup{creds[0].OAuth.Access: {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Expires: creds[0].OAuth.Expires}}, "")

	result := markKeyCooldown(ctx, p, oldAccess, "test-model", &APIError{StatusCode: 403, Message: "forbidden"})
	if !result.oauthRefreshed {
		t.Fatal("expected oauthRefreshed=true")
	}
	if result.cooldownApplied {
		t.Fatal("expected cooldownApplied=false after successful refresh")
	}
	if result.refreshedKey != newAccess {
		t.Fatalf("refreshedKey = %q, want refreshed access", result.refreshedKey)
	}
}

func TestMarkKeyCooldown401OAuthNoRefresherPersistsDeactivatedKey(t *testing.T) {
	ctx := context.Background()
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{
		Access:        "oauth-key",
		Refresh:       "refresh-token",
		Expires:       time.Now().Add(time.Hour).UnixMilli(),
		AccountUserID: "user-1__acc-1", AccountID: "acc-1",
	}}}}
	var authMu sync.Mutex
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"oauth-key"})
	p.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", "", &auth, &authMu, map[string]OAuthKeySetup{
		"oauth-key": {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Expires: auth["openai"][0].OAuth.Expires},
	}, "")

	result := markKeyCooldown(ctx, p, "oauth-key", "test-model", &APIError{StatusCode: 401, Code: "account_deactivated", Message: "account deactivated"})
	if !result.cooldownApplied {
		t.Fatal("expected cooldownApplied=true")
	}
	if auth["openai"][0].OAuth.Status != config.OAuthStatusDeactivated {
		t.Fatal("expected auth config OAuth credential to be marked deactivated")
	}

	_, _, err := p.SelectKeyWithContext(ctx)
	if err == nil {
		t.Fatal("expected NoUsableKeysError after disabling only OAuth key")
	}
	if _, ok := errors.AsType[*NoUsableKeysError](err); !ok {
		t.Fatalf("expected NoUsableKeysError, got %T: %v", err, err)
	}
}

func TestMarkKeyCooldown401OAuthNoRefresherDeactivatesKey(t *testing.T) {
	ctx := context.Background()
	p := newTestProviderConfig([]string{"oauth-key"})
	p.mu.Lock()
	p.keyStates[0].OAuthInfo = &OAuthKeyInfo{Expires: time.Now().Add(time.Hour).UnixMilli()}
	p.mu.Unlock()
	// no OAuthRefresher set → TryRefreshOAuthKey returns false → MarkDeactivated
	result := markKeyCooldown(ctx, p, "oauth-key", "test-model", &APIError{StatusCode: 401, Code: "account_deactivated", Message: "account deactivated"})
	if !result.cooldownApplied {
		t.Fatal("expected cooldownApplied=true")
	}
	_, total := p.AvailableKeyCount()
	if total != 0 {
		t.Fatalf("total = %d, want 0: deactivated OAuth key should be excluded", total)
	}
}

func TestMarkKeyCooldownOAuthMessageOnlyDeactivatesKey(t *testing.T) {
	ctx := context.Background()
	for _, status := range []int{401, 403} {
		t.Run(fmt.Sprintf("HTTP_%d", status), func(t *testing.T) {
			p := newTestProviderConfig([]string{"oauth-key"})
			p.mu.Lock()
			p.keyStates[0].OAuthInfo = &OAuthKeyInfo{
				AccountID: "acc-1",
				Email:     "user@example.com",
				Expires:   time.Now().Add(time.Hour).UnixMilli(),
			}
			p.mu.Unlock()

			result := markKeyCooldown(ctx, p, "oauth-key", "test-model", &APIError{StatusCode: status, Message: "Your account has been disabled."})
			if !result.cooldownApplied {
				t.Fatal("expected cooldownApplied=true")
			}
			if result.deactivatedAccountID != "acc-1" || result.deactivatedEmail != "user@example.com" {
				t.Fatalf("deactivated identity = %q/%q, want acc-1/user@example.com", result.deactivatedAccountID, result.deactivatedEmail)
			}
			_, total := p.AvailableKeyCount()
			if total != 0 {
				t.Fatalf("total = %d, want 0: deactivated OAuth key should be excluded", total)
			}
		})
	}
}

func TestMarkKeyCooldown401OAuthInvalidatedSkipsRefreshAndDeactivatesKey(t *testing.T) {
	ctx := context.Background()
	refreshHit := false
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshHit = true
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"access_token":"new-access-token","refresh_token":"new-refresh-token","expires_in":3600}`)
	}))
	defer refreshServer.Close()

	expires := time.Now().Add(time.Hour).UnixMilli()
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{
		Access:        "oauth-key",
		Refresh:       "refresh-token",
		Expires:       expires,
		AccountUserID: "user-1__acc-1", AccountID: "acc-1",
	}}}}
	var authMu sync.Mutex
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"oauth-key"})
	p.SetOAuthRefresher(refreshServer.URL, "client-id", "", "", &auth, &authMu, map[string]OAuthKeySetup{
		"oauth-key": {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Expires: expires},
	}, "")

	result := markKeyCooldown(ctx, p, "oauth-key", "test-model", &APIError{StatusCode: 401, Code: "account_invalidated", Message: "account invalidated"})
	if !result.cooldownApplied {
		t.Fatal("expected cooldownApplied=true")
	}
	if refreshHit {
		t.Fatal("refresh endpoint was called for invalidated account")
	}
	if auth["openai"][0].OAuth.Status != config.OAuthStatusInvalidated {
		t.Fatalf("OAuth status = %q, want invalidated", auth["openai"][0].OAuth.Status)
	}
	_, total := p.AvailableKeyCount()
	if total != 0 {
		t.Fatalf("total = %d, want 0: invalidated OAuth key should be excluded", total)
	}
}

func TestMarkKeyCooldownOAuthMessageOnlyInvalidatedSkipsRefresh(t *testing.T) {
	ctx := context.Background()
	for _, status := range []int{401, 403} {
		t.Run(fmt.Sprintf("HTTP_%d", status), func(t *testing.T) {
			refreshHit := false
			refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				refreshHit = true
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, `{"access_token":"new-access-token","refresh_token":"new-refresh-token","expires_in":3600}`)
			}))
			defer refreshServer.Close()

			expires := time.Now().Add(time.Hour).UnixMilli()
			auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{
				Access:        "oauth-key",
				Refresh:       "refresh-token",
				Expires:       expires,
				AccountUserID: "user-1__acc-1",
				AccountID:     "acc-1",
				Email:         "user@example.com",
			}}}}
			var authMu sync.Mutex
			p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"oauth-key"})
			p.SetOAuthRefresher(refreshServer.URL, "client-id", "", "", &auth, &authMu, map[string]OAuthKeySetup{
				"oauth-key": {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Email: "user@example.com", Expires: expires},
			}, "")

			result := markKeyCooldown(ctx, p, "oauth-key", "test-model", &APIError{StatusCode: status, Message: "Your authentication token has been revoked."})
			if !result.cooldownApplied {
				t.Fatal("expected cooldownApplied=true")
			}
			if refreshHit {
				t.Fatal("refresh endpoint was called for message-only invalidated account")
			}
			if result.invalidatedAccountID != "acc-1" || result.invalidatedEmail != "user@example.com" {
				t.Fatalf("invalidated identity = %q/%q, want acc-1/user@example.com", result.invalidatedAccountID, result.invalidatedEmail)
			}
			if auth["openai"][0].OAuth.Status != config.OAuthStatusInvalidated {
				t.Fatalf("OAuth status = %q, want invalidated", auth["openai"][0].OAuth.Status)
			}
			_, total := p.AvailableKeyCount()
			if total != 0 {
				t.Fatalf("total = %d, want 0: invalidated OAuth key should be excluded", total)
			}
		})
	}
}

func TestMarkKeyCooldown401OAuthEmptyRefreshTokenMarksExpiredWithoutHTTP(t *testing.T) {
	ctx := context.Background()
	refreshHit := false
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshHit = true
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"access_token":"new-access-token","refresh_token":"new-refresh-token","expires_in":3600}`)
	}))
	defer refreshServer.Close()

	expires := time.Now().Add(time.Hour).UnixMilli()
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{
		Access:        "oauth-key",
		Expires:       expires,
		AccountUserID: "user-1__acc-1", AccountID: "acc-1",
	}}}}
	var authMu sync.Mutex
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"oauth-key"})
	p.SetOAuthRefresher(refreshServer.URL, "client-id", "", "", &auth, &authMu, map[string]OAuthKeySetup{
		"oauth-key": {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Expires: expires},
	}, "")

	result := markKeyCooldown(ctx, p, "oauth-key", "test-model", &APIError{StatusCode: 401, Message: "unauthorized"})
	if !result.cooldownApplied {
		t.Fatal("expected cooldownApplied=true")
	}
	if refreshHit {
		t.Fatal("refresh endpoint was called despite empty refresh token")
	}
	if auth["openai"][0].OAuth.Status != config.OAuthStatusExpired {
		t.Fatalf("OAuth status = %q, want expired: missing refresh token cannot recover after access token is rejected", auth["openai"][0].OAuth.Status)
	}
	available, total := p.AvailableKeyCount()
	if available != 0 || total != 0 {
		t.Fatalf("AvailableKeyCount = %d/%d, want 0/0: unrecoverable OAuth key should be permanently excluded", available, total)
	}
}

func TestMarkKeyCooldown401OAuthTokenInvalidatedCodeSkipsRefreshAndInvalidatesKey(t *testing.T) {
	ctx := context.Background()
	refreshHit := false
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshHit = true
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"access_token":"new-access-token","refresh_token":"new-refresh-token","expires_in":3600}`)
	}))
	defer refreshServer.Close()

	expires := time.Now().Add(time.Hour).UnixMilli()
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{
		Access:        "oauth-key",
		Refresh:       "refresh-token",
		Expires:       expires,
		AccountUserID: "user-1__acc-1", AccountID: "acc-1",
	}}}}
	var authMu sync.Mutex
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"oauth-key"})
	p.SetOAuthRefresher(refreshServer.URL, "client-id", "", "", &auth, &authMu, map[string]OAuthKeySetup{
		"oauth-key": {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Expires: expires},
	}, "")

	result := markKeyCooldown(ctx, p, "oauth-key", "test-model", &APIError{StatusCode: 401, Code: "token_invalidated", Message: "Your authentication token has been invalidated. Please try signing in again."})
	if !result.cooldownApplied {
		t.Fatal("expected cooldownApplied=true")
	}
	if refreshHit {
		t.Fatal("refresh endpoint was called for token_invalidated")
	}
	if auth["openai"][0].OAuth.Status != config.OAuthStatusInvalidated {
		t.Fatalf("OAuth status = %q, want invalidated", auth["openai"][0].OAuth.Status)
	}
}

func TestMarkKeyCooldown401OAuthMalformedAuthTokenDetailInvalidatesKey(t *testing.T) {
	ctx := context.Background()
	refreshHit := false
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshHit = true
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"access_token":"new-access-token","refresh_token":"new-refresh-token","expires_in":3600}`)
	}))
	defer refreshServer.Close()

	expires := time.Now().Add(time.Hour).UnixMilli()
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{
		Access:        "oauth-key",
		Refresh:       "refresh-token",
		Expires:       expires,
		AccountUserID: "user-1__acc-1", AccountID: "acc-1",
	}}}}
	var authMu sync.Mutex
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"oauth-key"})
	p.SetOAuthRefresher(refreshServer.URL, "client-id", "", "", &auth, &authMu, map[string]OAuthKeySetup{
		"oauth-key": {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Expires: expires},
	}, "")

	apiErr := parseOpenAIHTTPErrorFromBytes(http.StatusUnauthorized, nil, []byte(`{"detail":"Could not parse your authentication token. Please try signing in again."}`))
	result := markKeyCooldown(ctx, p, "oauth-key", "test-model", apiErr)
	if !result.cooldownApplied {
		t.Fatal("expected cooldownApplied=true")
	}
	if refreshHit {
		t.Fatal("refresh endpoint was called for malformed authentication token")
	}
	if auth["openai"][0].OAuth.Status != config.OAuthStatusInvalidated {
		t.Fatalf("OAuth status = %q, want invalidated", auth["openai"][0].OAuth.Status)
	}
}

func TestMarkKeyCooldown401OAuthRefreshUnknownUnauthorizedMarksExpired(t *testing.T) {
	ctx := context.Background()
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"detail":"unauthorized"}`)
	}))
	defer refreshServer.Close()

	expires := time.Now().Add(time.Hour).UnixMilli()
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{
		Access:        "oauth-key",
		Refresh:       "refresh-token",
		Expires:       expires,
		AccountUserID: "user-1__acc-1", AccountID: "acc-1",
	}}}}
	var authMu sync.Mutex
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"oauth-key"})
	p.SetOAuthRefresher(refreshServer.URL, "client-id", "", "", &auth, &authMu, map[string]OAuthKeySetup{
		"oauth-key": {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Expires: expires},
	}, "")

	result := markKeyCooldown(ctx, p, "oauth-key", "test-model", &APIError{StatusCode: 401, Message: "unauthorized"})
	if !result.cooldownApplied {
		t.Fatal("expected cooldownApplied=true")
	}
	if auth["openai"][0].OAuth.Status != config.OAuthStatusExpired {
		t.Fatalf("OAuth status = %q, want expired for refresh 401", auth["openai"][0].OAuth.Status)
	}
}

func TestCompleteStreamTerminal401MarksInvalidatedOAuthBeforeReturning(t *testing.T) {
	ctx := context.Background()
	expires := time.Now().Add(time.Hour).UnixMilli()
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{
		Access:        "oauth-key",
		Refresh:       "refresh-token",
		Expires:       expires,
		AccountUserID: "user-1__acc-1", AccountID: "acc-1",
		Email: "user@example.com",
	}}}}
	var authMu sync.Mutex
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"oauth-key"})
	p.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", "", &auth, &authMu, map[string]OAuthKeySetup{
		"oauth-key": {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Email: "user@example.com", Expires: expires},
	}, "")
	impl := &constantErrProvider{err: &APIError{StatusCode: 401, Code: "account_invalidated", Message: "account invalidated"}}
	client := NewClient(p, impl, "model", 128, "")
	client.SetTerminalAPIStatusCodes(401)

	var deltas []message.StreamDelta
	_, err := client.CompleteStream(ctx, []message.Message{{Role: "user", Content: "hello"}}, nil, func(delta message.StreamDelta) {
		deltas = append(deltas, delta)
	})
	if err == nil {
		t.Fatal("expected terminal API error")
	}
	if auth["openai"][0].OAuth.Status != config.OAuthStatusInvalidated {
		t.Fatalf("expected auth config OAuth credential to be marked invalidated, got %q", auth["openai"][0].OAuth.Status)
	}
	foundInvalidatedDelta := false
	for _, delta := range deltas {
		if delta.Type == "key_invalidated" && delta.AccountID == "acc-1" && delta.Email == "user@example.com" {
			foundInvalidatedDelta = true
			break
		}
	}
	if !foundInvalidatedDelta {
		t.Fatalf("expected key_invalidated delta with account metadata, got %#v", deltas)
	}
}

func TestMarkKeyCooldown403OAuthInvalidatedSkipsRefreshAndInvalidatesKey(t *testing.T) {
	ctx := context.Background()
	refreshHit := false
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshHit = true
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"access_token":"new-access-token","refresh_token":"new-refresh-token","expires_in":3600}`)
	}))
	defer refreshServer.Close()

	expires := time.Now().Add(time.Hour).UnixMilli()
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{
		Access:        "oauth-key",
		Refresh:       "refresh-token",
		Expires:       expires,
		AccountUserID: "user-1__acc-1", AccountID: "acc-1",
	}}}}
	var authMu sync.Mutex
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"oauth-key"})
	p.SetOAuthRefresher(refreshServer.URL, "client-id", "", "", &auth, &authMu, map[string]OAuthKeySetup{
		"oauth-key": {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Expires: expires},
	}, "")

	result := markKeyCooldown(ctx, p, "oauth-key", "test-model", &APIError{StatusCode: 403, Code: "account_invalidated", Message: "account invalidated"})
	if !result.cooldownApplied {
		t.Fatal("expected cooldownApplied=true")
	}
	if refreshHit {
		t.Fatal("refresh endpoint was called for invalidated account")
	}
	if auth["openai"][0].OAuth.Status != config.OAuthStatusInvalidated {
		t.Fatalf("OAuth status = %q, want invalidated", auth["openai"][0].OAuth.Status)
	}
	_, total := p.AvailableKeyCount()
	if total != 0 {
		t.Fatalf("total = %d, want 0: invalidated OAuth key should be excluded", total)
	}
}

func TestMarkKeyCooldown403OAuthNoRefresherPersistsDeactivatedKey(t *testing.T) {
	ctx := context.Background()
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{
		Access:        "oauth-key",
		Refresh:       "refresh-token",
		Expires:       time.Now().Add(time.Hour).UnixMilli(),
		AccountUserID: "user-1__acc-1", AccountID: "acc-1",
	}}}}
	var authMu sync.Mutex
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"oauth-key"})
	p.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", "", &auth, &authMu, map[string]OAuthKeySetup{
		"oauth-key": {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Expires: auth["openai"][0].OAuth.Expires},
	}, "")

	result := markKeyCooldown(ctx, p, "oauth-key", "test-model", &APIError{StatusCode: 403, Code: "account_deactivated", Message: "account deactivated"})
	if !result.cooldownApplied {
		t.Fatal("expected cooldownApplied=true")
	}
	if auth["openai"][0].OAuth.Status != config.OAuthStatusDeactivated {
		t.Fatal("expected auth config OAuth credential to be marked deactivated")
	}

	_, _, err := p.SelectKeyWithContext(ctx)
	if err == nil {
		t.Fatal("expected NoUsableKeysError after disabling only OAuth key")
	}
	if _, ok := errors.AsType[*NoUsableKeysError](err); !ok {
		t.Fatalf("expected NoUsableKeysError, got %T: %v", err, err)
	}
}

func TestMarkKeyCooldown403OAuthNoRefresherDeactivatesKey(t *testing.T) {
	ctx := context.Background()
	p := newTestProviderConfig([]string{"oauth-key"})
	p.mu.Lock()
	p.keyStates[0].OAuthInfo = &OAuthKeyInfo{Expires: time.Now().Add(time.Hour).UnixMilli()}
	p.mu.Unlock()
	result := markKeyCooldown(ctx, p, "oauth-key", "test-model", &APIError{StatusCode: 403, Code: "account_deactivated", Message: "account deactivated"})
	if !result.cooldownApplied {
		t.Fatal("expected cooldownApplied=true")
	}
	_, total := p.AvailableKeyCount()
	if total != 0 {
		t.Fatalf("total = %d, want 0: deactivated OAuth key should be excluded", total)
	}
}

func TestCompleteStreamTerminal401MarksExpiredOAuthBeforeReturning(t *testing.T) {
	ctx := context.Background()
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"Your refresh token has already been used to generate a new access token.","code":"refresh_token_reused"}}`)
	}))
	defer refreshServer.Close()

	expires := time.Now().Add(time.Hour).UnixMilli()
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{
		Access:        "oauth-key",
		Refresh:       "refresh-token",
		Expires:       expires,
		AccountUserID: "user-1__acc-1", AccountID: "acc-1",
		Email: "user@example.com",
	}}}}
	var authMu sync.Mutex
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"oauth-key"})
	p.SetOAuthRefresher(refreshServer.URL, "client-id", "", "", &auth, &authMu, map[string]OAuthKeySetup{
		"oauth-key": {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Email: "user@example.com", Expires: expires},
	}, "")
	impl := &constantErrProvider{err: &APIError{StatusCode: 401, Message: "unauthorized"}}
	client := NewClient(p, impl, "model", 128, "")
	client.SetTerminalAPIStatusCodes(401)

	var deltas []message.StreamDelta
	_, err := client.CompleteStream(ctx, []message.Message{{Role: "user", Content: "hello"}}, nil, func(delta message.StreamDelta) {
		deltas = append(deltas, delta)
	})
	if err == nil {
		t.Fatal("expected terminal API error")
	}
	if auth["openai"][0].OAuth.Status != config.OAuthStatusExpired {
		t.Fatalf("expected auth config OAuth credential to be marked expired, got %q", auth["openai"][0].OAuth.Status)
	}
	if impl.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", impl.calls)
	}
	foundExpiredDelta := false
	for _, delta := range deltas {
		if delta.Type == "key_expired" && delta.AccountID == "acc-1" && delta.Email == "user@example.com" {
			foundExpiredDelta = true
			break
		}
	}
	if !foundExpiredDelta {
		t.Fatalf("expected key_expired delta with account metadata, got %#v", deltas)
	}
}

func TestMarkKeyCooldown403OAuthNonDeactivationMessageUsesCooldown(t *testing.T) {
	ctx := context.Background()
	p := newTestProviderConfig([]string{"oauth-key"})
	p.mu.Lock()
	p.keyStates[0].OAuthInfo = &OAuthKeyInfo{Expires: time.Now().Add(time.Hour).UnixMilli()}
	p.mu.Unlock()
	// generic 403 (e.g. from proxy) — message does not match deactivation keywords
	result := markKeyCooldown(ctx, p, "oauth-key", "test-model", &APIError{StatusCode: 403, Message: "forbidden"})
	if !result.cooldownApplied {
		t.Fatal("expected cooldownApplied=true")
	}
	if result.deactivatedAccountID != "" || result.deactivatedEmail != "" {
		t.Fatal("expected no deactivation for proxy 403")
	}
	_, total := p.AvailableKeyCount()
	if total != 1 {
		t.Fatalf("total = %d, want 1: proxy 403 should not deactivate key", total)
	}
}

func TestMarkKeyCooldown401NonOAuthUsesCooldown(t *testing.T) {
	ctx := context.Background()
	p := newTestProviderConfig([]string{"plain-key"})
	result := markKeyCooldown(ctx, p, "plain-key", "test-model", &APIError{StatusCode: 401, Message: "unauthorized"})
	if !result.cooldownApplied {
		t.Fatal("expected cooldownApplied=true for plain key 401")
	}
	// plain key should use cooldown, not deactivation — total stays 1
	_, total := p.AvailableKeyCount()
	if total != 1 {
		t.Fatalf("total = %d, want 1: non-OAuth key should not be deactivated", total)
	}
}

func TestClientComplete401OAuthRefreshRotatesToNextKey(t *testing.T) {
	oldAccess := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-1","chatgpt_user_id":"user-1"}`)
	newAccess := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-1","exp":4102444800}`)
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"`+newAccess+`","refresh_token":"new-refresh-token","expires_in":3600}`)
	}))
	defer refreshServer.Close()

	creds := []config.ProviderCredential{
		{OAuth: &config.OAuthCredential{Access: oldAccess, Refresh: "refresh-1", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountUserID: "user-1__acc-1", AccountID: "acc-1"}},
		{APIKey: "key-2"},
	}
	auth := config.AuthConfig{"openai": creds}
	var authMu sync.Mutex
	primaryCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"gpt-test": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, config.ExtractAPIKeys(creds))
	primaryCfg.SetOAuthRefresher(refreshServer.URL, "client-id", "", "", &auth, &authMu, map[string]OAuthKeySetup{creds[0].OAuth.Access: {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Expires: creds[0].OAuth.Expires}}, "")

	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{err: &APIError{StatusCode: 401, Message: "account deactivated"}},
		{resp: &message.Response{Content: "ok from key-2"}},
	}
	c := NewClient(primaryCfg, impl, "gpt-test", 4096, "sys")

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("Complete returned error: %v", err)
	}
	if resp == nil || resp.Content != "ok from key-2" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if len(impl.apiKeys) != 2 {
		t.Fatalf("expected 2 provider calls, got %d", len(impl.apiKeys))
	}
	if impl.apiKeys[0] != oldAccess {
		t.Fatalf("first call key = %q, want old access", impl.apiKeys[0])
	}
	if impl.apiKeys[1] != "key-2" {
		t.Fatalf("second call key = %q, want key-2", impl.apiKeys[1])
	}
}

func TestClientCompleteStreamVisibleAuthInvalidatedRotatesKey(t *testing.T) {
	ctx := context.Background()
	refreshHit := false
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshHit = true
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"access_token":"new-access-token","refresh_token":"new-refresh-token","expires_in":3600}`)
	}))
	defer refreshServer.Close()

	expires := time.Now().Add(time.Hour).UnixMilli()
	auth := config.AuthConfig{"openai": {
		{OAuth: &config.OAuthCredential{Access: "oauth-key-1", Refresh: "refresh-1", Expires: expires, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Email: "user@example.com"}},
		{OAuth: &config.OAuthCredential{Access: "oauth-key-2", Refresh: "refresh-2", Expires: expires, AccountUserID: "user-2__acc-2", AccountID: "acc-2", Email: "other@example.com"}},
	}}
	var authMu sync.Mutex
	providerCfg := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, config.ExtractAPIKeys(auth["openai"]))
	providerCfg.SetOAuthRefresher(refreshServer.URL, "client-id", "", "", &auth, &authMu, map[string]OAuthKeySetup{
		"oauth-key-1": {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Email: "user@example.com", Expires: expires},
		"oauth-key-2": {CredentialIndex: 1, AccountUserID: "user-2__acc-2", AccountID: "acc-2", Email: "other@example.com", Expires: expires},
	}, "")

	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{
			streams: []message.StreamDelta{{Type: message.StreamDeltaText, Text: "partial"}},
			err:     &APIError{StatusCode: 401, Code: "token_invalidated", Message: "Your authentication token has been invalidated. Please try signing in again."},
		},
		{resp: &message.Response{Content: "ok from second key"}},
	}
	c := NewClient(providerCfg, impl, "gpt-test", 4096, "sys")

	var deltas []message.StreamDelta
	resp, err := c.CompleteStream(ctx, []message.Message{{Role: "user", Content: "hi"}}, nil, func(delta message.StreamDelta) {
		deltas = append(deltas, delta)
	})
	if err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}
	if resp == nil || resp.Content != "ok from second key" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if refreshHit {
		t.Fatal("refresh endpoint was called for token_invalidated")
	}
	if got := impl.apiKeys; len(got) != 2 || got[0] != "oauth-key-1" || got[1] != "oauth-key-2" {
		t.Fatalf("api keys = %#v, want [oauth-key-1 oauth-key-2]", got)
	}
	if auth["openai"][0].OAuth.Status != config.OAuthStatusInvalidated {
		t.Fatalf("first OAuth status = %q, want invalidated", auth["openai"][0].OAuth.Status)
	}
	healthy, total := providerCfg.HealthyKeyCount()
	if healthy != 1 || total != 1 {
		t.Fatalf("HealthyKeyCount = %d/%d, want 1/1 after invalidating first key", healthy, total)
	}
	foundInvalidatedDelta := false
	foundRetryErrorIdentity := false
	for _, delta := range deltas {
		if delta.Type == message.StreamDeltaKeyInvalidated && delta.AccountID == "acc-1" && delta.Email == "user@example.com" {
			foundInvalidatedDelta = true
		}
		if delta.Type == message.StreamDeltaRetryError && delta.MaskedKey == maskedKey("oauth-key-1") && delta.AccountID == "acc-1" && delta.Email == "user@example.com" {
			foundRetryErrorIdentity = true
		}
	}
	if !foundInvalidatedDelta {
		t.Fatalf("expected key_invalidated delta with account metadata, got %#v", deltas)
	}
	if !foundRetryErrorIdentity {
		t.Fatalf("expected retry_error delta with OAuth identity metadata, got %#v", deltas)
	}
}

func TestMaskedKeyProtectsShortCredentials(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want string
	}{
		{name: "empty", key: "", want: ""},
		{name: "short", key: "short-key", want: "****"},
		{name: "below disclosure threshold", key: "1234567890123456789", want: "****"},
		{name: "at disclosure threshold", key: "12345678901234567890", want: "1234...7890"},
		{name: "long provider key", key: "sk-example-provider-secret-xyz9", want: "sk-e...xyz9"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := maskedKey(tt.key); got != tt.want {
				t.Fatalf("maskedKey(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

func TestKeyLogIDDoesNotExposeCredentialPrefix(t *testing.T) {
	key := "sk-example-provider-secret-xyz9"
	got := keyLogID(key)
	if strings.Contains(got, "sk-e") || strings.Contains(got, key) {
		t.Fatalf("keyLogID(%q) exposed credential prefix: %q", key, got)
	}
	if !strings.Contains(got, "...xyz9/fp=") {
		t.Fatalf("keyLogID(%q) = %q, want suffix and fingerprint", key, got)
	}
	if got2 := keyLogID(key); got2 != got {
		t.Fatalf("keyLogID is unstable: %q then %q", got, got2)
	}
	if other := keyLogID("other-example-provider-secret-xyz9"); other == got {
		t.Fatalf("same-suffix credentials share keyLogID %q", got)
	}
}

func TestClientCompleteStream401OAuthRefreshRotatesToNextKey(t *testing.T) {
	oldAccess := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-1","chatgpt_user_id":"user-1"}`)
	newAccess := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-1","exp":4102444800}`)
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"`+newAccess+`","refresh_token":"new-refresh-token","expires_in":3600}`)
	}))
	defer refreshServer.Close()

	creds := []config.ProviderCredential{
		{OAuth: &config.OAuthCredential{Access: oldAccess, Refresh: "refresh-1", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountUserID: "user-1__acc-1", AccountID: "acc-1"}},
		{APIKey: "key-2"},
	}
	auth := config.AuthConfig{"openai": creds}
	var authMu sync.Mutex
	primaryCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"gpt-test": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, config.ExtractAPIKeys(creds))
	primaryCfg.SetOAuthRefresher(refreshServer.URL, "client-id", "", "", &auth, &authMu, map[string]OAuthKeySetup{creds[0].OAuth.Access: {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Expires: creds[0].OAuth.Expires}}, "")

	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{err: &APIError{StatusCode: 401, Message: "account deactivated"}},
		{resp: &message.Response{Content: "ok from key-2"}},
	}
	c := NewClient(primaryCfg, impl, "gpt-test", 4096, "sys")

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}
	if resp == nil || resp.Content != "ok from key-2" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if len(impl.apiKeys) != 2 {
		t.Fatalf("expected 2 provider calls, got %d", len(impl.apiKeys))
	}
	if impl.apiKeys[0] != oldAccess {
		t.Fatalf("first call key = %q, want old access", impl.apiKeys[0])
	}
	if impl.apiKeys[1] != "key-2" {
		t.Fatalf("second call key = %q, want key-2", impl.apiKeys[1])
	}
}

func TestClientComplete403OAuthRefreshRotatesToNextKey(t *testing.T) {
	oldAccess := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-1","chatgpt_user_id":"user-1"}`)
	newAccess := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-1","exp":4102444800}`)
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"`+newAccess+`","refresh_token":"new-refresh-token","expires_in":3600}`)
	}))
	defer refreshServer.Close()

	creds := []config.ProviderCredential{
		{OAuth: &config.OAuthCredential{Access: oldAccess, Refresh: "refresh-1", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountUserID: "user-1__acc-1", AccountID: "acc-1"}},
		{APIKey: "key-2"},
	}
	auth := config.AuthConfig{"openai": creds}
	var authMu sync.Mutex
	primaryCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"gpt-test": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, config.ExtractAPIKeys(creds))
	primaryCfg.SetOAuthRefresher(refreshServer.URL, "client-id", "", "", &auth, &authMu, map[string]OAuthKeySetup{creds[0].OAuth.Access: {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Expires: creds[0].OAuth.Expires}}, "")

	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{err: &APIError{StatusCode: 403, Message: "forbidden"}},
		{resp: &message.Response{Content: "ok from key-2"}},
	}
	c := NewClient(primaryCfg, impl, "gpt-test", 4096, "sys")

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}
	if resp == nil || resp.Content != "ok from key-2" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if len(impl.apiKeys) != 2 {
		t.Fatalf("expected 2 provider calls, got %d", len(impl.apiKeys))
	}
	if impl.apiKeys[0] != oldAccess {
		t.Fatalf("first call key = %q, want old access", impl.apiKeys[0])
	}
	if impl.apiKeys[1] != "key-2" {
		t.Fatalf("second call key = %q, want key-2", impl.apiKeys[1])
	}
}

func TestClientCompleteStreamDeactivatedOnlyOAuthKeyReturnsNoUsableKeys(t *testing.T) {
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{
		Access:    "deactivated-oauth-key",
		Refresh:   "refresh-token",
		Expires:   time.Now().Add(time.Hour).UnixMilli(),
		AccountID: "acc-deactivated",
		Status:    config.OAuthStatusDeactivated,
	}}}}
	var authMu sync.Mutex
	primaryCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"gpt-test": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"deactivated-oauth-key"})
	primaryCfg.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", "", &auth, &authMu, map[string]OAuthKeySetup{
		"deactivated-oauth-key": {CredentialIndex: 0, AccountID: "acc-deactivated", Expires: auth["openai"][0].OAuth.Expires, Status: config.OAuthStatusDeactivated},
	}, "")

	impl := &recordingProvider{}
	c := NewClient(primaryCfg, impl, "gpt-test", 4096, "sys")

	_, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err == nil {
		t.Fatal("expected NoUsableKeysError")
	}
	if _, ok := errors.AsType[*NoUsableKeysError](err); !ok {
		t.Fatalf("expected NoUsableKeysError, got %T: %v", err, err)
	}
	if len(impl.apiKeys) != 0 {
		t.Fatalf("expected no provider calls, got %d", len(impl.apiKeys))
	}
}

func TestClientCompleteStream401OAuthRefreshThenFallback(t *testing.T) {
	oldAccess := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-1","chatgpt_user_id":"user-1"}`)
	newAccess := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-1","exp":4102444800}`)
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"`+newAccess+`","refresh_token":"new-refresh-token","expires_in":3600}`)
	}))
	defer refreshServer.Close()

	creds := []config.ProviderCredential{
		{OAuth: &config.OAuthCredential{Access: oldAccess, Refresh: "refresh-1", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountUserID: "user-1__acc-1", AccountID: "acc-1"}},
		{APIKey: "key-2"},
	}
	auth := config.AuthConfig{"openai": creds}
	var authMu sync.Mutex
	primaryCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"gpt-test": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, config.ExtractAPIKeys(creds))
	primaryCfg.SetOAuthRefresher(refreshServer.URL, "client-id", "", "", &auth, &authMu, map[string]OAuthKeySetup{creds[0].OAuth.Access: {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Expires: creds[0].OAuth.Expires}}, "")
	fallbackCfg := testProviderConfig("fallback-prov", "fallback-model")

	primaryImpl := &recordingProvider{}
	primaryImpl.calls = []scriptedCall{
		{err: &APIError{StatusCode: 401, Message: "account deactivated"}},
		{err: &APIError{StatusCode: 401, Message: "invalid api key"}},
	}
	fallbackImpl := &recordingProvider{}
	fallbackImpl.calls = []scriptedCall{{resp: &message.Response{Content: "ok from fallback"}}}
	c := NewClient(primaryCfg, primaryImpl, "gpt-test", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   128000,
	}})

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}
	if resp == nil || resp.Content != "ok from fallback" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if len(primaryImpl.apiKeys) != 2 {
		t.Fatalf("expected 2 initial-entry calls, got %d", len(primaryImpl.apiKeys))
	}
	if primaryImpl.apiKeys[0] != oldAccess || primaryImpl.apiKeys[1] != "key-2" {
		t.Fatalf("initial-entry keys = %#v, want [old access key-2]", primaryImpl.apiKeys)
	}
	if len(fallbackImpl.apiKeys) != 1 {
		t.Fatalf("expected 1 fallback call, got %d", len(fallbackImpl.apiKeys))
	}
	st := c.LastCallStatus()
	if !st.FallbackTriggered {
		t.Fatal("expected fallback to trigger")
	}
}
