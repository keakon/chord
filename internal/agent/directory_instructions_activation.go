package agent

import (
	"context"
	"fmt"
	"maps"
	"strings"

	"github.com/keakon/chord/internal/message"
)

// Detached activation inputs are immutable while the owner collects subsequent
// events. No worker reads or clears the runtime's owner-only pending map.
func directoryCandidateSnapshot(r *directoryInstructionRuntime) *directoryInstructionRuntime {
	snapshot := &directoryInstructionRuntime{binding: r.binding, initial: directoryInstructionSnapshot{Observations: maps.Clone(r.initial.Observations)}, pending: maps.Clone(r.pending)}
	return snapshot
}
func (a *MainAgent) ensureDirectoryInstructionCandidates(ctx context.Context, turnID uint64, sourceID string, r *directoryInstructionRuntime) bool {
	if a.directoryPreparation != nil {
		return false
	}
	if len(r.pending) == 0 {
		return true
	}
	history := a.ctxMgr.Snapshot()
	key := a.directoryInstructionActivationKey(history)
	snapshot := directoryCandidateSnapshot(r)
	filterSuppressedDirectoryCandidates(snapshot, r.blockedRevision, key)
	if len(snapshot.pending) == 0 {
		r.pending = nil
		return true
	}
	prep := &directoryInstructionPreparation{binding: r.binding, turnID: turnID, sourceID: sourceID, dynamic: true, activationKey: key, historyFingerprint: compactionTranscriptFingerprint(history)}
	inputs := a.directoryCandidateInputs(history)
	a.directoryPreparation = prep
	go func() {
		msg, err := buildDirectoryCandidateMessage(snapshot, history, inputs.system, inputs.defs, inputs.budget, inputs.overlays)
		var facts []message.DirectoryInstructionFact
		if msg != nil {
			facts = msg.DirectoryInstructions.Facts
		}
		if err == nil {
			err = ctx.Err()
		}
		a.sendEvent(Event{Type: eventDirectoryInstructionsPrepared, TurnID: turnID, Payload: directoryInstructionsPrepared{preparation: prep, runtime: r, facts: facts, err: err}})
	}()
	return false
}
func (s *SubAgent) ensureDirectoryInstructionCandidates(turn *Turn, r *directoryInstructionRuntime) bool {
	if s.directoryPreparation != nil {
		return false
	}
	if len(r.pending) == 0 {
		return true
	}
	if s.directoryPreparedCh == nil {
		s.directoryPreparedCh = make(chan directoryInstructionsPrepared, 1)
	}
	history := s.ctxMgr.Snapshot()
	key := s.directoryInstructionActivationKey(history)
	snapshot := directoryCandidateSnapshot(r)
	filterSuppressedDirectoryCandidates(snapshot, r.blockedRevision, key)
	if len(snapshot.pending) == 0 {
		r.pending = nil
		return true
	}
	prep := &directoryInstructionPreparation{binding: r.binding, turnID: turn.ID, dynamic: true, activationKey: key, historyFingerprint: compactionTranscriptFingerprint(history)}
	inputs := s.directoryCandidateInputs(history)
	s.directoryPreparation = prep
	s.llmRequestInFlight.Store(true)
	s.llmWG.Go(func() {
		msg, err := buildDirectoryCandidateMessage(snapshot, history, inputs.system, inputs.defs, inputs.budget, inputs.overlays)
		var facts []message.DirectoryInstructionFact
		if msg != nil {
			facts = msg.DirectoryInstructions.Facts
		}
		if err == nil {
			err = turn.Ctx.Err()
		}
		e := directoryInstructionsPrepared{preparation: prep, runtime: r, facts: facts, err: err}
		select {
		case s.directoryPreparedCh <- e:
		case <-s.parentCtx.Done():
		}
	})
	return false
}

