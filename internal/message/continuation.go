package message

import "slices"

// LastConversationMessageIndex skips local facts that never participate in a
// model turn. User-role question metadata is local too, even though it records
// an explicit user action.
func LastConversationMessageIndex(messages []Message) int {
	for i, message := range slices.Backward(messages) {
		if message.Kind == KindQuestionState {
			continue
		}
		switch message.Role {
		case RoleUser, RoleTool, RoleAssistant:
			return i
		}
	}
	return -1
}
