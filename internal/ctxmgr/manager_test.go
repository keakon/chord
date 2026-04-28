package ctxmgr

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestRepairOrphanToolMessagesInPlace(t *testing.T) {
	m := NewManager(1000, false, 0.8)
	m.Append(message.Message{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "ok", Name: "Read", Args: json.RawMessage(`{}`)}}})
	m.Append(message.Message{Role: "tool", ToolCallID: "ok", Content: "kept"})
	m.Append(message.Message{Role: "tool", ToolCallID: "missing", Content: "dropped"})

	if got := m.RepairOrphanToolMessagesInPlace(); got != 1 {
		t.Fatalf("RepairOrphanToolMessagesInPlace = %d, want 1", got)
	}
	snap := m.Snapshot()
	if len(snap) != 2 || snap[1].ToolCallID != "ok" {
		t.Fatalf("snapshot after repair = %+v", snap)
	}
	if got := m.RepairOrphanToolMessagesInPlace(); got != 0 {
		t.Fatalf("second repair = %d, want 0", got)
	}
}

func TestAnyAssistantDeclaresToolCallID(t *testing.T) {
	m := NewManager(1000, false, 0.8)
	m.Append(message.Message{Role: "user", Content: "hello"})
	m.Append(message.Message{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "call-1", Name: "Read", Args: json.RawMessage(`{}`)}}})
	if !m.AnyAssistantDeclaresToolCallID("call-1") {
		t.Fatal("expected call-1 to be declared")
	}
	if m.AnyAssistantDeclaresToolCallID("missing") {
		t.Fatal("did not expect missing call to be declared")
	}
	if m.AnyAssistantDeclaresToolCallID("") {
		t.Fatal("empty call id should not be declared")
	}
}

func TestSafeKeepBoundaryAndManagerWrapper(t *testing.T) {
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "a", Name: "Read", Args: json.RawMessage(`{}`)}}},
		{Role: "tool", ToolCallID: "a", Content: "result"},
		{Role: "user", Content: "u2"},
	}
	if got := SafeKeepBoundary(msgs, 2); got != 1 {
		t.Fatalf("SafeKeepBoundary at tool = %d, want 1", got)
	}
	if got := SafeKeepBoundary(msgs, 3); got != 3 {
		t.Fatalf("SafeKeepBoundary at user = %d, want 3", got)
	}
	if got := SafeKeepBoundary(msgs, -1); got != 0 {
		t.Fatalf("SafeKeepBoundary negative = %d, want 0", got)
	}
	if got := SafeKeepBoundary(msgs, len(msgs)+1); got != len(msgs) {
		t.Fatalf("SafeKeepBoundary beyond end = %d, want %d", got, len(msgs))
	}

	m := NewManager(1000, false, 0.8)
	m.RestoreMessages(msgs)
	if got := m.ComputeSafeKeepBoundary(2); got != 1 {
		t.Fatalf("ComputeSafeKeepBoundary = %d, want 1", got)
	}
}

func TestCompressForTarget(t *testing.T) {
	m := NewManager(1000, false, 0.8)
	msgs := []message.Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "older message that should be dropped"},
		{Role: "assistant", Content: "recent assistant"},
		{Role: "user", Content: "recent user"},
	}
	got := m.CompressForTarget(msgs, 15)
	if len(got) != 4 {
		t.Fatalf("compressed len = %d, want 4: %+v", len(got), got)
	}
	if got[0].Role != "system" || got[1].Role != "user" || !strings.Contains(got[1].Content, "Context was compressed") || got[3].Content != "recent user" {
		t.Fatalf("unexpected compressed messages: %+v", got)
	}
	if got := m.CompressForTarget(msgs[:2], 15); got != nil {
		t.Fatalf("CompressForTarget short history = %+v, want nil", got)
	}
	if got := m.CompressForTarget(msgs, 0); got != nil {
		t.Fatalf("CompressForTarget zero target = %+v, want nil", got)
	}
}

func TestShouldAutoCompact(t *testing.T) {
	m := NewManager(1000, true, 0.8)
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 799})
	if m.ShouldAutoCompact() {
		t.Fatal("expected threshold check to stay false below 80%")
	}

	m.UpdateFromUsage(message.TokenUsage{InputTokens: 800})
	if !m.ShouldAutoCompact() {
		t.Fatal("expected threshold check to become true at 80%")
	}
}

