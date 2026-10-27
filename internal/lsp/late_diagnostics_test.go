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
	mgr     *Manager
	clients map[string]*Client
	path    string
	uri     protocol.DocumentURI
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
	f := &lateDiagnosticsFixture{mgr: mgr, clients: make(map[string]*Client), path: path, uri: uri}
	f.addServer("gopls")
	return f
}

func (f *lateDiagnosticsFixture) addServer(name string) *Client {
	client := &Client{cwd: f.mgr.projectRootPath(), cfg: config.LSPServerConfig{FileTypes: []string{".go"}}, diagnostics: map[protocol.DocumentURI][]protocol.Diagnostic{}}
	f.mgr.clientsMu.Lock()
	f.mgr.clients[clientKey{name: name, root: f.mgr.projectRootPath()}] = client
	f.mgr.clientsMu.Unlock()
	f.clients[name] = client
	return client
}

// publish mirrors a server publish: the client caches the set before the
// manager callback runs. It returns the manager-wide publish sequence.
func (f *lateDiagnosticsFixture) publish(server string, version int32, diags ...protocol.Diagnostic) uint64 {
	client := f.clients[server]
	client.diagnosticsMu.Lock()
	client.diagnostics[f.uri] = diags
	client.diagnosticsMu.Unlock()
	f.mgr.onDiagnostics(clientKey{name: server, root: f.mgr.projectRootPath()}, nil)(string(f.uri), "", diags, version)
	f.mgr.lateMu.Lock()
	seq := f.mgr.latePublishSeq
	f.mgr.lateMu.Unlock()
	return seq
}

// baseline snapshots what a wait sees now: the state after the publish that
// satisfied it, before any later analysis pass.
func (f *lateDiagnosticsFixture) baseline() map[diagnosticIdentity]struct{} {
	var set map[diagnosticIdentity]struct{}
	for _, d := range f.mgr.diagnosticsForPaths([]string{f.path})[f.path] {
		if set == nil {
			set = make(map[diagnosticIdentity]struct{})
		}
		set[diagnosticIdentityKey(d)] = struct{}{}
	}
	return set
}

func (f *lateDiagnosticsFixture) watch(serverVersions map[string]int32, afterSeq uint64) {
	f.mgr.watchLateDiagnostics(f.path, serverVersions, f.baseline(), afterSeq)
}

func lateDiag(severity protocol.DiagnosticSeverity, line uint32, msg string) protocol.Diagnostic {
	return protocol.Diagnostic{Severity: severity, Range: protocol.Range{Start: protocol.Position{Line: line}}, Message: msg}
}

