package main

import (
	"context"
	"time"
)

// shutdownOutputDeadline is shared by command replies and final output draining.
// Starting it when application cancellation is observed keeps a blocked reply
// from adding another output wait budget before cleanup can run.
func (w *stdoutWriter) shutdownOutputDeadline() time.Time {
	w.shutdownOnce.Do(func() {
		w.shutdownDeadline = time.Now().Add(agentShutdownWait)
	})
	return w.shutdownDeadline
}

// inputReplyContext preserves ordinary output backpressure until the application
// stops. Cancellation then bounds the reply by the shared output drain deadline,
// while a writer failure interrupts it immediately.
func (w *stdoutWriter) inputReplyContext() (context.Context, context.CancelFunc) {
	if w.sendContext().Err() != nil {
		return context.WithDeadline(w.ctx, w.shutdownOutputDeadline())
	}
	ctx, cancel := context.WithCancel(w.ctx)
	stop := context.AfterFunc(w.sendContext(), func() {
		drainCtx, stopDrain := context.WithDeadline(ctx, w.shutdownOutputDeadline())
		defer stopDrain()
		<-drainCtx.Done()
		cancel()
	})
	return ctx, func() {
		stop()
		cancel()
	}
}
