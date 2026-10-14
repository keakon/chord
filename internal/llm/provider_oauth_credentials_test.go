package llm

import (
	"context"
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
)

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

func TestPersistCredentialStatusWritesAuthStateWithRefreshFallback(t *testing.T) {
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{Access: "access-a", Refresh: "refresh-a", AccountID: "acc-1", Email: "user@example.com"}}}}
	authMu := &sync.Mutex{}
	statePath := filepath.Join(t.TempDir(), "auth.state.json")
	r := &OAuthRefresher{providerName: "openai", authConfig: &auth, authConfigMu: authMu, authStatePath: statePath}

	if err := r.persistCredentialStatus(config.OAuthCredentialMatch{AccountID: "acc-1", Email: "user@example.com", RefreshSHA256: config.OAuthRefreshStateKey("refresh-a")}, config.OAuthStatusInvalidated); err != nil {
		t.Fatalf("persistCredentialStatus: %v", err)
	}
	state, err := config.LoadAuthState(statePath)
	if err != nil {
		t.Fatalf("LoadAuthState: %v", err)
	}
	record, ok := config.FindOAuthStateRecord(state, config.OAuthStateKey{Provider: "openai", RefreshSHA256: config.OAuthRefreshStateKey("refresh-a")})
	if !ok || record.Status != config.OAuthStatusInvalidated || record.Email != "user@example.com" {
		t.Fatalf("state record = %#v ok=%v, want invalidated refresh record", record, ok)
	}
}

func TestPersistCredentialStatusWritesAuthStateWhenAuthConfigMatchFails(t *testing.T) {
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{Access: "new-access", Refresh: "new-refresh", AccountID: "acc-1", Email: "user@example.com"}}}}
	authMu := &sync.Mutex{}
	statePath := filepath.Join(t.TempDir(), "auth.state.json")
	r := &OAuthRefresher{providerName: "openai", authConfig: &auth, authConfigMu: authMu, authStatePath: statePath}
	oldRefreshSHA := config.OAuthRefreshStateKey("old-refresh")

	err := r.persistCredentialStatus(config.OAuthCredentialMatch{Access: "old-access", RefreshSHA256: oldRefreshSHA}, config.OAuthStatusInvalidated)
	if err != nil {
		t.Fatalf("persistCredentialStatus returned error despite direct auth.state key: %v", err)
	}
	state, err := config.LoadAuthState(statePath)
	if err != nil {
		t.Fatalf("LoadAuthState: %v", err)
	}
	record, ok := config.FindOAuthStateRecord(state, config.OAuthStateKey{Provider: "openai", RefreshSHA256: oldRefreshSHA})
	if !ok || record.Status != config.OAuthStatusInvalidated {
		t.Fatalf("state record = %#v ok=%v, want invalidated refresh record", record, ok)
	}
	if got := auth["openai"][0].OAuth.Status; got != config.OAuthStatusNormal {
		t.Fatalf("auth config status = %q, want unchanged when mirror match fails", got)
	}
}

