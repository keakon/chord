package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// TestSendDraftRecalcsViewportBeforeAppend regresses the bug where sendDraft
// appended the new USER block using a stale viewport.height. Any upstream path
// that shrinks the viewport (multi-line composer, an extra queued-draft row, a
// transient toast) and then reaches sendDraft without a fresh recalc would
// cause scrollToEnd/Render to clip the card's trailing rows (padBottom,
// marginBottom, and sometimes body lines). This test simulates the stale
// viewport height directly and asserts sendDraft restores it.
func TestSendDraftRecalcsViewportBeforeAppend(t *testing.T) {
	const termHeight = 24
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 80, termHeight)
	m.mode = ModeInsert

	initialHeight := m.viewport.height
	if initialHeight <= 5 {
		t.Fatalf("precondition failed: initial viewport.height = %d, want >5", initialHeight)
	}

	// Simulate the stale-height window: something upstream (multi-line composer,
	// pending queued draft, transient toast) previously shrank the viewport and
	// never restored it.
	shrunk := initialHeight - 5
	m.viewport.SetSize(m.viewport.width, shrunk)

	_ = m.sendDraft(queuedDraft{Content: "hello from test", QueuedAt: time.Now()})

	if got := m.viewport.height; got != initialHeight {
		t.Fatalf("viewport.height after sendDraft = %d, want %d (sendDraft must recalc before AppendBlock)", got, initialHeight)
	}

	totalSent := len(backend.sentMessages) + len(backend.sentMultipart)
	if totalSent != 1 {
		t.Fatalf("user message dispatch count = %d (text=%d parts=%d), want 1", totalSent, len(backend.sentMessages), len(backend.sentMultipart))
	}

	// The USER block must still fit within the restored viewport so Render does
	// not clip trailing card rows.
	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 1 {
		t.Fatalf("viewport block count = %d, want 1", len(blocks))
	}
	blockLines := blocks[0].LineCount(m.viewport.width)
	if blockLines > m.viewport.height {
		t.Fatalf("USER block has %d lines but viewport only has %d rows: card would be clipped", blockLines, m.viewport.height)
	}
	rendered := stripANSI(m.viewport.Render("", nil, -1))
	if !strings.Contains(rendered, "hello from test") {
		t.Fatalf("rendered viewport missing body text. Rendered:\n%s", rendered)
	}
}

// TestViewRecalcsViewportHeightFromLayoutBeforeRender ensures Draw/View
// self-heals a stale viewport.height by syncing it to layout.main.Dy(). Without
// this, scrollToEnd/maxOffset can be computed using an oversized viewport and
// the last transcript rows become unreachable behind the bottom chrome.
func TestViewRecalcsViewportHeightFromLayoutBeforeRender(t *testing.T) {
	const termHeight = 24
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 80, termHeight)
	m.mode = ModeInsert

	for i := 0; i < 12; i++ {
		m.viewport.AppendBlock(&Block{ID: i + 1, Type: BlockUser, Content: strings.Repeat("line ", 12) + fmt.Sprintf("tail-%02d", i)})
	}
	m.recalcViewportSize()
	m.layout = m.generateLayout(m.width, m.height)
	wantHeight := m.layout.main.Dy()
	if wantHeight <= 3 {
		t.Fatalf("precondition failed: layout main height = %d, want > 3", wantHeight)
	}

	m.viewport.SetSize(m.viewport.width, wantHeight+3)
	if m.viewport.height != wantHeight+3 {
		t.Fatalf("precondition failed: viewport.height = %d, want %d", m.viewport.height, wantHeight+3)
	}

	view := m.View()
	if got := m.viewport.height; got != wantHeight {
		t.Fatalf("viewport.height after View = %d, want %d (must sync with layout.main.Dy())", got, wantHeight)
	}
	if !strings.Contains(stripANSI(view.Content), "tail-11") {
		t.Fatalf("rendered view missing last transcript tail after stale-height self-heal; got:\n%s", stripANSI(view.Content))
	}
}

// TestHandleInsertSubmitRestoresViewportHeightInAgentView covers the focused-
// subagent branch of handleInsertKey (app_keys_insert.go): that branch reaches
// sendDraft without its own recalcViewportSize, so the composer's previously-
// shrunken viewport height would otherwise survive into the next render.
func TestHandleInsertSubmitRestoresViewportHeightInAgentView(t *testing.T) {
	const termHeight = 24
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 80, termHeight)
	m.mode = ModeInsert
	// Simulate viewing a worker: the subagent branch of handleInsertKey takes
	// over when focusedAgentID != "".
	m.focusedAgentID = "worker-1"

	initialHeight := m.viewport.height
	m.input.SetValue(strings.Join([]string{"a", "b", "c", "d", "e"}, "\n"))
	m.input.syncHeight()
	m.recalcViewportSize()

	if m.viewport.height >= initialHeight {
		t.Fatalf("precondition failed: viewport did not shrink for worker view (initial=%d now=%d)", initialHeight, m.viewport.height)
	}

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	if got := m.viewport.height; got != initialHeight {
		t.Fatalf("viewport.height after worker submit = %d, want %d (restored)", got, initialHeight)
	}
}
