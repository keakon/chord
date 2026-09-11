package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
)

// Runtime status cards carry harness text, so they start folded to their
// opening lines and expand on the shared space/enter toggle.
func TestRuntimeStatusNoticeFoldsToSummaryByDefault(t *testing.T) {
	m := NewModelWithSize(nil, 120, 30)
	_ = m.handleAgentEvent(agentEventMsg{event: agent.LoopNoticeEvent{
		Title: "LOOP CONTINUE",
		Text:  "Unresolved work:\n- pending verification\n- remaining subagent",
	}})

	block := m.viewport.blocks[len(m.viewport.blocks)-1]
	if block.Type != BlockStatus || !block.Collapsed {
		t.Fatalf("block = %+v, want a folded status card", block)
	}
	collapsed := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(collapsed, "LOOP CONTINUE") || !strings.Contains(collapsed, "▸") {
		t.Fatalf("folded card missing badge/marker:\n%s", collapsed)
	}
	if !strings.Contains(collapsed, "pending verification") {
		t.Fatalf("folded card missing the summary lines:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "remaining subagent") {
		t.Fatalf("folded card leaked a hidden line:\n%s", collapsed)
	}
	if !strings.Contains(collapsed, "more lines hidden.") {
		t.Fatalf("folded card missing the hidden-line hint:\n%s", collapsed)
	}

	if !block.ToggleAtWidth(120) || block.Collapsed {
		t.Fatal("expected the status card to expand")
	}
	expanded := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	for _, want := range []string{"▾", "pending verification", "remaining subagent"} {
		if !strings.Contains(expanded, want) {
			t.Fatalf("expanded card missing %q:\n%s", want, expanded)
		}
	}
	if strings.Contains(expanded, "more lines hidden.") {
		t.Fatalf("expanded card kept the hidden-line hint:\n%s", expanded)
	}
}

// A short notice already is its own summary, so it must not grow a disclosure
// marker or hide anything.
func TestShortRuntimeStatusNoticeShowsNoDisclosureMarker(t *testing.T) {
	m := NewModelWithSize(nil, 120, 30)
	_ = m.handleAgentEvent(agentEventMsg{event: agent.InfoEvent{Message: "Session persistence recovered"}})

	block := m.viewport.blocks[len(m.viewport.blocks)-1]
	if !block.Collapsed {
		t.Fatal("runtime info notices must be created folded")
	}
	rendered := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(rendered, "Session persistence recovered") {
		t.Fatalf("short notice body hidden:\n%s", rendered)
	}
	if strings.Contains(rendered, "▸") || strings.Contains(rendered, "▾") || strings.Contains(rendered, "more lines hidden.") {
		t.Fatalf("short notice grew a disclosure marker:\n%s", rendered)
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
	if strings.Contains(rendered, "more lines hidden.") {
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

// Local status cards (export/diagnostics) are short and tell the user what just
// happened, so they keep their full body by default.
func TestLocalStatusCardStaysExpanded(t *testing.T) {
	m := NewModelWithSize(nil, 120, 30)
	m.appendLocalStatusCard("DIAGNOSTICS", "Diagnostics bundle exported to /tmp/diag\n\nBefore sharing it, please inspect the bundle and remove any sensitive content if needed.")

	block := m.viewport.blocks[len(m.viewport.blocks)-1]
	if block.Type != BlockStatus || block.Collapsed {
		t.Fatalf("block = %+v, want an expanded local status card", block)
	}
	rendered := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	for _, want := range []string{"Diagnostics bundle exported to /tmp/diag", "remove any sensitive content"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("local status card missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "more lines hidden.") || strings.Contains(rendered, "▸") {
		t.Fatalf("local status card must not fold by default:\n%s", rendered)
	}
}
