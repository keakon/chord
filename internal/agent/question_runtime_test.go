package agent

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestQuestionStalePresentationAndTimerCannotRearm(t *testing.T) {
	a := newQuestionTestAgent(t)
	a.globalConfig.QuestionAutoSelectTimeout = 3600
	items := questionItems("Choice")
	items[0].ResponsePolicy = tools.QuestionPolicyDefaultAllowed
	items[0].DefaultOptionID = "no"
	id := createTestQuestions(t, a, items, false).Result.QuestionIDs[0]
	old := a.questions.records[id].Version
	applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpPresented, QuestionID: id, SupportsInteraction: true}, time.Now())
	applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpInteract, QuestionID: id}, time.Now())
	if got := applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpPresented, QuestionID: id, Version: old, SupportsInteraction: true}, time.Now()); got.Accepted {
		t.Fatal("stale presentation accepted")
	}
	before := a.questions.records[id].Version
	applyTestQuestion(t, a, QuestionOperation{Operation: questionOpExpire, QuestionID: id, Version: old}, time.Now().Add(2*time.Hour))
	if q := a.questions.records[id]; q.Version != before || !q.pending() || q.Timer != QuestionTimerCancelled {
		t.Fatalf("stale timer changed state: %+v", q)
	}
}

func TestQuestionPauseRetainsZeroRemainingAndShutdownCommit(t *testing.T) {
	a := newQuestionTestAgent(t)
	a.globalConfig.QuestionTimeout = 3600
	items := questionItems("Choice")
	items[0].ResponsePolicy = tools.QuestionPolicyOptional
	id := createTestQuestions(t, a, items, false).Result.QuestionIDs[0]
	now := time.Now()
	applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpPresented, QuestionID: id, SupportsInteraction: true}, now)
	deadline := a.questions.records[id].Deadline
	runQuestionCommand(t, a, &questionCommand{operation: QuestionOperation{Operation: questionOpPause, OperationID: "pause"}, received: deadline})
	if q := a.questions.records[id]; q.Remaining != 0 || q.Timer != QuestionTimerSuspended {
		t.Fatalf("pause: %+v", q)
	}
	runQuestionCommand(t, a, &questionCommand{operation: QuestionOperation{Operation: questionOpResume, OperationID: "resume"}})
	applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpPresented, QuestionID: id, SupportsInteraction: true}, deadline.Add(time.Second))
	if a.questions.records[id].Outcome != tools.QuestionOutcomeNoResponse {
		t.Fatal("zero remaining gained another timeout")
	}
	// A committed write whose acknowledgment did not reach the loop is still
	// included by the shutdown read and gets a durable suspended timer.
	b := newQuestionTestAgent(t)
	b.globalConfig.QuestionTimeout = 3600
	id = createTestQuestions(t, b, items, false).Result.QuestionIDs[0]
	applyTestQuestion(t, b, QuestionOperation{Operation: QuestionOpPresented, QuestionID: id, SupportsInteraction: true}, now)
	b.questions.stoppedAt = now.Add(10 * time.Second)
	b.persistQuestionShutdown()
	msgs, err := b.recoveryManager().LoadMessages(identity.MainAgentID)
	if err != nil {
		t.Fatal(err)
	}
	b.restoreQuestions(msgs)
	q := b.questions.records[id]
	if q.Timer != QuestionTimerSuspended || q.Remaining != time.Hour-10*time.Second {
		t.Fatalf("shutdown remaining: %+v", q)
	}
}

func TestQuestionRestoreRebuildsCompleteBatchToolResult(t *testing.T) {
	a := newQuestionTestAgent(t)
	args := tools.QuestionArgs{Questions: questionItems("First", "Second", "Third")}
	raw, _ := json.Marshal(args)
	intent := message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call-1", Name: tools.NameQuestion, Args: raw}}}
	a.ctxMgr.Append(intent)
	r := runQuestionCommand(t, a, &questionCommand{operation: QuestionOperation{Operation: questionOpCreate, OperationID: "batch"}, call: "call-1", args: args})
	applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpAnswer, QuestionID: r.Result.QuestionIDs[0], Answers: []string{"no"}}, time.Time{})
	msgs := append(a.ctxMgr.Snapshot(), message.Message{Role: message.RoleTool, ToolCallID: "call-1", ToolRecoveryState: message.ToolRecoveryStateOutcomeUnknown})
	restored := restoreQuestionToolResults(msgs)
	var result tools.QuestionResult
	tool := restored[len(restored)-1]
	if err := json.Unmarshal([]byte(tool.ToolPayload), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != tools.QuestionStatusWaitInterrupted || len(result.Answers) != 1 || !slices.Equal(result.PendingIDs, r.Result.QuestionIDs[1:]) || tool.ToolRecoveryState != "" {
		t.Fatalf("restored result: %+v %+v", tool, result)
	}
	a.restoreQuestions(restored)
	if !a.questions.records[r.Result.QuestionIDs[1]].Visible || a.questions.records[r.Result.QuestionIDs[2]].Visible {
		t.Fatal("lost sequential progress")
	}
}

