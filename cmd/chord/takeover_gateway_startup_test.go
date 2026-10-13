package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/keakon/chord/internal/recovery"
)

func TestIncomingGatewayStartupOwnsLockAndRejectsInvalidBinding(t *testing.T) {
	withTestStateDir(t)
	t.Chdir(t.TempDir())
	loc, err := locateTakeover(t.Context(), "", "session-sample")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(loc.sessionDir, 0700); err != nil {
		t.Fatal(err)
	}
	id := recovery.TransferIdentity{TransferID: "transfer-sample", SessionID: "session-sample", SourceRuntimeID: "runtime-sample", Workspace: loc.workspace, TargetKind: "gateway", TargetInstanceID: "binding-sample"}
	snapshot := &recovery.SessionSnapshot{Pause: &recovery.SessionPauseCheckpoint{Identity: id, CheckpointID: "checkpoint-sample", Paused: true, CheckpointComplete: true}}
	manager := recovery.NewRecoveryManager(loc.sessionDir)
	if err := manager.SaveSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []struct{ transferID, binding string }{{"transfer-other", id.TargetInstanceID}, {id.TransferID, "binding-other"}} {
		if _, _, _, err := claimIncomingGatewayStartup(t.Context(), id.SessionID, invalid.transferID, invalid.binding); err == nil {
			t.Fatal("invalid incoming transfer admitted")
		}
	}
	plan, gotID, workDir, err := claimIncomingGatewayStartup(t.Context(), id.SessionID, id.TransferID, id.TargetInstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if gotID != id || workDir != loc.workspace {
		t.Fatalf("identity=%+v workDir=%s", gotID, workDir)
	}
	if lock, err := recovery.AcquireSessionLock(loc.sessionDir); err == nil {
		_ = lock.Release()
		t.Fatal("incoming target did not hold exclusive session lock")
	}
	_ = plan.SessionLock.Release()
	// A missing saved checkout must fail before any ordinary resume helper
	// can clear the metadata and substitute the workspace.
	meta := recovery.SessionMeta{WorktreePath: filepath.Join(loc.workspace, "missing-checkout")}
	if err := recovery.SaveSessionMeta(loc.sessionDir, meta); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := claimIncomingGatewayStartup(t.Context(), id.SessionID, id.TransferID, id.TargetInstanceID); err == nil {
		t.Fatal("missing checkout admitted")
	}
	gotMeta, err := recovery.LoadSessionMeta(loc.sessionDir)
	if err != nil || gotMeta.WorktreePath != meta.WorktreePath {
		t.Fatalf("startup modified saved checkout: meta=%+v err=%v", gotMeta, err)
	}
	lock, err := recovery.AcquireSessionLock(loc.sessionDir)
	if err != nil {
		t.Fatalf("failed startup leaked ownership: %v", err)
	}
	_ = lock.Release()
}
