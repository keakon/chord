package tui

import (
	"slices"
	"time"

	tea "github.com/keakon/bubbletea/v2"
)

const (
	composerUndoEntries  = 64
	composerUndoBytes    = 8 * 1024 * 1024
	composerTypingWindow = 750 * time.Millisecond
)

type composerEditSnapshot struct {
	draft         inputDraftSnapshot
	attachments   []Attachment
	queuedDraftID string
}

type composerUndoState struct {
	entries    []composerEditSnapshot
	bytes      int
	boundary   uint64
	depth      int
	typing     bool
	lastEdit   time.Time
	lastCursor int
}

func (s composerEditSnapshot) size() int {
	n := len(s.draft.Entry.Display) + len(s.queuedDraftID) + 128
	for _, p := range s.draft.Entry.InlinePastes {
		n += len(p.RawContent) + len(p.DisplayText) + 128
	}
	for _, a := range s.attachments {
		n += len(a.Data) + len(a.FileName) + len(a.MimeType) + len(a.ImagePath) + 128
	}
	return n
}

func (m *Model) composerEditSnapshot() composerEditSnapshot {
	return composerEditSnapshot{draft: m.input.draftSnapshot(), attachments: cloneAttachments(m.attachments), queuedDraftID: m.editingQueuedDraftID}
}

func sameComposerContent(a, b composerEditSnapshot) bool {
	if a.draft.Entry.Display != b.draft.Entry.Display || a.draft.Entry.BangMode != b.draft.Entry.BangMode || a.queuedDraftID != b.queuedDraftID || !slices.Equal(a.draft.Entry.InlinePastes, b.draft.Entry.InlinePastes) || len(a.attachments) != len(b.attachments) {
		return false
	}
	for idx, x := range a.attachments {
		y := b.attachments[idx]
		// Attachment bytes are immutable after admission. Compare identity so
		// normal typing does not scan megabytes of attached data.
		if x.FileName != y.FileName || x.MimeType != y.MimeType || x.ImagePath != y.ImagePath || x.InlineImagePlaceholder != y.InlineImagePlaceholder || len(x.Data) != len(y.Data) {
			return false
		}
		if len(x.Data) > 0 && &x.Data[0] != &y.Data[0] {
			return false
		}
	}
	return true
}

func (m *Model) syncComposerUndoBoundary() {
	if m.composerUndo.boundary != m.input.editBoundary {
		m.composerUndo = composerUndoState{boundary: m.input.editBoundary}
	}
}

// beginComposerEdit groups nested edits into one transaction. Only actual
// content changes enter history; navigation and layout changes do not.
func (m *Model) beginComposerEdit(typing bool) func() {
	if m.composerUndo.depth > 0 {
		return func() {}
	}
	m.syncComposerUndoBoundary()
	before := m.composerEditSnapshot()
	boundary := m.input.editBoundary
	m.composerUndo.depth++
	return func() {
		if boundary != m.input.editBoundary {
			m.composerUndo = composerUndoState{boundary: m.input.editBoundary}
			return
		}
		m.composerUndo.depth--
		after := m.composerEditSnapshot()
		if sameComposerContent(before, after) {
			m.composerUndo.typing = false
			return
		}
		now := time.Now()
		cursor := runeOffsetFromRowCol(before.draft.Entry.Display, before.draft.Row, before.draft.Col)
		merge := typing && m.composerUndo.typing && now.Sub(m.composerUndo.lastEdit) <= composerTypingWindow && cursor == m.composerUndo.lastCursor
		size := before.size()
		if size > composerUndoBytes {
			m.composerUndo = composerUndoState{boundary: boundary}
			return
		}
		if !merge || len(m.composerUndo.entries) == 0 {
			for len(m.composerUndo.entries) > 0 && (len(m.composerUndo.entries) >= composerUndoEntries || m.composerUndo.bytes+size > composerUndoBytes) {
				m.composerUndo.bytes -= m.composerUndo.entries[0].size()
				m.composerUndo.entries[0] = composerEditSnapshot{}
				m.composerUndo.entries = m.composerUndo.entries[1:]
			}
			m.composerUndo.entries = append(m.composerUndo.entries, before)
			m.composerUndo.bytes += size
		}
		m.composerUndo.typing = typing
		m.composerUndo.lastEdit = now
		m.composerUndo.lastCursor = runeOffsetFromRowCol(after.draft.Entry.Display, after.draft.Row, after.draft.Col)
	}
}

func (m *Model) undoComposerEdit() tea.Cmd {
	m.syncComposerUndoBoundary()
	if len(m.composerUndo.entries) == 0 {
		return nil
	}
	m.cancelClipboardAttachmentPaste()
	last := len(m.composerUndo.entries) - 1
	snapshot := m.composerUndo.entries[last]
	m.composerUndo.bytes -= snapshot.size()
	m.composerUndo.entries[last] = composerEditSnapshot{}
	m.composerUndo.entries = m.composerUndo.entries[:last]
	m.composerUndo.typing = false
	m.input.applyDraftSnapshot(snapshot.draft)
	m.composerUndo.boundary = m.input.editBoundary
	m.attachments = cloneAttachments(snapshot.attachments)
	m.editingQueuedDraftID = snapshot.queuedDraftID
	m.closeAtMention()
	m.slashCompleteSelected = 0
	m.input.syncHeight()
	m.recalcViewportSize()
	return nil
}
