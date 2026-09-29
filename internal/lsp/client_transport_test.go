package lsp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/keakon/x/powernap/pkg/lsp/protocol"
	powertransport "github.com/keakon/x/powernap/pkg/transport"
	"github.com/sourcegraph/jsonrpc2"

	"github.com/keakon/chord/internal/config"
)

type failingNotificationClient struct {
	*fakePowernapClient
	notify func(context.Context) error
}

func (f *failingNotificationClient) NotifyDidOpenTextDocument(ctx context.Context, _ string, _ string, _ int, _ string) error {
	return f.notify(ctx)
}
func (f *failingNotificationClient) NotifyDidChangeTextDocument(ctx context.Context, _ string, _ int, _ []protocol.TextDocumentContentChangeEvent) error {
	return f.notify(ctx)
}
func (f *failingNotificationClient) NotifyDidSaveTextDocument(ctx context.Context, _ string, _ *string) error {
	return f.notify(ctx)
}
func (f *failingNotificationClient) NotifyDidCloseTextDocument(ctx context.Context, _ string) error {
	return f.notify(ctx)
}
func (f *failingNotificationClient) NotifyDidChangeWatchedFiles(ctx context.Context, _ []protocol.FileEvent) error {
	return f.notify(ctx)
}

func TestClientDetectsPeerDisconnectDuringNotification(t *testing.T) {
	local, peer := net.Pipe()
	conn, err := powertransport.NewConnection(context.Background(), local, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := peer.Close(); err != nil {
		t.Fatal(err)
	}
	fake := &fakePowernapClient{}
	c := newResyncTestClient(fake)
	c.client = &failingNotificationClient{fakePowernapClient: fake, notify: func(ctx context.Context) error { return conn.Notify(ctx, "textDocument/didOpen", nil) }}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := c.DidOpen(ctx, "/tmp/main.go", "package main"); err == nil {
		t.Fatal("notification unexpectedly succeeded after peer disconnect")
	}
	if c.IsRunning() {
		t.Fatal("disconnected client still reported running")
	}
	if err := c.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if fake.kills != 1 || fake.shutdowns != 0 {
		t.Fatalf("dead client cleanup: kills=%d shutdowns=%d", fake.kills, fake.shutdowns)
	}
}

func TestClientNotificationErrorsDistinguishTransportFailure(t *testing.T) {
	operations := map[string]func(*Client) error{
		"open":   func(c *Client) error { _, e := c.DidOpen(context.Background(), "/tmp/main.go", "new"); return e },
		"change": func(c *Client) error { _, e := c.DidChange(context.Background(), "/tmp/main.go", "new"); return e },
		"save":   func(c *Client) error { return c.NotifyDidSave(context.Background(), "/tmp/main.go", "new") },
		"close":  func(c *Client) error { return c.DidClose(context.Background(), "/tmp/main.go") },
		"watch": func(c *Client) error {
			return c.NotifyWatchedFileChange(context.Background(), "/tmp/main.go", protocol.FileChangeType(2))
		},
		"resync": func(c *Client) error {
			_, e := c.ResyncFileIfChanged(context.Background(), "/tmp/main.go", "new")
			return e
		},
	}
	for name, op := range operations {
		for _, tc := range []struct {
			name string
			err  error
			dead bool
		}{
			{"closed", fmt.Errorf("notify: %w", jsonrpc2.ErrClosed), true},
			{"pipe", io.ErrClosedPipe, true},
			{"epipe", &fs.PathError{Op: "write", Path: "|1", Err: brokenPipeErrno}, true},
			{"file_closed", &fs.PathError{Op: "write", Path: "|1", Err: fs.ErrClosed}, true},
			{"canceled", context.Canceled, false},
			{"deadline", context.DeadlineExceeded, false},
			{"protocol", errors.New("request rejected"), false},
		} {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				fake := &fakePowernapClient{saveOptions: &protocol.SaveOptions{}}
				c := newResyncTestClient(fake)
				c.openFiles["/tmp/main.go"] = 1
				c.client = &failingNotificationClient{fakePowernapClient: fake, notify: func(context.Context) error { return tc.err }}
				if err := op(c); !errors.Is(err, tc.err) {
					t.Fatalf("error = %v, want %v", err, tc.err)
				}
				if c.IsRunning() == tc.dead {
					t.Fatalf("IsRunning=%v, dead=%v", c.IsRunning(), tc.dead)
				}
			})
		}
	}
}

