package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// miniMCPServerScript answers the initialize and tools/list requests of a
// stdio MCP server and keeps reading afterwards, so a test can tell whether
// the process Chord spawned is still running.
const miniMCPServerScript = `#!/bin/sh
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  case "$line" in
  *'"method":"initialize"'*)
    printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"sample","version":"1"}}}\n' "$id"
    ;;
  *'"method":"tools/list"'*)
    printf '{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"sample_ping","description":"ping","inputSchema":{"type":"object"}}]}}\n' "$id"
    ;;
  esac
done
`

func writeMiniMCPServer(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stdio fixture is a POSIX shell script")
	}
	path := filepath.Join(t.TempDir(), "sample-mcp-server.sh")
	if err := os.WriteFile(path, []byte(miniMCPServerScript), 0o755); err != nil {
		t.Fatalf("write stdio server fixture: %v", err)
	}
	return path
}

// shortenConnectAttemptTimeout shrinks the production connect attempt window so
// tests can watch what an attempt does when the client factory does not return
// in time, instead of waiting the real 20s out. The window is package state, so
// a test that connects must not run in parallel while another test has it
// shortened.
func shortenConnectAttemptTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	orig := connectAttemptTimeout
	connectAttemptTimeout = d
	t.Cleanup(func() { connectAttemptTimeout = orig })
}

// closeReportingTransport is a Transport whose Close is observable, so a test
// can wait for a client that the connect attempt already gave up on to be
// cleaned up.
type closeReportingTransport struct {
	closed chan struct{}
}

func (t *closeReportingTransport) Send(context.Context, JSONRPCRequest) (JSONRPCResponse, error) {
	return JSONRPCResponse{}, errors.New("closeReportingTransport: no server")
}

func (t *closeReportingTransport) Notify(context.Context, JSONRPCNotification) error {
	return errors.New("closeReportingTransport: no server")
}

func (t *closeReportingTransport) Close() error {
	close(t.closed)
	return nil
}

// hangingClientFactory installs a factory that blocks until release is closed
// and then hands back a client with a transport that reports its own Close.
func hangingClientFactory(mgr *Manager) (*closeReportingTransport, chan struct{}) {
	transport := &closeReportingTransport{closed: make(chan struct{})}
	release := make(chan struct{})
	mgr.newClientFactory = func(_ context.Context, cfg ServerConfig) (*Client, error) {
		<-release
		return NewClientWithInfo(cfg.Name, transport, testClientInfo), nil
	}
	return transport, release
}

// TestConnectOneBoundsHangingClientFactory pins that the connect attempt bounds
// client creation too. The factory runs with a context detached from the
// attempt (the process it starts has to outlive the handshake), so the attempt
// has to bound the wait itself: a factory that hangs must not hang the connect,
// and a client that arrives after the attempt gave up must be closed instead of
// leaving a server process behind.
func TestConnectOneBoundsHangingClientFactory(t *testing.T) {
	cfg := ServerConfig{Name: "sample", Command: "sample-mcp"}

	t.Run("attempt timeout fails the endpoint", func(t *testing.T) {
		shortenConnectAttemptTimeout(t, 50*time.Millisecond)
		mgr := NewPendingManagerWithClientInfo(nil, testClientInfo)
		t.Cleanup(mgr.Close)
		transport, release := hangingClientFactory(mgr)

		start := time.Now()
		err := mgr.ConnectOne(context.Background(), cfg)
		if err == nil {
			t.Fatal("ConnectOne returned no error for a client factory that never finished")
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("ConnectOne waited %v for a hanging factory, want roughly the connect attempt timeout", elapsed)
		}
		if !strings.Contains(err.Error(), "did not finish within") {
			t.Fatalf("ConnectOne error = %q, want the connect attempt to report the timeout", err)
		}
		status := onlyEndpointStatus(t, mgr)
		if status.OK || status.Pending || status.Error == "" {
			t.Fatalf("endpoint after a timed-out start = %+v, want a failed attempt carrying an error", status)
		}

		// The factory is still blocked: whatever it builds belongs to no
		// attempt any more and must be closed.
		close(release)
		select {
		case <-transport.closed:
		case <-time.After(5 * time.Second):
			t.Fatal("the abandoned client was not closed; its server process would keep running")
		}
	})

	t.Run("caller cancellation stays a pending connect", func(t *testing.T) {
		mgr := NewPendingManagerWithClientInfo(nil, testClientInfo)
		t.Cleanup(mgr.Close)
		transport, release := hangingClientFactory(mgr)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := mgr.ConnectOne(ctx, cfg)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ConnectOne error = %v, want context.Canceled", err)
		}
		status := onlyEndpointStatus(t, mgr)
		if !status.Pending || status.OK || status.Error != "" {
			t.Fatalf("endpoint after a cancelled connect = %+v, want a pending connect with no error", status)
		}

		close(release)
		select {
		case <-transport.closed:
		case <-time.After(5 * time.Second):
			t.Fatal("the abandoned client was not closed; its server process would keep running")
		}
	})
}

// TestConnectOneKeepsStdioServerAlive pins a stdio server's process lifetime to
// the client, not to the connect attempt: the attempt context bounds the
// handshake only. Spawning the process with the attempt context let exec kill a
// server that is its own command the moment initialization succeeded, so the
// next request (tools/list) failed with a broken pipe.
func TestConnectOneKeepsStdioServerAlive(t *testing.T) {
	script := writeMiniMCPServer(t)
	m := NewPendingManagerWithClientInfo(nil, testClientInfo)
	t.Cleanup(m.Close)

	// The factory is where the connect attempt context reaches the transport,
	// and the attempt context is cancelled before ConnectOne returns. The
	// context handed over must already be detached from that cancellation; this
	// half of the regression needs no timing.
	create := m.newClientFactory
	var handoffCtx context.Context
	m.newClientFactory = func(ctx context.Context, cfg ServerConfig) (*Client, error) {
		handoffCtx = ctx
		return create(ctx, cfg)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := m.ConnectOne(ctx, ServerConfig{Name: "sample", Command: script}); err != nil {
		t.Fatalf("ConnectOne: %v", err)
	}
	if handoffCtx == nil {
		t.Fatal("ConnectOne never built a client")
	}
	if err := handoffCtx.Err(); err != nil {
		t.Fatalf("the stdio server was handed a cancelled connect context: %v", err)
	}

	// A cancelled attempt context kills the process from its own goroutine.
	// Settle first: without it the request below can win the race and the
	// regression slips through.
	time.Sleep(200 * time.Millisecond)

	client := m.Client("sample")
	if client == nil {
		t.Fatal("ConnectOne registered no client")
	}
	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("tools/list after connect: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "sample_ping" {
		t.Fatalf("tools = %+v, want the fixture's sample_ping", tools)
	}
}
