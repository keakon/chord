package lsp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/keakon/x/powernap/pkg/lsp/protocol"

	"github.com/keakon/chord/internal/config"
)

func TestNoteLSPDegradationDedupesPerServerKindAndResets(t *testing.T) {
	mgr := NewManager(&config.Config{}, t.TempDir(), nil)

	first := mgr.noteLSPDegradation(lspDegradationStart, "gopls", "not started")
	if !strings.Contains(first, "gopls") || !strings.Contains(first, "do not treat this edit as verified") {
		t.Fatalf("first note = %q", first)
	}
	if again := mgr.noteLSPDegradation(lspDegradationStart, "gopls", "init timeout"); again != "" {
		t.Fatalf("repeat note = %q, want empty", again)
	}
	if other := mgr.noteLSPDegradation(lspDegradationTimeout, "gopls", ""); other == "" {
		t.Fatal("a timeout is a different kind and must not be deduped into the start failure")
	}
	if otherServer := mgr.noteLSPDegradation(lspDegradationStart, "pyright", ""); otherServer == "" {
		t.Fatal("notes must not be deduped across servers")
	}

	mgr.ResetReportedDiagnostics()
	if got := mgr.noteLSPDegradation(lspDegradationStart, "gopls", ""); got == "" {
		t.Fatal("ResetReportedDiagnostics should clear the degradation notes")
	}

	mgr.RestoreReportedDiagnostics(nil)
	if got := mgr.noteLSPDegradation(lspDegradationStart, "gopls", ""); got == "" {
		t.Fatal("RestoreReportedDiagnostics should clear the degradation notes")
	}
}

func TestAfterFileWriteToolResultNamesStartingServerWithoutSpendingTheNote(t *testing.T) {
	mgr, path, _ := newAfterWriteTestManager(t)
	key := testKey(mgr, "gopls")
	mgr.clientsMu.Lock()
	delete(mgr.clients, key)
	mgr.starting[key] = true
	mgr.clientsMu.Unlock()

	origStart := afterWriteStart
	origWait := afterWriteWaitForClient
	t.Cleanup(func() {
		afterWriteStart = origStart
		afterWriteWaitForClient = origWait
	})
	afterWriteStart = func(*Manager, context.Context, string) {}
	afterWriteWaitForClient = func(*Manager, context.Context, string, time.Duration) (*Client, bool) {
		return nil, false
	}

	const starting = "LSP diagnostics unavailable for this edit (gopls: still starting); do not treat this edit as verified."
	for range 2 {
		out := mgr.AfterFileWriteToolResult(context.Background(), path, "package main", "Successfully wrote 12 bytes", false, WatchedFileChanged, "")
		if !strings.HasSuffix(out, starting) {
			t.Fatalf("starting note = %q, want suffix %q", out, starting)
		}
	}

	// The launch then fails: the failure still gets its one line.
	mgr.clientsMu.Lock()
	delete(mgr.starting, key)
	mgr.clientsMu.Unlock()
	mgr.startFailMu.Lock()
	mgr.startFail[key] = "exec: gopls not found"
	mgr.startFailMu.Unlock()
	out := mgr.AfterFileWriteToolResult(context.Background(), path, "package main", "Successfully wrote 12 bytes", false, WatchedFileChanged, "")
	if !strings.Contains(out, "(gopls: exec: gopls not found)") {
		t.Fatalf("start failure after a slow launch = %q, want the failure line", out)
	}
}

func TestAfterFileWriteToolResultReportsExitedServerOnce(t *testing.T) {
	mgr, path, client := newAfterWriteTestManager(t)
	client.name = "gopls"
	fake := &stoppedPowernapClient{}
	client.client = fake

	origStart := afterWriteStart
	origWait := afterWriteWaitForClient
	t.Cleanup(func() {
		afterWriteStart = origStart
		afterWriteWaitForClient = origWait
	})
	var started int
	afterWriteStart = func(*Manager, context.Context, string) { started++ }
	afterWriteWaitForClient = func(*Manager, context.Context, string, time.Duration) (*Client, bool) {
		return nil, false
	}

	out := mgr.AfterFileWriteToolResult(context.Background(), path, "package main", "Successfully wrote 12 bytes", false, WatchedFileChanged, "")
	const want = "LSP diagnostics unavailable for this edit (gopls: server exited; restarting); do not treat this edit as verified."
	if !strings.Contains(out, want) {
		t.Fatalf("exited note = %q, want %q", out, want)
	}
	mgr.clientsMu.RLock()
	_, stillRegistered := mgr.clients[testKey(mgr, "gopls")]
	mgr.clientsMu.RUnlock()
	if stillRegistered {
		t.Fatal("the dead client must be removed so Start can relaunch the server")
	}
	if started != 1 {
		t.Fatalf("Start calls = %d, want 1 after pruning the dead client", started)
	}
	if fake.kills == 0 && fake.exits == 0 {
		t.Fatal("the dead client must be closed")
	}
	if got := DegradationNotes(out); len(got) != 1 || got[0] != want {
		t.Fatalf("DegradationNotes = %q, want [%q]", got, want)
	}
}

type stoppedPowernapClient struct{ fakePowernapClient }

func (*stoppedPowernapClient) IsRunning() bool { return false }

