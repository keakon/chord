package agent

import (
	"context"
	"slices"
	"sync/atomic"

	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

func prepareDirectoryInstructionAttempt(r *directoryInstructionRuntime, requestSeq uint64) func(context.Context, llm.RequestAttempt) (context.Context, error) {
	if r == nil {
		return nil
	}
	var sequence atomic.Uint64
	return func(ctx context.Context, surface llm.RequestAttempt) (context.Context, error) {
		view := r.view.Load()
		if view == nil {
			return ctx, nil
		}
		visible, err := validateDirectoryInstructionRequest(surface.Messages, *view)
		if err != nil {
			return ctx, err
		}
		// Rules never fail a request: if the request they produce is too large,
		// the ordinary trimming and compaction paths decide what to drop. A rule
		// file is prompt material, not a reason to reject a call.
		return llm.WithRequestStarted(ctx, func() {
			r.attempt.Store(&directoryInstructionAttempt{request: directoryInstructionRequest{Binding: r.binding, RequestSeq: requestSeq, AttemptSeq: sequence.Add(1), Visible: visible}})
		}), nil
	}
}
func projectDirectoryInstructionHistory(history []message.Message, r *directoryInstructionRuntime) ([]message.Message, error) {
	if r == nil {
		return history, nil
	}
	// Already projected fallback input keeps its immutable references and positions.
	hasFact := false
	for _, m := range history {
		if m.Kind == message.KindDirectoryInstructions {
			hasFact = true
			break
		}
	}
	v := r.view.Load()
	if hasFact || v == nil {
		next := buildDirectoryInstructionView(r.initial, history, r.binding)
		v = &next
		r.view.Store(v)
	}
	if v.Err != nil {
		return nil, v.Err
	}
	// Request history is already a detached, read-only snapshot. Without facts
	// to replace, preserve its backing array as other no-op request projections
	// do: a scope whose rules could not be loaded is simply absent and never
	// becomes a request message.
	if !hasFact {
		return slices.Clip(history), nil
	}
	return projectDirectoryInstructions(history, *v)
}
