package agent

import (
	"context"
	"maps"
	"path/filepath"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

const eventDirectoryInstructionsPrepared = "directory_instructions_prepared"

type directoryInstructionPreparation struct {
	candidate          *message.Message
	adopted            bool
	failed             bool
	dynamic            bool
	historyFingerprint string
	activationKey      string
	binding            directoryInstructionBinding
	turnID             uint64
	sourceID           string
}
type directoryInstructionsPrepared struct {
	preparation *directoryInstructionPreparation
	runtime     *directoryInstructionRuntime
	facts       []message.DirectoryInstructionFact
	err         error
	committed   bool
}

// instructionReadDenied matches only a read rule that names a path, not the
// wildcard default deny or a path-universal read rule: directory rules are
// prompt material, and a blanket default must not hide them. An explicit path
// deny is a user decision and also covers a rule-file symlink that points at
// the denied target.
func (a *MainAgent) instructionReadDenied(path string) bool {
	match := a.effectiveRuleset().LastNamedPathMatch(tools.NameRead, path, a.effectivePathScope())
	return match.Found && match.Rule.Action == permission.ActionDeny
}

func (s *SubAgent) instructionReadDenied(path string) bool {
	match := s.currentRuleset().LastNamedPathMatch(tools.NameRead, path, s.effectivePathScope())
	return match.Found && match.Rule.Action == permission.ActionDeny
}

func prepareDirectoryInstructions(ctx context.Context, binding directoryInstructionBinding, history []message.Message, readDenied func(string) bool) (*directoryInstructionRuntime, []message.DirectoryInstructionFact, error) {
	inputBinding := binding
	binding, err := prepareDirectoryInstructionBinding(ctx, binding)
	if err != nil {
		return nil, nil, err
	}
	r := newDirectoryInstructionRuntime(context.WithoutCancel(ctx), binding)
	r.inputBinding = inputBinding
	r.readDenied = readDenied
	fail := func(err error) (*directoryInstructionRuntime, []message.DirectoryInstructionFact, error) {
		r.cancel()
		return nil, nil, err
	}
	first, err := r.Discover(ctx, binding, []tools.InstructionTarget{{Path: binding.WorkDir, Mode: tools.InstructionTargetDirectory}})
	if err != nil {
		return fail(err)
	}
	r.initial = directoryInstructionSnapshot{Observations: maps.Clone(first.Observations)}
	latest := make(map[string]message.DirectoryInstructionFact)
	for _, m := range history {
		if m.Kind != message.KindDirectoryInstructions {
			continue
		}
		if err := message.ValidateDirectoryInstructions(m); err != nil {
			return fail(err)
		}
		for _, f := range m.DirectoryInstructions.Facts {
			if f.CheckoutRoot == binding.CheckoutRoot {
				latest[f.Scope] = f
			}
		}
	}
	observed := maps.Clone(first.Observations)
	for scope := range latest {
		target := tools.InstructionTarget{Path: filepath.Join(binding.CheckoutRoot, filepath.FromSlash(scope)), Mode: tools.InstructionTargetDirectory}
		d, err := r.Discover(ctx, binding, []tools.InstructionTarget{target})
		if err != nil {
			return fail(err)
		}
		maps.Copy(observed, d.Observations)
		resolution := d.Resolutions[target]
		switch {
		case resolution.Failure != nil:
			observed[scope] = *resolution.Failure
		case resolution.Directory != target.Path:
			// A formerly local scope may now be a nested checkout or a symlink
			// to another directory. Retire its old identity durably; any newly
			// resolved in-scope chain is reconciled through its own observations.
			observed[scope] = directoryInstructionObservation{Status: "absent", SourcePath: latest[scope].SourcePath}
		}
	}
	var facts []message.DirectoryInstructionFact
	for scope, o := range observed {
		old, known := latest[scope]
		f := directoryInstructionFactFromObservation(binding, scope, o)
		_, initial := r.initial.Observations[scope]
		if initial && !known {
			continue
		}
		if !known && f.Operation == message.DirectoryInstructionRemove {
			continue
		}
		if known && old == f {
			continue
		}
		facts = append(facts, f)
	}
	message.SortDirectoryInstructionFacts(facts)
	// A rule file that cannot be loaded is simply absent: directory rules are
	// prompt material, and the harness neither judges nor enforces their content.
	// A scope without them behaves exactly like a scope with no AGENTS.md, which
	// is also why the condition never reaches the model.
	merged := append([]message.Message(nil), history...)
	if len(facts) > 0 {
		merged = append(merged, directoryInstructionFactMessage(facts))
	}
	v := buildDirectoryInstructionView(r.initial, merged, binding)
	if v.Err != nil {
		return fail(v.Err)
	}
	r.view.Store(&v)
	return r, facts, nil
}
func directoryInstructionFactMessage(facts []message.DirectoryInstructionFact) message.Message {
	return message.Message{Role: message.RoleUser, Kind: message.KindDirectoryInstructions, DirectoryInstructions: &message.DirectoryInstructions{Facts: append([]message.DirectoryInstructionFact(nil), facts...)}}
}

func directoryInstructionBindingMatches(r *directoryInstructionRuntime, b directoryInstructionBinding) bool {
	return r != nil && r.binding.SessionEpoch == b.SessionEpoch && r.binding.AgentID == b.AgentID && r.binding.Generation == b.Generation && r.inputBinding.WorkDir == b.WorkDir && r.inputBinding.ContentRoot == b.ContentRoot
}
func (a *MainAgent) ensureDirectoryInstructionsBeforeRequest(ctx context.Context, turnID uint64, sourceID string) bool {
	binding := directoryInstructionBinding{SessionEpoch: a.sessionEpoch, AgentID: "main", Generation: a.workDirState.load().Generation, ContentRoot: a.ContentRoot(), WorkDir: a.effectiveToolBaseDir()}
	if p := a.directoryPreparation; p != nil {
		if !p.failed {
			return false
		}
		if err := a.reconcileDirectoryInstructionsAfterFlush(); err != nil {
			a.handleAgentError(Event{Type: EventAgentError, TurnID: turnID, SourceID: sourceID, Payload: err})
			return false
		}
		a.directoryPreparation = nil
	}
	if r := a.directoryInstructions.Load(); directoryInstructionBindingMatches(r, binding) {
		return a.ensureDirectoryInstructionCandidates(ctx, turnID, sourceID, r)
	}

	if old := a.directoryInstructions.Swap(nil); old != nil {
		old.cancel()
	}
	prep := &directoryInstructionPreparation{binding: binding, turnID: turnID, sourceID: sourceID}
	a.directoryPreparation = prep
	history := a.ctxMgr.Snapshot()
	prep.historyFingerprint = compactionTranscriptFingerprint(history)
	prep.activationKey = a.directoryInstructionActivationKey(history)
	inputs := a.directoryCandidateInputs(history)
	go func() {
		r, facts, err := prepareDirectoryInstructions(ctx, binding, history, a.instructionReadDenied)
		if err == nil {
			facts, err = budgetRestoredDirectoryInstructions(r, facts, history, inputs)
		}
		a.sendEvent(Event{Type: eventDirectoryInstructionsPrepared, TurnID: turnID, Payload: directoryInstructionsPrepared{preparation: prep, runtime: r, facts: facts, err: err}})
	}()
	return false
}
func (a *MainAgent) handleDirectoryInstructionsPrepared(evt Event) {
	e, ok := evt.Payload.(directoryInstructionsPrepared)
	if !ok {
		return
	}
	if e.committed && e.err == nil && a.directoryPreparation == e.preparation && a.sessionEpoch == e.preparation.binding.SessionEpoch && !e.preparation.adopted && len(e.facts) > 0 {
		e.preparation.adopted = true
		a.ctxMgr.Append(directoryInstructionFactMessage(e.facts))
		e.facts = nil
	}
	if a.directoryPreparation != e.preparation || a.sessionEpoch != e.preparation.binding.SessionEpoch || a.workDirState.load().Generation != e.preparation.binding.Generation || a.turn == nil || a.turn.ID != e.preparation.turnID || a.turn.Ctx.Err() != nil {
		if e.runtime != nil && !e.preparation.dynamic {
			e.runtime.cancel()
		}
		if a.directoryPreparation == e.preparation {
			if e.err != nil && e.preparation.candidate != nil {
				e.preparation.failed = true
				if a.turn != nil && a.turn.ID != e.preparation.turnID && a.turn.Ctx.Err() == nil {
					a.beginMainLLMAfterPreparation(a.turn.Ctx, a.turn.ID, e.preparation.sourceID)
				}
				return
			}
			if e.preparation.dynamic && e.runtime != nil {
				if e.preparation.adopted && a.directoryInstructions.Load() == e.runtime && e.runtime.binding == e.preparation.binding {
					recordDirectoryInstructionActivation(e.runtime, e.preparation)
				}
				e.runtime.pending = nil
				v := buildDirectoryInstructionView(e.runtime.initial, a.ctxMgr.Snapshot(), e.runtime.binding)
				e.runtime.view.Store(&v)
			}
			a.directoryPreparation = nil
			if a.turn != nil && a.turn.ID != e.preparation.turnID && a.turn.Ctx.Err() == nil {
				a.beginMainLLMAfterPreparation(a.turn.Ctx, a.turn.ID, e.preparation.sourceID)
			}
		}
		return
	}
	if e.err != nil {
		if e.runtime != nil && !e.preparation.dynamic {
			e.runtime.cancel()
		}
		e.preparation.failed = e.preparation.candidate != nil
		if !e.preparation.failed {
			a.directoryPreparation = nil
		}
		a.handleAgentError(Event{Type: EventAgentError, TurnID: e.preparation.turnID, SourceID: e.preparation.sourceID, Payload: e.err})
		return
	}
	if !e.committed && (e.preparation.historyFingerprint != compactionTranscriptFingerprint(a.ctxMgr.Snapshot()) || e.preparation.activationKey != a.directoryInstructionActivationKey(a.ctxMgr.Snapshot())) {
		if !e.preparation.dynamic {
			e.runtime.cancel()
		}
		a.directoryPreparation = nil
		a.beginMainLLMAfterPreparation(a.turn.Ctx, a.turn.ID, e.preparation.sourceID)
		return
	}
	if len(e.facts) > 0 && !e.committed {
		msg := directoryInstructionFactMessage(e.facts)
		e.preparation.candidate = &msg
		e.committed = true
		if !a.persistAsyncAfter(identity.MainAgentID, msg, func(err error) {
			e.err = err
			a.sendEvent(Event{Type: eventDirectoryInstructionsPrepared, TurnID: e.preparation.turnID, Payload: e})
		}) {
			e.preparation.candidate = nil
			e.err = errPersistenceQueueUnavailable
			a.handleDirectoryInstructionsPrepared(Event{Payload: e})
		}
		return
	}
	if len(e.facts) > 0 && !e.preparation.adopted {
		e.preparation.adopted = true
		a.ctxMgr.Append(directoryInstructionFactMessage(e.facts))
	}
	recordDirectoryInstructionActivation(e.runtime, e.preparation)
	e.runtime.pending = nil
	v := buildDirectoryInstructionView(e.runtime.initial, a.ctxMgr.Snapshot(), e.runtime.binding)
	e.runtime.view.Store(&v)
	if !e.preparation.dynamic {
		context.AfterFunc(a.parentCtx, e.runtime.cancel)
	}
	a.directoryInstructions.Store(e.runtime)
	a.directoryPreparation = nil
	a.refreshSessionContextReminder()
	a.beginMainLLMAfterPreparation(a.turn.Ctx, a.turn.ID, e.preparation.sourceID)
}
func (s *SubAgent) ensureDirectoryInstructionsBeforeRequest(turn *Turn) bool {
	root := s.effectiveToolBaseDir()
	if s.parent != nil {
		root = s.parent.ContentRoot()
	}
	binding := directoryInstructionBinding{SessionEpoch: s.sessionEpoch, AgentID: s.taskID, Generation: s.workDirState.load().Generation, ContentRoot: root, WorkDir: s.effectiveToolBaseDir()}
	if p := s.directoryPreparation; p != nil {
		if !p.failed {
			return false
		}
		if err := s.reconcileDirectoryInstructionsAfterFlush(); err != nil {
			s.finishLLMRequest()
			s.handleLLMResponse(&llmResult{turnID: turn.ID, err: err})
			return false
		}
		s.directoryPreparation = nil
	}
	if r := s.directoryInstructions.Load(); directoryInstructionBindingMatches(r, binding) {
		return s.ensureDirectoryInstructionCandidates(turn, r)
	}

	if old := s.directoryInstructions.Swap(nil); old != nil {
		old.cancel()
	}
	if s.directoryPreparedCh == nil {
		s.directoryPreparedCh = make(chan directoryInstructionsPrepared, 1)
	}
	prep := &directoryInstructionPreparation{binding: binding, turnID: turn.ID}
	s.directoryPreparation = prep
	history := s.ctxMgr.Snapshot()
	prep.historyFingerprint = compactionTranscriptFingerprint(history)
	prep.activationKey = s.directoryInstructionActivationKey(history)
	inputs := s.directoryCandidateInputs(history)
	s.llmRequestInFlight.Store(true)
	s.llmWG.Go(func() {
		r, facts, err := prepareDirectoryInstructions(turn.Ctx, binding, history, s.instructionReadDenied)
		if err == nil {
			facts, err = budgetRestoredDirectoryInstructions(r, facts, history, inputs)
		}
		e := directoryInstructionsPrepared{preparation: prep, runtime: r, facts: facts, err: err}
		select {
		case s.directoryPreparedCh <- e:
		case <-s.parentCtx.Done():
			if r != nil {
				r.cancel()
			}
		}
	})
	return false
}
func (s *SubAgent) handleDirectoryInstructionsPrepared(e directoryInstructionsPrepared) {
	if e.committed && e.err == nil && s.directoryPreparation == e.preparation && s.sessionEpoch == e.preparation.binding.SessionEpoch && !e.preparation.adopted && len(e.facts) > 0 {
		e.preparation.adopted = true
		s.ctxMgr.Append(directoryInstructionFactMessage(e.facts))
		e.facts = nil
	}
	if s.directoryPreparation != e.preparation || s.turn == nil || s.turn.ID != e.preparation.turnID || s.turn.Ctx.Err() != nil || s.workDirState.load().Generation != e.preparation.binding.Generation {
		if e.runtime != nil && !e.preparation.dynamic {
			e.runtime.cancel()
		}
		if s.directoryPreparation == e.preparation {
			if e.err != nil && e.preparation.candidate != nil {
				e.preparation.failed = true
				s.llmRequestInFlight.Store(false)
				if s.turn != nil && s.turn.ID != e.preparation.turnID && s.turn.Ctx.Err() == nil {
					s.asyncCallLLMWithFlightMarked(s.turn)
				}
				return
			}
			if e.preparation.dynamic && e.runtime != nil {
				if e.preparation.adopted && s.directoryInstructions.Load() == e.runtime && e.runtime.binding == e.preparation.binding {
					recordDirectoryInstructionActivation(e.runtime, e.preparation)
				}
				e.runtime.pending = nil
				v := buildDirectoryInstructionView(e.runtime.initial, s.ctxMgr.Snapshot(), e.runtime.binding)
				e.runtime.view.Store(&v)
			}
			s.directoryPreparation = nil
			s.llmRequestInFlight.Store(false)
			if s.turn != nil && s.turn.ID != e.preparation.turnID && s.turn.Ctx.Err() == nil {
				s.asyncCallLLMWithFlightMarked(s.turn)
			}
		}
		return
	}
	if e.err != nil {
		if e.runtime != nil && !e.preparation.dynamic {
			e.runtime.cancel()
		}
		e.preparation.failed = e.preparation.candidate != nil
		if !e.preparation.failed {
			s.directoryPreparation = nil
		}
		s.finishLLMRequest()
		s.handleLLMResponse(&llmResult{turnID: e.preparation.turnID, err: e.err})
		return
	}
	if !e.committed && (e.preparation.historyFingerprint != compactionTranscriptFingerprint(s.ctxMgr.Snapshot()) || e.preparation.activationKey != s.directoryInstructionActivationKey(s.ctxMgr.Snapshot())) {
		if !e.preparation.dynamic {
			e.runtime.cancel()
		}
		s.directoryPreparation = nil
		s.asyncCallLLMWithFlightMarked(s.turn)
		return
	}
	if len(e.facts) > 0 && !e.committed {
		msg := directoryInstructionFactMessage(e.facts)
		e.preparation.candidate = &msg
		barrier, accepted := s.persistMessageBarrier(msg, "directory instructions reconciliation")
		if !accepted {
			e.preparation.candidate = nil
		}
		e.committed = true
		s.llmWG.Go(func() {
			e.err = <-barrier
			select {
			case s.directoryPreparedCh <- e:
			case <-s.parentCtx.Done():
				e.runtime.cancel()
			}
		})
		return
	}
	if len(e.facts) > 0 && !e.preparation.adopted {
		e.preparation.adopted = true
		s.ctxMgr.Append(directoryInstructionFactMessage(e.facts))
	}
	recordDirectoryInstructionActivation(e.runtime, e.preparation)
	e.runtime.pending = nil
	v := buildDirectoryInstructionView(e.runtime.initial, s.ctxMgr.Snapshot(), e.runtime.binding)
	e.runtime.view.Store(&v)
	if !e.preparation.dynamic {
		context.AfterFunc(s.parentCtx, e.runtime.cancel)
	}
	s.directoryInstructions.Store(e.runtime)
	s.directoryPreparation = nil
	s.refreshSessionContextReminder()
	s.asyncCallLLMWithFlightMarked(s.turn)
}
