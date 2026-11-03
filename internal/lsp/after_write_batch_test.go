package lsp

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keakon/golog"
	"github.com/keakon/golog/log"
	"github.com/keakon/x/powernap/pkg/lsp/protocol"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/logtest"
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
		if got := results[path]; strings.Contains(got.Output, "LSP diagnostics unavailable") || got.Status != FileWriteStatusReported {
			t.Fatalf("result for %s carries a degradation note: %+v", path, got)
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
	if batch[alpha].Output != single {
		t.Fatalf("batch result %q != single-file result %q", batch[alpha].Output, single)
	}
	if batch[alpha].Status != FileWriteStatusReported {
		t.Fatalf("batch result status = %v, want %v", batch[alpha].Status, FileWriteStatusReported)
	}
	if !strings.Contains(batch[alpha].Output, "undeclared name: sample") {
		t.Fatalf("batch result should carry the cached diagnostic: %q", batch[alpha].Output)
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
	if !strings.Contains(results[alpha].Output, "wait timed out") {
		t.Fatalf("timeout note missing from result: %q", results[alpha].Output)
	}
	if results[alpha].Status != FileWriteStatusTimeout {
		t.Fatalf("result status = %v, want %v", results[alpha].Status, FileWriteStatusTimeout)
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
		if result.Status != FileWriteStatusStartFailed {
			t.Fatalf("result status for %s = %v, want %v", path, result.Status, FileWriteStatusStartFailed)
		}
		if strings.Contains(result.Output, "LSP diagnostics unavailable") {
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

	done := make(chan map[string]FileWriteResult, 1)
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
// waits with the cold-start total budget, not only the first one — the servers
// start before any client wait, so a sibling's wait finishing the start must
// not make a later file of the same batch look warm. The budget is the total
// wall clock the batch spends (client wait + sync + diagnostics wait), so each
// file's diagnostics wait is what remains of it.
func TestAfterFileWritesToolResultGivesEveryFileTheColdStartBudget(t *testing.T) {
	mgr, alpha, beta := newBatchTestManager(t)

	var ready atomic.Bool
	afterWriteHasReadyClient = func(m *Manager, path string) bool { return ready.Load() }
	afterWriteWaitForClient = func(m *Manager, ctx context.Context, path string, timeout time.Duration) (*Client, bool) {
		// The server finishes starting during the first file's client wait, so
		// per-file readiness would see every later file as warm.
		ready.Store(true)
		if timeout > coldStartWriteBudget {
			t.Errorf("client wait timeout %v exceeds the cold-start total budget", timeout)
		}
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
		// Both files draw from the same batch-start budget; the immediate
		// client wait leaves nearly all of it for the diagnostics wait, so a
		// wide lower bound keeps the assertion timing-tolerant while still
		// proving the wait is the remainder of the total budget, not the warm
		// default and not the old 8s cold wait.
		if got := timeouts[path]; got <= 2*time.Second || got > coldStartWriteBudget {
			t.Fatalf("diagnostics wait timeout for %s = %v, want the remainder of the %v cold-start total budget", path, got, coldStartWriteBudget)
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

	notesPath := filepath.Join(root, "notes.txt")
	results := mgr.AfterFileWritesToolResult(context.Background(), []FileWrite{
		{Path: notesPath, Content: "hello\n", ChangeType: WatchedFileCreated},
	}, "")
	if got := results[notesPath]; got.Output != "" || got.Status != FileWriteStatusNoServer {
		t.Fatalf("serverless file result = %+v, want empty output with %v status", got, FileWriteStatusNoServer)
	}
	if starts != 0 {
		t.Fatalf("serverless file must not start any server, starts=%d", starts)
	}
}

// TestAfterFileWritesBatchRegistersWatchForEveryFile guards the batch watch
// contract: every file of a same-root batch must register a late-diagnostics
// watch with the client generations the batch's last sync produced. The files
// share one client, and each file's didChange advances that client's sync
// sequence, so a watch carrying the generations captured during its own
// preparation would be pruned at registration and only the last-synced file
// could ever claim a late publish — notified and timed-out writes alike.
func TestAfterFileWritesBatchRegistersWatchForEveryFile(t *testing.T) {
	for _, tc := range []struct {
		name     string
		notified bool
	}{{"notified", true}, {"timed out", false}} {
		t.Run(tc.name, func(t *testing.T) {
			mgr, alpha, beta := newBatchTestManager(t)
			for _, path := range []string{alpha, beta} {
				if err := os.WriteFile(path, []byte("package main\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			var mu sync.Mutex
			versions := make(map[string]int32)
			origDidChange := afterWriteDidChange
			t.Cleanup(func() { afterWriteDidChange = origDidChange })
			afterWriteDidChange = func(m *Manager, ctx context.Context, path, content string) (map[string]int32, map[clientKey]uint64, map[string]error) {
				v, g, errs := m.didChangeVersions(ctx, path, content)
				mu.Lock()
				if version, ok := v["gopls"]; ok {
					versions[path] = version
				}
				mu.Unlock()
				return v, g, errs
			}
			afterWriteAwaitWaiter = func(*Manager, context.Context, string, chan diagnosticsEvent, diagnosticsWaitRequest, time.Duration) ([]Diagnostic, bool, uint64, map[diagnosticIdentity]struct{}) {
				return nil, tc.notified, 0, nil
			}

			results := mgr.AfterFileWritesToolResult(context.Background(), []FileWrite{
				{Path: alpha, Content: "package main\n", ChangeType: WatchedFileChanged},
				{Path: beta, Content: "package main\n", ChangeType: WatchedFileChanged},
			}, "")
			for _, path := range []string{alpha, beta} {
				if got := results[path].Status; got != FileWriteStatusReported && got != FileWriteStatusTimeout {
					t.Fatalf("result status for %s = %v, want reported or timeout", path, got)
				}
			}

			mgr.lateMu.Lock()
			_, alphaWatched := mgr.lateWatches[alpha]
			_, betaWatched := mgr.lateWatches[beta]
			mgr.lateMu.Unlock()
			if !alphaWatched || !betaWatched {
				t.Fatalf("watches after batch = alpha:%v beta:%v, want both", alphaWatched, betaWatched)
			}

			publish := func(path string, version int32, message string) {
				uri := protocol.DocumentURI("file://" + filepath.ToSlash(path))
				client := mgr.clients[testKey(mgr, "gopls")]
				diags := []protocol.Diagnostic{{Severity: protocol.SeverityWarning, Message: message}}
				client.diagnosticsMu.Lock()
				client.diagnostics[uri] = diags
				client.diagnosticsMu.Unlock()
				mgr.onDiagnostics(testKey(mgr, "gopls"), nil)(string(uri), uriToPath(string(uri)), nil, "", diags, version)
			}
			mu.Lock()
			publish(alpha, versions[alpha], "alpha analyzer finding")
			publish(beta, versions[beta], "beta analyzer finding")
			mu.Unlock()

			got := mgr.PendingLateDiagnostics([]string{alpha, beta}, "", "")
			if len(got) != 2 {
				t.Fatalf("pending = %+v, want one entry per file", got)
			}
			for _, item := range got {
				switch item.Path {
				case alpha:
					if !strings.Contains(item.Lines, "alpha analyzer finding") {
						t.Fatalf("alpha lines = %q", item.Lines)
					}
				case beta:
					if !strings.Contains(item.Lines, "beta analyzer finding") {
						t.Fatalf("beta lines = %q", item.Lines)
					}
				default:
					t.Fatalf("unexpected pending path %v", item.Path)
				}
			}

			// A sync outside the batch invalidates the batch's watches. Dropping
			// alpha's is direct; beta's dies with the client-level generation
			// the shared client's sync sequence advanced — the accepted
			// limitation of client-level invalidation, which keeps a publish
			// from an older client state from ever posing as current.
			if err := os.WriteFile(alpha, []byte("package main\n\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			mgr.syncAfterWrite(context.Background(), alpha, "package main\n\n", WatchedFileChanged, true)
			mu.Lock()
			publish(alpha, versions[alpha], "alpha second finding")
			publish(beta, versions[beta], "beta second finding")
			mu.Unlock()
			got = mgr.PendingLateDiagnostics([]string{alpha, beta}, "", "")
			if len(got) != 0 {
				t.Fatalf("pending after an out-of-batch sync = %+v, want none", got)
			}
		})
	}
}

// syncLogBuffer is a log capture target safe for the batch pipeline's
// concurrent diagnostics waits, which each log their own breakdown line.
type syncLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncLogBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncLogBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// TestAfterFileWritesToolResultLogsBatchClientWait guards the breakdown log's
// client_wait_ms for batch writes: the wait runs in the parallel phase before
// the prepare pass rebuilds each entry's prepared struct, so storing the
// duration on that struct directly would be overwritten and every batch line
// would report a zero client wait.
func TestAfterFileWritesToolResultLogsBatchClientWait(t *testing.T) {
	mgr, alpha, beta := newBatchTestManager(t)

	logs := &syncLogBuffer{}
	log.SetDefaultLogger(logtest.NewLogger(logs, golog.DebugLevel))
	t.Cleanup(func() { log.SetDefaultLogger(logtest.NewLogger(nil, golog.InfoLevel)) })

	origWaitForClient := afterWriteWaitForClient
	afterWriteWaitForClient = func(m *Manager, ctx context.Context, path string, timeout time.Duration) (*Client, bool) {
		time.Sleep(10 * time.Millisecond)
		return origWaitForClient(m, ctx, path, timeout)
	}
	afterWriteAwaitWaiter = func(m *Manager, ctx context.Context, path string, ch chan diagnosticsEvent, req diagnosticsWaitRequest, timeout time.Duration) ([]Diagnostic, bool, uint64, map[diagnosticIdentity]struct{}) {
		return nil, true, 0, nil
	}

	mgr.AfterFileWritesToolResult(context.Background(), []FileWrite{
		{Path: alpha, Content: "package main\n", ChangeType: WatchedFileChanged},
		{Path: beta, Content: "package main\n", ChangeType: WatchedFileChanged},
	}, "")

	out := logs.String()
	if !strings.Contains(out, "after-write diagnostics breakdown") {
		t.Fatalf("breakdown log missing:\n%s", out)
	}
	if strings.Contains(out, "client_wait_ms=0") {
		t.Fatalf("batch breakdown reports a zero client wait:\n%s", out)
	}
}
