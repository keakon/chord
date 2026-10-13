package agent

import "github.com/keakon/chord/internal/message"

// Reconciliation may discover new ancestors or changed bodies. Price all
// dynamic changes together; the initial chain remains mandatory and is
// admitted separately against the complete model pool.
func budgetRestoredDirectoryInstructions(r *directoryInstructionRuntime, facts []message.DirectoryInstructionFact, history []message.Message, inputs directoryCandidateBudgetInputs) ([]message.DirectoryInstructionFact, error) {
	snapshot := directoryCandidateSnapshot(r)
	snapshot.pending = make(map[string]directoryInstructionObservation)
	var initial []message.DirectoryInstructionFact
	for _, f := range facts {
		if _, ok := r.initial.Observations[f.Scope]; ok {
			initial = append(initial, f)
			continue
		}
		status := "blocked"
		if f.Operation == message.DirectoryInstructionUpsert {
			status = "present"
		} else if f.Operation == message.DirectoryInstructionRemove {
			status = "absent"
		}
		snapshot.pending[f.Scope] = directoryInstructionObservation{Status: status, SourcePath: f.SourcePath, Body: f.Body, BodyHash: f.BodyHash, Reason: f.Reason, Detail: f.Detail}
	}
	base := append([]message.Message(nil), history...)
	if len(initial) > 0 {
		base = append(base, directoryInstructionFactMessage(initial))
	}
	candidate, err := buildDirectoryCandidateMessage(snapshot, base, inputs.system, inputs.defs, inputs.budget, inputs.overlays)
	if err != nil {
		return nil, err
	}
	if candidate != nil {
		initial = append(initial, candidate.DirectoryInstructions.Facts...)
	}
	message.SortDirectoryInstructionFacts(initial)
	return initial, nil
}

// Only the owner calls this. Durable candidates already handed to persistence
// remain attached to their preparation so their acknowledgement can reconcile.
func (r *directoryInstructionRuntime) clearTurnDiscovery() {
	if r == nil {
		return
	}
	r.pending = nil
	if attempt := r.attempt.Swap(nil); attempt != nil {
		attempt.gate.Close(directoryInstructionStopReplan)
	}
}
