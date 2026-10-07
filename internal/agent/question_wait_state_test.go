package agent

import (
	"slices"
	"testing"
	"time"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/tools"
)

func TestQuestionWaitProjectionTracksInstallAndCancellation(t *testing.T) {
	a := newQuestionTestAgent(t)
	created := createTestQuestions(t, a, questionItems("Target"), false)
	id := created.Result.QuestionIDs[0]
	drainAgentEvents(a.Events())
	c := &questionCommand{operation: QuestionOperation{Operation: questionOpWait, OperationID: "wait"}, args: tools.QuestionArgs{WaitFor: []string{id}}, owner: identity.MainAgentID, task: identity.MainAgentID, scope: a.questions.scope, reply: make(chan QuestionReceipt, 1)}
	a.installQuestionWait(c)
	assertQuestionWaitProjection(t, a, []string{id})
	a.handleQuestionCommand(&questionCommand{operation: QuestionOperation{Operation: questionOpUnwait, OperationID: "wait"}})
	assertQuestionWaitProjection(t, a, nil)
	if a.questionWaiting.Load() {
		t.Fatal("cancelled wait retained the runtime waiting flag")
	}
}

func TestQuestionWaitProjectionDeduplicatesAndClearsInterruptedWaits(t *testing.T) {
	a := newQuestionTestAgent(t)
	created := createTestQuestions(t, a, questionItems("Target"), false)
	id := created.Result.QuestionIDs[0]
	for _, op := range []string{"first", "second"} {
		a.installQuestionWait(&questionCommand{operation: QuestionOperation{Operation: questionOpWait, OperationID: op}, args: tools.QuestionArgs{WaitFor: []string{id}}, owner: identity.MainAgentID, task: identity.MainAgentID, scope: a.questions.scope, reply: make(chan QuestionReceipt, 1)})
	}
	assertQuestionWaitProjection(t, a, []string{id})
	a.interruptQuestionWaits()
	assertQuestionWaitProjection(t, a, nil)
}

func assertQuestionWaitProjection(t *testing.T, a *MainAgent, want []string) {
	t.Helper()
	var last *QuestionWaitStateEvent
	for _, event := range drainAgentEvents(a.Events()) {
		if e, ok := event.(QuestionWaitStateEvent); ok {
			last = &e
		}
	}
	if last == nil || last.BindingID != a.questions.binding || !slices.Equal(last.QuestionIDs, want) {
		t.Fatalf("wait projection = %+v, want %v", last, want)
	}
}

func TestQuestionAnsweredBeforeWaitInstallationSettlesImmediately(t *testing.T) {
	a := newQuestionTestAgent(t)
	created := createTestQuestions(t, a, questionItems("Target"), true)
	id := created.Result.QuestionIDs[0]
	applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpAnswer, QuestionID: id, Answers: []string{"yes"}}, time.Time{})
	c := &questionCommand{operation: QuestionOperation{Operation: questionOpWait, OperationID: "late-wait"}, args: tools.QuestionArgs{WaitFor: []string{id}}, owner: identity.MainAgentID, task: identity.MainAgentID, scope: a.questions.scope, reply: make(chan QuestionReceipt, 1)}
	a.installQuestionWait(c)
	select {
	case r := <-c.reply:
		if !r.Accepted || r.Result.Status != tools.QuestionStatusResolved || len(r.Result.Answers) != 1 || r.Result.Answers[0].Outcome != tools.QuestionOutcomeAnswered {
			t.Fatalf("late wait = %+v", r)
		}
	default:
		t.Fatal("answer before wait installation left a stuck waiter")
	}
	if len(a.questions.waits) != 0 || a.questionWaiting.Load() {
		t.Fatal("already-answered question retained a waiter")
	}
}
