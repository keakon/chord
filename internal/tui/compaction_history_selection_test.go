package tui

import (
	"reflect"
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"
	"github.com/keakon/x/ansi"

	"github.com/keakon/chord/internal/message"
)

func compactionHistoryTextPoint(t *testing.T, m *Model, blockID int, text string) tea.Mouse {
	t.Helper()
	h := &m.compactionHistory
	m.renderCompactionHistory(m.compactionHistoryArea())
	block := h.viewport.GetFocusedBlock(blockID)
	offset, ok := h.viewport.LineOffsetForBlockID(blockID)
	if !ok || block == nil {
		t.Fatalf("block %d is not installed", blockID)
	}
	for line := 0; line < block.LineCount(h.viewport.width); line++ {
		plain, _ := h.viewport.GetLinePlain(blockID, line)
		if before, _, found := strings.Cut(plain, text); found {
			return tea.Mouse{
				X:      h.area.Min.X + ansi.StringWidth(before),
				Y:      h.contentTop() + offset + line - h.viewport.offset,
				Button: tea.MouseLeft,
			}
		}
	}
	t.Fatalf("block %d does not render %q", blockID, text)
	return tea.Mouse{}
}

func TestCompactionHistoryDragSelectsRenderedColumns(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "forward", true: "reverse"}[reverse], func(t *testing.T) {
			m := installedCompactionHistoryModel(t, []message.Message{{Role: message.RoleUser, Content: "alpha bravo charlie"}})
			t.Cleanup(func() { _ = m.Close() })
			start := compactionHistoryTextPoint(t, m, 0, "bravo")
			end := start
			end.X += len("bravo") - 1
			if reverse {
				start, end = end, start
			}
			m.handleCompactionHistoryMouse(tea.MouseClickMsg(start))
			m.handleCompactionHistoryMouse(tea.MouseMotionMsg(end))
			m.handleCompactionHistoryMouse(tea.MouseReleaseMsg(end))
			selection := m.compactionHistory.selection.rangePtr()
			if selection == nil || m.compactionHistory.viewport.ExtractSelectionText(*selection) != "bravo" {
				t.Fatalf("selection = %#v, want bravo", selection)
			}
			if m.compactionHistory.selection.dragging {
				t.Fatal("mouse release did not end selection")
			}
		})
	}
}

func TestCompactionHistorySelectionCrossesCards(t *testing.T) {
	m := installedCompactionHistoryModel(t, []message.Message{
		{Role: message.RoleUser, Content: "alpha bravo"},
		{Role: message.RoleAssistant, Content: "charlie delta"},
	})
	t.Cleanup(func() { _ = m.Close() })
	start := compactionHistoryTextPoint(t, m, 0, "bravo")
	end := compactionHistoryTextPoint(t, m, 1, "charlie")
	end.X += len("charlie") - 1
	m.handleCompactionHistoryMouse(tea.MouseClickMsg(start))
	m.handleCompactionHistoryMouse(tea.MouseMotionMsg(end))
	m.handleCompactionHistoryMouse(tea.MouseReleaseMsg(end))
	selection := m.compactionHistory.selection.rangePtr()
	if selection == nil {
		t.Fatal("no selection was created")
	}
	if text := m.compactionHistory.viewport.ExtractSelectionText(*selection); text != "bravo\nASSISTANT #2\ncharlie" {
		t.Fatalf("cross-card text = %q", text)
	}
}

func TestCompactionHistorySelectionUsesDisplayWidth(t *testing.T) {
	m := installedCompactionHistoryModel(t, []message.Message{{Role: message.RoleUser, Content: "alpha 中文 beta"}})
	t.Cleanup(func() { _ = m.Close() })
	start := compactionHistoryTextPoint(t, m, 0, "中文")
	end := start
	end.X += 3
	m.handleCompactionHistoryMouse(tea.MouseClickMsg(start))
	m.handleCompactionHistoryMouse(tea.MouseMotionMsg(end))
	m.handleCompactionHistoryMouse(tea.MouseReleaseMsg(end))
	selection := m.compactionHistory.selection.rangePtr()
	if selection == nil || m.compactionHistory.viewport.ExtractSelectionText(*selection) != "中文" {
		t.Fatalf("wide character selection = %#v", selection)
	}
}

