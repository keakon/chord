package lsp

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	powernap "github.com/keakon/x/powernap/pkg/lsp"
	"github.com/keakon/x/powernap/pkg/lsp/protocol"

	"github.com/keakon/chord/internal/config"
)

func TestAfterWriteToolResultCancelledWaitDoesNotAppendWaitNote(t *testing.T) {
	mgr, path, client := newAfterWriteTestManager(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	out := mgr.AfterWriteToolResult(ctx, path, "package main", "Successfully wrote 12 bytes", false)
	if strings.Contains(out, "Failed to sync buffer") {
		t.Fatalf("sync errors should no longer be appended to tool output: %q", out)
	}
	if out != "Successfully wrote 12 bytes" {
		t.Fatalf("non-actionable notes should not modify base output: %q", out)
	}
	if strings.Contains(out, "No diagnostic push received") {
		t.Fatalf("wait note should not be appended to tool output: %q", out)
	}

	_ = client
}

func TestAfterWriteToolResultAppendsCachedDiagnosticsWithoutWaitNote(t *testing.T) {
	mgr, path, client := newAfterWriteTestManager(t)
	uri := protocol.DocumentURI(client.pathToURI(path))
	client.diagnostics[uri] = []protocol.Diagnostic{
		{
			Severity: protocol.SeverityError,
			Range: protocol.Range{
				Start: protocol.Position{Line: 0, Character: 0},
				End:   protocol.Position{Line: 0, Character: 1},
			},
			Message: "expected package name",
			Source:  "gopls",
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	out := mgr.AfterWriteToolResult(ctx, path, "package main", "Successfully wrote 12 bytes", false)
	if !strings.Contains(out, "[E] 1:1 expected package name") {
		t.Fatalf("cached diagnostics should still be appended: %q", out)
	}
	if strings.Contains(out, "Failed to sync buffer") {
		t.Fatalf("sync errors should not leak to tool output: %q", out)
	}
	if strings.Contains(out, "No diagnostic push received") {
		t.Fatalf("wait note should not be appended to tool output: %q", out)
	}
}

func TestAfterWriteToolResultPassesCallerContextToDidChangeAndWaiter(t *testing.T) {
	mgr, path, _ := newAfterWriteTestManager(t)

	origDidChange := afterWriteDidChange
	origAwait := afterWriteAwaitWaiter
	t.Cleanup(func() {
		afterWriteDidChange = origDidChange
		afterWriteAwaitWaiter = origAwait
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var didChangeErr error
	var awaitErr error
	afterWriteDidChange = func(_ *Manager, gotCtx context.Context, gotPath string, content string) error {
		if gotPath != path {
			t.Fatalf("didChange path = %q, want %q", gotPath, path)
		}
		if content != "package main" {
			t.Fatalf("didChange content = %q, want package main", content)
		}
		didChangeErr = gotCtx.Err()
		return nil
	}
	afterWriteAwaitWaiter = func(_ *Manager, gotCtx context.Context, gotPath string, _ chan []Diagnostic, _ time.Duration) ([]Diagnostic, bool) {
		if gotPath != path {
			t.Fatalf("await path = %q, want %q", gotPath, path)
		}
		awaitErr = gotCtx.Err()
		return nil, false
	}

	out := mgr.AfterWriteToolResult(ctx, path, "package main", "Successfully wrote 12 bytes", false)
	if out != "Successfully wrote 12 bytes" {
		t.Fatalf("AfterWriteToolResult output = %q", out)
	}
	if didChangeErr != context.Canceled {
		t.Fatalf("didChange ctx err = %v, want context.Canceled", didChangeErr)
	}
	if awaitErr != context.Canceled {
		t.Fatalf("await ctx err = %v, want context.Canceled", awaitErr)
	}
}

func TestAfterWriteToolResultStartsMatchingServerBeforeWaiting(t *testing.T) {
	mgr, path, _ := newAfterWriteTestManager(t)

	origStart := afterWriteStart
	origHasReadyClient := afterWriteHasReadyClient
	origWait := afterWriteWaitForClient
	t.Cleanup(func() {
		afterWriteStart = origStart
		afterWriteHasReadyClient = origHasReadyClient
		afterWriteWaitForClient = origWait
	})

	var startedPath string
	afterWriteStart = func(_ *Manager, _ context.Context, gotPath string) {
		startedPath = gotPath
	}
	afterWriteHasReadyClient = func(_ *Manager, _ string) bool { return false }
	afterWriteWaitForClient = func(_ *Manager, _ context.Context, gotPath string, _ time.Duration) (*Client, bool) {
		if gotPath != path {
			t.Fatalf("wait path = %q, want %q", gotPath, path)
		}
		return nil, false
	}

	out := mgr.AfterWriteToolResult(context.Background(), path, "package main", "Successfully wrote 12 bytes", false)
	if startedPath != path {
		t.Fatalf("after-write should start matching server for %q, got %q", path, startedPath)
	}
	if out != "Successfully wrote 12 bytes" {
		t.Fatalf("non-actionable startup failures should not modify base output: %q", out)
	}
}

func newAfterWriteTestManager(t *testing.T) (*Manager, string, *Client) {
	t.Helper()

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
	client := &Client{
		client:      &powernap.Client{},
		cwd:         root,
		cfg:         config.LSPServerConfig{FileTypes: []string{".go"}},
		openFiles:   make(map[string]int32),
		diagnostics: make(map[protocol.DocumentURI][]protocol.Diagnostic),
	}
	mgr.clients["gopls"] = client
	return mgr, path, client
}
