package lsp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/x/powernap/pkg/lsp/protocol"

	"github.com/keakon/chord/internal/config"
)

type lateDiagnosticsFixture struct {
	mgr    *Manager
	client *Client
	path   string
	uri    protocol.DocumentURI
}

func newLateDiagnosticsFixture(t *testing.T) *lateDiagnosticsFixture {
	t.Helper()
	root := resolvedTempDir(t)
	mgr := NewManager(&config.Config{LSP: config.LSPConfig{
		"gopls": {Command: "gopls", FileTypes: []string{".go"}},
	}}, root, nil)
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	uri := protocol.DocumentURI("file://" + filepath.ToSlash(path))
	client := &Client{cwd: root, diagnostics: map[protocol.DocumentURI][]protocol.Diagnostic{}}
	mgr.clientsMu.Lock()
	mgr.clients[testKey(mgr, "gopls")] = client
	mgr.clientsMu.Unlock()
	return &lateDiagnosticsFixture{mgr: mgr, client: client, path: path, uri: uri}
}

// publish mirrors a server publish: the client caches the set before the
// manager callback runs.
func (f *lateDiagnosticsFixture) publish(version int32, diags ...protocol.Diagnostic) {
	f.client.diagnosticsMu.Lock()
	f.client.diagnostics[f.uri] = diags
	f.client.diagnosticsMu.Unlock()
	f.mgr.onDiagnostics(testKey(f.mgr, "gopls"), nil)(string(f.uri), "", diags, version)
}

func lateDiag(severity protocol.DiagnosticSeverity, line uint32, msg string) protocol.Diagnostic {
	return protocol.Diagnostic{Severity: severity, Range: protocol.Range{Start: protocol.Position{Line: line}}, Message: msg}
}

func TestLateDiagnosticsReportsOnlyDiagnosticsAddedForTheSyncedVersion(t *testing.T) {
	f := newLateDiagnosticsFixture(t)
	typeErr := lateDiag(protocol.SeverityError, 3, "undefined: missing")
	f.publish(2, typeErr)
	f.mgr.watchLateDiagnostics(f.path, map[string]int32{"gopls": 2})

	if got := f.mgr.PendingLateDiagnostics([]string{f.path}, ""); len(got) != 0 {
		t.Fatalf("pending before any later publish = %+v, want none", got)
	}
	f.publish(2, typeErr, lateDiag(protocol.SeverityWarning, 7, "this value is never used"))

	got := f.mgr.PendingLateDiagnostics([]string{f.path}, filepath.Dir(f.path))
	if len(got) != 1 {
		t.Fatalf("pending = %+v, want one entry", got)
	}
	if !strings.Contains(got[0].Lines, "this value is never used") || strings.Contains(got[0].Lines, "undefined: missing") {
		t.Fatalf("lines = %q, want only the later analyzer diagnostic", got[0].Lines)
	}
	if got[0].DisplayPath != "main.go" {
		t.Fatalf("display path = %q, want main.go", got[0].DisplayPath)
	}
	if len(got[0].Reviews) != 1 || got[0].Reviews[0].Errors != 1 || got[0].Reviews[0].Warnings != 1 {
		t.Fatalf("reviews = %+v, want the current gopls counts", got[0].Reviews)
	}
}

func TestLateDiagnosticsAckConsumesAndLaterPublishReportsOnlyNewer(t *testing.T) {
	f := newLateDiagnosticsFixture(t)
	f.publish(2)
	f.mgr.watchLateDiagnostics(f.path, map[string]int32{"gopls": 2})
	first := lateDiag(protocol.SeverityWarning, 1, "first analyzer finding")
	f.publish(2, first)

	peeked := f.mgr.PendingLateDiagnostics([]string{f.path}, "")
	if again := f.mgr.PendingLateDiagnostics([]string{f.path}, ""); len(again) != 1 || len(peeked) != 1 {
		t.Fatalf("peek consumed pending diagnostics: first=%+v second=%+v", peeked, again)
	}
	f.mgr.AckLateDiagnostics(peeked)
	if got := f.mgr.PendingLateDiagnostics([]string{f.path}, ""); len(got) != 0 {
		t.Fatalf("pending after ack = %+v, want none", got)
	}

	f.publish(2, first, lateDiag(protocol.SeverityWarning, 9, "second analyzer finding"))
	got := f.mgr.PendingLateDiagnostics([]string{f.path}, "")
	if len(got) != 1 || strings.Contains(got[0].Lines, "first analyzer finding") || !strings.Contains(got[0].Lines, "second analyzer finding") {
		t.Fatalf("pending after newer publish = %+v, want only the newer finding", got)
	}
}

