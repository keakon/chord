package main

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/takeover"
)

func TestTakeoverPreflightDoesNotDispatchStart(t *testing.T) {
	if !takeover.Supported() {
		t.Skip("takeover requires macOS or Linux")
	}
	stateDir, err := os.MkdirTemp("/tmp", "ct-preflight-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stateDir) })
	workspace, err := takeover.CanonicalWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sessionDir := filepath.Join(workspace, "session-1")
	if err := os.Mkdir(sessionDir, 0700); err != nil {
		t.Fatal(err)
	}
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)
	configText := "providers:\n  sample:\n    type: messages\n    models:\n      test-model: {}\nmodel_pools:\n  default: [sample/test-model]\n"
	if err := os.WriteFile(filepath.Join(configHome, "config.yaml"), []byte(configText), 0600); err != nil {
		t.Fatal(err)
	}
	starts := new(atomic.Int32)
	server, err := takeover.StartServer(takeover.ServerOptions{
		StateDir: stateDir,
		Record:   takeover.Record{Protocol: takeover.ProtocolVersion, RuntimeID: "runtime-1", SessionEpoch: 2, SessionID: "session-1", Workspace: workspace, OwnerKind: takeover.OwnerTUI},
		Start: func(context.Context, recovery.TransferIdentity) (recovery.SessionPauseStatus, error) {
			starts.Add(1)
			return recovery.SessionPauseStatus{}, context.DeadlineExceeded
		},
		Status: func(string) (recovery.SessionPauseStatus, error) {
			return recovery.SessionPauseStatus{}, &takeover.Error{Code: takeover.CodeStatusUnknown, Message: "unknown transfer"}
		},
		RetrySave: func(context.Context, string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	loc := takeoverLocation{stateDir: stateDir, workspace: workspace, sessionDir: sessionDir}
	status, err := preflightTakeoverTarget(t.Context(), loc, "session-1", "transfer-1", takeover.OwnerGateway, "binding-1")
	if err != nil || status.State != recovery.SessionPausePreflight || status.Workspace != workspace || status.SourceRuntimeID != "runtime-1" || status.SourceSessionEpoch != 2 || status.TargetInstanceID != "binding-1" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	status, err = preflightTakeoverTarget(t.Context(), loc, "session-1", "transfer-1", takeover.OwnerTUI, "target-1")
	if err == nil || status.State != recovery.SessionPausePreflight {
		t.Fatalf("same-owner status=%+v err=%v", status, err)
	}
	if starts.Load() != 0 {
		t.Fatalf("preflight dispatched %d start requests", starts.Load())
	}
	// Once start reaches the source, losing its admission outcome must retain
	// the guard rather than reuse the read-only preflight result.
	timeout := time.Duration(0)
	status, err = startTakeover(t.Context(), loc, "session-1", "transfer-1", takeover.OwnerGateway, "binding-1", &timeout)
	if err == nil || status.State != takeover.CodeStatusUnknown || starts.Load() != 1 {
		t.Fatalf("dispatched status=%+v err=%v starts=%d", status, err, starts.Load())
	}
}
