package agent

import (
	"github.com/keakon/chord/internal/agent/requestshape"
	"github.com/keakon/chord/internal/message"
)

func cloneMessageSliceForRequestShape(messages []message.Message) []message.Message {
	if len(messages) == 0 {
		return nil
	}
	cloned := make([]message.Message, len(messages))
	for i := range messages {
		cloned[i] = messages[i].Clone()
	}
	return cloned
}

// cowRequestShapeSlice builds the stored request-shape copy of prepared
// against the previous stored copy: field-equal messages share the previous
// copy's message values, only changed messages deep-clone. Sharing is safe
// because the previous copy is already storage-owned, the LLM layer
// deep-copies messages per target before any mutation
// (modelcompat.NormalizeForTarget), and every stored-prefix consumer treats it
// as read-only — so an in-place edit cannot leak into storage.
//
// "Field-equal" is requestshape.MessageEquivalent, which compares the
// reduction shape: the role, content, tool calls, provenance and parts the
// shape hash covers. Bookkeeping fields outside that set — FileState,
// LSPReviews, ToolPayload, ToolNotes, Usage, StopReason, MCPTools — are not
// compared, so a shared message keeps the values the previous stored copy had.
// That holds because ctxmgr messages are not rewritten after append; a future
// field that does change in place must either join the shape or force a clone.
func cowRequestShapeSlice(previous, prepared []message.Message) []message.Message {
	if len(prepared) == 0 {
		return nil
	}
	cloned := make([]message.Message, len(prepared))
	for i := range prepared {
		if i < len(previous) && requestshape.MessageEquivalent(&previous[i], &prepared[i]) {
			cloned[i] = previous[i]
			continue
		}
		cloned[i] = prepared[i].Clone()
	}
	return cloned
}
