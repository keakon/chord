package mcp

import (
	"context"
	"testing"
	"time"
)

func TestRefreshCredentialSnapshotReadsDisabledCredentialWithoutConnection(t *testing.T) {
	target := refreshTestTarget()
	target.Config.Name = "sample"
	target.Ref.ServerName = "sample"
	target.Config.Manual = true
	target.ConfigGeneration = 1
	store := NewCredentialStore(t.TempDir())
	auth := NewAuthService(AuthOptions{Store: store})
	defer auth.Close()
	manager := NewPendingManagerWithClientInfo([]Target{target}, testClientInfo, auth)
	defer manager.Close()
	manager.newClientFactory = func(context.Context, ServerConfig) (*Client, error) {
		t.Fatal("snapshot must not connect")
		return nil, nil
	}
	seedRefreshTest(t, store, target, time.Now())
	if err := store.UpdateLocked(t.Context(), target, func(tx *CredentialTransaction) error {
		tx.File.Slots[target.ProfileID].Token.ExpiresAt = time.Now().Add(time.Hour).UTC()
		_, err := tx.Commit()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	manager.RefreshCredentialSnapshot(t.Context())
	snapshot := manager.Snapshot()
	if len(snapshot.Servers) != 1 || snapshot.Servers[0].Auth != AuthReady || snapshot.Servers[0].DesiredEnabled {
		t.Fatalf("saved credential missing: %#v", snapshot)
	}
	if _, err := auth.Logout(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	manager.RefreshCredentialSnapshot(t.Context())
	if snapshot = manager.Snapshot(); snapshot.Servers[0].Auth != AuthMissing {
		t.Fatalf("logout missing: %#v", snapshot)
	}
}