func TestMarkInvalidatedInvalidatesDuplicateAccessTokenSlots(t *testing.T) {
	access := "shared-access"
	auth := config.AuthConfig{"openai": {
		{OAuth: &config.OAuthCredential{Access: access, Refresh: "refresh-a", AccountUserID: "user-a__acct-a", AccountID: "acct-a", Email: "a@example.com"}},
		{OAuth: &config.OAuthCredential{Access: access, Refresh: "refresh-b", AccountUserID: "user-b__acct-b", AccountID: "acct-b", Email: "b@example.com"}},
	}}
	var authMu sync.Mutex
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{access, access})
	p.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", filepath.Join(t.TempDir(), "auth.state.json"), &auth, &authMu, map[string]OAuthKeySetup{
		OAuthKeySetupSlotKey(0, access): {HasKeySlot: true, KeySlot: 0, CredentialIndex: 0, Access: access, AccountUserID: "user-a__acct-a", AccountID: "acct-a", Email: "a@example.com"},
		OAuthKeySetupSlotKey(1, access): {HasKeySlot: true, KeySlot: 1, CredentialIndex: 1, Access: access, AccountUserID: "user-b__acct-b", AccountID: "acct-b", Email: "b@example.com"},
	}, "")
	defer p.Close()

	p.MarkInvalidated(access)

	p.mu.Lock()
	invalid0 := p.keyStates[0].Invalid
	invalid1 := p.keyStates[1].Invalid
	status0 := p.keyStates[0].OAuthInfo.Status
	status1 := p.keyStates[1].OAuthInfo.Status
	p.mu.Unlock()
	if !invalid0 || !invalid1 || status0 != config.OAuthStatusInvalidated || status1 != config.OAuthStatusInvalidated {
		t.Fatalf("duplicate key slot states = invalid %v/%v status %q/%q, want both invalidated", invalid0, invalid1, status0, status1)
	}
	if got := auth["openai"][0].OAuth.Status; got != config.OAuthStatusInvalidated {
		t.Fatalf("first auth status = %q, want invalidated", got)
	}
	if got := auth["openai"][1].OAuth.Status; got != config.OAuthStatusInvalidated {
		t.Fatalf("second auth status = %q, want invalidated", got)
	}
}

func TestOAuthInfoForDuplicateAccessTokenClearsConflictingIdentity(t *testing.T) {
	access := "shared-access"
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{access, access})
	p.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", "", nil, nil, map[string]OAuthKeySetup{
		OAuthKeySetupSlotKey(0, access): {HasKeySlot: true, KeySlot: 0, CredentialIndex: 0, Access: access, AccountUserID: "user-a__acct-a", AccountID: "acct-a", Email: "a@example.com"},
		OAuthKeySetupSlotKey(1, access): {HasKeySlot: true, KeySlot: 1, CredentialIndex: 1, Access: access, AccountUserID: "user-b__acct-b", AccountID: "acct-b", Email: "b@example.com"},
	}, "")
	defer p.Close()

	info := p.oauthInfoForKey(access)
	if info == nil {
		t.Fatal("oauthInfoForKey returned nil, want OAuth metadata without conflicting identity")
	}
	if info.AccountUserID != "" || info.AccountID != "" || info.Email != "" {
		t.Fatalf("oauthInfoForKey identity = user %q account %q email %q, want conflicting fields cleared", info.AccountUserID, info.AccountID, info.Email)
	}
	if !p.isOpenAIOAuthKey(access) {
		t.Fatal("duplicate access token should still be recognized as OpenAI OAuth")
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
	if _, ok := errors.AsType[*NoUsableKeysError](err); !ok {
		t.Fatalf("expected NoUsableKeysError, got %T: %v", err, err)
	}
}

