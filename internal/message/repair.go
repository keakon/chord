package message

// toolMessageSupportedByHistory reports whether msgs[i] is a tool message whose
// ToolCallID appears in the nearest preceding assistant message that declares
// tool_calls. Used to strip orphan tool results that would break strict APIs
// (e.g. OpenAI Responses function_call_output without a matching function_call).
func toolMessageSupportedByHistory(msgs []Message, toolIdx int) bool {
	if toolIdx < 0 || toolIdx >= len(msgs) {
		return false
	}
	tid := msgs[toolIdx].ToolCallID
	if tid == "" {
		return true
	}
	for j := toolIdx - 1; j >= 0; j-- {
		if msgs[j].Role != "assistant" || len(msgs[j].ToolCalls) == 0 {
			continue
		}
		for _, tc := range msgs[j].ToolCalls {
			if tc.ID == tid {
				return true
			}
		}
		return false
	}
	return false
}

// RepairOrphanToolResults returns a copy of msgs with tool-role messages removed
// when no preceding assistant message declared that tool_call_id. It returns the
// number of dropped messages. Preserves nil vs empty slice: nil input yields nil, 0.
func RepairOrphanToolResults(msgs []Message) ([]Message, int) {
	if len(msgs) == 0 {
		return msgs, 0
	}
	out := make([]Message, 0, len(msgs))
	removed := 0
	for i := range msgs {
		msg := msgs[i]
		if msg.Role == "tool" && !toolMessageSupportedByHistory(msgs, i) {
			removed++
			continue
		}
		out = append(out, msg)
	}
	return out, removed
}
