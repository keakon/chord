package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

const (
	questionOpConsume     = "consume"
	questionOpComplete    = "complete_task"
	QuestionOpCancelTask  = "cancel_task"
	QuestionOpNewTask     = "new_task"
	EventQuestionConsumed = "question_consumed"
)

var errQuestionHistoryRewriteUnsettled = errors.New("question transaction must settle or recover before rewriting history")

func (a *MainAgent) interruptQuestionWaits() {
	for id, w := range a.questions.waits {
		result, _ := a.questionWaitResult(w)
		result.Status = tools.QuestionStatusWaitInterrupted
		questionReply(w.command, QuestionReceipt{Accepted: true, Result: result})
		delete(a.questions.waits, id)
	}
	a.questions.waiting = false
	a.questionWaiting.Store(false)
	a.publishQuestionWaits()
}
func (a *MainAgent) pauseQuestionWork() {
	if len(a.questions.records) == 0 {
		return
	}
	a.interruptQuestionWaits()
	// One internal pause barrier is sufficient even when repeated cancellation
	// or session-switch requests arrive before its durable commit finishes.
	if active := a.questions.active; active != nil && active.fact.Operation == questionOpPause {
		return
	}
	for _, queued := range a.questions.queue {
		if queued.operation.Operation == questionOpPause {
			return
		}
	}
	a.handleQuestionCommand(&questionCommand{
		operation: QuestionOperation{Operation: questionOpPause, OperationID: makeRequestID()}, ctx: a.parentCtx,
		onReply: func(receipt QuestionReceipt) {
			// A failed commit is handled after queued commands are rejected.
			// Rejections before dispatch have no commit event to finish the barrier.
			if receipt.Error != "" && !a.questions.failed && a.questions.switchEvent != nil {
				a.questions.switchEvent = nil
				a.questions.switching = false
				a.emitToTUI(ErrorEvent{Err: fmt.Errorf("pause questions before session switch: %s", receipt.Error)})
			}
		},
	})
}
func (a *MainAgent) prepareQuestionSessionSwitch(evt Event) bool {
	if a.questions.failed {
		// The failed write may already be on disk. Navigation can retire this
		// runtime and let resume replay durable facts, but fork must not copy
		// the incomplete in-memory history or rewrite the source transcript.
		a.questions.switchEvent = nil
		a.questions.switching = false
		payload, ok := evt.Payload.(*sessionControlPayload)
		if ok && payload.Kind == sessionControlFork {
			a.emitToTUI(ErrorEvent{Err: fmt.Errorf("question persistence requires session recovery before forking; resume the session or start a new one")})
			return true
		}
		a.interruptQuestionWaits()
		return false
	}
	if len(a.questions.records) == 0 || a.questions.paused {
		return false
	}
	a.questions.switchEvent = &evt
	a.questions.switching = true
	a.pauseQuestionWork()
	return true
}
func (a *MainAgent) prepareQuestionLifecycle(c *questionCommand) (QuestionFact, message.Message, QuestionReceipt, error) {
	r := &a.questions
	fact := QuestionFact{TransactionID: c.operation.OperationID, Fingerprint: questionFingerprint(c.operation), Operation: c.operation.Operation}
	if old, ok := r.transactions[fact.TransactionID]; ok {
		if old.Fingerprint != fact.Fingerprint {
			return QuestionFact{}, message.Message{}, QuestionReceipt{}, fmt.Errorf("question operation ID reused with different arguments")
		}
		return QuestionFact{}, message.Message{}, r.receipts[fact.TransactionID], nil
	}
	msg := message.Message{Role: message.RoleSystem, Kind: message.KindQuestionState}
	receipt := QuestionReceipt{Accepted: true}
	switch c.operation.Operation {
	case questionOpComplete, QuestionOpCancelTask:
		if c.operation.Operation == QuestionOpCancelTask && strings.TrimSpace(c.operation.UserText) == "" {
			return fact, msg, receipt, fmt.Errorf("cancel_task requires explicit user text")
		}
		if c.operation.Operation == questionOpComplete && a.unconsumedQuestionResults() > 0 {
			return fact, msg, receipt, fmt.Errorf("question decisions have not been consumed")
		}
		fact.ClosedScopeID = c.scope
		for _, q := range r.records {
			if q.ScopeID != c.scope || q.TaskID != identity.MainAgentID {
				continue
			}
			if c.operation.Operation == questionOpComplete && q.Dependency == QuestionDependencyPending {
				return fact, msg, receipt, fmt.Errorf("required decisions are pending")
			}
			if q.pending() {
				q.Outcome = tools.QuestionOutcomeCancelled
				q.Reason = c.operation.Operation
				q.ResultID = makeRequestID()
				q.Timer = QuestionTimerDisabled
				q.Deadline = time.Time{}
				q.Version++
				if c.operation.Operation == QuestionOpCancelTask {
					q.Dependency = QuestionDependencyWithdrawn
					q.UserSourceID = fact.TransactionID
				}
				fact.Updates = append(fact.Updates, q)
			} else if c.operation.Operation == QuestionOpCancelTask && q.Dependency == QuestionDependencyPending {
				q.Dependency = QuestionDependencyWithdrawn
				q.UserSourceID = fact.TransactionID
				q.Version++
				fact.Updates = append(fact.Updates, q)
			}
		}
		if c.operation.Operation == QuestionOpCancelTask {
			fact.SourceID = fact.TransactionID
			msg.Role = message.RoleUser
			msg.Kind = ""
			msg.Content = c.operation.UserText
		}
	case questionOpPause, questionOpResume:
		paused := c.operation.Operation == questionOpPause
		fact.Paused = new(paused)
		for _, q := range r.records {
			if !q.pending() {
				continue
			}
			if !paused && (q.Timer == QuestionTimerSuspended || q.Timer == QuestionTimerAwaiting) {
				q.Version++
				fact.Updates = append(fact.Updates, q)
			}
			if paused && q.Timer == QuestionTimerArmed {
				q.Remaining = max(q.Deadline.Sub(c.received), 0)
				q.Timer = QuestionTimerSuspended
				q.Deadline = time.Time{}
				q.Version++
				fact.Updates = append(fact.Updates, q)
			}
		}
	case QuestionOpNewTask:
		if c.operation.UserText == "" {
			return fact, msg, receipt, fmt.Errorf("new_task requires explicit user text")
		}
		fact.ScopeID = makeRequestID()
		fact.SourceID = fact.TransactionID
		msg.Role = message.RoleUser
		msg.Kind = ""
		msg.Content = c.operation.UserText
	default:
		return fact, msg, receipt, fmt.Errorf("unknown question lifecycle operation")
	}
	fact.Receipt = receipt
	data, err := json.Marshal(fact)
	if err != nil {
		return fact, msg, receipt, fmt.Errorf("encode question lifecycle: %w", err)
	}
	msg.Question = data
	return fact, msg, receipt, nil
}

