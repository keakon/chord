package lsp

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keakon/x/powernap/pkg/lsp/protocol"

	"github.com/keakon/chord/internal/config"
)

// restoreAfterWriteHooks snapshots the after-write pipeline test hooks and
// restores the snapshot when the test ends.
func restoreAfterWriteHooks(t *testing.T) {
	t.Helper()
	origHasReadyClient := afterWriteHasReadyClient
	origStart := afterWriteStart
	origWaitForClient := afterWriteWaitForClient
	origDidChange := afterWriteDidChange
	origDidSave := afterWriteDidSave
	origNotifyWatched := afterWriteNotifyWatchedFileChanged
	origAwaitWaiter := afterWriteAwaitWaiter
	t.Cleanup(func() {
		afterWriteHasReadyClient = origHasReadyClient
		afterWriteStart = origStart
		afterWriteWaitForClient = origWaitForClient
		afterWriteDidChange = origDidChange
		afterWriteDidSave = origDidSave
		afterWriteNotifyWatchedFileChanged = origNotifyWatched
		afterWriteAwaitWaiter = origAwaitWaiter
	})
}

// newBatchTestManager prepares a manager with one warm fake gopls client for
// root, plus paths for two files in that root.
func newBatchTestManager(t *testing.T) (*Manager, string, string) {
	t.Helper()
	restoreAfterWriteHooks(t)

	root := resolvedTempDir(t)
	mgr := NewManager(&config.Config{
		LSP: config.LSPConfig{
			"gopls": {Command: "gopls", FileTypes: []string{".go"}},
		},
	}, root, nil)
	client := &Client{
		client:      &fakePowernapClient{},
		cwd:         root,
		cfg:         config.LSPServerConfig{FileTypes: []string{".go"}},
		openFiles:   make(map[string]int32),
		diagnostics: make(map[protocol.DocumentURI][]protocol.Diagnostic),
	}
	mgr.clients[testKey(mgr, "gopls")] = client
	return mgr, filepath.Join(root, "alpha.go"), filepath.Join(root, "beta.go")
}

