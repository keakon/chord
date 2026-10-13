package llm

import (
	"context"
)

type requestDispatchContextKey struct{}

// RequestDispatch records adapter evidence on the request goroutine. An
// untracked provider remains conservative: its failures may have been sent.
type RequestDispatch struct {
	prepared, sent bool
	onSent         func()
}

func WithRequestDispatch(ctx context.Context, onSent func()) (context.Context, *RequestDispatch) {
	dispatch := &RequestDispatch{onSent: onSent}
	return context.WithValue(ctx, requestDispatchContextKey{}, dispatch), dispatch
}

func (d *RequestDispatch) NotSent() bool { return d.prepared && !d.sent }

func markRequestPrepared(ctx context.Context) {
	if d, ok := ctx.Value(requestDispatchContextKey{}).(*RequestDispatch); ok {
		d.prepared = true
	}
}

func markRequestSent(ctx context.Context) {
	markRequestStarted(ctx)
	if d, ok := ctx.Value(requestDispatchContextKey{}).(*RequestDispatch); ok && !d.sent {
		d.sent = true
		if d.onSent != nil {
			d.onSent()
		}
	}
}

// WithRequestStarted observes every admitted transport submission in a call.
// The callback must be short and must not perform I/O; it is independent of
// request journals, which record each native request separately.
func WithRequestStarted(ctx context.Context, onStarted func()) context.Context {
	parent, _ := ctx.Value(requestStartedContextKey{}).(*requestStarted)
	if parent != nil && parent.onStarted != nil {
		current := onStarted
		onStarted = func() {
			parent.onStarted()
			if current != nil {
				current()
			}
		}
	}
	return context.WithValue(ctx, requestStartedContextKey{}, &requestStarted{onStarted: onStarted})
}

type requestStartedContextKey struct{}
type requestStarted struct{ onStarted func() }

func markRequestStarted(ctx context.Context) {
	if s, ok := ctx.Value(requestStartedContextKey{}).(*requestStarted); ok {
		if s.onStarted != nil {
			s.onStarted()
		}
	}
}
