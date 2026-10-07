package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

const (
	EventQuestionCommand        = "question_command"
	EventQuestionCommitted      = "question_committed"
	EventQuestionCommitRetry    = "question_commit_retry"
	QuestionOpPresented         = "presented"
	QuestionOpInteract          = "interact"
	QuestionOpAnswer            = "answer"
	QuestionOpDecline           = "decline"
	QuestionOpRevise            = "revise"
	QuestionOpWithdraw          = "withdraw_requirement"
	QuestionOpReplace           = "replace_requirement"
	questionOpCreate            = "create"
	questionOpWait              = "wait"
	questionOpUnwait            = "unwait"
	questionOpExpire            = "expire"
	questionOpSnapshot          = "snapshot"
	questionOpPause             = "pause"
	questionOpResume            = "resume"
	QuestionTimerDisabled       = "disabled"
	QuestionTimerAwaiting       = "awaiting_presentation"
	QuestionTimerArmed          = "armed"
	QuestionTimerCancelled      = "cancelled"
	QuestionTimerSuspended      = "suspended"
	QuestionDependencyPending   = "awaiting_decision"
	QuestionDependencyReceived  = "decision_received"
	QuestionDependencyWithdrawn = "withdrawn"
	QuestionDependencyReplaced  = "replaced"
	maxQuestionOperations       = 64
	questionCommitRetryDelay    = 10 * time.Millisecond
)

// QuestionSnapshot is an immutable projection of committed question facts.
type QuestionSnapshot struct {
	BindingID    string             `json:"binding_id"`
	ID           string             `json:"request_id"`
	BatchID      string             `json:"batch_id"`
	AgentID      string             `json:"agent_id"`
	TaskID       string             `json:"task_id"`
	ScopeID      string             `json:"request_scope_id"`
	CallID       string             `json:"tool_call_id"`
	TurnID       uint64             `json:"turn_id"`
	Index        int                `json:"index"`
	Item         tools.QuestionItem `json:"item"`
	Version      uint64             `json:"version"`
	Async        bool               `json:"async"`
	Visible      bool               `json:"visible"`
	Outcome      string             `json:"outcome,omitempty"`
	Reason       string             `json:"reason,omitempty"`
	ResultID     string             `json:"result_id,omitempty"`
	Selected     []string           `json:"selected,omitempty"`
	SelectedIDs  []string           `json:"selected_ids,omitempty"`
	Timer        string             `json:"timer"`
	Duration     time.Duration      `json:"duration_ns,omitempty"`
	Remaining    time.Duration      `json:"remaining_ns,omitempty"`
	Deadline     time.Time          `json:"deadline"`
	Dependency   string             `json:"dependency,omitempty"`
	ReplacedBy   string             `json:"replaced_by,omitempty"`
	UserSourceID string             `json:"user_source_id,omitempty"`
	// Revision is the latest durable revise answer for this question, projected
	// into status and events. Outcome and ResultID keep naming the original decision.
	Revision *tools.QuestionAnswer `json:"revision,omitempty"`
}

func (q QuestionSnapshot) pending() bool { return q.Outcome == "" }
func (q QuestionSnapshot) answer() tools.QuestionAnswer {
	return tools.QuestionAnswer{QuestionID: q.ID, ResultID: q.ResultID, Header: q.Item.Header, Outcome: q.Outcome, Selected: slices.Clone(q.Selected), SelectedIDs: slices.Clone(q.SelectedIDs)}
}

// QuestionFact is stored on a transcript message, never in a separate answer log.
type QuestionFact struct {
	Operation     string                `json:"operation"`
	ScopeID       string                `json:"scope_id,omitempty"`
	Paused        *bool                 `json:"paused,omitempty"`
	Receipt       QuestionReceipt       `json:"receipt"`
	TransactionID string                `json:"transaction_id"`
	Fingerprint   string                `json:"fingerprint"`
	Updates       []QuestionSnapshot    `json:"updates,omitempty"`
	Result        *tools.QuestionAnswer `json:"result,omitempty"`
	Consumed      []string              `json:"consumed,omitempty"`
	ClosedScopeID string                `json:"closed_scope_id,omitempty"`
	SourceID      string                `json:"source_id,omitempty"`
}

// QuestionOperation represents an explicit client action. Option selections use IDs;
// Custom marks free text so text equal to an option ID is never misinterpreted.
type QuestionOperation struct {
	BindingID           string   `json:"binding_id,omitempty"`
	Operation           string   `json:"operation"`
	OperationID         string   `json:"operation_id"`
	QuestionID          string   `json:"request_id"`
	Version             uint64   `json:"version,omitempty"`
	Answers             []string `json:"answers,omitempty"`
	Custom              bool     `json:"custom,omitempty"`
	ReplacementID       string   `json:"replacement_id,omitempty"`
	UserText            string   `json:"user_text,omitempty"`
	SupportsInteraction bool     `json:"supports_interaction,omitempty"`
}

type QuestionReceipt struct {
	SessionID   string               `json:"session_id,omitempty"`
	Accepted    bool                 `json:"accepted"`
	Status      string               `json:"status,omitempty"`
	Version     uint64               `json:"version,omitempty"`
	Error       string               `json:"error,omitempty"`
	Result      tools.QuestionResult `json:"result"`
	Questions   []QuestionSnapshot   `json:"questions,omitempty"`
	BindingID   string               `json:"binding_id,omitempty"`
	NextAfterID string               `json:"next_after_id,omitempty"`
}

