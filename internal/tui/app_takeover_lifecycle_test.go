package tui

import (
	"context"
	"errors"
	"testing"
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/recovery"
)

func TestTakeoverObservationAndActivationFreezeComposer(t *testing.T) {
	m, backend := newTakeoverTestModel()
	m = enterTakeoverObservation(t, m)
	for _, phase := range []string{"observing", "activating"} {
		t.Run(phase, func(t *testing.T) {
			if phase == "activating" {
				status := takeoverObserveIdentity("other-session")
				status.State = recovery.SessionPauseReleased
				if m.applyTakeoverObservation(takeoverObserveMsg{sequence: m.takeover.sequence, status: status}) == nil {
					t.Fatal("release did not schedule activation")
				}
			}
			if !m.takeoverInputFrozen() {
				t.Fatal("composer reopened before activation finished")
			}
			m.handleKeyMsg(modelSelectKey("x"))
			m.handleNonKeyInputMsg(tea.PasteMsg{Content: "sample draft"})
			m.Update(attachmentReadyMsg{attachment: Attachment{FileName: "sample.png", MimeType: "image/png", Data: []byte("sample")}})
			m.clipboardAttachmentPending = true
			m.clipboardAttachmentSeq++
			m.Update(clipboardAttachmentReadyMsg{requestID: m.clipboardAttachmentSeq, agentID: m.focusedAgentID, mimeType: "image/png", data: []byte("sample")})
			if m.input.Value() != "" || len(m.attachments) != 0 || m.clipboardAttachmentPending {
				t.Fatal("transfer admitted a new draft or attachment")
			}
		})
	}
	if backend.resumeCount != 0 {
		t.Fatal("activation ran outside its command")
	}
}

func TestTakeoverObservationSerializesQueriesAndActivation(t *testing.T) {
	m, backend := newTakeoverTestModel()
	m = enterTakeoverObservation(t, m)
	m.SetTakeoverStatusHandler(func(_ context.Context, sid, _ string) (recovery.SessionPauseStatus, error) {
		status := takeoverObserveIdentity(sid)
		status.State = recovery.SessionPauseReleased
		return status, nil
	})
	now := time.Now()
	query := m.pollTakeoverObservation(now)
	if query == nil {
		t.Fatal("no status query")
	}
	if m.pollTakeoverObservation(now.Add(takeoverObserveQueryTimeout+time.Second)) != nil {
		t.Fatal("another query was admitted before the first result")
	}
	msg := query()
	_, activation := m.Update(msg)
	if activation == nil || m.takeover.observing || !m.takeover.activating {
		t.Fatal("release did not atomically enter activation")
	}
	if _, duplicate := m.Update(msg); duplicate != nil {
		t.Fatal("duplicate release scheduled another activation")
	}
	if m.pollTakeoverObservation(now.Add(time.Minute)) != nil {
		t.Fatal("activation kept polling")
	}
	result := activation()
	// The runtime emits this before returning its activation receipt.
	m.handleAgentEvent(agentEventMsg{event: agent.SessionSwitchStartedEvent{Kind: "resume", SessionID: "other-session"}})
	if m.takeover.ctx.Err() != nil {
		t.Fatal("activation cancelled its own restore")
	}
	m.Update(result)
	if backend.resumeCount != 1 || m.takeoverInputFrozen() {
		t.Fatal("activation did not finish exactly once")
	}
}

func TestTakeoverSessionSwitchInvalidatesPendingCommands(t *testing.T) {
	for _, phase := range []string{"waiting", "observing", "activating"} {
		t.Run(phase, func(t *testing.T) {
			m, backend := newTakeoverTestModel()
			var pending tea.Cmd
			if phase == "waiting" {
				m.SetTakeoverHandler(func(context.Context, string, *time.Duration) (recovery.SessionPauseStatus, error) {
					t.Error("cancelled wait contacted the source")
					return recovery.SessionPauseStatus{}, nil
				})
				pending = m.startTakeoverCommand("/takeover other-session")
			} else {
				m = enterTakeoverObservation(t, m)
				m.SetTakeoverStatusHandler(func(context.Context, string, string) (recovery.SessionPauseStatus, error) {
					t.Error("cancelled query contacted the source")
					return recovery.SessionPauseStatus{}, nil
				})
				if phase == "observing" {
					pending = m.pollTakeoverObservation(time.Now())
				} else {
					status := takeoverObserveIdentity("other-session")
					status.State = recovery.SessionPauseReleased
					pending = m.applyTakeoverObservation(takeoverObserveMsg{sequence: m.takeover.sequence, status: status})
				}
			}
			ctx, seq := m.takeover.ctx, m.takeover.sequence
			m.handleAgentEvent(agentEventMsg{event: agent.SessionSwitchStartedEvent{Kind: "new"}})
			if !errors.Is(ctx.Err(), context.Canceled) || m.takeover.sequence == seq || m.takeoverInputFrozen() {
				t.Fatal("session switch did not invalidate the transfer")
			}
			m.Update(pending())
			status := takeoverObserveIdentity("other-session")
			status.State = recovery.SessionPauseReleased
			if m.applyTakeoverObservation(takeoverObserveMsg{sequence: seq, status: status}) != nil {
				t.Fatal("old release activated after the switch")
			}
			if backend.resumeCount != 0 {
				t.Fatal("cancelled command resumed a different session")
			}
		})
	}
}

func TestTakeoverSessionSwitchCancelsRunningObservation(t *testing.T) {
	m, _ := newTakeoverTestModel()
	m = enterTakeoverObservation(t, m)
	started := make(chan struct{})
	result := make(chan tea.Msg, 1)
	m.SetTakeoverStatusHandler(func(ctx context.Context, _, _ string) (recovery.SessionPauseStatus, error) {
		close(started)
		<-ctx.Done()
		return recovery.SessionPauseStatus{}, ctx.Err()
	})
	query := m.pollTakeoverObservation(time.Now())
	go func() { result <- query() }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("status query did not start")
	}
	m.handleAgentEvent(agentEventMsg{event: agent.SessionSwitchStartedEvent{Kind: "resume", SessionID: "another-session"}})
	select {
	case msg := <-result:
		m.Update(msg)
	case <-time.After(time.Second):
		t.Fatal("status query did not stop on session switch")
	}
	if m.takeover.observing || m.takeover.waiting {
		t.Fatal("cancelled result reopened the transfer")
	}
}

func TestTakeoverCloseCancelsPendingActivation(t *testing.T) {
	m, backend := newTakeoverTestModel()
	m = enterTakeoverObservation(t, m)
	status := takeoverObserveIdentity("other-session")
	status.State = recovery.SessionPauseReleased
	activation := m.applyTakeoverObservation(takeoverObserveMsg{sequence: m.takeover.sequence, status: status})
	ctx := m.takeover.ctx
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	result := activation().(takeoverResultMsg)
	if !errors.Is(ctx.Err(), context.Canceled) || !errors.Is(result.err, context.Canceled) || backend.resumeCount != 0 {
		t.Fatal("closed UI allowed a pending activation")
	}
}
