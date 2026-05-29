package llm

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ratelimit"
)

func testProviderOAuthJWT(payload string) string {
	return "e30." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".sig"
}

func newTestProviderConfig(keys []string) *ProviderConfig {
	cfg := config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{},
	}
	return NewProviderConfig("test", cfg, keys)
}

func TestMutateCredentialInMemoryUsesIndexToDisambiguateDuplicateAccountID(t *testing.T) {
	auth := config.AuthConfig{
		"openai": {
			{OAuth: &config.OAuthCredential{Refresh: "refresh-a", Access: "access-a", Expires: 111, AccountID: "shared-acc"}},
			{OAuth: &config.OAuthCredential{Refresh: "refresh-b", Access: "access-b", Expires: 222, AccountID: "shared-acc"}},
		},
	}
	authMu := &sync.Mutex{}
	r := &OAuthRefresher{providerName: "openai", authConfig: &auth, authConfigMu: authMu}

	credentialIndex := 1
	if err := r.persistCredentialStatus(config.OAuthCredentialMatch{AccountID: "shared-acc", CredentialIndex: &credentialIndex}, config.OAuthStatusExpired); err != nil {
		t.Fatalf("persistCredentialStatus: %v", err)
	}
	if got := auth["openai"][0].OAuth; got == nil || got.Access != "access-a" || got.Status != config.OAuthStatusNormal {
		t.Fatalf("expected first duplicate account_id credential unchanged, got %#v", got)
	}
	if got := auth["openai"][1].OAuth; got == nil || got.Access != "access-b" || got.Status != config.OAuthStatusExpired {
		t.Fatalf("expected second duplicate account_id credential marked expired, got %#v", got)
	}
}

// --- SelectKey / SelectKeyWithContext ---

func TestSelectKey_NoKeys(t *testing.T) {
	p := newTestProviderConfig(nil)
	key, _, err := p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error for no keys: %v", err)
	}
	if key != "" {
		t.Fatalf("expected empty key for no-keys provider, got %q", key)
	}
}

func TestSelectKey_DeactivatedOnlyOAuthKeysReturnsNoUsableKeys(t *testing.T) {
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"oauth-deactivated"})
	p.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", "", &config.AuthConfig{}, &sync.Mutex{}, map[string]OAuthKeySetup{
		"oauth-deactivated": {CredentialIndex: 0, AccountID: "acc-deactivated", Expires: time.Now().Add(time.Hour).UnixMilli(), Status: config.OAuthStatusDeactivated},
	}, "")

	_, _, err := p.SelectKeyWithContext(context.Background())
	if err == nil {
		t.Fatal("expected NoUsableKeysError, got nil")
	}
	var noUsable *NoUsableKeysError
	if !errors.As(err, &noUsable) {
		t.Fatalf("expected NoUsableKeysError, got %T: %v", err, err)
	}
}

func TestTryRefreshOAuthKey_PreservesLatestAuthFileChanges(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.yaml")
	expires := time.Now().Add(time.Hour).UnixMilli()
	oldAccess := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-1"}`)
	newAccess := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-1","exp":4102444800}`)
	if err := os.WriteFile(authPath, []byte(fmt.Sprintf(`openai:
  # primary oauth comment
  - refresh: old-refresh
    access: %s
    expires: %d
    account_id: acc-1
    codex_primary_reset_at: 111
    codex_secondary_reset_at: 222
  # sibling oauth comment
  - refresh: sibling-refresh
    access: %s
    expires: %d
    account_id: acc-2
    email: sibling@example.com
`, oldAccess, expires, testProviderOAuthJWT(`{"chatgpt_account_id":"acc-2"}`), expires)), 0o600); err != nil {
		t.Fatalf("WriteFile(initial auth): %v", err)
	}

	auth, err := config.LoadAuthConfig(authPath)
	if err != nil {
		t.Fatalf("LoadAuthConfig(initial): %v", err)
	}
	var authMu sync.Mutex
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{oldAccess})

	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"`+newAccess+`","refresh_token":"new-refresh","expires_in":3600}`)
	}))
	defer refreshServer.Close()

	p.SetOAuthRefresher(refreshServer.URL, "client-id", authPath, "", &auth, &authMu, map[string]OAuthKeySetup{
		oldAccess: {
			CredentialIndex:       0,
			AccountID:             "acc-1",
			Expires:               auth["openai"][0].OAuth.Expires,
			CodexPrimaryResetAt:   auth["openai"][0].OAuth.CodexPrimaryResetAt,
			CodexSecondaryResetAt: auth["openai"][0].OAuth.CodexSecondaryResetAt,
		},
	}, "")

	if err := os.WriteFile(authPath, []byte(fmt.Sprintf(`openai:
  # primary oauth comment
  - refresh: old-refresh
    access: %s
    expires: %d
    account_id: acc-1
    codex_primary_reset_at: 333
    codex_secondary_reset_at: 444
  # sibling oauth comment
  - refresh: sibling-refresh
    access: %s
    expires: %d
    account_id: acc-2
    email: external@example.com
`, oldAccess, expires, testProviderOAuthJWT(`{"chatgpt_account_id":"acc-2"}`), expires)), 0o600); err != nil {
		t.Fatalf("WriteFile(latest auth): %v", err)
	}

	refreshedKey, ok, err := p.TryRefreshOAuthKey(context.Background(), oldAccess)
	if err != nil {
		t.Fatalf("TryRefreshOAuthKey: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true for OAuth refresh")
	}
	if refreshedKey != newAccess {
		t.Fatalf("refreshedKey = %q, want refreshed access", refreshedKey)
	}

	updated, err := config.LoadAuthConfig(authPath)
	if err != nil {
		t.Fatalf("LoadAuthConfig(updated): %v", err)
	}
	if got := updated["openai"][0].OAuth; got == nil || got.Access != newAccess || got.Refresh != "new-refresh" {
		t.Fatalf("expected refreshed primary oauth credential, got %#v", got)
	}
	if got := updated["openai"][0].OAuth; got.CodexPrimaryResetAt != 333 || got.CodexSecondaryResetAt != 444 {
		t.Fatalf("expected refreshed oauth credential to preserve codex reset hints, got %#v", got)
	}
	if got := updated["openai"][1].OAuth; got == nil || got.Email != "external@example.com" || got.Status != config.OAuthStatusNormal {
		t.Fatalf("expected sibling oauth credential to keep latest auth.yaml fields without status, got %#v", got)
	}
	if got := auth["openai"][1].OAuth; got == nil || got.Email != "external@example.com" || got.Status != config.OAuthStatusNormal {
		t.Fatalf("expected in-memory auth cache to refresh latest auth.yaml fields without status, got %#v", got)
	}

	data, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	for _, want := range []string{"# primary oauth comment", "# sibling oauth comment", "codex_primary_reset_at: 333", "codex_secondary_reset_at: 444"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("expected auth.yaml to contain %q, got:\n%s", want, string(data))
		}
	}
	if strings.Contains(string(data), "status:") {
		t.Fatalf("auth.yaml should not contain status, got:\n%s", string(data))
	}
}

func TestSetOAuthRefresherRefreshOnlyBindsByKeySlot(t *testing.T) {
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses}, []string{"", ""})
	auth := config.AuthConfig{}
	var authMu sync.Mutex
	p.SetOAuthRefresher("https://example.invalid/token", "client-id", "", "", &auth, &authMu, map[string]OAuthKeySetup{
		"key_slot:1": {CredentialIndex: 1, AccountID: "acc-refresh", Status: config.OAuthStatusNormal},
	}, "")

	if p.keyStates[0].OAuthInfo != nil {
		t.Fatalf("explicit empty key slot should not receive OAuth info: %#v", p.keyStates[0].OAuthInfo)
	}
	if p.keyStates[1].OAuthInfo == nil || p.keyStates[1].OAuthInfo.AccountID != "acc-refresh" {
		t.Fatalf("refresh-only OAuth slot not bound by key slot: %#v", p.keyStates[1].OAuthInfo)
	}
}

func TestSelectKeyUsesExistingOAuthAccessTokenWithoutPreRefresh(t *testing.T) {
	expires := time.Now().Add(-time.Minute).UnixMilli()
	access := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-1"}`)
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{
		Access:    access,
		Refresh:   "refresh-token",
		Expires:   expires,
		AccountID: "acc-1",
	}}}}
	var authMu sync.Mutex
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("refresh endpoint should not be called before trying existing access token")
	}))
	defer refreshServer.Close()

	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{access})
	p.SetOAuthRefresher(refreshServer.URL, "client-id", "", "", &auth, &authMu, map[string]OAuthKeySetup{
		access: {CredentialIndex: 0, AccountID: "acc-1", Expires: expires},
	}, "")

	key, _, err := p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("SelectKeyWithContext: %v", err)
	}
	if key != access {
		t.Fatalf("selected key = %q, want access token", key)
	}
	if got := auth["openai"][0].OAuth.Status; got != config.OAuthStatusNormal {
		t.Fatalf("OAuth status = %q, want normal", got)
	}
}

