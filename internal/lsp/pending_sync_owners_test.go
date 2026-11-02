package lsp

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/keakon/x/powernap/pkg/lsp/protocol"

	"github.com/keakon/chord/internal/config"
)

func pendingOwnerClient(m *Manager, name string) *Client {
	return &Client{client: &fakePowernapClient{saveOptions: &protocol.SaveOptions{}}, cwd: m.projectRootPath(), cfg: m.cfg.LSP[name], openFiles: make(map[string]int32), diagnostics: make(map[protocol.DocumentURI][]protocol.Diagnostic)}
}

func TestPendingSyncWaitsForEveryOwnerAndUpdatesOldBuffer(t *testing.T) {
	m, path := newWarmupTestManager(t)
	m.cfg.LSP["secondary"] = config.LSPServerConfig{Command: "sample-server", FileTypes: []string{".go"}}
	slowKey := testKey(m, "gopls")
	fastKey := testKey(m, "secondary")
	fast := pendingOwnerClient(m, "secondary")
	m.clients[fastKey] = fast
	if _, err := fast.DidChange(context.Background(), path, "package sample\n"); err != nil {
		t.Fatal(err)
	}
	m.registerPendingSync(path, "package main\n", WatchedFileChanged, "")
	m.resyncPendingWrites(context.Background())
	if got := m.pendingSyncCount(); got != 1 {
		t.Fatalf("ready owner consumed slow owner's responsibility: %d", got)
	}
	slow := pendingOwnerClient(m, "gopls")
	m.clientsMu.Lock()
	m.clients[slowKey] = slow
	m.clientsMu.Unlock()
	m.resyncPendingWrites(context.Background())
	if fast.syncedDocumentVersion(path) != 2 || slow.syncedDocumentVersion(path) != 1 {
		t.Fatalf("old and new owners were not both updated: fast=%d slow=%d", fast.syncedDocumentVersion(path), slow.syncedDocumentVersion(path))
	}
	for _, owner := range []struct {
		key    clientKey
		client *Client
	}{{fastKey, fast}, {slowKey, slow}} {
		diags := []protocol.Diagnostic{{Severity: protocol.SeverityError, Message: owner.key.name + " finding"}}
		uri := protocol.DocumentURI(owner.client.pathToURI(path))
		owner.client.diagnostics[uri] = diags
		m.onDiagnostics(owner.key, owner.client)(string(uri), path, nil, "", diags, owner.client.syncedDocumentVersion(path))
	}
	if items := m.PendingLateDiagnostics([]string{path}, "", ""); len(items) != 1 || len(items[0].Reviews) != 2 {
		t.Fatalf("late results from owners = %+v", items)
	}
}

func TestPendingSyncFailureKeepsOtherStartingOwner(t *testing.T) {
	m, path := newWarmupTestManager(t)
	m.cfg.LSP["secondary"] = config.LSPServerConfig{Command: "sample-server", FileTypes: []string{".go"}}
	other := testKey(m, "secondary")
	seedPendingLaunch(t, m, other)
	m.registerPendingSync(path, "package main\n", WatchedFileChanged, "")
	m.clientsMu.Lock()
	m.clearPendingSyncsForLaunch(testKey(m, "gopls"))
	m.clientsMu.Unlock()
	if got := m.pendingSyncCount(); got != 1 {
		t.Fatalf("failed owner dropped another starting owner: %d", got)
	}
	client := pendingOwnerClient(m, "secondary")
	m.clientsMu.Lock()
	m.clients[other] = client
	m.clientsMu.Unlock()
	m.resyncPendingWrites(context.Background())
	if got := client.syncedDocumentVersion(path); got != 1 {
		t.Fatalf("surviving owner never received the content: %d", got)
	}
}

func TestPendingSyncPreservesSuccessfulSuccessorBuffer(t *testing.T) {
	m, path := newWarmupTestManager(t)
	key := testKey(m, "gopls")
	c := pendingOwnerClient(m, "gopls")
	m.clientsMu.Lock()
	m.clients[key] = c
	m.clientsMu.Unlock()
	if err := os.WriteFile(path, []byte("package updated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The owner is already admitted, so registration schedules a re-sync right
	// away. Holding the re-sync gate keeps it from racing the successor sync
	// below; releasing it lets whichever re-sync runs see the newer buffer.
	m.pendingResyncMu.Lock()
	release := sync.OnceFunc(m.pendingResyncMu.Unlock)
	t.Cleanup(release)
	m.registerPendingSync(path, "package main\n", WatchedFileChanged, "")
	if _, err := c.DidChange(context.Background(), path, "package updated\n"); err != nil {
		t.Fatal(err)
	}
	release()
	m.resyncPendingWrites(context.Background())
	if got := c.syncedDocumentVersion(path); got != 1 {
		t.Fatalf("parked content replaced the successor buffer: version=%d", got)
	}
}

func TestPendingSyncDoesNotSuppressUnreviewedStartupFinding(t *testing.T) {
	m, path := newWarmupTestManager(t)
	m.registerPendingSync(path, "package main\n", WatchedFileChanged, "")
	client := seedWarmupClient(m, &fakePowernapClient{})
	diags := []protocol.Diagnostic{{Severity: protocol.SeverityError, Message: "undefined: sample"}}
	uri := protocol.DocumentURI(client.pathToURI(path))
	client.diagnostics[uri] = diags
	m.onDiagnostics(testKey(m, "gopls"), client)(string(uri), path, nil, "", diags, 0)
	m.resyncPendingWrites(context.Background())
	m.onDiagnostics(testKey(m, "gopls"), client)(string(uri), path, nil, "", diags, client.syncedDocumentVersion(path))
	if got := m.PendingLateDiagnostics([]string{path}, "", ""); len(got) != 1 || !strings.Contains(got[0].Lines, "undefined: sample") {
		t.Fatalf("unreviewed startup finding suppressed: %+v", got)
	}
}

func TestPendingSyncNotificationFailureDoesNotConfirmOrWatch(t *testing.T) {
	m, path := newWarmupTestManager(t)
	m.registerPendingSync(path, "package main\n", WatchedFileChanged, "")
	seedWarmupClient(m, &fakePowernapClient{})
	afterWriteNotifyWatchedFileChanged = func(*Manager, context.Context, string, protocol.FileChangeType) map[string]error {
		return map[string]error{"gopls": errors.New("sample notification failure")}
	}
	m.resyncPendingWrites(context.Background())
	if got := m.PendingLateDiagnostics([]string{path}, "", ""); len(got) != 0 {
		t.Fatalf("failed sync produced a late result: %+v", got)
	}
	m.diagMu.RLock()
	fresh := m.diagnosticsFreshLocked(path)
	m.diagMu.RUnlock()
	if fresh {
		t.Fatal("failed sync was confirmed")
	}
}
