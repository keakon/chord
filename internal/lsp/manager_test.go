package lsp

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/keakon/x/powernap/pkg/lsp/protocol"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

// newTestManagerWithRoot builds a Manager bound to root without starting any
// language server, so path-resolution tests can inspect its binding.
func newTestManagerWithRoot(root string) *Manager {
	m := &Manager{}
	m.projectRoot.Store(&root)
	return m
}

// testKey builds a clientKey for a test manager, using the manager's project
// root as the workspace root so it matches clients that use the same root.
func testKey(m *Manager, name string) clientKey {
	if m == nil {
		return clientKey{name: name}
	}
	return clientKey{name: name, root: m.projectRootPath()}
}

func TestRelPathEscapesDir(t *testing.T) {
	if !relPathEscapesDir("..") {
		t.Fatal("relPathEscapesDir should reject parent directory")
	}
	if !relPathEscapesDir(filepath.Join("..", "outside.go")) {
		t.Fatal("relPathEscapesDir should reject paths outside directory")
	}
	if relPathEscapesDir("..foo") {
		t.Fatal("relPathEscapesDir should allow sibling-like names inside directory")
	}
	if relPathEscapesDir(filepath.Join("sub", "..foo")) {
		t.Fatal("relPathEscapesDir should allow nested names starting with dots")
	}
}

func TestClientHandlesFileAllowsDotDotPrefixWithinRoot(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "..foo.go")
	client := &Client{cwd: root, cfg: config.LSPServerConfig{FileTypes: []string{".go"}}}
	if !client.HandlesFile(path) {
		t.Fatal("HandlesFile should allow files inside root whose name starts with '..'")
	}
}

func TestClientHandlesFileRejectsOutsideRoot(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(filepath.Dir(root), "outside.go")
	client := &Client{cwd: root, cfg: config.LSPServerConfig{FileTypes: []string{".go"}}}
	if client.HandlesFile(path) {
		t.Fatal("HandlesFile should reject files outside root")
	}
}

func TestNotifyWatchedFileChangedRoutesByFileTypeAcrossLanguages(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(&config.Config{}, root, nil)
	goFake := &fakePowernapClient{}
	tsFake := &fakePowernapClient{}
	mgr.clients[testKey(mgr, "gopls")] = &Client{client: goFake, cwd: root, cfg: config.LSPServerConfig{FileTypes: []string{".go"}}}
	mgr.clients[testKey(mgr, "typescript")] = &Client{client: tsFake, cwd: root, cfg: config.LSPServerConfig{FileTypes: []string{".ts", ".js"}}}

	goPath := filepath.Join(root, "main.go")
	jsPath := filepath.Join(root, "src", "main.js")
	if err := mgr.NotifyWatchedFileChanged(context.Background(), goPath, WatchedFileCreated); err != nil {
		t.Fatalf("NotifyWatchedFileChanged(go) error = %v", err)
	}
	if err := mgr.NotifyWatchedFileChanged(context.Background(), jsPath, WatchedFileChanged); err != nil {
		t.Fatalf("NotifyWatchedFileChanged(js) error = %v", err)
	}

	if len(goFake.watchedFileEvents) != 1 {
		t.Fatalf("gopls watched events = %+v, want 1", goFake.watchedFileEvents)
	}
	if got := goFake.watchedFileEvents[0].Type; got != protocol.Created {
		t.Fatalf("gopls event type = %v, want Created", got)
	}
	if len(tsFake.watchedFileEvents) != 1 {
		t.Fatalf("typescript watched events = %+v, want 1", tsFake.watchedFileEvents)
	}
	if got := tsFake.watchedFileEvents[0].Type; got != protocol.Changed {
		t.Fatalf("typescript event type = %v, want Changed", got)
	}
}

func TestNotifyWatchedFileChangedSendsDeletedEvent(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(&config.Config{}, root, nil)
	fake := &fakePowernapClient{}
	mgr.clients[testKey(mgr, "rust-analyzer")] = &Client{client: fake, cwd: root, cfg: config.LSPServerConfig{FileTypes: []string{".rs"}}}

	path := filepath.Join(root, "src", "lib.rs")
	if err := mgr.NotifyWatchedFileChanged(context.Background(), path, WatchedFileDeleted); err != nil {
		t.Fatalf("NotifyWatchedFileChanged(delete) error = %v", err)
	}
	if len(fake.watchedFileEvents) != 1 {
		t.Fatalf("watched events = %+v, want 1", fake.watchedFileEvents)
	}
	if got := fake.watchedFileEvents[0].Type; got != protocol.Deleted {
		t.Fatalf("event type = %v, want Deleted", got)
	}
}

