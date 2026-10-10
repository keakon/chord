package llm

import (
	"fmt"
	"slices"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

// IsPendingAnthropicNativeTurn identifies a server call waiting for the client
// tool results from this assistant response. Its result arrives in a later
// assistant response, so the intervening user message must contain only results.
func IsPendingAnthropicNativeTurn(msg *message.Message) bool {
	if msg == nil || msg.Role != message.RoleAssistant || msg.NativeTools == nil || msg.NativeTools.Protocol != config.ProviderTypeMessages {
		return false
	}
	return slices.ContainsFunc(msg.NativeTools.Calls, func(call message.HostedCall) bool {
		return len(call.Result) == 0 && call.Error == ""
	})
}

func anthropicNativeContinuationIndex(messages []message.Message) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == message.RoleAssistant {
			if IsPendingAnthropicNativeTurn(&messages[i]) {
				return i
			}
			break
		}
	}
	return -1
}

func validateAnthropicNativeContinuation(messages []message.Message) error {
	index := anthropicNativeContinuationIndex(messages)
	if index < 0 {
		return nil
	}
	// pause_turn resumes the assistant as-is, without client results or input.
	if len(messages[index].ToolCalls) == 0 && index == len(messages)-1 {
		return nil
	}
	waiting := make(map[string]bool, len(messages[index].ToolCalls))
	for _, call := range messages[index].ToolCalls {
		waiting[call.ID] = true
	}
	for _, msg := range messages[index+1:] {
		if msg.Role == message.RoleTool && waiting[msg.ToolCallID] {
			delete(waiting, msg.ToolCallID)
			continue
		}
		// These hints are projected inside the final tool_result, never as
		// top-level user content. Independent inputs retain their own turn.
		if isAnthropicNativeOverlay(msg) && len(waiting) == 0 {
			continue
		}
		return fmt.Errorf("pending native Messages turn requires only its client tool results; defer independent input until it completes")
	}
	if len(waiting) != 0 || len(messages[index].ToolCalls) == 0 {
		return fmt.Errorf("pending native Messages turn is missing client tool results")
	}
	return nil
}

func isAnthropicNativeOverlay(msg message.Message) bool {
	return msg.Role == message.RoleUser && msg.Kind == message.KindTurnOverlay && !slices.ContainsFunc(msg.Parts, message.ContentPart.IsBinary)
}

func anthropicNativeOverlayText(msg message.Message) string {
	if len(msg.Parts) == 0 {
		return msg.Content
	}
	var text string
	for _, part := range msg.Parts {
		text = joinAdjacentPartText(text, part.Text)
	}
	return text
}

// anthropicCacheDurableMessageCount also excludes a tool result whose wire
// content includes transient reminders for an unfinished native turn.
func anthropicCacheDurableMessageCount(messages []message.Message) int {
	end := promptCacheDurableMessageCount(messages)
	if index := anthropicNativeContinuationIndex(messages); index >= 0 && end > index+1 && end < len(messages) {
		if slices.ContainsFunc(messages[end:], isAnthropicNativeOverlay) {
			return end - 1
		}
	}
	return end
}

// foldAnthropicNativeOverlay owns the modified blocks. Canonical tool results
// and signed assistant blocks remain untouched; only the wire projection grows.
func foldAnthropicNativeOverlay(result []anthropicMessage, text string) bool {
	if len(result) == 0 || result[len(result)-1].Role != "user" {
		return false
	}
	blocks, ok := result[len(result)-1].Content.([]anthropicContent)
	if !ok || len(blocks) == 0 || blocks[len(blocks)-1].Type != "tool_result" {
		return false
	}
	blocks = slices.Clone(blocks)
	last := &blocks[len(blocks)-1]
	switch content := last.Content.(type) {
	case string:
		last.Content = joinAdjacentPartText(content, text)
	case []anthropicContent:
		last.Content = append(slices.Clone(content), anthropicContent{Type: "text", Text: text})
	default:
		return false
	}
	result[len(result)-1].Content = blocks
	return true
}
