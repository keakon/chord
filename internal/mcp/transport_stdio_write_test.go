package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestStdioQueuedCallsCancelWithoutWriting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		writer := &scriptedWriteCloser{onWrite: func([]byte) {
			close(started)
			select {
			case <-release:
			case <-time.After(5 * time.Second):
				t.Error("first write was not released")
			}
		}}
		tr := newScriptedTransport(writer)
		first := make(chan error, 1)
		go func() {
			first <- tr.Notify(t.Context(), JSONRPCNotification{JSONRPC: "2.0", Method: "notifications/initialized"})
		}()
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("first write did not start")
		}

		for _, notify := range []bool{false, true} {
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() {
				if notify {
					done <- tr.Notify(ctx, JSONRPCNotification{JSONRPC: "2.0", Method: "notifications/initialized"})
				} else {
					_, err := tr.Send(ctx, JSONRPCRequest{JSONRPC: "2.0", ID: 2, Method: "tools/call"})
					done <- err
				}
			}()
			synctest.Wait()
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("queued call error = %v, want cancellation", err)
				}
				if !notify {
					requireCallOutcome(t, err, callNotSent)
				}
			case <-time.After(time.Second):
				t.Fatal("queued call did not return before the first write completed")
			}
		}
		tr.mu.Lock()
		pending := len(tr.pending)
		tr.mu.Unlock()
		if pending != 0 {
			t.Fatalf("cancelled call leaked %d pending waiters", pending)
		}
		close(release)
		select {
		case err := <-first:
			if err != nil {
				t.Fatalf("unrelated write failed: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("first write did not finish")
		}
		if writer.calls != 1 {
			t.Fatalf("cancelled calls wrote frames: %d writes, want 1", writer.calls)
		}
		writer.onWrite = func([]byte) {
			tr.mu.Lock()
			reply := tr.pending[3]
			delete(tr.pending, 3)
			tr.mu.Unlock()
			reply <- stdioReply{resp: JSONRPCResponse{JSONRPC: "2.0", ID: 3, Result: json.RawMessage(`{}`)}}
		}
		if _, err := tr.Send(t.Context(), JSONRPCRequest{JSONRPC: "2.0", ID: 3, Method: "tools/call"}); err != nil {
			t.Fatalf("call after cancellation failed: %v", err)
		}
	})
}

func TestStdioQueuedSendStopsWhenReaderExits(t *testing.T) {
	tr := newScriptedTransport(&scriptedWriteCloser{})
	tr.writeGate <- struct{}{}
	done := make(chan error, 1)
	go func() {
		_, err := tr.Send(t.Context(), JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "tools/call"})
		done <- err
	}()
	close(tr.done)
	select {
	case err := <-done:
		requireCallOutcome(t, err, callNotSent)
	case <-time.After(time.Second):
		t.Fatal("queued Send did not return after reader exit")
	}
	<-tr.writeGate
	if len(tr.pending) != 0 {
		t.Fatalf("pending waiter leaked: %d", len(tr.pending))
	}
}

func TestStdioNotifyCancellationAndWriteFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result writeOutcome
		cancel bool
		want   error
	}{
		{name: "cancelled", cancel: true, want: context.Canceled},
		{name: "short write", result: writeOutcome{written: 3}, want: io.ErrShortWrite},
		{name: "write failure", result: writeOutcome{err: io.ErrClosedPipe}, want: io.ErrClosedPipe},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writer := &scriptedWriteCloser{results: []writeOutcome{tc.result}}
			tr := newScriptedTransport(writer)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			err := tr.Notify(ctx, JSONRPCNotification{JSONRPC: "2.0", Method: "notifications/initialized"})
			if !errors.Is(err, tc.want) {
				t.Fatalf("Notify error = %v, want %v", err, tc.want)
			}
			if tc.cancel && writer.calls != 0 {
				t.Fatal("cancelled notification was written")
			}
			writer.results = nil
			err = tr.Notify(t.Context(), JSONRPCNotification{JSONRPC: "2.0", Method: "notifications/initialized"})
			if tc.cancel && err != nil {
				t.Fatalf("cancelled queue entry poisoned writer: %v", err)
			}
			if !tc.cancel && !errors.Is(err, tc.want) {
				t.Fatalf("failed writer accepted another frame: %v", err)
			}
		})
	}
}

func TestStdioBlockedWriteDeadlineAndFrameOwnership(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	tr := newScriptedTransport(writer)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := tr.Send(ctx, JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: strings.Repeat("x", 4<<20)})
		done <- err
	}()
	select {
	case err := <-done:
		requireCallOutcome(t, err, callOutcomeUnknown)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("deadline error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocked write ignored deadline")
	}
	ctx2, cancel2 := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel2()
	_, err = tr.Send(ctx2, JSONRPCRequest{JSONRPC: "2.0", ID: 2, Method: "tools/call"})
	requireCallOutcome(t, err, callNotSent)
	tr.mu.Lock()
	finished := tr.writerDone
	pending := len(tr.pending)
	tr.mu.Unlock()
	if pending != 0 || len(tr.writeGate) != 1 {
		t.Fatalf("pending=%d gate=%d after caller cancellation", pending, len(tr.writeGate))
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("pipe close did not release writer")
	}
	if len(tr.writeGate) != 0 {
		t.Fatal("completed writer retained gate")
	}
}

func TestStdioCloseReclaimsBlockedWriter(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires a POSIX shell")
	}
	tr, err := NewStdioTransport(t.Context(), "/bin/sh", []string{"-c", "sleep 30"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- tr.Notify(ctx, JSONRPCNotification{JSONRPC: "2.0", Method: "sample", Params: strings.Repeat("x", 4<<20)})
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Notify error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("notification ignored deadline")
	}
	tr.mu.Lock()
	finished := tr.writerDone
	tr.mu.Unlock()
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("Close left a blocked writer")
	}
}