func TestLateDiagnosticsAckKeepsPendingWhenPublishArrivedAfterPeek(t *testing.T) {
	f := newLateDiagnosticsFixture(t)
	f.publish(2)
	f.mgr.watchLateDiagnostics(f.path, map[string]int32{"gopls": 2})
	first := lateDiag(protocol.SeverityWarning, 1, "first analyzer finding")
	f.publish(2, first)
	peeked := f.mgr.PendingLateDiagnostics([]string{f.path}, "")
	f.publish(2, first, lateDiag(protocol.SeverityWarning, 9, "second analyzer finding"))

	f.mgr.AckLateDiagnostics(peeked)
	got := f.mgr.PendingLateDiagnostics([]string{f.path}, "")
	if len(got) != 1 || !strings.Contains(got[0].Lines, "second analyzer finding") || strings.Contains(got[0].Lines, "first analyzer finding") {
		t.Fatalf("pending = %+v, want the publish that raced the ack kept", got)
	}
}

func TestLateDiagnosticsIgnoresOtherVersionsAndPublishesAfterDeadline(t *testing.T) {
	f := newLateDiagnosticsFixture(t)
	f.publish(2)
	f.mgr.watchLateDiagnostics(f.path, map[string]int32{"gopls": 2})
	f.publish(3, lateDiag(protocol.SeverityWarning, 1, "newer version"))
	if got := f.mgr.PendingLateDiagnostics([]string{f.path}, ""); len(got) != 0 {
		t.Fatalf("pending after another version = %+v, want none", got)
	}

	f.mgr.lateMu.Lock()
	f.mgr.lateWatches[f.path].deadline = time.Now().Add(-time.Second)
	f.mgr.lateMu.Unlock()
	f.publish(2, lateDiag(protocol.SeverityWarning, 1, "too late"))
	if got := f.mgr.PendingLateDiagnostics([]string{f.path}, ""); len(got) != 0 {
		t.Fatalf("pending after deadline = %+v, want none", got)
	}
}

func TestLateDiagnosticsSkipsPathsNotRequestedOrStale(t *testing.T) {
	f := newLateDiagnosticsFixture(t)
	f.publish(2)
	f.mgr.watchLateDiagnostics(f.path, map[string]int32{"gopls": 2})
	f.publish(2, lateDiag(protocol.SeverityWarning, 1, "analyzer finding"))

	if got := f.mgr.PendingLateDiagnostics([]string{filepath.Join(filepath.Dir(f.path), "other.go")}, ""); len(got) != 0 {
		t.Fatalf("pending for an unwritten path = %+v, want none", got)
	}
	f.mgr.diagMu.Lock()
	state := f.mgr.diagState[f.path]
	state.stale = true
	f.mgr.diagState[f.path] = state
	f.mgr.diagMu.Unlock()
	if got := f.mgr.PendingLateDiagnostics([]string{f.path}, ""); len(got) != 0 {
		t.Fatalf("pending for a file changed on disk = %+v, want none", got)
	}
}

func TestLateDiagnosticsNewSyncReplacesWatchAndStaleAckIsIgnored(t *testing.T) {
	f := newLateDiagnosticsFixture(t)
	f.publish(2)
	f.mgr.watchLateDiagnostics(f.path, map[string]int32{"gopls": 2})
	f.publish(2, lateDiag(protocol.SeverityWarning, 1, "old version finding"))
	stale := f.mgr.PendingLateDiagnostics([]string{f.path}, "")

	f.mgr.dropLateDiagnostics(f.path)
	if got := f.mgr.PendingLateDiagnostics([]string{f.path}, ""); len(got) != 0 {
		t.Fatalf("pending after a new sync = %+v, want none", got)
	}

	f.mgr.watchLateDiagnostics(f.path, map[string]int32{"gopls": 4})
	f.publish(4, lateDiag(protocol.SeverityWarning, 1, "old version finding"), lateDiag(protocol.SeverityWarning, 2, "new version finding"))
	f.mgr.AckLateDiagnostics(stale)
	got := f.mgr.PendingLateDiagnostics([]string{f.path}, "")
	if len(got) != 1 || !strings.Contains(got[0].Lines, "new version finding") {
		t.Fatalf("pending = %+v, want the replacing watch untouched by the stale ack", got)
	}
}

func TestLateDiagnosticsInformationOnlyDoesNotTriggerButRidesAlong(t *testing.T) {
	f := newLateDiagnosticsFixture(t)
	f.publish(2)
	f.mgr.watchLateDiagnostics(f.path, map[string]int32{"gopls": 2})
	hint := lateDiag(protocol.SeverityHint, 2, "loop can be simplified")
	f.publish(2, hint, lateDiag(protocol.SeverityInformation, 4, "var is unused"))
	if got := f.mgr.PendingLateDiagnostics([]string{f.path}, ""); len(got) != 0 {
		t.Fatalf("pending with only information and hints = %+v, want none", got)
	}

	f.publish(2, hint, lateDiag(protocol.SeverityInformation, 4, "var is unused"), lateDiag(protocol.SeverityWarning, 6, "printf arg has wrong type"))
	got := f.mgr.PendingLateDiagnostics([]string{f.path}, "")
	if len(got) != 1 {
		t.Fatalf("pending = %+v, want one entry once a warning appeared", got)
	}
	for _, want := range []string{"printf arg has wrong type", "loop can be simplified", "var is unused"} {
		if !strings.Contains(got[0].Lines, want) {
			t.Fatalf("lines = %q, want %q alongside the warning", got[0].Lines, want)
		}
	}
}

