package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func questionItems(headers ...string) []tools.QuestionItem {
	var items []tools.QuestionItem
	for _, h := range headers {
		items = append(items, tools.QuestionItem{Header: h, Question: "Choose an option", Options: []tools.QuestionOption{{ID: "yes", Label: "Yes"}, {ID: "no", Label: "No"}}})
	}
	return items
}
func newQuestionTestAgent(t *testing.T) *MainAgent {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn("")
	a.initQuestions()
	t.Cleanup(func() { a.restoreQuestions(nil) })
	return a
}
func runQuestionCommand(t *testing.T, a *MainAgent, c *questionCommand) QuestionReceipt {
	t.Helper()
	if c.reply == nil {
		c.reply = make(chan QuestionReceipt, 1)
	}
	if c.ctx == nil {
		c.ctx = context.Background()
	}
	if c.owner == "" {
		c.owner = identity.MainAgentID
	}
	if c.task == "" {
		c.task = identity.MainAgentID
	}
	a.handleQuestionCommand(c)
	timeout := time.After(3 * time.Second)
	for {
		select {
		case receipt := <-c.reply:
			return receipt
		case evt := <-a.eventCh:
			if evt.Type == EventQuestionCommitted {
				a.handleQuestionCommitted(evt.Payload.(*questionCommit))
			} else if evt.Type == EventQuestionCommand {
				a.handleQuestionCommand(evt.Payload.(*questionCommand))
			}
		case <-timeout:
			t.Fatal("question command failed to settle")
			return QuestionReceipt{}
		}
	}
}
func createTestQuestions(t *testing.T, a *MainAgent, items []tools.QuestionItem, wait bool) QuestionReceipt {
	t.Helper()
	r := runQuestionCommand(t, a, &questionCommand{operation: QuestionOperation{Operation: questionOpCreate, OperationID: makeRequestID()}, call: makeRequestID(), args: tools.QuestionArgs{Questions: items, Wait: new(wait)}})
	if !r.Accepted {
		t.Fatalf("creation rejected: %+v", r)
	}
	return r
}
func applyTestQuestion(t *testing.T, a *MainAgent, op QuestionOperation, at time.Time) QuestionReceipt {
	t.Helper()
	if op.Operation == QuestionOpPresented && op.Version == 0 {
		op.Version = a.questions.records[op.QuestionID].Version
	}
	if op.Operation == QuestionOpPresented && op.BindingID == "" {
		op.BindingID = a.questions.records[op.QuestionID].BindingID
	}
	if op.OperationID == "" {
		op.OperationID = makeRequestID()
	}
	return runQuestionCommand(t, a, &questionCommand{operation: op, received: at})
}
func TestQuestionRequiredIgnoresConfiguredTimeouts(t *testing.T) {
	t.Parallel()
	a := newQuestionTestAgent(t)
	a.globalConfig.QuestionTimeout = 60
	a.globalConfig.QuestionAutoSelectTimeout = 60
	r := createTestQuestions(t, a, questionItems("Choice"), false)
	q := a.questions.records[r.Result.QuestionIDs[0]]
	if q.Timer != QuestionTimerDisabled || !q.Deadline.IsZero() || q.Dependency != QuestionDependencyPending {
		t.Fatalf("required state: %+v", q)
	}
	if len(a.requiredQuestionIDs()) != 1 {
		t.Fatal("missing completion gate")
	}
}
func TestQuestionDefaultStartsAfterPresentationAndDoesNotAuthorize(t *testing.T) {
	t.Parallel()
	a := newQuestionTestAgent(t)
	a.globalConfig.QuestionAutoSelectTimeout = 3600
	items := questionItems("Choice")
	items[0].ResponsePolicy = tools.QuestionPolicyDefaultAllowed
	items[0].DefaultOptionID = "no"
	r := createTestQuestions(t, a, items, false)
	id := r.Result.QuestionIDs[0]
	if q := a.questions.records[id]; q.Timer != QuestionTimerAwaiting || !q.Deadline.IsZero() {
		t.Fatalf("armed before presentation: %+v", q)
	}
	now := time.Now()
	applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpPresented, QuestionID: id, SupportsInteraction: true}, now)
	q := a.questions.records[id]
	if q.Deadline != now.Add(time.Hour) {
		t.Fatal("incorrect presentation deadline")
	}
	receipt := applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpAnswer, QuestionID: id, Answers: []string{"yes"}}, q.Deadline)
	q = a.questions.records[id]
	if receipt.Accepted || q.Outcome != tools.QuestionOutcomeDefaulted || q.SelectedIDs[0] != "no" {
		t.Fatalf("deadline did not decide default: %+v %+v", q, receipt)
	}
	history := a.ctxMgr.Snapshot()
	last := history[len(history)-1]
	if message.IsUserAuthored(last) || last.Kind != message.KindQuestionResult {
		t.Fatal("system default impersonated user")
	}
}