func TestAfterWriteDisconnectSkipsWaitAndRecoversNextOperation(t *testing.T) {
	mgr, path, c := newAfterWriteTestManager(t)
	fake := c.client.(*fakePowernapClient)
	c.client = &failingNotificationClient{fakePowernapClient: fake, notify: func(context.Context) error { return jsonrpc2.ErrClosed }}
	c.diagnostics[protocol.DocumentURI(c.pathToURI(path))] = []protocol.Diagnostic{{Message: "stale diagnostic", Severity: 1}}
	oldStart, oldWait := afterWriteStart, afterWriteAwaitWaiter
	t.Cleanup(func() { afterWriteStart = oldStart; afterWriteAwaitWaiter = oldWait })
	starts, waits := 0, 0
	afterWriteStart = func(m *Manager, _ context.Context, _ string) {
		starts++
		if starts == 2 {
			next := newResyncTestClient(&fakePowernapClient{})
			next.cwd = c.cwd
			next.cfg = c.cfg
			// The failed first write already spawned an async sidebar refresh
			// that reads clients under clientsMu.
			m.clientsMu.Lock()
			m.clients[testKey(m, "gopls")] = next
			m.clientsMu.Unlock()
		}
	}
	afterWriteAwaitWaiter = func(m *Manager, _ context.Context, p string, ch chan diagnosticsEvent, _ diagnosticsWaitRequest, _ time.Duration) ([]Diagnostic, bool) {
		waits++
		m.waitersMu.Lock()
		m.removeWaiter(p, ch)
		m.waitersMu.Unlock()
		return nil, true
	}
	out := mgr.AfterFileWriteToolResult(context.Background(), path, "package main", "written", false, WatchedFileChanged, "")
	if waits != 0 || len(mgr.clients) != 0 || fake.kills != 1 {
		t.Fatalf("waits=%d clients=%d kills=%d", waits, len(mgr.clients), fake.kills)
	}
	if strings.Contains(out, "stale diagnostic") || !strings.Contains(out, "connection lost") {
		t.Fatalf("output = %s", out)
	}
	if len(mgr.waiters[path]) != 0 {
		t.Fatal("failed write left a diagnostics waiter")
	}
	mgr.AfterFileWriteToolResult(context.Background(), path, "package main", "written", false, WatchedFileChanged, "")
	if starts != 2 || waits != 1 || len(mgr.clients) != 1 {
		t.Fatalf("recovery: starts=%d waits=%d clients=%d", starts, waits, len(mgr.clients))
	}
}

// The errors a write to a language server's stdin pipe returns once the server
// is gone must count as a transport failure, not just jsonrpc2's closed error.
func TestIsTransportFailureRecognizesStdioPipeErrors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows reports a broken pipe with its own error codes")
	}
	t.Run("reader_gone", func(t *testing.T) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = w.Close() })
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
		_, err = w.Write([]byte("{}"))
		if err == nil || !isTransportFailure(err) {
			t.Fatalf("write to a pipe without reader: err = %v, want transport failure", err)
		}
	})
	t.Run("pipe_closed", func(t *testing.T) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = r.Close() })
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		_, err = w.Write([]byte("{}"))
		if err == nil || !isTransportFailure(err) {
			t.Fatalf("write to a closed pipe: err = %v, want transport failure", err)
		}
	})
}

// newAfterWriteServerPair builds a manager with two configured servers,
// "gopls" and "samplelsp", that both own one .go file under the project root.
// Each server gets a running fake client; a non-nil notify makes every
// notification that server sends fail with it. The returned maps are keyed by
// server name.
func newAfterWriteServerPair(t *testing.T, goplsNotify, sampleNotify func(context.Context) error) (*Manager, string, map[string]*Client, map[string]*fakePowernapClient) {
	t.Helper()
	root := t.TempDir()
	mgr := NewManager(&config.Config{
		LSP: config.LSPConfig{
			"gopls":     {Command: "gopls", FileTypes: []string{".go"}},
			"samplelsp": {Command: "samplelsp", FileTypes: []string{".go"}},
		},
	}, root, nil)
	path := filepath.Join(root, "main.go")

	clients := make(map[string]*Client, 2)
	fakes := make(map[string]*fakePowernapClient, 2)
	for name, notify := range map[string]func(context.Context) error{"gopls": goplsNotify, "samplelsp": sampleNotify} {
		fake := &fakePowernapClient{}
		c := newResyncTestClient(fake)
		c.name = name
		c.cwd = root
		c.cfg = config.LSPServerConfig{FileTypes: []string{".go"}}
		c.diagnostics = make(map[protocol.DocumentURI][]protocol.Diagnostic)
		if notify != nil {
			c.client = &failingNotificationClient{fakePowernapClient: fake, notify: notify}
		}
		mgr.clients[testKey(mgr, name)] = c
		clients[name] = c
		fakes[name] = fake
	}
	return mgr, path, clients, fakes
}