// Called on the owner after flushing its persistence pump, before a history
// rewrite/freeze. A write acknowledgement queued behind this operation must
// not cause the rewrite to forget a fact already on disk.
func (a *MainAgent) reconcileDirectoryInstructionsAfterFlush() error {
	p := a.directoryPreparation
	if p == nil || p.candidate == nil || p.adopted {
		return nil
	}
	if err := a.persist.strictError(a.recoveryManager()); err != nil {
		return err
	}
	a.ctxMgr.Append(p.candidate.Clone())
	p.adopted = true
	if r := a.directoryInstructions.Load(); p.dynamic && r != nil && r.binding == p.binding {
		recordDirectoryInstructionActivation(r, p)
		r.pending = nil
		v := buildDirectoryInstructionView(r.initial, a.ctxMgr.Snapshot(), r.binding)
		r.view.Store(&v)
	}
	return nil
}
func (s *SubAgent) reconcileDirectoryInstructionsAfterFlush() error {
	p := s.directoryPreparation
	if p == nil || p.candidate == nil || p.adopted {
		return nil
	}
	if s.parent != nil {
		if err := s.parent.persist.strictError(s.recoveryManager()); err != nil {
			return err
		}
	}
	s.ctxMgr.Append(p.candidate.Clone())
	p.adopted = true
	if r := s.directoryInstructions.Load(); p.dynamic && r != nil && r.binding == p.binding {
		recordDirectoryInstructionActivation(r, p)
		r.pending = nil
		v := buildDirectoryInstructionView(r.initial, s.ctxMgr.Snapshot(), r.binding)
		r.view.Store(&v)
	}
	return nil
}

func directoryInstructionActivationKey(history []message.Message, model string, budget int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s:%d", model, budget)
	userOrdinal := 0
	for _, m := range history {
		if message.IsUserAuthored(m) {
			userOrdinal++
			fmt.Fprintf(&b, "\nu:%d:%s:%s", userOrdinal, m.InputID, message.DirectoryInstructionBodyHash(m.Content))
		}
		if m.IsCompactionSummary {
			fmt.Fprintf(&b, "\nc:%s", message.DirectoryInstructionBodyHash(m.Content))
			if m.CompactionSkillActivation != nil {
				fmt.Fprintf(&b, ":%d", m.CompactionSkillActivation.ContextEpoch)
			}
		}
	}
	return message.DirectoryInstructionBodyHash(b.String())
}
func (a *MainAgent) directoryInstructionActivationKey(history []message.Message) string {
	a.llmMu.RLock()
	client := a.llmClient
	a.llmMu.RUnlock()
	model := ""
	if client != nil {
		model = client.NextRequestModelRef()
	}
	return directoryInstructionActivationKey(history, model, a.ctxMgr.GetInputBudget())
}
func (s *SubAgent) directoryInstructionActivationKey(history []message.Message) string {
	client, _ := s.llmSnapshot()
	model := ""
	if client != nil {
		model = client.NextRequestModelRef()
	}
	return directoryInstructionActivationKey(history, model, s.ctxMgr.GetInputBudget())
}
func filterSuppressedDirectoryCandidates(snapshot *directoryInstructionRuntime, blocked map[string]string, key string) {
	for scope := range snapshot.pending {
		if blocked[scope] == key {
			delete(snapshot.pending, scope)
		}
	}
	for scope, o := range snapshot.pending {
		if _, initial := snapshot.initial.Observations[scope]; initial {
			snapshot.initial.Observations[scope] = o
		}
	}
}

func recordDirectoryInstructionActivation(r *directoryInstructionRuntime, p *directoryInstructionPreparation) {
	if p.candidate == nil {
		return
	}
	if r.blockedRevision == nil {
		r.blockedRevision = make(map[string]string)
	}
	for _, f := range p.candidate.DirectoryInstructions.Facts {
		if _, initial := r.initial.Observations[f.Scope]; initial {
			status := "blocked"
			if f.Operation == message.DirectoryInstructionUpsert {
				status = "present"
			} else if f.Operation == message.DirectoryInstructionRemove {
				status = "absent"
			}
			r.initial.Observations[f.Scope] = directoryInstructionObservation{Status: status, SourcePath: f.SourcePath, Body: f.Body, BodyHash: f.BodyHash, Reason: f.Reason, Detail: f.Detail}
		}
		if f.Operation == message.DirectoryInstructionBlocked && f.Reason == message.DirectoryInstructionReasonInputBudget {
			r.blockedRevision[f.Scope] = p.activationKey
		} else {
			delete(r.blockedRevision, f.Scope)
		}
	}
}
