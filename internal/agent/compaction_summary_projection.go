package agent

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/keakon/chord/internal/message"
)

const (
	summaryReplayReasoning    = "reasoning"
	summaryReplayMessage      = "message"
	summaryReplayFunctionCall = "function_call"
	summaryReplayThought      = "thought"
	summaryReplayText         = "text"
)

// A lossy Markdown archive cannot recover replay-only facts. The runner must
// preserve the original history instead of committing a fallback checkpoint.
var errCompactionSummaryProjection = errors.New("compaction summary projection")

// projectMessagesForCompactionSummary removes independent reasoning from a
// summary-only copy. Retained nested data is shared and must remain read-only;
// this is not a general-purpose deep clone. Message indices stay unchanged.
func projectMessagesForCompactionSummary(messages []message.Message) ([]message.Message, error) {
	if messages == nil {
		return nil, nil
	}
	projected := make([]message.Message, len(messages))
	for i, msg := range messages {
		if msg.Role == message.RoleAssistant {
			canonicalText := strings.TrimSpace(msg.Content) != ""
			for _, part := range msg.Parts {
				if part.Type == message.ContentPartText && strings.TrimSpace(part.Text) != "" {
					canonicalText = true
				}
			}
			visibleReplay := false
			callIDs := make(map[string]bool, len(msg.ToolCalls))
			for _, call := range msg.ToolCalls {
				callIDs[call.ID] = true
			}
			for _, item := range msg.ResponsesOutput {
				if item.Type == summaryReplayMessage {
					for _, content := range item.Content {
						if strings.TrimSpace(content.Text) != "" || strings.TrimSpace(content.Refusal) != "" {
							visibleReplay = true
						}
					}
				}
			}
			for _, part := range msg.GeminiParts {
				if part.Type == summaryReplayText && strings.TrimSpace(part.Text) != "" {
					visibleReplay = true
				}
			}
			if visibleReplay && !canonicalText {
				return nil, fmt.Errorf("%w: message %d has visible replay text without canonical text", errCompactionSummaryProjection, i)
			}
			for _, item := range msg.ResponsesOutput {
				if item.Type == summaryReplayFunctionCall && (item.CallID == "" || !callIDs[item.CallID]) {
					return nil, fmt.Errorf("%w: message %d has replay call without canonical tool call", errCompactionSummaryProjection, i)
				}
			}
			for _, part := range msg.GeminiParts {
				if part.Type == summaryReplayFunctionCall && (part.ToolCallID == "" || !callIDs[part.ToolCallID]) {
					return nil, fmt.Errorf("%w: message %d has replay call without canonical tool call", errCompactionSummaryProjection, i)
				}
			}
		}
		msg.ThinkingBlocks = nil
		msg.ReasoningContent = ""
		if msg.ResponsesOutput != nil {
			items := make([]message.ResponsesOutputItem, 0, len(msg.ResponsesOutput))
			for _, item := range msg.ResponsesOutput {
				if item.Type == summaryReplayReasoning {
					continue
				}
				item.Summary = nil
				item.EncryptedContent = ""
				items = append(items, item)
			}
			msg.ResponsesOutput = items
		}
		if msg.GeminiParts != nil {
			parts := make([]message.GeminiReplayPart, 0, len(msg.GeminiParts))
			for _, part := range msg.GeminiParts {
				if part.Type == summaryReplayThought {
					continue
				}
				part.ThoughtSignature = ""
				parts = append(parts, part)
			}
			msg.GeminiParts = parts
		}
		msg.ToolCalls = slices.Clone(msg.ToolCalls)
		for j := range msg.ToolCalls {
			msg.ToolCalls[j].ThoughtSignature = ""
		}
		projected[i] = msg
	}
	return projected, nil
}

func (a *MainAgent) prepareCompactionSummaryMessages(head []message.Message) (pruned []message.Message, summaryMessages []message.Message, err error) {
	pruned = a.compactionReductionScratch().prepareMessagesForLLM(head)
	projected, err := projectMessagesForCompactionSummary(pruned)
	if err != nil {
		return nil, nil, err
	}
	return pruned, normalizeMessagesForSummary(projected), nil
}