// A file owned by two servers must still be verified when one server's
// connection dies during the post-write sync: the surviving server's
// diagnostics are awaited and presented, while only the failed instance is
// reclaimed and flagged as unverified.
func TestAfterWriteSyncFailureWaitsForSurvivingServer(t *testing.T) {
	mgr, path, clients, fakes := newAfterWriteServerPair(t,
		func(context.Context) error { return jsonrpc2.ErrClosed },
		nil)
	clients["gopls"].diagnostics[protocol.DocumentURI(clients["gopls"].pathToURI(path))] = []protocol.Diagnostic{{Message: "stale diagnostic", Severity: 1}}
	clients["samplelsp"].diagnostics[protocol.DocumentURI(clients["samplelsp"].pathToURI(path))] = []protocol.Diagnostic{{
		Severity: protocol.SeverityError,
		Range: protocol.Range{
			Start: protocol.Position{Line: 0, Character: 0},
			End:   protocol.Position{Line: 0, Character: 1},
		},
		Message: "survivor diagnostic",
	}}

	oldStart, oldAwait := afterWriteStart, afterWriteAwaitWaiter
	t.Cleanup(func() { afterWriteStart = oldStart; afterWriteAwaitWaiter = oldAwait })
	starts, awaits := 0, 0
	var awaitedVersions map[string]int32
	afterWriteStart = func(*Manager, context.Context, string) { starts++ }
	afterWriteAwaitWaiter = func(m *Manager, _ context.Context, p string, ch chan diagnosticsEvent, req diagnosticsWaitRequest, _ time.Duration) ([]Diagnostic, bool) {
		awaits++
		awaitedVersions = req.serverVersions
		m.waitersMu.Lock()
		m.removeWaiter(p, ch)
		m.waitersMu.Unlock()
		return nil, true
	}

	out := mgr.AfterFileWriteToolResult(context.Background(), path, "package main", "written", false, WatchedFileChanged, "")
	if starts != 1 || awaits != 1 {
		t.Fatalf("starts=%d awaits=%d, want the surviving server's diagnostics awaited once", starts, awaits)
	}
	if _, ok := awaitedVersions["samplelsp"]; !ok {
		t.Fatalf("await versions = %v, want the survivor's document version", awaitedVersions)
	}
	if _, ok := awaitedVersions["gopls"]; ok {
		t.Fatalf("await versions = %v, want no version for the failed server", awaitedVersions)
	}
	mgr.clientsMu.RLock()
	_, deadRegistered := mgr.clients[testKey(mgr, "gopls")]
	_, survivorRegistered := mgr.clients[testKey(mgr, "samplelsp")]
	mgr.clientsMu.RUnlock()
	if deadRegistered || !survivorRegistered {
		t.Fatalf("dead registered=%v, survivor registered=%v, want only the survivor left", deadRegistered, survivorRegistered)
	}
	if fakes["gopls"].kills != 1 {
		t.Fatalf("dead client kills = %d, want 1", fakes["gopls"].kills)
	}
	if !strings.Contains(out, "survivor diagnostic") {
		t.Fatalf("output = %q, want the survivor's diagnostics presented", out)
	}
	if strings.Contains(out, "stale diagnostic") {
		t.Fatalf("output = %q, want the failed server's stale diagnostics dropped", out)
	}
	if !strings.Contains(out, "gopls: connection lost") {
		t.Fatalf("output = %q, want the failed server named in a degradation note", out)
	}
	if len(mgr.waiters[path]) != 0 {
		t.Fatal("the awaited waiter must be removed")
	}
}

