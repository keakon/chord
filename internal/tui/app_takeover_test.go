package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

type takeoverAgentStub struct {
	*sessionControlAgent
	status            recovery.SessionPauseStatus
	pending           []recovery.PendingInput
	resumeErr         error
	resumeIdentity    recovery.TransferIdentity
	resumeCount       int
	preparerInstalled bool
	mirrorRejected    bool
	queuedOwners      []string
}

func (s *takeoverAgentStub) UseSessionPauseEventPreparer()                          { s.preparerInstalled = true }
func (s *takeoverAgentStub) CurrentSessionPauseStatus() recovery.SessionPauseStatus { return s.status }
func (s *takeoverAgentStub) PendingSessionInputs() []recovery.PendingInput          { return s.pending }
func (s *takeoverAgentStub) ResumePausedSession(_ context.Context, sid string, id recovery.TransferIdentity) error {
	s.resumeCount++
	s.resumeIdentity = id
	if s.resumeErr != nil {
		return s.resumeErr
	}
	s.sessionSummary = &agent.SessionSummary{ID: sid}
	s.status = recovery.SessionPauseStatus{TransferIdentity: id, State: recovery.SessionPauseReady}
	return nil
}
func (s *takeoverAgentStub) QueueSubAgentDraft(id string, parts []message.ContentPart) bool {
	if s.mirrorRejected {
		return false
	}
	s.queuedOwners = append(s.queuedOwners, s.focused)
	return s.sessionControlAgent.QueueSubAgentDraft(id, parts)
}

func newTakeoverTestModel() (Model, *takeoverAgentStub) {
	backend := &takeoverAgentStub{sessionControlAgent: &sessionControlAgent{sessionSummary: &agent.SessionSummary{ID: "source-session"}}}
	return NewModelWithSize(backend, 120, 30), backend
}

func TestTakeoverPreparationMirrorsSubmittedInputsBeforeACK(t *testing.T) {
	m, backend := newTakeoverTestModel()
	if !backend.preparerInstalled {
		t.Fatal("source preparer not installed")
	}
	m.queuedDrafts = []queuedDraft{{ID: "draft-1", Content: "queued constraint"}, {ID: "draft-2", Content: "already accepted", Mirrored: true}}
	reply := make(chan error, 1)
	event := agent.SessionTransferPreparingEvent{Identity: recovery.TransferIdentity{SessionID: "source-session", TransferID: "transfer-1"}, Reply: reply}
	cmd := m.prepareSourceTakeover(event)
	if !m.takeoverInputFrozen() {
		t.Fatal("source input was not frozen before mirroring")
	}
	select {
	case <-reply:
		t.Fatal("ACK arrived before mirroring")
	default:
	}
	result := cmd().(takeoverPreparedMsg)
	if len(backend.queuedDraftIDs) != 1 || backend.queuedDraftIDs[0] != "draft-1" {
		t.Fatalf("mirrored = %v", backend.queuedDraftIDs)
	}
	select {
	case <-reply:
		t.Fatal("ACK arrived before UI applied projection")
	default:
	}
	m.Update(result)
	if err := <-reply; err != nil {
		t.Fatal(err)
	}
	if !m.queuedDrafts[0].Mirrored || backend.focused != "" {
		t.Fatal("mirror ownership was not restored")
	}
}

func TestTakeoverPreparationRejectsUnsubmittedDraftWithoutLosingIt(t *testing.T) {
	m, _ := newTakeoverTestModel()
	m.input.SetValue("unsubmitted draft")
	reply := make(chan error, 1)
	m.prepareSourceTakeover(agent.SessionTransferPreparingEvent{Identity: recovery.TransferIdentity{SessionID: "source-session"}, Reply: reply})
	if err := <-reply; err == nil {
		t.Fatal("draft should reject preparation")
	}
	if m.input.Value() != "unsubmitted draft" || m.takeoverInputFrozen() {
		t.Fatal("draft was lost or input stayed frozen")
	}
}

