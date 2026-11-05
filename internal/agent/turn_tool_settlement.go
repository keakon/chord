package agent

import (
	"context"
	"sort"

	"github.com/keakon/chord/internal/message"
)

// turnContextKey binds a Turn to the context every tool dispatch derives from
// it, so an executing tool can resolve the turn it belongs to even after the
// agent moved on to a newer turn.
type turnContextKey struct{}

func contextWithTurn(ctx context.Context, turn *Turn) context.Context {
	if ctx == nil || turn == nil {
		return ctx
	}
	return context.WithValue(ctx, turnContextKey{}, turn)
}

func turnFromContext(ctx context.Context) *Turn {
	if ctx == nil {
		return nil
	}
	turn, _ := ctx.Value(turnContextKey{}).(*Turn)
	return turn
}

// interruptedToolOutcomeUnknownText is the synthetic terminal text for a call
// that was admitted to execute but whose result never reached the turn. It must
// not claim the call never ran: the external side effect may already exist.
const interruptedToolOutcomeUnknownText = "Tool call was interrupted after it started; its result is unknown. Side effects may be partially or fully applied — verify the current state before retrying."

// toolSettlementEvidence is the per-turn snapshot a cancellation closeout uses
// to prove what each pending call is allowed to report:
//   - admitted calls may have produced external side effects, so without a
//     trusted terminal result they settle as outcome_unknown;
//   - calls that never passed admission are known not to have executed and
//     settle as ordinary cancellations;
//   - ready results were produced by an execution goroutine but not yet
//     consumed by the event loop, and outrank the cancellation.
type toolSettlementEvidence struct {
	admitted map[string]struct{}
	ready    map[string]any
}

func (e *toolSettlementEvidence) addReadyResult(callID string, result any) {
	if callID == "" || result == nil {
		return
	}
	if e.ready == nil {
		e.ready = make(map[string]any)
	}
	if _, exists := e.ready[callID]; !exists {
		e.ready[callID] = result
	}
}

func (e toolSettlementEvidence) wasAdmitted(callID string) bool {
	_, ok := e.admitted[callID]
	return ok
}

func (e toolSettlementEvidence) readyResult(callID string) (any, bool) {
	result, ok := e.ready[callID]
	return result, ok && result != nil
}

// admitToolExecution reserves execution for callID. A turn whose admission is
// already closed (cancelled, failed, replaced) refuses, which lets the
// cancellation closeout prove the call never started.
func (t *Turn) admitToolExecution(callID string) bool {
	if t == nil || callID == "" {
		return true
	}
	t.pendingToolMu.Lock()
	defer t.pendingToolMu.Unlock()
	if t.toolAdmissionsClosed {
		return false
	}
	if _, abandoned := t.abandonedToolCalls[callID]; abandoned {
		return false
	}
	if _, settled := t.settledToolCalls[callID]; settled {
		return false
	}
	if t.toolAdmissions == nil {
		t.toolAdmissions = make(map[string]struct{})
	}
	t.toolAdmissions[callID] = struct{}{}
	return true
}

// abandonToolCall refuses any later admission for callID and reports whether
// the call had already passed admission. The intent barrier uses it per call so
// a speculative execution that has not started yet cannot run after the
// barrier decision, while the turn stays open for later LLM rounds.
func (t *Turn) abandonToolCall(callID string) bool {
	if t == nil || callID == "" {
		return false
	}
	t.pendingToolMu.Lock()
	defer t.pendingToolMu.Unlock()
	if t.abandonedToolCalls == nil {
		t.abandonedToolCalls = make(map[string]struct{})
	}
	t.abandonedToolCalls[callID] = struct{}{}
	_, admitted := t.toolAdmissions[callID]
	return admitted
}

// closeToolAdmissions stops new executions. It runs at the moment the turn is
// cancelled or failed, before the closeout that settles pending calls, so a tool
// that has not passed admission can never start afterwards.
func (t *Turn) closeToolAdmissions() {
	if t == nil {
		return
	}
	t.pendingToolMu.Lock()
	t.toolAdmissionsClosed = true
	t.pendingToolMu.Unlock()
}

// takeToolSettlement snapshots the turn's execution evidence and hands ready
// results to the closeout. Admissions stay closed and the ready entries move to
// the returned evidence, so a result produced after this point can only be
// dropped: its call is settled by the same closeout.
func (t *Turn) takeToolSettlement() toolSettlementEvidence {
	if t == nil {
		return toolSettlementEvidence{}
	}
	t.pendingToolMu.Lock()
	defer t.pendingToolMu.Unlock()
	t.toolAdmissionsClosed = true
	evidence := toolSettlementEvidence{admitted: t.toolAdmissions, ready: t.readyToolResults}
	t.toolAdmissions = nil
	t.readyToolResults = nil
	if evidence.ready == nil {
		evidence.ready = make(map[string]any)
	}
	return evidence
}

