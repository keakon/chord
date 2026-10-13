package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/takeover"
)

func TestTakeoverRepeatedSaveRestoreReclaimsRuntime(t *testing.T) {
	if !takeover.Supported() {
		t.Skip("local session takeover requires macOS or Linux")
	}
	withTestStateDir(t)
	configText := "providers:\n  sample:\n    type: messages\n    models:\n      test-model: {}\nmodel_pools:\n  default: [sample/test-model]\n"
	if err := os.WriteFile(filepath.Join(flagConfigHome, "config.yaml"), []byte(configText), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp("/tmp", "ch-stress-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	flagStateDir = filepath.Join(root, "state")
	flagSessionsDir = filepath.Join(flagStateDir, "sessions")
	t.Setenv("CHORD_STATE_DIR", flagStateDir)
	t.Setenv("CHORD_SESSIONS_DIR", flagSessionsDir)
	if err := os.MkdirAll(flagStateDir, 0700); err != nil {
		t.Fatal(err)
	}
	countFD := func() int {
		for _, dir := range []string{"/proc/self/fd", "/dev/fd"} {
			if entries, err := os.ReadDir(dir); err == nil {
				return len(entries)
			}
		}
		t.Skip("descriptor directory unavailable")
		return 0
	}
	beforeFD, beforeWorkers := countFD(), runtime.NumGoroutine()
	for iteration := range 100 {
		for _, retry := range []bool{false, true} {
			t.Run(fmt.Sprintf("%03d/retry=%t", iteration, retry), func(t *testing.T) {
				workspace, err := os.MkdirTemp(root, "workspace-")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.RemoveAll(workspace) })
				runTakeoverStressCycle(t, workspace, retry)
			})
			if t.Failed() {
				return
			}
		}
	}
	// All explicit workers have joined. Allow context watchers already notified
	// by cancellation to leave the scheduler before checking aggregate residue.
	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > beforeWorkers+2 && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if after := runtime.NumGoroutine(); after > beforeWorkers+2 {
		t.Fatalf("runtime worker leak: before=%d after=%d", beforeWorkers, after)
	}
	if after := countFD(); after > beforeFD+1 {
		t.Fatalf("descriptor leak: before=%d after=%d", beforeFD, after)
	}
	entries, err := os.ReadDir(filepath.Join(flagStateDir, "takeover"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("endpoint artifacts remain: entries=%d error=%v", len(entries), err)
	}
}

func runTakeoverStressCycle(t *testing.T, workspace string, retry bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	ac, provider := takeoverProcessApp(t, workspace, false)
	runDone, outputDone := make(chan error, 1), make(chan struct{})
	statuses := make(chan agent.SessionPauseStatus, 16)
	outputErrors := make(chan error, 1)
	go func() {
		defer close(outputDone)
		for event := range ac.MainAgent.Events() {
			switch event := event.(type) {
			case agent.SessionTransferEvent:
				statuses <- event.Status
			case agent.ErrorEvent:
				select {
				case outputErrors <- event.Err:
				default:
				}
			}
		}
	}()
	go func() { runDone <- ac.MainAgent.Run(ac.Ctx) }()
	endpoint, err := startRuntimeTakeover(ac, takeover.OwnerTUI)
	if err != nil {
		ac.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { endpoint.close(); ac.Close(); <-outputDone })
	ac.MainAgent.SendUserMessage("Start the request.")
	select {
	case <-provider.started:
	case <-ctx.Done():
		t.Fatal("source request never started")
	}
	if !ac.MainAgent.QueuePendingUserDraft("stress-draft", []message.ContentPart{{Type: message.ContentPartText, Text: "Queued instruction."}}) {
		t.Fatal("queued input rejected")
	}
	loc, err := locateTakeover(ctx, workspace, "sample-session")
	if err != nil {
		t.Fatal(err)
	}
	zero := time.Duration(0)
	status, err := startTakeover(ctx, loc, "sample-session", "transfer-stress", takeover.OwnerGateway, "binding-stress", &zero)
	if err != nil || status.State != agent.SessionPauseDraining {
		t.Fatalf("start state=%s error=%v", status.State, err)
	}
	if lock, err := recovery.AcquireSessionLock(ac.SessionDir); err == nil {
		_ = lock.Release()
		t.Fatal("draining source released ownership")
	}
	faultPath := filepath.Join(ac.SessionDir, "sync-fault")
	if retry {
		if err := os.Symlink(workspace, faultPath); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(workspace, "release-request"), []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	waitState := func(want string) agent.SessionPauseStatus {
		for {
			select {
			case status := <-statuses:
				if status.State == want {
					return status
				}
				if status.State == agent.SessionPauseSaveFailed && want != agent.SessionPauseSaveFailed {
					t.Fatalf("unexpected save failure: %s", status.Error)
				}
			case err := <-outputErrors:
				t.Fatalf("source output error: %v", err)
			case <-ctx.Done():
				t.Fatalf("missing %s status", want)
			}
		}
	}
	if retry {
		failed := waitState(agent.SessionPauseSaveFailed)
		if failed.RecoverableAction != agent.SessionPauseActionRetrySave {
			t.Fatalf("failure is not safely retryable: %+v", failed)
		}
		if lock, err := recovery.AcquireSessionLock(ac.SessionDir); err == nil {
			_ = lock.Release()
			t.Fatal("save failure released ownership")
		}
		if err := os.Remove(faultPath); err != nil {
			t.Fatal(err)
		}
		if _, err := (takeover.Client{StateDir: loc.stateDir, Record: endpoint.identity()}).RetrySave(ctx, status.TransferID); err != nil {
			t.Fatal(err)
		}
	}
	saved := waitState(agent.SessionPauseSaved)
	if saved.PendingInputCount != 1 || provider.calls.Load() != 1 {
		t.Fatal("save or retry lost input or replayed model work")
	}
	if err := ac.MainAgent.ReleasePausedSession(saved.TransferID); err != nil {
		t.Fatal(err)
	}
	if err := <-runDone; err != nil {
		t.Fatal(err)
	}
	<-outputDone
	if err := endpoint.server.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(endpoint.identity().SocketPath); !os.IsNotExist(err) {
		t.Fatal("closed endpoint retained its socket")
	}
	mainPath := filepath.Join(ac.SessionDir, identity.MainSessionLogFilename)
	transcript, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	ac.Close()
	if after, err := os.ReadFile(mainPath); err != nil || !bytes.Equal(transcript, after) {
		t.Fatal("source cleanup wrote after ownership release")
	}
	released, err := queryTakeover(ctx, loc, "sample-session", saved.TransferID)
	if err != nil || released.State != agent.SessionPauseReleased {
		t.Fatalf("released status=%s error=%v", released.State, err)
	}
	target, targetProvider := takeoverProcessApp(t, workspace, true)
	t.Cleanup(target.Close)
	ready, ok := pausedStartupStatus(target)
	if !ok || ready.State != agent.SessionPauseReady || ready.PendingInputCount != 1 || targetProvider.calls.Load() != 0 {
		t.Fatal("ordinary target restore did not preserve paused input")
	}
	if lock, err := recovery.AcquireSessionLock(target.SessionDir); err == nil {
		_ = lock.Release()
		t.Fatal("target did not claim unique ownership")
	}
	target.Close()
	lock, err := recovery.AcquireSessionLock(target.SessionDir)
	if err != nil {
		t.Fatal("target shutdown retained session lock")
	}
	_ = lock.Release()
}
