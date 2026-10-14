package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

// stdoutWriter serializes JSON envelopes to stdout via a single goroutine.
type stdoutWriter struct {
	writer           io.Writer
	ch               chan any
	ctx              context.Context
	commandCtx       context.Context
	cancel           context.CancelFunc
	admission        chan struct{}
	ordering         chan struct{}
	closeOnce        sync.Once
	shutdownOnce     sync.Once
	shutdownDeadline time.Time
	done             chan struct{}
	errCh            chan error
}

// newStdoutWriter creates a stdoutWriter with a buffered channel.
func newStdoutWriter(ctx context.Context, w io.Writer) *stdoutWriter {
	writerCtx, cancel := context.WithCancel(ctx)
	return &stdoutWriter{
		writer:    w,
		ch:        make(chan any, 256),
		ctx:       writerCtx,
		cancel:    cancel,
		admission: make(chan struct{}, 1),
		ordering:  make(chan struct{}, 1),
		done:      make(chan struct{}),
		errCh:     make(chan error, 1),
	}
}

// run processes the channel and writes JSON envelopes to stdout.
func (w *stdoutWriter) run() {
	defer close(w.done)
	defer close(w.errCh)
	for msg := range w.ch {
		if barrier, ok := msg.(stdoutBarrier); ok {
			close(barrier.done)
			continue
		}
		line, err := json.Marshal(msg)
		if err == nil && len(line)+1 > headlessMaxEnvelopeBytes {
			err = fmt.Errorf("encoded envelope exceeds %d bytes", headlessMaxEnvelopeBytes)
		}
		if err == nil {
			line = append(line, '\n')
			for len(line) > 0 {
				n, writeErr := w.writer.Write(line)
				if writeErr != nil {
					err = writeErr
					break
				}
				if n <= 0 {
					err = io.ErrShortWrite
					break
				}
				line = line[n:]
			}
		}
		if err != nil {
			w.errCh <- fmt.Errorf("write headless output: %w", err)
			w.cancel()
			return
		}
	}
}

func (w *stdoutWriter) errors() <-chan error {
	if w == nil {
		return nil
	}
	return w.errCh
}

// ordered runs f while holding the push-ordering lock. Seq allocation and the
// channel enqueue for a state-carrying push must happen inside one ordered
// section (on both the event-loop and the command path): otherwise a concurrent
// push could allocate the next seq and reach the wire in the stamp-to-emit gap,
// and a client that applies only newer versions would drop the older push. For
// role_change that loss is permanent — the announcement dedupe marker already
// advanced at stamp time, so the switch is never re-announced.
//
// Sections must not enter while holding state.mu. Ordering and queue admission
// are cancellable so output backpressure cannot trap the command loop during
// application shutdown. Event draining uses the writer's separate lifetime.
func (w *stdoutWriter) ordered(f func()) {
	w.orderedWithContext(w.sendContext(), f)
}

func (w *stdoutWriter) sendContext() context.Context {
	if w.commandCtx != nil {
		return w.commandCtx
	}
	return w.ctx
}

func (w *stdoutWriter) orderedWithContext(ctx context.Context, f func()) {
	select {
	case w.ordering <- struct{}{}:
	case <-ctx.Done():
		return
	case <-w.ctx.Done():
		return
	}
	defer func() { <-w.ordering }()
	if ctx.Err() != nil || w.ctx.Err() != nil {
		return
	}
	f()
}

// emit sends a message to the channel. It blocks if the channel is full
// (stdio is a reliable pipe; control requests, SubAgent lifecycle events,
// and response events must never be silently dropped).
// Returns false if the context was cancelled before the message could be sent.
func (w *stdoutWriter) emit(msg any) bool {
	return w.emitWithContext(w.sendContext(), msg)
}

func (w *stdoutWriter) emitWithContext(ctx context.Context, msg any) bool {
	select {
	case w.admission <- struct{}{}:
	case <-ctx.Done():
		return false
	case <-w.ctx.Done():
		return false
	}
	defer func() { <-w.admission }()
	if ctx.Err() != nil || w.ctx.Err() != nil {
		return false
	}
	select {
	case w.ch <- msg:
		return true
	case <-ctx.Done():
		return false
	case <-w.ctx.Done():
		return false
	}
}

// closeUntil cancels new sends, closes the input queue, and waits for the
// writer to finish up to deadline. A blocked underlying Write cannot always be
// interrupted through io.Writer, so a bounded close may return while that
// goroutine remains alive; callers must then abandon the transport rather than
// waiting indefinitely.
func (w *stdoutWriter) closeUntil(deadline <-chan time.Time) bool {
	if w == nil {
		return true
	}
	w.cancel()
	select {
	case w.admission <- struct{}{}:
	case <-deadline:
		return false
	}
	w.closeOnce.Do(func() {
		close(w.ch)
	})
	<-w.admission
	if deadline == nil {
		<-w.done
		return true
	}
	select {
	case <-w.done:
		return true
	case <-deadline:
		return false
	}
}
