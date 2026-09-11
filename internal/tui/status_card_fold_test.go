package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
)

// A runtime notice whose body hides more than one line starts collapsed to the
// badge line alone with the ▸ marker, and the toggle expands it to the full
// indented body. The marker sits on the badge — the line that survives the
// toggle — matching the tool cards and the JOB RESULT headlines.
func TestRuntimeStatusNoticeCollapsesToBadgeLine(t *testing.T) {
	m := NewModelWithSize(nil, 120, 30)
	_ = m.handleAgentEvent(agentEventMsg{event: agent.LoopNoticeEvent{
		Title: "LOOP CONTINUE",
		Text:  "Unresolved work:\n- pending verification\n- remaining subagent",
	}})

	block := m.viewport.blocks[len(m.viewport.blocks)-1]
	if block.Type != BlockStatus || !block.Collapsed {
		t.Fatalf("block = %+v, want a collapsed status card", block)
	}
	collapsedLines := stripANSILines(block.Render(120, ""))
	collapsed := strings.Join(collapsedLines, "\n")
	if !strings.Contains(collapsed, "LOOP CONTINUE") || !strings.Contains(collapsed, "▸") {
		t.Fatalf("collapsed card = %q, want the badge with a ▸ marker", collapsed)
	}
	for _, line := range collapsedLines {
		if strings.Contains(line, "LOOP CONTINUE") && !strings.Contains(line, "▸") {
			t.Fatalf("collapsed badge must carry the ▸ marker:\n%s", collapsed)
		}
	}
	for _, hidden := range []string{"Unresolved work:", "pending verification", "remaining subagent"} {
		if strings.Contains(collapsed, hidden) {
			t.Fatalf("collapsed card leaked %q:\n%s", hidden, collapsed)
		}
	}

	if !block.ToggleAtWidth(120) || block.Collapsed {
		t.Fatal("toggling must expand the notice")
	}
	expanded := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	for _, want := range []string{"LOOP CONTINUE", "▾", "  Unresolved work:", "  • pending verification", "  • remaining subagent"} {
		if !strings.Contains(expanded, want) {
			t.Fatalf("expanded card missing %q:\n%s", want, expanded)
		}
	}
	if strings.Contains(expanded, "more lines hidden.") {
		t.Fatalf("expanded card must show the whole body:\n%s", expanded)
	}
}

// The generic NOTICE carries command replies and runtime diagnostics, so it is
// created expanded: a bare NOTICE badge would hide the output the reader asked
// for. Space still folds it to the badge line.
func TestMultiLineNoticeStartsExpandedAndFolds(t *testing.T) {
	m := NewModelWithSize(nil, 120, 30)
	_ = m.handleAgentEvent(agentEventMsg{event: agent.InfoEvent{
		Message: "Current role: builder\n\nAvailable roles:\n- builder (current)\n- planner",
	}})

	block := m.viewport.blocks[len(m.viewport.blocks)-1]
	if block.Type != BlockStatus || block.Collapsed {
		t.Fatalf("block = %+v, want an expanded NOTICE card", block)
	}
	expanded := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	for _, want := range []string{"NOTICE", "▾", "Current role: builder", "• planner"} {
		if !strings.Contains(expanded, want) {
			t.Fatalf("expanded notice missing %q:\n%s", want, expanded)
		}
	}
	if !block.ToggleAtWidth(120) || !block.Collapsed {
		t.Fatal("toggling must collapse the notice")
	}
	collapsed := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(collapsed, "▸") || strings.Contains(collapsed, "Current role: builder") {
		t.Fatalf("collapsed notice = %q, want the badge line alone", collapsed)
	}
}

// A body that already renders to one line is its own summary, so the card keeps
// the badge/body shape, grows no disclosure marker, and space is a no-op.
func TestShortRuntimeStatusNoticeShowsNoDisclosureMarker(t *testing.T) {
	m := NewModelWithSize(nil, 120, 30)
	_ = m.handleAgentEvent(agentEventMsg{event: agent.InfoEvent{Message: "Session persistence recovered"}})

	block := m.viewport.blocks[len(m.viewport.blocks)-1]
	rendered := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(rendered, "Session persistence recovered") {
		t.Fatalf("short notice body hidden:\n%s", rendered)
	}
	if strings.Contains(rendered, "▸") || strings.Contains(rendered, "▾") || strings.Contains(rendered, "more lines hidden.") {
		t.Fatalf("short notice grew a disclosure marker:\n%s", rendered)
	}
	if block.ToggleAtWidth(120) || block.Collapsed {
		t.Fatal("space must be a no-op when the body has nothing to hide")
	}
}