func TestSelectKeyRefreshFailureDoesNotMarkExpiredFromLocalExpires(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.yaml")
	expires := time.Now().Add(-time.Minute).UnixMilli()
	access := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-1"}`)
	if err := os.WriteFile(authPath, []byte(fmt.Sprintf(`openai:
  - refresh: old-refresh
    access: %s
    expires: %d
    account_id: acc-1
`, access, expires)), 0o600); err != nil {
		t.Fatalf("WriteFile(auth): %v", err)
	}
	auth, err := config.LoadAuthConfig(authPath)
	if err != nil {
		t.Fatalf("LoadAuthConfig: %v", err)
	}
	var authMu sync.Mutex
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{access})
	refreshHit := false
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshHit = true
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer refreshServer.Close()
	p.SetOAuthRefresher(refreshServer.URL, "client-id", authPath, "", &auth, &authMu, map[string]OAuthKeySetup{
		access: {CredentialIndex: 0, AccountID: "acc-1", Expires: expires},
	}, "")

	key, _, err := p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("SelectKeyWithContext: %v", err)
	}
	if key != access {
		t.Fatalf("selected key = %q, want original access", key)
	}
	if refreshHit {
		t.Fatal("refresh endpoint should not be called before trying existing access token")
	}
	if got := auth["openai"][0].OAuth; got == nil || got.Status != config.OAuthStatusNormal {
		t.Fatalf("expected in-memory auth OAuth credential status=normal, got %#v", got)
	}
}

func TestSelectKeyMissingRefreshTokenStillUsesExistingAccessToken(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.yaml")
	expires := time.Now().Add(-time.Minute).UnixMilli()
	access := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-1"}`)
	if err := os.WriteFile(authPath, []byte(fmt.Sprintf(`openai:
  - access: %s
    expires: %d
    account_id: acc-1
`, access, expires)), 0o600); err != nil {
		t.Fatalf("WriteFile(auth): %v", err)
	}
	auth, err := config.LoadAuthConfig(authPath)
	if err != nil {
		t.Fatalf("LoadAuthConfig: %v", err)
	}
	var authMu sync.Mutex
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{access})
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("refresh endpoint should not be called before trying existing access token")
	}))
	defer refreshServer.Close()
	p.SetOAuthRefresher(refreshServer.URL, "client-id", authPath, "", &auth, &authMu, map[string]OAuthKeySetup{
		access: {CredentialIndex: 0, AccountID: "acc-1", Expires: expires},
	}, "")

	key, _, err := p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("SelectKeyWithContext: %v", err)
	}
	if key != access {
		t.Fatalf("selected key = %q, want existing access token", key)
	}
	if got := auth["openai"][0].OAuth; got == nil || got.Status != config.OAuthStatusNormal {
		t.Fatalf("expected in-memory auth OAuth credential status=normal, got %#v", got)
	}
}

func TestSelectKeyDoesNotSkipExpiredOAuthTokenBeforeProbe(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.yaml")
	expired := time.Now().Add(-time.Minute).UnixMilli()
	valid := time.Now().Add(time.Hour).UnixMilli()
	expiredAccess := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-1"}`)
	validAccess := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-2","exp":4102444800}`)
	if err := os.WriteFile(authPath, []byte(fmt.Sprintf(`openai:
  - refresh: old-refresh
    access: %s
    expires: %d
    account_id: acc-1
  - refresh: valid-refresh
    access: %s
    expires: %d
    account_id: acc-2
`, expiredAccess, expired, validAccess, valid)), 0o600); err != nil {
		t.Fatalf("WriteFile(auth): %v", err)
	}
	auth, err := config.LoadAuthConfig(authPath)
	if err != nil {
		t.Fatalf("LoadAuthConfig: %v", err)
	}
	var authMu sync.Mutex
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex, KeyOrder: config.KeyOrderSequential}, []string{expiredAccess, validAccess})
	refreshHit := false
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshHit = true
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer refreshServer.Close()
	p.SetOAuthRefresher(refreshServer.URL, "client-id", authPath, "", &auth, &authMu, map[string]OAuthKeySetup{
		expiredAccess: {CredentialIndex: 0, AccountID: "acc-1", Expires: expired},
		validAccess:   {CredentialIndex: 1, AccountID: "acc-2", Expires: valid},
	}, "")

	key, _, err := p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("SelectKeyWithContext: %v", err)
	}
	if key != expiredAccess {
		t.Fatalf("selected key = %q, want first OAuth token before any auth probe", key)
	}
	if refreshHit {
		t.Fatal("refresh endpoint should not be called during key selection")
	}
	if got := auth["openai"][0].OAuth; got == nil || got.Status != config.OAuthStatusNormal {
		t.Fatalf("expected first OAuth credential status=normal, got %#v", got)
	}
	if got := auth["openai"][1].OAuth; got == nil || got.Status != config.OAuthStatusNormal {
		t.Fatalf("expected second OAuth credential to remain normal, got %#v", got)
	}
}

func TestSelectKey_SingleKey(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a"})
	key, _, err := p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "key-a" {
		t.Fatalf("expected key-a, got %s", key)
	}
	// Calling again should still return the same key (only one available).
	key, _, err = p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "key-a" {
		t.Fatalf("expected key-a, got %s", key)
	}
}

func TestSelectKey_MultipleKeys_Sticky(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a", "key-b", "key-c"})

	// Default strategy is sticky: always returns the same key until it cools down.
	for i := 0; i < 4; i++ {
		key, _, err := p.SelectKeyWithContext(context.Background())
		if err != nil {
			t.Fatalf("call %d: unexpected error: %v", i, err)
		}
		if key != "key-a" {
			t.Fatalf("call %d: expected key-a (sticky), got %s", i, key)
		}
	}
}

