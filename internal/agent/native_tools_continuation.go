package agent

import (
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

// Independent inputs must not end a Messages turn whose server call is waiting
// for local tool results. Derive this from canonical history, including resume.
func nativeMessagesTurnPending(mgr *ctxmgr.Manager) bool {
	if mgr == nil {
		return false
	}
	pending := false
	mgr.ScanBackward(func(msg *message.Message) bool {
		if msg.Role != message.RoleAssistant {
			return false
		}
		pending = llm.IsPendingAnthropicNativeTurn(msg)
		return true
	})
	return pending
}

// Context-only inputs (such as local shell output) retain their exact message
// semantics rather than passing through user command expansion. They rejoin the
// canonical transcript at a request/idle boundary after the native turn settles.
func (a *MainAgent) flushPendingNativeContextAppends(messages []message.Message, tailOverlayCount int) []message.Message {
	if len(a.pendingNativeContextAppends) == 0 || nativeMessagesTurnPending(a.ctxMgr) {
		return messages
	}
	pending := a.pendingNativeContextAppends
	a.pendingNativeContextAppends = nil
	for _, msg := range pending {
		a.handleAppendContext(Event{Payload: msg})
	}
	if messages == nil {
		return nil
	}
	insertion := len(messages) - min(max(tailOverlayCount, 0), len(messages))
	projected := make([]message.Message, 0, len(messages)+len(pending))
	projected = append(projected, messages[:insertion]...)
	projected = append(projected, pending...)
	return append(projected, messages[insertion:]...)
}