// deliverLate runs one write of the fixture file at version through its late
// publish, peeks and acks; it returns what was delivered.
func (f *lateDiagnosticsFixture) deliverLate(version int32, late ...protocol.Diagnostic) []LateDiagnostics {
	f.mgr.dropLateDiagnostics(f.path)
	f.publish(version)
	f.mgr.watchLateDiagnostics(f.path, map[string]int32{"gopls": version})
	f.publish(version, late...)
	got := f.mgr.PendingLateDiagnostics([]string{f.path}, "")
	f.mgr.AckLateDiagnostics(got)
	return got
}

func TestLateDiagnosticsDeliveredFindingIsNotRepeatedWhenAnEditMovesIt(t *testing.T) {
	f := newLateDiagnosticsFixture(t)
	if got := f.deliverLate(2, lateDiag(protocol.SeverityWarning, 7, "printf arg has wrong type")); len(got) != 1 {
		t.Fatalf("first delivery = %+v, want one entry", got)
	}

	moved := lateDiag(protocol.SeverityWarning, 9, "printf arg has wrong type")
	if got := f.deliverLate(3, moved); len(got) != 0 {
		t.Fatalf("moved finding was delivered again: %+v", got)
	}
	got := f.deliverLate(4, moved, lateDiag(protocol.SeverityError, 12, "unreachable code"))
	if len(got) != 1 || strings.Contains(got[0].Lines, "printf arg") || !strings.Contains(got[0].Lines, "unreachable code") {
		t.Fatalf("delivery = %+v, want only the new finding", got)
	}
}

func TestLateDiagnosticsFindingIsReportedAgainAfterItLeftTheFile(t *testing.T) {
	f := newLateDiagnosticsFixture(t)
	finding := lateDiag(protocol.SeverityWarning, 7, "printf arg has wrong type")
	f.deliverLate(2, finding)
	// The fix lands: the version's final publish no longer carries the finding.
	f.deliverLate(3, lateDiag(protocol.SeverityInformation, 1, "var is unused"))

	if got := f.deliverLate(4, finding); len(got) != 1 || !strings.Contains(got[0].Lines, "printf arg") {
		t.Fatalf("reintroduced finding = %+v, want it reported again", got)
	}
}

func TestLateDiagnosticsQuietWindowCountsAsCompleteButAnOpenWindowDoesNot(t *testing.T) {
	finding := lateDiag(protocol.SeverityWarning, 7, "printf arg has wrong type")
	for _, tc := range []struct {
		name    string
		expired bool
		want    int
	}{{"quiet window expired", true, 1}, {"window still open", false, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLateDiagnosticsFixture(t)
			f.deliverLate(2, finding)
			// The next version's analysis pass never republished: gopls skips a
			// publish identical to the type-check pass.
			f.mgr.dropLateDiagnostics(f.path)
			f.publish(3)
			f.mgr.watchLateDiagnostics(f.path, map[string]int32{"gopls": 3})
			if tc.expired {
				f.mgr.lateMu.Lock()
				f.mgr.lateWatches[f.path].deadline = time.Now().Add(-time.Second)
				f.mgr.lateMu.Unlock()
			}

			if got := f.deliverLate(4, finding); len(got) != tc.want {
				t.Fatalf("delivery = %+v, want %d entries", got, tc.want)
			}
		})
	}
}

func TestAwaitLateDiagnosticsWaitsOnlyForPendingAnalysisPasses(t *testing.T) {
	f := newLateDiagnosticsFixture(t)
	f.publish(2)
	f.mgr.watchLateDiagnostics(f.path, map[string]int32{"gopls": 2})

	if !f.mgr.LateDiagnosticsAwaitable([]string{f.path}, time.Hour) {
		t.Fatal("a write still owed its analysis pass is not awaitable")
	}
	done := make(chan struct{})
	go func() {
		f.mgr.AwaitLateDiagnostics(context.Background(), []string{f.path}, time.Hour)
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("returned before the analysis pass arrived")
	case <-time.After(50 * time.Millisecond):
	}
	f.publish(2, lateDiag(protocol.SeverityWarning, 1, "analyzer finding"))
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the analysis pass did not end the wait")
	}

	if f.mgr.LateDiagnosticsAwaitable([]string{f.path}, time.Hour) {
		t.Fatal("a write whose analysis pass arrived is still awaitable")
	}
	start := time.Now()
	f.mgr.AwaitLateDiagnostics(context.Background(), []string{f.path}, time.Hour)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("a path whose pass already arrived waited %v", elapsed)
	}
}

