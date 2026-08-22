package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

// commitPromotedToolSideEffects applies the minimal post-execution side effects
// that speculative execution intentionally skipped, but that must still happen
// once a tool call is validated and committed to context.
func (a *MainAgent) commitPromotedToolSideEffects(tc message.ToolCall, payload *ToolResultPayload) error {
	if a == nil || payload == nil || !toolExecutionCommitted(payload.Error) {
		return nil
	}
	if payload.speculativeHooks != nil && payload.speculativeHooks.commit != nil {
		if err := appendPromotedTodoActivity(a.recovery, identity.MainAgentID, payload.TurnID, tc); err != nil {
			return err
		}
		if err := payload.speculativeHooks.commit(); err != nil {
			return err
		}
	}
	commitPromotedReadToolSideEffects(a.fileTrack, a.instanceID, tc.Name, payload.Result, payload.FileState)
	return nil
}

func appendPromotedTodoActivity(rm *recovery.RecoveryManager, agentID string, turnID uint64, tc message.ToolCall) error {
	if rm == nil || tools.NormalizeName(tc.Name) != tools.NameTodoWrite {
		return nil
	}
	if err := rm.AppendToolActivity(recovery.ToolActivityRecord{
		CallID:  tc.ID,
		AgentID: agentID,
		TurnID:  turnID,
		Tool:    tools.NameTodoWrite,
		State:   recovery.ToolActivityStateStarted,
		TS:      time.Now().UnixNano(),
	}); err != nil {
		return fmt.Errorf("record promoted todo execution: %w", err)
	}
	return nil
}

// executeToolCallWithHook is a small wrapper used by the streaming-tool finalize
// path: we sometimes need to run a tool call after running the on_tool_call hook
// ourselves (e.g. hook-induced args drift) and must avoid firing the hook twice.
func (a *MainAgent) executeToolCallWithHook(ctx context.Context, tc message.ToolCall, fireHook bool) (ToolExecutionResult, error) {
	if intercept, ok := a.maybeInterceptRepeatedToolCall(ctx, tc); ok {
		execResult := ToolExecutionResult{
			EffectiveArgsJSON: string(tc.Args),
			Result:            intercept.toolResult,
			// See executeToolCall: intercepted calls never execute a tool body, so
			// their duration must not include the confirmation wait.
			ExecStartedAt:  time.Now(),
			walltimeTarget: a.captureMainWalltimeTarget(),
		}
		return execResult, intercept.confirmErr
	}
	return a.toolExecutionPipeline().execute(ctx, tc, fireHook)
}
