package lsp

import (
	"context"
	"crypto/sha256"
	"path/filepath"
	"testing"

	"github.com/keakon/x/powernap/pkg/lsp/protocol"
	"github.com/sourcegraph/jsonrpc2"

	"github.com/keakon/chord/internal/config"
)

func wholeDocumentChangeText(changes []protocol.TextDocumentContentChangeEvent) string {
	for _, change := range changes {
		if whole, ok := change.Value.(protocol.TextDocumentContentChangeWholeDocument); ok {
			return whole.Text
		}
	}
	return ""
}

func newResyncTestClient(fake *fakePowernapClient) *Client {
	return &Client{
		client:       fake,
		name:         "gopls",
		openFiles:    make(map[string]int32),
		syncedDigest: make(map[string][sha256.Size]byte),
	}
}

func TestClientResyncFileIfChangedSendsOnlyChangedContent(t *testing.T) {
	fake := &fakePowernapClient{}
	c := newResyncTestClient(fake)
	path := "/tmp/main.go"
	ctx := context.Background()

	if _, err := c.DidOpen(ctx, path, "one\n"); err != nil {
		t.Fatalf("DidOpen() error = %v", err)
	}
	if sent, err := c.ResyncFileIfChanged(ctx, path, "one\n"); err != nil || sent {
		t.Fatalf("resync with unchanged content = (%v, %v), want (false, nil)", sent, err)
	}
	if len(fake.didChangeURIs) != 0 {
		t.Fatalf("unchanged content sent didChange for %v", fake.didChangeURIs)
	}

	sent, err := c.ResyncFileIfChanged(ctx, path, "two\n")
	if err != nil {
		t.Fatalf("ResyncFileIfChanged(changed) error = %v", err)
	}
	if !sent {
		t.Fatal("changed content did not send didChange")
	}
	if len(fake.didChangeURIs) != 1 || fake.didChangeURIs[0] != c.pathToURI(path) {
		t.Fatalf("didChange URIs = %v, want [%s]", fake.didChangeURIs, c.pathToURI(path))
	}
	if fake.didChangeTexts[0] != "two\n" {
		t.Fatalf("didChange text = %q, want current file bytes", fake.didChangeTexts[0])
	}
	if fake.didChangeVersions[0] != 2 {
		t.Fatalf("didChange version = %d, want 2", fake.didChangeVersions[0])
	}
	// The sent content is now the known state: a repeat resync is a no-op.
	if sent, err := c.ResyncFileIfChanged(ctx, path, "two\n"); err != nil || sent {
		t.Fatalf("repeat resync = (%v, %v), want (false, nil)", sent, err)
	}
	if len(fake.didChangeURIs) != 1 {
		t.Fatalf("repeat resync sent didChange again: %v", fake.didChangeURIs)
	}
}

func TestClientResyncFileIfChangedSkipsUnopenedAndClosedClients(t *testing.T) {
	fake := &fakePowernapClient{}
	c := newResyncTestClient(fake)
	path := "/tmp/main.go"

	if sent, err := c.ResyncFileIfChanged(context.Background(), path, "one"); err != nil || sent {
		t.Fatalf("unopened resync = (%v, %v), want (false, nil)", sent, err)
	}
	c.closed = true
	if sent, err := c.ResyncFileIfChanged(context.Background(), path, "one"); err != nil || sent {
		t.Fatalf("closed resync = (%v, %v), want (false, nil)", sent, err)
	}
	if len(fake.didChangeURIs) != 0 {
		t.Fatalf("didChange sent for unopened/closed clients: %v", fake.didChangeURIs)
	}
}

func TestClientResyncFileIfChangedSendsWhenDigestUnknown(t *testing.T) {
	fake := &fakePowernapClient{}
	path := "/tmp/main.go"
	c := &Client{
		client:       fake,
		name:         "gopls",
		openFiles:    map[string]int32{path: 3},
		syncedDigest: make(map[string][sha256.Size]byte),
	}

	sent, err := c.ResyncFileIfChanged(context.Background(), path, "one")
	if err != nil || !sent {
		t.Fatalf("resync without a recorded digest = (%v, %v), want (true, nil)", sent, err)
	}
	if fake.didChangeVersions[0] != 4 {
		t.Fatalf("didChange version = %d, want 4", fake.didChangeVersions[0])
	}
}