func (a *MainAgent) parkForRequiredQuestions() bool {
	ids := a.requiredQuestionIDs()
	if len(ids) == 0 {
		return false
	}
	a.questions.waiting = true
	a.questionWaiting.Store(true)
	a.setIdleAndDrainPending()
	if a.turn == nil {
		a.emitActivity(identity.MainAgentID, ActivityWaitingInput, "Waiting for required answers")
	}
	return true
}

func questionResultsInMessages(msgs []message.Message) []string {
	var ids []string
	for _, msg := range msgs {
		if len(msg.Question) > 0 && msg.Kind != message.KindQuestionState {
			var fact QuestionFact
			if json.Unmarshal(msg.Question, &fact) == nil && fact.Result != nil && fact.Result.ResultID != "" {
				ids = append(ids, fact.Result.ResultID)
			}
		}
		if msg.Role == message.RoleTool {
			var result tools.QuestionResult
			if json.Unmarshal([]byte(msg.ToolPayload), &result) != nil {
				continue
			}
			for _, a := range result.Answers {
				if a.ResultID != "" {
					ids = append(ids, a.ResultID)
				}
			}
		}
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}
func (a *MainAgent) attachQuestionConsumption(msg *message.Message, ids []string) {
	if len(ids) == 0 {
		return
	}
	fact := QuestionFact{TransactionID: fmt.Sprintf("consume:%s:%d", a.questions.scope, msg.RequestBatch), Operation: questionOpConsume, Consumed: ids}
	data, err := json.Marshal(fact)
	if err == nil {
		msg.Question = data
	}
}

func retainQuestionFacts(original, compacted []message.Message) []message.Message {
	seen := map[string]bool{}
	for _, msg := range compacted {
		var fact QuestionFact
		if json.Unmarshal(msg.Question, &fact) == nil {
			seen[fact.TransactionID] = true
		}
	}
	var retained []message.Message
	for _, msg := range original {
		var fact QuestionFact
		if len(msg.Question) == 0 || json.Unmarshal(msg.Question, &fact) != nil || seen[fact.TransactionID] {
			continue
		}
		// The checkpoint summarizes archived conversation content. Keep only
		// the durable fact here; unconsumed decisions are projected into the
		// next request from the restored question state.
		retained = append(retained, message.Message{
			Role: message.RoleSystem, Kind: message.KindQuestionState,
			Question: msg.Question, RequestBatch: msg.RequestBatch,
		})
	}
	if len(compacted) == 0 {
		return retained
	}
	// The checkpoint remains the transcript head for transaction recovery
	// and session previews. Facts keep their order before the retained tail.
	result := make([]message.Message, 0, len(compacted)+len(retained))
	result = append(result, compacted[0])
	result = append(result, retained...)
	return append(result, compacted[1:]...)
}
func (a *MainAgent) resumeQuestionWork() bool {
	if !a.questions.paused {
		return false
	}
	a.handleQuestionCommand(&questionCommand{operation: QuestionOperation{Operation: questionOpResume, OperationID: makeRequestID()}, ctx: context.Background()})
	// Resuming owns the continuation only when a pending question will actually
	// be re-presented or resumed. With none this is bookkeeping, and reporting
	// it as handled would swallow the caller's continue and leave the session
	// idle with nothing to do.
	for _, q := range a.questions.records {
		if q.ScopeID == a.questions.scope && q.TaskID == identity.MainAgentID && q.pending() {
			return true
		}
	}
	return false
}

// stopQuestionWork runs on the exiting event loop. The shutdown writer records
// the frozen time after the persistence queue has drained.
func (a *MainAgent) stopQuestionWork() {
	r := &a.questions
	r.stoppedAt = time.Now()
	for _, timer := range r.timers {
		timer.Stop()
	}
	for id, timer := range r.retryTimers {
		timer.Stop()
		delete(r.retryTimers, id)
	}
	for _, wait := range r.waits {
		questionReply(wait.command, QuestionReceipt{Error: ErrAgentShutdown.Error()})
	}
	for _, c := range r.queue {
		questionReply(c, QuestionReceipt{Error: ErrAgentShutdown.Error()})
	}
}

// persistQuestionShutdown runs after Run and the persistence pump have stopped.
// Re-reading the committed transcript includes an outcome whose receipt raced
// shutdown. A successful pause sync also establishes durability of those facts.
func (a *MainAgent) persistQuestionShutdown() {
	manager := a.recoveryManager()
	if manager == nil || len(a.questions.records) == 0 {
		return
	}
	msgs, err := manager.LoadMessages(identity.MainAgentID)
	if err != nil {
		a.notePersistenceFailure(err)
		return
	}
	stoppedAt := a.questions.stoppedAt
	if stoppedAt.IsZero() {
		stoppedAt = time.Now()
	}
	r := &a.questions
	for _, msg := range msgs {
		var fact QuestionFact
		if len(msg.Question) == 0 || json.Unmarshal(msg.Question, &fact) != nil {
			continue
		}
		for _, q := range fact.Updates {
			r.records[q.ID] = q
		}
	}
	c := &questionCommand{operation: QuestionOperation{Operation: questionOpPause, OperationID: makeRequestID()}, received: stoppedAt}
	_, msg, _, err := a.prepareQuestionLifecycle(c)
	if err == nil {
		err = manager.PersistMessageDurable(identity.MainAgentID, msg)
	}
	if err != nil {
		a.notePersistenceFailure(err)
	}
}

// Runtime dependencies remain discoverable after transcript compaction. Local
// timers, transaction IDs and client receipts are never sent to the model.
type questionRequestProjection struct {
	scope          string
	records        map[string]QuestionSnapshot
	transactions   map[string]QuestionFact
	transactionIDs []string // committed order of transactions
	unconsumed     map[string]bool
}
type questionRequestContextKey struct{}
type questionRequestObservation struct {
	projection questionRequestProjection
	results    []string
}

func (a *MainAgent) snapshotQuestionRequest() questionRequestProjection {
	r := &a.questions
	projection := questionRequestProjection{scope: r.scope, records: map[string]QuestionSnapshot{}, transactions: map[string]QuestionFact{}, unconsumed: map[string]bool{}}
	for id, q := range r.records {
		if q.ScopeID == r.scope && q.TaskID == identity.MainAgentID && (q.pending() || q.Dependency == QuestionDependencyPending) {
			projection.records[id] = q
		}
	}
	for _, id := range r.transactionOrder {
		fact, ok := r.transactions[id]
		if !ok {
			continue
		}
		if fact.Result == nil || !r.unconsumed[fact.Result.ResultID] {
			continue
		}
		q, ok := r.records[fact.Result.QuestionID]
		if !ok || q.ScopeID != r.scope || q.TaskID != identity.MainAgentID {
			continue
		}
		projection.records[q.ID] = q
		projection.transactions[id] = fact
		projection.transactionIDs = append(projection.transactionIDs, id)
		projection.unconsumed[fact.Result.ResultID] = true
	}
	return projection
}

func questionRequestContext(r questionRequestProjection, msgs []message.Message) []message.Message {
	type pending struct {
		ID         string `json:"id"`
		Header     string `json:"header"`
		Text       string `json:"question"`
		Outcome    string `json:"outcome,omitempty"`
		Dependency string `json:"dependency,omitempty"`
	}
	visible := make([]message.Message, 0, len(msgs))
	for _, msg := range msgs {
		var fact QuestionFact
		if len(msg.Question) > 0 && json.Unmarshal(msg.Question, &fact) == nil && (fact.Result != nil || fact.SourceID != "") && len(fact.Updates) > 0 && (fact.Updates[0].ScopeID != r.scope || fact.Updates[0].TaskID != identity.MainAgentID) {
			continue
		}
		visible = append(visible, msg)
	}
	seen := make(map[string]bool)
	for _, id := range questionResultsInMessages(visible) {
		seen[id] = true
	}
	for _, transactionID := range r.transactionIDs {
		fact := r.transactions[transactionID]
		if fact.Result == nil || !r.unconsumed[fact.Result.ResultID] || seen[fact.Result.ResultID] {
			continue
		}
		q, ok := r.records[fact.Result.QuestionID]
		if !ok || q.ScopeID != r.scope || q.TaskID != identity.MainAgentID {
			continue
		}
		data, _ := json.Marshal(fact.Result)
		meta, _ := json.Marshal(QuestionFact{Result: fact.Result})
		source := "User decision: "
		if fact.Result.Outcome == tools.QuestionOutcomeDefaulted || fact.Result.Outcome == tools.QuestionOutcomeNoResponse {
			source = "System decision (not user authorization): "
		}
		visible = append(visible, message.Message{Role: message.RoleSystem, Content: source + string(data), Question: meta})
		seen[fact.Result.ResultID] = true
	}
	var questions []pending
	for _, q := range r.records {
		if q.ScopeID == r.scope && q.TaskID == identity.MainAgentID && (q.pending() || q.Dependency == QuestionDependencyPending) {
			questions = append(questions, pending{q.ID, q.Item.Header, q.Item.Question, q.Outcome, q.Dependency})
		}
	}
	if len(questions) == 0 {
		return visible
	}
	slices.SortFunc(questions, func(x, y pending) int { return strings.Compare(x.ID, y.ID) })
	data, _ := json.Marshal(questions)
	return append(visible, message.Message{Role: message.RoleSystem, Content: "Current questions: " + string(data) + ". Continue independent work; use Question(wait_for) only when these decisions are needed. A declined question still requires a user decision or explicit withdrawal. Never recreate pending questions."})
}

func (a *MainAgent) publishRestoredQuestions() {
	a.publishQuestionWaits()
	for _, q := range a.questions.records {
		if q.Visible || !q.pending() {
			a.emitToTUI(QuestionStateEvent{Question: projectQuestionSnapshot(q, &a.questions), Restored: true})
		}
	}
}

// Completion closes unused preferences before a later timer can produce a decision.
func (a *MainAgent) closeCompletedQuestions() {
	if a.questions.resumeNeeded || a.questionCompletionPending.Load() > 0 || a.questions.completed[a.questions.scope] {
		return
	}
	found := false
	for _, q := range a.questions.records {
		if q.ScopeID == a.questions.scope && q.TaskID == identity.MainAgentID {
			found = true
			break
		}
	}
	if !found {
		return
	}
	a.handleQuestionCommand(&questionCommand{operation: QuestionOperation{Operation: questionOpComplete, OperationID: makeRequestID()}, scope: a.questions.scope, ctx: a.parentCtx})
}

// Unconsumed results belong to the active task even after their question's
// required dependency has been received. Revisions carry their own result IDs.
func (a *MainAgent) unconsumedQuestionResults() int {
	n := 0
	for _, fact := range a.questions.transactions {
		if fact.Result == nil || !a.questions.unconsumed[fact.Result.ResultID] {
			continue
		}
		q := a.questions.records[fact.Result.QuestionID]
		if q.ScopeID == a.questions.scope && q.TaskID == identity.MainAgentID {
			n++
		}
	}
	return n
}

func (a *MainAgent) updateQuestionCompletionGate() {
	a.questionCompletionPending.Store(int64(len(a.requiredQuestionIDs()) + a.unconsumedQuestionResults()))
}

// Only a durably committed assistant response acknowledges decision consumption.
// Tool dispatch also applies this acknowledgment after its intent barrier, so
// workers never race the queued consumption event when checking Done.
func (a *MainAgent) consumeQuestionResults(ids []string) {
	for _, id := range ids {
		delete(a.questions.unconsumed, id)
	}
	a.updateQuestionCompletionGate()
}

func questionCompactionMessages(msgs []message.Message) []message.Message {
	visible := filterQuestionStateMessages(msgs)
	for i := range visible {
		visible[i].Question = nil
	}
	return visible
}
