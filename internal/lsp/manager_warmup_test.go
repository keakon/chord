package lsp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keakon/x/powernap/pkg/lsp/protocol"

	"github.com/keakon/chord/internal/config"
)

// newWarmupTestManager prepares a manager with a gopls-shaped config and a
// written Go file and a simulated pending launch: the cold-start state after
// the write pipeline has started its server.
func newWarmupTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	restoreAfterWriteHooks(t)
	root := resolvedTempDir(t)
	mgr := NewManager(&config.Config{
		LSP: config.LSPConfig{
			"gopls": {Command: "gopls", FileTypes: []string{".go"}},
		},
	}, root, nil)
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	seedPendingLaunch(t, mgr, testKey(mgr, "gopls"))
	return mgr, path
}

func seedPendingLaunch(t *testing.T, mgr *Manager, key clientKey) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan struct{})
	close(done)
	mgr.starting[key] = true
	mgr.launches[key] = &startEntry{ctx: ctx, cancel: cancel, done: done, ready: make(chan struct{})}
}

func (m *Manager) pendingSyncCount() int {
	m.pendingSyncMu.Lock()
	defer m.pendingSyncMu.Unlock()
	return len(m.pendingSyncs)
}

// TestPendingSyncResyncsAtAdmissionAndClaimsLate covers the client-wait
// timeout catch-up: a write whose client never appeared parks a re-sync, the
// admission of that client re-sends the notifications and arms a late watch,
// and the analysis the server publishes afterwards is claimable by the next
// request.
func TestPendingSyncResyncsAtAdmissionAndClaimsLate(t *testing.T) {
	mgr, path := newWarmupTestManager(t)
	key := testKey(mgr, "gopls")

	afterWriteStart = func(*Manager, context.Context, string) {}
	afterWriteWaitForClient = func(*Manager, context.Context, string, time.Duration) (*Client, bool) {
		return nil, false // the launch is slow; the client-wait budget runs out
	}

	out := mgr.AfterFileWriteToolResult(context.Background(), path, "package main\n", "wrote", false, WatchedFileChanged, "")
	if !strings.Contains(out, "LSP diagnostics unavailable") {
		t.Fatalf("client-wait timeout output = %q, want the start-failure note", out)
	}
	if got := mgr.pendingSyncCount(); got != 1 {
		t.Fatalf("pending syncs = %d, want the timed-out write parked", got)
	}

	// The launch finishes and admits its client. No pipeline hooks are
	// overridden: the re-sync must go through the real notification sequence
	// against the fake client. The fake declares didSave support so the full
	// sequence is observable.
	client := &Client{
		client:      &fakePowernapClient{saveOptions: &protocol.SaveOptions{}},
		cwd:         mgr.projectRootPath(),
		cfg:         config.LSPServerConfig{FileTypes: []string{".go"}},
		openFiles:   make(map[string]int32),
		diagnostics: make(map[protocol.DocumentURI][]protocol.Diagnostic),
	}
	mgr.clientsMu.Lock()
	mgr.clients[key] = client
	mgr.clientsMu.Unlock()

	mgr.resyncPendingWrites(context.Background())

	if got := mgr.pendingSyncCount(); got != 0 {
		t.Fatalf("pending syncs after admission = %d, want the entry consumed", got)
	}
	fake := client.client.(*fakePowernapClient)
	if got := fake.syncedURIs(); len(got) != 1 {
		t.Fatalf("synced URIs = %v, want the parked write re-synchronized", got)
	}
	if got := fake.watchedFileEvents; len(got) != 1 {
		t.Fatalf("watched file events = %v, want the workspace change notification", got)
	}
	if got := fake.didOpenURIs; len(got) != 1 {
		t.Fatalf("didOpen URIs = %v, want the content push (the server never had the document)", got)
	}
	if got := fake.didSaveURIs; len(got) != 1 {
		t.Fatalf("didSave URIs = %v, want the save notification", got)
	}
	mgr.lateMu.Lock()
	_, watched := mgr.lateWatches[path]
	mgr.lateMu.Unlock()
	if !watched {
		t.Fatal("the re-synchronized write did not arm a late watch")
	}

	// The server's first analysis pass for the synced version is claimable.
	uri := protocol.DocumentURI("file://" + filepath.ToSlash(path))
	diags := []protocol.Diagnostic{{Severity: protocol.SeverityError, Message: "undefined: missing"}}
	client.diagnosticsMu.Lock()
	client.diagnostics[uri] = diags
	client.diagnosticsMu.Unlock()
	mgr.onDiagnostics(key, nil)(string(uri), uriToPath(string(uri)), nil, "", diags, 1)

	got := mgr.PendingLateDiagnostics([]string{path}, "", "")
	if len(got) != 1 || !strings.Contains(got[0].Lines, "undefined: missing") {
		t.Fatalf("pending after resync publish = %+v, want the first analysis finding", got)
	}
}