func TestNewProviderConfig_CodexDefaultsToSmartKeyOrder(t *testing.T) {
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"k1", "k2"})
	if p.keyOrder != config.KeyOrderSmart {
		t.Fatalf("keyOrder = %q, want %q", p.keyOrder, config.KeyOrderSmart)
	}
}

func TestNewProviderConfig_NormalizesUnknownKeySelectionDefensively(t *testing.T) {
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, KeyRotation: "per-call", KeyOrder: "round_robin"}, []string{"k1", "k2"})
	if p.keyRotation != config.KeyRotationOnFailure {
		t.Fatalf("keyRotation = %q, want %q", p.keyRotation, config.KeyRotationOnFailure)
	}
	if p.keyOrder != config.KeyOrderSequential {
		t.Fatalf("keyOrder = %q, want %q", p.keyOrder, config.KeyOrderSequential)
	}
}

func TestNewProviderConfig_PreservesValidPerRequestRandom(t *testing.T) {
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, KeyRotation: config.KeyRotationPerRequest, KeyOrder: config.KeyOrderRandom}, []string{"k1", "k2"})
	if p.keyRotation != config.KeyRotationPerRequest {
		t.Fatalf("keyRotation = %q, want %q", p.keyRotation, config.KeyRotationPerRequest)
	}
	if p.keyOrder != config.KeyOrderRandom {
		t.Fatalf("keyOrder = %q, want %q", p.keyOrder, config.KeyOrderRandom)
	}
}

func TestSelectKey_CodexSmartTreatsSnapshotAsHeadroomNotSoftCooldown(t *testing.T) {
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"key-a", "key-b"})
	reset := time.Now().Add(time.Hour)
	p.UpdateKeySnapshot("key-a", &ratelimit.KeyRateLimitSnapshot{
		Primary: &ratelimit.RateLimitWindow{UsedPct: 20, ResetsAt: reset},
	})

	key, _, err := p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("SelectKeyWithContext: %v", err)
	}
	if key != "key-a" {
		t.Fatalf("expected snapshot-backed key-a, got %s", key)
	}
}

func TestSelectKey_CodexSmartPrefersSoonResetUsableHeadroom(t *testing.T) {
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"key-a", "key-b"})
	now := time.Now()
	p.UpdateKeySnapshot("key-a", &ratelimit.KeyRateLimitSnapshot{
		Primary: &ratelimit.RateLimitWindow{UsedPct: 20, ResetsAt: now.Add(6 * time.Hour)},
	})
	p.UpdateKeySnapshot("key-b", &ratelimit.KeyRateLimitSnapshot{
		Primary: &ratelimit.RateLimitWindow{UsedPct: 90, ResetsAt: now.Add(10 * time.Minute)},
	})

	key, _, err := p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("SelectKeyWithContext: %v", err)
	}
	if key != "key-b" {
		t.Fatalf("selected key = %q, want key-b with soon-reset usable headroom", key)
	}
}

func TestSelectKey_CodexSmartDoesNotDemoteNinetyNinePercentUsed(t *testing.T) {
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"key-a", "key-b"})
	now := time.Now()
	p.UpdateKeySnapshot("key-a", &ratelimit.KeyRateLimitSnapshot{
		Primary: &ratelimit.RateLimitWindow{UsedPct: 99, WindowMinutes: 300, ResetsAt: now.Add(10 * time.Minute)},
	})
	p.UpdateKeySnapshot("key-b", &ratelimit.KeyRateLimitSnapshot{
		Primary: &ratelimit.RateLimitWindow{UsedPct: 20, WindowMinutes: 300, ResetsAt: now.Add(6 * time.Hour)},
	})

	key, _, err := p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("SelectKeyWithContext: %v", err)
	}
	if key != "key-a" {
		t.Fatalf("selected key = %q, want key-a because 99%% used still has usable quota and resets sooner", key)
	}
}

func TestSelectKey_CodexSmartDemotesOneHundredPercentUsed(t *testing.T) {
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"key-a", "key-b"})
	now := time.Now()
	p.UpdateKeySnapshot("key-a", &ratelimit.KeyRateLimitSnapshot{
		Primary: &ratelimit.RateLimitWindow{UsedPct: 100, WindowMinutes: 300, ResetsAt: now.Add(10 * time.Minute)},
	})
	p.UpdateKeySnapshot("key-b", &ratelimit.KeyRateLimitSnapshot{
		Primary: &ratelimit.RateLimitWindow{UsedPct: 20, WindowMinutes: 300, ResetsAt: now.Add(6 * time.Hour)},
	})

	key, _, err := p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("SelectKeyWithContext: %v", err)
	}
	if key != "key-b" {
		t.Fatalf("selected key = %q, want key-b because 100%% used key-a is tried last", key)
	}
}

func TestSelectKey_CodexSmartComparesPrimaryBeforeSecondaryWindow(t *testing.T) {
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"key-a", "key-b"})
	now := time.Now()
	p.UpdateKeySnapshot("key-a", &ratelimit.KeyRateLimitSnapshot{
		Primary:   &ratelimit.RateLimitWindow{UsedPct: 10, WindowMinutes: 300, ResetsAt: now.Add(6 * time.Hour)},
		Secondary: &ratelimit.RateLimitWindow{UsedPct: 10, WindowMinutes: 10080, ResetsAt: now.Add(10 * time.Minute)},
	})
	p.UpdateKeySnapshot("key-b", &ratelimit.KeyRateLimitSnapshot{
		Primary:   &ratelimit.RateLimitWindow{UsedPct: 90, WindowMinutes: 300, ResetsAt: now.Add(10 * time.Minute)},
		Secondary: &ratelimit.RateLimitWindow{UsedPct: 90, WindowMinutes: 10080, ResetsAt: now.Add(6 * time.Hour)},
	})

	key, _, err := p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("SelectKeyWithContext: %v", err)
	}
	if key != "key-b" {
		t.Fatalf("selected key = %q, want key-b because the primary 5h window resets sooner", key)
	}
}

func TestSelectKey_UpdatesInlineDisplayToSelectedKeySnapshot(t *testing.T) {
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"key-a", "key-b"})
	snapA := &ratelimit.KeyRateLimitSnapshot{
		Provider: "openai",
		Primary:  &ratelimit.RateLimitWindow{UsedPct: 20},
		Source:   ratelimit.SnapshotSourceInlineKey,
	}
	p.UpdateKeySnapshot("key-a", snapA)

	key, _, err := p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("SelectKeyWithContext: %v", err)
	}
	if key != "key-a" {
		t.Fatalf("selected key = %q, want key-a", key)
	}
	if got := p.CurrentInlineRateLimitSnapshot(); got != snapA {
		t.Fatalf("CurrentInlineRateLimitSnapshot() = %#v, want key-a snapshot %#v", got, snapA)
	}

	p.MarkCooldown("key-a", time.Minute)
	key, _, err = p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("SelectKeyWithContext after cooldown: %v", err)
	}
	if key != "key-b" {
		t.Fatalf("selected key = %q, want key-b", key)
	}
	if got := p.CurrentInlineRateLimitSnapshot(); got != nil {
		t.Fatalf("CurrentInlineRateLimitSnapshot() = %#v, want nil for selected key without snapshot", got)
	}

	snapB := &ratelimit.KeyRateLimitSnapshot{
		Provider: "openai",
		Primary:  &ratelimit.RateLimitWindow{UsedPct: 40},
		Source:   ratelimit.SnapshotSourceInlineKey,
	}
	p.UpdateKeySnapshot("key-b", snapB)
	if got := p.CurrentInlineRateLimitSnapshot(); got != snapB {
		t.Fatalf("CurrentInlineRateLimitSnapshot() = %#v, want key-b snapshot %#v", got, snapB)
	}
}

