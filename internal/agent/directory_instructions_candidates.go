package agent

import (
	"fmt"
	"maps"
	"slices"

	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

func (r *directoryInstructionRuntime) collectDiscovery(d *directoryInstructionDiscovery) {
	if r == nil || d == nil || d.Binding != r.binding {
		return
	}
	attempt := r.attempt.Load()
	if attempt == nil || attempt.request.RequestSeq != d.RequestSeq || attempt.request.AttemptSeq != d.AttemptSeq {
		return
	}
	view := r.view.Load()
	for scope, o := range d.Observations {
		if o.Status == "absent" {
			known := false
			if view != nil {
				for _, fs := range [][]message.DirectoryInstructionFact{view.Dynamic, view.Blocked} {
					for _, f := range fs {
						if f.Scope == scope {
							known = true
							break
						}
					}
				}
			}
			if !known {
				continue
			}
		}
		if o.Status == "blocked" && view != nil {
			candidate := directoryInstructionFactFromObservation(r.binding, scope, o)
			if slices.Contains(view.Blocked, candidate) {
				continue
			}
		}

		if o.Status == "present" && view != nil {
			unchanged := false
			for _, active := range view.Dynamic {
				if active.Scope == scope && active.SourcePath == o.SourcePath && active.BodyHash == o.BodyHash {
					unchanged = true
					break
				}
			}
			if unchanged {
				continue
			}
		}
		if initial, ok := r.initial.Observations[scope]; ok && (initial.Status == "present" || initial.Status == "absent") {
			continue
		}
		if r.pending == nil {
			r.pending = make(map[string]directoryInstructionObservation)
		}
		r.pending[scope] = o
	}
}
func buildDirectoryCandidateMessage(r *directoryInstructionRuntime, history []message.Message, system string, defs []message.ToolDefinition, budget int, overlays ...[]message.Message) (*message.Message, error) {
	if r == nil || len(r.pending) == 0 {
		return nil, nil
	}
	observations := maps.Clone(r.pending)
	// Price the complete candidate surface before choosing one atomic activation.
	proposed := directoryInstructionCandidateFacts(r.binding, observations, history, int(^uint(0)>>1), 0)
	if len(proposed) == 0 {
		return nil, nil
	}
	candidate := directoryInstructionFactMessage(proposed)
	complete := append(append([]message.Message(nil), history...), candidate)
	view := buildDirectoryInstructionView(r.initial, complete, r.binding)
	projected, err := projectDirectoryInstructions(complete, view)
	if err != nil {
		return nil, fmt.Errorf("prepare directory instruction candidates: %w", err)
	}
	if len(view.Initial) > 0 {
		projected = append(projected, directoryInstructionProjection(view.Initial))
	}
	for _, extra := range overlays {
		projected = append(projected, extra...)
	}
	estimate := llm.EstimateRequestInputTokens(system, projected, defs)
	facts := directoryInstructionCandidateFacts(r.binding, observations, history, budget, estimate)
	if len(facts) == 0 {
		return nil, nil
	}
	result := directoryInstructionFactMessage(facts)
	return &result, nil
}
