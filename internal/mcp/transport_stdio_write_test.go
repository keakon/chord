package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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
			if err := tr.Notify(t.Context(), JSONRPCNotification{JSONRPC: "2.0", Method: "notifications/initialized"}); err != nil {
				t.Fatalf("writer gate leaked: %v", err)
			}
		})
	}
}
