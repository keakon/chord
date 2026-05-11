package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
)

// ---------------------------------------------------------------------------
// Tool execution
// ---------------------------------------------------------------------------

// executeToolCall runs a single tool invocation with permission checks,
// repetition detection, and output truncation.
func (a *MainAgent) executeToolCall(ctx context.Context, tc message.ToolCall) (ToolExecutionResult, error) {
	return a.toolExecutionPipeline().execute(ctx, tc, true)
}

// executeToolCallSpeculative runs a tool without firing hooks, repetition detection,
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
		checkRepetition: func(name string, args json.RawMessage) bool {
			if a.repetition == nil {
				return true
			}
			a.repMu.Lock()
			allowed := a.repetition.Check(name, args)
			a.repMu.Unlock()
			return allowed
		},
		reservedToolError: func(name string) error {
			if isMainAgentReservedTool(name) {
				return fmt.Errorf("tool %q is reserved for SubAgents and unavailable to MainAgent", name)
			}
			return nil
		},
	}
}

// normalizeDenyReason trims, collapses newlines, and limits length of a deny reason.
func normalizeDenyReason(reason string) string {
	reason = strings.TrimSpace(reason)
	reason = strings.ReplaceAll(reason, "\n", " ")
	if len([]rune(reason)) > 200 {
		reason = string([]rune(reason)[:200])
	}
	return reason
}