type questionCommand struct {
	operation                QuestionOperation
	args                     tools.QuestionArgs
	owner, task, scope, call string
	turn                     uint64
	received                 time.Time
	reply                    chan QuestionReceipt
	onReply                  func(QuestionReceipt)
	ctx                      context.Context
	snapshotQuery            QuestionSnapshotQuery
}

type questionWait struct {
	command  *questionCommand
	ids      []string
	original bool
}
type questionCommit struct {
	command *questionCommand
	fact    QuestionFact
	msg     message.Message
	receipt QuestionReceipt
	manager *recovery.RecoveryManager
	err     error
}
type questionRuntime struct {
	stoppedAt            time.Time
	failed               bool
	switching            bool
	switchEvent          *Event
	records              map[string]QuestionSnapshot
	recordOrder          []string // creation order rebuilt from durable facts
	transactions         map[string]QuestionFact
	transactionOrder     []string          // durable commit order of transaction IDs
	revisionTransactions map[string]string // latest durable revision transaction per question
	receipts             map[string]QuestionReceipt
	timers               map[string]*time.Timer
	retryTimers          map[string]*time.Timer
	waits                map[string]*questionWait
	queue                []*questionCommand
	active               *questionCommit
	binding              string
	scope                string
	paused               bool
	waiting              bool
	resumeNeeded         bool
	completed            map[string]bool
	unconsumed           map[string]bool
}

func (a *MainAgent) initQuestions() {
	r := &a.questions
	if r.records != nil {
		return
	}
	*r = questionRuntime{records: map[string]QuestionSnapshot{}, transactions: map[string]QuestionFact{}, revisionTransactions: map[string]string{}, receipts: map[string]QuestionReceipt{}, timers: map[string]*time.Timer{}, retryTimers: map[string]*time.Timer{}, waits: map[string]*questionWait{}, scope: makeRequestID(), binding: makeRequestID(), unconsumed: map[string]bool{}, completed: map[string]bool{}}
}

// ExecuteQuestion runs on a tool worker; the event loop owns all question state.
func (a *MainAgent) ExecuteQuestion(ctx context.Context, args tools.QuestionArgs) (tools.QuestionResult, error) {
	a.toolWg.Add(1)
	defer a.toolWg.Done()
	owner := a.agentIDForInteraction(ctx)
	if owner != identity.MainAgentID && (len(args.WaitFor) > 0 || !args.Waits()) {
		return tools.QuestionResult{}, fmt.Errorf("asynchronous questions require the main agent")
	}
	c := &questionCommand{operation: QuestionOperation{Operation: questionOpCreate, OperationID: makeRequestID()}, args: args, owner: owner, task: tools.TaskIDFromContext(ctx), call: tools.ToolCallIDFromContext(ctx), turn: a.turnIDForInteraction(ctx), ctx: ctx, reply: make(chan QuestionReceipt, 1)}
	if c.task == "" {
		c.task = identity.MainAgentID
	}
	if len(args.WaitFor) > 0 {
		c.operation.Operation = questionOpWait
		c.operation.OperationID = makeRequestID()
	}
	waitTarget := a.walltime.captureAt(owner, a.agentNameForInteraction(owner), c.turn)
	waitStarted := time.Now()
	receipt, err := a.requestQuestion(ctx, c)
	if len(args.WaitFor) > 0 {
		a.interaction.settleWait(waitTarget, waitStarted)
	}
	if err != nil {
		return tools.QuestionResult{}, err
	}
	if receipt.Error != "" {
		return receipt.Result, fmt.Errorf("%s", receipt.Error)
	}
	if len(args.WaitFor) > 0 || !args.Waits() {
		return receipt.Result, nil
	}
	c = &questionCommand{operation: QuestionOperation{Operation: questionOpWait, OperationID: makeRequestID()}, args: tools.QuestionArgs{WaitFor: receipt.Result.QuestionIDs}, owner: owner, task: c.task, call: c.call, turn: c.turn, ctx: ctx, reply: make(chan QuestionReceipt, 1)}
	waitTarget = a.walltime.captureAt(owner, a.agentNameForInteraction(owner), c.turn)
	waitStarted = time.Now()
	receipt, err = a.requestQuestion(ctx, c)
	a.interaction.settleWait(waitTarget, waitStarted)
	if err != nil {
		return tools.QuestionResult{}, err
	}
	if receipt.Error != "" {
		return receipt.Result, fmt.Errorf("%s", receipt.Error)
	}
	return receipt.Result, nil
}

func (a *MainAgent) requestQuestion(ctx context.Context, c *questionCommand) (QuestionReceipt, error) {
	if !a.sendEventUntil(Event{Type: EventQuestionCommand, Payload: c}, ctx.Done()) {
		if err := ctx.Err(); err != nil {
			return QuestionReceipt{}, err
		}
		return QuestionReceipt{}, ErrAgentShutdown
	}
	select {
	case receipt := <-c.reply:
		return receipt, nil
	case <-ctx.Done():
		if c.operation.Operation == questionOpWait {
			a.sendEvent(Event{Type: EventQuestionCommand, Payload: &questionCommand{operation: QuestionOperation{Operation: questionOpUnwait, OperationID: c.operation.OperationID}, call: c.call, owner: c.owner, task: c.task, ctx: context.Background()}})
		}
		return QuestionReceipt{}, ctx.Err()
	case <-a.stoppingCh:
		return QuestionReceipt{}, ErrAgentShutdown
	}
}