func TestQuestionResultRequestSnapshotAndRetry(t *testing.T) {
	a := newQuestionTestAgent(t)
	id := createTestQuestions(t, a, questionItems("Choice"), false).Result.QuestionIDs[0]
	before := a.snapshotQuestionRequest()
	applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpAnswer, QuestionID: id, Answers: []string{"no"}}, time.Time{})
	resultID := a.questions.records[id].ResultID
	if len(questionResultsInMessages(questionRequestContext(before, nil))) != 0 {
		t.Fatal("in-flight request changed")
	}
	projection := a.snapshotQuestionRequest()
	first := questionRequestContext(projection, nil)
	retry := questionRequestContext(projection, nil)
	if !slices.Equal(questionResultsInMessages(first), []string{resultID}) || !slices.Equal(questionResultsInMessages(retry), []string{resultID}) || !a.questions.unconsumed[resultID] {
		t.Fatal("retry lost decision or consumed early")
	}
	wire := questionCompactionMessages(a.ctxMgr.Snapshot())
	for _, m := range wire {
		if len(m.Question) != 0 || m.Kind == message.KindQuestionState {
			t.Fatal("compaction leaked local metadata")
		}
	}
}

func TestQuestionTaskCompletionAndIndependentScope(t *testing.T) {
	a := newQuestionTestAgent(t)
	required := createTestQuestions(t, a, questionItems("Required"), false).Result.QuestionIDs[0]
	oldScope := a.questions.scope
	c := &questionCommand{operation: QuestionOperation{Operation: QuestionOpNewTask, OperationID: "new-task", UserText: "Start another task"}}
	r := runQuestionCommand(t, a, c)
	if !r.Accepted || a.questions.scope == oldScope || len(a.requiredQuestionIDs()) != 0 {
		t.Fatal("old requirement leaked into new task")
	}
	scope := a.questions.scope
	retry := *c
	retry.reply = nil
	runQuestionCommand(t, a, &retry)
	if a.questions.scope != scope {
		t.Fatal("retry created a second scope")
	}
	items := questionItems("Preference")
	items[0].ResponsePolicy = tools.QuestionPolicyOptional
	id := createTestQuestions(t, a, items, false).Result.QuestionIDs[0]
	runQuestionCommand(t, a, &questionCommand{operation: QuestionOperation{Operation: questionOpComplete, OperationID: "complete"}})
	if q := a.questions.records[id]; q.Outcome != tools.QuestionOutcomeCancelled || q.Timer != QuestionTimerDisabled {
		t.Fatal("unused preference stayed open")
	}
	a.restoreQuestions(a.ctxMgr.Snapshot())
	if !a.questions.completed[scope] || !a.questions.records[required].pending() {
		t.Fatal("completion lost scope boundary")
	}
}

func TestWalltimeQuestionAnswerClassifiesAsUserWaitOnly(t *testing.T) {
	a := newQuestionTestAgent(t)
	ctx := tools.WithAgentID(tools.WithTaskID(tools.WithTurnID(context.Background(), a.turn.ID), identity.MainAgentID), identity.MainAgentID)
	done := make(chan error, 1)
	go func() {
		_, err := a.ExecuteQuestion(ctx, tools.QuestionArgs{Questions: questionItems("Choice")})
		done <- err
	}()
	timeout := time.After(3 * time.Second)
	for len(a.questions.waits) == 0 {
		select {
		case evt := <-a.eventCh:
			switch evt.Type {
			case EventQuestionCommand:
				a.handleQuestionCommand(evt.Payload.(*questionCommand))
			case EventQuestionCommitted:
				a.handleQuestionCommitted(evt.Payload.(*questionCommit))
			}
		case <-timeout:
			t.Fatal("wait not installed")
		}
	}
	var id string
	for qid := range a.questions.records {
		id = qid
	}
	applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpAnswer, QuestionID: id, Answers: []string{"yes"}}, time.Time{})
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-timeout:
		t.Fatal("answer did not wake worker")
	}
	stats := a.walltime.statsForAgent(identity.MainAgentID)
	if stats.UserWait <= 0 || stats.Tool != 0 {
		t.Fatalf("walltime: %+v", stats)
	}
}

