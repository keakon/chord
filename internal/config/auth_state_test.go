package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMergeAuthConfigWithStateOverlaysStatus(t *testing.T) {
	auth := AuthConfig{"openai": {{OAuth: &OAuthCredential{Access: "access-a", AccountUserID: "user-1__acc-1", AccountID: "acc-1", Email: "user@example.com"}}}}
	state := AuthStateFile{"openai": {"user-1__acc-1": {AccountUserID: "user-1__acc-1", AccountID: "acc-1", Email: "user@example.com", Status: OAuthStatusExpired}}}
	merged := MergeAuthConfigWithState(auth, state)
	if got := merged["openai"][0].OAuth; got == nil || got.Status != OAuthStatusExpired {
		t.Fatalf("merged oauth = %#v, want expired status from auth.state", got)
	}
}

func TestParseAuthStateIgnoresUnrecognizedStateKeys(t *testing.T) {
	state, err := ParseAuthState([]byte(`{
  "openai": {
    "openai:account_id:acc-old": {"status": "expired"},
    "account_id:acc-old2": {"status": "expired"},
    "openai:access_sha256:deadbeef": {"status": "invalidated"},
    "acc-ok": {"email": "user@example.com", "status": "expired"}
  }
}`))
	if err != nil {
		t.Fatalf("ParseAuthState: %v", err)
	}
	entries := state["openai"]
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1: %#v", len(entries), entries)
	}
	if got := entries["acc-ok"]; got.Status != OAuthStatusExpired || got.Email != "user@example.com" || got.AccountUserID != "acc-ok" || got.AccountID != "" {
		t.Fatalf("normalized entry = %#v, want account_user_id keyed record", got)
	}
}

func TestAuthStateRoundTripAndFind(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.state.json")
	hasCredits := true
	unlimited := false
	_, updated, changed, err := UpsertOAuthStateRecord(path, OAuthStateKey{Provider: "openai", AccountUserID: "user-1__acc-1", AccountID: "acc-1", Email: "user@example.com"}, func(record *OAuthStateRecord) (bool, error) {
		record.Status = OAuthStatusNormal
		record.CodexPrimaryUsedPct = 25
		record.CodexPrimaryWindowMin = 60
		record.CodexPrimaryResetAt = 12345
		record.CodexHasCredits = &hasCredits
		record.CodexUnlimited = &unlimited
		return true, nil
	})
	if err != nil {
		t.Fatalf("UpsertOAuthStateRecord: %v", err)
	}
	if !changed || updated == nil {
		t.Fatal("expected changed updated record")
	}
	state, err := LoadAuthState(path)
	if err != nil {
		t.Fatalf("LoadAuthState: %v", err)
	}
	if strings.Contains(string(mustReadFile(t, path)), "access") {
		t.Fatalf("auth.state.json should not persist access token:\n%s", mustReadFile(t, path))
	}
	record, ok := FindOAuthStateRecord(state, OAuthStateKey{Provider: "openai", AccountUserID: "user-1__acc-1", AccountID: "acc-1"})
	if !ok {
		t.Fatal("expected record by account user id")
	}
	if record.CodexPrimaryUsedPct != 25 || record.CodexPrimaryResetAt != 12345 || record.AccountUserID != "user-1__acc-1" || record.AccountID != "acc-1" {
		t.Fatalf("unexpected record: %#v", record)
	}
	if _, ok := state["openai"]["user-1__acc-1"]; !ok {
		t.Fatalf("state keys = %#v, want direct account_user_id key", state["openai"])
	}
}

func TestUpsertOAuthStateRecordRequiresAccountUserID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.state.json")
	_, _, _, err := UpsertOAuthStateRecord(path, OAuthStateKey{Provider: "openai"}, func(record *OAuthStateRecord) (bool, error) {
		record.Status = OAuthStatusInvalidated
		return true, nil
	})
	if err == nil {
		t.Fatal("expected empty state key error without account_user_id")
	}
	_, _, _, err = UpsertOAuthStateRecord(path, OAuthStateKey{Provider: "openai", AccountID: "acc-only"}, func(record *OAuthStateRecord) (bool, error) {
		record.Status = OAuthStatusInvalidated
		return true, nil
	})
	if err == nil {
		t.Fatal("expected empty state key error without account_user_id or refresh_sha256")
	}
	_, _, _, err = UpsertOAuthStateRecord(path, OAuthStateKey{Provider: "openai", RefreshSHA256: OAuthRefreshStateKey("refresh-token")}, func(record *OAuthStateRecord) (bool, error) {
		record.Status = OAuthStatusInvalidated
		return true, nil
	})
	if err != nil {
		t.Fatalf("expected refresh_sha256 state key to be accepted: %v", err)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	return data
}

func TestRemoveInvalidOAuthStateRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.state.yaml")
	_, _, _, err := UpsertOAuthStateRecord(path, OAuthStateKey{Provider: "openai", AccountUserID: "user-ok__acc-ok", AccountID: "acc-ok"}, func(record *OAuthStateRecord) (bool, error) {
		record.Status = OAuthStatusNormal
		return true, nil
	})
	if err != nil {
		t.Fatalf("Upsert ok: %v", err)
	}
	_, _, _, err = UpsertOAuthStateRecord(path, OAuthStateKey{Provider: "openai", AccountUserID: "user-expired__acc-expired", AccountID: "acc-expired", Email: "expired@example.com"}, func(record *OAuthStateRecord) (bool, error) {
		record.Status = OAuthStatusExpired
		return true, nil
	})
	if err != nil {
		t.Fatalf("Upsert expired: %v", err)
	}
	state, removed, err := RemoveInvalidOAuthStateRecords(path)
	if err != nil {
		t.Fatalf("RemoveInvalidOAuthStateRecords: %v", err)
	}
	if len(removed) != 1 {
		t.Fatalf("len(removed) = %d, want 1", len(removed))
	}
	if removed[0].AccountID != "acc-expired" || removed[0].Email != "expired@example.com" {
		t.Fatalf("removed = %#v, want expired account entry", removed)
	}
	if _, ok := FindOAuthStateRecord(state, OAuthStateKey{Provider: "openai", AccountUserID: "user-expired__acc-expired"}); ok {
		t.Fatal("expired state should be removed")
	}
	if _, ok := FindOAuthStateRecord(state, OAuthStateKey{Provider: "openai", AccountUserID: "user-ok__acc-ok"}); !ok {
		t.Fatal("valid state should remain")
	}
}
