package tui

import (
	"testing"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

func TestTakeoverExistingQuestionCanStillBeAnswered(t *testing.T) {
	backend := &questionResolverAgent{accepted: true}
	m := NewModelWithSize(backend, 100, 30)
	m.mode = ModeQuestion
	m.question = questionState{
		request:   &QuestionRequest{Item: tools.QuestionItem{Header: "Choice", Question: "Choose a value"}},
		requestID: "question-1", input: newQuestionTextarea(100),
	}
	m.takeover.sourceFrozen = true
	m.handleNonKeyInputMsg(tea.PasteMsg{Content: "sample answer"})
	runQuestionCmd(&m, m.handleKeyMsg(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})))
	if len(backend.calls) != 1 || len(backend.calls[0].answers) != 1 || backend.calls[0].answers[0] != "sample answer" {
		t.Fatalf("existing answer was blocked: %+v", backend.calls)
	}
	if !m.takeoverInputFrozen() {
		t.Fatal("answer reopened admission")
	}
}

func TestTakeoverQuestionCancelKeysStayBlocked(t *testing.T) {
	for _, key := range []tea.Key{{Code: tea.KeyEscape}, {Code: 'c', Mod: tea.ModCtrl}} {
		backend := &questionResolverAgent{accepted: true}
		m := NewModelWithSize(backend, 100, 30)
		m.mode = ModeQuestion
		m.question = questionState{
			request:   &QuestionRequest{Item: tools.QuestionItem{Header: "Choice", Question: "Choose a value"}},
			requestID: "question-1", input: newQuestionTextarea(100),
		}
		m.takeover.sourceFrozen = true
		m.handleKeyMsg(tea.KeyPressMsg(key))
		if m.question.submitting || m.mode != ModeQuestion || m.question.request == nil {
			t.Fatalf("%s cancelled the question during transfer", tea.KeyPressMsg(key).String())
		}
	}
}

func TestTakeoverStopAndNewInputHaveSeparateAdmission(t *testing.T) {
	m, backend := newTakeoverTestModel()
	m.takeover.sourceFrozen = true
	m.activities["main"] = agent.AgentActivityEvent{AgentID: "main", Type: agent.ActivityStreaming}
	m.handleKeyMsg(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if backend.cancelCalls != 1 {
		t.Fatal("explicit stop was blocked")
	}
	m.handleKeyMsg(tea.KeyPressMsg(tea.Key{Code: 'a', Text: "a"}))
	m.handleNonKeyInputMsg(tea.PasteMsg{Content: "new input"})
	m.handleKeyMsg(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if m.input.Value() != "" || len(backend.sentMessages) != 0 || !m.takeoverInputFrozen() {
		t.Fatal("transfer accepted new work")
	}
	m.handleKeyMsg(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl}))
	if m.quit.by != "ctrl+c" {
		t.Fatal("explicit quit was blocked")
	}
}

func TestTakeoverExistingJobCanStillBeStopped(t *testing.T) {
	m := newJobsTestModel(t, 120, 40)
	id := startTestJob(t, "sleep 60", "sample background job")
	refreshJobs(m)
	m.takeover.sourceFrozen = true
	m.handleKeyMsg(tea.KeyPressMsg(tea.Key{Code: 'j', Mod: tea.ModCtrl}))
	if m.mode != ModeJobsOverlay {
		t.Fatal("jobs list was blocked")
	}
	m.handleKeyMsg(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if m.mode != ModeStopJobConfirm {
		t.Fatal("stop confirmation was blocked")
	}
	m.handleKeyMsg(tea.KeyPressMsg(tea.Key{Code: 'y', Text: "y"}))
	if m.mode == ModeStopJobConfirm {
		t.Fatal("explicit job stop was blocked")
	}
	waitForJobStatus(t, id, "killed")
}

func TestTakeoverRestoredEmptyQueueKeepsIdleStatus(t *testing.T) {
	m, backend := newTakeoverTestModel()
	backend.status = recovery.SessionPauseStatus{State: recovery.SessionPauseReady}
	m.restoreTakeoverPendingInputs()
	if text := m.takeoverStatusText(); text != "" {
		t.Fatalf("empty queue replaced idle status: %q", text)
	}
	if m.takeover.status.State != recovery.SessionPauseReady || len(backend.sentMessages) != 0 {
		t.Fatal("idle presentation resumed execution")
	}
	backend.status.PendingInputCount = 1
	m.restoreTakeoverPendingInputs()
	if text := m.takeoverStatusText(); text == "" {
		t.Fatal("pending input has no pause hint")
	}
}