// When every owning server's sync fails with a transport failure nobody is
// left to publish diagnostics, so the wait is skipped entirely and both
// instances are reclaimed for the next write.
func TestAfterWriteSyncFailureWithAllServersDeadSkipsWait(t *testing.T) {
	mgr, path, clients, fakes := newAfterWriteServerPair(t,
		func(context.Context) error { return jsonrpc2.ErrClosed },
		func(context.Context) error { return jsonrpc2.ErrClosed })
	for name, c := range clients {
		c.diagnostics[protocol.DocumentURI(c.pathToURI(path))] = []protocol.Diagnostic{{Message: "stale " + name, Severity: 1}}
	}

	oldStart, oldAwait := afterWriteStart, afterWriteAwaitWaiter
	t.Cleanup(func() { afterWriteStart = oldStart; afterWriteAwaitWaiter = oldAwait })
	awaits := 0
	afterWriteStart = func(*Manager, context.Context, string) {}
	afterWriteAwaitWaiter = func(m *Manager, _ context.Context, p string, ch chan diagnosticsEvent, _ diagnosticsWaitRequest, _ time.Duration) ([]Diagnostic, bool) {
		awaits++
		m.waitersMu.Lock()
		m.removeWaiter(p, ch)
		m.waitersMu.Unlock()
		return nil, true
	}

	out := mgr.AfterFileWriteToolResult(context.Background(), path, "package main", "written", false, WatchedFileChanged, "")
	if awaits != 0 {
		t.Fatalf("awaits = %d, want the wait skipped when every server failed", awaits)
	}
	if len(mgr.clients) != 0 {
		t.Fatalf("clients = %d, want both dead instances reclaimed", len(mgr.clients))
	}
	for _, name := range []string{"gopls", "samplelsp"} {
		if fakes[name].kills != 1 {
			t.Fatalf("%s kills = %d, want 1", name, fakes[name].kills)
		}
		if !strings.Contains(out, name+": connection lost") {
			t.Fatalf("output = %q, want %s named in a degradation note", out, name)
		}
		if strings.Contains(out, "stale "+name) {
			t.Fatalf("output = %q, want %s's stale diagnostics dropped", out, name)
		}
	}
	if len(mgr.waiters[path]) != 0 {
		t.Fatal("the skipped wait must not leave a diagnostics waiter")
	}
}

// A sync failure must be reported per server: one server's note must not
// consume another server's only line, and each server's failure is reported
// once per session.
func TestAfterWriteSyncFailureNotesEachFailedServer(t *testing.T) {
	mgr, path, _, _ := newAfterWriteServerPair(t,
		func(context.Context) error { return errors.New("request rejected") },
		func(context.Context) error { return errors.New("request rejected") })

	oldStart, oldAwait := afterWriteStart, afterWriteAwaitWaiter
	t.Cleanup(func() { afterWriteStart = oldStart; afterWriteAwaitWaiter = oldAwait })
	awaits := 0
	afterWriteStart = func(*Manager, context.Context, string) {}
	afterWriteAwaitWaiter = func(m *Manager, _ context.Context, p string, ch chan diagnosticsEvent, _ diagnosticsWaitRequest, _ time.Duration) ([]Diagnostic, bool) {
		awaits++
		m.waitersMu.Lock()
		m.removeWaiter(p, ch)
		m.waitersMu.Unlock()
		return nil, true
	}

	out := mgr.AfterFileWriteToolResult(context.Background(), path, "package main", "written", false, WatchedFileChanged, "")
	for _, name := range []string{"gopls", "samplelsp"} {
		want := "LSP diagnostics unavailable for this edit (" + name + ": file synchronization failed); do not treat this edit as verified."
		if !strings.Contains(out, want) {
			t.Fatalf("output = %q, want %s's own degradation note", out, name)
		}
	}
	if got := DegradationNotes(out); len(got) != 2 {
		t.Fatalf("DegradationNotes = %q, want one note per failed server", got)
	}
	if awaits != 0 {
		t.Fatalf("awaits = %d, want the wait skipped when every server failed", awaits)
	}

	repeat := mgr.AfterFileWriteToolResult(context.Background(), path, "package main", "written", false, WatchedFileChanged, "")
	if strings.Contains(repeat, "LSP diagnostics unavailable") {
		t.Fatalf("repeat output = %q, want each server's sync failure reported once per session", repeat)
	}
	if repeat != "written" {
		t.Fatalf("repeat output = %q, want the untouched base result", repeat)
	}
}