func TestUpdateFromUsageTracksTrueContextBurden(t *testing.T) {
	m := NewManager(1000, false, 0.8)
	m.UpdateFromUsage(message.TokenUsage{
		InputTokens:      100,
		OutputTokens:     40,
		CacheReadTokens:  60,
		CacheWriteTokens: 20,
		ReasoningTokens:  10,
	})
	if got := m.LastInputTokens(); got != 100 {
		t.Fatalf("LastInputTokens() = %d, want 100", got)
	}
	if got := m.LastTotalContextTokens(); got != 120 {
		t.Fatalf("LastTotalContextTokens() = %d, want 120 (input + cache_write only)", got)
	}
}

func TestEstimateMessagesTokensCountsToolCallsAndThinking(t *testing.T) {
	msgs := []message.Message{
		{
			Role:    "assistant",
			Content: "abcd",
			ToolCalls: []message.ToolCall{
				{ID: "1", Name: "Read", Args: json.RawMessage(`{"path":"README.md"}`)},
			},
			ThinkingBlocks: []message.ThinkingBlock{
				{Thinking: "reasoning"},
			},
		},
	}

	got := EstimateMessagesTokens(msgs)
	if got <= 1 {
		t.Fatalf("expected token estimate to include tool args and thinking, got %d", got)
	}
}

func TestRestoreMessagesDropsOrphanToolResults(t *testing.T) {
	m := NewManager(1000, false, 0.8)
	in := []message.Message{
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "a", Name: "Read", Args: json.RawMessage(`{}`)}}},
		{Role: "tool", ToolCallID: "a", Content: "ok"},
		{Role: "tool", ToolCallID: "ghost", Content: "orphan"},
	}
	m.RestoreMessages(in)
	snap := m.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("len(snap)=%d want 2", len(snap))
	}
	if snap[1].ToolCallID != "a" {
		t.Fatalf("last message tool id = %q", snap[1].ToolCallID)
	}
}

