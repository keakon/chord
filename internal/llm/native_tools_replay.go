package llm

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/keakon/chord/internal/message"
)

// Canonical native blocks are immutable, including signatures and server tool
// receipts. Normalization can remove a client call or its result from the
// request projection. Reject that projection before dispatch rather than
// restoring an orphan call from the raw blocks or rewriting signed history.
func validateNativeClientToolReplay(messages []message.Message) error {
	if err := validateAnthropicNativeContinuation(messages); err != nil {
		return err
	}
	if !slices.ContainsFunc(messages, func(msg message.Message) bool { return msg.NativeTools != nil && len(msg.NativeTools.Items) > 0 }) {
		return nil
	}
	results := make(map[string]bool)
	for _, msg := range slices.Backward(messages) {
		if msg.Role == message.RoleTool {
			results[msg.ToolCallID] = true
		}
		if msg.Role != message.RoleAssistant || msg.NativeTools == nil {
			continue
		}
		calls := make(map[string]bool, len(msg.ToolCalls))
		for _, call := range msg.ToolCalls {
			calls[call.ID] = true
		}
		for _, raw := range msg.NativeTools.Items {
			var item struct {
				Type, ID string
				CallID   string `json:"call_id"`
			}
			if err := json.Unmarshal(raw, &item); err != nil {
				return fmt.Errorf("decode native replay item: %w", err)
			}
			var id string
			switch item.Type {
			case "function_call", "custom_tool_call":
				id = item.CallID
			case "tool_use":
				id = item.ID
			default:
				continue
			}
			if id == "" || !calls[id] || !results[id] {
				return fmt.Errorf("native history contains an unpaired client tool call %q; start a new session", id)
			}
			delete(results, id)
			delete(calls, id)
		}
		if len(calls) > 0 {
			return fmt.Errorf("native history is missing client tool declarations; start a new session")
		}
	}
	return nil
}
