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

func TestAfterFileWriteToolResultCancelledWaitDoesNotAppendWaitNote(t *testing.T) {
	mgr, path, client := newAfterWriteTestManager(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	out := mgr.AfterFileWriteToolResult(ctx, path, "package main", "Successfully wrote 12 bytes", false, WatchedFileChanged)
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

func TestAfterFileWriteToolResultAppendsCachedDiagnosticsWithoutWaitNote(t *testing.T) {
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

	out := mgr.AfterFileWriteToolResult(ctx, path, "package main", "Successfully wrote 12 bytes", false, WatchedFileChanged)
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

func TestAfterFileWriteToolResultPassesCallerContextToDidChangeAndWaiter(t *testing.T) {
	mgr, path, _ := newAfterWriteTestManager(t)

	origDidChange := afterWriteDidChange
	origNotify := afterWriteNotifyWatchedFileChanged
	origAwait := afterWriteAwaitWaiter
	t.Cleanup(func() {
		afterWriteDidChange = origDidChange
		afterWriteNotifyWatchedFileChanged = origNotify
		afterWriteAwaitWaiter = origAwait
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var didChangeErr error
	var notifyErr error
	var awaitErr error
	afterWriteNotifyWatchedFileChanged = func(_ *Manager, gotCtx context.Context, gotPath string, changeType protocol.FileChangeType) error {
		if gotPath != path {
			t.Fatalf("watched-file path = %q, want %q", gotPath, path)
		}
		if changeType != protocol.Changed {
			t.Fatalf("watched-file change type = %v, want Changed", changeType)
		}
		notifyErr = gotCtx.Err()
		return nil
	}
	afterWriteDidChange = func(_ *Manager, gotCtx context.Context, gotPath string, content string) (map[string]int32, error) {
		if gotPath != path {
			t.Fatalf("didChange path = %q, want %q", gotPath, path)
		}
		if content != "package main" {
			t.Fatalf("didChange content = %q, want package main", content)
		}
		didChangeErr = gotCtx.Err()
		return nil, nil
	}
	afterWriteAwaitWaiter = func(_ *Manager, gotCtx context.Context, gotPath string, _ chan diagnosticsEvent, _ diagnosticsWaitRequest, _ time.Duration) ([]Diagnostic, bool) {
		if gotPath != path {
			t.Fatalf("await path = %q, want %q", gotPath, path)
		}
		awaitErr = gotCtx.Err()
		return nil, false
	}

	out := mgr.AfterFileWriteToolResult(ctx, path, "package main", "Successfully wrote 12 bytes", false, WatchedFileChanged)
	if out != "Successfully wrote 12 bytes" {
		t.Fatalf("AfterFileWriteToolResult output = %q", out)
	}
	if didChangeErr != context.Canceled {
		t.Fatalf("didChange ctx err = %v, want context.Canceled", didChangeErr)
	}
	if notifyErr != context.Canceled {
		t.Fatalf("watched-file ctx err = %v, want context.Canceled", notifyErr)
	}
	if awaitErr != context.Canceled {
		t.Fatalf("await ctx err = %v, want context.Canceled", awaitErr)
	}
}

func TestAfterFileWriteToolResultNotifiesWatchedFileBeforeDidChange(t *testing.T) {
	mgr, path, _ := newAfterWriteTestManager(t)

	origNotify := afterWriteNotifyWatchedFileChanged
	origDidChange := afterWriteDidChange
	origAwait := afterWriteAwaitWaiter
	t.Cleanup(func() {
		afterWriteNotifyWatchedFileChanged = origNotify
		afterWriteDidChange = origDidChange
		afterWriteAwaitWaiter = origAwait
	})

	var order []string
	afterWriteNotifyWatchedFileChanged = func(_ *Manager, _ context.Context, gotPath string, changeType protocol.FileChangeType) error {
		if gotPath != path {
			t.Fatalf("watched-file path = %q, want %q", gotPath, path)
		}
		if changeType != protocol.Created {
			t.Fatalf("watched-file change type = %v, want Created", changeType)
		}
		order = append(order, "watched")
		return nil
	}
	afterWriteDidChange = func(_ *Manager, _ context.Context, gotPath string, content string) (map[string]int32, error) {
		if gotPath != path {
			t.Fatalf("didChange path = %q, want %q", gotPath, path)
		}
		if content != "package main" {
			t.Fatalf("didChange content = %q, want package main", content)
		}
		order = append(order, "didChange")
		return nil, nil
	}
	afterWriteAwaitWaiter = func(_ *Manager, _ context.Context, _ string, _ chan diagnosticsEvent, _ diagnosticsWaitRequest, _ time.Duration) ([]Diagnostic, bool) {
		return nil, false
	}

	out := mgr.AfterFileWriteToolResult(context.Background(), path, "package main", "Successfully wrote 12 bytes", false, WatchedFileCreated)
	if out != "Successfully wrote 12 bytes" {
		t.Fatalf("AfterFileWriteToolResult output = %q", out)
	}
	if got := strings.Join(order, ","); got != "watched,didChange" {
		t.Fatalf("notification order = %q, want watched,didChange", got)
	}
}

func TestAfterFileWriteToolResultSkipsDisabledMatchingServer(t *testing.T) {
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
	afterWriteStart = func(_ *Manager, _ context.Context, _ string) {
		t.Fatal("disabled gopls should not be started after write")
	}
	afterWriteWaitForClient = func(_ *Manager, _ context.Context, _ string, _ time.Duration) (*Client, bool) {
		t.Fatal("disabled gopls should not be waited on after write")
		return nil, false
	}
	afterWriteDidChange = func(_ *Manager, _ context.Context, _ string, _ string) (map[string]int32, error) {
		t.Fatal("disabled gopls should not receive didChange after write")
		return nil, nil
	}
	afterWriteNotifyWatchedFileChanged = func(_ *Manager, _ context.Context, _ string, _ protocol.FileChangeType) error {
		t.Fatal("disabled gopls should not receive watched-file notifications after write")
		return nil
	}
	afterWriteAwaitWaiter = func(_ *Manager, _ context.Context, _ string, _ chan diagnosticsEvent, _ diagnosticsWaitRequest, _ time.Duration) ([]Diagnostic, bool) {
		t.Fatal("disabled gopls diagnostics should not be awaited after write")
		return nil, false
	}

	out := mgr.AfterFileWriteToolResult(context.Background(), path, "package main", "Successfully wrote 12 bytes", false, WatchedFileChanged)
	if out != "Successfully wrote 12 bytes" {
		t.Fatalf("AfterFileWriteToolResult output = %q", out)
	}
}

func TestAfterFileWriteToolResultStartsMatchingServerBeforeWaiting(t *testing.T) {
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

	out := mgr.AfterFileWriteToolResult(context.Background(), path, "package main", "Successfully wrote 12 bytes", false, WatchedFileChanged)
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
