package requestshape

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/toolname"
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
			{ID: "call-1", Name: "read", Args: json.RawMessage(`{"path":"a.go"}`), ThoughtSignature: "tool-sig"},
		},
		ThinkingBlocks:  []message.ThinkingBlock{{Thinking: "t", Signature: "s", Data: "d"}},
		ResponsesOutput: []message.ResponsesOutputItem{{Type: "reasoning", ID: "rs", EncryptedContent: "enc"}},
		GeminiParts:     []message.GeminiReplayPart{{Type: "text", Text: "hello", ThoughtSignature: "sig"}},
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
// Shape, extend both MessageEquivalent
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
		"compaction_request_sources": func(m *message.Message) {
			m.CompactionRequestSources = map[string]string{"original_request": "source-1"}
		},
		"compaction_flag": func(m *message.Message) { m.IsCompactionSummary = true },
		"tool_call_id_field": func(m *message.Message) {
			m.ToolCalls = []message.ToolCall{{ID: "x", Name: "read", Args: json.RawMessage(`{"path":"a.go"}`)}}
		},
		"tool_call_name": func(m *message.Message) {
			m.ToolCalls = []message.ToolCall{{ID: "call-1", Name: "grep", Args: json.RawMessage(`{"path":"a.go"}`)}}
		},
		"tool_call_args": func(m *message.Message) {
			m.ToolCalls = []message.ToolCall{{ID: "call-1", Name: "read", Args: json.RawMessage(`{"path":"b.go"}`)}}
		},
		"tool_call_signature": func(m *message.Message) { m.ToolCalls[0].ThoughtSignature = "changed" },
		"tool_calls_len":      func(m *message.Message) { m.ToolCalls = nil },
		"thinking_text": func(m *message.Message) {
			m.ThinkingBlocks = []message.ThinkingBlock{{Thinking: "changed", Signature: "s"}}
		},
		"thinking_signature": func(m *message.Message) {
			m.ThinkingBlocks = []message.ThinkingBlock{{Thinking: "t", Signature: "changed"}}
		},
		"thinking_len":     func(m *message.Message) { m.ThinkingBlocks = nil },
		"thinking_data":    func(m *message.Message) { m.ThinkingBlocks[0].Data = "changed" },
		"responses_output": func(m *message.Message) { m.ResponsesOutput[0].EncryptedContent = "changed" },
		"gemini_parts":     func(m *message.Message) { m.GeminiParts[0].ThoughtSignature = "changed" },
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
	if !MessageEquivalent(&base, &same) {
		t.Fatal("identical messages reported non-equivalent")
	}
	if ShapeOf(&base) != ShapeOf(&same) {
		t.Fatal("identical messages produced different shapes")
	}

	for name, mutate := range mutations {
		mutated := shapeEquivBaseMessage()
		mutate(&mutated)
		equiv := MessageEquivalent(&base, &mutated)
		shapesEqual := ShapeOf(&base) == ShapeOf(&mutated)
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
	if !MessageEquivalent(&base, &nonShape) {
		t.Fatal("non-shape field change unexpectedly broke equivalence")
	}
}

func TestStableReductionEmptySequenceHashMatchesHasher(t *testing.T) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], 0)
	inner := sha256.Sum256(length[:])
	want := sha256.Sum256(inner[:])

	if got := contentPartsHash(nil); got != want {
		t.Fatalf("empty content parts hash = %x, want %x", got, want)
	}
	if got := thinkingBlocksHash(nil); got != want {
		t.Fatalf("empty thinking blocks hash = %x, want %x", got, want)
	}
	if got := toolCallsHash(nil); got != want {
		t.Fatalf("empty tool calls hash = %x, want %x", got, want)
	}
}

