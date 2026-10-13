package agent

import (
	"context"
	"errors"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

const EventSkillDiscoveryPublish = "skill_discovery_publish"

type skillDiscoveryGateResult uint8

const (
	skillDiscoveryReady skillDiscoveryGateResult = iota
	skillDiscoveryWaiting
	skillDiscoveryBlocked
)

type skillDiscoveryTicket struct {
	SessionEpoch                                               uint64
	TaskID                                                     string
	Attempt, TurnID, WorkDirGeneration, ContextEpoch, Sequence uint64
}
type skillDiscoveryPublishEvent struct {
	Ticket   skillDiscoveryTicket
	NoticeID string
	Err      error
}

// The publisher owns one immutable candidate until its original pump entry has
// been acknowledged. Request cancellation never re-enqueues that entry.
type skillDiscoveryPublisher struct {
	memoryOnlyAppend func(message.Message)
	checkoutRoot     string
	ticket           skillDiscoveryTicket
	candidate        *message.Message
	waiting          bool
	ready            bool
	err              error
	sequence         uint64
	resume           func()
	pendingResume    func()
	manager          *recovery.RecoveryManager
}

// skillDiscoveryPublisherOptions exposes memory-only persistence exclusively to
// explicit test adapters. The zero-value production publisher stays durable.
type skillDiscoveryPublisherOptions struct{ MemoryOnlyAppend func(message.Message) }

func newSkillDiscoveryPublisher(options skillDiscoveryPublisherOptions) skillDiscoveryPublisher {
	return skillDiscoveryPublisher{memoryOnlyAppend: options.MemoryOnlyAppend}
}

func waitSkillDiscoveryBarrier(ctx context.Context, pump *persistencePump, manager *recovery.RecoveryManager) error {
	if manager == nil || pump == nil {
		return errPersistenceQueueUnavailable
	}
	barrier := make(chan struct{})
	if !pump.enqueue(persistEntry{barrier: barrier}, ctx.Done()) {
		return errPersistenceQueueUnavailable
	}
	select {
	case <-barrier:
		return pump.strictError(manager)
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (p *skillDiscoveryPublisher) launchBarrier(ctx context.Context, pump *persistencePump, send func(skillDiscoveryPublishEvent)) {
	ticket := p.ticket
	manager := p.manager
	id := ""
	if p.candidate != nil {
		id = p.candidate.SkillDiscovery.ID
	}
	p.waiting = true
	go func() {
		err := waitSkillDiscoveryBarrier(ctx, pump, manager)
		send(skillDiscoveryPublishEvent{Ticket: ticket, NoticeID: id, Err: err})
	}()
}
func (p *skillDiscoveryPublisher) ensure(ctx context.Context, ticket skillDiscoveryTicket, history []message.Message, view skillActivationView, manager *recovery.RecoveryManager, pump *persistencePump, send func(skillDiscoveryPublishEvent), resume func()) skillDiscoveryGateResult {
	if p.memoryOnlyAppend != nil {
		if notice := buildSkillDiscoveryMessage(history, view); notice != nil {
			p.memoryOnlyAppend(*notice)
		}
		return skillDiscoveryReady
	}
	// A new session has a different durable owner. Its predecessor's pump
	// keeps the original write; recovery of that session performs reconciliation.
	if p.ticket.SessionEpoch != ticket.SessionEpoch || (p.ticket.TaskID != "" && p.ticket.TaskID != ticket.TaskID) {
		sequence := p.sequence
		*p = skillDiscoveryPublisher{sequence: sequence}
	}
	if p.ready {
		p.ready = false
		return skillDiscoveryReady
	}
	if p.waiting {
		p.pendingResume = resume
		return skillDiscoveryWaiting
	}
	if p.err != nil {
		if pump == nil || pump.strictError(p.manager) != nil {
			return skillDiscoveryBlocked
		}
		p.err = nil
		// Keep the original write's ticket and candidate for acknowledgment,
		// while retaining the current request independently. The retry callback
		// may belong to a later turn and must re-enter rather than inherit ready.
		p.resume = resume
		p.pendingResume = resume
		p.launchBarrier(ctx, pump, send)
		return skillDiscoveryWaiting
	}
	if buildSkillDiscoveryMessage(history, view) == nil {
		return skillDiscoveryReady
	}
	p.sequence++
	ticket.Sequence = p.sequence
	p.ticket = ticket
	p.checkoutRoot = view.CheckoutRoot
	p.manager = manager
	p.resume = resume
	p.launchBarrier(ctx, pump, send)
	return skillDiscoveryWaiting
}
func (p *skillDiscoveryPublisher) handle(evt skillDiscoveryPublishEvent, ctx context.Context, history []message.Message, view skillActivationView, pump *persistencePump, current bool, enqueue func(*recovery.RecoveryManager, message.Message) bool, appendMessage func(message.Message), send func(skillDiscoveryPublishEvent)) error {
	if evt.Ticket != p.ticket || !p.waiting {
		return nil
	}
	if current && evt.Ticket.ContextEpoch != skillActivationEpoch(history, view.TaskID) {
		// The same turn may outlive a successful compaction. Reconcile this
		// original write, then re-enter the gate without granting a ready pass.
		current = false
		if p.pendingResume == nil {
			p.pendingResume = p.resume
		}
	}
	p.waiting = false
	if evt.Err != nil {
		p.err = evt.Err
		return p.err
	}
	if evt.NoticeID != "" {
		// Reconcile an already committed candidate even if its turn was cancelled.
		if p.candidate != nil {
			found := false
			for _, msg := range history {
				if msg.SkillDiscovery != nil && msg.SkillDiscovery.ID == evt.NoticeID {
					found = true
					break
				}
			}
			if !found {
				appendMessage(*p.candidate)
			}
		}
		p.candidate = nil
		if !current && p.pendingResume != nil {
			next := p.pendingResume
			p.pendingResume = nil
			next()
			return nil
		}
		p.pendingResume = nil
		if current {
			p.ready = true
			if p.resume != nil {
				p.resume()
			}
		}
		return nil
	}
	if !current {
		p.resume = nil
		if p.pendingResume != nil {
			next := p.pendingResume
			p.pendingResume = nil
			next()
		}
		return nil
	}
	p.candidate = buildSkillDiscoveryMessage(history, view)
	if p.candidate == nil {
		p.ready = true
		if p.resume != nil {
			p.resume()
		}
		return nil
	}
	if !enqueue(p.manager, *p.candidate) {
		// Admission failed: no barrier may later certify this draft as written.
		// Accepted writes retain their immutable candidate across write errors.
		p.candidate = nil
		p.err = errPersistenceQueueUnavailable
		return p.err
	}
	p.launchBarrier(ctx, pump, send)
	return nil
}
func (a *MainAgent) skillDiscoverySend(evt skillDiscoveryPublishEvent) {
	a.queueLoopEvent(Event{Type: EventSkillDiscoveryPublish, Payload: evt})
}
func (a *MainAgent) ensureSkillDiscoveryBeforeRequest(turn *Turn, resume func()) skillDiscoveryGateResult {
	if turn == nil || a.ctxMgr == nil {
		return skillDiscoveryReady
	}
	view := a.skillActivationView()
	history := a.ctxMgr.Snapshot()
	ticket := skillDiscoveryTicket{SessionEpoch: a.sessionEpoch, TaskID: view.TaskID, TurnID: turn.ID, Attempt: turn.Epoch, ContextEpoch: skillActivationEpoch(history, view.TaskID), WorkDirGeneration: a.workDirState.load().Generation}
	guarded := func() {
		if a.sessionEpoch == ticket.SessionEpoch && a.turn == turn && turn.Ctx.Err() == nil && a.workDirState.load().Generation == ticket.WorkDirGeneration {
			resume()
		}
	}
	return a.skillPublisher.ensure(a.parentCtx, ticket, history, view, a.recoveryManager(), a.persist, a.skillDiscoverySend, guarded)
}
func (a *MainAgent) handleSkillDiscoveryPublish(evt skillDiscoveryPublishEvent) {
	if evt.Ticket.SessionEpoch != a.sessionEpoch || a.ctxMgr == nil {
		return
	}
	view := a.skillActivationView()
	if evt.Ticket.TaskID != view.TaskID {
		return
	}
	current := a.turn != nil && a.turn.ID == evt.Ticket.TurnID && a.turn.Epoch == evt.Ticket.Attempt && a.turn.Ctx.Err() == nil && a.workDirState.load().Generation == evt.Ticket.WorkDirGeneration && view.CheckoutRoot == a.skillPublisher.checkoutRoot
	err := a.skillPublisher.handle(evt, a.parentCtx, a.ctxMgr.Snapshot(), view, a.persist, current, func(manager *recovery.RecoveryManager, msg message.Message) bool {
		return a.persistAsyncThrough(manager, "main", msg, nil)
	}, a.ctxMgr.Append, a.skillDiscoverySend)
	if err != nil {
		a.mainLLMRequestInFlight.Store(false)
		if !errors.Is(err, context.Canceled) {
			a.notePersistenceFailure(err)
			a.emitToTUI(ErrorEvent{Err: err})
		}
	}
}
func (s *SubAgent) skillDiscoverySend(evt skillDiscoveryPublishEvent) {
	select {
	case s.skillPublishCh <- evt:
	case <-s.parentCtx.Done():
	}
}
func (s *SubAgent) ensureSkillDiscoveryBeforeRequest(turn *Turn, resume func()) skillDiscoveryGateResult {
	if turn == nil || s.ctxMgr == nil {
		return skillDiscoveryReady
	}
	if s.skillPublishCh == nil {
		s.skillPublishCh = make(chan skillDiscoveryPublishEvent, 1)
	}
	view := s.skillActivationView()
	history := s.ctxMgr.Snapshot()
	ticket := skillDiscoveryTicket{SessionEpoch: s.sessionEpoch, TaskID: view.TaskID, TurnID: turn.ID, Attempt: turn.Epoch, ContextEpoch: skillActivationEpoch(history, view.TaskID), WorkDirGeneration: s.workDirState.load().Generation}
	var manager *recovery.RecoveryManager
	var pump *persistencePump
	if s.parent != nil {
		manager = s.parent.recoveryManagerForEpoch(s.sessionEpoch)
		pump = s.parent.persist
	}
	guarded := func() {
		if s.sessionEpoch == ticket.SessionEpoch && s.turn == turn && turn.Ctx.Err() == nil && s.workDirState.load().Generation == ticket.WorkDirGeneration {
			resume()
		}
	}
	return s.skillPublisher.ensure(s.parentCtx, ticket, history, view, manager, pump, s.skillDiscoverySend, guarded)
}
func (s *SubAgent) handleSkillDiscoveryPublish(evt skillDiscoveryPublishEvent) {
	if evt.Ticket.SessionEpoch != s.sessionEpoch || s.ctxMgr == nil || s.parent == nil {
		return
	}
	view := s.skillActivationView()
	if evt.Ticket.TaskID != view.TaskID {
		return
	}
	current := s.turn != nil && s.turn.ID == evt.Ticket.TurnID && s.turn.Epoch == evt.Ticket.Attempt && s.turn.Ctx.Err() == nil && s.workDirState.load().Generation == evt.Ticket.WorkDirGeneration && view.CheckoutRoot == s.skillPublisher.checkoutRoot
	err := s.skillPublisher.handle(evt, s.parentCtx, s.ctxMgr.Snapshot(), view, s.parent.persist, current, func(manager *recovery.RecoveryManager, msg message.Message) bool {
		return s.parent.persistAsyncThrough(manager, s.instanceID, msg, nil)
	}, s.ctxMgr.Append, s.skillDiscoverySend)
	if err != nil {
		s.llmRequestInFlight.Store(false)
		s.signalWake()
		if !errors.Is(err, context.Canceled) {
			s.notePersistenceFailure(err)
			s.parent.emitToTUI(ErrorEvent{AgentID: s.instanceID, Err: err})
		}
	}
}

// reconcileAfterFlush is called only after the owning ordered pump has fully
// drained, before any snapshot is used to replace/freeze its transcript.
func (p *skillDiscoveryPublisher) reconcileAfterFlush(pump *persistencePump, history []message.Message, appendMessage func(message.Message)) error {
	if p.candidate == nil {
		return nil
	}
	if p.manager == nil || pump == nil {
		return errPersistenceQueueUnavailable
	}
	if err := pump.strictError(p.manager); err != nil {
		return err
	}
	id := p.candidate.SkillDiscovery.ID
	found := false
	for _, msg := range history {
		if msg.SkillDiscovery != nil && msg.SkillDiscovery.ID == id {
			found = true
			break
		}
	}
	if !found {
		appendMessage(*p.candidate)
	}
	p.candidate = nil
	p.waiting = false
	p.resume = nil
	p.ready = false
	p.err = nil
	return nil
}