func TestTakeoverPreparationMirrorFailureKeepsQueue(t *testing.T) {
	m, backend := newTakeoverTestModel()
	backend.mirrorRejected = true
	m.queuedDrafts = []queuedDraft{{ID: "draft-1", Content: "queued constraint"}}
	reply := make(chan error, 1)
	cmd := m.prepareSourceTakeover(agent.SessionTransferPreparingEvent{Identity: recovery.TransferIdentity{SessionID: "source-session"}, Reply: reply})
	m.Update(cmd())
	if err := <-reply; err == nil {
		t.Fatal("missing rejection")
	}
	if len(m.queuedDrafts) != 1 || m.queuedDrafts[0].Mirrored || m.takeoverInputFrozen() {
		t.Fatal("failed mirror changed queue or froze input")
	}
}

func TestTakeoverCommandWaitsForValidatedActivation(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "restore failure"}[fail], func(t *testing.T) {
			m, backend := newTakeoverTestModel()
			id := recovery.TransferIdentity{SessionID: "target-session", TransferID: "transfer-1"}
			if fail {
				backend.resumeErr = errors.New("lock busy")
			}
			m.SetTakeoverHandler(func(_ context.Context, sid string, timeout *time.Duration) (recovery.SessionPauseStatus, error) {
				if sid != "target-session" || timeout == nil || *timeout != 2*time.Second {
					t.Fatal("command arguments changed")
				}
				return recovery.SessionPauseStatus{TransferIdentity: id, State: recovery.SessionPauseReleased}, nil
			})
			m.input.SetValue("/takeover target-session --timeout 2s")
			cmd := m.startTakeoverCommand(m.input.Value())
			if cmd == nil || !m.takeover.waiting {
				t.Fatal("takeover did not start")
			}
			m.handleKeyMsg(modelSelectKey("a"))
			m.handleNonKeyInputMsg(tea.PasteMsg{Content: "late input"})
			if m.input.Value() != "" {
				t.Fatal("waiting admitted new input")
			}
			_, activation := m.Update(cmd())
			if activation == nil || !m.takeover.activating || !m.takeoverInputFrozen() || backend.resumeCount != 0 {
				t.Fatal("wait result did not schedule a frozen activation")
			}
			m.Update(activation())
			if backend.resumeCount != 1 || backend.resumeIdentity != id || m.takeover.waiting {
				t.Fatal("activation receipt was not applied")
			}
			want := "target-session"
			if fail {
				want = "source-session"
			}
			if m.currentSessionID() != want {
				t.Fatalf("session=%s, want=%s", m.currentSessionID(), want)
			}
		})
	}
}

func TestTakeoverCommandDoesNotActivateUnreleasedSource(t *testing.T) {
	m, backend := newTakeoverTestModel()
	m.SetTakeoverHandler(func(context.Context, string, *time.Duration) (recovery.SessionPauseStatus, error) {
		return recovery.SessionPauseStatus{State: recovery.SessionPauseDraining}, nil
	})
	m.Update(m.startTakeoverCommand("/takeover target-session")())
	if backend.resumeCount != 0 || m.currentSessionID() != "source-session" {
		t.Fatal("unreleased source was activated")
	}
}

func TestTakeoverZeroTimeoutStartsWithoutActivating(t *testing.T) {
	m, backend := newTakeoverTestModel()
	called := false
	m.SetTakeoverHandler(func(_ context.Context, sid string, timeout *time.Duration) (recovery.SessionPauseStatus, error) {
		called = true
		if timeout == nil || *timeout != 0 {
			t.Fatal("zero observation timeout was not preserved")
		}
		return recovery.SessionPauseStatus{SessionID: sid, TransferID: "transfer-1", State: recovery.SessionPauseDraining}, nil
	})
	m.Update(m.startTakeoverCommand("/takeover target-session --timeout 0s")())
	if !called || backend.resumeCount != 0 {
		t.Fatal("zero timeout was rejected or activated the target")
	}
}

