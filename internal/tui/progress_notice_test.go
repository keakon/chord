package tui

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

func TestProgressNoticeLiveRestoreAndDeduplication(t *testing.T) {
	const text = "Briefly report what is known or still unresolved and what comes next before continuing with more tools."
	m := NewModelWithSize(nil, 80, 30)
	evt := agent.ProgressNoticeEvent{Message: text, MessageIndex: 0}
	_ = m.handleAgentEvent(agentEventMsg{event: evt})
	_ = m.handleAgentEvent(agentEventMsg{event: evt})
	if len(m.viewport.blocks) != 1 {
		t.Fatalf("blocks = %d, want one reminder", len(m.viewport.blocks))
	}
	live := m.viewport.blocks[0]
	nextID := 1
	restored := messagesToBlocks([]message.Message{{
		Role: message.RoleUser, Kind: message.KindProgressNotice, Content: message.SystemReminderOverlayText(text),
	}}, &nextID)
	if len(restored) != 1 || restored[0].Type != BlockStatus || restored[0].Content != live.Content ||
		restored[0].StatusTitle != live.StatusTitle || restored[0].MsgIndex != live.MsgIndex || !restored[0].Collapsed {
		t.Fatalf("restore = %+v, live = %+v", restored, live)
	}
	collapsed := terminaltext.StripANSI(strings.Join(live.Render(80, ""), "\n"))
	if !strings.Contains(collapsed, progressNoticeCardTitle) || strings.Contains(collapsed, "Briefly report") {
		t.Fatalf("collapsed card = %q", collapsed)
	}
	if !live.ToggleAtWidth(80) {
		t.Fatal("reminder must expand")
	}
	expanded := terminaltext.StripANSI(strings.Join(live.Render(80, ""), "\n"))
	if !strings.Contains(expanded, "Briefly report") || strings.Contains(expanded, "<system-reminder>") {
		t.Fatalf("expanded card = %q", expanded)
	}
	evt.MessageIndex = 1
	_ = m.handleAgentEvent(agentEventMsg{event: evt})
	if len(m.viewport.blocks) != 2 {
		t.Fatal("a later text-free chain must retain its own reminder card")
	}

	// Restore-then-live: persistence recovery can replay the live event for a
	// durable row that is already in the viewport. The transcript index owns
	// the card, so the replay must not add a second one.
	restoredModel := NewModelWithSize(nil, 80, 30)
	restoredModel.appendProgressNotice(0, text)
	_ = restoredModel.handleAgentEvent(agentEventMsg{event: agent.ProgressNoticeEvent{Message: text, MessageIndex: 0}})
	if len(restoredModel.viewport.blocks) != 1 {
		t.Fatalf("restore + live replay produced %d cards", len(restoredModel.viewport.blocks))
	}
}
