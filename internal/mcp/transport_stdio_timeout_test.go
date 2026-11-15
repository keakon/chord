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

// TestStdioTransportTimeoutCleansPendingAndKeepsConnectionUsable exercises the
// configured-deadline path on the real stdio transport: a hung first request
// times out, its pending waiter is removed, the late reply is dropped without
// harming the reader, and a subsequent call on the same connection still gets
// routed and answered while the first operation is still running.
func TestStdioTransportTimeoutCleansPendingAndKeepsConnectionUsable(t *testing.T) {
	// Deadlock guard: this test launches a child shell, so a transport
	// regression can hang the whole package. Dump goroutines and panic well
	// before the package-level timeout so the failure is actionable.
	timer := time.AfterFunc(15*time.Second, func() {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		panic("TestStdioTransportTimeoutCleansPendingAndKeepsConnectionUsable deadlocked:\n" + string(buf[:n]))
	})
	defer timer.Stop()

	// The first request is answered only after a 1s delay (longer than the
	// caller deadline below); every later request is answered immediately.
	script := filepath.Join(t.TempDir(), "late-reply.sh")
	content := `#!/bin/sh
first=1
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9][0-9]*\).*/\1/p')
  if [ "$first" = "1" ]; then
    first=0
    sleep 1
    printf '{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"late"}]}}\n' "$id"
  else
    printf '{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"fast"}]}}\n' "$id"
  fi
done
`
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	tr, err := NewStdioTransport(context.Background(), script, nil, nil)
	if err != nil {
		t.Fatalf("NewStdioTransport: %v", err)
	}
	defer tr.Close()

	// The first call expires via the caller deadline — what Client.CallTool
	// applies from a configured timeout_ms.
	ctx1, cancel1 := context.WithTimeout(context.Background(), 150*time.Millisecond)
	_, err = tr.Send(ctx1, JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "tools/call"})
	cancel1()
	if err == nil {
		t.Fatal("expected deadline error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
	// The deadline stops the wait, not the server's work: the outcome must
	// stay unknown, never claim the request was not sent.
	if outcome, ok := callOutcomeOf(err); !ok || outcome != callOutcomeUnknown {
		t.Fatalf("outcome = %d (found %v), want callOutcomeUnknown", outcome, ok)
	}

	// The waiter must be gone: a late reply finds no dispatcher and is dropped.
	tr.mu.Lock()
	pending := len(tr.pending)
	tr.mu.Unlock()
	if pending != 0 {
		t.Fatalf("pending waiters after timeout = %d, want 0", pending)
	}

	// Overlap regression: the server is still working on the first request
	// (its reply lands ~1s after start), yet a second call on the same
	// connection must be routed and answered. Success also proves the reader
	// consumed and dropped the late id=1 reply without panicking.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	resp, err := tr.Send(ctx2, JSONRPCRequest{JSONRPC: "2.0", ID: 2, Method: "tools/call"})
	cancel2()
	if err != nil {
		t.Fatalf("second call after timeout: %v", err)
	}
	if resp.ID != 2 || !strings.Contains(string(resp.Result), "fast") {
		t.Fatalf("second call response = %+v, want id 2 with a fast result", resp)
	}

	tr.mu.Lock()
	pending = len(tr.pending)
	tr.mu.Unlock()
	if pending != 0 {
		t.Fatalf("pending waiters after second call = %d, want 0", pending)
	}
}