func TestClientDidCloseAndCloseAllFilesClearSyncedDigest(t *testing.T) {
	fake := &fakePowernapClient{}
	c := newResyncTestClient(fake)
	path := "/tmp/main.go"

	if _, err := c.DidOpen(context.Background(), path, "one"); err != nil {
		t.Fatalf("DidOpen() error = %v", err)
	}
	if _, ok := c.syncedDigest[path]; !ok {
		t.Fatal("DidOpen did not record the synced digest")
	}
	if err := c.DidClose(context.Background(), path); err != nil {
		t.Fatalf("DidClose() error = %v", err)
	}
	if _, ok := c.syncedDigest[path]; ok {
		t.Fatal("DidClose kept the synced digest")
	}

	if _, err := c.DidOpen(context.Background(), path, "one"); err != nil {
		t.Fatalf("DidOpen() error = %v", err)
	}
	c.CloseAllFiles(context.Background())
	if len(c.syncedDigest) != 0 {
		t.Fatalf("CloseAllFiles kept digests: %v", c.syncedDigest)
	}
}

func TestManagerResyncFileRoutesOnlyToOwningOpenClients(t *testing.T) {
	root := t.TempDir()
	goPath := filepath.Join(root, "main.go")
	tsPath := filepath.Join(root, "app.ts")
	otherGoPath := filepath.Join(root, "other.go")

	goFake := &fakePowernapClient{}
	tsFake := &fakePowernapClient{}
	mgr := &Manager{
		clients: map[clientKey]*Client{
			{name: "gopls", root: root}: {
				client:       goFake,
				name:         "gopls",
				cwd:          root,
				cfg:          config.LSPServerConfig{FileTypes: []string{".go"}},
				openFiles:    map[string]int32{goPath: 1},
				syncedDigest: map[string][sha256.Size]byte{},
			},
			{name: "typescript", root: root}: {
				client:       tsFake,
				name:         "typescript",
				cwd:          root,
				cfg:          config.LSPServerConfig{FileTypes: []string{".ts"}},
				openFiles:    map[string]int32{tsPath: 1},
				syncedDigest: map[string][sha256.Size]byte{},
			},
		},
	}

	mgr.ResyncFile(context.Background(), goPath, "package main")
	if len(goFake.didChangeURIs) != 1 {
		t.Fatalf("owning client didChange count = %d, want 1", len(goFake.didChangeURIs))
	}
	if len(tsFake.didChangeURIs) != 0 {
		t.Fatalf("non-owning client received didChange: %v", tsFake.didChangeURIs)
	}

	// A file the owning server never opened has no stale copy to fix.
	mgr.ResyncFile(context.Background(), otherGoPath, "package other")
	if len(goFake.didChangeURIs) != 1 {
		t.Fatalf("unopened file sent didChange: %v", goFake.didChangeURIs)
	}
}

// Reading must never launch a server: ResyncFile only talks to clients that are
// already running, so a configured server with no instance stays unstarted.
func TestManagerResyncFileNeverStartsServers(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	mgr := NewManager(&config.Config{LSP: config.LSPConfig{
		"gopls": {Command: "gopls", FileTypes: []string{".go"}},
	}}, root, nil)
	defer stopManager(mgr)

	mgr.ResyncFile(context.Background(), path, "package main")
	mgr.clientsMu.RLock()
	defer mgr.clientsMu.RUnlock()
	if len(mgr.clients) != 0 || len(mgr.starting) != 0 {
		t.Fatalf("resync started servers: clients=%d starting=%d", len(mgr.clients), len(mgr.starting))
	}
}

// A resync that finds the connection gone reclaims the instance and drops the
// diagnostics it published, so they are not shown as current; it does not
// restart the server.
func TestManagerResyncFileDisconnectReclaimsClient(t *testing.T) {
	mgr, path, c := newAfterWriteTestManager(t)
	fake := c.client.(*fakePowernapClient)
	c.name = "gopls"
	c.openFiles[path] = 1
	c.diagnostics[protocol.DocumentURI(c.pathToURI(path))] = []protocol.Diagnostic{{Message: "stale diagnostic", Severity: 1}}
	c.client = &failingNotificationClient{fakePowernapClient: fake, notify: func(context.Context) error { return jsonrpc2.ErrClosed }}

	mgr.ResyncFile(context.Background(), path, "package main // changed")
	mgr.clientsMu.RLock()
	clients, starting := len(mgr.clients), len(mgr.starting)
	mgr.clientsMu.RUnlock()
	if clients != 0 || starting != 0 || fake.kills != 1 {
		t.Fatalf("clients=%d starting=%d kills=%d", clients, starting, fake.kills)
	}
	if diags := mgr.currentFileDiagnostics(path); len(diags) != 0 {
		t.Fatalf("stale diagnostics survived the disconnect: %v", diags)
	}
}
