package modelcompat

import "github.com/keakon/chord/internal/message"

func collectNativeToolResultIDs(messages []message.Message) map[string]bool {
	var ids map[string]bool
	for _, msg := range messages {
		if msg.Role != message.RoleAssistant || msg.NativeTools == nil || len(msg.NativeTools.Items) == 0 {
			continue
		}
		for _, call := range msg.ToolCalls {
			if ids == nil {
				ids = make(map[string]bool)
			}
			ids[call.ID] = true
		}
	}
	return ids
}