func TestAwaitFreshWaiterIgnoresStaleVersionAndSettlesOnFresh(t *testing.T) {
	mgr := NewManager(&config.Config{}, t.TempDir(), nil)
	path := filepath.Join(mgr.projectRootPath(), "main.go")
	ch := mgr.PrepareWaiter(path)

	go func() {
		ch <- diagnosticsEvent{
			diagnostics: []Diagnostic{{Severity: 1, Message: "stale"}},
			serverID:    "gopls",
			version:     1,
			receivedAt:  time.Now(),
		}
		time.Sleep(20 * time.Millisecond)
		ch <- diagnosticsEvent{
			diagnostics: []Diagnostic{{Severity: 1, Message: "fresh"}},
			serverID:    "gopls",
			version:     2,
			receivedAt:  time.Now(),
		}
	}()

	diags, ok := mgr.AwaitFreshWaiter(context.Background(), path, ch, diagnosticsWaitRequest{
		serverVersions: map[string]int32{"gopls": 2},
		settle:         10 * time.Millisecond,
	}, time.Second)
	if !ok {
		t.Fatal("AwaitFreshWaiter did not report fresh diagnostics")
	}
	if len(diags) != 1 || diags[0].Message != "fresh" {
		t.Fatalf("diagnostics = %+v, want fresh only", diags)
	}
}

func TestAwaitFreshWaiterUsesLatestEventDuringSettleWindow(t *testing.T) {
	mgr := NewManager(&config.Config{}, t.TempDir(), nil)
	path := filepath.Join(mgr.projectRootPath(), "main.go")
	ch := mgr.PrepareWaiter(path)

	go func() {
		ch <- diagnosticsEvent{diagnostics: []Diagnostic{{Severity: 1, Message: "first"}}, serverID: "gopls", version: 2, receivedAt: time.Now()}
		time.Sleep(10 * time.Millisecond)
		ch <- diagnosticsEvent{diagnostics: []Diagnostic{{Severity: 1, Message: "second"}}, serverID: "gopls", version: 2, receivedAt: time.Now()}
	}()

	diags, ok := mgr.AwaitFreshWaiter(context.Background(), path, ch, diagnosticsWaitRequest{
		serverVersions: map[string]int32{"gopls": 2},
		settle:         25 * time.Millisecond,
	}, time.Second)
	if !ok {
		t.Fatal("AwaitFreshWaiter did not report fresh diagnostics")
	}
	if len(diags) != 1 || diags[0].Message != "second" {
		t.Fatalf("diagnostics = %+v, want latest settled event", diags)
	}
}

func TestWaitForClientForPathWaitsForAsyncStartup(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(&config.Config{
		LSP: config.LSPConfig{
			"gopls": {
				Command:   "gopls",
				FileTypes: []string{".go"},
			},
		},
	}, root, nil)

	path := filepath.Join(root, "main.go")

	mgr.clientsMu.Lock()
	mgr.starting[testKey(mgr, "gopls")] = true
	mgr.clientsMu.Unlock()

	go func() {
		time.Sleep(50 * time.Millisecond)
		mgr.clientsMu.Lock()
		mgr.clients[testKey(mgr, "gopls")] = &Client{
			cwd: root,
			cfg: config.LSPServerConfig{FileTypes: []string{".go"}},
		}
		delete(mgr.starting, testKey(mgr, "gopls"))
		mgr.clientsMu.Unlock()
	}()

	client, ok := mgr.waitForClientForPath(context.Background(), path, 300*time.Millisecond)
	if !ok {
		t.Fatal("waitForClientForPath did not wait for the async client startup")
	}
	if client == nil {
		t.Fatal("waitForClientForPath returned a nil client")
	}
}

