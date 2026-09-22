package agent

import (
	"encoding/json"
	"time"

	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

type streamToolDeltaReducer struct {
	agentID                      string
	turn                         *Turn
	registry                     *tools.Registry
	ruleset                      func() permission.Ruleset
	pathScope                    permission.PathScope
	visibleToolNames             func() map[string]struct{}
	emit                         func(AgentEvent)
	flushBeforeTool              func()
	promoteStreamingActivity     func(source string)
	recordToolUseEnd             func(callID, callName, agentID string, at time.Time)
	discardSpeculativeOnRollback func(turn *Turn, reason string)
	// syncHookGate reports whether sync tool hooks are configured; when they
	// are, speculative execution stands down so every call dispatches through
	// the pipeline whose sync hooks run off the event loop.
	syncHookGate func() bool
	// drainPartialOnRollback clears the turn's partial-text accumulator when a
	// streamed attempt is rolled back to retry, so the abandoned attempt's text
	// does not concatenate with the replacement. Sub-agents leave this false:
	// their contract is that produced text is never discarded, and partial text
	// from an abandoned attempt still reads as prose. Reasoning is not covered
	// by this switch — see handleRollback.
	drainPartialOnRollback bool
}

func (r streamToolDeltaReducer) Handle(delta message.StreamDelta) bool {
	switch delta.Type {
	case message.StreamDeltaToolUseStart:
		r.handleToolUseStart(delta)
		return true
	case message.StreamDeltaToolUseDelta:
		r.handleToolUseDelta(delta)
		return true
	case message.StreamDeltaToolUseEnd:
		r.handleToolUseEnd(delta)
		return true
	case message.StreamDeltaRollback:
		r.handleRollback(delta)
		return true
	default:
		return false
	}
}

func (r streamToolDeltaReducer) handleToolUseStart(delta message.StreamDelta) {
	if r.flushBeforeTool != nil {
		r.flushBeforeTool()
	}
	if r.promoteStreamingActivity != nil {
		r.promoteStreamingActivity("tool_use_start")
	}
	if delta.ToolCall == nil {
		return
	}
	name := tools.NormalizeName(delta.ToolCall.Name)
	if r.turn != nil {
		r.turn.recordStreamingToolCall(PendingToolCall{
			CallID:   delta.ToolCall.ID,
			Name:     name,
			ArgsJSON: delta.ToolCall.Input,
			AgentID:  r.agentID,
		})
		// A start can carry the first argument bytes; they seed the fragment
		// accumulator so later fragments concatenate onto it. The seed is
		// sanitized to match recordStreamingToolCall's hygiene for the
		// recorded present.
		if seed := delta.ToolCall.Input; seed != "" {
			r.turn.appendStreamingToolCallInput(delta.ToolCall.ID, name, tools.StripZeroWidthFormat(seed), r.agentID)
		}
	}
	if r.emit != nil {
		r.emit(ToolCallStartEvent{
			ID:       delta.ToolCall.ID,
			Name:     name,
			ArgsJSON: delta.ToolCall.Input,
			AgentID:  r.agentID,
		})
	}
	r.maybeStartEarlySpeculativeTool(delta.ToolCall.ID)
}

func (r streamToolDeltaReducer) handleToolUseDelta(delta message.StreamDelta) {
	if delta.ToolCall == nil || r.turn == nil || delta.ToolCall.ID == "" {
		return
	}
	if r.promoteStreamingActivity != nil {
		r.promoteStreamingActivity("tool_use_delta")
	}
	name := tools.NormalizeName(delta.ToolCall.Name)
	if delta.ToolCall.InputText != "" {
		// Freeform input (Responses custom apply_patch deltas): accumulate the
		// raw text and hand it to the TUI as the growing patch preview,
		// coalesced to the args flush cadence — each update carries the whole
		// accumulated text, so flushing per fragment is quadratic in the patch
		// size, far faster than the UI can re-render it (the JSON fragment path
		// below makes the same tradeoff). The canonical {patch} args object is
		// deliberately not carried here — it is a full copy of the patch, so
		// emitting it per fragment is quadratic too — and nothing reads it before
		// the arguments complete: the TUI renders InputText, and speculative
		// validation and finalize both run off the stored call at args-end,
		// where the envelope is materialized.
		r.turn.appendStreamingToolCallInputText(delta.ToolCall.ID, name, delta.ToolCall.InputText, r.agentID)
		if !r.turn.streamingArgsFlushDue(delta.ToolCall.ID, time.Now()) {
			return
		}
		accumulated, ok := r.turn.streamingToolCallInputText(delta.ToolCall.ID)
		if !ok || accumulated == "" {
			return
		}
		if r.emit != nil {
			r.emit(ToolCallUpdateEvent{
				ID:        delta.ToolCall.ID,
				Name:      name,
				InputText: accumulated,
				AgentID:   r.agentID,
			})
		}
		return
	}
	if delta.ToolCall.Input == "" {
		return
	}
	// Fragments accumulate in the turn's per-call builder; both the TUI update
	// and the speculative-start attempt run at the args flush cadence. Per-
	// fragment updates each carried the accumulated args — quadratic in the
	// args' size — far faster than the UI can re-render them (see the freeform
	// path above for the same tradeoff on the text side).
	r.turn.appendStreamingToolCallInput(delta.ToolCall.ID, name, delta.ToolCall.Input, r.agentID)
	if !r.turn.streamingArgsFlushDue(delta.ToolCall.ID, time.Now()) {
		return
	}
	call, ok := r.turn.getStreamingToolCall(delta.ToolCall.ID)
	if !ok {
		return
	}
	if r.emit != nil {
		r.emit(ToolCallUpdateEvent{
			ID:       delta.ToolCall.ID,
			Name:     name,
			ArgsJSON: call.ArgsJSON,
			AgentID:  r.agentID,
		})
	}
	r.maybeStartEarlySpeculativeToolCall(delta.ToolCall.ID, call)
}

func (r streamToolDeltaReducer) maybeStartEarlySpeculativeTool(callID string) {
	if r.turn == nil || callID == "" {
		return
	}
	call, ok := r.turn.getStreamingToolCall(callID)
	if !ok {
		return
	}
	r.maybeStartEarlySpeculativeToolCall(callID, call)
}

// maybeStartEarlySpeculativeToolCall runs the early speculative-start attempt
// with an already-materialized call, so callers that just materialized (the
// throttled delta path) do not pay a second args clone.
func (r streamToolDeltaReducer) maybeStartEarlySpeculativeToolCall(callID string, call PendingToolCall) {
	if r.turn == nil || r.turn.streamingToolExec == nil || callID == "" {
		return
	}
	callName := tools.NormalizeName(call.Name)
	if callName == "" || call.ArgsJSON == "" {
		return
	}
	if r.registry == nil {
		return
	}
	tool, ok := r.registry.Get(callName)
	if !ok {
		return
	}
	early, ok := tool.(tools.EarlyRenderableReadOnlyTool)
	if !ok || !early.CanRenderBeforeToolUseEnd(json.RawMessage(call.ArgsJSON)) {
		return
	}
	// Unknown fields follow the execution-time contract: the pipeline strips
	// them before running the tool, so they must not disqualify speculative
	// execution either.
	if err := tools.ValidateToolArgs(tool, llm.UnwrapToolArgs(json.RawMessage(call.ArgsJSON))); err != nil {
		return
	}
	ruleset := permission.Ruleset(nil)
	if r.ruleset != nil {
		ruleset = r.ruleset()
	}
	decision := rejectSpeculativeExecution("sync_hooks_configured")
	if r.syncHookGate == nil || !r.syncHookGate() {
		decision = evaluateSpeculativeExecutionPolicyWithPrefix(r.registry, ruleset, callName, json.RawMessage(call.ArgsJSON), r.turn.streamingToolCallsBefore(callID), r.pathScope)
	}
	if decision.Allowed {
		decision = r.checkVisibleSpeculativeTool(callName)
	}
	logSpeculativeExecutionDecision(callID, callName, decision)
	if !decision.Allowed {
		return
	}
	r.turn.streamingToolExec.Start(message.ToolCall{ID: callID, Name: callName, Args: json.RawMessage(call.ArgsJSON)})
}

func (r streamToolDeltaReducer) handleToolUseEnd(delta message.StreamDelta) {
	if delta.ToolCall == nil || r.turn == nil || delta.ToolCall.ID == "" {
		return
	}
	callID := delta.ToolCall.ID
	callName := tools.NormalizeName(delta.ToolCall.Name)
	argsJSON := ""
	if call, ok := r.turn.getStreamingToolCall(callID); ok {
		if callName == "" {
			callName = tools.NormalizeName(call.Name)
		}
		argsJSON = call.ArgsJSON
	}
	ruleset := permission.Ruleset(nil)
	if r.ruleset != nil {
		ruleset = r.ruleset()
	}
	decision := rejectSpeculativeExecution("sync_hooks_configured")
	if r.syncHookGate == nil || !r.syncHookGate() {
		decision = evaluateSpeculativeExecutionPolicyWithPrefix(r.registry, ruleset, callName, json.RawMessage(argsJSON), r.turn.streamingToolCallsBefore(callID), r.pathScope)
	}
	if decision.Allowed && r.registry != nil {
		if tool, ok := r.registry.Get(callName); ok {
			if err := tools.ValidateToolArgs(tool, llm.UnwrapToolArgs(json.RawMessage(argsJSON))); err != nil {
				decision = rejectSpeculativeExecution("invalid_args")
			}
		}
	}
	if decision.Allowed {
		decision = r.checkVisibleSpeculativeTool(callName)
	}
	logSpeculativeExecutionDecision(callID, callName, decision)
	if decision.Allowed && r.turn.streamingToolExec != nil {
		r.turn.streamingToolExec.Start(message.ToolCall{ID: callID, Name: callName, Args: json.RawMessage(argsJSON)})
	}
	if r.recordToolUseEnd != nil {
		r.recordToolUseEnd(callID, callName, r.agentID, time.Now())
	}
	if r.emit != nil {
		r.emit(ToolCallUpdateEvent{
			ID:                callID,
			Name:              callName,
			ArgsJSON:          argsJSON,
			ArgsStreamingDone: true,
			AgentID:           r.agentID,
		})
	}
}

func (r streamToolDeltaReducer) checkVisibleSpeculativeTool(name string) speculativeExecutionDecision {
	if r.visibleToolNames == nil {
		return speculativeExecutionDecision{Allowed: true}
	}
	err := (toolExecutionPipeline{visibleToolNames: r.visibleToolNames}).checkVisible(name)
	if err == nil {
		return speculativeExecutionDecision{Allowed: true}
	}
	return rejectSpeculativeExecution("hidden_tool:" + tools.NormalizeName(name))
}

func (r streamToolDeltaReducer) handleRollback(delta message.StreamDelta) {
	if r.turn != nil {
		// Reasoning is always dropped, whatever the text policy is. A finalized
		// reasoning item is bound to the attempt that produced it, so keeping
		// it would pair the replacement message with items from a response the
		// backend abandoned — a rejected replay, not merely stale prose.
		r.turn.drainPartialResponsesOutput()
		if r.drainPartialOnRollback {
			r.turn.drainPartialText()
		}
		if r.discardSpeculativeOnRollback != nil {
			r.discardSpeculativeOnRollback(r.turn, "rollback")
		}
	}
	reason := ""
	if delta.Rollback != nil {
		reason = delta.Rollback.Reason
	}
	if r.emit != nil {
		r.emit(StreamRollbackEvent{Reason: reason, AgentID: r.agentID})
	}
}
