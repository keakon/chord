package message

import (
	"testing"
)

func TestRepairOrphanToolResults_KeepsValidChain(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_a", Name: "Read", Args: []byte(`{}`)}}},
		{Role: "tool", ToolCallID: "call_a", Content: "ok"},
	}
	out, n := RepairOrphanToolResults(msgs)
	if n != 0 {
		t.Fatalf("removed = %d, want 0", n)
	}
	if len(out) != len(msgs) {
		t.Fatalf("len = %d, want %d", len(out), len(msgs))
	}
}

func TestRepairOrphanToolResults_DropsOrphanOutput(t *testing.T) {
	msgs := []Message{
		{Role: "assistant", ToolCalls: []ToolCall{
			{ID: "call_1", Name: "Read", Args: []byte(`{}`)},
			{ID: "call_2", Name: "Grep", Args: []byte(`{}`)},
		}},
		{Role: "tool", ToolCallID: "call_1", Content: "a"},
		{Role: "tool", ToolCallID: "call_2", Content: "b"},
		{Role: "tool", ToolCallID: "call_orphan", Content: "no assistant call"},
	}
	out, n := RepairOrphanToolResults(msgs)
	if n != 1 {
		t.Fatalf("removed = %d, want 1", n)
	}
	if len(out) != 3 {
		t.Fatalf("len = %d, want 3", len(out))
	}
	if out[2].Role != "tool" || out[2].ToolCallID != "call_2" {
		t.Fatalf("last kept message = %+v", out[2])
	}
}

func TestRepairOrphanToolResults_UserBetweenAssistantAndTool(t *testing.T) {
	msgs := []Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_x", Name: "Bash", Args: []byte(`{}`)}}},
		{Role: "user", Content: "wait"},
		{Role: "tool", ToolCallID: "call_x", Content: "done"},
	}
	out, n := RepairOrphanToolResults(msgs)
	if n != 0 || len(out) != 3 {
		t.Fatalf("n=%d len=%d want n=0 len=3", n, len(out))
	}
}

func TestRepairOrphanToolResults_NilSlice(t *testing.T) {
	out, n := RepairOrphanToolResults(nil)
	if out != nil || n != 0 {
		t.Fatalf("out=%v n=%d", out, n)
	}
}

func TestToolMessageSupportedByHistory_NearestAssistantWins(t *testing.T) {
	msgs := []Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "old", Name: "Read", Args: []byte(`{}`)}}},
		{Role: "tool", ToolCallID: "old", Content: "1"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "new", Name: "Read", Args: []byte(`{}`)}}},
		{Role: "tool", ToolCallID: "new", Content: "2"},
	}
	if !toolMessageSupportedByHistory(msgs, 3) {
		t.Fatal("expected new to match")
	}
	if toolMessageSupportedByHistory(msgs, 99) {
		t.Fatal("oob should be false")
	}
}
