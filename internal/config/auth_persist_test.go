package config

import (
	"os"
	"strings"
	"testing"
)

func TestUpsertOAuthCredentialInFile_PreservesCommentsAndOtherProviders(t *testing.T) {
	path := writeAuthFixture(t, `# auth comment
openai:
  # env credential comment
  - $OPENAI_API_KEY
  # oauth slot comment
  - refresh: old-refresh
    access: old-access
    expires: 111
    account_id: acc-1
    # email field comment
    email: old@example.com
anthropic:
  # untouched provider comment
  - sk-ant-test
`)
	t.Setenv("OPENAI_API_KEY", "env-openai-key")

	cred := &OAuthCredential{
		Refresh:   "new-refresh",
		Access:    "new-access",
		Expires:   222,
		AccountID: "acc-1",
		Email:     "new@example.com",
	}

	auth, err := UpsertOAuthCredentialInFile(path, "openai", cred)
	if err != nil {
		t.Fatalf("UpsertOAuthCredentialInFile: %v", err)
	}

	creds := auth["openai"]
	if len(creds) != 2 {
		t.Fatalf("expected 2 visible openai credentials, got %#v", creds)
	}
	if creds[0].APIKey != "env-openai-key" {
		t.Fatalf("expected env credential to remain, got %#v", creds[0])
	}
	if creds[1].OAuth == nil || creds[1].OAuth.Access != "new-access" || creds[1].OAuth.AccountID != "acc-1" {
		t.Fatalf("expected oauth credential to be updated in place, got %#v", creds[1])
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"# auth comment",
		"# env credential comment",
		"# oauth slot comment",
		"# email field comment",
		"# untouched provider comment",
		"$OPENAI_API_KEY",
		"new-access",
		"anthropic:",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected auth.yaml to contain %q, got:\n%s", want, text)
		}
	}
}

func TestUpsertOAuthCredentialInFile_AppendsWhenExistingSlotHasNoAccountID(t *testing.T) {
	path := writeAuthFixture(t, `openai:
  - refresh: old-refresh
    access: old-access
    expires: 111
`)

	cred := &OAuthCredential{
		Refresh:   "new-refresh",
		Access:    "new-access",
		Expires:   222,
		AccountID: "acc-1",
		Email:     "new@example.com",
	}

	auth, err := UpsertOAuthCredentialInFile(path, "openai", cred)
	if err != nil {
		t.Fatalf("UpsertOAuthCredentialInFile: %v", err)
	}

	creds := auth["openai"]
	if len(creds) != 2 {
		t.Fatalf("expected 2 oauth credentials, got %#v", creds)
	}
	if creds[0].OAuth == nil || creds[0].OAuth.Access != "old-access" || creds[0].OAuth.AccountID != "" {
		t.Fatalf("expected existing slot unchanged, got %#v", creds[0])
	}
	if creds[1].OAuth == nil || creds[1].OAuth.Access != "new-access" || creds[1].OAuth.AccountID != "acc-1" {
		t.Fatalf("expected new slot appended, got %#v", creds[1])
	}
}