func TestAfterFileWriteToolResultReportsStartFailureOnce(t *testing.T) {
	mgr, path, _ := newAfterWriteTestManager(t)
	mgr.clientsMu.Lock()
	delete(mgr.clients, testKey(mgr, "gopls"))
	mgr.clientsMu.Unlock()

	origStart := afterWriteStart
	origWait := afterWriteWaitForClient
	t.Cleanup(func() {
		afterWriteStart = origStart
		afterWriteWaitForClient = origWait
	})
	afterWriteStart = func(*Manager, context.Context, string) {}
	afterWriteWaitForClient = func(*Manager, context.Context, string, time.Duration) (*Client, bool) {
		return nil, false
	}

	const want = "LSP diagnostics unavailable for this edit (gopls: not started); do not treat this edit as verified."
	out := mgr.AfterFileWriteToolResult(context.Background(), path, "package main", "Successfully wrote 12 bytes", false, WatchedFileChanged, "")
	if !strings.HasSuffix(out, want) {
		t.Fatalf("start-failure note = %q, want suffix %q", out, want)
	}

	repeat := mgr.AfterFileWriteToolResult(context.Background(), path, "package main", "Successfully wrote 12 bytes", false, WatchedFileChanged, "")
	if strings.Contains(repeat, "LSP diagnostics unavailable") {
		t.Fatalf("start failure should be reported once per server: %q", repeat)
	}
	if repeat != "Successfully wrote 12 bytes" {
		t.Fatalf("repeat output = %q, want the untouched base result", repeat)
	}
}

func TestAfterFileWriteToolResultReportsTimeoutOnce(t *testing.T) {
	mgr, path, client := newAfterWriteTestManager(t)
	client.name = "gopls"

	origStart := afterWriteStart
	origWait := afterWriteWaitForClient
	origDidChange := afterWriteDidChange
	origNotify := afterWriteNotifyWatchedFileChanged
	origAwait := afterWriteAwaitWaiter
	t.Cleanup(func() {
		afterWriteStart = origStart
		afterWriteWaitForClient = origWait
		afterWriteDidChange = origDidChange
		afterWriteNotifyWatchedFileChanged = origNotify
		afterWriteAwaitWaiter = origAwait
	})
	afterWriteStart = func(*Manager, context.Context, string) {}
	afterWriteWaitForClient = func(*Manager, context.Context, string, time.Duration) (*Client, bool) {
		return client, true
	}
	afterWriteDidChange = func(*Manager, context.Context, string, string) (map[string]int32, error) {
		return nil, nil
	}
	afterWriteNotifyWatchedFileChanged = func(*Manager, context.Context, string, protocol.FileChangeType) error {
		return nil
	}
	afterWriteAwaitWaiter = func(*Manager, context.Context, string, chan diagnosticsEvent, diagnosticsWaitRequest, time.Duration) ([]Diagnostic, bool) {
		return nil, false
	}

	const want = "LSP diagnostics unavailable for this edit (language server: no diagnostics within 3s); do not treat this edit as verified."
	out := mgr.AfterFileWriteToolResult(context.Background(), path, "package main", "Successfully wrote 12 bytes", false, WatchedFileChanged, "")
	if !strings.Contains(out, want) {
		t.Fatalf("timeout note = %q, want %q", out, want)
	}

	repeat := mgr.AfterFileWriteToolResult(context.Background(), path, "package main", "Successfully wrote 12 bytes", false, WatchedFileChanged, "")
	if strings.Contains(repeat, "LSP diagnostics unavailable") {
		t.Fatalf("timeout should be reported once per session: %q", repeat)
	}
}

func TestAfterFileWriteToolResultCanceledContextAddsNoDegradationNote(t *testing.T) {
	mgr, path, client := newAfterWriteTestManager(t)

	origStart := afterWriteStart
	origWait := afterWriteWaitForClient
	origDidChange := afterWriteDidChange
	origNotify := afterWriteNotifyWatchedFileChanged
	origAwait := afterWriteAwaitWaiter
	t.Cleanup(func() {
		afterWriteStart = origStart
		afterWriteWaitForClient = origWait
		afterWriteDidChange = origDidChange
		afterWriteNotifyWatchedFileChanged = origNotify
		afterWriteAwaitWaiter = origAwait
	})
	afterWriteStart = func(*Manager, context.Context, string) {}
	afterWriteDidChange = func(*Manager, context.Context, string, string) (map[string]int32, error) {
		return nil, nil
	}
	afterWriteNotifyWatchedFileChanged = func(*Manager, context.Context, string, protocol.FileChangeType) error {
		return nil
	}
	afterWriteAwaitWaiter = func(*Manager, context.Context, string, chan diagnosticsEvent, diagnosticsWaitRequest, time.Duration) ([]Diagnostic, bool) {
		return nil, false
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	afterWriteWaitForClient = func(*Manager, context.Context, string, time.Duration) (*Client, bool) {
		return nil, false
	}
	if out := mgr.AfterFileWriteToolResult(ctx, path, "package main", "Successfully wrote 12 bytes", false, WatchedFileChanged, ""); out != "Successfully wrote 12 bytes" {
		t.Fatalf("cancelled start failure output = %q, want the untouched base result", out)
	}

	afterWriteWaitForClient = func(*Manager, context.Context, string, time.Duration) (*Client, bool) {
		return client, true
	}
	if out := mgr.AfterFileWriteToolResult(ctx, path, "package main", "Successfully wrote 12 bytes", false, WatchedFileChanged, ""); out != "Successfully wrote 12 bytes" {
		t.Fatalf("cancelled timeout output = %q, want the untouched base result", out)
	}
}
