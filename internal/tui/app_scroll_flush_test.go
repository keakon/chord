package tui

import (
	"strings"
	"testing"
	"time"
)

func TestConsumeScrollFlushSkipsHostRedrawWhenViewportUnchanged(t *testing.T) {
	m := NewModelWithSize(nil, 120, 24)
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
}

func TestConsumeScrollFlushCoalescesUntilConsumed(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	first := m.scheduleScrollFlush(16 * time.Millisecond)
	if first == nil {
		t.Fatal("first scheduleScrollFlush should return a tick command")
	}
	if !m.scrollFlushScheduled {
		t.Fatal("scheduleScrollFlush should mark flush as scheduled")
	}
	gen := m.scrollFlushGeneration
	if second := m.scheduleScrollFlush(16 * time.Millisecond); second != nil {
		t.Fatal("second scheduleScrollFlush before consume should be coalesced")
	}
	if cmd := m.consumeScrollFlush(scrollFlushTickMsg{generation: gen - 1}); cmd != nil {
		t.Fatal("consumeScrollFlush should ignore stale generation")
	}
	if !m.scrollFlushScheduled {
		t.Fatal("stale generation should not clear scheduled flag")
	}
	m.pendingScrollDelta = 6
	m.viewport = nil
	if cmd := m.consumeScrollFlush(scrollFlushTickMsg{generation: gen}); cmd != nil {
		t.Fatal("consumeScrollFlush without viewport should not emit redraw cmd")
	}
	if m.scrollFlushScheduled {
		t.Fatal("consumeScrollFlush should clear scheduled flag")
	}
	if m.pendingScrollDelta != 0 {
		t.Fatal("consumeScrollFlush should clear pendingScrollDelta")
	}
	if third := m.scheduleScrollFlush(16 * time.Millisecond); third == nil {
		t.Fatal("scheduleScrollFlush should schedule again after consume")
	}
}

func TestConsumeScrollFlushMovesViewportWithoutFollowUpCommand(t *testing.T) {
	m := NewModelWithSize(nil, 120, 24)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: strings.Repeat("hello\n", 80)})
	m.width = 40
	m.viewport.width = 40
	m.viewport.height = 5
	m.recalcViewportSize()
	m.viewport.ScrollToTop()
	m.pendingScrollDelta = 20
	m.scrollFlushScheduled = true
	m.scrollFlushGeneration = 1
	cmd := m.consumeScrollFlush(scrollFlushTickMsg{generation: 1})
	if cmd != nil {
		t.Fatal("consumeScrollFlush should not emit a follow-up command when viewport moved")
	}
	if m.viewport.offset == 0 {
		t.Fatal("expected consumeScrollFlush to move viewport down")
	}
}

func TestStreamBoundaryFlushSchedulesStreamFlush(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)

	cmd := m.requestStreamBoundaryFlush()
	if cmd == nil {
		t.Fatal("stream boundary flush should schedule stream flush")
	}
}

func TestSendDraftDoesNotNeedFollowUpCommandWithoutInlineImages(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)

	cmd := m.sendDraft(queuedDraft{Content: "hello", QueuedAt: time.Now()})
	if cmd != nil {
		t.Fatal("sendDraft should not schedule a follow-up command without inline images")
	}
}
