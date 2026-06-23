package agent

import (
	"encoding/json"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

type streamToolDeltaReducer struct {
	agentID                      string
	turn                         *Turn
	registry                     *tools.Registry
	ruleset                      func() permission.Ruleset
	visibleToolNames             func() map[string]struct{}
	emit                         func(AgentEvent)
	flushBeforeTool              func()
	promoteStreamingActivity     func(source string)
	recordToolUseEnd             func(callID, callName, agentID string, at time.Time)
	discardSpeculativeOnRollback func(turn *Turn, reason string)
	drainPartialTextOnRollback   bool
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
	if delta.ToolCall == nil || r.turn == nil || delta.ToolCall.ID == "" || delta.ToolCall.Input == "" {
		return
	}
	if r.promoteStreamingActivity != nil {
		r.promoteStreamingActivity("tool_use_delta")
	}
	name := tools.NormalizeName(delta.ToolCall.Name)
	accumulated := r.turn.appendStreamingToolCallInput(delta.ToolCall.ID, name, delta.ToolCall.Input, r.agentID)
	if accumulated == "" {
		return
	}
	if r.emit != nil {
		r.emit(ToolCallUpdateEvent{
			ID:       delta.ToolCall.ID,
			Name:     name,
			ArgsJSON: accumulated,
			AgentID:  r.agentID,
		})
	}
	r.maybeStartEarlySpeculativeTool(delta.ToolCall.ID)
}

func (r streamToolDeltaReducer) maybeStartEarlySpeculativeTool(callID string) {
	if r.turn == nil || r.turn.streamingToolExec == nil || callID == "" {
		return
	}
	call, ok := r.turn.getStreamingToolCall(callID)
	if !ok {
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
	if err := tools.ValidateToolArgs(tool, json.RawMessage(call.ArgsJSON)); err != nil {
		return
	}
	ruleset := permission.Ruleset(nil)
	if r.ruleset != nil {
		ruleset = r.ruleset()
	}
	decision := evaluateSpeculativeExecutionPolicyWithPrefix(r.registry, ruleset, callName, json.RawMessage(call.ArgsJSON), r.turn.streamingToolCallsBefore(callID))
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
	decision := evaluateSpeculativeExecutionPolicyWithPrefix(r.registry, ruleset, callName, json.RawMessage(argsJSON), r.turn.streamingToolCallsBefore(callID))
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
		if r.drainPartialTextOnRollback {
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
