package lsp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/keakon/x/powernap/pkg/lsp/protocol"
	powertransport "github.com/keakon/x/powernap/pkg/transport"
	"github.com/sourcegraph/jsonrpc2"
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
			{"epipe", &fs.PathError{Op: "write", Path: "|1", Err: syscall.EPIPE}, true},
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