func TestAwaitLateDiagnosticsBoundsAndCancellation(t *testing.T) {
	f := newLateDiagnosticsFixture(t)
	f.publish(2)
	f.mgr.watchLateDiagnostics(f.path, map[string]int32{"gopls": 2})

	start := time.Now()
	f.mgr.AwaitLateDiagnostics(context.Background(), []string{f.path}, 100*time.Millisecond)
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond || elapsed > 5*time.Second {
		t.Fatalf("window-bounded wait took %v, want about the window", elapsed)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start = time.Now()
	f.mgr.AwaitLateDiagnostics(ctx, []string{f.path}, time.Hour)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("cancelled wait took %v", elapsed)
	}

	other := newLateDiagnosticsFixture(t)
	other.mgr.cfg.LSP = config.LSPConfig{"pyright": {Command: "pyright-langserver"}}
	other.mgr.watchLateDiagnostics(other.path, map[string]int32{"pyright": 2})
	start = time.Now()
	other.mgr.AwaitLateDiagnostics(context.Background(), []string{other.path}, time.Hour)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("a single-pass server held the wait for %v", elapsed)
	}
}

func TestResetReportedDiagnosticsForgetsLateState(t *testing.T) {
	f := newLateDiagnosticsFixture(t)
	finding := lateDiag(protocol.SeverityWarning, 7, "printf arg has wrong type")
	f.deliverLate(2, finding)
	f.mgr.ResetReportedDiagnostics()
	f.mgr.lateMu.Lock()
	watches, reported := len(f.mgr.lateWatches), len(f.mgr.lateReported)
	f.mgr.lateMu.Unlock()
	if watches != 0 || reported != 0 {
		t.Fatalf("after reset watches=%d reported=%d, want none", watches, reported)
	}
	if got := f.deliverLate(3, finding); len(got) != 1 {
		t.Fatalf("delivery in a new session = %+v, want the finding reported", got)
	}
}

func TestSyncAfterWriteDropsLateDiagnosticsWatch(t *testing.T) {
	f := newLateDiagnosticsFixture(t)
	f.mgr.watchLateDiagnostics(f.path, map[string]int32{"gopls": 2})

	restoreNotify, restoreChange, restoreSave := afterWriteNotifyWatchedFileChanged, afterWriteDidChange, afterWriteDidSave
	t.Cleanup(func() {
		afterWriteNotifyWatchedFileChanged, afterWriteDidChange, afterWriteDidSave = restoreNotify, restoreChange, restoreSave
	})
	afterWriteNotifyWatchedFileChanged = func(*Manager, context.Context, string, protocol.FileChangeType) map[string]error { return nil }
	afterWriteDidChange = func(*Manager, context.Context, string, string) (map[string]int32, map[string]error) {
		return map[string]int32{"gopls": 3}, nil
	}
	afterWriteDidSave = func(*Manager, context.Context, string, string) map[string]error { return nil }

	f.mgr.syncAfterWrite(context.Background(), f.path, "package main\n", WatchedFileChanged, true)
	f.mgr.lateMu.Lock()
	_, watched := f.mgr.lateWatches[f.path]
	f.mgr.lateMu.Unlock()
	if watched {
		t.Fatal("a new sync kept the previous version's late-diagnostics watch")
	}
}

func TestAwaitPreparedAfterFileWriteWatchesOnlyNotifiedWrites(t *testing.T) {
	for _, tc := range []struct {
		name     string
		notified bool
	}{{"notified", true}, {"timed out", false}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLateDiagnosticsFixture(t)
			restore := afterWriteAwaitWaiter
			t.Cleanup(func() { afterWriteAwaitWaiter = restore })
			afterWriteAwaitWaiter = func(*Manager, context.Context, string, chan diagnosticsEvent, diagnosticsWaitRequest, time.Duration) ([]Diagnostic, bool) {
				return nil, tc.notified
			}
			prepared := afterWritePrepared{path: f.path, base: "ok", wait: true, waitTimeout: time.Second, sync: afterWriteSync{serverVersions: map[string]int32{"gopls": 2}}}

			f.mgr.awaitPreparedAfterFileWrite(context.Background(), &prepared, false, "")
			f.mgr.lateMu.Lock()
			_, watched := f.mgr.lateWatches[f.path]
			f.mgr.lateMu.Unlock()
			if watched != tc.notified {
				t.Fatalf("watched = %v, want %v", watched, tc.notified)
			}
		})
	}
}