func (a *MainAgent) ApplyQuestionOperation(ctx context.Context, op QuestionOperation) (QuestionReceipt, error) {
	if op.OperationID == "" {
		return QuestionReceipt{}, fmt.Errorf("operation_id is required")
	}
	switch op.Operation {
	case QuestionOpPresented, QuestionOpInteract, QuestionOpAnswer, QuestionOpDecline, QuestionOpRevise, QuestionOpWithdraw, QuestionOpReplace, QuestionOpNewTask, QuestionOpCancelTask:
	default:
		return QuestionReceipt{}, fmt.Errorf("invalid question operation")
	}
	return a.requestQuestion(ctx, &questionCommand{operation: op, ctx: ctx, reply: make(chan QuestionReceipt, 1)})
}
func (a *MainAgent) QuestionSnapshots(ctx context.Context, query QuestionSnapshotQuery) (QuestionReceipt, error) {
	return a.requestQuestion(ctx, &questionCommand{operation: QuestionOperation{Operation: questionOpSnapshot}, snapshotQuery: query, ctx: ctx, reply: make(chan QuestionReceipt, 1)})
}

func questionReply(c *questionCommand, r QuestionReceipt) {
	if c.onReply != nil {
		c.onReply(r)
	}
	if c.reply != nil {
		// Each request owns a capacity-one channel and consumes one receipt.
		// A cancelled caller can leave the first reply buffered; a full channel
		// already holds a reply. Never wait here: this runs on the event loop.
		select {
		case c.reply <- r:
		default:
		}
	}
}

func (a *MainAgent) handleQuestionCommand(c *questionCommand) {
	a.initQuestions()
	if c.received.IsZero() {
		c.received = time.Now()
	}
	r := &a.questions
	if c.operation.Operation == questionOpSnapshot {
		questionReply(c, a.questionSnapshotPage(c.snapshotQuery))
		return
	}
	if c.operation.Operation == questionOpUnwait {
		delete(r.waits, c.operation.OperationID)
		a.questionWaiting.Store(len(r.waits) > 0 || r.waiting)
		a.publishQuestionWaits()
		return
	}
	if r.failed {
		questionReply(c, QuestionReceipt{Error: "question persistence requires session recovery"})
		return
	}
	if r.switching && c.operation.Operation != questionOpPause {
		questionReply(c, QuestionReceipt{Error: "session switch in progress"})
		return
	}
	if r.active != nil {
		// Internal pause barriers retain a queue slot even under user-operation
		// pressure. They must settle before a deferred session switch can run.
		if len(r.queue) >= maxQuestionOperations && c.operation.Operation != questionOpPause {
			questionReply(c, QuestionReceipt{Error: "question operations are busy"})
			return
		}
		r.queue = append(r.queue, c)
		return
	}
	if c.ctx != nil && c.ctx.Err() != nil {
		questionReply(c, QuestionReceipt{Error: c.ctx.Err().Error()})
		return
	}
	if c.scope == "" {
		c.scope = r.scope
		if (c.operation.Operation == questionOpCreate || c.operation.Operation == questionOpWait) && c.owner == identity.MainAgentID && c.turn != 0 && a.turn != nil && a.turn.ID == c.turn && a.turn.questionRequestScope != "" {
			c.scope = a.turn.questionRequestScope
		}
		if q, ok := r.records[c.operation.QuestionID]; ok {
			c.scope = q.ScopeID
		}
	}
	// A request already in flight when the user starts an independent task must
	// not attach its questions or waits to the new task. The next model request
	// captures the new scope at its normal continuation boundary.
	if (c.operation.Operation == questionOpCreate || c.operation.Operation == questionOpWait) && c.owner == identity.MainAgentID && c.scope != r.scope {
		questionReply(c, QuestionReceipt{Error: "question task scope changed; process the new user request before asking or waiting"})
		return
	}
	if c.operation.Operation == questionOpWait {
		a.installQuestionWait(c)
		return
	}
	fact, msg, receipt, err := a.prepareQuestionTransaction(c)
	if err != nil {
		questionReply(c, QuestionReceipt{Error: err.Error()})
		return
	}
	if len(fact.Updates) == 0 && fact.Paused == nil && fact.ScopeID == "" && fact.ClosedScopeID == "" {
		questionReply(c, receipt)
		return
	}
	manager := a.recoveryManager()
	if manager == nil {
		questionReply(c, QuestionReceipt{Error: "question requires durable session storage"})
		return
	}
	commit := &questionCommit{command: c, fact: fact, msg: msg, receipt: receipt, manager: manager}
	r.active = commit
	if !a.tryEnqueueQuestionCommit(commit) {
		if c.operation.Operation != questionOpPause {
			r.active = nil
			questionReply(c, QuestionReceipt{Error: "question persistence queue is busy"})
			return
		}
		// A session switch deferred its questions behind this pause: failing it
		// would strand switching and reject every later question operation, so
		// keep the command active and retry once the pump drains.
		a.scheduleQuestionCommitRetry(commit)
	}
}

func (a *MainAgent) tryEnqueueQuestionCommit(commit *questionCommit) bool {
	return a.persist.tryEnqueue(persistEntry{agentID: identity.MainAgentID, msg: commit.msg, recovery: commit.manager, durable: true, after: func(err error) {
		completed := *commit
		completed.err = err
		a.sendEvent(Event{Type: EventQuestionCommitted, Payload: &completed})
	}})
}

