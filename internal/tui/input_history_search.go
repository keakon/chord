package tui

import (
	"context"
	"image"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/keakon/bubbles/v2/textinput"
	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/inputhistory"
	"github.com/keakon/chord/internal/tui/composer"
)

type inputHistoryOwner struct {
	epoch          uint64
	agent, project string
}
type inputHistoryToken struct {
	generation, queryRevision, snapshotVersion uint64
	owner                                      inputHistoryOwner
}
type inputHistorySearchState struct {
	token                          inputHistoryToken
	interaction                    uint64
	query                          textinput.Model
	allProjects, allKinds          bool
	focus, selected                int
	snapshot                       inputhistory.Snapshot
	results                        inputhistory.SearchResult
	loading                        bool
	refreshError                   error
	refreshing, dirty, refreshDisk bool
	entriesByID                    map[string]inputhistory.Entry
	queryLimited                   bool
	resetSelection                 bool
	snapshotContext                context.Context
	snapshotCancel                 context.CancelFunc
	prepared                       composer.HistoryEntry
	preparedID                     string
	preview                        inputhistory.PreviewPage
	previewRevision                uint64
	previewOffset                  int
	previewBack                    []int
	previewCheckpoints             []int
	previewDirection               int
	confirm, clearing              bool
	listBase, listRows, listStart  int
	listRect, previewRect          image.Rectangle
	filterRects                    [2]image.Rectangle
}
type inputHistorySnapshotMsg struct {
	generation uint64
	owner      inputHistoryOwner
	snapshot   inputhistory.Snapshot
	entries    map[string]inputhistory.Entry
	err        error
}
type inputHistoryMatchMsg struct {
	token  inputHistoryToken
	result inputhistory.SearchResult
}
type inputHistoryPreviewMsg struct {
	token    inputHistoryToken
	id       string
	revision uint64
	page     inputhistory.PreviewPage
}
type inputHistoryRestoreReadyMsg struct {
	token inputHistoryToken
	id    string
	entry composer.HistoryEntry
}
type inputHistoryTickMsg struct{ generation uint64 }

func (m *Model) inputHistoryOwner() inputHistoryOwner {
	return inputHistoryOwner{m.sessionTranscriptEpoch, m.focusedAgentID, m.inputHistoryProjectKey}
}
func (m *Model) historyActive() bool {
	return m.mode == ModeInputHistorySearch || m.mode == ModeInputHistoryClearConfirm
}
func (m *Model) closeInputHistory() {
	if !m.historyActive() {
		return
	}
	m.inputHistoryRuntime.invalidate()
	if m.inputHistory.snapshotCancel != nil {
		m.inputHistory.snapshotCancel()
	}
	generation := m.inputHistory.token.generation + 1
	m.inputHistory = inputHistorySearchState{token: inputHistoryToken{generation: generation}}
	m.mode = ModeInsert
	m.input.Focus()
	m.invalidateDrawCaches()
}
func historyInitialQuery(entry composer.HistoryEntry) (string, bool) {
	// Walk structured objects without expanding the complete composer.
	var b strings.Builder
	count := 0
	started := false
	done := false
	limited := false
	visit := func(text string) {
		for _, c := range text {
			if done {
				return
			}
			if !started && unicode.IsSpace(c) {
				continue
			}
			started = true
			if c == '\n' || count == 256 {
				limited = count == 256 && c != '\n'
				done = true
				return
			}
			b.WriteRune(c)
			count++
		}
	}
	byteAt, runeAt := 0, 0
	for _, p := range entry.InlinePastes {
		start := byteAt
		for byteAt < len(entry.Display) && runeAt < p.Start {
			_, n := utf8.DecodeRuneInString(entry.Display[byteAt:])
			byteAt += n
			runeAt++
		}
		visit(entry.Display[start:byteAt])
		for byteAt < len(entry.Display) && runeAt < p.End {
			_, n := utf8.DecodeRuneInString(entry.Display[byteAt:])
			byteAt += n
			runeAt++
		}
		if p.Kind == composer.TokenPaste {
			visit(p.RawContent)
		}
		if done {
			return normalizeInitialHistoryQuery(b.String(), limited)
		}
	}
	visit(entry.Display[byteAt:])
	return normalizeInitialHistoryQuery(b.String(), limited)
}
func (m *Model) openInputHistory(empty bool) tea.Cmd {
	if m.takeoverInputFrozen() || m.startupRestorePending || m.sessionSwitch.active() {
		return m.enqueueToast("Wait for the session to finish loading or switching", "info")
	}
	if m.inputHistoryStore == nil {
		return m.enqueueToast("Input history is unavailable in this runtime", "info")
	}
	q := textinput.New()
	q.CharLimit = 257 // One extra rune lets the shared limiter report truncation.
	q.Prompt = "Search: "
	limited := false
	if !empty {
		var initial string
		initial, limited = historyInitialQuery(m.input.HistoryEntry())
		q.SetValue(initial)
	}
	focus := q.Focus()
	generation := m.inputHistory.token.generation + 1
	m.inputHistory = inputHistorySearchState{token: inputHistoryToken{generation: generation, queryRevision: 1, owner: m.inputHistoryOwner()}, interaction: m.input.Revision().Interaction, query: q, queryLimited: limited, loading: true}
	m.inputHistory.snapshotContext, m.inputHistory.snapshotCancel = context.WithCancel(m.inputHistoryRuntime.ctx)
	m.closeAtMention()
	m.mode = ModeInputHistorySearch
	// Keep the composer selection and interaction version intact while searching.
	m.input.Suspend()
	return tea.Batch(focus, m.requestInputHistorySnapshot(true), m.inputHistoryTick())
}
func (m *Model) inputHistoryTick() tea.Cmd {
	g := m.inputHistory.token.generation
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return inputHistoryTickMsg{g} })
}

