package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/memory"
)

type memoryPanelState struct {
	prevMode        Mode
	view            *agent.MemoryView
	tab             int
	query           string
	inputFocused    bool
	instructionMode bool
	instructionAll  bool
	instruction     string
	list            *OverlayList
	listBaseRow     int
	selected        map[string]bool
	detail          bool
	preview         *memory.ManualDraft
	loading         bool
	err             string
	seq             uint64
	epoch           uint64
	cancel          context.CancelFunc
	autoOrganize    *string
	viewer          contentViewerState
	pendingResult   *memoryPanelResultMsg
}

type memoryPanelResultMsg struct {
	seq     uint64
	epoch   uint64
	view    *agent.MemoryView
	draft   *memory.ManualDraft
	changed bool
	err     error
}

func (m *Model) memoryController() (agent.MemoryController, bool) {
	c, ok := m.agent.(agent.MemoryController)
	return c, ok
}

func (m *Model) openMemoryPanel(auto *string) tea.Cmd {
	c, ok := m.memoryController()
	if !ok {
		return m.enqueueToast("Memory management is unavailable for this connection", "info")
	}
	if m.memoryPanel.cancel != nil {
		m.memoryPanel.cancel()
	}
	seq := m.memoryPanel.seq + 1
	m.memoryPanel = memoryPanelState{prevMode: m.mode, seq: seq, epoch: m.sessionTranscriptEpoch, selected: map[string]bool{}, loading: true, autoOrganize: auto}
	m.clearChordState()
	m.clearActiveSearch()
	m.input.Blur()
	cmd := m.switchModeWithIME(ModeMemoryPanel)
	m.recalcViewportSize()
	return tea.Batch(cmd, m.loadMemoryPanel(c))
}