func TestWaitForClientForPathReturnsImmediatelyWhenMatchingServerDisabled(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(&config.Config{
		LSP: config.LSPConfig{
			"gopls": {
				Command:   "gopls",
				FileTypes: []string{".go"},
				Disabled:  true,
			},
		},
	}, root, nil)

	path := filepath.Join(root, "main.go")
	mgr.clientsMu.Lock()
	mgr.starting[testKey(mgr, "gopls")] = true
	mgr.clientsMu.Unlock()

	start := time.Now()
	if _, ok := mgr.waitForClientForPath(context.Background(), path, time.Second); ok {
		t.Fatal("waitForClientForPath unexpectedly found a client")
	}
	if elapsed := time.Since(start); elapsed >= 100*time.Millisecond {
		t.Fatalf("waitForClientForPath should not wait for disabled gopls, took %v", elapsed)
	}
}

func TestWaitForClientForPathReturnsWhenStartupSettles(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(&config.Config{
		LSP: config.LSPConfig{
			"gopls": {
				Command:   "gopls",
				FileTypes: []string{".go"},
			},
		},
	}, root, nil)

	path := filepath.Join(root, "main.go")

	mgr.clientsMu.Lock()
	mgr.starting[testKey(mgr, "gopls")] = true
	mgr.clientsMu.Unlock()

	go func() {
		time.Sleep(40 * time.Millisecond)
		mgr.clientsMu.Lock()
		delete(mgr.starting, testKey(mgr, "gopls"))
		mgr.clientsMu.Unlock()
	}()

	start := time.Now()
	if _, ok := mgr.waitForClientForPath(context.Background(), path, time.Second); ok {
		t.Fatal("waitForClientForPath unexpectedly found a client")
	}
	if elapsed := time.Since(start); elapsed >= 500*time.Millisecond {
		t.Fatalf("waitForClientForPath should stop once startup settles, took %v", elapsed)
	}
}