// Snapshot I/O has its own bounded request, so an unchanged refresh cannot
// cancel matching, restoration or preview work.
func (m *Model) requestInputHistorySnapshot(refresh bool) tea.Cmd {
	s := &m.inputHistory
	s.refreshDisk = s.refreshDisk || refresh
	if s.refreshing {
		s.dirty = true
		return nil
	}
	s.refreshing = true
	refresh = s.refreshDisk
	s.refreshDisk = false
	generation, owner := s.token.generation, s.token.owner
	store, ctx := m.inputHistoryStore, s.snapshotContext
	return func() tea.Msg {
		snapshot, err := store.Snapshot(ctx, refresh)
		entries := make(map[string]inputhistory.Entry, len(snapshot.Entries))
		for _, entry := range snapshot.Entries {
			entries[entry.Record.ID] = entry
		}
		return inputHistorySnapshotMsg{generation, owner, snapshot, entries, err}
	}
}
func (m *Model) queueInputHistoryMatch(queryChanged bool) {
	s := &m.inputHistory
	if queryChanged {
		s.token.queryRevision++
		s.selected = 0
		s.resetSelection = true
	}
	s.loading = true
	s.preparedID = ""
	token, snapshot := s.token, s.snapshot
	q := inputhistory.Query{Text: s.query.Value(), ProjectKey: m.inputHistoryProjectKey, AllProjects: s.allProjects, AllKinds: s.allKinds}
	m.inputHistoryRuntime.submit(historySearchTask{priority: 0, run: func(ctx context.Context) tea.Msg {
		result, err := inputhistory.Match(ctx, snapshot, q)
		if err != nil {
			return nil
		}
		return inputHistoryMatchMsg{token, result}
	}})
}
func (m *Model) validHistoryToken(t inputHistoryToken) bool {
	return m.mode == ModeInputHistorySearch && t.generation == m.inputHistory.token.generation && t.queryRevision == m.inputHistory.token.queryRevision && t.snapshotVersion == m.inputHistory.token.snapshotVersion && t.owner == m.inputHistoryOwner()
}
func (m *Model) selectedHistoryRecord() (inputhistory.Record, bool) {
	s := &m.inputHistory
	if s.selected < 0 || s.selected >= len(s.results.Hits) {
		return inputhistory.Record{}, false
	}
	id := s.results.Hits[s.selected].ID
	entry, ok := s.entriesByID[id]
	return entry.Record, ok
}
func (m *Model) prepareInputHistorySelection() {
	s := &m.inputHistory
	s.preparedID = ""
	s.preview = inputhistory.PreviewPage{}
	s.previewOffset = 0
	s.previewBack = nil
	s.previewCheckpoints = nil
	record, ok := m.selectedHistoryRecord()
	if !ok {
		return
	}
	token := s.token
	m.inputHistoryRuntime.submit(historySearchTask{priority: 1, run: func(ctx context.Context) tea.Msg {
		entry := historyRestoreEntry(record)
		if ctx.Err() != nil {
			return nil
		}
		return inputHistoryRestoreReadyMsg{token, record.ID, entry}
	}})
	m.queueInputHistoryPreview()
}
func (m *Model) queueInputHistoryPreview() {
	s := &m.inputHistory
	r, ok := m.selectedHistoryRecord()
	if !ok {
		return
	}
	s.previewRevision++
	rev, token := s.previewRevision, s.token
	checkpoint := 0
	for _, offset := range s.previewCheckpoints {
		if offset >= s.previewOffset {
			break
		}
		checkpoint = offset
	}
	req := inputhistory.PreviewRequest{Checkpoint: checkpoint, Direction: s.previewDirection, Offset: s.previewOffset, Width: m.inputHistoryLayout().width, Lines: max(1, m.inputHistoryLayout().previewRows), Query: s.query.Value()}
	s.previewDirection = 0
	m.inputHistoryRuntime.submit(historySearchTask{priority: 2, run: func(ctx context.Context) tea.Msg {
		page, err := inputhistory.Preview(ctx, r, req)
		if err != nil {
			return nil
		}
		return inputHistoryPreviewMsg{token, r.ID, rev, page}
	}})
}
func (m *Model) applyInputHistorySelection() tea.Cmd {
	s := &m.inputHistory
	if s.loading {
		return m.enqueueToast("Searching input history", "info")
	}
	r, ok := m.selectedHistoryRecord()
	if !ok {
		return nil
	}
	if s.token.owner != m.inputHistoryOwner() || s.interaction != m.input.Revision().Interaction {
		m.closeInputHistory()
		return m.enqueueToast("The draft changed; search closed to preserve it", "info")
	}
	if s.preparedID != r.ID {
		return m.enqueueToast("Preparing input; press Enter again when ready", "info")
	}
	if m.composerEditSnapshot().size() > composerUndoBytes {
		return m.enqueueToast("Save or clear this draft first; it exceeds the 8 MiB undo limit", "warn")
	}
	m.cancelClipboardAttachmentPaste()
	finish := m.beginComposerEdit(false)
	entry := s.prepared
	m.input.SetDisplayValueAndPastes(entry.Display, entry.InlinePastes, entry.NextPasteSeq)
	m.input.SetBangMode(entry.BangMode)
	m.attachments = nil
	m.input.ClearSelection()
	m.closeAtMention()
	m.slashCompleteSelected = 0
	m.input.CursorEnd()
	m.input.EndHistoryNavigation()
	finish()
	m.closeInputHistory()
	m.input.SyncHeight()
	m.recalcViewportSize()
	if r.AttachmentCount > 0 {
		return m.enqueueToast("History attachments were not restored; add them again", "info")
	}
	return nil
}
func (m *Model) handleInputHistoryMessage(msg tea.Msg) (tea.Cmd, bool) {
	switch v := msg.(type) {
	case inputHistoryClipboardMsg:
		if m.mode == ModeInputHistorySearch && v.generation == m.inputHistory.token.generation {
			if v.err != nil {
				return m.enqueueToast("Unable to read clipboard text", "warn"), true
			}
			return m.pasteInputHistoryQuery(v.text), true
		}
		return nil, true
	case inputHistoryStoreChangedMsg:
		status := m.inputHistoryStore.Status()
		var toast tea.Cmd
		if status.Err != nil && status.Count != m.inputHistoryIssueCount && time.Since(m.inputHistoryLastIssue) >= 30*time.Second {
			m.inputHistoryLastIssue = time.Now()
			m.inputHistoryIssueCount = status.Count
			text := "Input not saved to history"
			if status.Retained {
				text += "; searchable in this process only"
			}
			if status.State == inputhistory.Unknown {
				text = "Unable to confirm input history was saved"
			}
			if status.Reason == inputhistory.TooLarge {
				text = "Input too long; not saved to history"
			}
			toast = m.enqueueToast(text, "warn")
		}
		var refresh tea.Cmd
		if m.mode == ModeInputHistorySearch {
			refresh = m.requestInputHistorySnapshot(false)
		}
		return tea.Batch(toast, refresh, m.waitInputHistoryStore()), true
	case inputHistoryTickMsg:
		if m.mode == ModeInputHistorySearch && v.generation == m.inputHistory.token.generation {
			return tea.Batch(m.requestInputHistorySnapshot(true), m.inputHistoryTick()), true
		}
		return nil, true
	case inputHistorySnapshotMsg:
		s := &m.inputHistory
		if m.mode != ModeInputHistorySearch || v.generation != s.token.generation || v.owner != m.inputHistoryOwner() {
			return nil, true
		}
		s.refreshing = false
		s.refreshError = v.err
		if s.snapshot.Version != v.snapshot.Version || s.entriesByID == nil {
			s.snapshot, s.entriesByID = v.snapshot, v.entries
			s.token.snapshotVersion = v.snapshot.Version
			m.queueInputHistoryMatch(false)
		}
		if s.dirty {
			s.dirty = false
			return m.requestInputHistorySnapshot(s.refreshDisk), true
		}
		return nil, true
	case inputHistoryMatchMsg:
		if m.validHistoryToken(v.token) {
			s := &m.inputHistory
			oldID := ""
			if !s.resetSelection && s.selected >= 0 && s.selected < len(s.results.Hits) {
				oldID = s.results.Hits[s.selected].ID
			}
			s.resetSelection = false
			s.results = v.result
			s.loading = false
			for i, h := range s.results.Hits {
				if h.ID == oldID {
					s.selected = i
					break
				}
			}
			s.selected = min(s.selected, max(0, len(s.results.Hits)-1))
			m.prepareInputHistorySelection()
		}
		return m.waitInputHistorySearch(), true
	case inputHistoryRestoreReadyMsg:
		if m.validHistoryToken(v.token) && v.token.snapshotVersion == m.inputHistory.token.snapshotVersion {
			if r, ok := m.selectedHistoryRecord(); ok && r.ID == v.id {
				m.inputHistory.prepared = v.entry
				m.inputHistory.preparedID = v.id
			}
		}
		return m.waitInputHistorySearch(), true
	case inputHistoryPreviewMsg:
		if m.validHistoryToken(v.token) && v.token.snapshotVersion == m.inputHistory.token.snapshotVersion && v.revision == m.inputHistory.previewRevision {
			if r, ok := m.selectedHistoryRecord(); ok && r.ID == v.id {
				m.inputHistory.preview = v.page
				m.inputHistory.previewOffset = v.page.Start
			}
		}
		return m.waitInputHistorySearch(), true
	case inputHistoryClearMsg:
		if v.generation == m.inputHistory.token.generation {
			m.inputHistory.clearing = false
		}
		if v.result.Err != nil {
			return m.enqueueToast("Input history was not cleared: "+v.result.Err.Error(), "warn"), true
		}
		if m.mode == ModeInputHistoryClearConfirm && v.generation == m.inputHistory.token.generation {
			m.closeInputHistory()
		}
		return m.enqueueToast("Saved input history cleared", "info"), true
	}
	return nil, false
}
func normalizeInitialHistoryQuery(value string, limited bool) (string, bool) {
	normalized := inputhistory.NormalizeQuery(value)
	return normalized, limited || len(normalized) < len(value)
}

func (m *Model) resizeInputHistoryPreview() {
	if m.mode != ModeInputHistorySearch {
		return
	}
	s := &m.inputHistory
	s.previewOffset = s.preview.Start
	s.preview = inputhistory.PreviewPage{}
	s.previewBack = nil
	s.previewDirection = 0
	m.queueInputHistoryPreview()
}
