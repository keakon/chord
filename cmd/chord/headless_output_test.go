package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
)

type headlessOutputFunc func([]byte) (int, error)

func (f headlessOutputFunc) Write(data []byte) (int, error) { return f(data) }

func TestRunHeadlessReturnsOutputFailure(t *testing.T) {
	for _, duringClose := range []bool{false, true} {
		name := "active"
		if duringClose {
			name = "shutdown drain"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			ac := &AppContext{Ctx: ctx, Cancel: cancel, SessionDir: filepath.Join(t.TempDir(), "session-output")}
			events := make(chan agent.AgentEvent)
			close(events)
			closing := make(chan struct{})
			rt := &fakeHeadlessRuntime{events: events, backend: &mockBackend{}, onClose: func() { close(closing) }}
			var stdin io.Reader = strings.NewReader("")
			if !duringClose {
				reader, writer := io.Pipe()
				t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
				stdin = reader
			}
			stdout := headlessOutputFunc(func([]byte) (int, error) {
				if duringClose {
					select {
					case <-closing:
					case <-time.After(3 * time.Second):
						return 0, io.ErrNoProgress
					}
				}
				return 0, io.ErrClosedPipe
			})
			err := runHeadlessWithDeps(headlessRunDeps{
				initApp:       func(bool, string, sessionStartupOptions) (*AppContext, error) { return ac, nil },
				createRuntime: func(*AppContext) (headlessRuntime, error) { return rt, nil },
				stdin:         stdin,
				stdout:        stdout,
			})
			if !errors.Is(err, io.ErrClosedPipe) {
				t.Fatalf("runHeadlessWithDeps error = %v, want closed pipe", err)
			}
			if !rt.closed || ctx.Err() == nil {
				t.Fatal("output failure did not stop the runtime and application")
			}
		})
	}
}

func TestRunHeadlessReturnsEnvelopeLimitFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	ac := &AppContext{Ctx: ctx, Cancel: cancel, SessionDir: filepath.Join(t.TempDir(), "session-limit")}
	events := make(chan agent.AgentEvent, 1)
	events <- agent.ContextNoticeEvent{Level: "pressure", Message: strings.Repeat("x", headlessMaxEnvelopeBytes), MessageIndex: 1}
	close(events)
	rt := &fakeHeadlessRuntime{events: events, backend: &mockBackend{}}
	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	err := runHeadlessWithDeps(headlessRunDeps{
		initApp:       func(bool, string, sessionStartupOptions) (*AppContext, error) { return ac, nil },
		createRuntime: func(*AppContext) (headlessRuntime, error) { return rt, nil },
		stdin:         reader,
		stdout:        io.Discard,
	})
	if err == nil || !strings.Contains(err.Error(), "encoded envelope exceeds") {
		t.Fatalf("runHeadlessWithDeps error = %v, want envelope limit failure", err)
	}
	if !rt.closed || ctx.Err() == nil {
		t.Fatal("envelope limit failure did not stop the runtime and application")
	}
}

func TestStdoutWriterConcurrentClosePreservesAcceptedEnvelopes(t *testing.T) {
	var buf bytes.Buffer
	out := newStdoutWriter(context.Background(), &buf)
	go out.run()
	var accepted atomic.Int32
	var senders sync.WaitGroup
	if out.emit(headlessEnvelope{Type: "ready"}) {
		accepted.Add(1)
	}
	for range 32 {
		senders.Go(func() {
			if out.emit(headlessEnvelope{Type: "status"}) {
				accepted.Add(1)
			}
		})
	}
	var closers sync.WaitGroup
	for range 3 {
		closers.Go(func() {
			if !out.closeUntil(time.After(time.Second)) {
				t.Error("concurrent close timed out")
			}
		})
	}
	senders.Wait()
	closers.Wait()
	if got := len(decodeHeadlessJSONLines(t, buf.Bytes())); got != int(accepted.Load()) {
		t.Fatalf("wrote %d envelopes, accepted %d", got, accepted.Load())
	}
	if out.emit(headlessEnvelope{Type: "late"}) {
		t.Fatal("closed writer accepted a new envelope")
	}
}

func TestStdoutWriterApplicationCancellationReleasesFullCommandQueue(t *testing.T) {
	var buf bytes.Buffer
	out := newStdoutWriter(context.Background(), &buf)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out.commandCtx = ctx
	for range cap(out.ch) {
		if !out.emit(headlessEnvelope{Type: "status"}) {
			t.Fatal("could not fill output queue")
		}
	}
	result := make(chan bool, 1)
	go func() { result <- out.emit(headlessEnvelope{Type: "late"}) }()
	cancel()
	select {
	case accepted := <-result:
		if accepted {
			t.Fatal("cancelled command send entered a full queue")
		}
	case <-time.After(time.Second):
		t.Fatal("application cancellation did not unblock command send")
	}
	go out.run()
	if !out.closeUntil(time.After(time.Second)) {
		t.Fatal("accepted output did not drain")
	}
	if got := len(decodeHeadlessJSONLines(t, buf.Bytes())); got != cap(out.ch) {
		t.Fatalf("drained %d envelopes, want %d", got, cap(out.ch))
	}
}

func TestStdoutWriterApplicationCancellationReleasesOrderingWait(t *testing.T) {
	out := newStdoutWriter(context.Background(), &bytes.Buffer{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out.commandCtx = ctx
	out.ordering <- struct{}{}
	done := make(chan struct{})
	var invoked atomic.Bool
	go func() {
		out.ordered(func() { invoked.Store(true) })
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("application cancellation did not release ordering wait")
	}
	if invoked.Load() {
		t.Fatal("cancelled ordering wait still published a reply")
	}
	<-out.ordering
}