// resyncPublishRacer is a fake client whose first content push carries a
// synchronous diagnostics publish: the server's reply races the re-sync
// instead of landing after the watch registered.
type resyncPublishRacer struct {
	fakePowernapClient
	onOpen func(version int)
}

func (f *resyncPublishRacer) NotifyDidOpenTextDocument(ctx context.Context, uri, languageID string, version int, text string) error {
	if err := f.fakePowernapClient.NotifyDidOpenTextDocument(ctx, uri, languageID, version, text); err != nil {
		return err
	}
	if f.onOpen != nil {
		f.onOpen(version)
	}
	return nil
}

// TestPendingSyncResyncClaimsPublishRacingSync pins the re-sync's replay
// coverage: a publish for the synced version that lands while the catch-up
// sync is still running — before any watch exists — is recorded in the replay
// ring and must be claimable by the next request. A watch sequence captured
// only after the sync would filter that publish out and strand the write's
// only analysis pass unclaimed.
func TestPendingSyncResyncClaimsPublishRacingSync(t *testing.T) {
	mgr, path := newWarmupTestManager(t)
	key := testKey(mgr, "gopls")
	mgr.registerPendingSync(path, "package main\n", WatchedFileChanged, "")

	client := &Client{
		cwd:         mgr.projectRootPath(),
		cfg:         config.LSPServerConfig{FileTypes: []string{".go"}},
		openFiles:   make(map[string]int32),
		diagnostics: make(map[protocol.DocumentURI][]protocol.Diagnostic),
	}
	uri := protocol.DocumentURI("file://" + filepath.ToSlash(path))
	diags := []protocol.Diagnostic{{Severity: protocol.SeverityError, Message: "undefined: missing"}}
	racer := &resyncPublishRacer{}
	racer.saveOptions = new(protocol.SaveOptions{})
	racer.onOpen = func(version int) {
		// The server answers the content push while the re-sync is still
		// running: no watch exists yet, so this publish only reaches the
		// replay ring.
		mgr.onDiagnostics(key, nil)(string(uri), uriToPath(string(uri)), nil, "", diags, int32(version))
	}
	client.client = racer
	mgr.clientsMu.Lock()
	mgr.clients[key] = client
	mgr.clientsMu.Unlock()

	mgr.resyncPendingWrites(context.Background())

	got := mgr.PendingLateDiagnostics([]string{path}, "", "")
	if len(got) != 1 || !strings.Contains(got[0].Lines, "undefined: missing") {
		t.Fatalf("pending after a publish racing the re-sync = %+v, want the synced version's finding", got)
	}
}

