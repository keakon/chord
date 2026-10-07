package agent

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestQuestionCompactionPreservesCheckpointCrashRecovery(t *testing.T) {
	a := newQuestionTestAgent(t)
	createTestQuestions(t, a, questionItems("Choice"), false)
	head := message.Message{Role: message.RoleUser, Content: "checkpoint", IsCompactionSummary: true, CompactionSummaryMode: compactionSummaryModeModelDriven, RequestBatch: 7}
	tail := message.Message{Role: message.RoleUser, Content: "tail request"}
	messages := retainQuestionFacts(a.ctxMgr.Snapshot(), []message.Message{head, tail})
	if !messages[0].IsCompactionSummary || messages[len(messages)-1].Content != tail.Content {
		t.Fatalf("checkpoint or tail moved: %#v", messages)
	}
	dir := t.TempDir()
	if err := writeCompactionTransactionManifest(dir, compactionTransactionManifest{
		TransactionID: "transaction-1", ProposalID: "proposal-1",
		TargetFingerprint: compactionTranscriptFingerprint(messages), Status: compactionTransactionPrepared,
	}); err != nil {
		t.Fatal(err)
	}
	if err := reconcileCompactionTransactions(dir, messages); err != nil {
		t.Fatal(err)
	}
	if batch, ok := modelDrivenCommittedApplyBatch(dir, messages, "proposal-1"); !ok || batch != 7 {
		t.Fatalf("committed apply: batch=%d ok=%v", batch, ok)
	}
	a.restoreQuestions(messages)
	if len(a.questions.records) != 1 {
		t.Fatal("pending question was lost")
	}
}

func TestQuestionCompactionProjectsOnlyUnconsumedDecisions(t *testing.T) {
	a := newQuestionTestAgent(t)
	id := createTestQuestions(t, a, questionItems("Choice"), false).Result.QuestionIDs[0]
	applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpAnswer, QuestionID: id, Answers: []string{"yes"}}, time.Now())
	resultID := a.questions.records[id].ResultID
	checkpoint := message.Message{Role: message.RoleUser, Content: "checkpoint", IsCompactionSummary: true}
	compacted := retainQuestionFacts(a.ctxMgr.Snapshot(), []message.Message{checkpoint})
	a.restoreQuestions(compacted)
	projected := questionRequestContext(a.snapshotQuestionRequest(), filterQuestionStateMessages(compacted))
	if len(questionResultsInMessages(projected)) != 1 || !a.questions.unconsumed[resultID] {
		t.Fatal("archived unconsumed answer was not projected")
	}
	fact, err := json.Marshal(QuestionFact{Operation: questionOpConsume, TransactionID: "consume-1", Consumed: []string{resultID}})
	if err != nil {
		t.Fatal(err)
	}
	original := append(compacted, message.Message{Role: message.RoleAssistant, Content: "answer applied", Question: fact})
	compacted = retainQuestionFacts(original, []message.Message{checkpoint})
	a.restoreQuestions(compacted)
	projected = questionRequestContext(a.snapshotQuestionRequest(), filterQuestionStateMessages(compacted))
	if len(projected) != 1 || len(questionResultsInMessages(projected)) != 0 || a.questions.unconsumed[resultID] {
		t.Fatalf("consumed answer reentered request: %#v", projected)
	}
	for _, msg := range compacted[1:] {
		if msg.Kind != message.KindQuestionState || msg.Content != "" || msg.Role != message.RoleSystem {
			t.Fatalf("archived conversation was retained as content: %#v", msg)
		}
	}
	if got := a.questions.records[id]; got.TaskID != identity.MainAgentID || got.Outcome != tools.QuestionOutcomeAnswered {
		t.Fatalf("answer state lost: %#v", got)
	}
	if repeated := retainQuestionFacts(compacted, []message.Message{checkpoint}); len(repeated) != len(compacted) {
		t.Fatal("repeated compaction duplicated facts")
	}
}

// Decisions are projected in durable commit order; lexically smaller client
// operation IDs must not move later revisions ahead of the answers they
// supersede.
func TestQuestionDecisionsKeepCommittedOrderAfterCompaction(t *testing.T) {
	a := newQuestionTestAgent(t)
	id := createTestQuestions(t, a, questionItems("Choice"), false).Result.QuestionIDs[0]
	for _, op := range []QuestionOperation{
		{Operation: QuestionOpAnswer, OperationID: "z-answer", QuestionID: id, Answers: []string{"no"}},
		{Operation: QuestionOpRevise, OperationID: "m-revision", QuestionID: id, Answers: []string{"yes"}},
		{Operation: QuestionOpRevise, OperationID: "a-revision", QuestionID: id, Answers: []string{"no"}},
	} {
		if receipt := applyTestQuestion(t, a, op, time.Time{}); !receipt.Accepted {
			t.Fatalf("operation %q rejected: %+v", op.OperationID, receipt)
		}
	}
	compacted := retainQuestionFacts(a.ctxMgr.Snapshot(), []message.Message{{Role: message.RoleSystem, Content: "Summary"}})
	a.restoreQuestions(compacted)
	projected := questionRequestContext(a.snapshotQuestionRequest(), filterQuestionStateMessages(compacted))
	var selected []string
	for _, msg := range projected {
		var fact QuestionFact
		if json.Unmarshal(msg.Question, &fact) == nil && fact.Result != nil {
			selected = append(selected, fact.Result.SelectedIDs...)
		}
	}
	if len(selected) != 3 || selected[0] != "no" || selected[1] != "yes" || selected[2] != "no" {
		t.Fatalf("decisions projected out of committed order: %v", selected)
	}
}