// TestAfterFileWritesToolResultSyncsAllFilesBeforeWaiting guards the batch
// invariant the pipeline exists for: no file's diagnostics wait may block
// another file's content sync. The await hook blocks until both files' syncs
// have run, so a serial implementation fails on its own await timeout instead
// of passing.
func TestAfterFileWritesToolResultSyncsAllFilesBeforeWaiting(t *testing.T) {
	mgr, alpha, beta := newBatchTestManager(t)

	var mu sync.Mutex
	synced := make(map[string]int)
	bothSynced := make(chan struct{})
	var bothOnce sync.Once
	afterWriteDidChange = func(m *Manager, ctx context.Context, path, content string) (map[string]int32, map[clientKey]uint64, map[string]error) {
		mu.Lock()
		synced[path]++
		count := len(synced)
		mu.Unlock()
		if count >= 2 {
			bothOnce.Do(func() { close(bothSynced) })
		}
		return m.didChangeVersions(ctx, path, content)
	}
	afterWriteAwaitWaiter = func(m *Manager, ctx context.Context, path string, ch chan diagnosticsEvent, req diagnosticsWaitRequest, timeout time.Duration) ([]Diagnostic, bool, uint64, map[diagnosticIdentity]struct{}) {
		select {
		case <-bothSynced:
		case <-time.After(2 * time.Second):
			t.Errorf("diagnostics wait for %s blocked before both files were synced", path)
			return nil, true, 0, nil
		}
		return nil, true, 0, nil
	}

	results := mgr.AfterFileWritesToolResult(context.Background(), []FileWrite{
		{Path: alpha, Content: "package main\n", ChangeType: WatchedFileChanged},
		{Path: beta, Content: "package main\n", ChangeType: WatchedFileChanged},
	}, "")
	for _, path := range []string{alpha, beta} {
		if got := results[path]; strings.Contains(got, "LSP diagnostics unavailable") {
			t.Fatalf("result for %s carries a degradation note: %q", path, got)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if synced[alpha] != 1 || synced[beta] != 1 {
		t.Fatalf("each file must sync exactly once, got %v", synced)
	}
}

// TestAfterFileWritesToolResultRegistersWaiterBeforeSync guards the freshness
// invariant per file: the diagnostics waiter must exist while the file's
// didChange goes out, so a fast server's publish is never missed.
func TestAfterFileWritesToolResultRegistersWaiterBeforeSync(t *testing.T) {
	mgr, alpha, beta := newBatchTestManager(t)
	afterWriteAwaitWaiter = func(m *Manager, ctx context.Context, path string, ch chan diagnosticsEvent, req diagnosticsWaitRequest, timeout time.Duration) ([]Diagnostic, bool, uint64, map[diagnosticIdentity]struct{}) {
		return nil, true, 0, nil
	}

	var mu sync.Mutex
	checked := 0
	afterWriteDidChange = func(m *Manager, ctx context.Context, path, content string) (map[string]int32, map[clientKey]uint64, map[string]error) {
		m.waitersMu.Lock()
		waiters := len(m.waiters[normalizeWaiterPath(path)])
		m.waitersMu.Unlock()
		if waiters == 0 {
			t.Errorf("didChange for %s ran without a registered waiter", path)
		}
		mu.Lock()
		checked++
		mu.Unlock()
		return m.didChangeVersions(ctx, path, content)
	}

	mgr.AfterFileWritesToolResult(context.Background(), []FileWrite{
		{Path: alpha, Content: "package main\n", ChangeType: WatchedFileChanged},
		{Path: beta, Content: "package main\n", ChangeType: WatchedFileChanged},
	}, "")
	mu.Lock()
	defer mu.Unlock()
	if checked != 2 {
		t.Fatalf("didChange hook ran %d times, want 2", checked)
	}
}

// TestAfterFileWritesToolResultMatchesSingleFilePipeline pins the batch result
// shape to the single-file pipeline: with one write and no wait-time signal,
// the batched result must equal what AfterFileWriteToolResult produces.
func TestAfterFileWritesToolResultMatchesSingleFilePipeline(t *testing.T) {
	mgr, alpha, _ := newBatchTestManager(t)
	client := mgr.clients[testKey(mgr, "gopls")]
	uri := protocol.DocumentURI(client.pathToURI(alpha))
	client.diagnostics[uri] = []protocol.Diagnostic{
		{Severity: protocol.SeverityError, Range: protocol.Range{Start: protocol.Position{Line: 2, Character: 0}}, Message: "undeclared name: sample"},
	}
	afterWriteAwaitWaiter = func(m *Manager, ctx context.Context, path string, ch chan diagnosticsEvent, req diagnosticsWaitRequest, timeout time.Duration) ([]Diagnostic, bool, uint64, map[diagnosticIdentity]struct{}) {
		return nil, true, 0, nil
	}

	ctx := context.Background()
	batch := mgr.AfterFileWritesToolResult(ctx, []FileWrite{
		{Path: alpha, Content: "package main\n", ChangeType: WatchedFileChanged},
	}, "")
	single := mgr.AfterFileWriteToolResult(ctx, alpha, "package main\n", "", false, WatchedFileChanged, "")
	if batch[alpha] != single {
		t.Fatalf("batch result %q != single-file result %q", batch[alpha], single)
	}
	if !strings.Contains(batch[alpha], "undeclared name: sample") {
		t.Fatalf("batch result should carry the cached diagnostic: %q", batch[alpha])
	}
}

// TestAfterFileWritesToolResultAppendsTimeoutNote keeps the bounded-wait
// contract: a wait that runs out without a publish appends the once-per-
// session degradation note instead of reporting a clean result.
func TestAfterFileWritesToolResultAppendsTimeoutNote(t *testing.T) {
	mgr, alpha, _ := newBatchTestManager(t)
	afterWriteAwaitWaiter = func(m *Manager, ctx context.Context, path string, ch chan diagnosticsEvent, req diagnosticsWaitRequest, timeout time.Duration) ([]Diagnostic, bool, uint64, map[diagnosticIdentity]struct{}) {
		return nil, false, 0, nil
	}

	results := mgr.AfterFileWritesToolResult(context.Background(), []FileWrite{
		{Path: alpha, Content: "package main\n", ChangeType: WatchedFileChanged},
	}, "")
	if !strings.Contains(results[alpha], "no diagnostics within") {
		t.Fatalf("timeout note missing from result: %q", results[alpha])
	}
}

// TestAfterFileWritesToolResultReportsStartFailure keeps the per-path failure
// contract: a server that never comes up yields the start degradation note for
// that path while the other files of the same batch still complete.
func TestAfterFileWritesToolResultReportsStartFailure(t *testing.T) {
	mgr, alpha, beta := newBatchTestManager(t)
	afterWriteWaitForClient = func(m *Manager, ctx context.Context, path string, timeout time.Duration) (*Client, bool) {
		return nil, false
	}

	results := mgr.AfterFileWritesToolResult(context.Background(), []FileWrite{
		{Path: alpha, Content: "package main\n", ChangeType: WatchedFileChanged},
		{Path: beta, Content: "package main\n", ChangeType: WatchedFileChanged},
	}, "")
	// The start note is once per session per server and root. Batch iteration
	// order is not part of the contract, so any one file of the root may carry
	// it; exactly one must, and the other must still complete.
	noted := 0
	for _, path := range []string{alpha, beta} {
		result, ok := results[path]
		if !ok {
			t.Fatalf("batch result missing for %s", path)
		}
		if strings.Contains(result, "LSP diagnostics unavailable") {
			noted++
		}
	}
	if noted != 1 {
		t.Fatalf("start-failure note appeared in %d results, want exactly 1: %v", noted, results)
	}
}

// TestAfterFileWritesToolResultWaitsForClientsConcurrently pins the batch
// startup budget: all servers are started before the client phase, so the
// per-file waits must overlap instead of serializing a hung launch into one
// full budget per file.
func TestAfterFileWritesToolResultWaitsForClientsConcurrently(t *testing.T) {
	mgr, alpha, beta := newBatchTestManager(t)

	started := make(chan string, 2)
	release := make(chan struct{})
	afterWriteWaitForClient = func(m *Manager, ctx context.Context, path string, timeout time.Duration) (*Client, bool) {
		started <- path
		<-release
		return m.clients[testKey(m, "gopls")], true
	}
	afterWriteAwaitWaiter = func(m *Manager, ctx context.Context, path string, ch chan diagnosticsEvent, req diagnosticsWaitRequest, timeout time.Duration) ([]Diagnostic, bool, uint64, map[diagnosticIdentity]struct{}) {
		return nil, true, 0, nil
	}

	done := make(chan map[string]string, 1)
	go func() {
		done <- mgr.AfterFileWritesToolResult(context.Background(), []FileWrite{
			{Path: alpha, Content: "package main\n", ChangeType: WatchedFileChanged},
			{Path: beta, Content: "package main\n", ChangeType: WatchedFileChanged},
		}, "")
	}()

	for range 2 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			close(release)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("batch did not exit after releasing the timed-out client wait")
			}
			t.Fatal("client waits did not run concurrently")
		}
	}
	close(release)
	select {
	case results := <-done:
		for _, path := range []string{alpha, beta} {
			if _, ok := results[path]; !ok {
				t.Fatalf("batch result missing for %s", path)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("batch did not finish after releasing the client waits")
	}
}

// TestAfterFileWritesToolResultGivesEveryFileTheColdStartBudget guards the
// batch cold-start contract: every file of a batch that starts a cold server
// waits with the cold-start diagnostics budget, not only the first one — the
// servers start before any client wait, so a sibling's wait finishing the
// start must not make a later file of the same batch look warm.
func TestAfterFileWritesToolResultGivesEveryFileTheColdStartBudget(t *testing.T) {
	mgr, alpha, beta := newBatchTestManager(t)

	var ready atomic.Bool
	afterWriteHasReadyClient = func(m *Manager, path string) bool { return ready.Load() }
	afterWriteWaitForClient = func(m *Manager, ctx context.Context, path string, timeout time.Duration) (*Client, bool) {
		// The server finishes starting during the first file's client wait, so
		// per-file readiness would see every later file as warm.
		ready.Store(true)
		return mgr.clients[testKey(mgr, "gopls")], true
	}
	var mu sync.Mutex
	timeouts := make(map[string]time.Duration)
	afterWriteAwaitWaiter = func(m *Manager, ctx context.Context, path string, ch chan diagnosticsEvent, req diagnosticsWaitRequest, timeout time.Duration) ([]Diagnostic, bool, uint64, map[diagnosticIdentity]struct{}) {
		mu.Lock()
		timeouts[path] = timeout
		mu.Unlock()
		return nil, true, 0, nil
	}

	mgr.AfterFileWritesToolResult(context.Background(), []FileWrite{
		{Path: alpha, Content: "package main\n", ChangeType: WatchedFileChanged},
		{Path: beta, Content: "package main\n", ChangeType: WatchedFileChanged},
	}, "")
	mu.Lock()
	defer mu.Unlock()
	for _, path := range []string{alpha, beta} {
		if got := timeouts[path]; got != coldStartDiagnosticsWaitTimeout {
			t.Fatalf("diagnostics wait timeout for %s = %v, want cold-start budget %v", path, got, coldStartDiagnosticsWaitTimeout)
		}
	}
}

// TestAfterFileWritesToolResultSkipsFilesWithoutServer keeps the skip
// contract: a file type no configured server covers produces an empty result
// and never starts or syncs anything.
func TestAfterFileWritesToolResultSkipsFilesWithoutServer(t *testing.T) {
	root := resolvedTempDir(t)
	restoreAfterWriteHooks(t)
	mgr := NewManager(&config.Config{}, root, nil)

	starts := 0
	afterWriteStart = func(m *Manager, ctx context.Context, path string) {
		starts++
		m.Start(ctx, path)
	}

	results := mgr.AfterFileWritesToolResult(context.Background(), []FileWrite{
		{Path: filepath.Join(root, "notes.txt"), Content: "hello\n", ChangeType: WatchedFileCreated},
	}, "")
	if got := results[filepath.Join(root, "notes.txt")]; got != "" {
		t.Fatalf("serverless file should produce an empty result, got %q", got)
	}
	if starts != 0 {
		t.Fatalf("serverless file must not start any server, starts=%d", starts)
	}
}