func TestSelectKey_CodexSmartPrefersNonSoftCooledKey(t *testing.T) {
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"key-a", "key-b"})
	p.mu.Lock()
	p.keyStates[0].OAuthInfo = &OAuthKeyInfo{AccountID: "acc-a", CodexPrimaryResetAt: time.Now().Add(2 * time.Hour).UnixMilli()}
	p.keyStates[0].SoftCooldownUntil = time.Now().Add(2 * time.Hour)
	p.keyStates[1].OAuthInfo = &OAuthKeyInfo{AccountID: "acc-b"}
	p.mu.Unlock()
	key, _, err := p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("SelectKeyWithContext: %v", err)
	}
	if key != "key-b" {
		t.Fatalf("expected non-soft-cooled key-b, got %s", key)
	}
}

// --- Cooldown ---

func TestSelectKey_CooldownSkipsKey(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a", "key-b"})

	// Use key-a first so it gets a LastUsed timestamp.
	key, _, _ := p.SelectKeyWithContext(context.Background())
	if key != "key-a" {
		t.Fatalf("expected key-a, got %s", key)
	}

	// Put key-b in cooldown for 1 minute.
	p.MarkCooldown("key-b", 1*time.Minute)

	// Next select should skip key-b (in cooldown) and pick key-a again.
	key, _, _ = p.SelectKeyWithContext(context.Background())
	if key != "key-a" {
		t.Fatalf("expected key-a (key-b in cooldown), got %s", key)
	}
}

func TestSelectKey_AllKeysCooldown(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a", "key-b"})

	// Put both keys in cooldown.
	p.MarkCooldown("key-a", 10*time.Second)
	p.MarkCooldown("key-b", 20*time.Second)

	_, _, err := p.SelectKeyWithContext(context.Background())
	if err == nil {
		t.Fatal("expected error when all keys are cooling")
	}

	var cooling *AllKeysCoolingError
	if !errors.As(err, &cooling) {
		t.Fatalf("expected AllKeysCoolingError, got %T: %v", err, err)
	}

	// RetryAfter should be close to 10s (the earliest cooldown end).
	if cooling.RetryAfter < 5*time.Second || cooling.RetryAfter > 15*time.Second {
		t.Fatalf("expected RetryAfter ~10s, got %v", cooling.RetryAfter)
	}
}

func TestSelectKey_CooldownExpires(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a", "key-b"})

	// Put key-a in cooldown that already expired.
	p.mu.Lock()
	for _, ks := range p.keyStates {
		if ks.Key == "key-a" {
			ks.CooldownEnd = time.Now().Add(-1 * time.Second) // expired
		}
	}
	p.mu.Unlock()

	key, _, err := p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// key-a's cooldown has expired, so it should be selectable.
	// Both keys are at zero LastUsed, but key-a's cooldown just expired,
	// so key-a is still eligible.
	if key != "key-a" && key != "key-b" {
		t.Fatalf("expected key-a or key-b, got %s", key)
	}
}

func TestMarkCooldown_NonExistentKey(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a"})
	// Should not panic.
	p.MarkCooldown("nonexistent", 10*time.Second)
}

func TestMarkTemporaryUnavailable_skipsWhenAlreadyCooling(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a", "key-b"})
	p.MarkCooldown("key-a", 10*time.Minute)
	availBefore, _ := p.AvailableKeyCount()
	p.MarkTemporaryUnavailable("key-a", time.Second)
	availAfter, _ := p.AvailableKeyCount()
	if availBefore != availAfter {
		t.Fatalf("available %d -> %d, want unchanged when API cooldown active", availBefore, availAfter)
	}
}

func TestMarkTemporaryUnavailable_blocksUntilExpiry(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a", "key-b"})
	p.MarkTemporaryUnavailable("key-a", 30*time.Second)
	avail, total := p.AvailableKeyCount()
	if total != 2 || avail != 1 {
		t.Fatalf("AvailableKeyCount = %d/%d, want 1/2", avail, total)
	}
	// key-a is in cooldown (not yet healthy), key-b is healthy
	healthy, htotal := p.HealthyKeyCount()
	if htotal != 2 || healthy != 1 {
		t.Fatalf("HealthyKeyCount = %d/%d, want 1/2 (key-b healthy, key-a cooling)", healthy, htotal)
	}
}

func TestHealthyKeyCount_recoveringKeyNotCounted(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a", "key-b"})
	p.MarkTemporaryUnavailable("key-a", 30*time.Second)
	// Expire the cooldown so key-a is selectable but still Recovering
	p.mu.Lock()
	for _, ks := range p.keyStates {
		if ks.Key == "key-a" {
			ks.CooldownEnd = time.Now().Add(-time.Second)
		}
	}
	p.mu.Unlock()
	// AvailableKeyCount sees both keys as selectable
	avail, total := p.AvailableKeyCount()
	if total != 2 || avail != 2 {
		t.Fatalf("AvailableKeyCount = %d/%d, want 2/2 once temporary cooldown expired", avail, total)
	}
	// HealthyKeyCount excludes recovering key-a
	healthy, htotal := p.HealthyKeyCount()
	if htotal != 2 || healthy != 1 {
		t.Fatalf("HealthyKeyCount = %d/%d, want 1/2 (key-a recovering, key-b healthy)", healthy, htotal)
	}
}

func TestAvailableKeyCount_codexSnapshotAt100DoesNotReduceAvailability(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a", "key-b"})
	reset := time.Now().Add(time.Hour)
	p.UpdateKeySnapshot("key-a", &ratelimit.KeyRateLimitSnapshot{
		Primary: &ratelimit.RateLimitWindow{UsedPct: 100, ResetsAt: reset},
	})
	avail, total := p.AvailableKeyCount()
	if total != 2 {
		t.Fatalf("total = %d, want 2", total)
	}
	if avail != 2 {
		t.Fatalf("available = %d, want 2 (100%% in headers does not block selection)", avail)
	}
}

func TestSelectKey_Sticky_keepsPinnedKeyDespiteCodex100PercentSnapshot(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a", "key-b"})
	reset := time.Now().Add(time.Hour)
	p.UpdateKeySnapshot("key-a", &ratelimit.KeyRateLimitSnapshot{
		Primary: &ratelimit.RateLimitWindow{UsedPct: 100, ResetsAt: reset},
	})

	key, _, err := p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "key-a" {
		t.Fatalf("expected key-a (sticky; header quota is informational only), got %s", key)
	}
}

func TestKeyPoolNextTransition_zeroWhenSingleKey(t *testing.T) {
	p := newTestProviderConfig([]string{"only"})
	p.MarkCooldown("only", time.Minute)
	if d := p.KeyPoolNextTransition(); d != 0 {
		t.Fatalf("KeyPoolNextTransition = %v, want 0 for single-key pool", d)
	}
}