func TestCompactionHistoryCopyPrefersSelectedText(t *testing.T) {
	m := installedCompactionHistoryModel(t, []message.Message{{Role: message.RoleUser, Content: "alpha bravo charlie"}})
	t.Cleanup(func() { _ = m.Close() })
	oldWrite := clipboardWriteAll
	var copied string
	clipboardWriteAll = func(text string) error {
		copied = text
		return nil
	}
	t.Cleanup(func() { clipboardWriteAll = oldWrite })
	start := compactionHistoryTextPoint(t, m, 0, "bravo")
	end := start
	end.X += len("bravo") - 1
	m.handleCompactionHistoryMouse(tea.MouseClickMsg(start))
	m.handleCompactionHistoryMouse(tea.MouseMotionMsg(end))
	m.handleCompactionHistoryMouse(tea.MouseReleaseMsg(end))
	for _, key := range []tea.KeyMsg{
		tea.KeyPressMsg(tea.Key{Code: 'y', Text: "y"}),
		tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModSuper}),
	} {
		cmd := m.handleCompactionHistoryKey(key)
		if cmd == nil {
			t.Fatal("copy did not return a clipboard command")
		}
		sequence := reflect.ValueOf(cmd())
		if sequence.Kind() != reflect.Slice || sequence.Len() != 2 {
			t.Fatalf("clipboard command = %v, want a two-command sequence", sequence.Kind())
		}
		result := sequence.Index(1).Call(nil)[0].Interface().(clipboardWriteResultMsg)
		if result.err != nil || copied != "bravo" {
			t.Fatalf("copied = %q, error = %v", copied, result.err)
		}
	}
}

func TestCompactionHistoryMultiClickAndNavigationKeepLiveSelection(t *testing.T) {
	m := installedCompactionHistoryModel(t, []message.Message{
		{Role: message.RoleUser, Content: "alpha bravo charlie"},
		{Role: message.RoleAssistant, Content: "another card"},
	})
	t.Cleanup(func() { _ = m.Close() })
	m.mouseSelection = viewportSelection{active: true, rangeValue: SelectionRange{StartBlockID: 8, EndBlockID: 9}}
	liveSelection := m.mouseSelection
	point := compactionHistoryTextPoint(t, m, 0, "bravo")
	for click := 1; click <= 3; click++ {
		m.handleCompactionHistoryMouse(tea.MouseClickMsg(point))
		m.handleCompactionHistoryMouse(tea.MouseReleaseMsg(point))
		if click >= 2 {
			selection := m.compactionHistory.selection.rangePtr()
			want := "bravo"
			if click == 3 {
				want = "alpha bravo charlie"
			}
			if selection == nil || m.compactionHistory.viewport.ExtractSelectionText(*selection) != want {
				t.Fatalf("click %d selection = %#v, want %q", click, selection, want)
			}
		}
	}
	h := &m.compactionHistory
	if !h.viewport.GetFocusedBlock(0).Focused {
		t.Fatal("clicking a card did not focus it")
	}
	m.handleCompactionHistoryKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyHome}))
	if h.focusedBlockID != -1 || h.viewport.GetFocusedBlock(0).Focused {
		t.Fatal("Home left stale focus on a history card")
	}
	m.closeCompactionHistory()
	if m.mouseSelection != liveSelection {
		t.Fatal("history selection modified the live selection")
	}
}

func TestCompactionHistoryNavigationFindsSameCardType(t *testing.T) {
	m := installedCompactionHistoryModel(t, []message.Message{
		{Role: message.RoleUser, Content: "first user card"},
		{Role: message.RoleAssistant, Content: "assistant card"},
		{Role: message.RoleUser, Content: "second user card"},
	})
	t.Cleanup(func() { _ = m.Close() })
	m.setCompactionHistoryFocus(0)
	m.handleCompactionHistoryKey(tea.KeyPressMsg(tea.Key{Code: ']', Text: "]"}))
	h := &m.compactionHistory
	if h.focusedBlockID != 2 || !h.viewport.GetFocusedBlock(2).Focused || h.viewport.GetFocusedBlock(0).Focused {
		t.Fatal("] did not focus the next user card")
	}
	m.handleCompactionHistoryKey(tea.KeyPressMsg(tea.Key{Code: '[', Text: "["}))
	if h.focusedBlockID != 0 || !h.viewport.GetFocusedBlock(0).Focused || h.viewport.GetFocusedBlock(2).Focused {
		t.Fatal("[ did not return focus to the previous user card")
	}
}
