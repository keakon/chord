package agent

import (
	"context"

	"github.com/keakon/chord/internal/message"
)

// commitPromotedToolSideEffects applies the minimal post-execution side effects
// that speculative execution intentionally skipped, but that must still happen
// once a tool call is validated and committed to context.
func (a *MainAgent) commitPromotedToolSideEffects(tc message.ToolCall, payload *ToolResultPayload) {
	if a == nil || payload == nil || payload.Error != nil {
		return
	}
	if payload.speculativeHooks != nil && payload.speculativeHooks.commit != nil {
		payload.speculativeHooks.commit()
	}
	commitPromotedReadToolSideEffects(a.fileTrack, a.instanceID, tc.Name, payload.ArgsJSON, payload.FileState)
}

// executeToolCallWithHook is a small wrapper used by the streaming-tool finalize
// path: we sometimes need to run a tool call after running the on_tool_call hook
// ourselves (e.g. hook-induced args drift) and must avoid firing the hook twice.
func (a *MainAgent) executeToolCallWithHook(ctx context.Context, tc message.ToolCall, fireHook bool) (ToolExecutionResult, error) {
	if intercept, ok := a.maybeInterceptRepeatedToolCall(ctx, tc); ok {
		execResult := ToolExecutionResult{EffectiveArgsJSON: string(tc.Args), Result: intercept.toolResult}
		return execResult, intercept.confirmErr
	}
	return a.toolExecutionPipeline().execute(ctx, tc, fireHook)
}
