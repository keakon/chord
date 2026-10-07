package message

import "encoding/json"

// Clone returns an ownership-isolated copy of a message. Message values are
// copied by value in many request and persistence paths, so this method is the
// single place that protects the canonical history from nested slices, maps,
// raw JSON and optional pointer fields being mutated by a caller.
func (m Message) Clone() Message {
	cloned := m
	cloned.Question = append(json.RawMessage(nil), m.Question...)
	if len(m.Parts) > 0 {
		cloned.Parts = make([]ContentPart, len(m.Parts))
		for i, part := range m.Parts {
			cloned.Parts[i] = part
			cloned.Parts[i].Data = append([]byte(nil), part.Data...)
		}
	}
	if len(m.ThinkingBlocks) > 0 {
		cloned.ThinkingBlocks = append([]ThinkingBlock(nil), m.ThinkingBlocks...)
	}
	if len(m.ResponsesOutput) > 0 {
		cloned.ResponsesOutput = make([]ResponsesOutputItem, len(m.ResponsesOutput))
		for i, item := range m.ResponsesOutput {
			cloned.ResponsesOutput[i] = item
			cloned.ResponsesOutput[i].Content = append([]ResponsesOutputContent(nil), item.Content...)
			cloned.ResponsesOutput[i].Summary = append([]ResponsesReasoningSummary(nil), item.Summary...)
		}
	}
	if len(m.GeminiParts) > 0 {
		cloned.GeminiParts = append([]GeminiReplayPart(nil), m.GeminiParts...)
	}
	if len(m.ToolCalls) > 0 {
		cloned.ToolCalls = make([]ToolCall, len(m.ToolCalls))
		for i, call := range m.ToolCalls {
			cloned.ToolCalls[i] = call
			cloned.ToolCalls[i].Args = append(json.RawMessage(nil), call.Args...)
		}
	}
	if len(m.ToolNotes) > 0 {
		cloned.ToolNotes = append([]string(nil), m.ToolNotes...)
	}
	if len(m.ToolChangedPaths) > 0 {
		cloned.ToolChangedPaths = append([]string(nil), m.ToolChangedPaths...)
	}
	if len(m.LSPReviews) > 0 {
		cloned.LSPReviews = append([]LSPReview(nil), m.LSPReviews...)
	}
	if m.FileState != nil {
		cloned.FileState = m.FileState.Clone()
	}
	if m.Audit != nil {
		cloned.Audit = m.Audit.Clone()
	}
	if len(m.CompactionFileRevisions) > 0 {
		cloned.CompactionFileRevisions = make(map[string]string, len(m.CompactionFileRevisions))
		for key, value := range m.CompactionFileRevisions {
			cloned.CompactionFileRevisions[key] = value
		}
	}
	if m.Provenance != nil {
		provenance := *m.Provenance
		cloned.Provenance = &provenance
	}
	if m.Usage != nil {
		usage := *m.Usage
		cloned.Usage = &usage
	}
	if m.Mailbox != nil {
		mailbox := *m.Mailbox
		cloned.Mailbox = &mailbox
	}
	if len(m.MCPTools) > 0 {
		cloned.MCPTools = cloneToolDefinitions(m.MCPTools)
	}
	return cloned
}

func cloneToolDefinitions(definitions []ToolDefinition) []ToolDefinition {
	cloned := make([]ToolDefinition, len(definitions))
	for i, definition := range definitions {
		cloned[i] = definition
		if definition.InputSchema != nil {
			cloned[i].InputSchema = cloneJSONMap(definition.InputSchema)
		}
	}
	return cloned
}

func cloneJSONMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	cloned := make(map[string]any, len(input))
	for key, value := range input {
		cloned[key] = cloneJSONValue(value)
	}
	return cloned
}

func cloneJSONValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		return cloneJSONMap(value)
	case []any:
		cloned := make([]any, len(value))
		for i, item := range value {
			cloned[i] = cloneJSONValue(item)
		}
		return cloned
	case []string:
		return append([]string(nil), value...)
	case json.RawMessage:
		return append(json.RawMessage(nil), value...)
	default:
		return value
	}
}
