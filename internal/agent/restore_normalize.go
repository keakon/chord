package agent

import (
	"errors"
	"strings"

	"github.com/keakon/chord/internal/message"
)

// normalizeRestoredMessages repairs structural defects that can survive a
// session interruption: trailing assistants stopped mid-stream (interrupted),
// and tool_calls whose matching tool result never got persisted before the
// process exited.
//
// New session writes are canonical and do not rely on this layer — it only
// runs on transcripts loaded from disk on resume. Anything that depends on
// payload content shape (text heuristics, missing ToolStatus fields, etc.)
// belongs at write time, not here.
func normalizeRestoredMessages(msgs []message.Message) []message.Message {
	if len(msgs) == 0 {
		return msgs
	}
	msgs = dropTrailingInterruptedAssistants(msgs)
	if len(msgs) == 0 {
		return msgs
	}
	msgs = dropEmptyAssistantMessages(msgs)
	if len(msgs) == 0 {
		return msgs
	}
	return repairOrphanToolCalls(msgs)
}

func dropTrailingInterruptedAssistants(msgs []message.Message) []message.Message {
	for len(msgs) > 0 {
		last := msgs[len(msgs)-1]
		if last.Role != "assistant" || last.StopReason != "interrupted" {
			break
		}
		if assistantMessageHasOutput(last) && len(last.ToolCalls) == 0 {
			break
		}
		msgs = msgs[:len(msgs)-1]
	}
	return msgs
}

func dropEmptyAssistantMessages(msgs []message.Message) []message.Message {
	out := msgs[:0]
	for _, msg := range msgs {
		if msg.Role == "assistant" && !assistantMessageHasOutput(msg) {
			continue
		}
		out = append(out, msg)
	}
	return out
}

func assistantMessageHasOutput(msg message.Message) bool {
	if strings.TrimSpace(msg.Content) != "" || len(msg.Parts) > 0 || len(msg.ToolCalls) > 0 || message.HasReplayableThinkingBlocks(msg.ThinkingBlocks) {
		return true
	}
	for _, item := range msg.ResponsesOutput {
		if item.Type == "message" || item.Type == "function_call" {
			return true
		}
	}
	for _, part := range msg.GeminiParts {
		if part.Type == "text" || part.Type == "function_call" {
			return true
		}
	}
	return false
}

// repairOrphanToolCalls walks the transcript and synthesizes an error tool
// message for every assistant tool_call whose matching tool result is missing.
// Without this, sending the loaded history to a provider that requires
// function_call ↔ function_call_output pairing (OpenAI Responses, Anthropic
// tool_use ↔ tool_result) produces an API 400.
func repairOrphanToolCalls(msgs []message.Message) []message.Message {
	out := make([]message.Message, 0, len(msgs))
	pending := make(map[string]struct{})
	pendingOrder := make([]string, 0)
	flushPending := func() {
		for _, id := range pendingOrder {
			if _, ok := pending[id]; !ok {
				continue
			}
			out = append(out, syntheticInterruptedToolResult(id))
			delete(pending, id)
		}
		pendingOrder = pendingOrder[:0]
	}

	for _, msg := range msgs {
		if msg.Role != "tool" && len(pending) > 0 {
			flushPending()
		}

		switch msg.Role {
		case "assistant":
			out = append(out, msg)
			for _, tc := range msg.ToolCalls {
				if tc.ID == "" {
					continue
				}
				if _, exists := pending[tc.ID]; exists {
					continue
				}
				pending[tc.ID] = struct{}{}
				pendingOrder = append(pendingOrder, tc.ID)
			}

		case "tool":
			if msg.ToolCallID == "" {
				continue
			}
			if _, ok := pending[msg.ToolCallID]; !ok {
				continue
			}
			delete(pending, msg.ToolCallID)
			out = append(out, msg)

		default:
			out = append(out, msg)
		}
	}

	if len(pending) > 0 {
		flushPending()
	}
	return out
}

var errRestoreToolResultMissing = errors.New("session restored before tool result was persisted")

func syntheticInterruptedToolResult(callID string) message.Message {
	return message.Message{
		Role:       "tool",
		ToolCallID: callID,
		Content:    toolCallFailureMessage(errRestoreToolResultMissing),
		ToolStatus: string(ToolResultStatusError),
	}
}
