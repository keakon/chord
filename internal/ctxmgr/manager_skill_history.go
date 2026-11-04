package ctxmgr

import (
	"slices"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/toolname"
)

// SkillHistorySnapshot returns an ownership-isolated projection for skill
// load/activation state, plus the maximum request batch across the full history.
// It preserves turn boundaries, checkpoints, skill calls/results and activation
// evidence, but omits unrelated output, attachments and replay/file metadata.
// Catalog, permission and body-age decisions remain with the caller.
func (m *Manager) SkillHistorySnapshot() ([]message.Message, uint64) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	relevantCalls := make(map[string]struct{})
	var maxBatch uint64
	for i := range m.messages {
		msg := &m.messages[i]
		maxBatch = max(maxBatch, msg.RequestBatch)
		if msg.Role == message.RoleAssistant {
			for _, call := range msg.ToolCalls {
				if toolname.Normalize(call.Name) == toolname.Skill {
					relevantCalls[call.ID] = struct{}{}
				}
			}
		}
		if msg.SkillActivation != nil {
			relevantCalls[msg.ToolCallID] = struct{}{}
		}
	}

	var out []message.Message
	for i := range m.messages {
		msg := &m.messages[i]
		hasEvidence := msg.SkillActivation != nil || msg.SkillDiscovery != nil || msg.CompactionSkillActivation != nil
		_, relevantResult := relevantCalls[msg.ToolCallID]
		relevantResult = relevantResult && msg.Role == message.RoleTool
		keep := hasEvidence || relevantResult || msg.IsCompactionSummary ||
			msg.Kind == message.KindSkillDiscovery || message.IsUserAuthored(*msg)

		var calls []message.ToolCall
		if msg.Role == message.RoleAssistant {
			for _, call := range msg.ToolCalls {
				if _, ok := relevantCalls[call.ID]; !ok {
					continue
				}
				// Retain every declaration of a relevant ID, even if a later
				// declaration changes its name. Filtering by name here could
				// turn an invalid pair into valid load/activation evidence.
				copied := message.ToolCall{ID: call.ID, Name: call.Name}
				if toolname.Normalize(call.Name) == toolname.Skill {
					copied.Args = slices.Clone(call.Args)
				}
				calls = append(calls, copied)
			}
		}
		if !keep && len(calls) == 0 {
			continue
		}
		projected := message.Message{
			Role: msg.Role, Kind: msg.Kind, RequestBatch: msg.RequestBatch,
			IsCompactionSummary:       msg.IsCompactionSummary,
			ToolCalls:                 calls,
			ToolCallID:                msg.ToolCallID,
			ToolStatus:                msg.ToolStatus,
			Question:                  slices.Clone(msg.Question),
			SkillActivation:           msg.SkillActivation.Clone(),
			SkillDiscovery:            msg.SkillDiscovery.Clone(),
			CompactionSkillActivation: msg.CompactionSkillActivation.Clone(),
		}
		if relevantResult || msg.IsCompactionSummary || msg.SkillDiscovery != nil {
			projected.Content = msg.Content
		}
		out = append(out, projected)
	}
	return out, maxBatch
}