func TestMarkQuotaExhaustedUntil_blocksSelectionUntilReset(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a", "key-b"})
	p.MarkQuotaExhaustedUntil("key-a", time.Now().Add(2*time.Hour))
	avail, total := p.AvailableKeyCount()
	if total != 2 || avail != 1 {
		t.Fatalf("AvailableKeyCount = %d/%d, want 1/2 with one exhausted key", avail, total)
	}
	// Exhausted key is not selectable, so healthy = available here
	healthy, htotal := p.HealthyKeyCount()
	if htotal != 2 || healthy != 1 {
		t.Fatalf("HealthyKeyCount = %d/%d, want 1/2 with one exhausted key", healthy, htotal)
	}
	key, _, err := p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "key-b" {
		t.Fatalf("selected key = %q, want key-b when key-a exhausted", key)
	}
}

func TestMarkQuotaExhaustedUntil_blocksAllDuplicateKeySlots(t *testing.T) {
	p := newTestProviderConfig([]string{"shared-key", "other-key", "shared-key"})
	p.mu.Lock()
	p.stickyIdx = 2
	p.mu.Unlock()
	key, _, err := p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("initial SelectKeyWithContext: %v", err)
	}
	if key != "shared-key" {
		t.Fatalf("initial selected key = %q, want shared-key", key)
	}

	p.MarkQuotaExhaustedUntil("shared-key", time.Now().Add(2*time.Hour))
	avail, total := p.AvailableKeyCount()
	if total != 3 || avail != 1 {
		t.Fatalf("AvailableKeyCount = %d/%d, want 1/3 with both duplicate shared-key slots exhausted", avail, total)
	}
	key, _, err = p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("second SelectKeyWithContext: %v", err)
	}
	if key != "other-key" {
		t.Fatalf("selected key = %q, want other-key after duplicate shared-key slots exhausted", key)
	}
}

func TestMarkKeySuccess_ClearsPersistedCodexResetHints(t *testing.T) {
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{
		Access:                "oauth-key",
		Refresh:               "refresh-token",
		Expires:               time.Now().Add(time.Hour).UnixMilli(),
		AccountID:             "acc-1",
		CodexPrimaryResetAt:   time.Now().Add(2 * time.Hour).UnixMilli(),
		CodexSecondaryResetAt: time.Now().Add(6 * time.Hour).UnixMilli(),
	}}}}
	var authMu sync.Mutex
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"oauth-key"})
	p.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", "", &auth, &authMu, map[string]OAuthKeySetup{
		"oauth-key": {
			CredentialIndex:       0,
			AccountID:             "acc-1",
			Expires:               auth["openai"][0].OAuth.Expires,
			CodexPrimaryResetAt:   auth["openai"][0].OAuth.CodexPrimaryResetAt,
			CodexSecondaryResetAt: auth["openai"][0].OAuth.CodexSecondaryResetAt,
		},
	}, "")
	p.MarkKeySuccess("oauth-key")
	if got := auth["openai"][0].OAuth; got.CodexPrimaryResetAt != 0 || got.CodexSecondaryResetAt != 0 {
		t.Fatalf("expected MarkKeySuccess to clear persisted codex hints, got %#v", got)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if info := p.keyStates[0].OAuthInfo; info == nil || info.CodexPrimaryResetAt != 0 || info.CodexSecondaryResetAt != 0 {
		t.Fatalf("expected runtime OAuth info codex hints to be cleared, got %#v", info)
	}
}

func TestHealthyKeyCount_exhaustedKeyTransitionsToRecoveringOnSelect(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a", "key-b"})
	// Mark key-a exhausted but already expired (reset in the past)
	p.mu.Lock()
	for _, ks := range p.keyStates {
		if ks.Key == "key-a" {
			ks.ExhaustedUntil = time.Now().Add(-time.Second)
		}
	}
	p.mu.Unlock()
	// Before selection: key-a ExhaustedUntil expired, Recovering=false → counted as healthy
	healthy, total := p.HealthyKeyCount()
	if total != 2 || healthy != 2 {
		t.Fatalf("HealthyKeyCount before select = %d/%d, want 2/2", healthy, total)
	}
	// Select key-a — SelectKeyWithContext should set Recovering=true
	p.mu.Lock()
	p.stickyIdx = 0 // force selection of key-a
	p.mu.Unlock()
	_, _, err := p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// After selection: key-a is Recovering, only key-b is healthy
	healthy, total = p.HealthyKeyCount()
	if total != 2 || healthy != 1 {
		t.Fatalf("HealthyKeyCount after select = %d/%d, want 1/2 (key-a recovering)", healthy, total)
	}
}

func TestKeyPoolNextTransition_considersExhaustedUntil(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a", "key-b"})
	p.MarkQuotaExhaustedUntil("key-a", time.Now().Add(90*time.Second))
	d := p.KeyPoolNextTransition()
	if d < 60*time.Second || d > 95*time.Second {
		t.Fatalf("KeyPoolNextTransition = %v, want ~90s from exhausted reset", d)
	}
}

// --- MarkDeactivated ---

func TestMarkDeactivated_removesKeyFromTotal(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a", "key-b"})
	p.MarkDeactivated("key-a")

	// total should exclude deactivated key
	avail, total := p.AvailableKeyCount()
	if total != 1 || avail != 1 {
		t.Fatalf("AvailableKeyCount = %d/%d, want 1/1 after deactivating key-a", avail, total)
	}
	healthy, htotal := p.HealthyKeyCount()
	if htotal != 1 || healthy != 1 {
		t.Fatalf("HealthyKeyCount = %d/%d, want 1/1 after deactivating key-a", healthy, htotal)
	}
	if cnt := p.KeyCount(); cnt != 1 {
		t.Fatalf("KeyCount = %d, want 1 after deactivating key-a", cnt)
	}
}

func TestMarkDeactivated_keyNotSelected(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a", "key-b"})
	p.MarkDeactivated("key-a")

	for i := 0; i < 5; i++ {
		key, _, err := p.SelectKeyWithContext(context.Background())
		if err != nil {
			t.Fatalf("SelectKeyWithContext: %v", err)
		}
		if key == "key-a" {
			t.Fatal("deactivated key-a was selected")
		}
	}
}

func TestMarkDeactivated_allKeysDeactivated(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a", "key-b"})
	p.MarkDeactivated("key-a")
	p.MarkDeactivated("key-b")

	if cnt := p.KeyCount(); cnt != 0 {
		t.Fatalf("KeyCount = %d, want 0 after deactivating all keys", cnt)
	}
	avail, total := p.AvailableKeyCount()
	if total != 0 || avail != 0 {
		t.Fatalf("AvailableKeyCount = %d/%d, want 0/0", avail, total)
	}
}

func TestMarkDeactivated_nonExistentKeyNoOp(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a"})
	// Should not panic.
	p.MarkDeactivated("nonexistent")
	if cnt := p.KeyCount(); cnt != 1 {
		t.Fatalf("KeyCount = %d, want 1 after no-op deactivate", cnt)
	}
}

// --- Context cancellation ---

func TestSelectKeyWithContext_Cancelled(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a"})
	// Set rate limiter with very low rate to force waiting.
	p.SetRateLimiter(1) // 1 rpm = very slow

	// Use the one allowed burst token.
	_, _, err := p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("first call should succeed: %v", err)
	}

	// Now cancel context before the next token is available.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err = p.SelectKeyWithContext(ctx)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

// --- Rate limiter ---

