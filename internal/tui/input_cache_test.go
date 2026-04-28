package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
)

func TestViewRebuildsInputRenderWhenBangModeChanges(t *testing.T) {
	m := NewModelWithSize(nil, 24, 12)

	_ = m.View()
	before := m.cachedInputRender.text

	m.input.SetBangMode(true)
	_ = m.View()
	after := m.cachedInputRender.text

	if before == after {
		t.Fatal("cached input render did not change after bang-mode prompt change")
	}
	if !strings.Contains(after, "! ") {
		t.Fatalf("cached input render = %q, want shell prompt", after)
	}
}

func TestViewRebuildsInputRenderWhenSelectionRangeChanges(t *testing.T) {
	m := NewModelWithSize(nil, 24, 12)
	m.input.SetValue("abcdef")
	m.input.StartSelection(0, 1)
	m.input.UpdateSelection(0, 3)

	_ = m.View()
	before := m.cachedInputRender.text

	m.input.UpdateSelection(0, 5)
	_ = m.View()
	after := m.cachedInputRender.text

	if before == after {
		t.Fatal("cached input render did not change after selection range change")
	}
}

func TestViewUpdatesRenderedCursorWhenInputCursorMoves(t *testing.T) {
	m := NewModelWithSize(nil, 24, 12)
	m.input.SetValue("first\nsecond")
	m.input.SetCursorPosition(0, 0)

	first := m.View()
	if first.Cursor == nil {
		t.Fatal("first view cursor = nil, want visible input cursor")
	}
	firstPos := first.Cursor.Position

	m.input.SetCursorPosition(1, 0)
	second := m.View()
	if second.Cursor == nil {
		t.Fatal("second view cursor = nil, want visible input cursor")
	}
	secondPos := second.Cursor.Position
	if firstPos == secondPos {
		t.Fatalf("cursor position did not change after input cursor move: %v", firstPos)
	}
}

func TestViewRefreshesSearchInputAfterTyping(t *testing.T) {
	m := NewModelWithSize(nil, 40, 12)
	m.mode = ModeSearch
	m.search = NewSearchModel(ModeNormal)
	m.search.Input.SetWidth(m.width - 4)

	initial := stripANSI(m.View().Content)
	if strings.Contains(initial, "needle") {
		t.Fatalf("initial View() unexpectedly contains future search text: %q", initial)
	}

	m.search.Input.SetValue("needle")

	updated := stripANSI(m.View().Content)
	if !strings.Contains(updated, "/needle") {
		t.Fatalf("View() should refresh search input after typing, got:\n%s", updated)
	}
	if !strings.Contains(updated, " /needle") {
		t.Fatalf("search input should keep left inset for visual alignment, got:\n%s", updated)
	}
}

func TestViewRebuildsInputRenderWhenBusyAnimationFrameChanges(t *testing.T) {
	m := NewModelWithSize(nil, 24, 12)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityConnecting, AgentID: "main"}

	_ = m.View()
	stale := "stale input render"
	m.cachedInputRender.text = stale
	m.cachedInputKey = "stale-key"
	m.cachedInputAnimKey = m.inputAnimationCacheKeyAt(time.Now().Add(200 * time.Millisecond))
	_ = m.View()
	after := m.cachedInputRender.text

	if after == stale {
		t.Fatal("cached input render did not rebuild after animation frame change")
	}
	if m.cachedInputAnimKey == "" {
		t.Fatal("cached input animation key should be repopulated")
	}
}
