package agent

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestQuestionRequestProjectionExcludesConsumedHistory(t *testing.T) {
	t.Parallel()
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

// The request projection is the only thing that tells the model about archived
// question state once a compaction dropped the tool call that asked it, so its
// rows have to be built in a shape the provider conversion actually delivers.
// A mid-conversation system row is dropped by all four wire converters (OpenAI
// chat, Anthropic, Gemini and Responses) — the projection used to be one, and
// the model was blind to it in production while these tests stayed green — and
// a plain user row appended at the request tail moves the current-turn boundary
// that reasoning replay and Gemini signature placeholders are keyed on. A
// request-scoped user overlay is the one shape that avoids both.
func TestQuestionProjectionUsesADeliveredRequestShape(t *testing.T) {
	t.Parallel()
	a := newQuestionTestAgent(t)
	id := createTestQuestions(t, a, questionItems("Choice"), false).Result.QuestionIDs[0]

	pending := questionRequestContext(a.snapshotQuestionRequest(), nil)
	if len(pending) != 1 {
		t.Fatalf("pending projection = %#v, want one row", pending)
	}
	assertDeliveredProjectionRow(t, pending[0])
	if !strings.Contains(pending[0].Content, "Current questions: ") || !strings.Contains(pending[0].Content, id) {
		t.Fatalf("pending row does not name the pending question: %q", pending[0].Content)
	}

	applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpAnswer, QuestionID: id, Answers: []string{"yes"}}, time.Time{})
	decided := questionRequestContext(a.snapshotQuestionRequest(), nil)
	if len(decided) != 1 {
		t.Fatalf("decision projection = %#v, want one row", decided)
	}
	assertDeliveredProjectionRow(t, decided[0])
	if len(decided[0].Question) == 0 {
		t.Fatal("decision row lost the durable fact payload")
	}
	// The decision has to carry the question itself: by the time this row is the
	// only trace of the call that asked it, a bare header would leave the model
	// correlating an answer against a question it cannot see.
	for _, want := range []string{"Choice", "Choose an option", "Selected: Yes"} {
		if !strings.Contains(decided[0].Content, want) {
			t.Fatalf("decision row %q is missing %q", decided[0].Content, want)
		}
	}
	if got := questionResultsInMessages(decided); len(got) != 1 || got[0] != a.questions.records[id].ResultID {
		t.Fatalf("decision row is not recognizable as a delivered result: %v", got)
	}
}

func TestQuestionProjectionIncludesStableIDsForDuplicateQuestionsAndRevisions(t *testing.T) {
	t.Parallel()
	a := newQuestionTestAgent(t)
	created := createTestQuestions(t, a, questionItems("Repeated question", "Repeated question"), false)
	ids := created.Result.QuestionIDs
	if len(ids) != 2 || ids[0] == ids[1] {
		t.Fatalf("question IDs = %v, want two distinct IDs", ids)
	}

	applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpAnswer, QuestionID: ids[0], Answers: []string{"yes"}}, time.Time{})
	applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpAnswer, QuestionID: ids[1], Answers: []string{"no"}}, time.Time{})
	applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpRevise, QuestionID: ids[0], Answers: []string{"no"}}, time.Time{})

	projected := questionRequestContext(a.snapshotQuestionRequest(), nil)
	if len(projected) != 3 {
		t.Fatalf("decision projections = %d, want two answers and one revision", len(projected))
	}
	seenResults := map[string]bool{}
	for _, msg := range projected {
		var fact QuestionFact
		if err := json.Unmarshal(msg.Question, &fact); err != nil || fact.Result == nil {
			t.Fatalf("projection fact = %+v, err=%v", fact, err)
		}
		result := fact.Result
		if result.QuestionID == "" || result.ResultID == "" {
			t.Fatalf("decision result lost stable IDs: %+v", result)
		}
		if seenResults[result.ResultID] {
			t.Fatalf("duplicate result ID %q", result.ResultID)
		}
		seenResults[result.ResultID] = true
		for _, want := range []string{"Question ID: " + result.QuestionID, "Result ID: " + result.ResultID} {
			if !strings.Contains(msg.Content, want) {
				t.Fatalf("decision projection %q is missing %q", msg.Content, want)
			}
		}
	}
	if len(seenResults) != 3 {
		t.Fatalf("distinct projected result IDs = %d, want 3", len(seenResults))
	}
}