func TestReplacePrefixAtomic(t *testing.T) {
	t.Run("basic replacement", func(t *testing.T) {
		m := NewManager(1000, false, 0.8)
		m.Append(message.Message{Role: "user", Content: "u1"})
		m.Append(message.Message{Role: "assistant", Content: "a1"})
		m.Append(message.Message{Role: "user", Content: "u2"})
		m.Append(message.Message{Role: "assistant", Content: "a2"})

		prefix := []message.Message{
			{Role: "user", Content: "summary"},
		}
		err := m.ReplacePrefixAtomic(2, prefix, func(tail []message.Message) ([]message.Message, error) {
			if len(tail) != 2 {
				t.Fatalf("expected tail len 2, got %d", len(tail))
			}
			result := make([]message.Message, 0, len(prefix)+len(tail))
			result = append(result, prefix...)
			result = append(result, tail...)
			return result, nil
		})
		if err != nil {
			t.Fatalf("ReplacePrefixAtomic failed: %v", err)
		}

		snap := m.Snapshot()
		if len(snap) != 3 {
			t.Fatalf("expected 3 messages, got %d", len(snap))
		}
		if snap[0].Content != "summary" {
			t.Fatalf("first message = %q, want summary", snap[0].Content)
		}
		if snap[1].Content != "u2" {
			t.Fatalf("second message = %q, want u2", snap[1].Content)
		}
	})

	t.Run("empty tail", func(t *testing.T) {
		m := NewManager(1000, false, 0.8)
		m.Append(message.Message{Role: "user", Content: "u1"})
		m.Append(message.Message{Role: "assistant", Content: "a1"})

		prefix := []message.Message{
			{Role: "user", Content: "summary"},
		}
		err := m.ReplacePrefixAtomic(2, prefix, func(tail []message.Message) ([]message.Message, error) {
			if len(tail) != 0 {
				t.Fatalf("expected empty tail, got %d", len(tail))
			}
			return prefix, nil
		})
		if err != nil {
			t.Fatalf("ReplacePrefixAtomic failed: %v", err)
		}

		snap := m.Snapshot()
		if len(snap) != 1 {
			t.Fatalf("expected 1 message, got %d", len(snap))
		}
	})

	t.Run("repairs orphan tool result in tail", func(t *testing.T) {
		m := NewManager(1000, false, 0.8)
		// head: assistant with tool_call "a"
		m.Append(message.Message{
			Role:      "assistant",
			Content:   "a1",
			ToolCalls: []message.ToolCall{{ID: "a", Name: "Read", Args: json.RawMessage(`{}`)}},
		})
		m.Append(message.Message{Role: "tool", ToolCallID: "a", Content: "result-a"})
		// tail: tool result "b" without matching tool_call (orphan)
		m.Append(message.Message{Role: "tool", ToolCallID: "b", Content: "orphan-result"})
		m.Append(message.Message{Role: "user", Content: "u2"})

		prefix := []message.Message{
			{Role: "user", Content: "summary"},
		}
		err := m.ReplacePrefixAtomic(2, prefix, func(tail []message.Message) ([]message.Message, error) {
			// orphan tool result should be removed
			if len(tail) != 1 {
				t.Fatalf("expected tail len 1 (orphan removed), got %d", len(tail))
			}
			if tail[0].Content != "u2" {
				t.Fatalf("tail[0].Content = %q, want u2", tail[0].Content)
			}
			result := make([]message.Message, 0, len(prefix)+len(tail))
			result = append(result, prefix...)
			result = append(result, tail...)
			return result, nil
		})
		if err != nil {
			t.Fatalf("ReplacePrefixAtomic failed: %v", err)
		}

		snap := m.Snapshot()
		if len(snap) != 2 {
			t.Fatalf("expected 2 messages, got %d", len(snap))
		}
	})

	t.Run("keeps valid tool result in tail", func(t *testing.T) {
		m := NewManager(1000, false, 0.8)
		// head: assistant with tool_call "a"
		m.Append(message.Message{
			Role:      "assistant",
			Content:   "a1",
			ToolCalls: []message.ToolCall{{ID: "a", Name: "Read", Args: json.RawMessage(`{}`)}},
		})
		m.Append(message.Message{Role: "tool", ToolCallID: "a", Content: "result-a"})
		// tail: assistant with tool_call "b" and its result
		m.Append(message.Message{
			Role:      "assistant",
			Content:   "a2",
			ToolCalls: []message.ToolCall{{ID: "b", Name: "Read", Args: json.RawMessage(`{}`)}},
		})
		m.Append(message.Message{Role: "tool", ToolCallID: "b", Content: "result-b"})

		prefix := []message.Message{
			{Role: "user", Content: "summary"},
		}
		err := m.ReplacePrefixAtomic(2, prefix, func(tail []message.Message) ([]message.Message, error) {
			// both messages in tail should be kept (tool result "b" has matching tool_call in tail)
			if len(tail) != 2 {
				t.Fatalf("expected tail len 2, got %d", len(tail))
			}
			result := make([]message.Message, 0, len(prefix)+len(tail))
			result = append(result, prefix...)
			result = append(result, tail...)
			return result, nil
		})
		if err != nil {
			t.Fatalf("ReplacePrefixAtomic failed: %v", err)
		}

		snap := m.Snapshot()
		if len(snap) != 3 {
			t.Fatalf("expected 3 messages, got %d", len(snap))
		}
	})

	t.Run("callback error aborts", func(t *testing.T) {
		m := NewManager(1000, false, 0.8)
		m.Append(message.Message{Role: "user", Content: "u1"})
		m.Append(message.Message{Role: "assistant", Content: "a1"})
		m.Append(message.Message{Role: "user", Content: "u2"})

		prefix := []message.Message{
			{Role: "user", Content: "summary"},
		}
		err := m.ReplacePrefixAtomic(2, prefix, func(tail []message.Message) ([]message.Message, error) {
			return nil, fmt.Errorf("callback error")
		})
		if err == nil {
			t.Fatal("expected error, got nil")
		}

		// Original messages should be unchanged
		snap := m.Snapshot()
		if len(snap) != 3 {
			t.Fatalf("expected 3 original messages, got %d", len(snap))
		}
		if snap[0].Content != "u1" {
			t.Fatalf("first message = %q, want u1", snap[0].Content)
		}
	})

	t.Run("nil callback applies prefix and tail directly", func(t *testing.T) {
		m := NewManager(1000, false, 0.8)
		m.Append(message.Message{Role: "user", Content: "u1"})
		m.Append(message.Message{Role: "assistant", Content: "a1"})
		m.Append(message.Message{Role: "user", Content: "u2"})

		prefix := []message.Message{
			{Role: "user", Content: "summary"},
		}
		err := m.ReplacePrefixAtomic(2, prefix, nil)
		if err != nil {
			t.Fatalf("ReplacePrefixAtomic failed: %v", err)
		}

		snap := m.Snapshot()
		if len(snap) != 2 {
			t.Fatalf("expected 2 messages, got %d", len(snap))
		}
		if snap[0].Content != "summary" {
			t.Fatalf("first message = %q, want summary", snap[0].Content)
		}
		if snap[1].Content != "u2" {
			t.Fatalf("second message = %q, want u2", snap[1].Content)
		}
	})
}
