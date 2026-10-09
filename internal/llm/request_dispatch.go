package llm

import "context"

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
	if d, ok := ctx.Value(requestDispatchContextKey{}).(*RequestDispatch); ok && !d.sent {
		d.sent = true
		if d.onSent != nil {
			d.onSent()
		}
	}
}
