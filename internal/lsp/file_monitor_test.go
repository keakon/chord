package lsp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/keakon/x/powernap/pkg/lsp/protocol"

	"github.com/keakon/chord/internal/config"
)

func monitoredTestFile(t *testing.T) (*Manager, *Client, *fakePowernapClient, string) {
	t.Helper()
	root := resolvedTempDir(t)
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("package sample\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fake := &fakePowernapClient{}
	c := newResyncTestClient(fake)
	c.cwd = root
	c.diagnostics = make(map[protocol.DocumentURI][]protocol.Diagnostic)
	c.cfg = config.LSPServerConfig{FileTypes: []string{"go"}}
	m := NewManager(&config.Config{}, root, nil)
	key := clientKey{name: "gopls", root: root}
	m.clients[key] = c
	if _, err := c.DidOpen(context.Background(), path, "package sample\n"); err != nil {
		t.Fatal(err)
	}
	m.MarkTouched(path)
	diags := []protocol.Diagnostic{{Severity: 1, Message: "sample diagnostic"}}
	c.diagnostics[protocol.DocumentURI(c.pathToURI(path))] = diags
	uri := c.pathToURI(path)
	m.onDiagnostics(key, c)(uri, uriToPath(uri), nil, "gopls", diags, 1)
	m.recordReviewSnapshot(path)
	return m, c, fake, path
}

func TestFileMaintenanceExternalChangeAndDelete(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(map[bool]string{false: "change", true: "delete"}[remove], func(t *testing.T) {
			m, c, _, path := monitoredTestFile(t)
			if remove {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			} else {
				replacement := path + ".new"
				if err := os.WriteFile(replacement, []byte("package replaced\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(replacement, path); err != nil {
					t.Fatal(err)
				}
			}
			cursor := 0
			m.reconcileFiles(context.Background(), make(map[string]os.FileInfo), nil, &cursor, true)
			if len(c.openFiles) != 0 || len(c.syncedDigest) != 0 || len(c.syncedFiles) != 0 || len(c.GetDiagnostics(path)) != 0 {
				t.Fatal("stale overlay or diagnostics retained")
			}
			if len(m.diagByServer) != 0 || len(m.publishedDiagByServer) != 0 || len(m.diagState) != 0 {
				t.Fatal("stale manager diagnostics retained")
			}
			if remove && (len(m.TouchedPaths()) != 0 || len(m.reviewByServer) != 0) {
				t.Fatal("deleted file retained live review state")
			}
			if !remove && len(m.TouchedPaths()) != 1 {
				t.Fatal("external modification lost session membership")
			}
			for _, row := range m.SidebarEntries() {
				if row.Errors != 0 {
					t.Fatal("sidebar retained old errors")
				}
			}
			// A versioned response from the closed overlay cannot restore state.
			uri := c.pathToURI(path)
			m.onDiagnostics(clientKey{name: "gopls", root: c.cwd}, c)(uri, uriToPath(uri), nil, "", []protocol.Diagnostic{{Severity: 1}}, 1)
			if len(m.diagByServer) != 0 {
				t.Fatal("late diagnostic restored old counts")
			}
		})
	}
}

func TestFileMaintenancePreservesConcurrentSync(t *testing.T) {
	m, c, _, path := monitoredTestFile(t)
	previous := c.syncedFiles[path]
	if _, err := c.DidChange(context.Background(), path, "package latest\n"); err != nil {
		t.Fatal(err)
	}
	if m.releaseOverlay(context.Background(), c, path, previous, overlayReleaseChanged) {
		t.Fatal("maintenance closed a newer tool synchronization")
	}
	if len(c.openFiles) != 1 {
		t.Fatal("new overlay was lost")
	}
}

// A tool write changes the disk file the monitor observed earlier, but the
// overlay Chord synced right after it matches the new disk state; that is not
// an external change, and closing the overlay would make the server re-read
// the file from disk and publish unversioned diagnostics.
func TestFileMaintenanceKeepsOverlaySyncedAfterToolRewrite(t *testing.T) {
	m, c, fake, path := monitoredTestFile(t)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	observed := map[string]os.FileInfo{path: before}

	const rewritten = "package sample\n\nconst rewritten = true\n"
	if err := os.WriteFile(path, []byte(rewritten), 0600); err != nil {
		t.Fatal(err)
	}
	later := before.ModTime().Add(time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DidChange(context.Background(), path, rewritten); err != nil {
		t.Fatal(err)
	}

	cursor := 0
	m.reconcileFiles(context.Background(), observed, map[string]fsnotify.Op{path: fsnotify.Write}, &cursor, false)
	if len(c.openFiles) != 1 || len(fake.didCloseURIs) != 0 {
		t.Fatalf("tool-synced overlay was closed: open=%v didClose=%v", c.openFiles, fake.didCloseURIs)
	}
}

func TestDocumentVersionDoesNotRepeatAfterReopen(t *testing.T) {
	_, c, _, path := monitoredTestFile(t)
	old := c.openFiles[path]
	if err := c.DidClose(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	version, err := c.DidOpen(context.Background(), path, "package sample\n")
	if err != nil {
		t.Fatal(err)
	}
	_, oldAccepted := c.acceptDiagnosticsInfo(path, old)
	if version <= old || oldAccepted {
		t.Fatal("reopened document accepts an old version")
	}
	if _, ok := c.acceptDiagnosticsInfo(path, version); !ok {
		t.Fatal("current version rejected")
	}
}

func TestFileMaintenanceClosesIdleDocuments(t *testing.T) {
	m, c, _, path := monitoredTestFile(t)
	state := c.syncedFiles[path]
	state.used = time.Now().Add(-openFileIdleTimeout - time.Second)
	c.syncedFiles[path] = state
	cursor := 0
	m.reconcileFiles(context.Background(), make(map[string]os.FileInfo), nil, &cursor, true)
	if len(c.openFiles) != 0 {
		t.Fatal("idle overlay retained")
	}
	if len(m.TouchedPaths()) != 1 {
		t.Fatal("idle cleanup rewrote session membership")
	}
}

func TestMaintenanceLoopStopsOnCancellation(t *testing.T) {
	m := NewManager(&config.Config{}, resolvedTempDir(t), nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go m.maintainFiles(ctx, done)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("maintenance loop did not stop")
	}
}

func TestDeletedCachedFileWithoutOverlayIsForgotten(t *testing.T) {
	m, c, _, path := monitoredTestFile(t)
	if err := c.DidClose(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	cursor := 0
	m.reconcileFiles(context.Background(), make(map[string]os.FileInfo), nil, &cursor, true)
	if len(m.reviewByServer) != 0 || len(m.diagState) != 0 || len(m.TouchedPaths()) != 0 || len(c.diagnostics) != 0 {
		t.Fatal("deleted closed file retained cached state")
	}
}

func TestFileMaintenanceUnchangedFileIsSilent(t *testing.T) {
	m, c, fake, path := monitoredTestFile(t)
	cursor := 0
	m.reconcileFiles(context.Background(), make(map[string]os.FileInfo), nil, &cursor, true)
	if len(c.openFiles) != 1 || len(fake.didCloseURIs) != 0 || len(fake.watchedFileEvents) != 0 {
		t.Fatal("unchanged file generated protocol traffic")
	}
	if m.reviewByServer["gopls"][path].errors != 1 {
		t.Fatal("unchanged file lost diagnostics")
	}
}

func TestMissingRootClearsCleanTouchedFile(t *testing.T) {
	m, c, _, path := monitoredTestFile(t)
	m.clearFileState(path, false)
	c.clearDiagnosticsForPath(path)
	if err := os.RemoveAll(c.cwd); err != nil {
		t.Fatal(err)
	}
	m.pruneMissingRoots(context.Background())
	if len(m.clients) != 0 || len(m.TouchedPaths()) != 0 || len(m.reviewByServer) != 0 || len(m.diagState) != 0 {
		t.Fatal("missing root retained live file state")
	}
}

func TestIdleInstanceReleasesDiagnostics(t *testing.T) {
	m, c, _, path := monitoredTestFile(t)
	if err := c.DidClose(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	c.lastUsed.Store(time.Now().Add(-workspaceIdleTimeout - time.Minute).UnixNano())
	m.pruneMissingRoots(context.Background())
	if len(m.clients) != 0 || len(m.reviewByServer) != 0 || len(m.publishedDiagByServer) != 0 || len(m.diagState) != 0 {
		t.Fatal("idle instance retained caches")
	}
	if len(m.TouchedPaths()) != 1 {
		t.Fatal("instance eviction changed session history")
	}
}

type pausedChangeClient struct {
	*fakePowernapClient
	entered chan struct{}
	release chan struct{}
}

func (c *pausedChangeClient) NotifyDidChangeTextDocument(ctx context.Context, uri string, version int, changes []protocol.TextDocumentContentChangeEvent) error {
	close(c.entered)
	select {
	case <-c.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return c.fakePowernapClient.NotifyDidChangeTextDocument(ctx, uri, version, changes)
}

func TestDocumentIOReleasesManagerRegistryLock(t *testing.T) {
	m, c, fake, path := monitoredTestFile(t)
	paused := &pausedChangeClient{fakePowernapClient: fake, entered: make(chan struct{}), release: make(chan struct{})}
	c.client = paused
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { m.DidChange(ctx, path, "package changed\n"); close(done) }()
	<-paused.entered
	acquired := make(chan struct{})
	go func() {
		m.clientsMu.Lock()
		if len(m.clients) != 1 {
			t.Error("unexpected client registry")
		}
		m.clientsMu.Unlock()
		close(acquired)
	}()
	select {
	case <-acquired:
	case <-ctx.Done():
		t.Error("protocol IO held the registry lock")
	}
	close(paused.release)
	<-done
	<-acquired
}

func TestFileMaintenanceEvictsOldestOverlayAtCapacity(t *testing.T) {
	m, c, _, oldest := monitoredTestFile(t)
	seedOverlayCapacity(c, oldest)
	state := c.syncedFiles[oldest]
	state.used = time.Now().Add(-time.Minute)
	c.syncedFiles[oldest] = state
	cursor := 0
	m.reconcileFiles(t.Context(), make(map[string]os.FileInfo), map[string]fsnotify.Op{oldest: fsnotify.Write}, &cursor, false)
	if len(c.openFiles) != maxOpenFilesPerClient {
		t.Fatalf("open documents=%d", len(c.openFiles))
	}
	if _, ok := c.openFiles[oldest]; ok {
		t.Fatal("oldest overlay was not evicted")
	}
}

// Capacity is a property of the open-document set. Only the selected document
// needs disk contents; the other overlays need no writes or protocol round trips.
func seedOverlayCapacity(c *Client, source string) {
	state := c.syncedFiles[source]
	state.used = time.Now()
	for i := range maxOpenFilesPerClient {
		path := filepath.Join(c.cwd, fmt.Sprintf("capacity%03d.go", i))
		c.openFiles[path] = 1
		c.syncedFiles[path] = state
		c.syncedDigest[path] = c.syncedDigest[source]
	}
}

func TestCachedCleanupPreservesNewlyOpenedDocument(t *testing.T) {
	m, c, _, path := monitoredTestFile(t)
	// Simulate a cache-only scan whose result is stale because a tool opened
	// the document before cleanup acquired its lifecycle lock.
	m.clearCachedFile(context.Background(), path, false)
	if len(c.openFiles) != 1 || len(c.GetDiagnostics(path)) != 1 || m.reviewByServer["gopls"][path].errors != 1 {
		t.Fatal("cache cleanup discarded an active document")
	}
}

func TestFileMaintenanceScopesDiagnosticInvalidation(t *testing.T) {
	for _, action := range []string{"idle", "capacity", "change", "delete"} {
		t.Run(action, func(t *testing.T) {
			m, idle, _, path := monitoredTestFile(t)
			activeTransport := &fakePowernapClient{}
			active := newResyncTestClient(activeTransport)
			active.cwd, active.cfg = idle.cwd, idle.cfg
			active.name = "sample-checker"
			active.diagnostics = make(map[protocol.DocumentURI][]protocol.Diagnostic)
			key := clientKey{name: active.name, root: active.cwd}
			m.clients[key] = active
			version, err := active.DidOpen(t.Context(), path, "package sample\n")
			if err != nil {
				t.Fatal(err)
			}
			diags := []protocol.Diagnostic{{Severity: 2, Message: "sample warning"}}
			uri := active.pathToURI(path)
			active.diagnostics[protocol.DocumentURI(uri)] = diags
			m.onDiagnostics(key, active)(uri, path, nil, key.name, diags, version)
			m.recordReviewSnapshot(path)
			m.recordReportedLocked(path, convertDiagnostics(diags))
			originalState := m.diagState[path]
			originalReview := m.reviewByServer[key.name][path]
			var notifications []DiagnosticsPayload
			m.broadcast = func(kind string, payload any) {
				if kind == TypeLSPDiagnostics {
					notifications = append(notifications, payload.(DiagnosticsPayload))
				}
			}
			state := idle.syncedFiles[path]
			switch action {
			case "idle":
				state.used = time.Now().Add(-openFileIdleTimeout - time.Second)
				idle.syncedFiles[path] = state
			case "capacity":
				state.used = time.Now().Add(-time.Minute)
				idle.syncedFiles[path] = state
				seedOverlayCapacity(idle, path)
			case "change":
				if err := os.WriteFile(path, []byte("package changed\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "delete":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			cursor := 0
			// Selecting the path directly also covers eviction during watcher traffic.
			m.reconcileFiles(t.Context(), make(map[string]os.FileInfo), map[string]fsnotify.Op{path: fsnotify.Write}, &cursor, false)
			if _, open := idle.openFiles[path]; open {
				t.Fatal("overlay was not released")
			}
			if m.diagByServer[clientKey{name: idle.name, root: idle.cwd}][uri].errors != 0 {
				t.Fatal("retired diagnostic retained")
			}
			if action == "change" || action == "delete" {
				if len(active.openFiles) != 0 || len(active.GetDiagnostics(path)) != 0 || len(m.diagByServer) != 0 || len(m.publishedDiagByServer) != 0 {
					t.Fatal("disk change retained stale diagnostics")
				}
				return
			}
			if len(active.openFiles) != 1 || len(active.GetDiagnostics(path)) != 1 || len(activeTransport.didCloseURIs) != 0 || len(activeTransport.watchedFileEvents) != 0 {
				t.Fatal("active server was disturbed")
			}
			if m.diagByServer[key][uri].warnings != 1 || m.reviewByServer[key.name][path] != originalReview || len(m.publishedDiagByServer[key][path]) != 1 || m.diagState[path] != originalState || len(m.reportedByPath[path]) != 1 {
				t.Fatal("active diagnostic, review, freshness or suppression state changed")
			}
			if len(notifications) != 1 || notifications[0].ServerID != idle.name || len(notifications[0].Diagnostics) != 0 {
				t.Fatalf("notifications=%+v", notifications)
			}
			if len(m.TouchedPaths()) != 1 {
				t.Fatal("session membership changed")
			}
		})
	}
}
