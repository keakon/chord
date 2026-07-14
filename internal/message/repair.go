package message

func assistantDeclaresToolCall(msg Message, callID string) bool {
	if msg.Role != RoleAssistant || callID == "" {
		return false
	}
	for _, tc := range msg.ToolCalls {
		if tc.ID == callID {
			return true
		}
	}
	return false
}

// repairAdjacentOutOfOrderToolResults fixes the durable-write race produced by
// older runtimes: one or more tool results could be appended immediately before
// the assistant message that declared them. Only a contiguous block where every
// result is uniquely justified by that following assistant is moved.
func repairAdjacentOutOfOrderToolResults(msgs []Message) []Message {
	if len(msgs) < 2 {
		return msgs
	}
	out := make([]Message, 0, len(msgs))
	for i := 0; i < len(msgs); {
		if msgs[i].Role != RoleTool {
			out = append(out, msgs[i])
			i++
			continue
		}
		end := i
		for end < len(msgs) && msgs[end].Role == RoleTool {
			end++
		}
		if end >= len(msgs) || msgs[end].Role != RoleAssistant {
			out = append(out, msgs[i:end]...)
			i = end
			continue
		}
		canMove := true
		for j := i; j < end; j++ {
			if toolMessageSupportedByHistory(msgs, j) || !assistantDeclaresToolCall(msgs[end], msgs[j].ToolCallID) {
				canMove = false
				break
			}
		}
		if !canMove {
			out = append(out, msgs[i:end]...)
			i = end
			continue
		}
		out = append(out, msgs[end])
		out = append(out, msgs[i:end]...)
		i = end + 1
	}
	return out
}

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
		if msgs[j].Role != RoleAssistant || len(msgs[j].ToolCalls) == 0 {
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
	msgs = repairAdjacentOutOfOrderToolResults(msgs)
	out := make([]Message, 0, len(msgs))
	removed := 0
	for i := range msgs {
		msg := msgs[i]
		if msg.Role == RoleTool && !toolMessageSupportedByHistory(msgs, i) {
			removed++
			continue
		}
		out = append(out, msg)
	}
	return out, removed
}