// scheduleQuestionCommitRetry re-attempts a commit the persistence pump
// rejected because its queue was momentarily full. The timer keeps the event
// loop free; the retry event re-runs the admission attempt on the loop.
func (a *MainAgent) scheduleQuestionCommitRetry(commit *questionCommit) {
	r := &a.questions
	id := commit.fact.TransactionID
	if _, ok := r.retryTimers[id]; ok {
		return
	}
	r.retryTimers[id] = time.AfterFunc(questionCommitRetryDelay, func() {
		a.sendEvent(Event{Type: EventQuestionCommitRetry, Payload: commit})
	})
}

func (a *MainAgent) retryQuestionCommit(commit *questionCommit) {
	r := &a.questions
	if timer := r.retryTimers[commit.fact.TransactionID]; timer != nil {
		timer.Stop()
		delete(r.retryTimers, commit.fact.TransactionID)
	}
	if r.active != commit {
		return
	}
	if !a.tryEnqueueQuestionCommit(commit) {
		a.scheduleQuestionCommitRetry(commit)
	}
}

// projectQuestionSnapshot gives status, live updates and restored events the
// same latest revision without scanning or copying the entire transaction history.
// The index is rebuilt from durable facts; projected answer slices are caller-owned.
func projectQuestionSnapshot(q QuestionSnapshot, r *questionRuntime) QuestionSnapshot {
	q.Revision = nil
	if id := r.revisionTransactions[q.ID]; id != "" {
		if result := r.transactions[id].Result; result != nil {
			answer := *result
			answer.Selected = slices.Clone(answer.Selected)
			answer.SelectedIDs = slices.Clone(answer.SelectedIDs)
			q.Revision = &answer
		}
	}
	return q
}

