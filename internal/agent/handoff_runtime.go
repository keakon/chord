package agent

import (
	"fmt"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// handoffResolvePayload carries the user's decision for a pending handoff.
type handoffResolvePayload struct {
	RequestID  string
	Action     string // "approve" | "deny" | "cancel"
	AgentName  string // selected target role for approve (defaults to "builder")
	DenyReason string // rejection reason for deny
}

// handoffResolveActions are the accepted values for handoffResolvePayload.Action.
const (
	handoffResolveApprove = "approve"
	handoffResolveDeny    = "deny"
	handoffResolveCancel  = "cancel"
)

// ResolveHandoff delivers the user's plan-execution decision back to the pending
// handoff flow. action is one of handoffResolveApprove / handoffResolveDeny /
// handoffResolveCancel. It is safe to call from any goroutine (posts to the
// event loop).
func (a *MainAgent) ResolveHandoff(requestID, action, agentName, denyReason string) {
	a.sendEvent(Event{Type: EventHandoffResolve, Payload: &handoffResolvePayload{
		RequestID:  requestID,
		Action:     action,
		AgentName:  agentName,
		DenyReason: denyReason,
	}})
}

// handleHandoffResolveEvent settles the pending handoff with the user's
// decision: the deferred tool result is emitted and persisted, the user wait
// is closed, and the flow continues (plan execution, context continuation, or
// idle for a cancel).
func (a *MainAgent) handleHandoffResolveEvent(evt Event) {
	p, ok := evt.Payload.(*handoffResolvePayload)
	if !ok {
		log.Errorf("handleHandoffResolveEvent: invalid payload type payload_type=%v", fmt.Sprintf("%T", evt.Payload))
		return
	}
	if a.pendingHandoff == nil || a.pendingHandoff.RequestID != p.RequestID {
		log.Warnf("handleHandoffResolveEvent: no matching pending handoff request_id=%v action=%v", p.RequestID, p.Action)
		return
	}
	pc := a.pendingHandoff
	a.pendingHandoff = nil
	a.interaction.settleHandoff(pc.RequestID)

	switch p.Action {
	case handoffResolveApprove:
		target := strings.TrimSpace(p.AgentName)
		if target == "" {
			target = "builder"
		}
		// Only an eligible, non-current main-agent role may receive the plan.
		// An invalid target (unknown role, SubAgent, or the active role) is a
		// user decision that cannot be executed: record it as an error on the
		// handoff tool card instead of pretending the handoff succeeded.
		if !a.validHandoffTarget(target) {
			msg := fmt.Sprintf("handoff target %q is not available", target)
			a.emitToTUI(ErrorEvent{Err: fmt.Errorf("%s", msg)})
			a.emitDeferredHandoffToolResult(pc, msg, ToolResultStatusError)
			// The user's decision was consumed but cannot be executed, exactly
			// like the preparation failures below: return the agent to idle so
			// queued input drains instead of leaving the turn hanging.
			a.setIdleAndDrainPending()
			return
		}
		staging, err := a.beginPlanExecution(pc.PlanPath, target)
		if err != nil {
			a.emitToTUI(ErrorEvent{Err: err})
			a.emitDeferredHandoffToolResult(pc, err.Error(), ToolResultStatusError)
			a.setIdleAndDrainPending()
			return
		}
		// The user's decision is final and preparation succeeded: settle the
		// deferred call now, while the session holding the paired tool call is
		// still active. Crossing the session boundary first would strand the
		// result in the fresh execution session without its call (strict
		// providers require tool_use/tool_result pairing).
		a.emitDeferredHandoffToolResult(pc, pc.Result, ToolResultStatusSuccess)
		if err := a.commitPlanExecution(staging); err != nil {
			a.emitToTUI(ErrorEvent{Err: err})
			a.setIdleAndDrainPending()
			return
		}
	case handoffResolveDeny:
		reason := strings.TrimSpace(p.DenyReason)
		if reason == "" {
			reason = "User rejected the plan."
		}
		result := "Handoff rejected: " + reason
		a.emitDeferredHandoffToolResult(pc, result, ToolResultStatusSuccess)
		// Keep the rejection visible to the model as a user message anchored to
		// the plan, matching the previous TUI-side rejection flow.
		msg := message.Message{
			Role:    message.RoleUser,
			Content: fmt.Sprintf("Handoff rejected: %s\n\nPlan path: %s", reason, pc.PlanPath),
		}
		a.ctxMgr.Append(msg)
		a.recordEvidenceFromMessage(msg)
		if a.recovery != nil {
			a.persistAsync(identity.MainAgentID, msg)
		}
		a.handleContinueFromContext(Event{Type: EventContinue})
	case handoffResolveCancel:
		a.emitDeferredHandoffToolResult(pc, "Cancelled", ToolResultStatusCancelled)
	default:
		log.Warnf("handleHandoffResolveEvent: unsupported handoff action=%v request_id=%v", p.Action, p.RequestID)
	}
}

// abandonPendingHandoff drops a pending handoff without a user decision,
// settling its open user-wait and emitting a cancelled terminal result so the
// transcript never keeps an unresolved handoff tool call (strict providers
// require tool_use/tool_result pairing). Used when a new turn or session
// switch invalidates the pending handoff before the user resolves it.
func (a *MainAgent) abandonPendingHandoff() {
	if a == nil || a.pendingHandoff == nil {
		return
	}
	pc := a.pendingHandoff
	a.pendingHandoff = nil
	if reqID := pc.RequestID; reqID != "" {
		a.interaction.settleHandoff(reqID)
	}
	a.emitDeferredHandoffToolResult(pc, "Cancelled", ToolResultStatusCancelled)
}

// settlePendingHandoffAtShutdown resolves a handoff the user never decided
// before process exit: it emits and persists a cancelled terminal result so
// the transcript never ends on an unresolved handoff tool call (strict
// providers require tool_use/tool_result pairing on resume). It is called from
// Shutdown after the event loop has fully exited, so reading pendingHandoff is
// race-free; the persist pump is already closed, so the write goes through the
// recovery manager synchronously.
func (a *MainAgent) settlePendingHandoffAtShutdown() {
	if a == nil || a.pendingHandoff == nil {
		return
	}
	pc := a.pendingHandoff
	a.pendingHandoff = nil
	if reqID := pc.RequestID; reqID != "" {
		a.interaction.settleHandoff(reqID)
	}
	if pc.CallID == "" {
		log.Warnf("settlePendingHandoffAtShutdown: missing handoff call metadata")
		return
	}
	toolMsg := message.Message{
		Role:           message.RoleTool,
		Content:        "Cancelled",
		ToolCallID:     pc.CallID,
		ToolStatus:     string(ToolResultStatusCancelled),
		ToolDurationMs: pc.Duration.Milliseconds(),
	}
	a.ctxMgr.Append(toolMsg)
	a.recordEvidenceFromMessage(toolMsg)
	if a.recovery != nil {
		if err := a.recovery.PersistMessage(identity.MainAgentID, toolMsg); err != nil {
			log.Warnf("failed to persist cancelled handoff result at shutdown error=%v", err)
		}
	}
}

// emitDeferredHandoffToolResult delivers the handoff tool call's terminal
// ToolResultEvent and persists its tool message, replaying the original call
// metadata (CallID, ArgsJSON, execution duration) captured when the tool ran.
func (a *MainAgent) emitDeferredHandoffToolResult(pc *HandoffResult, result string, status ToolResultStatus) {
	if pc == nil || pc.CallID == "" {
		log.Warnf("emitDeferredHandoffToolResult: missing handoff call metadata")
		return
	}
	a.emitToTUI(ToolResultEvent{
		CallID:   pc.CallID,
		Name:     tools.NameHandoff,
		ArgsJSON: pc.ArgsJSON,
		Result:   result,
		Status:   status,
		Duration: pc.Duration,
	})
	toolMsg := message.Message{
		Role:           message.RoleTool,
		Content:        result,
		ToolCallID:     pc.CallID,
		ToolStatus:     string(status),
		ToolDurationMs: pc.Duration.Milliseconds(),
	}
	a.ctxMgr.Append(toolMsg)
	a.recordEvidenceFromMessage(toolMsg)
	if a.recovery != nil {
		a.persistAsync(identity.MainAgentID, toolMsg)
	}
}
