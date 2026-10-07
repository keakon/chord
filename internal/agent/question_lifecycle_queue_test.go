package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

func awaitQuestionSessionSwitch(t *testing.T, a *MainAgent, previous string) {
	t.Helper()
	timeout := time.After(3 * time.Second)
	for a.SessionDir() == previous {
		select {
		case evt := <-a.eventCh:
			switch evt.Type {
			case EventQuestionCommitted:
				a.handleQuestionCommitted(evt.Payload.(*questionCommit))
			case EventQuestionCommitRetry:
				a.retryQuestionCommit(evt.Payload.(*questionCommit))
			}
		case <-timeout:
			t.Fatalf("session switch did not finish: active=%v queued=%d paused=%v switching=%v", a.questions.active != nil, len(a.questions.queue), a.questions.paused, a.questions.switching)
		}
	}
	if a.questions.switching || a.questions.active != nil || len(a.questions.queue) != 0 || len(a.questions.retryTimers) != 0 {
		t.Fatalf("session switch retained pending question work: active=%v queued=%d retries=%d switching=%v paused=%v", a.questions.active != nil, len(a.questions.queue), len(a.questions.retryTimers), a.questions.switching, a.questions.paused)
	}
}

func TestQuestionSessionSwitchCompletesAfterPersistencePressure(t *testing.T) {
	for _, pressure := range []string{"queue", "admission"} {
		t.Run(pressure, func(t *testing.T) {
			a := newQuestionTestAgent(t)
			a.markAgentsMDReady()
			a.MarkSkillsReady()
			a.markMCPReady()
			createTestQuestions(t, a, questionItems("Choice"), false)
			previous := a.SessionDir()
			original := a.persist
			blocked := newPersistencePump(1)
			a.persist = blocked
			defer func() { a.persist = original; a.stopQuestionWork() }()
			if pressure == "queue" {
				blocked.ch <- persistEntry{}
			} else {
				blocked.admission <- struct{}{}
			}
			if !a.prepareQuestionSessionSwitch(Event{Type: EventSessionControl, Payload: &sessionControlPayload{Kind: sessionControlNew}}) {
				t.Fatal("session switch was not deferred")
			}
			active := a.questions.active
			a.pauseQuestionWork()
			if active == nil || a.questions.active != active || len(a.questions.queue) != 0 {
				t.Fatal("duplicate pause created another operation")
			}
			if pressure == "queue" {
				<-blocked.ch
			} else {
				<-blocked.admission
			}
			a.persist = original
			awaitQuestionSessionSwitch(t, a, previous)
		})
	}
}

func TestQuestionSessionSwitchCompletesWithFullOperationQueue(t *testing.T) {
	a := newQuestionTestAgent(t)
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()
	id := createTestQuestions(t, a, questionItems("Choice"), false).Result.QuestionIDs[0]
	previous := a.SessionDir()
	original := a.persist
	a.persist = newPersistencePump(1)
	defer func() { a.persist = original; a.stopQuestionWork() }()
	a.handleQuestionCommand(&questionCommand{owner: identity.MainAgentID, operation: QuestionOperation{Operation: QuestionOpAnswer, OperationID: "answer", QuestionID: id, Answers: []string{"yes"}}})
	if a.questions.active == nil {
		t.Fatal("answer did not start a durable commit")
	}
	for i := range maxQuestionOperations {
		a.handleQuestionCommand(&questionCommand{ctx: context.Background(), operation: QuestionOperation{Operation: QuestionOpInteract, OperationID: fmt.Sprintf("interact-%d", i), QuestionID: id}})
	}
	busy := &questionCommand{operation: QuestionOperation{Operation: QuestionOpInteract, QuestionID: id}, reply: make(chan QuestionReceipt, 1)}
	a.handleQuestionCommand(busy)
	if receipt := <-busy.reply; receipt.Accepted || receipt.Error == "" {
		t.Fatal("full operation queue admitted another user operation")
	}
	if !a.prepareQuestionSessionSwitch(Event{Type: EventSessionControl, Payload: &sessionControlPayload{Kind: sessionControlNew}}) {
		t.Fatal("session switch was not deferred")
	}
	a.pauseQuestionWork()
	if len(a.questions.queue) != maxQuestionOperations+1 || a.questions.queue[maxQuestionOperations].operation.Operation != questionOpPause {
		t.Fatal("internal pause did not retain exactly one reserved slot")
	}
	entry := <-a.persist.ch
	a.persist = original
	entry.after(entry.recovery.PersistMessageDurable(entry.agentID, entry.msg))
	awaitQuestionSessionSwitch(t, a, previous)
	if len(a.questions.records) != 0 {
		t.Fatal("new session retained the previous session's questions")
	}
	if receipt := createTestQuestions(t, a, questionItems("New choice"), false); !receipt.Accepted {
		t.Fatalf("new session cannot create questions: %+v", receipt)
	}
}

