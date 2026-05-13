package config

import (
	"path/filepath"
	"testing"
)

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
		record.Email = "expired@example.com"
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
	if removed[0].Email != "expired@example.com" {
		t.Fatalf("removed email = %q, want expired@example.com", removed[0].Email)
	}
	if _, ok := FindOAuthStateRecord(state, OAuthStateKey{Provider: "openai", AccountID: "acc-expired"}); ok {
		t.Fatal("expired state should be removed")
	}
	if _, ok := FindOAuthStateRecord(state, OAuthStateKey{Provider: "openai", AccountID: "acc-ok"}); !ok {
		t.Fatal("valid state should remain")
	}
}