func TestStableReductionSequenceHashesMatchStreamingEncoding(t *testing.T) {
	parts := []message.ContentPart{{
		Type: message.ContentPartText, Text: "text", DisplayText: "display", InlineToken: "token",
		MimeType: "text/plain", Data: []byte("data"), ImagePath: "/tmp/image", FileName: "file.txt",
	}}
	blocks := []message.ThinkingBlock{{Thinking: "reasoning", Signature: "signature"}}
	calls := []message.ToolCall{{ID: "call-1", Name: toolname.Read, Args: json.RawMessage(`{"path":"x.go"}`)}}

	assertHash := func(name string, got [sha256.Size]byte, write func(interface{ Write([]byte) (int, error) })) {
		t.Helper()
		h := sha256.New()
		write(h)
		want := sha256.Sum256(h.Sum(nil))
		if got != want {
			t.Fatalf("%s hash = %x, want %x", name, got, want)
		}
	}
	assertHash("content parts", contentPartsHash(parts), func(h interface{ Write([]byte) (int, error) }) {
		writeInt(h, len(parts))
		for _, part := range parts {
			writeString(h, string(part.Type))
			writeString(h, part.Text)
			writeString(h, part.DisplayText)
			writeString(h, part.InlineToken)
			writeString(h, part.MimeType)
			writeBytes(h, part.Data)
			writeString(h, part.ImagePath)
			writeString(h, part.FileName)
		}
	})
	assertHash("thinking blocks", thinkingBlocksHash(blocks), func(h interface{ Write([]byte) (int, error) }) {
		writeInt(h, len(blocks))
		for _, block := range blocks {
			writeString(h, block.Thinking)
			writeString(h, block.Signature)
			writeString(h, block.Data)
		}
	})
	assertHash("tool calls", toolCallsHash(calls), func(h interface{ Write([]byte) (int, error) }) {
		writeInt(h, len(calls))
		for _, call := range calls {
			writeString(h, call.ID)
			writeString(h, call.Name)
			writeBytes(h, call.Args)
			writeString(h, call.ThoughtSignature)
		}
	})
}

func TestStableReductionLargeBinaryPartAllocsGuard(t *testing.T) {
	parts := []message.ContentPart{{Type: message.ContentPartImage, Data: make([]byte, 4<<20)}}
	allocs := testing.AllocsPerRun(10, func() {
		_ = contentPartsHash(parts)
	})
	const maxAllocs = 20
	if allocs > maxAllocs {
		t.Fatalf("large binary content-part hash allocs = %.0f, want ≤%d", allocs, maxAllocs)
	}
}

func TestToolDefinitionsHashDistinguishesInputSchema(t *testing.T) {
	base := []message.ToolDefinition{{
		Name:        "shell",
		Description: "Run a command",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string"},
			},
		},
	}}
	same := []message.ToolDefinition{{
		Name:        "shell",
		Description: "Run a command",
		InputSchema: map[string]any{
			"properties": map[string]any{
				"command": map[string]any{"type": "string"},
			},
			"type": "object",
		},
	}}
	changedSchema := []message.ToolDefinition{{
		Name:        "shell",
		Description: "Run a command",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string"},
				"timeout": map[string]any{"type": "integer"},
			},
		},
	}}

	if ToolDefinitionsHash(base) != ToolDefinitionsHash(same) {
		t.Fatal("ToolDefinitionsHash() differs for schemas equal up to map key order")
	}
	if ToolDefinitionsHash(base) == ToolDefinitionsHash(changedSchema) {
		t.Fatal("ToolDefinitionsHash() collides when only InputSchema changed")
	}
}

func TestDeliveryMetadataFingerprintTracksIdentity(t *testing.T) {
	base := message.Message{DeliveredFiles: []message.DeliveredFile{{ID: "delivery", Locator: message.FileLocator{Kind: message.FileLocatorWorkspace, Workspace: &message.WorkspaceFileLocator{Root: "/project", Path: "/project/report.txt"}}}}}
	original := deliveryMetadataHash(base)
	changed := base.Clone()
	changed.DeliveredFiles[0].Locator.Workspace.Path = "/project/other.txt"
	if original == deliveryMetadataHash(changed) {
		t.Fatal("locator change did not invalidate fingerprint")
	}
	checkpoint := message.Message{CompactionDeliveredFiles: base.DeliveredFiles}
	if original == deliveryMetadataHash(checkpoint) {
		t.Fatal("checkpoint ownership change did not invalidate fingerprint")
	}
	if deliveryMetadataHash(message.Message{}) != deliveryMetadataHash(message.Message{DeliveredFiles: []message.DeliveredFile{}}) {
		t.Fatal("empty metadata representations disagree")
	}
	if deliveryMetadataHash(message.Message{}) == deliveryMetadataHash(message.Message{FileLinkBaseDir: "/project"}) {
		t.Fatal("source binding missing from fingerprint")
	}
}