func TestQuestionFailedSessionSwitchReopensOperations(t *testing.T) {
	a := newQuestionTestAgent(t)
	id := createTestQuestions(t, a, questionItems("Choice"), false).Result.QuestionIDs[0]
	previous := a.SessionDir()
	if !a.prepareQuestionSessionSwitch(Event{Type: EventSessionControl, Payload: &sessionControlPayload{Kind: sessionControlFork, MsgIndex: -1}}) {
		t.Fatal("session switch was not deferred")
	}
	timeout := time.After(3 * time.Second)
	for a.questions.switching {
		select {
		case evt := <-a.eventCh:
			if evt.Type == EventQuestionCommitted {
				a.handleQuestionCommitted(evt.Payload.(*questionCommit))
			}
		case <-timeout:
			t.Fatal("failed session switch retained the operation gate")
		}
	}
	if a.SessionDir() != previous {
		t.Fatal("failed fork changed the session")
	}
	if receipt := applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpWithdraw, QuestionID: id, UserText: "No longer required"}, time.Time{}); !receipt.Accepted {
		t.Fatalf("failed session switch blocked later operations: %+v", receipt)
	}
}

func TestQuestionForkRebuildsCopiedFacts(t *testing.T) {
	a := newQuestionTestAgent(t)
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "Original request"})
	id := createTestQuestions(t, a, questionItems("Choice"), false).Result.QuestionIDs[0]
	applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpAnswer, QuestionID: id, Answers: []string{"yes"}}, time.Time{})
	applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpRevise, QuestionID: id, Answers: []string{"no"}}, time.Time{})
	draftIndex := a.ctxMgr.MessageCount()
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "Follow-up request"})
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, Content: "Follow-up answer"})
	previous := a.SessionDir()
	binding := a.questions.binding
	if !a.prepareQuestionSessionSwitch(Event{Type: EventSessionControl, Payload: &sessionControlPayload{Kind: sessionControlFork, MsgIndex: draftIndex}}) {
		t.Fatal("fork was not deferred")
	}
	awaitQuestionSessionSwitch(t, a, previous)
	if a.questions.binding == binding || a.questions.records[id].ID != id {
		t.Fatal("fork did not rebuild its own question binding")
	}
	snapshots := runQuestionCommand(t, a, &questionCommand{operation: QuestionOperation{Operation: questionOpSnapshot}}).Questions
	if len(snapshots) != 1 || snapshots[0].Revision == nil || snapshots[0].Revision.SelectedIDs[0] != "no" {
		t.Fatalf("fork lost copied revision: %+v", snapshots)
	}
	found := false
	for _, event := range drainAgentEvents(a.outputCh) {
		if state, ok := event.(QuestionStateEvent); ok && state.Restored && state.Question.ID == id {
			found = state.Question.Revision != nil && state.Question.Revision.SelectedIDs[0] == "no"
		}
	}
	if !found {
		t.Fatal("fork did not publish the rebuilt revision")
	}
}

