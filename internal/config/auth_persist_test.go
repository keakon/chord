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
		t.Fatalf("expected legacy slot unchanged, got %#v", creds[0])
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
func writeAuthFixture(t *testing.T, body string) string {
	t.Helper()
	path := t.TempDir() + "/auth.yaml"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
	return path
}
