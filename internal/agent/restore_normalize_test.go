package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestNormalizeRestoredMessages_DropsTrailingInterruptedAssistant(t *testing.T) {
	msgs := []message.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "partial", StopReason: "interrupted"},
	}
	got := normalizeRestoredMessages(msgs)
	if len(got) != 1 || got[0].Role != "user" {
		t.Fatalf("unexpected result: %#v", got)
	}
}

func TestNormalizeRestoredMessages_KeepsCompletedAssistant(t *testing.T) {
	msgs := []message.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "done", StopReason: "stop"},
	}
	got := normalizeRestoredMessages(msgs)
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d: %#v", len(got), got)
	}
}

func TestNormalizeRestoredMessages_PreservesPairedToolCalls(t *testing.T) {
	msgs := []message.Message{
		{Role: "user", Content: "do it"},
		{
			Role: "assistant",
			ToolCalls: []message.ToolCall{
				{ID: "call_1", Name: "read"},
			},
		},
		{Role: "tool", ToolCallID: "call_1", Content: "ok", ToolStatus: string(ToolResultStatusSuccess)},
	}
	got := normalizeRestoredMessages(msgs)
	if len(got) != 3 {
		t.Fatalf("expected 3 messages, got %d: %#v", len(got), got)
	}
	if got[2].ToolCallID != "call_1" || got[2].ToolStatus != string(ToolResultStatusSuccess) {
		t.Fatalf("paired tool message altered: %#v", got[2])
	}
}

func TestNormalizeRestoredMessages_SynthesizesOrphanToolResult(t *testing.T) {
	msgs := []message.Message{
		{Role: "user", Content: "do it"},
		{
			Role: "assistant",
			ToolCalls: []message.ToolCall{
				{ID: "call_orphan", Name: "shell"},
			},
		},
		{Role: "user", Content: "ping"},
	}
	got := normalizeRestoredMessages(msgs)
	if len(got) != 4 {
		t.Fatalf("expected synthesized tool result, got %d messages: %#v", len(got), got)
	}
	synth := got[2]
	if synth.Role != "tool" || synth.ToolCallID != "call_orphan" {
		t.Fatalf("synthesized message not in tool position: %#v", synth)
	}
	if synth.ToolStatus != string(ToolResultStatusError) {
		t.Fatalf("synthesized tool status = %q, want error", synth.ToolStatus)
	}
	if !strings.Contains(synth.Content, "Model stopped") {
		t.Fatalf("synthesized content = %q", synth.Content)
	}
}

func TestNormalizeRestoredMessages_SynthesizesOrphansAtTail(t *testing.T) {
	msgs := []message.Message{
		{Role: "user", Content: "do it"},
		{
			Role: "assistant",
			ToolCalls: []message.ToolCall{
				{ID: "call_a", Name: "shell"},
				{ID: "call_b", Name: "shell"},
			},
		},
		{Role: "tool", ToolCallID: "call_a", Content: "ok", ToolStatus: string(ToolResultStatusSuccess)},
	}
	got := normalizeRestoredMessages(msgs)
	if len(got) != 4 {
		t.Fatalf("expected 4 messages, got %d: %#v", len(got), got)
	}
	tail := got[3]
	if tail.Role != "tool" || tail.ToolCallID != "call_b" || tail.ToolStatus != string(ToolResultStatusError) {
		t.Fatalf("expected synthesized tail tool error for call_b, got %#v", tail)
	}
}

func TestNormalizeRestoredMessages_DropsDuplicateToolResults(t *testing.T) {
	msgs := []message.Message{
		{
			Role: "assistant",
			ToolCalls: []message.ToolCall{
				{ID: "call_a", Name: "shell"},
			},
		},
		{Role: "tool", ToolCallID: "call_a", Content: "first", ToolStatus: string(ToolResultStatusSuccess)},
		{Role: "tool", ToolCallID: "call_a", Content: "second", ToolStatus: string(ToolResultStatusSuccess)},
	}
	got := normalizeRestoredMessages(msgs)
	if len(got) != 2 {
		t.Fatalf("expected duplicate tool result to be dropped, got %d messages: %#v", len(got), got)
	}
	if got[1].Role != "tool" || got[1].ToolCallID != "call_a" || got[1].Content != "first" {
		t.Fatalf("unexpected preserved tool result: %#v", got[1])
	}
}

func TestNormalizeRestoredMessages_DoesNotMutateToolContentText(t *testing.T) {
	msgs := []message.Message{
		{
			Role: "assistant",
			ToolCalls: []message.ToolCall{
				{ID: "call_x", Name: "shell"},
			},
		},
		{
			Role:       "tool",
			ToolCallID: "call_x",
			Content:    "permission denied by upstream service",
			ToolStatus: string(ToolResultStatusSuccess),
		},
	}
	got := normalizeRestoredMessages(msgs)
	if got[1].Content != "permission denied by upstream service" {
		t.Fatalf("tool content rewritten by heuristics: %q", got[1].Content)
	}
	if got[1].ToolStatus != string(ToolResultStatusSuccess) {
		t.Fatalf("tool status rewritten: %q", got[1].ToolStatus)
	}
}
