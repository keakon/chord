package agent

import (
	"encoding/json"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func shapeEquivBaseMessage() message.Message {
	return message.Message{
		Role:             message.RoleTool,
		Content:          "tool output body",
		ReasoningContent: "reasoning",
		ToolCallID:       "call-1",
		ToolDiff:         "@@ -1 +1 @@",
		ToolDiffAdded:    2,
		ToolDiffRemoved:  1,
		ToolStatus:       "success",
		Kind:             "",
		ToolCalls: []message.ToolCall{
			{ID: "call-1", Name: "read", Args: json.RawMessage(`{"path":"a.go"}`)},
		},
		ThinkingBlocks: []message.ThinkingBlock{{Thinking: "t", Signature: "s"}},
		Parts: []message.ContentPart{
			{Type: message.ContentPartText, Text: "hello", DisplayText: "hello", MimeType: "", Data: nil},
		},
		Provenance:          &message.MessageProvenance{Source: "llm", ProviderID: "p", ModelID: "m"},
		IsCompactionSummary: false,
	}
}

// TestStableReductionMessageEquivalentMatchesShapeHashes asserts that the
// field-equality fast path agrees with the hash-based shape comparison for
// every field the shape covers. If a field is added to
// stableReductionMessageShape, extend both stableReductionMessageEquivalent
// and this table.
func TestStableReductionMessageEquivalentMatchesShapeHashes(t *testing.T) {
	base := shapeEquivBaseMessage()

	mutations := map[string]func(*message.Message){
		"role":              func(m *message.Message) { m.Role = message.RoleUser },
		"content":           func(m *message.Message) { m.Content = "changed" },
		"reasoning":         func(m *message.Message) { m.ReasoningContent = "changed" },
		"tool_call_id":      func(m *message.Message) { m.ToolCallID = "call-2" },
		"tool_diff":         func(m *message.Message) { m.ToolDiff = "changed" },
		"tool_diff_added":   func(m *message.Message) { m.ToolDiffAdded = 9 },
		"tool_diff_removed": func(m *message.Message) { m.ToolDiffRemoved = 9 },
		"tool_status":       func(m *message.Message) { m.ToolStatus = "error" },
		"kind":              func(m *message.Message) { m.Kind = "loop_notice" },
		"compaction_flag":   func(m *message.Message) { m.IsCompactionSummary = true },
		"tool_call_id_field": func(m *message.Message) {
			m.ToolCalls = []message.ToolCall{{ID: "x", Name: "read", Args: json.RawMessage(`{"path":"a.go"}`)}}
		},
		"tool_call_name": func(m *message.Message) {
			m.ToolCalls = []message.ToolCall{{ID: "call-1", Name: "grep", Args: json.RawMessage(`{"path":"a.go"}`)}}
		},
		"tool_call_args": func(m *message.Message) {
			m.ToolCalls = []message.ToolCall{{ID: "call-1", Name: "read", Args: json.RawMessage(`{"path":"b.go"}`)}}
		},
		"tool_calls_len": func(m *message.Message) { m.ToolCalls = nil },
		"thinking_text": func(m *message.Message) {
			m.ThinkingBlocks = []message.ThinkingBlock{{Thinking: "changed", Signature: "s"}}
		},
		"thinking_signature": func(m *message.Message) {
			m.ThinkingBlocks = []message.ThinkingBlock{{Thinking: "t", Signature: "changed"}}
		},
		"thinking_len": func(m *message.Message) { m.ThinkingBlocks = nil },
		"part_text": func(m *message.Message) {
			m.Parts = []message.ContentPart{{Type: message.ContentPartText, Text: "changed", DisplayText: "hello"}}
		},
		"part_type": func(m *message.Message) {
			m.Parts = []message.ContentPart{{Type: message.ContentPartImage, Text: "hello", DisplayText: "hello"}}
		},
		"part_display_text": func(m *message.Message) {
			m.Parts = []message.ContentPart{{Type: message.ContentPartText, Text: "hello", DisplayText: "changed"}}
		},
		"part_data": func(m *message.Message) {
			m.Parts = []message.ContentPart{{Type: message.ContentPartText, Text: "hello", DisplayText: "hello", Data: []byte{1}}}
		},
		"parts_len":         func(m *message.Message) { m.Parts = nil },
		"provenance_nil":    func(m *message.Message) { m.Provenance = nil },
		"provenance_source": func(m *message.Message) { m.Provenance = &message.MessageProvenance{Source: "import"} },
	}

	// Identical copies must be equivalent and produce identical shapes.
	same := shapeEquivBaseMessage()
	if !stableReductionMessageEquivalent(&base, &same) {
		t.Fatal("identical messages reported non-equivalent")
	}
	if stableReductionMessageShapeOf(&base) != stableReductionMessageShapeOf(&same) {
		t.Fatal("identical messages produced different shapes")
	}

	for name, mutate := range mutations {
		mutated := shapeEquivBaseMessage()
		mutate(&mutated)
		equiv := stableReductionMessageEquivalent(&base, &mutated)
		shapesEqual := stableReductionMessageShapeOf(&base) == stableReductionMessageShapeOf(&mutated)
		if equiv != shapesEqual {
			t.Errorf("%s: equivalence (%v) disagrees with shape hash comparison (%v)", name, equiv, shapesEqual)
		}
		if equiv {
			t.Errorf("%s: mutation was not detected by the equivalence comparator", name)
		}
	}

	// Non-shape fields must not affect equivalence (mirrors hash semantics).
	nonShape := shapeEquivBaseMessage()
	nonShape.ToolDurationMs = 1234
	nonShape.StopReason = "end_turn"
	nonShape.Usage = &message.TokenUsage{}
	if !stableReductionMessageEquivalent(&base, &nonShape) {
		t.Fatal("non-shape field change unexpectedly broke equivalence")
	}
}