func TestSetRateLimiter(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a"})

	// No limiter by default.
	if p.limiter != nil {
		t.Fatal("expected no limiter by default")
	}

	// Set a limiter.
	p.SetRateLimiter(60)
	if p.limiter == nil {
		t.Fatal("expected limiter to be set")
	}

	// Disable.
	p.SetRateLimiter(0)
	if p.limiter != nil {
		t.Fatal("expected limiter to be nil after disabling")
	}
}

// --- Concurrency ---

func TestSelectKey_Concurrent(t *testing.T) {
	keys := []string{"key-a", "key-b", "key-c"}
	p := newTestProviderConfig(keys)

	var wg sync.WaitGroup
	results := make(chan string, 100)

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			key, _, err := p.SelectKeyWithContext(context.Background())
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			results <- key
		}()
	}

	wg.Wait()
	close(results)

	// Verify all returned keys are valid.
	validKeys := map[string]bool{"key-a": true, "key-b": true, "key-c": true}
	for key := range results {
		if !validKeys[key] {
			t.Errorf("invalid key: %s", key)
		}
	}
}

// --- GetRetryDelay ---

func TestGetRetryDelay(t *testing.T) {
	p := newTestProviderConfig(nil)

	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 0},
		{1, 1 * time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{4, 8 * time.Second},
		{5, 16 * time.Second},
		{6, 32 * time.Second},
		{7, 60 * time.Second},
		{35, 60 * time.Second},
	}

	for _, tt := range tests {
		got := p.GetRetryDelay(tt.attempt)
		if got != tt.want {
			t.Errorf("GetRetryDelay(%d) = %v, want %v", tt.attempt, got, tt.want)
		}
	}
}

func TestMarkCooldownSaturatesWithoutOverflow(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a"})
	for i := 0; i < 80; i++ {
		p.MarkCooldown("key-a", time.Second)
	}

	p.mu.Lock()
	cooldownEnd := p.keyStates[0].CooldownEnd
	p.mu.Unlock()

	until := time.Until(cooldownEnd)
	if until <= 0 {
		t.Fatalf("time.Until(CooldownEnd) = %v, want positive cooldown", until)
	}
	if until > time.Minute+2*time.Second {
		t.Fatalf("time.Until(CooldownEnd) = %v, want capped near 1m", until)
	}
}

// --- NewProviderConfig ---

func TestNewProviderConfig_NoDefaultURLForProviderTypes(t *testing.T) {
	tests := []string{
		config.ProviderTypeChatCompletions,
		config.ProviderTypeMessages,
		config.ProviderTypeGenerateContent,
	}

	for _, providerType := range tests {
		cfg := config.ProviderConfig{Type: providerType}
		p := NewProviderConfig("test", cfg, []string{"key"})
		if p.APIURL() != "" {
			t.Errorf("type=%q: got URL %q, want empty", providerType, p.APIURL())
		}
	}
}

func TestNewProviderConfig_CustomURL(t *testing.T) {
	cfg := config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		APIURL: "https://custom.api.example.com/v1/chat",
	}
	p := NewProviderConfig("test", cfg, []string{"key"})
	if p.APIURL() != "https://custom.api.example.com/v1/chat" {
		t.Errorf("expected custom URL, got %s", p.APIURL())
	}
}

func TestNewProviderConfig_OpenAICodexPreset_DefaultURL(t *testing.T) {
	cfg, _, err := config.NormalizeOpenAICodexProvider(config.ProviderConfig{
		Type:   config.ProviderTypeResponses,
		Preset: config.ProviderPresetCodex,
	}, false)
	if err != nil {
		t.Fatalf("NormalizeOpenAICodexProvider: %v", err)
	}
	p := NewProviderConfig("test", cfg, []string{"key"})
	if p.APIURL() != config.OpenAICodexResponsesURL {
		t.Fatalf("expected OpenAI Codex OAuth default URL %q, got %q", config.OpenAICodexResponsesURL, p.APIURL())
	}
}

func TestNewProviderConfig_KeyStatesInitialized(t *testing.T) {
	keys := []string{"a", "b", "c"}
	p := NewProviderConfig("test", config.ProviderConfig{Type: config.ProviderTypeChatCompletions}, keys)

	if len(p.keyStates) != 3 {
		t.Fatalf("expected 3 key states, got %d", len(p.keyStates))
	}
	for i, ks := range p.keyStates {
		if ks.Key != keys[i] {
			t.Errorf("keyStates[%d].Key = %q, want %q", i, ks.Key, keys[i])
		}
		if !ks.LastUsed.IsZero() {
			t.Errorf("keyStates[%d].LastUsed should be zero, got %v", i, ks.LastUsed)
		}
		if !ks.CooldownEnd.IsZero() {
			t.Errorf("keyStates[%d].CooldownEnd should be zero, got %v", i, ks.CooldownEnd)
		}
	}
}

// --- Accessors ---

func TestProviderConfig_Accessors(t *testing.T) {
	cfg := config.ProviderConfig{
		Type:   config.ProviderTypeMessages,
		APIURL: "https://test.example.com",
		Models: map[string]config.ModelConfig{
			"model-1": {Name: "Model One", Limit: config.ModelLimit{Context: 100000, Output: 4096}},
		},
	}
	p := NewProviderConfig("my-provider", cfg, []string{"key"})

	if p.Name() != "my-provider" {
		t.Errorf("Name() = %q, want %q", p.Name(), "my-provider")
	}
	if p.Type() != config.ProviderTypeMessages {
		t.Errorf("Type() = %q, want %q", p.Type(), config.ProviderTypeMessages)
	}

	m, ok := p.GetModel("model-1")
	if !ok {
		t.Fatal("GetModel(model-1) should return true")
	}
	if m.Limit.Context != 100000 {
		t.Errorf("model context = %d, want %d", m.Limit.Context, 100000)
	}

	_, ok = p.GetModel("nonexistent")
	if ok {
		t.Fatal("GetModel(nonexistent) should return false")
	}
}

func TestProviderConfig_ThinkingToolcallCompat_ModelOnly(t *testing.T) {
	cfg := config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"m1": {
				Compat: &config.ModelCompatConfig{
					ThinkingToolcall: &config.ThinkingToolcallCompatConfig{
						Enabled: new(true),
					},
				},
			},
		},
	}
	p := NewProviderConfig("test", cfg, []string{"k"})
	got := p.ThinkingToolcallCompat("m1")
	if got == nil || !got.EnabledValue() {
		t.Fatalf("expected model compat enabled=true, got %#v", got)
	}
}

func TestProviderConfig_ThinkingToolcallCompat_ProviderDefault(t *testing.T) {
	cfg := config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Compat: &config.ProviderCompatConfig{
			ThinkingToolcall: &config.ThinkingToolcallCompatConfig{
				Enabled: new(true),
			},
		},
		Models: map[string]config.ModelConfig{
			"m1": {},
		},
	}
	p := NewProviderConfig("test", cfg, []string{"k"})
	got := p.ThinkingToolcallCompat("m1")
	if got == nil || !got.EnabledValue() {
		t.Fatalf("expected provider default compat enabled=true, got %#v", got)
	}
}

