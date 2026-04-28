package tools

import "context"

// ToolProgressSnapshot is a best-effort, structured progress snapshot emitted
// by tools that have real progress signals.
type ToolProgressSnapshot struct {
	Label   string
	Current int64
	Total   int64
	Text    string
}

// ToolProgressReporter delivers progress snapshots for a single visible tool
// call. Tools should treat the reporter as optional.
type ToolProgressReporter interface {
	ReportToolProgress(progress ToolProgressSnapshot)
}

const toolProgressReporterKey contextKey = 100

// WithToolProgressReporter returns a new context that carries the given
// progress reporter.
func WithToolProgressReporter(ctx context.Context, reporter ToolProgressReporter) context.Context {
	if reporter == nil {
		return ctx
	}
	return context.WithValue(ctx, toolProgressReporterKey, reporter)
}

// ToolProgressReporterFromContext extracts the tool progress reporter from the
// context, or returns nil if absent.
func ToolProgressReporterFromContext(ctx context.Context) ToolProgressReporter {
	if v, ok := ctx.Value(toolProgressReporterKey).(ToolProgressReporter); ok {
		return v
	}
	return nil
}

func reportToolProgress(ctx context.Context, progress ToolProgressSnapshot) {
	if reporter := ToolProgressReporterFromContext(ctx); reporter != nil {
		reporter.ReportToolProgress(progress)
	}
}
