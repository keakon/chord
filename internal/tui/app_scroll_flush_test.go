package tui

import (
	"strings"
	"testing"
)

func TestConsumeScrollFlushSkipsHostRedrawWhenViewportUnchanged(t *testing.T) {
	m := NewModelWithSize(nil, 120, 24)
	m.SetFocusResizeFreezeEnabled(true)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: strings.Repeat("hello\n", 20)})
	m.width = 40
	m.viewport.width = 40
	m.viewport.height = 5
	m.recalcViewportSize()
	m.viewport.ScrollToTop()

	m.pendingScrollDelta = -20 // already at top; no movement
	m.scrollFlushScheduled = true
	m.scrollFlushGeneration = 1
	cmd := m.consumeScrollFlush(scrollFlushTickMsg{generation: 1})
	if cmd != nil {
		t.Fatal("consumeScrollFlush should not emit redraw command when viewport did not move")
	}
	if m.hostRedrawGeneration != 0 {
		t.Fatalf("hostRedrawGeneration = %d, want 0", m.hostRedrawGeneration)
	}
	if m.lastHostRedrawReason != "" {
		t.Fatalf("lastHostRedrawReason = %q, want empty", m.lastHostRedrawReason)
	}
}