func TestProviderConfig_ThinkingToolcallCompat_ModelOverride(t *testing.T) {
	cfg := config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Compat: &config.ProviderCompatConfig{
			ThinkingToolcall: &config.ThinkingToolcallCompatConfig{
				Enabled: new(true),
			},
		},
		Models: map[string]config.ModelConfig{
			"m1": {
				Compat: &config.ModelCompatConfig{
					ThinkingToolcall: &config.ThinkingToolcallCompatConfig{
						Enabled: new(false),
					},
				},
			},
		},
	}
	p := NewProviderConfig("test", cfg, []string{"k"})
	got := p.ThinkingToolcallCompat("m1")
	if got == nil {
		t.Fatal("expected non-nil compat config")
	}
	if got.EnabledValue() {
		t.Fatalf("expected model override enabled=false, got %#v", got)
	}
}

func TestProviderConfig_AnthropicTransportCompat(t *testing.T) {
	cfg := config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Compat: &config.ProviderCompatConfig{
			AnthropicTransport: &config.AnthropicTransportCompatConfig{
				SystemPrefix:   "prefix\n",
				ExtraBeta:      []string{"beta-a", "beta-b"},
				MetadataUserID: true,
			},
		},
	}
	p := NewProviderConfig("anthropic-main", cfg, []string{"k"})
	got := p.AnthropicTransportCompat()
	if got == nil {
		t.Fatal("expected anthropic transport compat config")
	}
	if got.SystemPrefix != "prefix\n" {
		t.Fatalf("unexpected system_prefix: %q", got.SystemPrefix)
	}
	if len(got.ExtraBeta) != 2 || got.ExtraBeta[0] != "beta-a" || got.ExtraBeta[1] != "beta-b" {
		t.Fatalf("unexpected extra_beta: %#v", got.ExtraBeta)
	}
	if !got.MetadataUserID {
		t.Fatal("expected metadata_user_id=true")
	}

	got.ExtraBeta[0] = "mutated"
	again := p.AnthropicTransportCompat()
	if again.ExtraBeta[0] != "beta-a" {
		t.Fatal("AnthropicTransportCompat should return a defensive copy")
	}
}

// --- Key Rotation: on_failure ---

// TestSelectKey_OnFailure_NoCooldown verifies that repeated calls with on_failure
// rotation always return the same key when no cooldown is active.
func TestSelectKey_OnFailure_NoCooldown(t *testing.T) {
	cp := NewProviderConfig("test", config.ProviderConfig{
		KeyRotation: config.KeyRotationOnFailure,
	}, []string{"key-a", "key-b", "key-c"})

	for i := 0; i < 5; i++ {
		key, _, err := cp.SelectKeyWithContext(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if key != "key-a" {
			t.Errorf("iteration %d: expected key-a, got %s", i, key)
		}
	}
}

// TestSelectKey_OnFailure_SwitchOnCooldown verifies that on_failure rotation
// switches to the next available key when the pinned key enters cooldown.
func TestSelectKey_OnFailure_SwitchOnCooldown(t *testing.T) {
	cp := NewProviderConfig("test", config.ProviderConfig{
		KeyRotation: config.KeyRotationOnFailure,
	}, []string{"key-a", "key-b"})

	key, _, err := cp.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "key-a" {
		t.Errorf("expected key-a, got %s", key)
	}

	cp.MarkCooldown("key-a", 10*time.Second)

	key, _, err = cp.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "key-b" {
		t.Errorf("expected key-b after cooldown, got %s", key)
	}

	// Subsequent calls should stay on key-b.
	key, _, err = cp.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "key-b" {
		t.Errorf("expected key-b (pinned), got %s", key)
	}
}

// TestSelectKey_OnFailure_AllCooldown verifies that AllKeysCoolingError is
// returned when all keys are in cooldown under on_failure rotation.
func TestSelectKey_OnFailure_AllCooldown(t *testing.T) {
	cp := NewProviderConfig("test", config.ProviderConfig{
		KeyRotation: config.KeyRotationOnFailure,
	}, []string{"key-a", "key-b"})

	cp.MarkCooldown("key-a", 10*time.Second)
	cp.MarkCooldown("key-b", 10*time.Second)

	_, _, err := cp.SelectKeyWithContext(context.Background())
	if err == nil {
		t.Fatal("expected AllKeysCoolingError, got nil")
	}
	var coolErr *AllKeysCoolingError
	if !errors.As(err, &coolErr) {
		t.Errorf("expected *AllKeysCoolingError, got %T: %v", err, err)
	}
}

func TestSelectKey_OnFailure_PrefersHealthyOverRecoveringPinnedKey(t *testing.T) {
	cp := NewProviderConfig("test", config.ProviderConfig{
		KeyRotation: config.KeyRotationOnFailure,
	}, []string{"key-a", "key-b"})
	cp.mu.Lock()
	cp.stickyIdx = 0
	cp.keyStates[0].Recovering = true
	cp.lastSelectedSlot = 0
	cp.lastSelectedKey = "key-a"
	cp.mu.Unlock()

	key, switched, err := cp.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "key-b" {
		t.Fatalf("expected healthy key-b, got %s", key)
	}
	if !switched {
		t.Fatal("expected slot switch when moving from recovering pinned key to healthy alternative")
	}
}

func TestSelectKey_OnFailure_UsesRecoveringPinnedKeyWhenNoHealthyAlternative(t *testing.T) {
	cp := NewProviderConfig("test", config.ProviderConfig{
		KeyRotation: config.KeyRotationOnFailure,
	}, []string{"key-a", "key-b"})
	cp.mu.Lock()
	cp.stickyIdx = 0
	cp.keyStates[0].Recovering = true
	cp.keyStates[1].CooldownEnd = time.Now().Add(time.Minute)
	cp.lastSelectedSlot = 0
	cp.lastSelectedKey = "key-a"
	cp.mu.Unlock()

	key, switched, err := cp.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "key-a" {
		t.Fatalf("expected recovering pinned key-a, got %s", key)
	}
	if switched {
		t.Fatal("did not expect slot switch when no healthy alternative exists")
	}
}

func TestSelectKey_OAuthRefreshInPlaceDoesNotEmitSwitch(t *testing.T) {
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"old-access-token"})
	p.mu.Lock()
	p.lastSelectedSlot = 0
	p.lastSelectedKey = "old-access-token"
	p.keyStates[0].Key = "new-access-token"
	p.keyStates[0].OAuthInfo = &OAuthKeyInfo{Expires: time.Now().Add(time.Hour).UnixMilli()}
	p.mu.Unlock()

	key, switched, err := p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("SelectKeyWithContext: %v", err)
	}
	if key != "new-access-token" {
		t.Fatalf("selected key = %q, want new-access-token", key)
	}
	if switched {
		t.Fatal("expected no key_switched event for in-place OAuth refresh on same slot")
	}
}

// TestSelectKey_SingleKeyNeverSwitches verifies that a provider with only one
// non-deactivated key never reports switched=true, even after cooldown/recovery
// cycles or compact ↔ main call interleaving that might leave lastSelectedSlot
// out of sync.
func TestSelectKey_SingleKeyNeverSwitches(t *testing.T) {
	cp := NewProviderConfig("test", config.ProviderConfig{
		KeyRotation: config.KeyRotationOnFailure,
	}, []string{"only-key"})

	// First selection: lastSelectedSlot starts at -1, switched should be false.
	key, switched, err := cp.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "only-key" {
		t.Fatalf("expected only-key, got %s", key)
	}
	if switched {
		t.Fatal("first selection should not report switched for single key")
	}

	// Put key in cooldown, then let it expire so it becomes recovering.
	cp.MarkCooldown("only-key", 1*time.Millisecond)
	time.Sleep(2 * time.Millisecond)

	// Second selection: key is recovering but still the only option.
	key, switched, err = cp.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error after cooldown: %v", err)
	}
	if key != "only-key" {
		t.Fatalf("expected only-key after cooldown, got %s", key)
	}
	if switched {
		t.Fatal("should not report switched for single key even after cooldown/recovery cycle")
	}

	// Mark success (clears recovery state), then select again.
	cp.MarkKeySuccess("only-key")
	key, switched, err = cp.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error after success: %v", err)
	}
	if key != "only-key" {
		t.Fatalf("expected only-key after success, got %s", key)
	}
	if switched {
		t.Fatal("should not report switched for single key after success")
	}
}

