package tui

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
)

func TestContextNoticeEventCreatesDurableBackedCard(t *testing.T) {
	m := NewModelWithSize(nil, 120, 30)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, Content: "hello", MsgIndex: 0})

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ContextNoticeEvent{
		Level:        "warning",
		Message:      "Context will be compacted at the next safe boundary.",
		MessageIndex: 3,
	}})

	var card *Block
	for _, block := range m.viewport.blocks {
		if block != nil && block.NoticeLevel != "" {
			card = block
		}
	}
	if card == nil {
		t.Fatal("expected a context notice card")
	}
	if card.Type != BlockStatus || card.StatusTitle != "COMPACT WARNING" || card.MsgIndex != 3 {
		t.Fatalf("card = %+v, want COMPACT WARNING status anchored to message 3", card)
	}
	if card.NoticeLevel != "warning" || !strings.Contains(card.Content, "next safe boundary") {
		t.Fatalf("card = %+v, want the warning level and notice text", card)
	}
}

func TestContextNoticeClearedEventRemovesOnlyNoticeCards(t *testing.T) {
	m := NewModelWithSize(nil, 120, 30)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, Content: "hello", MsgIndex: 0})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ContextNoticeEvent{
		Level:        "pressure",
		Message:      "Context is nearing the threshold.",
		MessageIndex: 1,
	}})

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ContextNoticeClearedEvent{}})

	userBlocks := 0
	for _, block := range m.viewport.blocks {
		if block == nil {
			continue
		}
		if block.NoticeLevel != "" {
			t.Fatalf("context notice card survived the cleared event: %+v", block)
		}
		if block.Type == BlockUser {
			userBlocks++
		}
	}
	if userBlocks != 1 {
		t.Fatalf("user blocks = %d, want the unrelated user card kept", userBlocks)
	}
}

func TestMessagesToBlocksRestoresContextNoticeCard(t *testing.T) {
	nextID := 0
	blocks := messagesToBlocks([]message.Message{
		{Role: message.RoleUser, Content: "hello"},
		{Role: message.RoleUser, Kind: message.KindContextNotice, Content: "Compaction is imminent.", NoticeLevel: "imminent"},
	}, &nextID)

	var card *Block
	for _, block := range blocks {
		if block != nil && block.NoticeLevel != "" {
			card = block
		}
	}
	if card == nil {
		t.Fatal("expected the restored transcript to rebuild the context notice card")
	}
	if card.Type != BlockStatus || card.StatusTitle != "COMPACT IMMINENT" || card.NoticeLevel != "imminent" {
		t.Fatalf("restored card = %+v, want COMPACT IMMINENT status", card)
	}
	if card.MsgIndex != 1 {
		t.Fatalf("restored card MsgIndex = %d, want 1", card.MsgIndex)
	}
	if card.Content != "Compaction is imminent." {
		t.Fatalf("restored card content = %q", card.Content)
	}
}
