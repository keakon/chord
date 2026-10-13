package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/recovery"
)

func takeoverStartupFixture(t *testing.T) (string, recovery.TransferIdentity, *recovery.SessionSnapshot) {
	t.Helper()
	root := t.TempDir()
	sessionDir := filepath.Join(root, "session-1")
	if err := os.Mkdir(sessionDir, 0700); err != nil {
		t.Fatal(err)
	}
	id := recovery.TransferIdentity{TransferID: "transfer-1", SessionID: "session-1", Workspace: root, SourceRuntimeID: "runtime-1", SourceSessionEpoch: 1, TargetKind: "tui", TargetInstanceID: "target-1"}
	snapshot := &recovery.SessionSnapshot{Pause: &recovery.SessionPauseCheckpoint{Identity: id, CheckpointID: "checkpoint-1", Paused: true, CheckpointComplete: true}}
	if err := recovery.NewRecoveryManager(sessionDir).SaveSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	return sessionDir, id, snapshot
}

func TestTakeoverStartupClaimsEmptyPausedSession(t *testing.T) {
	dir, id, _ := takeoverStartupFixture(t)
	plan, err := planSessionStartup(filepath.Dir(dir), t.TempDir(), sessionStartupOptions{ResumeID: id.SessionID, ExpectedTransfer: id})
	if err != nil {
		t.Fatal(err)
	}
	defer plan.SessionLock.Release()
	if plan.SessionDir != dir || !plan.RestoreOnStartup {
		t.Fatalf("plan=%+v", plan)
	}
	if lock, err := recovery.AcquireSessionLock(dir); err == nil {
		_ = lock.Release()
		t.Fatal("startup did not own session")
	}
}

func TestOrdinaryStartupClaimsEmptySessionWithUnconsumedInputs(t *testing.T) {
	dir, id, snapshot := takeoverStartupFixture(t)
	snapshot.Pause.Paused = false
	snapshot.Pause.PendingInputs = []recovery.PendingInput{{InputID: "input-1", OwnerTaskID: "main", Stage: recovery.PendingInputStageQueued, Content: "pending instruction"}}
	if err := recovery.NewRecoveryManager(dir).SaveSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	plan, err := planSessionStartup(filepath.Dir(dir), t.TempDir(), sessionStartupOptions{ResumeID: id.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.SessionLock.Release(); err != nil {
		t.Fatal(err)
	}
	if !plan.RestoreOnStartup || plan.SessionDir != dir {
		t.Fatalf("plan=%+v", plan)
	}
	if _, err := planSessionStartup(filepath.Dir(dir), t.TempDir(), sessionStartupOptions{ResumeID: id.SessionID, ExpectedTransfer: id}); err == nil {
		t.Fatal("incoming transfer accepted a checkpoint already continued by another owner")
	}
}

func TestTakeoverStartupRejectsChangedCheckpointAndReleasesLock(t *testing.T) {
	for _, change := range []string{"continued", "identity", "incomplete", "workdir"} {
		t.Run(change, func(t *testing.T) {
			dir, id, snapshot := takeoverStartupFixture(t)
			switch change {
			case "continued":
				snapshot.Pause.Paused = false
			case "identity":
				snapshot.Pause.Identity.SourceSessionEpoch++
			case "incomplete":
				snapshot.Pause.CheckpointComplete = false
			case "workdir":
				if err := recovery.SaveSessionMeta(dir, recovery.SessionMeta{WorktreePath: filepath.Join(id.Workspace, "unavailable")}); err != nil {
					t.Fatal(err)
				}
			}
			if err := recovery.NewRecoveryManager(dir).SaveSnapshot(snapshot); err != nil {
				t.Fatal(err)
			}
			// Existing messages keep the ordinary resume precondition satisfied;
			// rejection must come from transfer validation under the lock.
			if err := os.WriteFile(filepath.Join(dir, "main.jsonl"), []byte("{}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := planSessionStartup(filepath.Dir(dir), t.TempDir(), sessionStartupOptions{ResumeID: id.SessionID, ExpectedTransfer: id})
			if err == nil || !strings.Contains(err.Error(), "restore_failed:") {
				t.Fatalf("err=%v", err)
			}
			lock, err := recovery.AcquireSessionLock(dir)
			if err != nil {
				t.Fatalf("failed startup retained lock: %v", err)
			}
			_ = lock.Release()
		})
	}
}
