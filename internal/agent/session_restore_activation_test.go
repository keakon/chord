package agent

import (
	"testing"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestActivateLoadedSessionUsesLoadedStateWithoutRecomputingMerge(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	loaded := &loadedSessionState{
		SessionPath:            "/tmp/session-123",
		Messages:               []message.Message{{Role: "user", Content: "hi"}},
		TodoItems:              []tools.TodoItem{{ID: "todo-1", Status: "pending", Content: "from loaded"}},
		UsageStats:             analytics.SessionStats{InputTokens: 7, OutputTokens: 3, LLMCalls: 2},
		ContextUsage:           message.TokenUsage{InputTokens: 7, OutputTokens: 3},
		LastInputTokens:        11,
		LastTotalContextTokens: 29,
		ActiveRole:             "reviewer",
	}

	result := a.activateLoadedSession(loaded)
	if result.SessionPath != loaded.SessionPath || result.MessageCount != 1 || result.TodoCount != 1 {
		t.Fatalf("activateLoadedSession result = %+v, want loaded counts/path", result)
	}
	if got := a.GetTodos(); len(got) != 1 || got[0].Content != "from loaded" {
		t.Fatalf("GetTodos() = %+v, want loaded todos copied verbatim", got)
	}
	stats := a.GetUsageStats()
	if stats.LLMCalls != 2 || stats.InputTokens != 7 || stats.OutputTokens != 3 {
		t.Fatalf("GetUsageStats() = %+v, want loaded usage stats", stats)
	}
	current, _ := a.GetContextStats()
	if current != 29 {
		t.Fatalf("GetContextStats current = %d, want loaded total context tokens29", current)
	}
}