func TestWaitForClientForPathWaitsForNearestRootLaunchOverRunningAncestor(t *testing.T) {
	root := t.TempDir()
	frontend := filepath.Join(root, "frontend")
	if err := os.MkdirAll(filepath.Join(frontend, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(frontend, "tsconfig.json"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(&config.Config{
		LSP: config.LSPConfig{
			"typescript": {
				Command:     "ts",
				FileTypes:   []string{".ts"},
				RootMarkers: []string{"tsconfig.json"},
			},
		},
	}, root, nil)
	// An ancestor instance rooted at the repository root is already running
	// (it also accepts files inside the nested package), while the nearer
	// frontend instance is still launching. Readiness must wait for the target
	// (name, root) pair instead of routing the first request to the ancestor.
	ancestor := existingTrivialClient(root, ".ts")
	mgr.clientsMu.Lock()
	mgr.clients[clientKey{name: "typescript", root: root}] = ancestor
	mgr.starting[clientKey{name: "typescript", root: frontend}] = true
	mgr.clientsMu.Unlock()

	path := filepath.Join(frontend, "src", "a.ts")
	go func() {
		time.Sleep(60 * time.Millisecond)
		nested := existingTrivialClient(frontend, ".ts")
		mgr.clientsMu.Lock()
		mgr.clients[clientKey{name: "typescript", root: frontend}] = nested
		delete(mgr.starting, clientKey{name: "typescript", root: frontend})
		mgr.clientsMu.Unlock()
	}()

	client, ok := mgr.waitForClientForPath(context.Background(), path, time.Second)
	if !ok {
		t.Fatal("waitForClientForPath did not wait for the nearer-root launch")
	}
	if client == nil || client.cwd != frontend || client == ancestor {
		t.Fatalf("waitForClientForPath returned client rooted at %q, want the frontend root client", client.cwd)
	}
}

func TestSidebarEntriesIncludePerServerReviewedSnapshotsForTouchedFiles(t *testing.T) {
	mgr := NewManager(&config.Config{
		LSP: config.LSPConfig{
			"gopls": {
				Command:   "gopls",
				FileTypes: []string{".go"},
			},
			"pyright": {
				Command:   "pyright-langserver",
				FileTypes: []string{".py"},
			},
		},
	}, t.TempDir(), nil)
	mgr.clients[testKey(mgr, "gopls")] = &Client{}
	mgr.reviewByServer = map[string]map[string]reviewCounts{
		"gopls": {
			normalizeWaiterPath("/a.go"):         {errors: 1, warnings: 2},
			normalizeWaiterPath("/untouched.go"): {errors: 99, warnings: 99},
		},
	}
	mgr.touchedPaths = map[string]struct{}{
		normalizeWaiterPath("/a.go"): {},
	}

	rows := mgr.SidebarEntries()
	if len(rows) != 2 {
		t.Fatalf("SidebarEntries() len = %d, want 2", len(rows))
	}
	if rows[0].Name != "gopls" || !rows[0].OK || rows[0].Errors != 1 || rows[0].Warnings != 2 {
		t.Fatalf("gopls row = %+v, want OK with 1E/2W", rows[0])
	}
	if rows[1].Name != "pyright" || !rows[1].Pending || rows[1].Errors != 0 || rows[1].Warnings != 0 {
		t.Fatalf("pyright row = %+v, want pending with zero diagnostics", rows[1])
	}
}

func TestRecordReviewSnapshotDoesNotOverwriteOtherTouchedFiles(t *testing.T) {
	mgr := &Manager{
		diagByServer: map[clientKey]map[string]diagCounts{
			testKey(nil, "gopls"): {
				"file:///a.go": {errors: 1, warnings: 0},
				"file:///b.go": {errors: 0, warnings: 3},
			},
		},
		reviewByServer: map[string]map[string]reviewCounts{
			"gopls": {
				normalizeWaiterPath("/a.go"): {errors: 2, warnings: 0},
			},
		},
		touchedPaths: map[string]struct{}{
			normalizeWaiterPath("/a.go"): {},
			normalizeWaiterPath("/b.go"): {},
		},
	}
	mgr.recordReviewSnapshot("/b.go")
	gotA := mgr.reviewByServer["gopls"][normalizeWaiterPath("/a.go")]
	gotB := mgr.reviewByServer["gopls"][normalizeWaiterPath("/b.go")]
	if gotA.errors != 2 || gotA.warnings != 0 {
		t.Fatalf("review snapshot for a.go = %+v, want preserved 2E/0W", gotA)
	}
	if gotB.errors != 0 || gotB.warnings != 3 {
		t.Fatalf("review snapshot for b.go = %+v, want 0E/3W", gotB)
	}
}

func TestRecordReviewSnapshotClearsStaleDiagnosticsForCleanTouchedFile(t *testing.T) {
	mgr := NewManager(&config.Config{
		LSP: config.LSPConfig{
			"gopls": {
				Command:   "gopls",
				FileTypes: []string{".go"},
			},
		},
	}, t.TempDir(), nil)
	path := normalizeWaiterPath(filepath.Join(mgr.projectRootPath(), "a.go"))
	mgr.clients[testKey(mgr, "gopls")] = &Client{}
	mgr.reviewByServer = map[string]map[string]reviewCounts{
		"gopls": {
			path: {errors: 1, warnings: 0},
		},
	}
	mgr.touchedPaths = map[string]struct{}{
		path: {},
	}

	mgr.recordReviewSnapshot(path)

	got := mgr.reviewByServer["gopls"][path]
	if got.errors != 0 || got.warnings != 0 {
		t.Fatalf("review snapshot after clean edit = %+v, want 0E/0W", got)
	}
	rows := mgr.SidebarEntries()
	if len(rows) != 1 {
		t.Fatalf("SidebarEntries() len = %d, want 1", len(rows))
	}
	if rows[0].Name != "gopls" || rows[0].Errors != 0 || rows[0].Warnings != 0 {
		t.Fatalf("gopls row = %+v, want clean diagnostics", rows[0])
	}
}

func TestPublishedDiagnosticsRefreshExistingReviewedSnapshot(t *testing.T) {
	mgr := NewManager(&config.Config{
		LSP: config.LSPConfig{
			"gopls": {
				Command:   "gopls",
				FileTypes: []string{".go"},
			},
		},
	}, t.TempDir(), nil)
	path := normalizeWaiterPath("/a.go")
	reviewedAt := time.Now().Add(-time.Minute)
	mgr.clients[testKey(mgr, "gopls")] = &Client{}
	mgr.reviewByServer = map[string]map[string]reviewCounts{
		"gopls": {
			path: {errors: 1, reviewedAt: reviewedAt},
		},
	}
	mgr.touchedPaths = map[string]struct{}{path: {}}
	publish := mgr.onDiagnostics(testKey(nil, "gopls"))

	publish("file:///a.go", "", []protocol.Diagnostic{{Severity: protocol.SeverityWarning, Message: "warning"}}, 1)
	got := mgr.reviewByServer["gopls"][path]
	if got.errors != 0 || got.warnings != 1 {
		t.Fatalf("review snapshot after warning publish = %+v, want 0E/1W", got)
	}
	if !got.reviewedAt.Equal(reviewedAt) {
		t.Fatalf("reviewedAt = %v, want preserved %v", got.reviewedAt, reviewedAt)
	}

	publish("file:///a.go", "", nil, 2)
	got = mgr.reviewByServer["gopls"][path]
	if got.errors != 0 || got.warnings != 0 {
		t.Fatalf("review snapshot after clean publish = %+v, want 0E/0W", got)
	}
	rows := mgr.SidebarEntries()
	if len(rows) != 1 || rows[0].Errors != 0 || rows[0].Warnings != 0 {
		t.Fatalf("SidebarEntries() = %+v, want clean gopls row", rows)
	}
}

// TestSidebarEntriesTruncateLongErrorOnRuneBoundary guards that a start-failure
// reason over the row budget is cut on a UTF-8 rune boundary. The row reaches
// the LSP status payload, and the old byte slice through a multi-byte character
// left invalid UTF-8 in it.
func TestSidebarEntriesTruncateLongErrorOnRuneBoundary(t *testing.T) {
	mgr := NewManager(&config.Config{
		LSP: config.LSPConfig{
			"gopls": {Command: "gopls", FileTypes: []string{".go"}},
		},
	}, t.TempDir(), nil)

	// 116 ASCII bytes put the 117-byte budget one byte into "界" (3 bytes).
	head := strings.Repeat("x", 116)
	mgr.startFailMu.Lock()
	mgr.startFail[testKey(mgr, "gopls")] = head + "界" + strings.Repeat("y", 40)
	mgr.startFailMu.Unlock()

	rows := mgr.SidebarEntries()
	if len(rows) != 1 {
		t.Fatalf("SidebarEntries() len = %d, want 1", len(rows))
	}
	if !utf8.ValidString(rows[0].Error) {
		t.Fatalf("start-failure row is not valid UTF-8: %q", rows[0].Error)
	}
	if want := head + "..."; rows[0].Error != want {
		t.Fatalf("start-failure row = %q, want %q", rows[0].Error, want)
	}
}

func TestPublishedDiagnosticsDoNotAdmitUnreviewedPathToSidebar(t *testing.T) {
	mgr := NewManager(&config.Config{
		LSP: config.LSPConfig{
			"gopls": {
				Command:   "gopls",
				FileTypes: []string{".go"},
			},
		},
	}, t.TempDir(), nil)
	path := normalizeWaiterPath("/a.go")
	mgr.clients[testKey(mgr, "gopls")] = &Client{}
	mgr.touchedPaths = map[string]struct{}{path: {}}

	mgr.onDiagnostics(testKey(nil, "gopls"))("file:///a.go", "", []protocol.Diagnostic{{Severity: protocol.SeverityError, Message: "existing project error"}}, 1)

	if byPath := mgr.reviewByServer["gopls"]; len(byPath) != 0 {
		t.Fatalf("unreviewed publish created sidebar snapshots: %+v", byPath)
	}
	rows := mgr.SidebarEntries()
	if len(rows) != 1 || rows[0].Errors != 0 || rows[0].Warnings != 0 {
		t.Fatalf("SidebarEntries() = %+v, want unreviewed diagnostics hidden", rows)
	}
}

func TestCurrentReviewSnapshotsIncludesCleanConnectedServer(t *testing.T) {
	mgr := NewManager(&config.Config{
		LSP: config.LSPConfig{
			"gopls": {
				Command:   "gopls",
				FileTypes: []string{".go"},
			},
		},
	}, t.TempDir(), nil)
	path := filepath.Join(mgr.projectRootPath(), "a.go")
	mgr.clients[testKey(mgr, "gopls")] = &Client{cwd: mgr.projectRootPath(), cfg: config.LSPServerConfig{FileTypes: []string{".go"}}}

	got := mgr.CurrentReviewSnapshots(path)
	want := []message.LSPReview{{Path: path, ServerID: "gopls", Errors: 0, Warnings: 0}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CurrentReviewSnapshots() = %#v, want %#v", got, want)
	}
}

func TestRecordReviewSnapshotIgnoresDiagnosticsFromNonOwnerRoot(t *testing.T) {
	mgr := NewManager(&config.Config{
		LSP: config.LSPConfig{
			"gopls": {Command: "gopls", FileTypes: []string{".go"}},
		},
	}, t.TempDir(), nil)
	path := normalizeWaiterPath(filepath.Join(mgr.projectRootPath(), "nested", "a.go"))
	mgr.clients[clientKey{name: "gopls", root: mgr.projectRootPath()}] = &Client{cwd: mgr.projectRootPath(), cfg: config.LSPServerConfig{FileTypes: []string{".go"}}}
	mgr.clients[clientKey{name: "gopls", root: filepath.Join(mgr.projectRootPath(), "nested")}] = &Client{cwd: filepath.Join(mgr.projectRootPath(), "nested"), cfg: config.LSPServerConfig{FileTypes: []string{".go"}}}
	mgr.diagByServer = map[clientKey]map[string]diagCounts{
		{name: "gopls", root: mgr.projectRootPath()}:                          {string(protocol.URIFromPath(path)): {errors: 2}},
		{name: "gopls", root: filepath.Join(mgr.projectRootPath(), "nested")}: {string(protocol.URIFromPath(path)): {warnings: 1}},
	}

	mgr.recordReviewSnapshot(path)
	got := mgr.reviewByServer["gopls"][path]
	if got.errors != 0 || got.warnings != 1 {
		t.Fatalf("review snapshot = %+v, want only owner-root 0E/1W", got)
	}
}

func TestAllDiagnosticsByAbsPathIgnoresDiagnosticsFromNonOwnerRoot(t *testing.T) {
	mgr := NewManager(&config.Config{
		LSP: config.LSPConfig{
			"gopls": {Command: "gopls", FileTypes: []string{".go"}},
		},
	}, t.TempDir(), nil)
	path := normalizeWaiterPath(filepath.Join(mgr.projectRootPath(), "nested", "a.go"))
	outer := &Client{cwd: mgr.projectRootPath(), cfg: config.LSPServerConfig{FileTypes: []string{".go"}}, diagnostics: map[protocol.DocumentURI][]protocol.Diagnostic{}}
	inner := &Client{cwd: filepath.Join(mgr.projectRootPath(), "nested"), cfg: config.LSPServerConfig{FileTypes: []string{".go"}}, diagnostics: map[protocol.DocumentURI][]protocol.Diagnostic{}}
	uri := protocol.DocumentURI(protocol.URIFromPath(path))
	outer.diagnostics[uri] = []protocol.Diagnostic{{Severity: protocol.SeverityError, Message: "stale outer"}}
	inner.diagnostics[uri] = []protocol.Diagnostic{{Severity: protocol.SeverityWarning, Message: "fresh inner"}}
	mgr.clients[clientKey{name: "gopls", root: mgr.projectRootPath()}] = outer
	mgr.clients[clientKey{name: "gopls", root: filepath.Join(mgr.projectRootPath(), "nested")}] = inner

	got := mgr.allDiagnosticsByAbsPath()[path]
	if len(got) != 1 || got[0].Severity != int(protocol.SeverityWarning) || got[0].Message != "fresh inner" {
		t.Fatalf("diagnostics = %+v, want only owner-root warning", got)
	}
}

func TestAllDiagnosticsByAbsPathKeepsDistinctServerDiagnosticsPerPath(t *testing.T) {
	mgr := NewManager(&config.Config{}, t.TempDir(), nil)
	path := normalizeWaiterPath(filepath.Join(mgr.projectRootPath(), "pkg", "a.py"))
	outer := &Client{cwd: mgr.projectRootPath(), cfg: config.LSPServerConfig{FileTypes: []string{".py"}}, diagnostics: map[protocol.DocumentURI][]protocol.Diagnostic{}}
	inner := &Client{cwd: filepath.Join(mgr.projectRootPath(), "pkg"), cfg: config.LSPServerConfig{FileTypes: []string{".py"}}, diagnostics: map[protocol.DocumentURI][]protocol.Diagnostic{}}
	uri := protocol.DocumentURI(protocol.URIFromPath(path))
	outer.diagnostics[uri] = []protocol.Diagnostic{{Severity: protocol.SeverityWarning, Message: "outer server warning"}}
	inner.diagnostics[uri] = []protocol.Diagnostic{{Severity: protocol.SeverityError, Message: "inner server error"}}
	mgr.clients[clientKey{name: "pyright", root: mgr.projectRootPath()}] = outer
	mgr.clients[clientKey{name: "basedpyright", root: filepath.Join(mgr.projectRootPath(), "pkg")}] = inner

	// Two servers both own the same file, each from its own root. The owner
	// cache must distinguish them; keying it by path alone reused the first
	// server's owner roots and silently dropped the second server's diagnostics.
	got := mgr.allDiagnosticsByAbsPath()[path]
	messages := make(map[string]int, len(got))
	for _, d := range got {
		messages[d.Message]++
	}
	if messages["outer server warning"] != 1 || messages["inner server error"] != 1 {
		t.Fatalf("diagnostics = %+v, want one entry from each server", got)
	}
}

func TestRebuildTouchedPathsNormalizesAndSorts(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(&config.Config{}, root, nil)
	mgr.RebuildTouchedPaths([]string{"bar.go", filepath.Join(root, "foo.go")})
	got := mgr.TouchedPaths()
	want := []string{
		normalizeWaiterPath(filepath.Join(root, "bar.go")),
		normalizeWaiterPath(filepath.Join(root, "foo.go")),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TouchedPaths() = %#v, want %#v", got, want)
	}
}

func TestConfiguredServersSortsNamesAndFileTypesAndReturnsCopies(t *testing.T) {
	mgr := NewManager(&config.Config{
		LSP: config.LSPConfig{
			"zed": {
				Command:   "zed-lsp",
				FileTypes: []string{"tsx", ".ts", "Go", ""},
			},
			"alpha": {
				Command:   "alpha-lsp",
				FileTypes: []string{".py", "pyi"},
			},
			"disabled": {
				Command:   "off",
				Disabled:  true,
				FileTypes: []string{".txt"},
			},
		},
	}, t.TempDir(), nil)

	got := mgr.ConfiguredServers()
	want := []ConfiguredServerInfo{
		{Name: "alpha", FileTypes: []string{"*.py", "*.pyi"}},
		{Name: "zed", FileTypes: []string{"*.go", "*.ts", "*.tsx"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ConfiguredServers() = %#v, want %#v", got, want)
	}

	got[0].FileTypes[0] = "*.mutated"
	if again := mgr.ConfiguredServers(); !reflect.DeepEqual(again, want) {
		t.Fatalf("ConfiguredServers() should return copies, got %#v after mutation, want %#v", again, want)
	}
}

// TestConcurrentDiagnosticsAndCloseDoNotDeadlock exercises the two lock
// directions at once: review snapshotting and diagnostics publishing take
// diagMu and then clientsMu (reviewCountsForPathLocked / reviewServerIDsForPathLocked),
// while DidCloseErr touches clientsMu and diagMu. A pending clientsMu writer
// between the two forces readers to wait, which is exactly the window in which
// an inverted nested order would deadlock. Regression guard for DidCloseErr
// taking diagMu while still holding clientsMu.
func TestConcurrentDiagnosticsAndCloseDoNotDeadlock(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(&config.Config{
		LSP: config.LSPConfig{
			"gopls": {Command: "gopls", FileTypes: []string{".go"}},
		},
	}, root, nil)
	path := filepath.Join(root, "a.go")
	uri := string(protocol.URIFromPath(path))
	key := clientKey{name: "gopls", root: root}
	mgr.clients[key] = &Client{cwd: root, cfg: config.LSPServerConfig{FileTypes: []string{".go"}}}
	mgr.diagByServer = map[clientKey]map[string]diagCounts{
		key: {uri: {errors: 1}},
	}
	mgr.reviewByServer = map[string]map[string]reviewCounts{
		"gopls": {normalizeWaiterPath(path): {errors: 1}},
	}
	mgr.touchedPaths = map[string]struct{}{normalizeWaiterPath(path): {}}

	start := make(chan struct{})
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		<-start
		for range 200 {
			mgr.recordReviewSnapshot(path)
			mgr.CurrentReviewSnapshots(path)
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for range 200 {
			_ = mgr.DidCloseErr(context.Background(), path)
			mgr.diagMu.Lock()
			mgr.diagByServer[key] = map[string]diagCounts{uri: {errors: 1}}
			mgr.diagMu.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for range 200 {
			mgr.clientsMu.Lock()
			_ = mgr.clients[key]
			mgr.clientsMu.Unlock()
		}
	}()
	go func() {
		wg.Wait()
		close(done)
	}()
	close(start)

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent review snapshotting and DidCloseErr deadlocked")
	}
}