func questionFingerprint(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (a *MainAgent) prepareQuestionTransaction(c *questionCommand) (QuestionFact, message.Message, QuestionReceipt, error) {
	r := &a.questions
	op := c.operation
	if op.Operation == questionOpPause || op.Operation == questionOpResume || op.Operation == QuestionOpNewTask || op.Operation == questionOpComplete || op.Operation == QuestionOpCancelTask {
		return a.prepareQuestionLifecycle(c)
	}
	txn := op.OperationID
	if op.Operation == questionOpCreate && c.call != "" {
		txn = "create:" + c.task + ":" + c.call
	}
	fingerprint := questionFingerprint(struct {
		Op    QuestionOperation
		Args  tools.QuestionArgs
		Task  string
		Scope string
	}{op, c.args, c.task, c.scope})
	if op.Operation == questionOpCreate {
		fingerprint = questionFingerprint(struct {
			Args  tools.QuestionArgs
			Task  string
			Scope string
		}{c.args, c.task, c.scope})
	}
	if old, ok := r.transactions[txn]; ok {
		if old.Fingerprint != fingerprint {
			return QuestionFact{}, message.Message{}, QuestionReceipt{}, fmt.Errorf("question operation ID reused with different arguments")
		}
		return QuestionFact{}, message.Message{}, r.receipts[txn], nil
	}
	fact := QuestionFact{TransactionID: txn, Fingerprint: fingerprint, Operation: op.Operation}
	receipt := QuestionReceipt{Accepted: true}
	msg := message.Message{Role: message.RoleSystem, Kind: message.KindQuestionState}
	if op.Operation == questionOpCreate {
		if err := tools.ValidateQuestionItems(c.args.Questions); err != nil {
			return fact, msg, receipt, err
		}
		pending := 0
		pendingBytes := 0
		for _, q := range r.records {
			if q.pending() {
				pending++
				data, _ := json.Marshal(q.Item)
				pendingBytes += len(data)
			}
		}
		if pending+len(c.args.Questions) > tools.MaxPendingQuestions {
			return fact, msg, receipt, fmt.Errorf("pending question limit reached")
		}
		for _, item := range c.args.Questions {
			if item.ResponsePolicy == "" {
				item.ResponsePolicy = tools.QuestionPolicyUserRequired
			}
			data, _ := json.Marshal(item)
			pendingBytes += len(data)
		}
		if pendingBytes > maxPendingQuestionBytes {
			return fact, msg, receipt, fmt.Errorf("pending question text budget reached; wait for existing questions before creating more")
		}
		fact.ScopeID = c.scope
		batch := makeRequestID()
		receipt.Result = tools.QuestionResult{Status: tools.QuestionStatusAccepted, BatchID: batch}
		for i, item := range c.args.Questions {
			if item.ResponsePolicy == "" {
				item.ResponsePolicy = tools.QuestionPolicyUserRequired
			}
			q := QuestionSnapshot{BindingID: r.binding, ID: makeRequestID(), BatchID: batch, AgentID: c.owner, TaskID: c.task, ScopeID: c.scope, CallID: c.call, TurnID: c.turn, Item: item, Index: i, Version: 1, Async: !c.args.Waits(), Visible: !c.args.Waits() || i == 0, Timer: QuestionTimerDisabled}
			if item.ResponsePolicy == tools.QuestionPolicyUserRequired {
				q.Dependency = QuestionDependencyPending
			}
			if cfg := a.globalConfig; cfg != nil {
				if item.ResponsePolicy == tools.QuestionPolicyDefaultAllowed {
					q.Duration = time.Duration(cfg.QuestionAutoSelectTimeout) * time.Second
				}
				if item.ResponsePolicy == tools.QuestionPolicyOptional {
					q.Duration = time.Duration(cfg.QuestionTimeout) * time.Second
				}
			}
			if q.Duration > 0 {
				q.Timer = QuestionTimerAwaiting
				q.Remaining = q.Duration
			}
			fact.Updates = append(fact.Updates, q)
			receipt.Result.QuestionIDs = append(receipt.Result.QuestionIDs, q.ID)
		}
	} else {
		q, ok := r.records[op.QuestionID]
		if !ok {
			return fact, msg, receipt, fmt.Errorf("unknown question ID")
		}
		if op.Operation == QuestionOpPresented && (op.Version != q.Version || op.BindingID != q.BindingID) {
			return fact, msg, receipt, fmt.Errorf("stale question presentation")
		}
		if op.Version > q.Version {
			return fact, msg, receipt, fmt.Errorf("question version is ahead of Core")
		}
		if op.Operation == questionOpExpire && op.Version != q.Version {
			return QuestionFact{}, msg, QuestionReceipt{Accepted: true}, nil
		}
		// The Core receipt time, including time queued behind a commit, orders actions.
		expired := q.pending() && q.Timer == QuestionTimerArmed && !c.received.Before(q.Deadline)
		if expired && op.Operation != QuestionOpWithdraw && op.Operation != QuestionOpReplace {
			a.defaultQuestion(&q)
		} else {
			switch op.Operation {
			case QuestionOpPresented:
				if !q.pending() || !q.Visible {
					return fact, msg, receipt, fmt.Errorf("question is not available")
				}
				receipt.Version = q.Version
				if !op.SupportsInteraction {
					if q.Timer == QuestionTimerCancelled || q.Timer == QuestionTimerDisabled {
						return fact, msg, receipt, nil
					}
					q.Timer = QuestionTimerCancelled
					q.Deadline = time.Time{}
				} else {
					if r.paused || (q.Timer != QuestionTimerAwaiting && q.Timer != QuestionTimerSuspended) {
						return fact, msg, receipt, nil
					}
					if q.Remaining == 0 {
						a.defaultQuestion(&q)
					} else {
						q.Timer = QuestionTimerArmed
						q.Deadline = c.received.Add(q.Remaining)
					}
				}
			case QuestionOpInteract:
				if !q.pending() {
					return fact, msg, receipt, fmt.Errorf("question is already %s", q.Outcome)
				}
				q.Timer = QuestionTimerCancelled
				q.Deadline = time.Time{}
			case QuestionOpAnswer, QuestionOpDecline:
				if !q.pending() {
					return fact, msg, receipt, fmt.Errorf("question is already %s", q.Outcome)
				}
				if op.Operation == QuestionOpDecline {
					if len(op.Answers) > 0 {
						return fact, msg, receipt, fmt.Errorf("decline cannot carry answers")
					}
					q.Outcome = tools.QuestionOutcomeDeclined
				} else {
					answer, ids, err := validateQuestionAnswer(q.Item, op.Answers, op.Custom)
					if err != nil {
						return fact, msg, receipt, err
					}
					q.Outcome = tools.QuestionOutcomeAnswered
					q.Selected = answer
					q.SelectedIDs = ids
					if q.Dependency == QuestionDependencyPending {
						q.Dependency = QuestionDependencyReceived
					}
				}
				q.ResultID = makeRequestID()
				q.Timer = QuestionTimerDisabled
				q.Deadline = time.Time{}
			case QuestionOpRevise:
				if q.pending() {
					return fact, msg, receipt, fmt.Errorf("use answer for a pending question")
				}
				answer, ids, err := validateQuestionAnswer(q.Item, op.Answers, op.Custom)
				if err != nil {
					return fact, msg, receipt, err
				}
				revised := tools.QuestionAnswer{QuestionID: q.ID, ResultID: makeRequestID(), Header: q.Item.Header, Outcome: tools.QuestionOutcomeAnswered, Selected: answer, SelectedIDs: ids}
				fact.Result = &revised
				if q.Dependency == QuestionDependencyPending {
					q.Dependency = QuestionDependencyReceived
				}
			case QuestionOpWithdraw, QuestionOpReplace:
				if strings.TrimSpace(op.UserText) == "" {
					return fact, msg, receipt, fmt.Errorf("explicit user withdrawal or replacement text is required")
				}
				if q.Dependency == QuestionDependencyWithdrawn || q.Dependency == QuestionDependencyReplaced {
					return fact, msg, receipt, fmt.Errorf("requirement is already disposed")
				}
				fact.SourceID = txn
				q.UserSourceID = txn
				if op.Operation == QuestionOpReplace {
					next, exists := r.records[op.ReplacementID]
					if !exists || next.ID == q.ID || next.TaskID != q.TaskID || next.ScopeID != q.ScopeID {
						return fact, msg, receipt, fmt.Errorf("replacement must reference another question in the same task")
					}
					q.Dependency = QuestionDependencyReplaced
					q.ReplacedBy = next.ID
					if q.pending() {
						q.Outcome = tools.QuestionOutcomeSuperseded
						q.ResultID = makeRequestID()
					}
				} else {
					q.Dependency = QuestionDependencyWithdrawn
					if q.pending() {
						q.Outcome = tools.QuestionOutcomeCancelled
						q.ResultID = makeRequestID()
					}
				}
				q.Timer = QuestionTimerDisabled
				q.Deadline = time.Time{}
			case questionOpExpire:
				if !expired {
					return QuestionFact{}, msg, QuestionReceipt{Accepted: true}, nil
				}
			default:
				return fact, msg, receipt, fmt.Errorf("invalid question operation")
			}
		}
		q.Version++
		fact.Updates = append(fact.Updates, q)
		if q.Outcome != "" && q.ResultID != "" && r.records[q.ID].pending() {
			answer := q.answer()
			fact.Result = &answer
		}
		if q.Outcome != "" && !q.Async {
			batch := a.questionBatch(q.BatchID)
			for _, next := range batch {
				if next.ID == q.ID || !next.pending() || next.Visible {
					continue
				}
				next.Version++
				if q.Outcome == tools.QuestionOutcomeAnswered || q.Outcome == tools.QuestionOutcomeDefaulted {
					next.Visible = true
					fact.Updates = append(fact.Updates, next)
					break
				}
				next.Outcome = tools.QuestionOutcomeCancelled
				next.Reason = "batch_stopped"
				next.ResultID = makeRequestID()
				next.Timer = QuestionTimerDisabled
				fact.Updates = append(fact.Updates, next)
			}
		}
		receipt.Status = q.Outcome
		receipt.Version = q.Version
		if op.Operation == QuestionOpAnswer || op.Operation == QuestionOpDecline || op.Operation == QuestionOpRevise || op.Operation == QuestionOpWithdraw || op.Operation == QuestionOpReplace {
			if expired {
				receipt.Accepted = false
				receipt.Error = "question deadline decided the outcome"
			}
			if !expired {
				msg.Role = message.RoleUser
				msg.Kind = message.KindQuestionState
			}
			if (q.Async || op.Operation == QuestionOpRevise) && !expired {
				msg.Kind = ""
			}
			if op.Operation == QuestionOpWithdraw || op.Operation == QuestionOpReplace {
				msg.Kind = ""
				msg.Content = op.UserText
			}
		}
		if fact.Result != nil && msg.Content == "" && q.Async {
			b, _ := json.Marshal(fact.Result)
			msg.Content = string(b)
			if msg.Role != message.RoleUser {
				msg.Role = message.RoleUser
				msg.Kind = message.KindQuestionResult
			}
		}
		if op.Operation == QuestionOpRevise && msg.Content == "" {
			b, _ := json.Marshal(fact.Result)
			msg.Content = string(b)
		}
	}
	fact.Receipt = receipt
	payload, err := json.Marshal(fact)
	if err != nil {
		return fact, msg, receipt, fmt.Errorf("encode question fact: %w", err)
	}
	msg.Question = payload
	return fact, msg, receipt, nil
}

func validateQuestionAnswer(item tools.QuestionItem, answers []string, custom bool) ([]string, []string, error) {
	if len(answers) == 0 || len(answers) > tools.MaxQuestionOptions {
		return nil, nil, fmt.Errorf("answer is required")
	}
	if custom {
		if len(answers) != 1 || strings.TrimSpace(answers[0]) == "" || len(answers[0]) > tools.MaxQuestionTextBytes {
			return nil, nil, fmt.Errorf("custom answer must be one bounded nonempty text")
		}
		return slices.Clone(answers), nil, nil
	}
	if !item.Multiple && len(answers) != 1 {
		return nil, nil, fmt.Errorf("single choice requires one option ID")
	}
	labels := make([]string, 0, len(answers))
	seen := map[string]bool{}
	for _, id := range answers {
		found := false
		for _, opt := range item.Options {
			if opt.ID == id && !seen[id] {
				labels = append(labels, opt.Label)
				seen[id] = true
				found = true
				break
			}
		}
		if !found {
			return nil, nil, fmt.Errorf("unknown or duplicate option ID")
		}
	}
	return labels, slices.Clone(answers), nil
}
func (a *MainAgent) defaultQuestion(q *QuestionSnapshot) {
	if q.Item.ResponsePolicy == tools.QuestionPolicyDefaultAllowed {
		q.Outcome = tools.QuestionOutcomeDefaulted
		q.SelectedIDs = []string{q.Item.DefaultOptionID}
		for _, opt := range q.Item.Options {
			if opt.ID == q.Item.DefaultOptionID {
				q.Selected = []string{opt.Label}
				break
			}
		}
	} else {
		q.Outcome = tools.QuestionOutcomeNoResponse
	}
	q.ResultID = makeRequestID()
	q.Timer = QuestionTimerDisabled
	q.Deadline = time.Time{}
}
func (a *MainAgent) questionBatch(id string) []QuestionSnapshot {
	var qs []QuestionSnapshot
	for _, q := range a.questions.records {
		if q.BatchID == id {
			qs = append(qs, q)
		}
	}
	slices.SortFunc(qs, func(x, y QuestionSnapshot) int { return x.Index - y.Index })
	return qs
}
func (a *MainAgent) handleQuestionCommitted(commit *questionCommit) {
	r := &a.questions
	if r.active == nil || r.active.fact.TransactionID != commit.fact.TransactionID || r.active.manager != commit.manager {
		return
	}
	r.active = nil
	if commit.err != nil {
		r.failed = true
		a.notePersistenceFailure(commit.err)
		for _, q := range commit.fact.Updates {
			if t := r.timers[q.ID]; t != nil {
				t.Stop()
				delete(r.timers, q.ID)
			}
		}
		questionReply(commit.command, QuestionReceipt{Error: "question commit failed; outcome requires recovery: " + commit.err.Error()})
	} else if a.recoveryManager() != commit.manager {
		questionReply(commit.command, QuestionReceipt{Error: "question committed to the previous session"})
	} else {
		msgIndex := a.ctxMgr.MessageCount()
		a.ctxMgr.Append(commit.msg)
		a.applyQuestionFact(commit.fact, commit.receipt)
		if commit.msg.Content != "" && commit.msg.Kind != message.KindQuestionState {
			a.emitToTUI(QuestionTranscriptEvent{Message: commit.msg, MessageIndex: msgIndex})
		}
		for _, q := range commit.fact.Updates {
			a.publishQuestion(q)
		}
		questionReply(commit.command, commit.receipt)
		if commit.fact.Operation == QuestionOpCancelTask {
			a.CancelCurrentTurn()
		}
		if commit.fact.Operation == QuestionOpNewTask && a.turn != nil {
			r.resumeNeeded = true
		}
		if commit.fact.Operation == QuestionOpNewTask || commit.fact.Operation == QuestionOpCancelTask {
			a.interruptQuestionWaits()
		}
		a.wakeQuestionWaits(commit.fact)
		if message.IsUserAuthored(commit.msg) && (len(commit.fact.Updates) == 0 || commit.fact.Updates[0].ScopeID == r.scope) {
			a.recordEvidenceFromMessage(commit.msg)
			a.explicitUserTurnCount.Add(1)
		}
		if commit.fact.Paused != nil && !*commit.fact.Paused {
			a.handleContinueFromContext()
		}
		if commit.fact.Operation == QuestionOpNewTask && a.turn == nil && !r.paused {
			a.handleContinueFromContext()
		}
		if (commit.fact.Result != nil || commit.fact.SourceID != "") && len(commit.fact.Updates) > 0 {
			if !r.completed[r.scope] && commit.command.owner == "" && commit.fact.Updates[0].ScopeID == r.scope && commit.fact.Updates[0].TaskID == identity.MainAgentID {
				if a.turn != nil {
					r.resumeNeeded = true
				} else if !r.paused {
					a.handleContinueFromContext()
				}
			}
		}
	}
	queued := r.queue
	r.queue = nil
	for _, c := range queued {
		a.handleQuestionCommand(c)
	}
	if r.active == nil && r.switchEvent != nil && (r.paused || r.failed) {
		evt := *r.switchEvent
		r.switchEvent = nil
		a.dispatch(evt)
	}
}

func (r *questionRuntime) recordQuestion(q QuestionSnapshot) {
	if _, exists := r.records[q.ID]; !exists {
		r.recordOrder = append(r.recordOrder, q.ID)
	}
	r.records[q.ID] = q
}

// recordTransaction stores a committed fact and keeps the durable commit
// order of transaction IDs. Request projection replays that order, so a
// client-supplied operation ID can never reorder decisions.
func (r *questionRuntime) recordTransaction(fact QuestionFact) {
	if _, ok := r.transactions[fact.TransactionID]; !ok {
		r.transactionOrder = append(r.transactionOrder, fact.TransactionID)
	}
	r.transactions[fact.TransactionID] = fact
	if fact.Operation == QuestionOpRevise && fact.Result != nil && fact.Result.QuestionID != "" {
		r.revisionTransactions[fact.Result.QuestionID] = fact.TransactionID
	}
}

func (a *MainAgent) applyQuestionFact(fact QuestionFact, receipt QuestionReceipt) {
	r := &a.questions
	if fact.ScopeID != "" {
		r.scope = fact.ScopeID
	}
	if fact.Paused != nil {
		r.paused = *fact.Paused
	}
	if fact.ClosedScopeID != "" {
		r.completed[fact.ClosedScopeID] = true
	}
	if fact.Operation == questionOpCreate && len(fact.Updates) > 0 && fact.Updates[0].TaskID == identity.MainAgentID {
		delete(r.completed, fact.ScopeID)
	}
	r.recordTransaction(fact)
	r.receipts[fact.TransactionID] = receipt
	for _, q := range fact.Updates {
		r.recordQuestion(q)
		if timer := r.timers[q.ID]; timer != nil {
			timer.Stop()
			delete(r.timers, q.ID)
		}
		if q.pending() && q.Timer == QuestionTimerArmed && !r.paused {
			id, version := q.ID, q.Version
			r.timers[id] = time.AfterFunc(max(time.Until(q.Deadline), 0), func() {
				a.sendEvent(Event{Type: EventQuestionCommand, Payload: &questionCommand{operation: QuestionOperation{Operation: questionOpExpire, OperationID: fmt.Sprintf("expire:%s:%d", id, version), QuestionID: id, Version: version}, ctx: a.parentCtx}})
			})
		}
	}
	if fact.Result != nil && fact.Result.ResultID != "" {
		r.unconsumed[fact.Result.ResultID] = true
	}
	for _, id := range fact.Consumed {
		delete(r.unconsumed, id)
	}
	a.updateQuestionCompletionGate()
}
func (a *MainAgent) publishQuestion(q QuestionSnapshot) {
	if !q.Visible && q.pending() {
		return
	}
	a.emitToTUI(QuestionStateEvent{Question: projectQuestionSnapshot(q, &a.questions)})
}
func (a *MainAgent) installQuestionWait(c *questionCommand) {
	if len(a.questions.waits) >= maxQuestionOperations {
		questionReply(c, QuestionReceipt{Error: "question wait limit reached"})
		return
	}
	wait := &questionWait{command: c, ids: slices.Clone(c.args.WaitFor)}
	for _, id := range wait.ids {
		q, ok := a.questions.records[id]
		if !ok || q.TaskID != c.task || q.ScopeID != c.scope {
			questionReply(c, QuestionReceipt{Error: "wait_for refers to an unknown question or another task"})
			return
		}
		if q.CallID == c.call && !q.Async {
			wait.original = true
		}
	}
	if result, ready := a.questionWaitResult(wait); ready {
		questionReply(c, QuestionReceipt{Accepted: true, Result: result})
		return
	}
	a.questions.waits[c.operation.OperationID] = wait
	a.questionWaiting.Store(true)
	a.publishQuestionWaits()
	a.emitActivity(c.owner, ActivityWaitingInput, "Waiting for an answer")
}
func (a *MainAgent) questionWaitResult(wait *questionWait) (tools.QuestionResult, bool) {
	result := tools.QuestionResult{Status: tools.QuestionStatusResolved}
	early := false
	for _, id := range wait.ids {
		q := a.questions.records[id]
		if q.pending() {
			result.PendingIDs = append(result.PendingIDs, id)
			continue
		}
		answer := q.answer()
		if !wait.original {
			answer.Selected = nil
			answer.SelectedIDs = nil
		}
		result.Answers = append(result.Answers, answer)
		if q.Outcome != tools.QuestionOutcomeAnswered && q.Outcome != tools.QuestionOutcomeDefaulted {
			early = true
		}
	}
	return result, len(result.PendingIDs) == 0 || early
}
func (a *MainAgent) wakeQuestionWaits(fact QuestionFact) {
	defer func() {
		a.questionWaiting.Store(len(a.questions.waits) > 0 || a.questions.waiting)
		a.publishQuestionWaits()
	}()
	for id, wait := range a.questions.waits {
		result, ready := a.questionWaitResult(wait)
		if !ready && fact.Result != nil {
			q := a.questions.records[fact.Result.QuestionID]
			if q.TaskID == wait.command.task && q.ScopeID == wait.command.scope && !slices.Contains(wait.ids, q.ID) {
				result.Status = tools.QuestionStatusWaitInterrupted
				ready = true
			}
		}
		if !ready && fact.SourceID != "" && len(fact.Updates) > 0 && fact.Updates[0].ScopeID == wait.command.scope {
			result.Status = tools.QuestionStatusWaitInterrupted
			ready = true
		}
		if ready {
			delete(a.questions.waits, id)
			questionReply(wait.command, QuestionReceipt{Accepted: true, Result: result})
		}
	}
}

// QuestionStateEvent is emitted only after the transcript fact was committed.
type QuestionStateEvent struct {
	Question QuestionSnapshot
	Restored bool
}

// QuestionTranscriptEvent projects a durable answer or explicit requirement
// change; clients must not create transcript cards from timer/state updates.
type QuestionTranscriptEvent struct {
	Message      message.Message
	MessageIndex int
}

func (QuestionTranscriptEvent) agentEvent() {}

func (QuestionStateEvent) agentEvent() {}

func (a *MainAgent) requiredQuestionIDs() []string {
	if a.questions.records == nil {
		return nil
	}
	var ids []string
	for _, q := range a.questions.records {
		if q.ScopeID == a.questions.scope && q.TaskID == identity.MainAgentID && q.Dependency == QuestionDependencyPending {
			ids = append(ids, q.ID)
		}
	}
	slices.Sort(ids)
	return ids
}
func (a *MainAgent) restoreQuestions(msgs []message.Message) {
	defer a.updateQuestionCompletionGate()
	if a.questions.active != nil {
		questionReply(a.questions.active.command, QuestionReceipt{Error: "session switch during question commit"})
	}
	for _, c := range a.questions.queue {
		questionReply(c, QuestionReceipt{Error: "session switch during question operation"})
	}
	for _, t := range a.questions.timers {
		t.Stop()
	}
	for _, timer := range a.questions.retryTimers {
		timer.Stop()
	}
	for _, w := range a.questions.waits {
		questionReply(w.command, QuestionReceipt{Error: "question wait stopped by session switch"})
	}
	a.questionWaiting.Store(false)
	a.questions = questionRuntime{}
	a.initQuestions()
	for _, msg := range msgs {
		if len(msg.Question) == 0 {
			continue
		}
		var fact QuestionFact
		if json.Unmarshal(msg.Question, &fact) != nil {
			continue
		}
		receipt := fact.Receipt
		if fact.ScopeID != "" {
			a.questions.scope = fact.ScopeID
		}
		if fact.Paused != nil {
			a.questions.paused = *fact.Paused
		}
		if fact.ClosedScopeID != "" {
			a.questions.completed[fact.ClosedScopeID] = true
		}
		if fact.Operation == questionOpCreate && len(fact.Updates) > 0 && fact.Updates[0].TaskID == identity.MainAgentID {
			delete(a.questions.completed, fact.ScopeID)
		}
		// Rebuild without arming any pre-crash deadline.
		a.questions.recordTransaction(fact)
		a.questions.receipts[fact.TransactionID] = receipt
		for _, q := range fact.Updates {
			a.questions.recordQuestion(q)
		}
		if fact.Result != nil {
			a.questions.unconsumed[fact.Result.ResultID] = true
		}
		for _, id := range fact.Consumed {
			delete(a.questions.unconsumed, id)
		}
	}
	for id, q := range a.questions.records {
		q.BindingID = a.questions.binding
		if q.pending() {
			q.Version++
		}
		if q.pending() && q.Timer == QuestionTimerArmed {
			q.Timer = QuestionTimerSuspended
			q.Deadline = time.Time{}
			q.Remaining = q.Duration
		}
		a.questions.records[id] = q
	}
}

func filterQuestionStateMessages(messages []message.Message) []message.Message {
	visible := make([]message.Message, 0, len(messages))
	for _, msg := range messages {
		if msg.Kind != message.KindQuestionState {
			visible = append(visible, msg)
		}
	}
	return visible
}