func TestUpsertOAuthCredentialInFile_RejectsMissingAccountID(t *testing.T) {
	path := writeAuthFixture(t, "")

	_, err := UpsertOAuthCredentialInFile(path, "openai", &OAuthCredential{
		Refresh: "refresh-token",
		Access:  "access-token",
		Expires: 111,
	})
	if err == nil {
		t.Fatal("expected missing account_id to be rejected")
	}
	if !strings.Contains(err.Error(), "account_id is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUpdateOAuthCredentialInFile_RequiresAccountIDMatch(t *testing.T) {
	path := writeAuthFixture(t, `openai:
  - $UNSET_AUTH_SLOT
  - refresh: refresh-token
    access: access-token
    expires: 111
`)
	_ = os.Unsetenv("UNSET_AUTH_SLOT")

	_, _, _, err := UpdateOAuthCredentialInFile(path, "openai", OAuthCredentialMatch{}, func(cred *OAuthCredential) (bool, error) {
		cred.Email = "user@example.com"
		return true, nil
	})
	if err == nil {
		t.Fatal("expected oauth credential not found error")
	}
	if !strings.Contains(err.Error(), "oauth credential not found") {
		t.Fatalf("expected oauth credential not found error, got %v", err)
	}
}

func TestUpdateOAuthCredentialInFile_MatchesOAuthWithoutAccountIDByAccess(t *testing.T) {
	path := writeAuthFixture(t, `openai:
  - refresh: refresh-token
    access: access-token
    expires: 111
    email: user@example.com
`)

	auth, updated, changed, err := UpdateOAuthCredentialInFile(path, "openai", OAuthCredentialMatch{Access: "access-token"}, func(cred *OAuthCredential) (bool, error) {
		cred.Status = OAuthStatusInvalidated
		return true, nil
	})
	if err != nil {
		t.Fatalf("UpdateOAuthCredentialInFile: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true when matching OAuth credential by access")
	}
	if updated == nil || updated.Access != "access-token" || updated.Status != OAuthStatusInvalidated {
		t.Fatalf("updated credential = %#v, want access-token with invalidated status", updated)
	}
	if got := auth["openai"][0].OAuth; got == nil || got.Access != "access-token" || got.Status != OAuthStatusNormal {
		t.Fatalf("expected auth.yaml view to omit status from persisted credential, got %#v", got)
	}
}

func TestUpdateOAuthCredentialInFile_MatchesOAuthWithoutAccountIDByCredentialIndex(t *testing.T) {
	path := writeAuthFixture(t, `openai:
  - refresh: refresh-a
    access: access-a
    expires: 111
  - refresh: refresh-b
    access: access-b
    expires: 222
`)

	credentialIndex := 1
	_, updated, changed, err := UpdateOAuthCredentialInFile(path, "openai", OAuthCredentialMatch{CredentialIndex: &credentialIndex}, func(cred *OAuthCredential) (bool, error) {
		cred.Email = "updated@example.com"
		return true, nil
	})
	if err != nil {
		t.Fatalf("UpdateOAuthCredentialInFile: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true when matching OAuth credential by index")
	}
	if updated == nil || updated.Access != "access-b" || updated.Email != "updated@example.com" {
		t.Fatalf("updated credential = %#v, want second credential updated", updated)
	}
}

func TestUpdateOAuthCredentialInFile_PrefersAccessAndCredentialIndexWhenAccountIDIsDuplicated(t *testing.T) {
	path := writeAuthFixture(t, `openai:
  - refresh: refresh-a
    access: access-a
    expires: 111
    account_id: shared-acc
  - refresh: refresh-b
    access: access-b
    expires: 222
    account_id: shared-acc
`)

	credentialIndex := 1
	auth, updated, changed, err := UpdateOAuthCredentialInFile(path, "openai", OAuthCredentialMatch{
		AccountID:       "shared-acc",
		Access:          "access-b",
		CredentialIndex: &credentialIndex,
	}, func(cred *OAuthCredential) (bool, error) {
		cred.Status = OAuthStatusExpired
		return true, nil
	})
	if err != nil {
		t.Fatalf("UpdateOAuthCredentialInFile: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true when duplicate account_id is disambiguated by access/index")
	}
	if updated == nil || updated.Access != "access-b" || updated.Status != OAuthStatusExpired {
		t.Fatalf("updated credential = %#v, want access-b with expired status", updated)
	}
	if got := auth["openai"][0].OAuth; got == nil || got.Access != "access-a" || got.Status != OAuthStatusNormal {
		t.Fatalf("expected first duplicate account_id credential unchanged, got %#v", got)
	}
	if got := auth["openai"][1].OAuth; got == nil || got.Access != "access-b" || got.Status != OAuthStatusNormal {
		t.Fatalf("expected auth.yaml to omit status from persisted credentials, got %#v", got)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	text := string(data)
	if strings.Contains(text, "status:") {
		t.Fatalf("expected auth.yaml to omit status fields, got:\n%s", text)
	}
}

func TestUpdateOAuthCredentialInFile_UpdatesAndClearsCodexResetHints(t *testing.T) {
	path := writeAuthFixture(t, `# auth comment
openai:
  - refresh: refresh-token
    access: access-token
    expires: 111
    account_id: acc-1
    # provider-local hints
    codex_primary_reset_at: 1000
    codex_secondary_reset_at: 2000
anthropic:
  - sk-ant-test
`)

	auth, updated, changed, err := UpdateOAuthCredentialInFile(path, "openai", OAuthCredentialMatch{AccountID: "acc-1"}, func(cred *OAuthCredential) (bool, error) {
		cred.CodexPrimaryResetAt = 3333
		cred.CodexSecondaryResetAt = 4444
		return true, nil
	})
	if err != nil {
		t.Fatalf("UpdateOAuthCredentialInFile(update): %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true when updating codex reset hints")
	}
	if updated == nil || updated.CodexPrimaryResetAt != 3333 || updated.CodexSecondaryResetAt != 4444 {
		t.Fatalf("updated credential = %#v, want reset hints 3333/4444", updated)
	}
	if got := auth["openai"][0].OAuth; got == nil || got.CodexPrimaryResetAt != 3333 || got.CodexSecondaryResetAt != 4444 {
		t.Fatalf("auth config oauth = %#v, want reset hints 3333/4444", got)
	}

	auth, updated, changed, err = UpdateOAuthCredentialInFile(path, "openai", OAuthCredentialMatch{AccountID: "acc-1"}, func(cred *OAuthCredential) (bool, error) {
		cred.CodexPrimaryResetAt = 0
		cred.CodexSecondaryResetAt = 0
		return true, nil
	})
	if err != nil {
		t.Fatalf("UpdateOAuthCredentialInFile(clear): %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true when clearing codex reset hints")
	}
	if updated == nil || updated.CodexPrimaryResetAt != 0 || updated.CodexSecondaryResetAt != 0 {
		t.Fatalf("updated credential after clear = %#v, want zero reset hints", updated)
	}
	if got := auth["openai"][0].OAuth; got == nil || got.CodexPrimaryResetAt != 0 || got.CodexSecondaryResetAt != 0 {
		t.Fatalf("auth config oauth after clear = %#v, want zero reset hints", got)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	text := string(data)
	for _, want := range []string{"# auth comment", "anthropic:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected auth.yaml to contain %q, got:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"codex_primary_reset_at:", "codex_secondary_reset_at:"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("expected auth.yaml to clear %q, got:\n%s", unwanted, text)
		}
	}
}

func TestUpsertAPIKeyCredentialInFile_PreservesOtherProvidersAndDeduplicates(t *testing.T) {
	path := writeAuthFixture(t, `openai:
  - old-openai-key
anthropic:
  - old-anthropic-key
`)

	changed, err := UpsertAPIKeyCredentialInFile(path, "openai", "old-openai-key")
	if err != nil {
		t.Fatalf("UpsertAPIKeyCredentialInFile(no-op): %v", err)
	}
	if changed {
		t.Fatal("expected no-op when credential already exists")
	}

	changed, err = UpsertAPIKeyCredentialInFile(path, "openai", "new-openai-key")
	if err != nil {
		t.Fatalf("UpsertAPIKeyCredentialInFile(update): %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true when inserting new api key")
	}

	auth, err := LoadAuthConfig(path)
	if err != nil {
		t.Fatalf("LoadAuthConfig: %v", err)
	}
	if got := auth["openai"]; len(got) != 2 || got[0].APIKey != "old-openai-key" || got[1].APIKey != "new-openai-key" {
		t.Fatalf("auth[openai] = %#v", got)
	}
	if got := auth["anthropic"]; len(got) != 1 || got[0].APIKey != "old-anthropic-key" {
		t.Fatalf("auth[anthropic] = %#v", got)
	}
}

func writeAuthFixture(t *testing.T, body string) string {
	t.Helper()
	path := t.TempDir() + "/auth.yaml"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
	return path
}
