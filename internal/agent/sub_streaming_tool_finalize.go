package agent

import (
	"context"

	"github.com/keakon/chord/internal/message"
)

func (s *SubAgent) commitPromotedToolSideEffects(tc message.ToolCall, result *toolResult) {
	if s == nil || result == nil || result.Error != nil {
		return
	}
	if result.speculativeHooks != nil && result.speculativeHooks.commit != nil {
		result.speculativeHooks.commit()
	}
	if s.parent == nil {
		return
	}
	commitPromotedReadToolSideEffects(s.parent.fileTrack, s.instanceID, tc.Name, result.ArgsJSON, result.FileState)
}

func (s *SubAgent) executeToolCallWithHook(ctx context.Context, tc message.ToolCall, fireHook bool) (ToolExecutionResult, error) {
	return s.toolExecutionPipeline().execute(ctx, tc, fireHook)
}

func (s *SubAgent) executeToolCallSpeculative(ctx context.Context, tc message.ToolCall) (ToolExecutionResult, error) {
	return s.toolExecutionPipeline().executeSpeculative(ctx, tc)
}
