package config

import (
	"path/filepath"
	"testing"
)

func TestMergeAuthConfigWithStateOverlaysStatus(t *testing.T) {
	auth := AuthConfig{"openai": {{OAuth: &OAuthCredential{Access: "access-a", AccountID: "acc-1", Email: "user@example.com"}}}}
	state := AuthStateFile{"openai": {"openai:account_id:acc-1": {AccountID: "acc-1", Email: "user@example.com", Status: OAuthStatusExpired}}}
	merged := MergeAuthConfigWithState(auth, state)
	if got := merged["openai"][0].OAuth; got == nil || got.Status != OAuthStatusExpired {
		t.Fatalf("merged oauth = %#v, want expired status from auth.state", got)
	}
}

func TestMergeAuthConfigWithStateOverlaysStatusByAccess(t *testing.T) {
	auth := AuthConfig{"openai": {{OAuth: &OAuthCredential{Access: "access-a", Email: "user@example.com"}}}}
	state := AuthStateFile{"openai": {OAuthStateRecordKey(OAuthStateKey{Provider: "openai", Access: "access-a"}): {Access: "access-a", Email: "user@example.com", Status: OAuthStatusInvalidated}}}
	merged := MergeAuthConfigWithState(auth, state)
	if got := merged["openai"][0].OAuth; got == nil || got.Status != OAuthStatusInvalidated {
		t.Fatalf("merged oauth = %#v, want invalidated status from access-keyed auth.state", got)
	}
}

func TestAuthStateRoundTripAndFind(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.state.yaml")
	hasCredits := true
	unlimited := false
	_, updated, changed, err := UpsertOAuthStateRecord(path, OAuthStateKey{Provider: "openai", AccountID: "acc-1", Email: "user@example.com", Access: "token-1"}, func(record *OAuthStateRecord) (bool, error) {
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
	record, ok := FindOAuthStateRecord(state, OAuthStateKey{Provider: "openai", AccountID: "acc-1"})
	if !ok {
		t.Fatal("expected record by account id")
	}
	if record.CodexPrimaryUsedPct != 25 || record.CodexPrimaryResetAt != 12345 {
		t.Fatalf("unexpected record: %#v", record)
	}
}

func TestAuthStateRoundTripAndFindByAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.state.yaml")
	_, updated, changed, err := UpsertOAuthStateRecord(path, OAuthStateKey{Provider: "openai", Access: "token-1", Email: "user@example.com"}, func(record *OAuthStateRecord) (bool, error) {
		record.Status = OAuthStatusInvalidated
		return true, nil
	})
	if err != nil {
		t.Fatalf("UpsertOAuthStateRecord: %v", err)
	}
	if !changed || updated == nil || updated.Access != "token-1" || updated.Email != "user@example.com" {
		t.Fatalf("updated = %#v changed=%v, want access-keyed record", updated, changed)
	}
	state, err := LoadAuthState(path)
	if err != nil {
		t.Fatalf("LoadAuthState: %v", err)
	}
	record, ok := FindOAuthStateRecord(state, OAuthStateKey{Provider: "openai", Access: "token-1"})
	if !ok {
		t.Fatal("expected record by access")
	}
	if record.Status != OAuthStatusInvalidated || record.Access != "token-1" || record.Email != "user@example.com" {
		t.Fatalf("record = %#v, want invalidated access-keyed record", record)
	}
}

func TestRemoveInvalidOAuthStateRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.state.yaml")
	_, _, _, err := UpsertOAuthStateRecord(path, OAuthStateKey{Provider: "openai", AccountID: "acc-ok"}, func(record *OAuthStateRecord) (bool, error) {
		record.Status = OAuthStatusNormal
		return true, nil
	})
	if err != nil {
		t.Fatalf("Upsert ok: %v", err)
	}
	_, _, _, err = UpsertOAuthStateRecord(path, OAuthStateKey{Provider: "openai", AccountID: "acc-expired", Email: "expired@example.com"}, func(record *OAuthStateRecord) (bool, error) {
		record.Status = OAuthStatusExpired
		return true, nil
	})
	if err != nil {
		t.Fatalf("Upsert expired: %v", err)
	}
	_, _, _, err = UpsertOAuthStateRecord(path, OAuthStateKey{Provider: "openai", Access: "access-invalid", Email: "invalid@example.com"}, func(record *OAuthStateRecord) (bool, error) {
		record.Status = OAuthStatusInvalidated
		return true, nil
	})
	if err != nil {
		t.Fatalf("Upsert invalidated: %v", err)
	}
	state, removed, err := RemoveInvalidOAuthStateRecords(path)
	if err != nil {
		t.Fatalf("RemoveInvalidOAuthStateRecords: %v", err)
	}
	if len(removed) != 2 {
		t.Fatalf("len(removed) = %d, want 2", len(removed))
	}
	seenExpired := false
	seenInvalidAccess := false
	for _, entry := range removed {
		if entry.AccountID == "acc-expired" {
			seenExpired = true
		}
		if entry.Access == "access-invalid" && entry.Email == "invalid@example.com" {
			seenInvalidAccess = true
		}
	}
	if !seenExpired || !seenInvalidAccess {
		t.Fatalf("removed = %#v, want account and access-keyed entries", removed)
	}
	if _, ok := FindOAuthStateRecord(state, OAuthStateKey{Provider: "openai", AccountID: "acc-expired"}); ok {
		t.Fatal("expired state should be removed")
	}
	if _, ok := FindOAuthStateRecord(state, OAuthStateKey{Provider: "openai", Access: "access-invalid"}); ok {
		t.Fatal("access-keyed invalid state should be removed")
	}
	if _, ok := FindOAuthStateRecord(state, OAuthStateKey{Provider: "openai", AccountID: "acc-ok"}); !ok {
		t.Fatal("valid state should remain")
	}
}