// TestPendingSyncSurvivesTurnCancellation pins the parking rule: the
// canceled-context guard only stops the write's own pipeline from parking an
// entry; a turn that ends after the pipeline returned does not undo the
// parking. The file did land on disk, so its diagnostics still belong to the
// next request once the server is up.
func TestPendingSyncSurvivesTurnCancellation(t *testing.T) {
	mgr, path := newWarmupTestManager(t)
	key := testKey(mgr, "gopls")

	afterWriteStart = func(*Manager, context.Context, string) {}
	afterWriteWaitForClient = func(*Manager, context.Context, string, time.Duration) (*Client, bool) {
		return nil, false // the launch is slow; the client-wait budget runs out
	}

	ctx, cancel := context.WithCancel(context.Background())
	mgr.AfterFileWriteToolResult(ctx, path, "package main\n", "wrote", false, WatchedFileChanged, "")
	if got := mgr.pendingSyncCount(); got != 1 {
		t.Fatalf("pending syncs = %d, want the timed-out write parked", got)
	}
	cancel()

	client := &Client{
		client:      &fakePowernapClient{},
		cwd:         mgr.projectRootPath(),
		cfg:         config.LSPServerConfig{FileTypes: []string{".go"}},
		openFiles:   make(map[string]int32),
		diagnostics: make(map[protocol.DocumentURI][]protocol.Diagnostic),
	}
	mgr.clientsMu.Lock()
	mgr.clients[key] = client
	mgr.clientsMu.Unlock()

	mgr.resyncPendingWrites(context.Background())

	if got := mgr.pendingSyncCount(); got != 0 {
		t.Fatalf("pending syncs after admission = %d, want the entry consumed", got)
	}
	fake := client.client.(*fakePowernapClient)
	if got := fake.syncedURIs(); len(got) != 1 {
		t.Fatalf("synced URIs = %v, want the parked write re-synchronized despite the canceled turn", got)
	}
	mgr.lateMu.Lock()
	_, watched := mgr.lateWatches[path]
	mgr.lateMu.Unlock()
	if !watched {
		t.Fatal("the re-synchronized write did not arm a late watch")
	}
}

// TestPendingSyncReplacedByLaterWrite guards the one-entry-per-path rule: a
// later write to the same path replaces the parked entry, and a re-sync of a
// stale snapshot must not resurrect the older content.
func TestPendingSyncReplacedByLaterWrite(t *testing.T) {
	mgr, path := newWarmupTestManager(t)

	mgr.registerPendingSync(path, "older content", WatchedFileChanged, "")
	mgr.registerPendingSync(path, "newer content", WatchedFileChanged, "")
	if got := mgr.pendingSyncCount(); got != 1 {
		t.Fatalf("pending syncs = %d, want one", got)
	}

	mgr.pendingSyncMu.Lock()
	entry := mgr.pendingSyncs[path]
	mgr.pendingSyncMu.Unlock()
	if entry.content != "newer content" {
		t.Fatalf("parked content = %q, want the newer write", entry.content)
	}
	stale := pendingSync{content: "older content", changeType: WatchedFileChanged, mtime: entry.mtime}
	if mgr.takePendingSync(path, stale) {
		t.Fatal("took a pending sync that a newer write had replaced")
	}
	if !mgr.takePendingSync(path, entry) {
		t.Fatal("could not take the pending sync it matched")
	}
}

// TestPendingSyncDroppedAfterExternalChange guards the attribution rule: when
// the file changed on disk between the parking and the admission, the parked
// write no longer describes the disk and must be dropped instead of pushing
// its old content into the server.
func TestPendingSyncDroppedAfterExternalChange(t *testing.T) {
	mgr, path := newWarmupTestManager(t)
	key := testKey(mgr, "gopls")

	mgr.registerPendingSync(path, "package main\n", WatchedFileChanged, "")
	past := time.Now().Add(-2 * time.Second)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatal(err)
	}

	client := &Client{
		client:      &fakePowernapClient{},
		cwd:         mgr.projectRootPath(),
		cfg:         config.LSPServerConfig{FileTypes: []string{".go"}},
		openFiles:   make(map[string]int32),
		diagnostics: make(map[protocol.DocumentURI][]protocol.Diagnostic),
	}
	mgr.clientsMu.Lock()
	mgr.clients[key] = client
	mgr.clientsMu.Unlock()

	mgr.resyncPendingWrites(context.Background())

	if got := mgr.pendingSyncCount(); got != 0 {
		t.Fatalf("pending syncs = %d, want the externally changed entry dropped", got)
	}
	fake := client.client.(*fakePowernapClient)
	if got := fake.syncedURIs(); len(got) != 0 {
		t.Fatalf("synced URIs = %v, want no re-sync of stale content", got)
	}
}