// registerReadyToolResult publishes a terminal result before its event is
// queued or sent. It fails once the call was settled, so a late result can never
// reopen a call that a closeout already closed.
func (t *Turn) registerReadyToolResult(callID string, result any) bool {
	if t == nil || callID == "" || result == nil {
		return true
	}
	t.pendingToolMu.Lock()
	defer t.pendingToolMu.Unlock()
	if _, settled := t.settledToolCalls[callID]; settled {
		return false
	}
	if t.readyToolResults == nil {
		t.readyToolResults = make(map[string]any)
	}
	t.readyToolResults[callID] = result
	return true
}

func (t *Turn) toolCallSettled(callID string) bool {
	if t == nil || callID == "" {
		return false
	}
	t.pendingToolMu.Lock()
	defer t.pendingToolMu.Unlock()
	_, settled := t.settledToolCalls[callID]
	return settled
}

// settleToolCallTerminal marks callID as having reached a terminal settlement
// exactly once. It returns false when the call was already settled, so a
// duplicate closeout cannot append a second terminal result.
func (t *Turn) settleToolCallTerminal(callID string) bool {
	if t == nil || callID == "" {
		return false
	}
	t.pendingToolMu.Lock()
	defer t.pendingToolMu.Unlock()
	return t.settleToolCallTerminalLocked(callID)
}

func (t *Turn) settleToolCallTerminalLocked(callID string) bool {
	if _, settled := t.settledToolCalls[callID]; settled {
		return false
	}
	if t.settledToolCalls == nil {
		t.settledToolCalls = make(map[string]struct{})
	}
	t.settledToolCalls[callID] = struct{}{}
	delete(t.readyToolResults, callID)
	delete(t.toolAdmissions, callID)
	return true
}

// closeInterruptedToolCalls settles every call exactly once. A call with a
// ready result is handed to preserve; an admitted call without a result is
// reported as outcome_unknown instead of an ordinary cancellation; the rest are
// returned for the caller's synthetic terminal status. preserve returns false
// for a result whose call is absent from the assistant history, which the
// caller's normal discard path already covers.
func closeInterruptedToolCalls(
	turn *Turn,
	evidence toolSettlementEvidence,
	calls []PendingToolCall,
	preserve func(PendingToolCall, any) bool,
) (synthetic []PendingToolCall, preserved int) {
	calls = appendReadyOnlyToolCalls(turn, evidence, calls)
	for _, call := range calls {
		if call.CallID == "" || !turn.settleToolCallTerminal(call.CallID) {
			continue
		}
		if result, ok := evidence.readyResult(call.CallID); ok {
			if preserve != nil && preserve(call, result) {
				preserved++
			}
			continue
		}
		if evidence.wasAdmitted(call.CallID) {
			call.TerminalText = interruptedToolOutcomeUnknownText
			call.TerminalRecoveryState = message.ToolRecoveryStateOutcomeUnknown
		}
		synthetic = append(synthetic, call)
	}
	return synthetic, preserved
}

// appendReadyOnlyToolCalls adds calls that have a ready result but no pending
// metadata (barrier-failure synthetics are published this way). Their terminal
// must be settled by the same closeout, or a late result could land in a turn
// that already ended.
func appendReadyOnlyToolCalls(turn *Turn, evidence toolSettlementEvidence, calls []PendingToolCall) []PendingToolCall {
	if len(evidence.ready) == 0 {
		return calls
	}
	known := make(map[string]struct{}, len(calls))
	for _, call := range calls {
		known[call.CallID] = struct{}{}
	}
	extra := make([]PendingToolCall, 0, len(evidence.ready))
	for callID, ready := range evidence.ready {
		if callID == "" {
			continue
		}
		if _, ok := known[callID]; ok {
			continue
		}
		extra = append(extra, pendingCallFromReadyResult(callID, ready))
	}
	if len(extra) == 0 {
		return calls
	}
	sort.Slice(extra, func(i, j int) bool { return extra[i].CallID < extra[j].CallID })
	extra = turn.filterCompletedToolCalls(extra)
	return append(calls, extra...)
}

func pendingCallFromReadyResult(callID string, ready any) PendingToolCall {
	switch result := ready.(type) {
	case *ToolResultPayload:
		return PendingToolCall{CallID: callID, Name: result.Name, ArgsJSON: result.ArgsJSON}
	case *toolResult:
		return PendingToolCall{CallID: callID, Name: result.Name, ArgsJSON: result.ArgsJSON}
	default:
		return PendingToolCall{CallID: callID}
	}
}