func TestQuestionReceiptRetryAfterNewScopeAndRecovery(t *testing.T) {
	a := newQuestionTestAgent(t)
	id := createTestQuestions(t, a, questionItems("Choice"), false).Result.QuestionIDs[0]
	op := QuestionOperation{Operation: QuestionOpAnswer, OperationID: "answer-retry", QuestionID: id, Answers: []string{"no"}}
	first := runQuestionCommand(t, a, &questionCommand{operation: op})
	runQuestionCommand(t, a, &questionCommand{operation: QuestionOperation{Operation: QuestionOpNewTask, OperationID: "another-task", UserText: "Another task"}})
	a.restoreQuestions(a.ctxMgr.Snapshot())
	before := a.ctxMgr.MessageCount()
	retry := runQuestionCommand(t, a, &questionCommand{operation: op})
	if !retry.Accepted || retry.Version != first.Version || before != a.ctxMgr.MessageCount() {
		t.Fatalf("recovered receipt retry: %+v %+v", first, retry)
	}
}

func TestQuestionUnknownCommitPreventsCompactionRewrite(t *testing.T) {
	a := newQuestionTestAgent(t)
	a.questions.failed = true
	if err := a.applyCompactionDraftAsync(&compactionDraft{}); err == nil {
		t.Fatal("rewrote history with an unresolved write outcome")
	}
	a.questions.failed = false
	a.questions.active = &questionCommit{}
	if err := a.applyCompactionDraftAsync(&compactionDraft{}); err == nil {
		t.Fatal("rewrote history before question commit publication")
	}
	a.questions.active = nil
}

func TestQuestionFollowupCreationReactivatesCompletedScope(t *testing.T) {
	a := newQuestionTestAgent(t)
	optional := questionItems("Preference")
	optional[0].ResponsePolicy = tools.QuestionPolicyOptional
	createTestQuestions(t, a, optional, false)
	runQuestionCommand(t, a, &questionCommand{operation: QuestionOperation{Operation: questionOpComplete, OperationID: "complete-first"}})
	if !a.questions.completed[a.questions.scope] {
		t.Fatal("scope not completed")
	}
	id := createTestQuestions(t, a, questionItems("Followup"), false).Result.QuestionIDs[0]
	if a.questions.completed[a.questions.scope] {
		t.Fatal("new decision cannot resume its task")
	}
	a.restoreQuestions(a.ctxMgr.Snapshot())
	if a.questions.completed[a.questions.scope] || !a.questions.records[id].pending() {
		t.Fatal("recovery lost followup activation")
	}
}

func TestQuestionInFlightRequestCannotAttachToIndependentTask(t *testing.T) {
	a := newQuestionTestAgent(t)
	a.turn.questionRequestScope = a.questions.scope
	runQuestionCommand(t, a, &questionCommand{operation: QuestionOperation{Operation: QuestionOpNewTask, OperationID: "independent-task", UserText: "Start another task"}})
	before := a.ctxMgr.MessageCount()
	stale := runQuestionCommand(t, a, &questionCommand{operation: QuestionOperation{Operation: questionOpCreate, OperationID: "stale-create"}, turn: a.turn.ID, args: tools.QuestionArgs{Questions: questionItems("Previous request"), Wait: new(false)}})
	if stale.Accepted || stale.Error == "" || a.ctxMgr.MessageCount() != before || len(a.questions.records) != 0 {
		t.Fatalf("old request created questions in new task: %+v", stale)
	}
	a.turn.questionRequestScope = a.questions.scope
	current := runQuestionCommand(t, a, &questionCommand{operation: QuestionOperation{Operation: questionOpCreate, OperationID: "current-create"}, turn: a.turn.ID, args: tools.QuestionArgs{Questions: questionItems("Current request"), Wait: new(false)}})
	if !current.Accepted || len(current.Result.QuestionIDs) != 1 {
		t.Fatalf("current request rejected: %+v", current)
	}
}
