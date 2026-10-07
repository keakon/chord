package agent

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestQuestionRequestProjectionExcludesConsumedHistory(t *testing.T) {
	a := newQuestionTestAgent(t)
	id := createTestQuestions(t, a, questionItems("Choice"), false).Result.QuestionIDs[0]
	for i := range 1000 {
		key := fmt.Sprintf("completed-%d", i)
		a.questions.records[key] = QuestionSnapshot{ID: key, ScopeID: a.questions.scope, TaskID: identity.MainAgentID, Outcome: tools.QuestionOutcomeAnswered, Dependency: QuestionDependencyReceived}
		a.questions.recordTransaction(QuestionFact{TransactionID: key, Result: &tools.QuestionAnswer{QuestionID: key, ResultID: key, Outcome: tools.QuestionOutcomeAnswered}})
	}
	projection := a.snapshotQuestionRequest()
	if len(projection.records) != 1 || projection.records[id].ID != id || len(projection.transactions) != 0 || len(projection.unconsumed) != 0 {
		t.Fatalf("request copied completed history: records=%d transactions=%d unconsumed=%d", len(projection.records), len(projection.transactions), len(projection.unconsumed))
	}
	if len(a.questions.records) != 1001 || len(a.questions.transactions) != 1001 {
		t.Fatal("request projection changed recoverable history")
	}
}

func BenchmarkQuestionRequestProjection(b *testing.B) {
	for _, history := range []int{100, 10000} {
		b.Run(fmt.Sprintf("history_%d", history), func(b *testing.B) {
			a := &MainAgent{}
			a.initQuestions()
			for i := range history {
				id := fmt.Sprintf("completed-%d", i)
				a.questions.records[id] = QuestionSnapshot{ID: id, ScopeID: a.questions.scope, TaskID: identity.MainAgentID, Outcome: tools.QuestionOutcomeAnswered, Dependency: QuestionDependencyReceived}
				a.questions.recordTransaction(QuestionFact{TransactionID: id, Result: &tools.QuestionAnswer{QuestionID: id, ResultID: id, Outcome: tools.QuestionOutcomeAnswered}})
			}
			for i := range 8 {
				id := fmt.Sprintf("pending-%d", i)
				a.questions.records[id] = QuestionSnapshot{ID: id, ScopeID: a.questions.scope, TaskID: identity.MainAgentID, Item: questionItems("Choice")[0]}
			}
			msgs := []message.Message{{Role: message.RoleUser, Content: "Continue independent work"}}
			b.ReportAllocs()
			for b.Loop() {
				questionRequestContext(a.snapshotQuestionRequest(), msgs)
			}
		})
	}
}

func TestQuestionSnapshotsCarryLatestRevision(t *testing.T) {
	a := newQuestionTestAgent(t)
	id := createTestQuestions(t, a, questionItems("Choice"), false).Result.QuestionIDs[0]
	answer := runQuestionCommand(t, a, &questionCommand{operation: QuestionOperation{
		Operation: QuestionOpAnswer, OperationID: "answer", QuestionID: id, Answers: []string{"yes"},
	}})
	if !answer.Accepted {
		t.Fatalf("answer rejected: %+v", answer)
	}
	revise := runQuestionCommand(t, a, &questionCommand{operation: QuestionOperation{
		Operation: QuestionOpRevise, OperationID: "revise", QuestionID: id, Answers: []string{"no"},
	}})
	if !revise.Accepted {
		t.Fatalf("revise rejected: %+v", revise)
	}

	receipt := runQuestionCommand(t, a, &questionCommand{operation: QuestionOperation{Operation: questionOpSnapshot}})
	var snapshot *QuestionSnapshot
	for i := range receipt.Questions {
		if receipt.Questions[i].ID == id {
			snapshot = &receipt.Questions[i]
			break
		}
	}
	if snapshot == nil {
		t.Fatalf("snapshot does not contain %q: %+v", id, receipt.Questions)
	}
	if snapshot.Revision == nil || snapshot.Revision.ResultID == "" || !slices.Equal(snapshot.Revision.SelectedIDs, []string{"no"}) {
		t.Fatalf("revision = %+v, want the latest revise answer", snapshot.Revision)
	}
	if snapshot.Outcome != tools.QuestionOutcomeAnswered || snapshot.ResultID == "" || !slices.Equal(snapshot.SelectedIDs, []string{"yes"}) {
		t.Fatalf("original decision was rewritten by the revision: %+v", snapshot)
	}
}

func TestQuestionStateProjectionsRetainRevision(t *testing.T) {
	for _, operation := range []string{QuestionOpWithdraw, QuestionOpReplace} {
		for _, restored := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/restored_%v", operation, restored), func(t *testing.T) {
				a := newQuestionTestAgent(t)
				ids := createTestQuestions(t, a, questionItems("First", "Second"), false).Result.QuestionIDs
				applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpAnswer, QuestionID: ids[0], Answers: []string{"yes"}}, time.Time{})
				applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpRevise, QuestionID: ids[0], Answers: []string{"yes"}}, time.Time{})
				applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpRevise, QuestionID: ids[0], Answers: []string{"no"}}, time.Time{})
				drainAgentEvents(a.outputCh)
				checkEvent := func(events []AgentEvent, wantRestored bool) {
					t.Helper()
					var update *QuestionSnapshot
					for _, event := range events {
						if state, ok := event.(QuestionStateEvent); ok && state.Question.ID == ids[0] {
							if state.Restored != wantRestored {
								t.Fatal("incorrect restore marker")
							}
							update = &state.Question
						}
					}
					if update == nil || update.Revision == nil || !slices.Equal(update.Revision.SelectedIDs, []string{"no"}) || !slices.Equal(update.SelectedIDs, []string{"yes"}) {
						t.Fatalf("event lost revision or original answer: %+v", update)
					}
					receipt := runQuestionCommand(t, a, &questionCommand{operation: QuestionOperation{Operation: questionOpSnapshot}})
					for _, snapshot := range receipt.Questions {
						if snapshot.ID == ids[0] && !reflect.DeepEqual(snapshot, *update) {
							t.Fatalf("event and snapshot disagree: %+v / %+v", *update, snapshot)
						}
					}
					update.Revision.SelectedIDs[0] = "changed"
					if got := projectQuestionSnapshot(a.questions.records[ids[0]], &a.questions).Revision.SelectedIDs; !slices.Equal(got, []string{"no"}) {
						t.Fatal("event mutation changed durable revision")
					}
				}
				if restored {
					a.restoreQuestions(a.ctxMgr.Snapshot())
					a.publishRestoredQuestions()
					checkEvent(drainAgentEvents(a.outputCh), true)
				}
				receipt := applyTestQuestion(t, a, QuestionOperation{Operation: operation, QuestionID: ids[0], ReplacementID: ids[1], UserText: "Update the requirement"}, time.Time{})
				if !receipt.Accepted {
					t.Fatalf("requirement update rejected: %+v", receipt)
				}
				checkEvent(drainAgentEvents(a.outputCh), false)
				for _, msg := range a.ctxMgr.Snapshot() {
					if len(msg.Question) == 0 {
						continue
					}
					var fact QuestionFact
					if err := json.Unmarshal(msg.Question, &fact); err != nil {
						t.Fatal(err)
					}
					for _, update := range fact.Updates {
						if update.Revision != nil {
							t.Fatal("derived revision was written into durable facts")
						}
					}
				}
			})
		}
	}
}