// A transport failure is terminal, so WaitForServerReady must return the
// timeout outcome immediately instead of polling out the clock: the caller
// continues anyway either way, and only the latency differs.
func TestWaitForServerReadyReturnsTimeoutOutcomeAfterTransportFailure(t *testing.T) {
	dead := newResyncTestClient(&fakePowernapClient{})
	dead.observeTransportError(jsonrpc2.ErrClosed)

	begin := time.Now()
	err := dead.WaitForServerReady(context.Background(), 5*time.Second)
	if err == nil || err.Error() != "timeout waiting for LSP server gopls" {
		t.Fatalf("WaitForServerReady after transport failure = %v, want the timeout outcome", err)
	}
	if elapsed := time.Since(begin); elapsed > time.Second {
		t.Fatalf("WaitForServerReady took %v after a terminal transport failure, want an immediate return", elapsed)
	}

	if err := newResyncTestClient(&fakePowernapClient{}).WaitForServerReady(context.Background(), time.Second); err != nil {
		t.Fatalf("WaitForServerReady on a running server = %v, want nil", err)
	}
}

// The Python semantic path must handle a failed synchronization like the
// generic path: no wait, the disconnected instance reclaimed, no stale
// diagnostics, and the next write restarting the server.
func TestPythonSemanticDisconnectSkipsWaitAndRecoversNextOperation(t *testing.T) {
	root := t.TempDir()
	srvCfg := config.LSPServerConfig{Command: "pyright-langserver", FileTypes: []string{".py"}}
	mgr := NewManager(&config.Config{LSP: config.LSPConfig{"pyright": srvCfg}}, root, nil)
	path := filepath.Join(root, "main.py")
	fake := &fakePowernapClient{}
	c := newResyncTestClient(fake)
	c.name = "pyright"
	c.cwd = root
	c.cfg = srvCfg
	c.diagnostics = make(map[protocol.DocumentURI][]protocol.Diagnostic)
	c.client = &failingNotificationClient{fakePowernapClient: fake, notify: func(context.Context) error {
		return &fs.PathError{Op: "write", Path: "|1", Err: brokenPipeErrno}
	}}
	c.diagnostics[protocol.DocumentURI(c.pathToURI(path))] = []protocol.Diagnostic{{Message: "stale diagnostic", Severity: 1}}
	mgr.clients[testKey(mgr, "pyright")] = c

	oldStart, oldWait := afterWriteStart, afterWriteAwaitWaiter
	t.Cleanup(func() { afterWriteStart = oldStart; afterWriteAwaitWaiter = oldWait })
	starts, waits := 0, 0
	afterWriteStart = func(m *Manager, _ context.Context, _ string) {
		starts++
		if starts == 2 {
			next := newResyncTestClient(&fakePowernapClient{})
			next.name = "pyright"
			next.cwd = root
			next.cfg = srvCfg
			m.clientsMu.Lock()
			m.clients[testKey(m, "pyright")] = next
			m.clientsMu.Unlock()
		}
	}
	afterWriteAwaitWaiter = func(m *Manager, _ context.Context, p string, ch chan diagnosticsEvent, _ diagnosticsWaitRequest, _ time.Duration) ([]Diagnostic, bool) {
		waits++
		m.waitersMu.Lock()
		m.removeWaiter(p, ch)
		m.waitersMu.Unlock()
		return nil, true
	}

	out := mgr.afterWriteLSPToolResult(context.Background(), path, "x = 1\n", "written", false, nil, WatchedFileChanged, "")
	if waits != 0 || len(mgr.clients) != 0 || fake.kills != 1 {
		t.Fatalf("waits=%d clients=%d kills=%d", waits, len(mgr.clients), fake.kills)
	}
	if strings.Contains(out, "stale diagnostic") || !strings.Contains(out, "connection lost") {
		t.Fatalf("output = %s", out)
	}
	if len(mgr.waiters[path]) != 0 {
		t.Fatal("failed write left a diagnostics waiter")
	}
	mgr.afterWriteLSPToolResult(context.Background(), path, "x = 1\n", "written", false, nil, WatchedFileChanged, "")
	if starts != 2 || waits != 1 || len(mgr.clients) != 1 {
		t.Fatalf("recovery: starts=%d waits=%d clients=%d", starts, waits, len(mgr.clients))
	}
}
