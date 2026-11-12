package agent

import (
	"slices"

	"github.com/keakon/chord/internal/agent/requestshape"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

// releasePendingMCPMounts returns unresponded declarations to the pending set.
// Their captured user hints become transient again, behind any input absorbed
// by a retry. No assistant output has bound itself to this prefix yet.
func (a *MainAgent) releasePendingMCPMounts(messages []message.Message) []message.Message {
	state := &a.mcpMountState
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.snapshots) == 0 {
		return messages
	}
	out := make([]message.Message, 0, len(messages))
	var overlays []message.Message
	snapshotIndex := 0
	for i, msg := range messages {
		if msg.Role != message.RoleSystem || len(msg.MCPTools) == 0 {
			out = append(out, msg)
			continue
		}
		if snapshotIndex >= len(state.snapshots) ||
			!slices.EqualFunc(msg.MCPTools, state.snapshots[snapshotIndex].tools, func(a, b message.ToolDefinition) bool {
				return toolDefinitionSignature(a) == toolDefinitionSignature(b)
			}) {
			out = append(out, msg)
			continue
		}
		snapshot := state.snapshots[snapshotIndex]
		snapshotIndex++
		if respondedAfter(messages, i+1) {
			out = append(out, msg)
			continue
		}
		start := len(out) - len(snapshot.overlays)
		if start >= 0 && slices.EqualFunc(out[start:], snapshot.overlays, func(a, b message.Message) bool {
			return requestshape.ShapeOf(&a) == requestshape.ShapeOf(&b)
		}) {
			overlays = append(overlays, out[start:]...)
			out = out[:start]
		}
	}
	source, _ := mcpMountSource(out)
	state.rollbackUnrespondedDeclarations(source, state.incrementalMessageShapes(source))
	return append(out, overlays...)
}

func (a *MainAgent) finishMCPBoundaryRequest(messages []message.Message) *llm.RequestSnapshot {
	messages = a.releasePendingMCPMounts(messages)
	messages = a.mountPendingRuntimeMCPDeclarations(messages, a.mcpToolMountMode())
	return &llm.RequestSnapshot{
		Messages:        messages,
		MCPDeclarations: a.effectiveRuntimeMCPDeclarations(),
	}
}