// TestQuestionAsyncAnswerStaysADecisionNotUserProse covers the other half of the
// same contract: an answer that arrives while the asking turn is still running.
// An asynchronous question leaves no pending tool result to carry the decision
// back, so it reaches the model on a user-role row — but as a recorded decision,
// not as prose a person typed. A user-authored row feeds composer recall, runtime
// evidence and the reasoning-replay turn boundary, and a decision is none of
// those; an empty kind additionally put a second USER card in the transcript.
//
// The text has to name the question as well, because by the time the answer
// lands the call that asked it may already be out of the request.
func TestQuestionAsyncAnswerStaysADecisionNotUserProse(t *testing.T) {
	t.Parallel()
	a := newQuestionTestAgent(t)
	id := createTestQuestions(t, a, questionItems("Push the tag"), false).Result.QuestionIDs[0]
	applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpAnswer, QuestionID: id, Answers: []string{"yes"}}, time.Time{})

	history := a.ctxMgr.Snapshot()
	last := history[len(history)-1]
	if last.Role != message.RoleUser || last.Kind != message.KindQuestionResult {
		t.Fatalf("async answer shape = role %q kind %q, want a user-role decision", last.Role, last.Kind)
	}
	if message.IsUserAuthored(last) {
		t.Fatal("a recorded decision counted as user prose")
	}
	if a.explicitUserTurnCount.Load() != 0 {
		t.Fatalf("explicit user turns = %d, want 0", a.explicitUserTurnCount.Load())
	}
	for _, want := range []string{"Push the tag", "Choose an option", "Yes", "Question ID: " + id} {
		if !strings.Contains(last.Content, want) {
			t.Fatalf("decision text = %q, want it to name %q", last.Content, want)
		}
	}
	if resultID := a.questions.records[id].ResultID; resultID == "" || !strings.Contains(last.Content, "Result ID: "+resultID) {
		t.Fatalf("decision text = %q, want its result ID %q", last.Content, resultID)
	}
	if strings.Contains(last.Content, "question_id") {
		t.Fatalf("decision text = %q, want text, not the raw answer JSON", last.Content)
	}
}

// TestQuestionWithdrawAfterDeadlineKeepsTheUsersWords covers the one withdrawal
// that does not reach the transcript through the ordinary decision path. The
// user role used to be set only while the question was unexpired, so a
// withdrawal arriving after the deadline was written to a system row — a shape
// every wire converter and the transcript renderer drop, which lost the text
// the user actually wrote.
func TestQuestionWithdrawAfterDeadlineKeepsTheUsersWords(t *testing.T) {
	t.Parallel()
	a := newQuestionTestAgent(t)
	a.globalConfig.QuestionTimeout = 60
	items := questionItems("Choice")
	items[0].ResponsePolicy = tools.QuestionPolicyOptional
	id := createTestQuestions(t, a, items, true).Result.QuestionIDs[0]
	applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpPresented, QuestionID: id, SupportsInteraction: true}, time.Now())
	deadline := a.questions.records[id].Deadline
	if deadline.IsZero() {
		t.Fatal("question was never armed; the deadline case cannot be reached")
	}

	applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpWithdraw, QuestionID: id, UserText: "changed my mind"}, deadline)

	history := a.ctxMgr.Snapshot()
	last := history[len(history)-1]
	if last.Role != message.RoleUser || last.Kind != "" {
		t.Fatalf("withdrawal shape = role %q kind %q, want the user's own prose", last.Role, last.Kind)
	}
	if last.Content != "changed my mind" || !message.IsUserAuthored(last) {
		t.Fatalf("withdrawal = %q userAuthored=%v, want the user's words as a user turn", last.Content, message.IsUserAuthored(last))
	}
	if outcome := a.questions.records[id].Outcome; outcome != tools.QuestionOutcomeCancelled {
		t.Fatalf("outcome = %q, want the question cancelled", outcome)
	}
}

