package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

func TestCompactionHistorySharesLayoutAndHidesComposer(t *testing.T) {
	m, backend := newCompactionHistoryIsolationModel(t)
	t.Cleanup(func() { _ = m.Close() })
	m.width = 150
	m.updateRightPanelVisible()
	m.mode = ModeInsert
	m.input.SetValue("unfinished draft")
	m.input.CursorEnd()
	m.input.Focus()
	m.attachments = []Attachment{{FileName: "sample-report.txt", SizeBytes: 2048}}
	m.queuedDrafts = []queuedDraft{{ID: "draft-1", Content: "queued follow-up", DisplayContent: "queued follow-up"}}
	m.View()
	liveLayout := m.layout
	liveViewport := m.viewport
	liveWidth, liveHeight := m.viewport.width, m.viewport.height
	line, column := m.input.Line(), m.input.Column()
	driveCompactionHistoryCmd(t, m, m.openCompactionHistory())
	view := m.View()
	plain := terminaltext.StripANSI(view.Content)
	if m.layout.main.Dx() != liveLayout.main.Dx() || m.layout.infoPanel.Dx() != rightPanelWidth ||
		m.layout.status != liveLayout.status || m.layout.main.Min.Y != 0 {
		t.Fatalf("history did not reuse the main layout: %+v", m.layout)
	}
	if m.layout.input.Dy() != 0 || m.layout.attachments.Dy() != 0 || m.layout.queue.Dy() != 0 ||
		m.layout.main.Max.Y != m.layout.statusSeparator.Min.Y ||
		m.layout.statusSeparator.Dy() != 1 || m.layout.statusSeparator.Max.Y != m.layout.status.Min.Y {
		t.Fatalf("history reserved composer space: %+v", m.layout)
	}
	rows := strings.Split(plain, "\n")
	if got := strings.TrimSpace(rows[m.layout.status.Min.Y-1]); got != strings.Repeat(SectionSeparator, m.width-1) {
		t.Fatalf("separator above status = %q", got)
	}
	if m.cachedInfoPanelRender.text == "" || !strings.Contains(plain, "HISTORY") {
		t.Fatalf("history is missing the sidebar or status: %s", plain)
	}
	for _, hidden := range []string{"unfinished draft", "sample-report.txt", "queued follow-up", "Earlier", "Later"} {
		if strings.Contains(plain, hidden) {
			t.Fatalf("history renders hidden content %q: %s", hidden, plain)
		}
	}
	if view.Cursor != nil || m.viewport != liveViewport || m.viewport.width != liveWidth || m.viewport.height != liveHeight {
		t.Fatal("history changed the live viewport or exposed its cursor")
	}
	m.handleMouseMsg(tea.MouseClickMsg{X: 1, Y: liveLayout.input.Min.Y + 1, Button: tea.MouseLeft})
	m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m.Update(tea.PasteMsg{Content: "pasted text"})
	m.Update(focusInputMsg{})
	if m.input.Value() != "unfinished draft" || m.input.Line() != line || m.input.Column() != column ||
		m.input.Focused() || len(backend.submittedInputs) != 0 || m.View().Cursor != nil {
		t.Fatal("history input reached the live composer")
	}
	status := terminaltext.StripANSI(m.renderStatusBar())
	if !strings.Contains(status, "HISTORY 1/1 · esc ⇢ live") || strings.Contains(status, "read-only") || strings.Contains(status, "Esc Live") {
		t.Fatalf("history does not use the standard left-side Esc hint: %q", status)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m.View()
	if m.compactionHistoryActive() || m.layout != liveLayout || !m.input.Focused() || m.input.Value() != "unfinished draft" ||
		m.input.Line() != line || m.input.Column() != column {
		t.Fatal("returning from history did not restore the layout and draft")
	}
}

func TestCompactionHistorySearchHasOwnRow(t *testing.T) {
	m := installedCompactionHistoryModel(t, reflowTestMessages(3))
	t.Cleanup(func() { _ = m.Close() })
	m.input.SetValue("unfinished draft")
	m.attachments = []Attachment{{FileName: "sample.txt"}}
	m.View()
	before := m.layout.main.Dy()
	m.beginCompactionHistorySearch()
	m.handleCompactionHistoryKey(tea.KeyPressMsg{Code: 'x', Text: "x"})
	plain := terminaltext.StripANSI(m.View().Content)
	if m.layout.input.Dy() != 1 || m.layout.main.Dy() != before-1 || m.layout.attachments.Dy() != 0 ||
		m.layout.input.Max.Y != m.layout.statusSeparator.Min.Y ||
		m.layout.statusSeparator.Max.Y != m.layout.status.Min.Y ||
		!strings.Contains(plain, "/x") || strings.Contains(plain, "unfinished draft") {
		t.Fatalf("search did not get its own row: %s", plain)
	}
	if status := terminaltext.StripANSI(m.renderStatusBar()); !strings.Contains(status, "esc ⇢ cancel search") {
		t.Fatalf("history search does not use the standard Esc hint: %q", status)
	}
	m.handleCompactionHistoryKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	m.View()
	if !m.compactionHistoryActive() || m.layout.input.Dy() != 0 || m.layout.main.Dy() != before {
		t.Fatal("leaving search did not restore the history content area")
	}
}

func TestCompactionHistorySidebarDoesNotNavigateHistory(t *testing.T) {
	m := installedCompactionHistoryModel(t, reflowTestMessages(120))
	t.Cleanup(func() { _ = m.Close() })
	m.width = 150
	m.updateRightPanelVisible()
	driveCompactionHistoryCmd(t, m, m.startCompactionHistoryReflow())
	m.View()
	h := &m.compactionHistory
	offset := h.viewport.offset
	mouse := tea.Mouse{X: m.layout.infoPanel.Min.X + 1, Y: 3, Button: tea.MouseWheelDown}
	m.handleMouseMsg(tea.MouseWheelMsg(mouse))
	mouse.Button = tea.MouseLeft
	m.handleMouseMsg(tea.MouseClickMsg(mouse))
	if h.viewport.offset != offset || h.selection.active || m.focusedAgentID != "" {
		t.Fatal("sidebar input reached history cards or changed the focused agent")
	}
	mouse.X = 1
	mouse.Button = tea.MouseWheelDown
	m.handleMouseMsg(tea.MouseWheelMsg(mouse))
	if h.viewport.offset <= offset {
		t.Fatal("scrolling over history content did not scroll the history")
	}
}

func TestCompactionHistoryResizeAcrossSidebarBoundary(t *testing.T) {
	m := installedCompactionHistoryModel(t, reflowTestMessages(120))
	t.Cleanup(func() { _ = m.Close() })
	h := &m.compactionHistory
	h.viewport.ScrollDown(120)
	wantID := h.viewport.GetBlockAtOffset().ID
	for _, size := range []tea.WindowSizeMsg{
		{Width: 150, Height: 35},
		{Width: 118, Height: 35},
		{Width: 115, Height: 35},
		{Width: 120, Height: 35},
		{Width: 120, Height: 25},
	} {
		t.Run(fmt.Sprintf("%dx%d", size.Width, size.Height), func(t *testing.T) {
			m.Update(size)
			m.Update(applyResizeMsg{version: m.resizeVersion})
			if h.installPending {
				driveCompactionHistoryCmd(t, m, m.handleCompactionHistoryInstall(compactionHistoryInstallMsg{
					generation: h.installGeneration,
				}))
			}
			m.View()
			if h.viewport.width != m.layout.main.Dx() || h.viewport.height != m.layout.main.Dy() ||
				len(m.compactionHistoryBlocks()) != 120 {
				t.Fatalf("history did not fit the resized layout: viewport=%dx%d, layout=%+v",
					h.viewport.width, h.viewport.height, m.layout)
			}
			if block := h.viewport.GetBlockAtOffset(); block == nil || block.ID != wantID {
				t.Fatal("resizing across the sidebar boundary lost the reading anchor")
			}
		})
	}
}

func TestCompactionHistorySourcesUseResizedContentWidth(t *testing.T) {
	m := installedCompactionHistoryModel(t, reflowTestMessages(1))
	t.Cleanup(func() { _ = m.Close() })
	h := &m.compactionHistory
	h.sourceListActive = true
	h.sources = []compactionHistorySource{{
		ref: message.CompactionArchiveRef{Index: 3, Topics: strings.Repeat("sample topic ", 10)},
	}}
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	m.View()
	rows := strings.Split(terminaltext.StripANSI(m.renderCompactionHistorySources()), "\n")
	if len([]rune(rows[2])) > m.layout.main.Dx() {
		t.Fatalf("source row does not fit the resized area: %q", rows[2])
	}
}
