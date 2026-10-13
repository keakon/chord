package tui

import (
	"context"
	"image"
	"strings"
	"testing"
	"time"

	tea "github.com/keakon/bubbletea/v2"
	"github.com/keakon/x/ansi"

	"github.com/keakon/chord/internal/inputhistory"
)

func historyReadyModel(t *testing.T, text string) Model {
	t.Helper()
	m := historyTestModel(t)
	m.applyTerminalSize(120, 40, false)
	m.input.SetValue(text)
	m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	// Flush before searching so the fixture has a stable published snapshot.
	if err := m.inputHistoryStore.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	m.openInputHistory(true)
	waitHistoryReady(t, &m)
	waitHistoryPreview(t, &m)
	return m
}

func waitHistoryPreview(t *testing.T, m *Model) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for m.inputHistory.preview.Text == "" {
		select {
		case msg := <-m.inputHistoryRuntime.results:
			m.handleInputHistoryMessage(msg)
		case <-deadline.C:
			t.Fatal("preview timed out")
		}
	}
}

func TestInputHistoryUnchangedRefreshPreservesPendingPreview(t *testing.T) {
	m := historyReadyModel(t, "sample body")
	r, _ := m.selectedHistoryRecord()
	token, revision := m.inputHistory.token, m.inputHistory.previewRevision
	entered, release := make(chan struct{}), make(chan struct{})
	m.inputHistory.preview = inputhistory.PreviewPage{}
	m.inputHistoryRuntime.submit(historySearchTask{priority: 2, run: func(ctx context.Context) tea.Msg {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return nil
		}
		return inputHistoryPreviewMsg{token, r.ID, revision, inputhistory.PreviewPage{Text: "sample body"}}
	}})
	<-entered
	refresh := m.requestInputHistorySnapshot(true)
	m.handleInputHistoryMessage(refresh())
	close(release)
	waitHistoryPreview(t, &m)
	if m.inputHistory.preview.Text != "sample body" {
		t.Fatal("unchanged refresh lost preview")
	}
}

func TestInputHistoryResizeRepaginatesAndRejectsOldPage(t *testing.T) {
	m := historyReadyModel(t, strings.Repeat("a", 70)+"marker"+strings.Repeat("b", 1000))
	r, _ := m.selectedHistoryRecord()
	old := inputHistoryPreviewMsg{m.inputHistory.token, r.ID, m.inputHistory.previewRevision, m.inputHistory.preview}
	m.Update(tea.WindowSizeMsg{Width: 50, Height: 40})
	m.handleInputHistoryMessage(old)
	if m.inputHistory.preview.Text != "" {
		t.Fatal("accepted preview with obsolete dimensions")
	}
	waitHistoryPreview(t, &m)
	if !strings.Contains(m.inputHistory.preview.Text, "marker") {
		t.Fatal("resized preview skipped text")
	}
	if m.inputHistory.preview.End >= old.page.End {
		t.Fatal("preview retained old page boundary")
	}
	rendered := ansi.Strip(m.renderInputHistory())
	if !strings.Contains(rendered, "marker") {
		t.Fatal("resized page was clipped")
	}
	end := m.inputHistory.preview.End
	m.scrollInputHistoryPreview(false)
	// Wait for the requested next page, not the previously displayed page.
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for m.inputHistory.preview.Start != end {
		select {
		case msg := <-m.inputHistoryRuntime.results:
			m.handleInputHistoryMessage(msg)
		case <-deadline.C:
			t.Fatal("next page timed out")
		}
	}
}

