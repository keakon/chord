package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

func TestNormalizeRestoredMessages_KeepsTrailingInterruptedTextAssistant(t *testing.T) {
	msgs := []message.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "partial", StopReason: "interrupted"},
	}
	got := normalizeRestoredMessages(msgs, nil, "main")
	if len(got) != 2 || got[1].Role != "assistant" || got[1].Content != "partial" || got[1].StopReason != "interrupted" {
		t.Fatalf("unexpected result: %#v", got)
	}
}

func TestNormalizeRestoredMessages_DropsTrailingInterruptedToolAssistant(t *testing.T) {
	msgs := []message.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", StopReason: "interrupted", ToolCalls: []message.ToolCall{{ID: "call_1", Name: "Read"}}},
	}
	got := normalizeRestoredMessages(msgs, nil, "main")
	if len(got) != 1 || got[0].Role != "user" {
		t.Fatalf("unexpected result: %#v", got)
	}
}

func TestNormalizeRestoredMessages_KeepsCompletedAssistant(t *testing.T) {
	msgs := []message.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "done", StopReason: "stop"},
	}
	got := normalizeRestoredMessages(msgs, nil, "main")
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d: %#v", len(got), got)
	}
}

func TestNormalizeRestoredMessages_DropsEmptyAssistant(t *testing.T) {
	msgs := []message.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", ThinkingBlocks: []message.ThinkingBlock{{Thinking: "analysis"}}, StopReason: "max_tokens"},
		{Role: "assistant", ReasoningContent: "hidden reasoning", StopReason: "max_tokens"},
		{Role: "user", Content: "continue"},
	}
	got := normalizeRestoredMessages(msgs, nil, "main")
	if len(got) != 2 || got[0].Role != "user" || got[1].Content != "continue" {
		t.Fatalf("unexpected result: %#v", got)
	}
}

func TestNormalizeRestoredMessages_KeepsProviderOutputAssistant(t *testing.T) {
	msgs := []message.Message{
		{Role: "user", Content: "continue"},
		{
			Role: message.RoleAssistant,
			ResponsesOutput: []message.ResponsesOutputItem{{
				Type: "function_call", CallID: "call-1", Name: "read", Arguments: "{}",
			}},
			GeminiParts: []message.GeminiReplayPart{{Type: "function_call", ToolCallID: "call-1", ThoughtSignature: "sig"}},
			StopReason:  "stop",
		},
	}
	got := normalizeRestoredMessages(msgs, nil, "main")
	if len(got) != 2 || len(got[1].ResponsesOutput) != 1 || len(got[1].GeminiParts) != 1 {
		t.Fatalf("provider output state was dropped: %#v", got)
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
	got := normalizeRestoredMessages(msgs, nil, "main")
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
	got := normalizeRestoredMessages(msgs, nil, "main")
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
	if !strings.Contains(synth.Content, "session restored") {
		t.Fatalf("synthesized content = %q", synth.Content)
	}
	if synth.ToolRecoveryState != message.ToolRecoveryStateOutcomeUnknown {
		t.Fatalf("synthesized recovery state = %q, want outcome_unknown (no journal info)", synth.ToolRecoveryState)
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
	got := normalizeRestoredMessages(msgs, nil, "main")
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
	got := normalizeRestoredMessages(msgs, nil, "main")
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
	got := normalizeRestoredMessages(msgs, nil, "main")
	if got[1].Content != "permission denied by upstream service" {
		t.Fatalf("tool content rewritten by heuristics: %q", got[1].Content)
	}
	if got[1].ToolStatus != string(ToolResultStatusSuccess) {
		t.Fatalf("tool status rewritten: %q", got[1].ToolStatus)
	}
}

func TestRepairOrphanToolCalls_StartedMarksOutcomeUnknown(t *testing.T) {
	msgs := []message.Message{
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "call_1", Name: "shell"}}},
	}
	started := map[recovery.ToolActivityKey]struct{}{
		{AgentID: "main", CallID: "call_1"}: {},
	}
	got := normalizeRestoredMessages(msgs, started, "main")
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d: %#v", len(got), got)
	}
	synth := got[1]
	if synth.ToolRecoveryState != message.ToolRecoveryStateOutcomeUnknown {
		t.Fatalf("recovery state = %q, want outcome_unknown", synth.ToolRecoveryState)
	}
	if !strings.Contains(synth.Content, "verify the current state before retrying") {
		t.Fatalf("outcome_unknown content = %q", synth.Content)
	}
}

func TestRepairOrphanToolCalls_EmptyJournalMarksNotStarted(t *testing.T) {
	msgs := []message.Message{
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "call_1", Name: "shell"}}},
	}
	started := map[recovery.ToolActivityKey]struct{}{}
	got := normalizeRestoredMessages(msgs, started, "main")
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d: %#v", len(got), got)
	}
	synth := got[1]
	if synth.ToolRecoveryState != message.ToolRecoveryStateNotStarted {
		t.Fatalf("recovery state = %q, want not_started", synth.ToolRecoveryState)
	}
	if !strings.Contains(synth.Content, "no result was produced") {
		t.Fatalf("not_started content = %q", synth.Content)
	}
}

func TestRepairOrphanToolCalls_ScopesByAgentID(t *testing.T) {
	msgs := []message.Message{
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "call_1", Name: "shell"}}},
	}
	started := map[recovery.ToolActivityKey]struct{}{
		{AgentID: "other-agent", CallID: "call_1"}: {},
	}
	got := normalizeRestoredMessages(msgs, started, "main")
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d: %#v", len(got), got)
	}
	if got[1].ToolRecoveryState != message.ToolRecoveryStateNotStarted {
		t.Fatalf("recovery state = %q, want not_started (different agent scope)", got[1].ToolRecoveryState)
	}
}
