package agent

import (
	"context"

	"github.com/keakon/chord/internal/message"
)

func (s *SubAgent) commitPromotedToolSideEffects(tc message.ToolCall, result *toolResult) error {
	if s == nil || result == nil || result.Error != nil {
		return nil
	}
	if result.speculativeHooks != nil && result.speculativeHooks.commit != nil {
		if err := result.speculativeHooks.commit(); err != nil {
			return err
		}
	}
	if s.parent == nil {
		return nil
	}
	commitPromotedReadToolSideEffects(s.parent.fileTrack, s.instanceID, tc.Name, result.ArgsJSON, result.FileState)
	return nil
}

func (s *SubAgent) executeToolCallWithHook(ctx context.Context, tc message.ToolCall, fireHook bool) (ToolExecutionResult, error) {
	return s.toolExecutionPipeline().execute(ctx, tc, fireHook)
}

func (s *SubAgent) executeToolCallSpeculative(ctx context.Context, tc message.ToolCall) (ToolExecutionResult, error) {
	return s.toolExecutionPipeline().executeSpeculative(ctx, tc)
}