func TestInputHistoryNarrowMouseMatchesRenderedRows(t *testing.T) {
	m := historyTestModel(t)
	m.width, m.height = 30, 30
	m.mode = ModeInputHistorySearch
	m.inputHistory.results = inputhistory.SearchResult{Hits: []inputhistory.Hit{{ID: "one", Summary: "first result"}, {ID: "two", Summary: "second result"}}}
	box := m.renderInputHistory()
	rect := centeredRect(image.Rect(0, 0, m.width, m.height), box)
	row := -1
	for i, line := range strings.Split(ansi.Strip(box), "\n") {
		if strings.Contains(line, "second result") {
			row = rect.Min.Y + i
			break
		}
	}
	if row < 0 {
		t.Fatal("result not visible")
	}
	click := func(x, y int) {
		m.handleInputHistoryMouse(tea.MouseClickMsg(tea.Mouse{X: x, Y: y, Button: tea.MouseLeft}))
	}
	click(0, row)
	if m.inputHistory.selected != 0 {
		t.Fatal("outside click changed selection")
	}
	click(m.inputHistory.listRect.Min.X, row)
	if m.inputHistory.selected != 1 {
		t.Fatal("click selected wrong rendered row")
	}
	for i, filter := range m.inputHistory.filterRects {
		click(filter.Min.X, filter.Min.Y)
		if m.inputHistory.focus != i+1 {
			t.Fatal("filter hit region does not match")
		}
	}
}

func TestInputHistoryQueryLimitsAcrossInputs(t *testing.T) {
	m := historyTestModel(t)
	m.input.SetValue(strings.Repeat("word ", 17))
	m.openInputHistory(false)
	if len(strings.Fields(m.inputHistory.query.Value())) != 16 || !m.inputHistory.queryLimited {
		t.Fatal("initial word limit missing")
	}
	m.closeInputHistory()
	m.input.SetValue(strings.Repeat("x", 257))
	m.openInputHistory(false)
	if len(m.inputHistory.query.Value()) != 256 || !m.inputHistory.queryLimited {
		t.Fatal("initial length limit missing")
	}
	m.inputHistory.query.SetValue(strings.Repeat("x", 256))
	m.inputHistory.query.CursorEnd()
	m.handleInputHistoryKey(tea.KeyPressMsg(tea.Key{Code: 'y', Text: "y"}))
	if len(m.inputHistory.query.Value()) != 256 || !m.inputHistory.queryLimited {
		t.Fatal("typed length limit missing")
	}
	m.inputHistory.query.SetValue("")
	m.pasteInputHistoryQuery(strings.Repeat("word ", 17))
	if len(strings.Fields(m.inputHistory.query.Value())) != 16 || !m.inputHistory.queryLimited {
		t.Fatal("pasted word limit missing")
	}
}

func TestInputHistorySnapshotOwnershipAndCancellation(t *testing.T) {
	m := historyTestModel(t)
	m.openInputHistory(true)
	ctx := m.inputHistory.snapshotContext
	old := inputHistorySnapshotMsg{generation: m.inputHistory.token.generation, owner: m.inputHistoryOwner(), snapshot: inputhistory.Snapshot{Version: 99}}
	m.closeInputHistory()
	if ctx.Err() == nil {
		t.Fatal("snapshot request outlived closed search")
	}
	m.openInputHistory(true)
	m.handleInputHistoryMessage(old)
	if m.inputHistory.snapshot.Version == 99 {
		t.Fatal("stale snapshot crossed search generation")
	}
}

func TestInputHistoryConfiguredBindingAndDisable(t *testing.T) {
	m := historyTestModel(t)
	m.keyMap = KeyMapFromConfig(map[string][]string{"insert_history_search": {"alt+h"}})
	m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'r', Mod: tea.ModCtrl}))
	if m.historyActive() {
		t.Fatal("default binding survived override")
	}
	m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'h', Mod: tea.ModAlt}))
	if m.mode != ModeInputHistorySearch {
		t.Fatal("configured history binding not registered")
	}
	m.closeInputHistory()
	m.keyMap = KeyMapFromConfig(map[string][]string{"insert_history_search": {}})
	m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'r', Mod: tea.ModCtrl}))
	if m.historyActive() || len(m.keyMap.InsertHistorySearch) != 0 {
		t.Fatal("disabled history binding remains active")
	}
	if len(m.keyMap.ServiceTier) != 1 || m.keyMap.ServiceTier[0] != "ctrl+r" {
		t.Fatal("normal mode service tier binding changed")
	}
}
