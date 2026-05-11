package agent

import (
	"encoding/json"
	"strings"
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
	emit                         func(AgentEvent)
	flushBeforeTool              func()
	promoteStreamingActivity     func(source string)
	recordToolUseEnd             func(callID, callName, agentID string, at time.Time)
	discardSpeculativeOnRollback func(turn *Turn, reason string)
	drainPartialTextOnRollback   bool
}

func (r streamToolDeltaReducer) Handle(delta message.StreamDelta) bool {
	switch delta.Type {
	case "tool_use_start":
		r.handleToolUseStart(delta)
		return true
	case "tool_use_delta":
		r.handleToolUseDelta(delta)
		return true
	case "tool_use_end":
		r.handleToolUseEnd(delta)
		return true
	case "rollback":
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
	if delta.ToolCall == nil || r.emit == nil {
		return
	}
	if r.turn != nil {
		r.turn.recordStreamingToolCall(PendingToolCall{
			CallID:   delta.ToolCall.ID,
			Name:     delta.ToolCall.Name,
			ArgsJSON: delta.ToolCall.Input,
			AgentID:  r.agentID,
		})
	}
	r.emit(ToolCallStartEvent{
		ID:       delta.ToolCall.ID,
		Name:     delta.ToolCall.Name,
		ArgsJSON: delta.ToolCall.Input,
		AgentID:  r.agentID,
	})
}

func (r streamToolDeltaReducer) handleToolUseDelta(delta message.StreamDelta) {
	if delta.ToolCall == nil || r.turn == nil || delta.ToolCall.ID == "" || delta.ToolCall.Input == "" {
		return
	}
	if r.promoteStreamingActivity != nil {
		r.promoteStreamingActivity("tool_use_delta")
	}
	accumulated := r.turn.appendStreamingToolCallInput(delta.ToolCall.ID, delta.ToolCall.Name, delta.ToolCall.Input, r.agentID)
	if accumulated == "" || r.emit == nil {
		return
	}
	r.emit(ToolCallUpdateEvent{
		ID:       delta.ToolCall.ID,
		Name:     delta.ToolCall.Name,
		ArgsJSON: accumulated,
		AgentID:  r.agentID,
	})
}

func (r streamToolDeltaReducer) handleToolUseEnd(delta message.StreamDelta) {
	if delta.ToolCall == nil || r.turn == nil || delta.ToolCall.ID == "" {
		return
	}
	callID := delta.ToolCall.ID
	callName := strings.TrimSpace(delta.ToolCall.Name)
	argsJSON := ""
	if call, ok := r.turn.getStreamingToolCall(callID); ok {
		if callName == "" {
			callName = call.Name
		}
		argsJSON = call.ArgsJSON
	}
	ruleset := permission.Ruleset(nil)
	if r.ruleset != nil {
		ruleset = r.ruleset()
	}
	decision := evaluateSpeculativeExecutionPolicy(r.registry, ruleset, callName, json.RawMessage(argsJSON))
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
