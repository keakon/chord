package agent

import (
	"errors"
	"strings"

	"github.com/keakon/chord/internal/message"
)

func normalizeRestoredMessages(msgs []message.Message) []message.Message {
	if len(msgs) == 0 {
		return msgs
	}
	msgs = dropTrailingInterruptedAssistants(msgs)
	if len(msgs) == 0 {
		return msgs
	}
	return normalizeRestoredToolChain(msgs)
}

func dropTrailingInterruptedAssistants(msgs []message.Message) []message.Message {
	for len(msgs) > 0 {
		last := msgs[len(msgs)-1]
		if last.Role != "assistant" || last.StopReason != "interrupted" {
			break
		}
		msgs = msgs[:len(msgs)-1]
	}
	return msgs
}

func normalizeRestoredToolChain(msgs []message.Message) []message.Message {
	out := make([]message.Message, 0, len(msgs))
	toolNames := make(map[string]string)
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
				toolNames[tc.ID] = strings.TrimSpace(tc.Name)
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
			if normalized, ok := normalizeBlankReadToolResult(msg, toolNames[msg.ToolCallID]); ok {
				msg = normalized
			}
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

func normalizeBlankReadToolResult(msg message.Message, toolName string) (message.Message, bool) {
	if strings.TrimSpace(toolName) != "Read" {
		return msg, false
	}
	content := strings.TrimRight(msg.Content, "\n")
	before, after, ok := strings.Cut(content, "\t")
	if !ok {
		return msg, false
	}
	if strings.TrimSpace(after) != "" {
		return msg, false
	}
	trimmedLineNo := strings.TrimSpace(before)
	if trimmedLineNo == "" {
		return msg, false
	}
	for _, r := range trimmedLineNo {
		if r < '0' || r > '9' {
			return msg, false
		}
	}
	msg.Content = "(empty file)"
	return msg, true
}

var errRestoreToolResultMissing = errors.New("session restored before tool result was persisted")

func syntheticInterruptedToolResult(callID string) message.Message {
	return message.Message{
		Role:       "tool",
		ToolCallID: callID,
		Content:    toolCallFailureMessage(errRestoreToolResultMissing),
	}
}