func TestQuestionInterventionPermanentlyDisablesTimer(t *testing.T) {
	t.Parallel()
	a := newQuestionTestAgent(t)
	a.globalConfig.QuestionTimeout = 3600
	items := questionItems("Choice")
	items[0].ResponsePolicy = tools.QuestionPolicyOptional
	r := createTestQuestions(t, a, items, false)
	id := r.Result.QuestionIDs[0]
	now := time.Now()
	applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpPresented, QuestionID: id, SupportsInteraction: true}, now)
	applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpInteract, OperationID: "interact", QuestionID: id}, now.Add(time.Second))
	applyTestQuestion(t, a, QuestionOperation{Operation: questionOpExpire, QuestionID: id}, now.Add(2*time.Hour))
	applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpPresented, QuestionID: id, SupportsInteraction: true}, now.Add(3*time.Hour))
	q := a.questions.records[id]
	if !q.pending() || q.Timer != QuestionTimerCancelled || !q.Deadline.IsZero() {
		t.Fatalf("intervention lost: %+v", q)
	}
}
func TestQuestionBatchCommitRetryReturnsOriginalIDs(t *testing.T) {
	t.Parallel()
	a := newQuestionTestAgent(t)
	c := &questionCommand{operation: QuestionOperation{Operation: questionOpCreate, OperationID: "attempt-1"}, call: "call-1", args: tools.QuestionArgs{Questions: questionItems("First", "Second", "Third"), Wait: new(false)}}
	first := runQuestionCommand(t, a, c)
	before := len(a.ctxMgr.Snapshot())
	retry := *c
	retry.reply = nil
	retry.operation.OperationID = "attempt-2"
	second := runQuestionCommand(t, a, &retry)
	if !second.Accepted || strings.Join(first.Result.QuestionIDs, ",") != strings.Join(second.Result.QuestionIDs, ",") || len(a.ctxMgr.Snapshot()) != before {
		t.Fatal("retry duplicated batch")
	}
	retry.args.Questions = questionItems("Different")
	retry.reply = nil
	if got := runQuestionCommand(t, a, &retry); got.Accepted {
		t.Fatal("same call accepted different arguments")
	}
}
func TestQuestionBatchRejectsBeforePublishingAnyItem(t *testing.T) {
	t.Parallel()
	a := newQuestionTestAgent(t)
	items := questionItems("First", "Second")
	items[1].ResponsePolicy = "invalid"
	receipt := runQuestionCommand(t, a, &questionCommand{operation: QuestionOperation{Operation: questionOpCreate, OperationID: "batch"}, args: tools.QuestionArgs{Questions: items}})
	if receipt.Accepted || len(a.questions.records) != 0 || len(a.ctxMgr.Snapshot()) != 0 {
		t.Fatal("invalid batch partially committed")
	}
}
func TestQuestionSyncBatchRestoresOrderAndCancelsUnopenedItems(t *testing.T) {
	t.Parallel()
	a := newQuestionTestAgent(t)
	r := createTestQuestions(t, a, questionItems("First", "Second", "Third"), true)
	ids := r.Result.QuestionIDs
	applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpAnswer, QuestionID: ids[0], Answers: []string{"yes"}}, time.Time{})
	if !a.questions.records[ids[1]].Visible || a.questions.records[ids[2]].Visible {
		t.Fatal("incorrect sequential progress")
	}
	applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpDecline, QuestionID: ids[1]}, time.Time{})
	if q := a.questions.records[ids[2]]; q.Outcome != tools.QuestionOutcomeCancelled || q.Reason != "batch_stopped" {
		t.Fatalf("unopened item: %+v", q)
	}
	saved := a.ctxMgr.Snapshot()
	a.restoreQuestions(saved)
	if a.questions.records[ids[0]].Outcome != tools.QuestionOutcomeAnswered || a.questions.records[ids[2]].Outcome != tools.QuestionOutcomeCancelled {
		t.Fatal("batch progress lost")
	}
}
func TestQuestionDeclineWakesWaitAndReplacementResolvesDependency(t *testing.T) {
	t.Parallel()
	a := newQuestionTestAgent(t)
	created := createTestQuestions(t, a, questionItems("First", "Second"), false)
	ids := created.Result.QuestionIDs
	wait := &questionCommand{operation: QuestionOperation{Operation: questionOpWait, OperationID: "wait"}, args: tools.QuestionArgs{WaitFor: ids}, owner: identity.MainAgentID, task: identity.MainAgentID, scope: a.questions.scope, reply: make(chan QuestionReceipt, 1)}
	a.installQuestionWait(wait)
	applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpDecline, QuestionID: ids[0]}, time.Time{})
	select {
	case receipt := <-wait.reply:
		if receipt.Result.Status != tools.QuestionStatusResolved {
			t.Fatal("wrong wait status")
		}
	default:
		t.Fatal("decline did not wake waiter")
	}
	if len(a.requiredQuestionIDs()) != 2 {
		t.Fatal("decline removed requirements")
	}
	applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpReplace, QuestionID: ids[0], ReplacementID: ids[1], UserText: "Use the alternative decision instead"}, time.Time{})
	applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpAnswer, QuestionID: ids[1], Answers: []string{"no"}}, time.Time{})
	if len(a.requiredQuestionIDs()) != 0 {
		t.Fatal("replacement left an impossible completion gate")
	}
	if a.questions.records[ids[0]].Outcome != tools.QuestionOutcomeDeclined {
		t.Fatal("replacement rewrote old terminal")
	}
}
func TestQuestionOutsideWaitSetInterruptsWithoutClosingQuestions(t *testing.T) {
	t.Parallel()
	a := newQuestionTestAgent(t)
	r := createTestQuestions(t, a, questionItems("First", "Second"), false)
	ids := r.Result.QuestionIDs
	c := &questionCommand{operation: QuestionOperation{Operation: questionOpWait, OperationID: "wait"}, args: tools.QuestionArgs{WaitFor: ids[:1]}, task: identity.MainAgentID, scope: a.questions.scope, reply: make(chan QuestionReceipt, 1)}
	a.installQuestionWait(c)
	applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpAnswer, QuestionID: ids[1], Answers: []string{"yes"}}, time.Time{})
	select {
	case reply := <-c.reply:
		if reply.Result.Status != tools.QuestionStatusWaitInterrupted {
			t.Fatal("did not interrupt")
		}
	default:
		t.Fatal("missing wake")
	}
	if !a.questions.records[ids[0]].pending() {
		t.Fatal("interruption closed unanswered question")
	}
}
func TestQuestionHistoryRevisionWithNewPendingQuestion(t *testing.T) {
	t.Parallel()
	a := newQuestionTestAgent(t)
	old := createTestQuestions(t, a, questionItems("First"), false).Result.QuestionIDs[0]
	applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpDecline, QuestionID: old}, time.Time{})
	next := createTestQuestions(t, a, questionItems("Second"), false).Result.QuestionIDs[0]
	receipt := applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpRevise, QuestionID: old, Answers: []string{"yes"}}, time.Time{})
	if !receipt.Accepted || !a.questions.records[next].pending() || a.questions.records[old].Outcome != tools.QuestionOutcomeDeclined || a.questions.records[old].Dependency != QuestionDependencyReceived {
		t.Fatal("historical reply routed to new question or changed terminal")
	}
}
func TestQuestionRecoveryPreservesInterventionAndResultConsumption(t *testing.T) {
	t.Parallel()
	a := newQuestionTestAgent(t)
	r := createTestQuestions(t, a, questionItems("Choice"), false)
	id := r.Result.QuestionIDs[0]
	applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpInteract, QuestionID: id}, time.Time{})
	applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpAnswer, QuestionID: id, Answers: []string{"yes"}}, time.Time{})
	msgs := a.ctxMgr.Snapshot()
	resultID := a.questions.records[id].ResultID
	assistant := message.Message{Role: message.RoleAssistant, Content: "Decision received", RequestBatch: 1}
	a.attachQuestionConsumption(&assistant, []string{resultID})
	msgs = append(msgs, assistant)
	a.restoreQuestions(msgs)
	if a.questions.records[id].Outcome != tools.QuestionOutcomeAnswered || a.questions.unconsumed[resultID] {
		t.Fatal("restore reopened terminal or lost consumption")
	}
}
func TestQuestionPersistenceFailurePublishesNoSuccess(t *testing.T) {
	t.Parallel()
	a := newQuestionTestAgent(t)
	a.recoveryManager().Close()
	receipt := runQuestionCommand(t, a, &questionCommand{operation: QuestionOperation{Operation: questionOpCreate, OperationID: "create"}, args: tools.QuestionArgs{Questions: questionItems("Choice"), Wait: new(false)}})
	if receipt.Accepted || !a.questions.failed || len(a.questions.records) != 0 {
		t.Fatal("failed commit reported success")
	}
}
func TestQuestionCommitVisibleOnlyAfterPersistence(t *testing.T) {
	t.Parallel()
	a := newQuestionTestAgent(t)
	a.persist.admission <- struct{}{}
	receipt := runQuestionCommand(t, a, &questionCommand{operation: QuestionOperation{Operation: questionOpCreate, OperationID: "create"}, args: tools.QuestionArgs{Questions: questionItems("Choice"), Wait: new(false)}})
	<-a.persist.admission
	if receipt.Accepted || len(a.questions.records) != 0 {
		t.Fatal("queue congestion published a question")
	}
}
func TestQuestionValidateSelectionsAndCustomText(t *testing.T) {
	item := questionItems("Choice")[0]
	if _, _, err := validateQuestionAnswer(item, []string{"Yes"}, false); err == nil {
		t.Fatal("accepted display label as identity")
	}
	text, ids, err := validateQuestionAnswer(item, []string{"yes"}, true)
	if err != nil || len(ids) != 0 || text[0] != "yes" {
		t.Fatal("custom text misinterpreted as option")
	}
}
func TestQuestionFactsExcludedFromModelAndPreservedByCompaction(t *testing.T) {
	t.Parallel()
	a := newQuestionTestAgent(t)
	createTestQuestions(t, a, questionItems("Choice"), false)
	msgs := a.ctxMgr.Snapshot()
	if len(filterQuestionStateMessages(msgs)) != 0 {
		t.Fatal("local metadata leaked into model")
	}
	retained := retainQuestionFacts(msgs, []message.Message{{Role: message.RoleUser, Content: "Summary"}})
	a.restoreQuestions(retained)
	if len(a.questions.records) != 1 {
		t.Fatal("compaction discarded pending question")
	}
	var fact QuestionFact
	if json.Unmarshal(retained[1].Question, &fact) != nil || len(fact.Updates) != 1 {
		t.Fatal("missing durable fact")
	}
}

func testQuestionTool() *tools.QuestionTool {
	return tools.NewQuestionTool(func(context.Context, tools.QuestionArgs) (tools.QuestionResult, error) {
		return tools.QuestionResult{}, nil
	})
}