// TestSelectKey_SingleActiveKeyAfterDeactivationNeverSwitches verifies that
// when a provider starts with two keys but one is deactivated, the remaining
// active key never reports switched=true even if lastSelectedSlot pointed to
// the now-deactivated slot.
func TestSelectKey_SingleActiveKeyAfterDeactivationNeverSwitches(t *testing.T) {
	cp := NewProviderConfig("test", config.ProviderConfig{
		KeyRotation: config.KeyRotationOnFailure,
	}, []string{"key-a", "key-b"})

	// Select key-a first (slot 0).
	key, switched, err := cp.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "key-a" {
		t.Fatalf("expected key-a, got %s", key)
	}
	if switched {
		t.Fatal("first selection should not report switched")
	}

	// Deactivate key-a; now only key-b remains active.
	cp.MarkDeactivated("key-a")

	// Select again: should get key-b (slot 1), but since only 1 active key,
	// switched should be false (no meaningful key switch).
	key, switched, err = cp.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "key-b" {
		t.Fatalf("expected key-b, got %s", key)
	}
	if switched {
		t.Fatal("should not report switched when only one active key remains after deactivation")
	}
}

// TestSelectKey_SingleSelectableKeyAfterCooldownNeverSwitches verifies that
// when multiple keys exist but only one is selectable (others are cooling),
// the remaining selectable key never reports switched=true even if
// lastSelectedSlot pointed to a now-cooling slot.
func TestSelectKey_SingleSelectableKeyAfterCooldownNeverSwitches(t *testing.T) {
	cp := NewProviderConfig("test", config.ProviderConfig{
		KeyRotation: config.KeyRotationOnFailure,
	}, []string{"key-a", "key-b", "key-c"})

	// Select key-a first (slot 0).
	key, switched, err := cp.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "key-a" {
		t.Fatalf("expected key-a, got %s", key)
	}
	if switched {
		t.Fatal("first selection should not report switched")
	}

	// Cool down key-a; key-b also cooling; only key-c is selectable.
	cp.MarkCooldown("key-a", 10*time.Minute)
	cp.MarkCooldown("key-b", 10*time.Minute)

	// Select again: should get key-c (slot 2), but since only 1 selectable key,
	// switched should be false — this is a retry, not a key switch.
	key, switched, err = cp.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "key-c" {
		t.Fatalf("expected key-c, got %s", key)
	}
	if switched {
		t.Fatal("should not report switched when only one selectable key remains after cooldown")
	}
}

// TestSelectKey_OnFailure_Random verifies that on_failure+random selects a
// random available key on failure, and pins to it until it fails.
func TestSelectKey_OnFailure_Random(t *testing.T) {
	cp := NewProviderConfig("test", config.ProviderConfig{
		KeyRotation: config.KeyRotationOnFailure,
		KeyOrder:    config.KeyOrderRandom,
	}, []string{"key-a", "key-b", "key-c"})

	// First call: one of the three keys is selected and pinned.
	first, _, err := cp.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Repeated calls should return the same key (pinned).
	for i := 0; i < 5; i++ {
		key, _, err := cp.SelectKeyWithContext(context.Background())
		if err != nil {
			t.Fatalf("iteration %d: unexpected error: %v", i, err)
		}
		if key != first {
			t.Errorf("iteration %d: expected pinned key %s, got %s", i, first, key)
		}
	}

	// After the pinned key fails, a new key is selected from remaining two.
	cp.MarkCooldown(first, 10*time.Second)
	second, _, err := cp.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error after first cooldown: %v", err)
	}
	if second == first {
		t.Errorf("expected a different key after cooldown, got %s again", first)
	}
}

// TestSelectKey_OnFailure_RandomInitialization verifies that on_failure+random
// initializes with a random stickyIdx, not always 0.
func TestSelectKey_OnFailure_RandomInitialization(t *testing.T) {
	// Test multiple independent configs to check random distribution
	const iterations = 100
	keyCount := 3
	keyList := make([]string, keyCount)
	for i := 0; i < keyCount; i++ {
		keyList[i] = fmt.Sprintf("key-%d", i)
	}

	// Collect first key choices from multiple independent configs
	firstChoices := make(map[string]int)

	for i := 0; i < iterations; i++ {
		cp := NewProviderConfig("test", config.ProviderConfig{
			KeyRotation: config.KeyRotationOnFailure,
			KeyOrder:    config.KeyOrderRandom,
		}, keyList)

		first, _, err := cp.SelectKeyWithContext(context.Background())
		if err != nil {
			t.Fatalf("iteration %d: unexpected error: %v", i, err)
		}

		// Verify it's one of the keys
		var found bool
		for _, key := range keyList {
			if key == first {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("iteration %d: got unknown key %s", i, first)
		}

		firstChoices[first]++
	}

	// Check that distribution is somewhat even (not all selecting key-0)
	for _, key := range keyList {
		if firstChoices[key] == 0 {
			t.Errorf("key %s was never selected in %d iterations", key, iterations)
		}
		// Check that no single key dominates too much (very loose check)
		if firstChoices[key] > iterations*80/keyCount {
			t.Errorf("key %s selected %d/%d times (%.1f%%), possible bias",
				key, firstChoices[key], iterations,
				float64(firstChoices[key])/iterations*100)
		}
	}
}

// TestWarmup_SetsLastUsed verifies that calling Warmup() sets a non-zero
// LastUsed timestamp on every key.
func TestWarmup_SetsLastUsed(t *testing.T) {
	cp := NewProviderConfig("test", config.ProviderConfig{}, []string{"key-a", "key-b", "key-c"})

	for _, ks := range cp.keyStates {
		if !ks.LastUsed.IsZero() {
			t.Errorf("expected zero LastUsed before Warmup, got %v for key %s", ks.LastUsed, ks.Key)
		}
	}

	cp.Warmup()

	for _, ks := range cp.keyStates {
		if ks.LastUsed.IsZero() {
			t.Errorf("expected non-zero LastUsed after Warmup, got zero for key %s", ks.Key)
		}
	}
}

func TestIsContextLengthExceeded(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "openai code",
			err:  &APIError{StatusCode: 400, Code: "context_length_exceeded", Message: "context_length_exceeded"},
			want: true,
		},
		{
			name: "openai message",
			err:  &APIError{StatusCode: 400, Message: "This model's maximum context length is 128000 tokens"},
			want: true,
		},
		{
			name: "anthropic prompt too long",
			err:  &APIError{StatusCode: 400, Message: "prompt is too long: 250000 tokens > 200000 max"},
			want: true,
		},
		{
			name: "ordinary 400",
			err:  &APIError{StatusCode: 400, Message: "invalid tool schema"},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsContextLengthExceeded(tc.err); got != tc.want {
				t.Fatalf("IsContextLengthExceeded(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
