package tui

import (
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
)

// The live card and the card a restore rebuilds from the durable follow-up
// message must be identical, so the user sees the same card in both.
func TestLSPFollowupCardMatchesLiveAndRestored(t *testing.T) {
	const bare = "LSP diagnostics arrived late.\n\nmain.go:\n[W] 3:1 this value is never used"

	m := NewModelWithSize(nil, 120, 30)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, Content: "hello", MsgIndex: 0})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.LSPFollowupEvent{Message: bare, MessageIndex: 4}})
	var live *Block
	for _, block := range m.viewport.blocks {
		if block != nil && block.StatusTitle == lspFollowupCardTitle {
			live = block
		}
	}
	if live == nil {
		t.Fatal("expected a live LSP diagnostics card")
	}
	if live.Type != BlockStatus || live.MsgIndex != 4 || live.Content != bare || !live.Collapsed {
		t.Fatalf("live card = %+v, want a collapsed status card anchored to message 4", live)
	}

	nextID := 0
	blocks := messagesToBlocks([]message.Message{
		{Role: message.RoleUser, Content: "hello"},
		{Role: message.RoleUser, Kind: message.KindLSPFollowup, Content: "<system-reminder>\n" + bare + "\n</system-reminder>"},
	}, &nextID)
	if len(blocks) != 2 {
		t.Fatalf("len(blocks) = %d, want the user card and the follow-up card", len(blocks))
	}
	restored := blocks[1]
	if restored.Type != BlockStatus || restored.StatusTitle != lspFollowupCardTitle || restored.MsgIndex != 1 {
		t.Fatalf("restored card = %+v, want an LSP diagnostics status card at message 1", restored)
	}
	if restored.Content != live.Content || restored.Collapsed != live.Collapsed {
		t.Fatalf("restored card content = %q collapsed=%v, want the live card's %q collapsed=%v", restored.Content, restored.Collapsed, live.Content, live.Collapsed)
	}
}
