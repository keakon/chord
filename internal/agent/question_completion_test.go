package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestQuestionPausedPresentationDoesNotPublishNewVersions(t *testing.T) {
	for _, armed := range []bool{false, true} {
		a := newQuestionTestAgent(t)
		a.globalConfig.QuestionTimeout = 3600
		items := questionItems("Preference")
		items[0].ResponsePolicy = tools.QuestionPolicyOptional
		id := createTestQuestions(t, a, items, false).Result.QuestionIDs[0]
		if armed {
			applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpPresented, QuestionID: id, SupportsInteraction: true}, time.Now())
		}
		runQuestionCommand(t, a, &questionCommand{operation: QuestionOperation{Operation: questionOpPause, OperationID: "pause"}})
		before := a.questions.records[id]
		count, transactions := a.ctxMgr.MessageCount(), len(a.questions.transactions)
		for range 3 {
			receipt := applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpPresented, QuestionID: id, SupportsInteraction: true}, time.Now())
			if !receipt.Accepted || receipt.Version != before.Version {
				t.Fatalf("presentation = %+v", receipt)
			}
		}
		if q := a.questions.records[id]; q.Version != before.Version || q.Timer != before.Timer || a.ctxMgr.MessageCount() != count || len(a.questions.transactions) != transactions {
			t.Fatal("paused presentation wrote a new state")
		}
		runQuestionCommand(t, a, &questionCommand{operation: QuestionOperation{Operation: questionOpResume, OperationID: "resume"}})
		if a.questions.records[id].Version <= before.Version {
			t.Fatal("resume did not request a fresh presentation")
		}
		applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpPresented, QuestionID: id, SupportsInteraction: true}, time.Now())
		if a.questions.records[id].Timer != QuestionTimerArmed {
			t.Fatal("resumed visible question did not arm its timer")
		}
	}
}

func TestQuestionCompletionRequiresConsumption(t *testing.T) {
	a := newQuestionTestAgent(t)
	id := createTestQuestions(t, a, questionItems("Choice"), false).Result.QuestionIDs[0]
	applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpAnswer, QuestionID: id, Answers: []string{"yes"}}, time.Time{})
	result := a.questions.records[id].ResultID
	if len(a.requiredQuestionIDs()) != 0 || a.questionCompletionPending.Load() == 0 {
		t.Fatal("received decision bypassed the consumption gate")
	}
	_, err := a.executeToolCall(context.Background(), message.ToolCall{Name: tools.NameDone})
	if !errors.Is(err, errRequiredQuestionDecisions) {
		t.Fatalf("Done before consumption = %v", err)
	}
	a.restoreQuestions(a.ctxMgr.Snapshot())
	if a.questionCompletionPending.Load() == 0 {
		t.Fatal("restoration lost the unconsumed decision")
	}
	r := runQuestionCommand(t, a, &questionCommand{operation: QuestionOperation{Operation: questionOpComplete, OperationID: "early-complete"}})
	if r.Accepted {
		t.Fatal("completion accepted an unconsumed decision")
	}
	a.consumeQuestionResults([]string{result})
	if a.questionCompletionPending.Load() != 0 {
		t.Fatal("consumption did not release completion")
	}
	r = runQuestionCommand(t, a, &questionCommand{operation: QuestionOperation{Operation: questionOpComplete, OperationID: "complete"}})
	if !r.Accepted || !a.questions.completed[a.questions.scope] {
		t.Fatalf("completion after consumption = %+v", r)
	}
}