func (m *Model) loadMemoryPanel(c agent.MemoryController) tea.Cmd {
	if m.memoryPanel.cancel != nil {
		m.memoryPanel.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.memoryPanel.cancel = cancel
	seq, epoch := m.memoryPanel.seq, m.memoryPanel.epoch
	return func() tea.Msg {
		view, err := c.ReviewMemory(ctx)
		return memoryPanelResultMsg{seq: seq, epoch: epoch, view: view, err: err}
	}
}

func (m *Model) closeMemoryPanel() tea.Cmd {
	if m.memoryPanel.cancel != nil {
		m.memoryPanel.cancel()
	}
	prev := m.memoryPanel.prevMode
	seq := m.memoryPanel.seq + 1
	m.memoryPanel = memoryPanelState{seq: seq}
	m.contentViewer = contentViewerState{}
	m.clearChordState()
	cmd := m.restoreModeWithIME(prev)
	m.recalcViewportSize()
	if prev == ModeInsert {
		return tea.Batch(cmd, m.input.Focus())
	}
	return cmd
}

func (m *Model) handleMemoryPanelResult(msg memoryPanelResultMsg) tea.Cmd {
	if msg.seq != m.memoryPanel.seq || msg.epoch != m.sessionTranscriptEpoch {
		return nil
	}
	if m.mode != ModeMemoryPanel {
		if m.confirm.prevMode == ModeMemoryPanel || m.question.prevMode == ModeMemoryPanel || m.handoffSelect.prevMode == ModeMemoryPanel {
			m.memoryPanel.pendingResult = &msg
		}
		return nil
	}
	m.memoryPanel.loading = false
	if msg.err != nil {
		m.memoryPanel.autoOrganize = nil
		m.memoryPanel.err = msg.err.Error()
		return m.enqueueToast("Memory: "+msg.err.Error(), "error")
	}
	m.memoryPanel.err = ""
	if msg.draft != nil {
		m.memoryPanel.preview = msg.draft
		m.showMemoryDetail("Review memory changes — a apply, Esc cancel", msg.draft.Preview())
		return nil
	}
	if msg.view != nil {
		m.memoryPanel.view = msg.view
		m.rebuildMemoryList()
	}
	if msg.changed {
		c, _ := m.memoryController()
		m.memoryPanel.preview = nil
		m.memoryPanel.detail = false
		m.memoryPanel.selected = map[string]bool{}
		m.memoryPanel.seq++
		m.memoryPanel.loading = true
		return tea.Batch(m.enqueueToast("Memory updated; subsequent main requests use the refreshed summary", "info"), m.loadMemoryPanel(c))
	}
	if m.memoryPanel.autoOrganize != nil {
		instruction := *m.memoryPanel.autoOrganize
		m.memoryPanel.autoOrganize = nil
		return m.startMemoryOrganization(true, instruction)
	}
	return nil
}

func (m *Model) rebuildMemoryList() {
	p := &m.memoryPanel
	oldID := ""
	if p.list != nil {
		if item, ok := p.list.SelectedItem(); ok {
			oldID = item.ID
		}
	}
	var items []OverlayListItem
	if p.view != nil {
		switch p.tab {
		case 0:
			for i, item := range p.view.Snapshot.Items {
				if !memoryQueryMatches(p.query, item.Entry.Summary+" "+string(memoryItemType(item))+" "+item.Content) {
					continue
				}
				marker := "[ ] "
				if p.selected[item.Entry.ID] {
					marker = "[x] "
				}
				label := marker + string(memoryItemType(item)) + "  " + item.Entry.Summary
				if item.Error != "" {
					label += " (unreadable)"
				}
				if p.query != "" && !memoryQueryMatches(p.query, item.Entry.Summary) {
					label += " · " + memoryMatchSnippet(item.Content, p.query)
				}
				items = append(items, OverlayListItem{ID: item.Entry.ID, Label: sanitizeToolDisplayText(label), Value: i})
			}
			if notes := p.view.Snapshot.Index.UserNotes(); notes != "" && memoryQueryMatches(p.query, notes) {
				items = append(items, OverlayListItem{ID: "user-notes", Label: "User notes (read only)", Value: -1})
			}
		case 1:
			if p.view.Applied != "" && memoryQueryMatches(p.query, p.view.Applied) {
				items = append(items, OverlayListItem{ID: "applied", Label: "Actual summary applied to this session"})
			}
		case 2:
			for i, item := range p.view.Snapshot.Suggestions {
				if memoryQueryMatches(p.query, item.Title+" "+item.Content) {
					items = append(items, OverlayListItem{ID: item.Path, Label: sanitizeToolDisplayText(item.Title), Value: i})
				}
			}
		}
	}
	if p.list == nil {
		p.list = NewOverlayList(items, max(1, m.height-12))
	} else {
		p.list.SetItems(items)
	}
	for i, item := range items {
		if item.ID == oldID {
			p.list.SetCursor(i)
			break
		}
	}
}

func memoryItemType(item memory.ReviewItem) memory.Type {
	if item.Record != nil {
		return item.Record.Type
	}
	return "record"
}
func memoryQueryMatches(query, content string) bool {
	content = strings.ToLower(content)
	for word := range strings.FieldsSeq(strings.ToLower(query)) {
		if !strings.Contains(content, word) {
			return false
		}
	}
	return true
}
func memoryMatchSnippet(content, query string) string {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return ""
	}
	for line := range strings.SplitSeq(content, "\n") {
		if strings.Contains(strings.ToLower(line), words[0]) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

func (m *Model) memoryCurrentContent() (title, content, path string) {
	p := &m.memoryPanel
	if p.view == nil || p.list == nil {
		return
	}
	item, ok := p.list.SelectedItem()
	if !ok {
		return
	}
	switch p.tab {
	case 0:
		idx, ok := item.Value.(int)
		if !ok {
			return
		}
		if idx < 0 {
			return "User notes", p.view.Snapshot.Index.UserNotes(), ""
		}
		record := p.view.Snapshot.Items[idx]
		content = record.Content
		if record.Error != "" {
			content = "Cannot read record: " + record.Error + "\n\nYou can remove its active index entry."
		}
		return record.Entry.Summary, content, record.Path
	case 1:
		return "Session applied summary", p.view.Applied, ""
	case 2:
		idx, ok := item.Value.(int)
		if !ok {
			return
		}
		record := p.view.Snapshot.Suggestions[idx]
		return record.Title, record.Content, record.Path
	}
	return
}

func (m *Model) memorySelectedSnapshot(all bool) (*memory.ReviewSnapshot, error) {
	p := &m.memoryPanel
	if p.view == nil {
		return nil, fmt.Errorf("memory is not loaded")
	}
	if p.tab != 0 && !all {
		return nil, fmt.Errorf("select project memories to modify")
	}
	if all {
		return p.view.Snapshot, nil
	}
	var ids []string
	for _, i := range p.view.Snapshot.Items {
		if p.selected[i.Entry.ID] {
			ids = append(ids, i.Entry.ID)
		}
	}
	if len(ids) == 0 && p.list != nil {
		if item, ok := p.list.SelectedItem(); ok && memory.ValidateRecordID(item.ID) {
			ids = append(ids, item.ID)
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("no project memory selected")
	}
	return p.view.Snapshot.Select(ids)
}

func (m *Model) showMemoryDetail(title, content string) {
	m.memoryPanel.detail = true
	m.memoryPanel.inputFocused = false
	m.clearChordState()
	m.contentViewer = contentViewerState{title: sanitizeToolDisplayText(title), content: content, prevMode: ModeMemoryPanel, selStartLine: -1, selEndLine: -1}
	m.memoryPanel.viewer = m.contentViewer
}

func (m *Model) resumeMemoryPanel() tea.Cmd {
	if msg := m.memoryPanel.pendingResult; msg != nil {
		m.memoryPanel.pendingResult = nil
		return m.handleMemoryPanelResult(*msg)
	}
	return nil
}