func TestTryRefreshOAuthKey_PreservesLatestAuthFileChanges(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.yaml")
	expires := time.Now().Add(time.Hour).UnixMilli()
	oldAccess := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-1","chatgpt_user_id":"user-1"}`)
	newAccess := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-1","chatgpt_user_id":"user-1","exp":4102444800}`)
	if err := os.WriteFile(authPath, fmt.Appendf(nil, `openai:
  # primary oauth comment
  - refresh: old-refresh
    access: %s
    expires: %d
    account_user_id: user-1__acc-1
    account_id: acc-1
    codex_primary_reset_at: 111
    codex_secondary_reset_at: 222
  # sibling oauth comment
  - refresh: sibling-refresh
    access: %s
    expires: %d
    account_user_id: user-2__acc-2
    account_id: acc-2
    email: sibling@example.com
`, oldAccess, expires, testProviderOAuthJWT(`{"chatgpt_account_id":"acc-2","chatgpt_user_id":"user-2"}`), expires), 0o600); err != nil {
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
			AccountUserID:         "user-1__acc-1",
			AccountID:             "acc-1",
			Expires:               auth["openai"][0].OAuth.Expires,
			CodexPrimaryResetAt:   auth["openai"][0].OAuth.CodexPrimaryResetAt,
			CodexSecondaryResetAt: auth["openai"][0].OAuth.CodexSecondaryResetAt,
		},
	}, "")

	if err := os.WriteFile(authPath, fmt.Appendf(nil, `openai:
  # primary oauth comment
  - refresh: old-refresh
    access: %s
    expires: %d
    account_user_id: user-1__acc-1
    account_id: acc-1
    codex_primary_reset_at: 333
    codex_secondary_reset_at: 444
  # sibling oauth comment
  - refresh: sibling-refresh
    access: %s
    expires: %d
    account_user_id: user-2__acc-2
    account_id: acc-2
    email: external@example.com
`, oldAccess, expires, testProviderOAuthJWT(`{"chatgpt_account_id":"acc-2","chatgpt_user_id":"user-2"}`), expires), 0o600); err != nil {
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

func TestSetOAuthRefresherRefreshOnlyBindsByRefreshStateKey(t *testing.T) {
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses}, []string{"", ""})
	auth := config.AuthConfig{"openai": {
		{APIKey: "", ExplicitEmpty: true},
		{OAuth: &config.OAuthCredential{Refresh: "refresh-only"}},
	}}
	var authMu sync.Mutex
	stateKey := config.OAuthRefreshStateKey("refresh-only")
	p.SetOAuthRefresher("https://example.invalid/token", "client-id", "", "", &auth, &authMu, map[string]OAuthKeySetup{
		stateKey: {CredentialIndex: 1, RefreshSHA256: stateKey, Status: config.OAuthStatusNormal},
	}, "")

	if p.keyStates[0].OAuthInfo != nil {
		t.Fatalf("explicit empty key slot should not receive OAuth info: %#v", p.keyStates[0].OAuthInfo)
	}
	if p.keyStates[1].OAuthInfo == nil || p.keyStates[1].OAuthInfo.RefreshSHA256 != stateKey {
		t.Fatalf("refresh-only OAuth slot not bound by refresh state key: %#v", p.keyStates[1].OAuthInfo)
	}
}

func TestSelectKeyUsesExistingOAuthAccessTokenWithoutPreRefresh(t *testing.T) {
	expires := time.Now().Add(-time.Minute).UnixMilli()
	access := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-1","chatgpt_user_id":"user-1"}`)
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{
		Access:        access,
		Refresh:       "refresh-token",
		Expires:       expires,
		AccountUserID: "user-1__acc-1", AccountID: "acc-1",
	}}}}
	var authMu sync.Mutex
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("refresh endpoint should not be called before trying existing access token")
	}))
	defer refreshServer.Close()

	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{access})
	p.SetOAuthRefresher(refreshServer.URL, "client-id", "", "", &auth, &authMu, map[string]OAuthKeySetup{
		access: {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Expires: expires},
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

func TestSelectKeyDoesNotSkipExpiredOAuthTokenBeforeProbe(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.yaml")
	expired := time.Now().Add(-time.Minute).UnixMilli()
	valid := time.Now().Add(time.Hour).UnixMilli()
	expiredAccess := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-1","chatgpt_user_id":"user-1"}`)
	validAccess := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-2","exp":4102444800}`)
	if err := os.WriteFile(authPath, fmt.Appendf(nil, `openai:
  - refresh: old-refresh
    access: %s
    expires: %d
    account_id: acc-1
  - refresh: valid-refresh
    access: %s
    expires: %d
    account_id: acc-2
`, expiredAccess, expired, validAccess, valid), 0o600); err != nil {
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
		expiredAccess: {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Expires: expired},
		validAccess:   {CredentialIndex: 1, AccountUserID: "user-2__acc-2", AccountID: "acc-2", Expires: valid},
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
