package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

func TestSkillPublisherMissingManagerFailsClosed(t *testing.T) {
	v := skillActivationFixture(t, 1)
	history := activationReadHistory(v)
	events := make(chan skillDiscoveryPublishEvent, 1)
	p := skillDiscoveryPublisher{}
	result := p.ensure(context.Background(), skillDiscoveryTicket{TaskID: v.TaskID}, history, v, nil, nil, func(e skillDiscoveryPublishEvent) { events <- e }, func() { t.Error("unexpected request") })
	if result != skillDiscoveryWaiting {
		t.Fatal("did not wait")
	}
	e := <-events
	if e.Err == nil {
		t.Fatal("missing durable manager accepted")
	}
	p.handle(e, context.Background(), history, v, nil, true, nil, nil, nil)
	if p.err == nil || p.ready {
		t.Fatal("missing manager did not block")
	}
}
func TestSkillPublisherCommitBeforeContextAndCancelledReconcile(t *testing.T) {
	v := skillActivationFixture(t, 1)
	history := activationReadHistory(v)
	p := skillDiscoveryPublisher{ticket: skillDiscoveryTicket{TaskID: v.TaskID}, waiting: true, manager: new(recovery.RecoveryManager)}
	pump := newPersistencePump(4)
	pump.start(func(e persistEntry) {
		if e.barrier != nil {
			close(e.barrier)
		}
	})
	defer pump.closeUntil(nil)
	events := make(chan skillDiscoveryPublishEvent, 1)
	enqueued := 0
	resumed := false
	p.resume = func() { resumed = true }
	p.handle(skillDiscoveryPublishEvent{Ticket: p.ticket}, context.Background(), history, v, pump, true, func(_ *recovery.RecoveryManager, msg message.Message) bool {
		enqueued++
		if msg.SkillDiscovery == nil {
			t.Fatal("no notice")
		}
		return true
	}, func(message.Message) { t.Error("appended before acknowledgment") }, func(e skillDiscoveryPublishEvent) { events <- e })
	if enqueued != 1 || p.candidate == nil {
		t.Fatal("candidate not frozen")
	}
	e := <-events
	p.handle(e, context.Background(), history, v, pump, false, nil, func(m message.Message) { history = append(history, m) }, nil)
	if len(history) != 3 || resumed || p.candidate != nil {
		t.Fatal("cancelled request lost durable message or resumed")
	}
	p.handle(e, context.Background(), history, v, pump, false, nil, func(message.Message) { t.Fatal("duplicate acknowledgment appended twice") }, nil)
}
func TestSkillPublisherFailedNoticeRetainsExactCandidate(t *testing.T) {
	v := skillActivationFixture(t, 1)
	history := activationReadHistory(v)
	notice := buildSkillDiscoveryMessage(history, v)
	p := skillDiscoveryPublisher{candidate: notice, waiting: true, ticket: skillDiscoveryTicket{Sequence: 1}}
	err := errors.New("write failed")
	p.handle(skillDiscoveryPublishEvent{Ticket: p.ticket, NoticeID: notice.SkillDiscovery.ID, Err: err}, context.Background(), history, v, nil, true, nil, nil, nil)
	if p.candidate != notice || p.err != err || p.ready {
		t.Fatal("failure discarded original candidate")
	}
}

func TestSkillPublisherReconcileFailureStopsHistoryReplacement(t *testing.T) {
	v := skillActivationFixture(t, 1)
	history := activationReadHistory(v)
	manager := new(recovery.RecoveryManager)
	pump := newPersistencePump(1)
	writeErr := errors.New("uncertain write")
	pump.rememberFailure(persistEntry{recovery: manager}, writeErr)
	p := skillDiscoveryPublisher{manager: manager, candidate: buildSkillDiscoveryMessage(history, v)}
	err := p.reconcileAfterFlush(pump, history, func(message.Message) { t.Fatal("failed write entered canonical history") })
	if !errors.Is(err, writeErr) || p.candidate == nil {
		t.Fatal("history replacement did not preserve blocked candidate")
	}
}

func TestSkillPublisherExplicitMemoryOnlyAdapter(t *testing.T) {
	v := skillActivationFixture(t, 1)
	history := activationReadHistory(v)
	var got message.Message
	p := newSkillDiscoveryPublisher(skillDiscoveryPublisherOptions{MemoryOnlyAppend: func(m message.Message) { got = m }})
	if p.ensure(context.Background(), skillDiscoveryTicket{TaskID: v.TaskID}, history, v, nil, nil, nil, nil) != skillDiscoveryReady {
		t.Fatal("memory adapter blocked")
	}
	if got.SkillDiscovery == nil {
		t.Fatal("memory adapter did not append notice")
	}
}

func TestSkillPublisherRejectedAdmissionReturnsErrorWithoutCommittedCandidate(t *testing.T) {
	v := skillActivationFixture(t, 1)
	history := activationReadHistory(v)
	p := skillDiscoveryPublisher{ticket: skillDiscoveryTicket{TaskID: v.TaskID}, waiting: true}
	err := p.handle(skillDiscoveryPublishEvent{Ticket: p.ticket}, context.Background(), history, v, nil, true, func(*recovery.RecoveryManager, message.Message) bool { return false }, func(message.Message) { t.Fatal("unaccepted notice entered history") }, nil)
	if !errors.Is(err, errPersistenceQueueUnavailable) || p.err == nil || p.waiting || p.ready {
		t.Fatal("admission rejection did not expose blocked result")
	}
	if p.candidate != nil {
		t.Fatal("unaccepted candidate can be mistaken for an acknowledged write")
	}
	if err := p.reconcileAfterFlush(nil, history, func(message.Message) { t.Fatal("flush certified an unaccepted notice") }); err != nil {
		t.Fatal(err)
	}
}