func TestQuestionSessionSwitchAfterUncertainCommit(t *testing.T) {
	for _, deferred := range []bool{false, true} {
		for _, kind := range []string{sessionControlNew, sessionControlResumeID, sessionControlFork} {
			t.Run(fmt.Sprintf("%s/deferred=%v", kind, deferred), func(t *testing.T) {
				a := newQuestionTestAgent(t)
				a.markAgentsMDReady()
				a.MarkSkillsReady()
				a.markMCPReady()
				id := createTestQuestions(t, a, questionItems("Choice"), false).Result.QuestionIDs[0]
				previous := a.SessionDir()
				sessionsDir, err := a.projectSessionsDir()
				if err != nil {
					t.Fatal(err)
				}
				targetDir := filepath.Join(sessionsDir, "target-session")
				target := recovery.NewRecoveryManager(targetDir)
				if err := target.PersistMessageDurable(identity.MainAgentID, message.Message{Role: message.RoleUser, Content: "Target request"}); err != nil {
					t.Fatal(err)
				}
				target.Close()
				evt := Event{Type: EventSessionControl, Payload: &sessionControlPayload{Kind: kind, SessionID: "target-session", MsgIndex: -1}}

				original := a.persist
				a.persist = newPersistencePump(1)
				c := &questionCommand{owner: identity.MainAgentID, operation: QuestionOperation{Operation: QuestionOpAnswer, OperationID: "answer", QuestionID: id, Answers: []string{"yes"}}, reply: make(chan QuestionReceipt, 1)}
				a.handleQuestionCommand(c)
				if deferred && !a.prepareQuestionSessionSwitch(evt) {
					t.Fatal("switch was not deferred")
				}
				entry := <-a.persist.ch
				a.persist = original
				// Simulate a write that reached disk but whose durability acknowledgement failed.
				if err := entry.recovery.PersistMessageDurable(entry.agentID, entry.msg); err != nil {
					t.Fatal(err)
				}
				entry.after(fmt.Errorf("durability acknowledgement unavailable"))
				select {
				case commitEvent := <-a.eventCh:
					a.handleQuestionCommitted(commitEvent.Payload.(*questionCommit))
				case <-time.After(3 * time.Second):
					t.Fatal("missing commit acknowledgement")
				}
				if receipt := <-c.reply; receipt.Accepted || receipt.Error == "" {
					t.Fatalf("uncertain write reported success: %+v", receipt)
				}
				if !deferred {
					a.dispatch(evt)
				}
				if a.questions.switching || a.questions.switchEvent != nil || a.questions.active != nil || len(a.questions.queue) != 0 {
					t.Fatal("switch barrier retained unfinished work")
				}
				if kind == sessionControlFork {
					if a.SessionDir() != previous || !a.questions.failed || !a.questions.records[id].pending() {
						t.Fatal("fork used uncertain in-memory facts")
					}
					a.dispatch(Event{Type: EventSessionControl, Payload: &sessionControlPayload{Kind: sessionControlNew}})
				}
				if a.SessionDir() == previous || a.questions.failed {
					t.Fatal("navigation did not retire the failed runtime")
				}
				// Navigation must not checkpoint the incomplete runtime over the uncertain durable fact.
				loaded, err := a.loadSessionState(previous)
				if err != nil {
					t.Fatal(err)
				}
				a.restoreQuestions(loaded.Messages)
				if a.questions.records[id].Outcome != tools.QuestionOutcomeAnswered {
					t.Fatal("navigation lost the durable answer")
				}
			})
		}
	}
}

func TestQuestionSwitchPauseRejectionClearsBarrier(t *testing.T) {
	a := newQuestionTestAgent(t)
	createTestQuestions(t, a, questionItems("Choice"), false)
	manager := a.recoveryManager()
	a.clearRecoveryManagerIf(manager)
	defer a.installRecoveryManager(manager)
	a.prepareQuestionSessionSwitch(Event{Type: EventSessionControl, Payload: &sessionControlPayload{Kind: sessionControlNew}})
	if a.questions.switching || a.questions.switchEvent != nil || a.questions.active != nil {
		t.Fatal("rejected pause retained the switch barrier")
	}
}
