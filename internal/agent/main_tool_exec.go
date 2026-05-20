package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
)

// ---------------------------------------------------------------------------
// Tool execution
// ---------------------------------------------------------------------------

// executeToolCall runs a single tool invocation with permission checks,
// output truncation.
func (a *MainAgent) executeToolCall(ctx context.Context, tc message.ToolCall) (ToolExecutionResult, error) {
	return a.toolExecutionPipeline().execute(ctx, tc, true)
}

// executeToolCallSpeculative runs a tool without firing hooks,
// or irreversible finalize-only side effects. Results are UI-only until the
// finalized call promotes them through the normal handleToolResult path.
func (a *MainAgent) executeToolCallSpeculative(ctx context.Context, tc message.ToolCall) (ToolExecutionResult, error) {
	return a.toolExecutionPipeline().executeSpeculative(ctx, tc)
}

func (a *MainAgent) toolExecutionPipeline() toolExecutionPipeline {
	return toolExecutionPipeline{
		agentID:      a.instanceID,
		eventAgentID: "",
		sessionDir:   a.sessionDir,
		registry:     a.tools,
		fileTrack:    a.fileTrack,
		eventSender:  a,
		emit:         a.emitToTUI,
		guidance:     mainToolOutputGuidance,
		currentRuleset: func() permission.Ruleset {
			return a.effectiveRuleset()
		},
		refreshRulesetAfterRuleIntent: func(toolName string, intent *ConfirmRuleIntent) permission.Ruleset {
			a.processRuleIntent(toolName, intent)
			return a.effectiveRuleset()
		},
		isInternalTool: isInternalControlTool,
		confirm:        a.confirmFn,
		currentTurnID:  a.currentTurnID,
		fireHook:       a.fireHook,
		updatePending: func(call PendingToolCall) {
			if a.turn != nil {
				a.turn.updatePendingToolCall(call)
			}
		},
		reservedToolError: func(name string) error {
			if isMainAgentReservedTool(name) {
				return fmt.Errorf("tool %q is reserved for SubAgents and unavailable to MainAgent", name)
			}
			return nil
		},
	}
}

// normalizeDenyReason trims surrounding whitespace in a deny reason while preserving
// the user's full text, including internal newlines, for display and model context.
func normalizeDenyReason(reason string) string {
	reason = strings.TrimSpace(reason)
	return reason
}
