package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestNormalizeRestoredMessagesDropsTrailingInterruptedAssistants(t *testing.T) {
	msgs := []message.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "partial", StopReason: "interrupted"},
	}

	normalized := normalizeRestoredMessages(msgs)
	if len(normalized) != 1 {
		t.Fatalf("len(normalized) = %d, want 1", len(normalized))
	}
	if normalized[0].Role != "user" {
		t.Fatalf("normalized[0].Role = %q, want user", normalized[0].Role)
	}
}

func TestNormalizeRestoredMessagesKeepsInterruptedAssistantInMiddle(t *testing.T) {
	msgs := []message.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "partial", StopReason: "interrupted"},
		{Role: "user", Content: "continue"},
	}

	normalized := normalizeRestoredMessages(msgs)
	if len(normalized) != 3 {
		t.Fatalf("len(normalized) = %d, want 3", len(normalized))
	}
	if normalized[1].Role != "assistant" || normalized[1].StopReason != "interrupted" {
		t.Fatalf("normalized[1] = %#v, want interrupted assistant preserved", normalized[1])
	}
}

func TestNormalizeRestoredMessagesDropsOrphanToolResult(t *testing.T) {
	msgs := []message.Message{
		{Role: "user", Content: "hi"},
		{Role: "tool", ToolCallID: "missing", Content: "oops"},
	}

	normalized := normalizeRestoredMessages(msgs)
	if len(normalized) != 1 {
		t.Fatalf("len(normalized) = %d, want 1", len(normalized))
	}
	if normalized[0].Role != "user" {
		t.Fatalf("normalized[0].Role = %q, want user", normalized[0].Role)
	}
}

func TestNormalizeRestoredMessagesDropsEmptyToolCallIDResult(t *testing.T) {
	msgs := []message.Message{
		{Role: "user", Content: "hi"},
		{Role: "tool", Content: "oops"},
	}

	normalized := normalizeRestoredMessages(msgs)
	if len(normalized) != 1 {
		t.Fatalf("len(normalized) = %d, want 1", len(normalized))
	}
}

func TestNormalizeRestoredMessagesSynthesizesDanglingToolResultAtEOF(t *testing.T) {
	msgs := []message.Message{
		{Role: "user", Content: "inspect"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tool-main-1", Name: "WebFetch", Args: []byte(`{"url":"https://example.com"}`)}}},
	}

	normalized := normalizeRestoredMessages(msgs)
	if len(normalized) != 3 {
		t.Fatalf("len(normalized) = %d, want 3", len(normalized))
	}
	last := normalized[2]
	if last.Role != "tool" || last.ToolCallID != "tool-main-1" {
		t.Fatalf("last = %#v, want synthetic tool result for tool-main-1", last)
	}
	if !strings.Contains(last.Content, "Model stopped before completing this tool call") {
		t.Fatalf("last.Content = %q, want interruption prefix", last.Content)
	}
	if !strings.Contains(last.Content, "session restored before tool result was persisted") {
		t.Fatalf("last.Content = %q, want restore-specific cause", last.Content)
	}
}

func TestNormalizeRestoredMessagesSynthesizesDanglingToolResultBeforeNextUser(t *testing.T) {
	msgs := []message.Message{
		{Role: "user", Content: "first"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tool-main-1", Name: "Read", Args: []byte(`{"path":"go.mod"}`)}}},
		{Role: "user", Content: "second"},
	}

	normalized := normalizeRestoredMessages(msgs)
	if len(normalized) != 4 {
		t.Fatalf("len(normalized) = %d, want 4", len(normalized))
	}
	if normalized[2].Role != "tool" || normalized[2].ToolCallID != "tool-main-1" {
		t.Fatalf("normalized[2] = %#v, want synthetic tool result before next user", normalized[2])
	}
	if normalized[3].Role != "user" || normalized[3].Content != "second" {
		t.Fatalf("normalized[3] = %#v, want trailing user message preserved", normalized[3])
	}
}

func TestNormalizeRestoredMessagesSynthesizesOnlyUnresolvedToolCalls(t *testing.T) {
	msgs := []message.Message{
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "A", Name: "Read", Args: []byte(`{"path":"go.mod"}`)}, {ID: "B", Name: "Grep", Args: []byte(`{"pattern":"TODO","path":"."}`)}}},
		{Role: "tool", ToolCallID: "A", Content: "ok"},
	}

	normalized := normalizeRestoredMessages(msgs)
	if len(normalized) != 3 {
		t.Fatalf("len(normalized) = %d, want 3", len(normalized))
	}
	if normalized[1].ToolCallID != "A" {
		t.Fatalf("normalized[1] = %#v, want preserved tool A result", normalized[1])
	}
	if normalized[2].Role != "tool" || normalized[2].ToolCallID != "B" {
		t.Fatalf("normalized[2] = %#v, want synthetic tool B result", normalized[2])
	}
}

func TestNormalizeRestoredMessagesDuplicateToolResultDropsSecond(t *testing.T) {
	msgs := []message.Message{
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "A", Name: "Read", Args: []byte(`{"path":"go.mod"}`)}}},
		{Role: "tool", ToolCallID: "A", Content: "first"},
		{Role: "tool", ToolCallID: "A", Content: "second"},
	}

	normalized := normalizeRestoredMessages(msgs)
	if len(normalized) != 2 {
		t.Fatalf("len(normalized) = %d, want 2", len(normalized))
	}
	if normalized[1].Content != "first" {
		t.Fatalf("normalized[1].Content = %q, want first", normalized[1].Content)
	}
}
