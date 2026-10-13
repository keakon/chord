package mcp

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestReconcileRecoversExternalLoginWithoutClient(t *testing.T) {
	target := refreshTestTarget()
	target.Config.Name = "sample"
	target.Ref.ServerName = "sample"
	target.ConfigGeneration = 1
	store := NewCredentialStore(t.TempDir())
	auth := NewAuthService(AuthOptions{Store: store})
	defer auth.Close()
	manager := NewPendingManagerWithClientInfo([]Target{target}, testClientInfo, auth)
	defer manager.Close()
	attempts := new(atomic.Int32)
	manager.newClientFactory = func(context.Context, ServerConfig) (*Client, error) {
		attempts.Add(1)
		return NewClientWithInfo("sample", readyFakeTransport(), testClientInfo), nil
	}
	if changed, err := manager.ReconcileCredentials(t.Context()); err != nil || changed || attempts.Load() != 0 {
		t.Fatalf("missing credential attempted connection: %v", err)
	}
	seedRefreshTest(t, store, target, time.Now())
	if err := store.UpdateLocked(t.Context(), target, func(tx *CredentialTransaction) error {
		tx.File.Slots[target.ProfileID].Token.ExpiresAt = time.Now().Add(time.Hour).UTC()
		_, err := tx.Commit()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if changed, err := manager.ReconcileCredentials(t.Context()); err != nil || !changed || attempts.Load() != 1 {
		t.Fatalf("external credential did not restore target: changed=%t attempts=%d err=%v", changed, attempts.Load(), err)
	}
	if manager.Client("sample") == nil {
		t.Fatal("candidate was not installed")
	}
	if changed, err := manager.ReconcileCredentials(t.Context()); err != nil || changed || attempts.Load() != 1 {
		t.Fatalf("stable credential reconnected: %v", err)
	}
	if _, err := auth.Logout(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	if changed, err := manager.ReconcileCredentials(t.Context()); err != nil || !changed || manager.Client("sample") != nil {
		t.Fatalf("logout did not retire old identity: %v", err)
	}
}

func TestReconcileSuppressesSameCredentialFailure(t *testing.T) {
	target := refreshTestTarget()
	target.Config.Name = "sample"
	target.Ref.ServerName = "sample"
	target.ConfigGeneration = 1
	store := NewCredentialStore(t.TempDir())
	seedRefreshTest(t, store, target, time.Now())
	if err := store.UpdateLocked(t.Context(), target, func(tx *CredentialTransaction) error {
		tx.File.Slots[target.ProfileID].Token.ExpiresAt = time.Now().Add(time.Hour).UTC()
		_, err := tx.Commit()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	auth := NewAuthService(AuthOptions{Store: store})
	defer auth.Close()
	manager := NewPendingManagerWithClientInfo([]Target{target}, testClientInfo, auth)
	defer manager.Close()
	attempts := 0
	manager.newClientFactory = func(context.Context, ServerConfig) (*Client, error) {
		attempts++
		return nil, errors.New("invalid configuration")
	}
	if _, err := manager.ReconcileCredentials(t.Context()); err != nil {
		t.Fatal(err)
	}
	firstAttempts := attempts
	if _, err := manager.ReconcileCredentials(t.Context()); err != nil {
		t.Fatal(err)
	}
	if firstAttempts == 0 || attempts != firstAttempts {
		t.Fatalf("unchanged failure retried: first=%d total=%d", firstAttempts, attempts)
	}
}

func TestReconcilePreservesAnonymousAutoConnection(t *testing.T) {
	target := refreshTestTarget()
	target.Config.Name = "sample"
	target.Ref.ServerName = "sample"
	target.Config.Auth = "auto"
	service := NewAuthService(AuthOptions{ConfigHome: t.TempDir()})
	defer service.Close()
	manager := NewPendingManagerWithClientInfo([]Target{target}, testClientInfo, service)
	defer manager.Close()
	manager.newClientFactory = func(context.Context, ServerConfig) (*Client, error) {
		return NewClientWithInfo("sample", readyFakeTransport(), testClientInfo), nil
	}
	if err := manager.ConnectOne(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	if changed, err := manager.ReconcileCredentials(t.Context()); err != nil || changed || manager.Client("sample") == nil {
		t.Fatalf("anonymous auto connection was retired: changed=%t err=%v", changed, err)
	}
}
