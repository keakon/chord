package llm

import "github.com/keakon/chord/internal/message"

// promptCacheDurableMessageCount excludes only the transient suffix. A file
// snapshot may use KindTurnOverlay earlier in the request; it must not hide
// all the following durable messages from the cache renderer.
func promptCacheDurableMessageCount(messages []message.Message) int {
	end := len(messages)
	for end > 0 {
		switch messages[end-1].Kind {
		case message.KindTurnOverlay, message.KindThinkingReplayPrefix:
			end--
		default:
			return end
		}
	}
	return end
}