func TestSkillPublisherEpochChangeReentersGateWithoutReadyPass(t *testing.T) {
	v := skillActivationFixture(t, 1)
	history := activationReadHistory(v)
	notice := buildSkillDiscoveryMessage(history, v)
	ticket := skillDiscoveryTicket{TaskID: v.TaskID, ContextEpoch: 0}
	history = []message.Message{{Role: message.RoleUser, IsCompactionSummary: true, CompactionSkillActivation: buildSkillActivationCarry(history, v, 1)}}
	p := skillDiscoveryPublisher{ticket: ticket, waiting: true, candidate: notice}
	resumed := false
	p.resume = func() {
		resumed = true
		if p.ready {
			t.Fatal("old epoch granted a ready pass")
		}
	}
	err := p.handle(skillDiscoveryPublishEvent{Ticket: ticket, NoticeID: notice.SkillDiscovery.ID}, context.Background(), history, v, nil, true, nil, func(m message.Message) { history = append(history, m) }, nil)
	if err != nil || !resumed || p.ready || p.candidate != nil || len(history) != 2 {
		t.Fatal("epoch change lost reconciliation or failed to re-enter request gate")
	}
}

func TestSkillPublisherOwnerFailureReleasesFlightAndReportsError(t *testing.T) {
	t.Parallel()
	t.Run("main", func(t *testing.T) {
		a := newReadyTestMainAgent(t)
		ticket := skillDiscoveryTicket{SessionEpoch: a.sessionEpoch, TaskID: a.skillActivationView().TaskID}
		a.skillPublisher = skillDiscoveryPublisher{ticket: ticket, waiting: true}
		a.mainLLMRequestInFlight.Store(true)
		failure := errors.New("sample persistence failure")
		a.handleSkillDiscoveryPublish(skillDiscoveryPublishEvent{Ticket: ticket, Err: failure})
		if a.mainLLMRequestInFlight.Load() {
			t.Fatal("failed publisher left request in flight")
		}
		found := false
		for _, evt := range drainAgentEvents(a.outputCh) {
			if e, ok := evt.(ErrorEvent); ok && errors.Is(e.Err, failure) {
				found = true
			}
		}
		if !found {
			t.Fatal("failed publisher did not report an error")
		}
	})
	t.Run("worker", func(t *testing.T) {
		parent, s := newMixedBatchTestSubAgent(t)
		ticket := skillDiscoveryTicket{SessionEpoch: s.sessionEpoch, TaskID: s.taskID}
		s.skillPublisher = skillDiscoveryPublisher{ticket: ticket, waiting: true}
		s.llmRequestInFlight.Store(true)
		failure := errors.New("sample worker persistence failure")
		s.handleSkillDiscoveryPublish(skillDiscoveryPublishEvent{Ticket: ticket, Err: failure})
		if s.llmRequestInFlight.Load() {
			t.Fatal("failed publisher left worker request in flight")
		}
		found := false
		for _, evt := range drainAgentEvents(parent.outputCh) {
			if e, ok := evt.(ErrorEvent); ok && e.AgentID == s.instanceID && errors.Is(e.Err, failure) {
				found = true
			}
		}
		if !found {
			t.Fatal("failed publisher did not report a worker error")
		}
	})
}

func TestSkillPublisherRecoveryResumesNewTurnAfterOriginalAcknowledgment(t *testing.T) {
	for _, written := range []bool{false, true} {
		name := "results"
		if written {
			name = "notice"
		}
		t.Run(name, func(t *testing.T) {
			v := skillActivationFixture(t, 1)
			history := activationReadHistory(v)
			original := skillDiscoveryTicket{TaskID: v.TaskID, TurnID: 1, Sequence: 1}
			next := skillDiscoveryTicket{TaskID: v.TaskID, TurnID: 2}
			manager := new(recovery.RecoveryManager)
			p := skillDiscoveryPublisher{ticket: original, manager: manager, err: errors.New("previous write failed")}
			if written {
				p.candidate = buildSkillDiscoveryMessage(history, v)
			}
			candidate := p.candidate
			pump := newPersistencePump(4)
			pump.start(func(entry persistEntry) {
				if entry.barrier != nil {
					close(entry.barrier)
				}
			})
			defer pump.closeUntil(nil)
			events := make(chan skillDiscoveryPublishEvent, 1)
			resumed := false
			result := p.ensure(context.Background(), next, history, v, manager, pump, func(e skillDiscoveryPublishEvent) { events <- e }, func() {
				resumed = true
				if p.ready {
					t.Fatal("new turn inherited the old request's ready pass")
				}
			})
			if result != skillDiscoveryWaiting || p.ticket != original || p.candidate != candidate {
				t.Fatal("recovery replaced original write ownership")
			}
			evt := <-events
			if evt.Ticket != original {
				t.Fatal("recovery acknowledged a replacement ticket")
			}
			err := p.handle(evt, context.Background(), history, v, pump, false, func(*recovery.RecoveryManager, message.Message) bool {
				t.Fatal("recovery enqueued original notice twice")
				return false
			}, func(m message.Message) { history = append(history, m) }, nil)
			if err != nil || !resumed || p.pendingResume != nil || p.candidate != nil {
				t.Fatal("new request continuation lost after original acknowledgment")
			}
			want := 2
			if written {
				want++
			}
			if len(history) != want {
				t.Fatal("recovery did not reconcile precisely the original notice")
			}
		})
	}
}