// Question and answer text is projected whole. User input and model-authored
// questions are short in practice, the pending-question budget already bounds the
// aggregate where it is created (maxPendingQuestionBytes, shared by every pending
// question), and a header-only echo would force the model to wait for a decision
// just to find out what it asks. The pending row is re-projected on every
// request, so this is also the guard that nobody starts truncating it silently.
func TestQuestionProjectionEchoesQuestionAndAnswerWhole(t *testing.T) {
	t.Parallel()
	a := newQuestionTestAgent(t)
	// 300 x 24 bytes = 7200 bytes: a long but valid question, near MaxQuestionTextBytes.
	long := strings.Repeat("详尽的问题正文。", 300)
	items := questionItems("Choice")
	items[0].Question = long
	id := createTestQuestions(t, a, items, false).Result.QuestionIDs[0]

	pending := questionRequestContext(a.snapshotQuestionRequest(), nil)
	if len(pending) != 1 {
		t.Fatalf("pending projection = %#v, want one row", pending)
	}
	if !strings.Contains(pending[0].Content, long) {
		t.Fatal("pending row truncated the question body")
	}
	if !strings.Contains(pending[0].Content, id) {
		t.Fatal("pending row lost the question id the model waits with")
	}

	applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpAnswer, QuestionID: id, Answers: []string{"yes"}}, time.Time{})
	decided := questionRequestContext(a.snapshotQuestionRequest(), nil)
	if len(decided) != 1 {
		t.Fatalf("decision projection = %#v, want one row", decided)
	}
	for _, want := range []string{long, "Selected: Yes"} {
		if !strings.Contains(decided[0].Content, want) {
			t.Fatalf("decision row is missing %q", want)
		}
	}
}

func assertDeliveredProjectionRow(t *testing.T, msg message.Message) {
	t.Helper()
	if msg.Role != message.RoleUser || msg.Kind != message.KindTurnOverlay {
		t.Fatalf("projection row = role %q kind %q, want the user/turn_overlay shape every wire converter delivers", msg.Role, msg.Kind)
	}
	if message.IsUserAuthored(msg) {
		t.Fatal("projection row counted as user-authored")
	}
	if strings.TrimSpace(msg.Content) == "" {
		t.Fatal("projection row has no content")
	}
}

// An outcome the runtime decided has to say so in the text itself, because the
// decision travels alone: the projection row no longer prefixes a provenance
// label of its own, and a default read as user consent would authorize work the
// user never approved.
func TestQuestionDecisionTextNamesNonUserOutcomes(t *testing.T) {
	for _, tc := range []struct{ outcome, want string }{
		{tools.QuestionOutcomeAnswered, "Question answered: Header"},
		{tools.QuestionOutcomeDeclined, "Question declined: Header"},
		{tools.QuestionOutcomeDefaulted, "not user consent or authorization"},
		{tools.QuestionOutcomeNoResponse, "not user consent or authorization"},
	} {
		t.Run(tc.outcome, func(t *testing.T) {
			text := questionDecisionText(QuestionFact{
				Result:  &tools.QuestionAnswer{QuestionID: "q-1", ResultID: "r-1", Header: "Header", Outcome: tc.outcome, Selected: []string{"Yes"}},
				Updates: []QuestionSnapshot{{ID: "q-1", Item: tools.QuestionItem{Header: "Header", Question: "Choose an option"}}},
			})
			if !strings.Contains(text, tc.want) {
				t.Fatalf("decision text = %q, want %q", text, tc.want)
			}
			if !strings.Contains(text, "Choose an option") {
				t.Fatalf("decision text = %q, want the question text", text)
			}
		})
	}
}

func TestQuestionSnapshotsCarryLatestRevision(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