func TestTakeoverFailurePreservesQueryableIdentity(t *testing.T) {
	status := recovery.SessionPauseStatus{TransferID: "transfer-1", SessionID: "target-session"}
	text := takeoverFailureMessage(status, errors.New("source observation unavailable"))
	if !strings.Contains(text, status.TransferID) || !strings.Contains(text, status.SessionID) {
		t.Fatal("failure hid the transfer identity")
	}
}

func TestTakeoverRestoredInputProjectionNeverResubmitsOnIdle(t *testing.T) {
	m, backend := newTakeoverTestModel()
	backend.status = recovery.SessionPauseStatus{State: recovery.SessionPauseReady, PendingInputCount: 1}
	backend.pending = []recovery.PendingInput{{InputID: "input-1", OwnerTaskID: "main", Content: "pending constraint"}}
	m.restoreTakeoverPendingInputs()
	m.restoreTakeoverPendingInputs()
	if len(m.queuedDrafts) != 1 || m.queuedDrafts[0].ID != "restored:input-1" || !m.queuedDrafts[0].Mirrored {
		t.Fatal("durable projection duplicated or became a new input")
	}
	m.handleAgentEvent(agentEventMsg{event: agent.IdleEvent{}})
	m.drainQueuedDrafts()
	m.editQueuedDraftAt(0)
	m.deleteQueuedDraftAt(0)
	if len(backend.sentMessages) > 0 || len(backend.queuedDraftIDs) > 0 || len(m.queuedDrafts) != 1 {
		t.Fatal("projection changed durable input on idle/edit")
	}
}

func TestTakeoverSavedExitsWithoutCancellation(t *testing.T) {
	m, backend := newTakeoverTestModel()
	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionTransferEvent{Status: recovery.SessionPauseStatus{TransferIdentity: recovery.TransferIdentity{SessionID: "source-session"}, State: recovery.SessionPauseSaved}}})
	found := false
	for _, msg := range runCmdTree(cmd) {
		if _, ok := msg.(tea.QuitMsg); ok {
			found = true
		}
	}
	if !found || backend.cancelCalls != 0 {
		t.Fatal("saved transfer did not exit independently of cancellation")
	}
}

func TestTakeoverSaveFailureShowsAvailableRecoveryAction(t *testing.T) {
	for _, action := range []string{recovery.SessionPauseActionRetrySave, recovery.SessionPauseActionManualRecovery, ""} {
		t.Run(action, func(t *testing.T) {
			m, backend := newTakeoverTestModel()
			m.handleAgentEvent(agentEventMsg{event: agent.SessionTransferEvent{Status: recovery.SessionPauseStatus{
				TransferIdentity: recovery.TransferIdentity{SessionID: "source-session"},
				State:            recovery.SessionPauseSaveFailed, RecoverableAction: action,
			}}})
			if !m.takeoverInputFrozen() || backend.cancelCalls != 0 || m.activeToast == nil {
				t.Fatal("save failure did not preserve frozen ownership and show recovery guidance")
			}
			if got := strings.Contains(m.activeToast.Message, "retry-save"); got != (action == recovery.SessionPauseActionRetrySave) {
				t.Fatalf("incorrect recovery guidance: %q", m.activeToast.Message)
			}
		})
	}
}

func TestTakeoverDurableRetirementRemovesProjection(t *testing.T) {
	m, backend := newTakeoverTestModel()
	backend.status = recovery.SessionPauseStatus{State: recovery.SessionPauseReady, PendingInputCount: 2}
	backend.pending = []recovery.PendingInput{{InputID: "input-1", OwnerTaskID: "main", Content: "first"}, {InputID: "input-2", OwnerTaskID: "main", Content: "second"}}
	m.restoreTakeoverPendingInputs()
	m.handleAgentEvent(agentEventMsg{event: agent.SessionPendingInputsUpdatedEvent{InputIDs: []string{"input-1"}}})
	if len(m.queuedDrafts) != 1 || m.queuedDrafts[0].ID != "restored:input-2" || m.takeover.status.PendingInputCount != 1 {
		t.Fatal("durable retirement did not reconcile projection")
	}
}