// TestClearPendingSyncsForLaunchKeepsOtherServers guards the per-server
// clearing: a failed launch only drops the entries it would have served.
func TestClearPendingSyncsForLaunchKeepsOtherServers(t *testing.T) {
	root := resolvedTempDir(t)
	mgr := NewManager(&config.Config{
		LSP: config.LSPConfig{
			"gopls":    {Command: "gopls", FileTypes: []string{".go"}},
			"tsserver": {Command: "typescript-language-server", FileTypes: []string{".ts"}},
		},
	}, root, nil)
	seedPendingLaunch(t, mgr, testKey(mgr, "gopls"))
	seedPendingLaunch(t, mgr, testKey(mgr, "tsserver"))
	goPath := filepath.Join(root, "main.go")
	tsPath := filepath.Join(root, "app.ts")
	for _, path := range []string{goPath, tsPath} {
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mgr.registerPendingSync(goPath, "package main\n", WatchedFileChanged, "")
	mgr.registerPendingSync(tsPath, "let x = 1;\n", WatchedFileChanged, "")

	mgr.clearPendingSyncsForLaunch(clientKey{name: "gopls", root: mgr.projectRootPath()})

	if got := mgr.pendingSyncCount(); got != 1 {
		t.Fatalf("pending syncs = %d, want only the .ts entry left", got)
	}
	mgr.pendingSyncMu.Lock()
	_, tsKept := mgr.pendingSyncs[tsPath]
	mgr.pendingSyncMu.Unlock()
	if !tsKept {
		t.Fatal("the failed gopls launch dropped another server's parked write")
	}
}

// TestStopClearsWarmupState guards the idle-unload contract: a stopped manager
// forgets admission times and parked re-syncs, so the next start is an honest
// cold start.
func TestStopClearsWarmupState(t *testing.T) {
	mgr, path := newWarmupTestManager(t)
	key := testKey(mgr, "gopls")

	mgr.registerPendingSync(path, "package main\n", WatchedFileChanged, "")
	mgr.clientsMu.Lock()
	mgr.admittedAt[key] = time.Now()
	mgr.clientsMu.Unlock()

	mgr.Stop(context.Background())

	mgr.clientsMu.RLock()
	_, admitted := mgr.admittedAt[key]
	mgr.clientsMu.RUnlock()
	if admitted {
		t.Fatal("Stop kept the admission time")
	}
	if got := mgr.pendingSyncCount(); got != 0 {
		t.Fatalf("pending syncs after Stop = %d, want none", got)
	}
}

// TestWaitForClientForPathResolvesOnLaunchReady pins the event-driven
// readiness wait: a pending launch's ready event resolves the wait the moment
// the client registers, with no polling delay in between.
func TestWaitForClientForPathResolvesOnLaunchReady(t *testing.T) {
	mgr := NewManager(&config.Config{
		// A command that does not exist keeps the manager's own Start inert if
		// the manually registered launch were ever removed.
		LSP: config.LSPConfig{
			"gopls": {Command: "chord-no-such-gopls", FileTypes: []string{".go"}},
		},
	}, resolvedTempDir(t), nil)
	path := filepath.Join(mgr.projectRootPath(), "main.go")
	if err := os.WriteFile(path, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	key := testKey(mgr, "gopls")
	entry := &startEntry{ctx: context.Background(), done: make(chan struct{}), ready: make(chan struct{})}
	mgr.clientsMu.Lock()
	mgr.starting[key] = true
	mgr.launches[key] = entry
	mgr.clientsMu.Unlock()

	type result struct {
		client *Client
		ok     bool
	}
	done := make(chan result, 1)
	go func() {
		c, ok := mgr.waitForClientForPath(context.Background(), path, 5*time.Second)
		done <- result{client: c, ok: ok}
	}()

	select {
	case r := <-done:
		t.Fatalf("wait resolved while the launch was pending: %+v", r)
	case <-time.After(50 * time.Millisecond):
	}

	client := &Client{
		client:      &fakePowernapClient{},
		cwd:         mgr.projectRootPath(),
		cfg:         config.LSPServerConfig{FileTypes: []string{".go"}},
		openFiles:   make(map[string]int32),
		diagnostics: make(map[protocol.DocumentURI][]protocol.Diagnostic),
	}
	mgr.clientsMu.Lock()
	mgr.clients[key] = client
	mgr.clientsMu.Unlock()
	close(entry.ready)

	select {
	case r := <-done:
		if !r.ok || r.client != client {
			t.Fatalf("wait after admission = (%v, %v), want the registered client", r.client, r.ok)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the launch ready event did not resolve the client wait")
	}
}

// TestWaitForClientForPathResolvesWhenLaunchFails pins the failure half: a
// launch that settles without registering a client ends the wait immediately,
// so the caller reports the start failure instead of waiting out its budget.
func TestWaitForClientForPathResolvesWhenLaunchFails(t *testing.T) {
	mgr := NewManager(&config.Config{
		LSP: config.LSPConfig{
			"gopls": {Command: "chord-no-such-gopls", FileTypes: []string{".go"}},
		},
	}, resolvedTempDir(t), nil)
	path := filepath.Join(mgr.projectRootPath(), "main.go")
	if err := os.WriteFile(path, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	key := testKey(mgr, "gopls")
	entry := &startEntry{ctx: context.Background(), done: make(chan struct{}), ready: make(chan struct{})}
	mgr.clientsMu.Lock()
	mgr.starting[key] = true
	mgr.launches[key] = entry
	mgr.clientsMu.Unlock()

	type result struct {
		client *Client
		ok     bool
	}
	done := make(chan result, 1)
	go func() {
		c, ok := mgr.waitForClientForPath(context.Background(), path, 5*time.Second)
		done <- result{client: c, ok: ok}
	}()
	select {
	case r := <-done:
		t.Fatalf("wait resolved while the launch was pending: %+v", r)
	case <-time.After(50 * time.Millisecond):
	}

	// The launch's exit path clears its registry markers before the readiness
	// event fires; mirror that order so the wait sees the settled state.
	mgr.clientsMu.Lock()
	delete(mgr.starting, key)
	delete(mgr.launches, key)
	mgr.clientsMu.Unlock()
	close(entry.ready)
	select {
	case r := <-done:
		if r.ok {
			t.Fatalf("wait after a failed launch = (%v, true), want no client", r.client)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the failed launch did not resolve the client wait")
	}
}

// TestAfterFileWriteWaitBudgetSelection pins the budget table: the warm
// default comes from the config, a client admitted within the warmup grace
// draws the cold total budget, an admitted-ago client draws the warm default,
// and any owner within the grace is enough.
func TestAfterFileWriteWaitBudgetSelection(t *testing.T) {
	type setup struct {
		name        string
		admittedAgo time.Duration
		waitMS      *int
		secondRoot  bool
		wantMin     time.Duration
		wantMax     time.Duration
	}
	cases := []setup{
		{name: "warm default", admittedAgo: -1, wantMin: diagnosticsWaitTimeout - 100*time.Millisecond, wantMax: diagnosticsWaitTimeout + 100*time.Millisecond},
		{name: "configured value", admittedAgo: -1, waitMS: new(1500), wantMin: 1500 * time.Millisecond, wantMax: 1500 * time.Millisecond},
		{name: "admitted within grace", admittedAgo: 2 * time.Second, wantMin: 2 * time.Second, wantMax: coldStartWriteBudget + 50*time.Millisecond},
		{name: "admitted beyond grace", admittedAgo: warmupGrace + time.Second, wantMin: diagnosticsWaitTimeout - 100*time.Millisecond, wantMax: diagnosticsWaitTimeout + 100*time.Millisecond},
		{name: "any owner within grace", admittedAgo: warmupGrace + time.Second, secondRoot: true, wantMin: 2 * time.Second, wantMax: coldStartWriteBudget + 50*time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mgr, path := newWarmupTestManager(t)
			mgr.cfg.Diagnostics.LSP.WaitTimeoutMS = tc.waitMS

			client := &Client{
				client:      &fakePowernapClient{},
				cwd:         mgr.projectRootPath(),
				cfg:         config.LSPServerConfig{FileTypes: []string{".go"}},
				openFiles:   make(map[string]int32),
				diagnostics: make(map[protocol.DocumentURI][]protocol.Diagnostic),
			}
			mgr.clientsMu.Lock()
			key := testKey(mgr, "gopls")
			mgr.clients[key] = client
			if tc.admittedAgo >= 0 {
				mgr.admittedAt[key] = time.Now().Add(-tc.admittedAgo)
			}
			if tc.secondRoot {
				nested := filepath.Join(mgr.projectRootPath(), "nested")
				if err := os.MkdirAll(nested, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(nested, ".git"), []byte("gitdir: ../.git\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				nestedPath := filepath.Join(nested, "main.go")
				if err := os.WriteFile(nestedPath, []byte("package nested\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				mgr.clients[clientKey{name: "gopls", root: nested}] = &Client{
					client:      &fakePowernapClient{},
					cwd:         nested,
					cfg:         config.LSPServerConfig{FileTypes: []string{".go"}},
					openFiles:   make(map[string]int32),
					diagnostics: make(map[protocol.DocumentURI][]protocol.Diagnostic),
				}
				mgr.admittedAt[clientKey{name: "gopls", root: nested}] = time.Now()
				path = nestedPath
			}
			mgr.clientsMu.Unlock()

			var mu sync.Mutex
			var got time.Duration
			afterWriteAwaitWaiter = func(_ *Manager, _ context.Context, _ string, _ chan diagnosticsEvent, _ diagnosticsWaitRequest, timeout time.Duration) ([]Diagnostic, bool, uint64, map[diagnosticIdentity]struct{}) {
				mu.Lock()
				got = timeout
				mu.Unlock()
				return nil, true, 0, nil
			}

			mgr.AfterFileWriteToolResult(context.Background(), path, "package main\n", "wrote", false, WatchedFileChanged, "")
			mu.Lock()
			defer mu.Unlock()
			if got < tc.wantMin || got > tc.wantMax {
				t.Fatalf("diagnostics wait = %v, want between %v and %v", got, tc.wantMin, tc.wantMax)
			}
		})
	}
}

// TestAfterFileWriteBudgetExhaustedStillWatches pins the end of the budget
// table: a client that appears just as the cold-start total budget runs out
// leaves no diagnostics wait at all, but the write is still synchronized and
// its late watch still arms — the catch-up does not depend on the wait having
// had time.
func TestAfterFileWriteBudgetExhaustedStillWatches(t *testing.T) {
	mgr, path := newWarmupTestManager(t)
	key := testKey(mgr, "gopls")

	afterWriteStart = func(*Manager, context.Context, string) {}
	afterWriteWaitForClient = func(_ *Manager, _ context.Context, _ string, timeout time.Duration) (*Client, bool) {
		// The launch takes the whole remaining budget: the client appears as
		// the total budget runs out.
		time.Sleep(timeout)
		return nil, true
	}
	afterWriteNotifyWatchedFileChanged = func(*Manager, context.Context, string, protocol.FileChangeType) map[string]error { return nil }
	afterWriteDidChange = func(m *Manager, _ context.Context, _ string, _ string) (map[string]int32, map[clientKey]uint64, map[string]error) {
		generation := m.noteLateDocumentSync(key)
		return map[string]int32{"gopls": 2}, map[clientKey]uint64{key: m.recordLateDocumentSync(key, generation)}, nil
	}
	afterWriteDidSave = func(*Manager, context.Context, string, string) map[string]error { return nil }
	var gotWait time.Duration
	afterWriteAwaitWaiter = func(_ *Manager, _ context.Context, _ string, _ chan diagnosticsEvent, _ diagnosticsWaitRequest, timeout time.Duration) ([]Diagnostic, bool, uint64, map[diagnosticIdentity]struct{}) {
		gotWait = timeout
		return nil, false, 0, nil
	}

	out := mgr.AfterFileWriteToolResult(context.Background(), path, "package main\n", "wrote", false, WatchedFileChanged, "")

	waitWritePreparations(t, mgr)
	if gotWait != 0 {
		t.Fatalf("diagnostics wait = %v, want 0: the client wait consumed the whole budget", gotWait)
	}
	if got := mgr.pendingSyncCount(); got != 0 {
		t.Fatalf("pending syncs = %d, want none: the client did appear", got)
	}
	if !strings.Contains(out, "eligible versioned diagnostics may follow with the next request") {
		t.Fatalf("output = %q, want the deferred diagnostics note", out)
	}
	mgr.lateMu.Lock()
	_, watched := mgr.lateWatches[path]
	mgr.lateMu.Unlock()
	if !watched {
		t.Fatal("the budget-exhausted write did not arm a late watch")
	}
}

// TestPythonAfterWriteKeepsColdStartDiagnosticsWait guards the Python carve
// out: the semantic backend keeps its own 8s cold-start wait, decoupled from
// the generic total budget, because a timeout there falls back to Ruff rather
// than a late watch.
func TestPythonAfterWriteKeepsColdStartDiagnosticsWait(t *testing.T) {
	restoreAfterWriteHooks(t)
	root := resolvedTempDir(t)
	mgr := NewManager(&config.Config{
		LSP: config.LSPConfig{
			"pyright": {Command: "pyright-langserver", FileTypes: []string{".py"}},
		},
	}, root, nil)
	path := filepath.Join(root, "main.py")
	if err := os.WriteFile(path, []byte("x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The launch is simulated: a real pyright would start here otherwise.
	afterWriteStart = func(*Manager, context.Context, string) {}
	afterWriteWaitForClient = func(m *Manager, ctx context.Context, p string, timeout time.Duration) (*Client, bool) {
		client := &Client{
			client:      &fakePowernapClient{},
			cwd:         m.projectRootPath(),
			cfg:         config.LSPServerConfig{FileTypes: []string{".py"}},
			openFiles:   make(map[string]int32),
			diagnostics: make(map[protocol.DocumentURI][]protocol.Diagnostic),
		}
		return client, true
	}

	var mu sync.Mutex
	var got time.Duration
	afterWriteAwaitWaiter = func(_ *Manager, _ context.Context, _ string, _ chan diagnosticsEvent, _ diagnosticsWaitRequest, timeout time.Duration) ([]Diagnostic, bool, uint64, map[diagnosticIdentity]struct{}) {
		mu.Lock()
		got = timeout
		mu.Unlock()
		return nil, false, 0, nil
	}

	result := mgr.afterWriteLSPToolResult(context.Background(), path, "x = 1\n", "wrote", false, nil, WatchedFileChanged, "")
	mu.Lock()
	defer mu.Unlock()
	if got != coldStartDiagnosticsWaitTimeout {
		t.Fatalf("python cold diagnostics wait = %v, want %v", got, coldStartDiagnosticsWaitTimeout)
	}
	_ = result
}

// TestPendingSyncNotRegisteredWhenTurnIsCancelled guards the turn-cancel
// rule: a write whose context is already cancelled when the client wait
// fails must not park a re-sync — the tool result is being discarded and no
// later request carries its turn anchor, so the parked entry could only
// deliver diagnostics for a write nobody is waiting on.
func TestPendingSyncNotRegisteredWhenTurnIsCancelled(t *testing.T) {
	mgr, path := newWarmupTestManager(t)

	afterWriteStart = func(*Manager, context.Context, string) {}
	afterWriteWaitForClient = func(*Manager, context.Context, string, time.Duration) (*Client, bool) {
		return nil, false
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	out := mgr.AfterFileWriteToolResult(ctx, path, "package main\n", "wrote", false, WatchedFileChanged, "")
	if out != "wrote" {
		t.Fatalf("cancelled output = %q, want the untouched base result", out)
	}
	if got := mgr.pendingSyncCount(); got != 0 {
		t.Fatalf("pending syncs = %d, want none for a cancelled turn", got)
	}
}

type publishOnSaveClient struct {
	fakePowernapClient
	publish func()
}

func (f *publishOnSaveClient) NotifyDidSaveTextDocument(ctx context.Context, uri string, text *string) error {
	if err := f.fakePowernapClient.NotifyDidSaveTextDocument(ctx, uri, text); err != nil {
		return err
	}
	f.publish()
	return nil
}

func seedWarmupClient(mgr *Manager, fake lspProcessClient) *Client {
	client := &Client{
		client: fake, cwd: mgr.projectRootPath(),
		cfg: mgr.cfg.LSP["gopls"], openFiles: make(map[string]int32),
		diagnostics: make(map[protocol.DocumentURI][]protocol.Diagnostic),
	}
	mgr.clientsMu.Lock()
	mgr.clients[testKey(mgr, "gopls")] = client
	mgr.clientsMu.Unlock()
	return client
}

func publishWarmupDiagnostic(mgr *Manager, client *Client, path string) {
	uri := protocol.DocumentURI(client.pathToURI(path))
	diags := []protocol.Diagnostic{{Severity: protocol.SeverityError, Message: "undefined: sample"}}
	client.diagnosticsMu.Lock()
	client.diagnostics[uri] = diags
	client.diagnosticsMu.Unlock()
	mgr.onDiagnostics(testKey(mgr, "gopls"), nil)(string(uri), uriToPath(string(uri)), nil, "", diags, client.syncedDocumentVersion(path))
}

func TestPendingSyncClaimsPublishDuringSync(t *testing.T) {
	mgr, path := newWarmupTestManager(t)
	mgr.registerPendingSync(path, "package main\n", WatchedFileChanged, "")
	fake := &publishOnSaveClient{}
	fake.saveOptions = &protocol.SaveOptions{}
	client := seedWarmupClient(mgr, fake)
	fake.publish = func() { publishWarmupDiagnostic(mgr, client, path) }
	mgr.resyncPendingWrites(context.Background())
	if got := mgr.PendingLateDiagnostics([]string{path}, "", ""); len(got) != 1 {
		t.Fatalf("pending diagnostics = %+v, want the publish received during sync", got)
	}
}

func TestPendingSyncBatchClaimsEveryFile(t *testing.T) {
	mgr, alpha := newWarmupTestManager(t)
	beta := filepath.Join(filepath.Dir(alpha), "other.go")
	if err := os.WriteFile(beta, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{alpha, beta} {
		mgr.registerPendingSync(path, "package main\n", WatchedFileChanged, "")
	}
	client := seedWarmupClient(mgr, &fakePowernapClient{})
	mgr.resyncPendingWrites(context.Background())
	for _, path := range []string{alpha, beta} {
		publishWarmupDiagnostic(mgr, client, path)
	}
	if got := mgr.PendingLateDiagnostics([]string{alpha, beta}, "", ""); len(got) != 2 {
		t.Fatalf("pending diagnostics = %+v, want both files", got)
	}
}

func TestPendingSyncAdmissionBeforeRegistration(t *testing.T) {
	mgr, path := newWarmupTestManager(t)
	afterWriteStart = func(*Manager, context.Context, string) {}
	synced := make(chan struct{}, 1)
	afterWriteWaitForClient = func(*Manager, context.Context, string, time.Duration) (*Client, bool) {
		fake := &publishOnSaveClient{publish: func() { synced <- struct{}{} }}
		fake.saveOptions = &protocol.SaveOptions{}
		seedWarmupClient(mgr, fake)
		mgr.resyncPendingWrites(context.Background())
		return nil, false
	}
	mgr.AfterFileWriteToolResult(context.Background(), path, "package main\n", "wrote", false, WatchedFileChanged, "")
	select {
	case <-synced:
	case <-time.After(2 * time.Second):
		t.Fatal("late registration did not synchronize with the already admitted client")
	}
	// Join the background drain before the test restores global hooks.
	mgr.pendingResyncMu.Lock()
	defer mgr.pendingResyncMu.Unlock()
	if got := mgr.pendingSyncCount(); got != 0 {
		t.Fatalf("pending syncs = %d, want none after the ready-client drain", got)
	}
}

func TestPendingSyncDoesNotRegisterAfterLaunchSettles(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed", true: "stopped"}[stopped], func(t *testing.T) {
			mgr, path := newWarmupTestManager(t)
			if stopped {
				mgr.launches[testKey(mgr, "gopls")].cancel()
				mgr.Stop(context.Background())
			} else {
				mgr.clientsMu.Lock()
				delete(mgr.starting, testKey(mgr, "gopls"))
				delete(mgr.launches, testKey(mgr, "gopls"))
				mgr.clientsMu.Unlock()
			}
			mgr.registerPendingSync(path, "package main\n", WatchedFileChanged, "")
			if got := mgr.pendingSyncCount(); got != 0 {
				t.Fatalf("pending syncs = %d, want no entry after launch settlement", got)
			}
		})
	}
}