func TestLateDiagnosticsReportsOnlyDiagnosticsAddedForTheSyncedVersion(t *testing.T) {
	f := newLateDiagnosticsFixture(t)
	typeErr := lateDiag(protocol.SeverityError, 3, "undefined: missing")
	seq := f.publish("gopls", 2, typeErr)
	f.watch(map[string]int32{"gopls": 2}, seq)

	if got := f.mgr.PendingLateDiagnostics([]string{f.path}, ""); len(got) != 0 {
		t.Fatalf("pending before any later publish = %+v, want none", got)
	}
	f.publish("gopls", 2, typeErr, lateDiag(protocol.SeverityWarning, 7, "this value is never used"))

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

func TestLateDiagnosticsReplaysAPublishThatRacedWatchRegistration(t *testing.T) {
	f := newLateDiagnosticsFixture(t)
	typeErr := lateDiag(protocol.SeverityError, 3, "undefined: missing")
	seq := f.publish("gopls", 2, typeErr)
	baseline := f.baseline()
	// The analysis pass lands after the wait returned but before the watch is
	// registered. The wait baseline is still the type-check set, so the new
	// analyzer finding must survive the replay.
	f.publish("gopls", 2, typeErr, lateDiag(protocol.SeverityWarning, 7, "printf arg has wrong type"))
	f.mgr.watchLateDiagnostics(f.path, map[string]int32{"gopls": 2}, baseline, seq)

	got := f.mgr.PendingLateDiagnostics([]string{f.path}, "")
	if len(got) != 1 || !strings.Contains(got[0].Lines, "printf arg has wrong type") {
		t.Fatalf("pending = %+v, want the publish that raced registration", got)
	}
}

func TestLateWatchDoesNotReplayThePublishTheWaitConsumed(t *testing.T) {
	f := newLateDiagnosticsFixture(t)
	warning := lateDiag(protocol.SeverityWarning, 1, "analyzer finding")
	seq := f.publish("gopls", 2, warning)
	f.watch(map[string]int32{"gopls": 2}, seq)

	f.mgr.lateMu.Lock()
	watch := f.mgr.lateWatches[f.path]
	lateGen, pending := watch.lateGen, watch.pending
	f.mgr.lateMu.Unlock()
	if lateGen != 0 || pending {
		t.Fatalf("the consumed publish was replayed: lateGen=%d pending=%v", lateGen, pending)
	}
}

func TestLateDiagnosticsAckConsumesAndLaterPublishReportsOnlyNewer(t *testing.T) {
	f := newLateDiagnosticsFixture(t)
	seq := f.publish("gopls", 2)
	f.watch(map[string]int32{"gopls": 2}, seq)
	first := lateDiag(protocol.SeverityWarning, 1, "first analyzer finding")
	f.publish("gopls", 2, first)

	peeked := f.mgr.PendingLateDiagnostics([]string{f.path}, "")
	if again := f.mgr.PendingLateDiagnostics([]string{f.path}, ""); len(again) != 1 || len(peeked) != 1 {
		t.Fatalf("peek consumed pending diagnostics: first=%+v second=%+v", peeked, again)
	}
	f.mgr.AckLateDiagnostics(peeked)
	if got := f.mgr.PendingLateDiagnostics([]string{f.path}, ""); len(got) != 0 {
		t.Fatalf("pending after ack = %+v, want none", got)
	}

	f.publish("gopls", 2, first, lateDiag(protocol.SeverityWarning, 9, "second analyzer finding"))
	got := f.mgr.PendingLateDiagnostics([]string{f.path}, "")
	if len(got) != 1 || strings.Contains(got[0].Lines, "first analyzer finding") || !strings.Contains(got[0].Lines, "second analyzer finding") {
		t.Fatalf("pending after newer publish = %+v, want only the newer finding", got)
	}
}

func TestLateDiagnosticsAckKeepsPendingWhenPublishArrivedAfterPeek(t *testing.T) {
	f := newLateDiagnosticsFixture(t)
	seq := f.publish("gopls", 2)
	f.watch(map[string]int32{"gopls": 2}, seq)
	first := lateDiag(protocol.SeverityWarning, 1, "first analyzer finding")
	f.publish("gopls", 2, first)
	peeked := f.mgr.PendingLateDiagnostics([]string{f.path}, "")
	f.publish("gopls", 2, first, lateDiag(protocol.SeverityWarning, 9, "second analyzer finding"))

	f.mgr.AckLateDiagnostics(peeked)
	got := f.mgr.PendingLateDiagnostics([]string{f.path}, "")
	if len(got) != 1 || !strings.Contains(got[0].Lines, "second analyzer finding") || strings.Contains(got[0].Lines, "first analyzer finding") {
		t.Fatalf("pending = %+v, want the publish that raced the ack kept", got)
	}
}

func TestLateDiagnosticsIgnoresOtherVersionsAndPublishesAfterDeadline(t *testing.T) {
	f := newLateDiagnosticsFixture(t)
	seq := f.publish("gopls", 2)
	f.watch(map[string]int32{"gopls": 2}, seq)
	f.publish("gopls", 3, lateDiag(protocol.SeverityWarning, 1, "newer version"))
	if got := f.mgr.PendingLateDiagnostics([]string{f.path}, ""); len(got) != 0 {
		t.Fatalf("pending after another version = %+v, want none", got)
	}

	f.mgr.lateMu.Lock()
	f.mgr.lateWatches[f.path].deadline = time.Now().Add(-time.Second)
	f.mgr.lateMu.Unlock()
	f.publish("gopls", 2, lateDiag(protocol.SeverityWarning, 1, "too late"))
	if got := f.mgr.PendingLateDiagnostics([]string{f.path}, ""); len(got) != 0 {
		t.Fatalf("pending after deadline = %+v, want none", got)
	}
}

func TestLateDiagnosticsSkipsPathsNotRequestedOrStale(t *testing.T) {
	f := newLateDiagnosticsFixture(t)
	seq := f.publish("gopls", 2)
	f.watch(map[string]int32{"gopls": 2}, seq)
	f.publish("gopls", 2, lateDiag(protocol.SeverityWarning, 1, "analyzer finding"))

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
	seq := f.publish("gopls", 2)
	f.watch(map[string]int32{"gopls": 2}, seq)
	f.publish("gopls", 2, lateDiag(protocol.SeverityWarning, 1, "old version finding"))
	stale := f.mgr.PendingLateDiagnostics([]string{f.path}, "")

	f.mgr.dropLateDiagnostics(f.path)
	if got := f.mgr.PendingLateDiagnostics([]string{f.path}, ""); len(got) != 0 {
		t.Fatalf("pending after a new sync = %+v, want none", got)
	}

	seq = f.publish("gopls", 4)
	f.watch(map[string]int32{"gopls": 4}, seq)
	f.publish("gopls", 4, lateDiag(protocol.SeverityWarning, 1, "old version finding"), lateDiag(protocol.SeverityWarning, 2, "new version finding"))
	f.mgr.AckLateDiagnostics(stale)
	got := f.mgr.PendingLateDiagnostics([]string{f.path}, "")
	if len(got) != 1 || !strings.Contains(got[0].Lines, "new version finding") {
		t.Fatalf("pending = %+v, want the replacing watch untouched by the stale ack", got)
	}
}

func TestLateDiagnosticsInformationOnlyDoesNotTriggerButRidesAlong(t *testing.T) {
	f := newLateDiagnosticsFixture(t)
	seq := f.publish("gopls", 2)
	f.watch(map[string]int32{"gopls": 2}, seq)
	hint := lateDiag(protocol.SeverityHint, 2, "loop can be simplified")
	f.publish("gopls", 2, hint, lateDiag(protocol.SeverityInformation, 4, "var is unused"))
	if got := f.mgr.PendingLateDiagnostics([]string{f.path}, ""); len(got) != 0 {
		t.Fatalf("pending with only information and hints = %+v, want none", got)
	}

	f.publish("gopls", 2, hint, lateDiag(protocol.SeverityInformation, 4, "var is unused"), lateDiag(protocol.SeverityWarning, 6, "printf arg has wrong type"))
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
	seq := f.publish("gopls", version)
	f.watch(map[string]int32{"gopls": version}, seq)
	f.publish("gopls", version, late...)
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

func TestLatePruneKeepsFindingAnotherServerStillReports(t *testing.T) {
	f := newLateDiagnosticsFixture(t)
	f.addServer("other")
	finding := lateDiag(protocol.SeverityWarning, 7, "printf arg has wrong type")
	key := lateFindingKeyOf(Diagnostic{Severity: int(protocol.SeverityWarning), Message: finding.Message})
	f.mgr.AckLateDiagnostics([]LateDiagnostics{{
		Path:           f.path,
		keys:           []lateFindingKey{key},
		findingServers: map[lateFindingKey]map[string]struct{}{key: {"gopls": {}, "other": {}}},
	}})

	// Only the other server still reports the finding.
	f.publish("other", 2, finding)
	f.mgr.pruneLateFindingsForServer(f.path, "gopls")
	f.mgr.lateMu.Lock()
	_, kept := f.mgr.lateReported[f.path][key]
	f.mgr.lateMu.Unlock()
	if !kept {
		t.Fatal("pruning gopls released a finding the other server still reports")
	}

	// Both servers have dropped it; a later publish must be delivered again.
	f.publish("other", 2)
	f.mgr.pruneLateFindingsForServer(f.path, "other")
	f.mgr.lateMu.Lock()
	_, kept = f.mgr.lateReported[f.path][key]
	f.mgr.lateMu.Unlock()
	if kept {
		t.Fatal("the finding stayed suppressed after every server dropped it")
	}

	f.mgr.dropLateDiagnostics(f.path)
	seq := f.publish("gopls", 3)
	f.mgr.watchLateDiagnostics(f.path, map[string]int32{"gopls": 3}, nil, seq)
	f.publish("gopls", 3, finding)
	got := f.mgr.PendingLateDiagnostics([]string{f.path}, "")
	if len(got) != 1 || !strings.Contains(got[0].Lines, "printf arg") {
		t.Fatalf("reintroduced finding = %+v, want it reported again", got)
	}
}

func TestLateDiagnosticsKeepsSameServerNameRootsIsolated(t *testing.T) {
	f := newLateDiagnosticsFixture(t)
	nested := filepath.Join(f.mgr.projectRootPath(), "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, ".git"), []byte("gitdir: ../.git\n"), 0o644); err != nil {
		t.Fatalf("WriteFile .git: %v", err)
	}
	nestedPath := filepath.Join(nested, "main.go")
	if err := os.WriteFile(nestedPath, []byte("package nested\n"), 0o644); err != nil {
		t.Fatalf("WriteFile nested file: %v", err)
	}
	nestedURI := protocol.DocumentURI("file://" + filepath.ToSlash(nestedPath))
	rootClient := f.clients["gopls"]
	nestedClient := &Client{
		cwd:         nested,
		cfg:         config.LSPServerConfig{FileTypes: []string{".go"}},
		diagnostics: map[protocol.DocumentURI][]protocol.Diagnostic{},
	}
	f.mgr.clientsMu.Lock()
	f.mgr.clients[clientKey{name: "gopls", root: nested}] = nestedClient
	f.mgr.clientsMu.Unlock()

	rootFinding := lateDiag(protocol.SeverityWarning, 1, "root finding")
	nestedFinding := lateDiag(protocol.SeverityWarning, 2, "nested finding")
	rootClient.diagnosticsMu.Lock()
	rootClient.diagnostics[f.uri] = []protocol.Diagnostic{rootFinding}
	rootClient.diagnosticsMu.Unlock()
	nestedClient.diagnosticsMu.Lock()
	nestedClient.diagnostics[nestedURI] = []protocol.Diagnostic{nestedFinding}
	nestedClient.diagnosticsMu.Unlock()

	got := f.mgr.diagnosticsForPathsByServer([]string{f.path})[f.path]["gopls"]
	if len(got) != 1 || got[0].Message != "root finding" {
		t.Fatalf("root diagnostics = %+v, want only the root client finding", got)
	}
	got = f.mgr.diagnosticsForPathsByServer([]string{nestedPath})[nestedPath]["gopls"]
	if len(got) != 1 || got[0].Message != "nested finding" {
		t.Fatalf("nested diagnostics = %+v, want only the nested client finding", got)
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
			seq := f.publish("gopls", 3)
			f.watch(map[string]int32{"gopls": 3}, seq)
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
	f.mgr.watchLateDiagnostics(f.path, map[string]int32{"gopls": 2}, nil, 0)

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
			afterWriteAwaitWaiter = func(*Manager, context.Context, string, chan diagnosticsEvent, diagnosticsWaitRequest, time.Duration) ([]Diagnostic, bool, uint64, map[diagnosticIdentity]struct{}) {
				return nil, tc.notified, 0, nil
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