// The fold gate follows the render width: the same notice becomes foldable once
// its body wraps past the first line.
func TestStatusCardFoldGateFollowsRenderWidth(t *testing.T) {
	notice := &Block{Type: BlockStatus, StatusTitle: "NOTICE", Content: "Session persistence recovered"}
	if notice.ToggleAtWidth(120) {
		t.Fatal("a one-line body must not fold at a wide width")
	}
	if !notice.ToggleAtWidth(20) || !notice.Collapsed {
		t.Fatal("the same body must fold once it wraps")
	}
}

// A sub-agent mailbox card carries the worker model's own message, so it stays
// fully visible even when the text is long.
func TestSubAgentMailboxCardStaysExpanded(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := newSubAgentMailboxBlock(1, "progress", "", "worker-1", "task-1", "line one\nline two\nline three\nline four", "")

	rendered := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(rendered, "line four") {
		t.Fatalf("mailbox card must keep the worker model's full message:\n%s", rendered)
	}
	if strings.Contains(rendered, "more lines hidden.") || strings.Contains(rendered, "▸") {
		t.Fatalf("mailbox card must not fold:\n%s", rendered)
	}
}

// Errors are short and must be readable without expanding a card, so the error
// card keeps its full body and space is a no-op on it.
func TestErrorCardStaysExpanded(t *testing.T) {
	m := NewModelWithSize(nil, 60, 24)
	cmd := m.handleAgentEvent(agentEventMsg{event: agent.ErrorEvent{
		Err: errors.New("failed: first\nsecond\nthird\nfourth\nfifth"),
	}})
	applyTestCmd(t, &m, cmd)

	block := m.viewport.blocks[len(m.viewport.blocks)-1]
	if block.Type != BlockError || block.Collapsed {
		t.Fatalf("block = %+v, want an expanded error card", block)
	}
	rendered := stripANSI(strings.Join(block.Render(60, ""), "\n"))
	if !strings.Contains(rendered, "fifth") {
		t.Fatalf("error card must keep its full body:\n%s", rendered)
	}
	if strings.Contains(rendered, "more lines hidden.") || strings.Contains(rendered, "▸") {
		t.Fatalf("error card must not fold:\n%s", rendered)
	}
	if block.ToggleAtWidth(60) {
		t.Fatal("space must be a no-op on error cards")
	}
}

// Local status cards (export / diagnostics) fold like other notices but are
// still created expanded: the default-collapse list covers the notice families
// the user asked to fold, not these on-demand reports.
func TestLocalStatusCardFoldsButStartsExpanded(t *testing.T) {
	m := NewModelWithSize(nil, 120, 30)
	m.appendLocalStatusCard("DIAGNOSTICS", "Diagnostics bundle exported to /tmp/diag\n\nBefore sharing it, please inspect the bundle and remove any sensitive content if needed.")

	block := m.viewport.blocks[len(m.viewport.blocks)-1]
	if block.Type != BlockStatus || block.Collapsed {
		t.Fatalf("block = %+v, want an expanded local status card", block)
	}
	expanded := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	for _, want := range []string{"Diagnostics bundle exported to /tmp/diag", "▾", "remove any sensitive content"} {
		if !strings.Contains(expanded, want) {
			t.Fatalf("local status card missing %q:\n%s", want, expanded)
		}
	}
	if !block.ToggleAtWidth(120) || !block.Collapsed {
		t.Fatal("toggling must collapse the local status card")
	}
	collapsed := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(collapsed, "▸") {
		t.Fatalf("collapsed local status card = %q, want a ▸ marker", collapsed)
	}
	for _, hidden := range []string{"Diagnostics bundle exported to /tmp/diag", "remove any sensitive content"} {
		if strings.Contains(collapsed, hidden) {
			t.Fatalf("collapsed local status card leaked %q:\n%s", hidden, collapsed)
		}
	}
}

// Restoring a session rebuilds the durable notice messages as collapsed cards,
// matching the state the live event produced.
func TestMessagesToBlocksRestoresCollapsedNoticeCards(t *testing.T) {
	msgs := []message.Message{
		{Role: "user", Content: "Your previous reply was interrupted before it completed.", Kind: message.KindStreamContinue},
		{Role: "user", Content: "The context is approaching the configured automatic-compaction threshold.", Kind: message.KindContextNotice, NoticeLevel: "pressure"},
	}
	nextID := 1
	blocks := messagesToBlocks(msgs, &nextID)
	if len(blocks) != 2 {
		t.Fatalf("block count = %d, want 2", len(blocks))
	}
	for i, block := range blocks {
		if block.Type != BlockStatus || !block.Collapsed {
			t.Fatalf("block %d = %+v, want a collapsed status card", i, block)
		}
	}
}
