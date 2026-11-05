package tui

import (
	"context"
	"errors"
	"fmt"
	"image"
	"strings"
	"time"

	"github.com/keakon/bubbles/v2/textinput"
	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

// The compaction history layer is a read-only foreground browser over the
// pre-compaction snapshots this session left on disk. It never touches the
// live model context, never answers or cancels a request, and never changes
// the live mode: the layer owns its own viewport, selection, search and
// keyboard state, and the live session keeps running underneath it.

// compactionHistoryInstallChunk bounds how many cards one main-loop step
// installs into the history viewport. Installing a card measures its rendered
// height, so an unbounded install would freeze the UI on a large snapshot.
// The budget is sized against one 60 Hz frame (16 ms) measured on the 8000-card
// neutral snapshot in compaction_history_scale_test.go: 64 cards sat on the
// edge of the frame budget across runs, 16 keeps a single batch well inside it
// even when the machine is busy. Smaller batches cost more round trips but
// keep input responsive; the extra round trips do not add user-visible wait
// because the loop keeps serving input between batches.
const compactionHistoryInstallChunk = 16

// compactionHistorySearchChunk bounds how many cards one main-loop step scans
// while searching. The scan is resumable, so a new query or an exit cancels
// the remaining work instead of blocking the loop. 128 cards repeatedly cost
// ~21 ms per batch on the same snapshot; 24 keeps the worst observed batch
// around a third of the frame budget.
const compactionHistorySearchChunk = 24

// compactionHistoryState owns the read-only compaction history browsing layer.
//
// The layer is independent of the live mode on purpose: opening it must not
// clear an open file or image viewer, must not drop the composer draft, and
// must not make the Update defer treat the session as idle. Live mode, live
// requests and live terminal states keep their own owners; this state only
// describes what the history browser shows.
type compactionHistoryState struct {
	active       bool
	sessionDir   string
	sessionsRoot string

	// snapshots lists the applied snapshots of the session, ordered by file
	// number. The file number is the compaction attempt number, not the count
	// of successful compactions, so the two must never be mixed.
	snapshots []recovery.CompactionHistorySnapshot
	// issues records directory entries that look like snapshots but whose
	// status record could not be read. They are surfaced, not swallowed.
	issues   []recovery.CompactionHistoryScanIssue
	current  int
	loading  bool
	notice   string
	progress string

	// generation guards every asynchronous result: a result whose generation is
	// stale belongs to a superseded request and is dropped without touching the
	// view.
	generation uint64
	cancel     context.CancelFunc
	// The active load survives closing the browser until its result releases
	// the slot. Cancelling it never starts a second read alongside it.
	loadGeneration uint64
	pendingIndex   int // newest requested position, or -1 when no load is pending
	scanGeneration uint64
	scanCancel     context.CancelFunc
	scanPending    bool

	viewport          *Viewport
	pendingBlocks     []*Block
	installedCount    int
	installPending    bool
	installGeneration uint64

	// A width change re-wraps every card, so measuring all of them in one
	// SetSize would freeze the main loop for seconds on a large snapshot.
	// Instead the layer empties the viewport (which makes SetSize cheap) and
	// re-installs the same cards through the bounded install loop above. The
	// reader's place is kept by block identity, because line numbers change.
	reflowActive      bool
	reflowTargetWidth int
	reflowAnchorID    int

	search           SearchState
	searchInput      textinput.Model
	searching        bool
	searchQuery      string
	searchBlockIndex int
	searchLineOffset int
	searchGeneration uint64
	searchPending    bool
	// A layout rebuild keeps the selected match by identity without scrolling
	// away from the reader's independently restored viewport anchor.
	searchRestoreBlockID *int

	// sources are the archives a compaction checkpoint named when it opened
	// the layer. They are resolved against the session directory after the
	// scan, so every entry can say whether this session still holds the
	// matching local snapshot.
	sources []compactionHistorySource
	// pendingSources holds the references of a checkpoint that opened the
	// layer, before the scan resolved them.
	pendingSources []message.CompactionArchiveRef
	// sourceListActive is true while the layer shows the source list instead
	// of a snapshot.
	sourceListActive bool
	// sourceCursor is the selected entry of the source list.
	sourceCursor int
	// referencedPosition is the position of the snapshot the layer opened
	// from a checkpoint card, or -1 when opened by a navigation shortcut.
	// The status marks that snapshot so the reader can tell the
	// referenced source apart from the neighbours they can browse to.
	referencedPosition int

	focusedBlockID int
	selection      viewportSelection

	// pendingIntent remembers the newest interface-opening request that
	// arrived while the history covered the screen. The request is replayed
	// when the user comes back, so the agent is not left waiting for a
	// selector that never appeared.
	pendingIntent compactionHistoryIntent

	// area is the message rectangle in the last frame; the mouse
	// hit tests use it to map a screen position onto the layer.
	area image.Rectangle
}

// compactionHistoryIntentKind names an interface-opening request that was
// deferred because the history layer covered the screen.
type compactionHistoryIntentKind int

const (
	compactionHistoryIntentNone compactionHistoryIntentKind = iota
	compactionHistoryIntentModel
	compactionHistoryIntentRole
	compactionHistoryIntentMCP
	compactionHistoryIntentSkill
	compactionHistoryIntentSession
)

// compactionHistoryIntent holds the newest deferred interface-opening request.
type compactionHistoryIntent struct {
	kind       compactionHistoryIntentKind
	model      agent.ModelPoolSelectorTarget
	sessions   []agent.SessionSummary
	prefetched bool
}

// compactionHistoryScannedMsg reports the result of the background directory
// scan that lists the applied snapshots of the session.
type compactionHistoryScannedMsg struct {
	generation uint64
	snapshots  []recovery.CompactionHistorySnapshot
	issues     []recovery.CompactionHistoryScanIssue
	err        error
}

// compactionHistoryLoadedMsg carries a decoded snapshot. The messages are
// decoded off the main loop; the card conversion happens on the main loop
// because it reads the shared theme styles.
type compactionHistoryLoadedMsg struct {
	generation uint64
	index      int
	messages   []message.Message
	err        error
}

// compactionHistoryInstallMsg asks the main loop to install the next batch of
// cards into the history viewport.
type compactionHistoryInstallMsg struct {
	generation uint64
}

// compactionHistorySearchStepMsg asks the main loop to scan the next batch of
// cards for the active query.
type compactionHistorySearchStepMsg struct {
	generation uint64
}

// compactionHistoryActive reports whether the read-only history layer is on
// screen. While it is, the layer owns keyboard, mouse and paste input, and
// the live session must not present dialogs or take focus.
func (m *Model) compactionHistoryActive() bool {
	return m != nil && m.compactionHistory.active
}

// handleCompactionHistoryMessage consumes the layer's own asynchronous
// results. A message that belongs to a superseded generation is dropped.
func (m *Model) handleCompactionHistoryMessage(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case compactionHistoryScannedMsg:
		return m.handleCompactionHistoryScanned(msg), true
	case compactionHistoryLoadedMsg:
		return m.handleCompactionHistoryLoaded(msg), true
	case compactionHistoryInstallMsg:
		return m.handleCompactionHistoryInstall(msg), true
	case compactionHistorySearchStepMsg:
		return m.handleCompactionHistorySearchStep(msg), true
	}
	return nil, false
}

// compactionHistoryActionFires keeps history navigation out of searches and
// dialogs, where Alt+arrows can belong to text editing.
func (m *Model) compactionHistoryActionFires(msg tea.KeyMsg) bool {
	return (m.mode == ModeNormal || m.mode == ModeInsert) &&
		keyMatches(msg.String(), m.keyMap.CompactionHistory)
}

// compactionHistorySource resolves the session directory that holds the
// snapshots and the root it must live under. The root is optional: when the
// agent cannot report it, the session directory itself is the boundary and the
// sealed-read rules still apply.
func (m *Model) compactionHistorySource() (dir string, root string) {
	if m == nil || m.agent == nil {
		return "", ""
	}
	provider, ok := m.agent.(interface{ SessionDir() string })
	if !ok {
		return "", ""
	}
	dir = strings.TrimSpace(provider.SessionDir())
	if dir == "" {
		return "", ""
	}
	if rootProvider, ok := m.agent.(interface{ SessionRoot() string }); ok {
		root = strings.TrimSpace(rootProvider.SessionRoot())
	}
	return dir, root
}

// openCompactionHistory opens the read-only history browser on the most recent
// applied snapshot of the current session.
func (m *Model) openCompactionHistory() tea.Cmd {
	return m.openCompactionHistoryWithSources(nil)
}

// openCompactionHistoryWithSources opens the read-only history browser.
//
// When refs is non-empty the layer was opened from a compaction checkpoint's
// card, and it resolves those references instead of the newest snapshot: the
// checkpoint names the archives it refers to, and handing the reader a
// different one would misreport where the history came from.
func (m *Model) openCompactionHistoryWithSources(refs []message.CompactionArchiveRef) tea.Cmd {
	if m.compactionHistory.active {
		return nil
	}
	dir, root := m.compactionHistorySource()
	if dir == "" {
		return m.enqueueToast("This session has no readable history directory", "warn")
	}
	h := &m.compactionHistory
	h.active = true
	h.sessionDir = dir
	h.sessionsRoot = root
	h.snapshots = nil
	h.issues = nil
	h.current = -1
	h.loading = true
	h.notice = ""
	h.progress = ""
	h.pendingIndex = -1
	h.focusedBlockID = -1
	h.sources = nil
	h.pendingSources = refs
	h.sourceListActive = false
	h.sourceCursor = 0
	h.referencedPosition = -1
	h.generation++
	h.scanPending = true
	h.installGeneration++
	h.searchGeneration++
	h.searching = false
	h.searchQuery = ""
	h.searchBlockIndex = 0
	h.searchLineOffset = 0
	h.searchPending = false
	h.searchRestoreBlockID = nil
	ClearSearch(&h.search)
	h.searchInput = newCompactionHistorySearchInput()
	h.viewport = NewViewport(m.compactionHistoryViewportWidth(), m.compactionHistoryContentHeight())
	h.viewport.SetSticky(false)
	// The composer keeps its draft and selection; only its focus moves to the
	// history layer, and it is restored when the layer closes.
	m.input.Suspend()
	m.ime.mu.Lock()
	m.ime.seq++
	m.ime.pending = false
	m.ime.pendingTarget = ""
	m.ime.mu.Unlock()
	m.invalidateDrawCaches()
	m.imageViewerCleanup = tea.Sequence(m.imageViewerCleanup, m.suspendHistoryImages())
	return m.startCompactionHistoryScan()
}

// closeCompactionHistory leaves the history layer. The live session keeps
// whatever it had: the draft, the pending requests and any dialog that is
// still valid. Nothing here answers, rejects or cancels a request.
func (m *Model) closeCompactionHistory() tea.Cmd {
	h := &m.compactionHistory
	if !h.active {
		return nil
	}
	intent := h.pendingIntent
	_ = m.disposeCompactionHistory()
	m.compactionHistory.pendingIntent = intent
	m.invalidateDrawCaches()
	var cmds []tea.Cmd
	if m.mode == ModeInsert {
		cmds = append(cmds, m.input.Focus())
	}
	if next := m.tryPresentNextDialog(); next != nil {
		cmds = append(cmds, next)
	}
	cmds = append(cmds, m.tryReplayCompactionHistoryIntent())
	if m.mode == ModeInsert {
		m.runIMERestoreIfNeeded()
	} else {
		m.reapplyIMEForCurrentModeOnFocus()
	}
	cmds = append(cmds, m.restoreHistoryImages())
	return tea.Batch(cmds...)
}

// disposeCompactionHistory releases browser resources without reopening any
// interface from the outgoing session. In-flight commands retain their slots
// until their cancelled results return.
func (m *Model) disposeCompactionHistory() error {
	h := &m.compactionHistory
	h.generation++
	h.installGeneration++
	h.searchGeneration++
	if h.cancel != nil {
		h.cancel()
	}
	if h.scanCancel != nil {
		h.scanCancel()
	}
	var err error
	if h.viewport != nil {
		err = h.viewport.Close()
	}
	m.compactionHistory = compactionHistoryState{
		generation:        h.generation,
		installGeneration: h.installGeneration,
		searchGeneration:  h.searchGeneration,
		loadGeneration:    h.loadGeneration,
		cancel:            h.cancel,
		scanGeneration:    h.scanGeneration,
		scanCancel:        h.scanCancel,
		pendingIndex:      -1,
	}
	return err
}

func (m *Model) tryReplayCompactionHistoryIntent() tea.Cmd {
	h := &m.compactionHistory
	if h.active || h.pendingIntent.kind == compactionHistoryIntentNone ||
		m.dialogActive() || m.interactionSuppressed() ||
		(m.mode != ModeInsert && m.mode != ModeNormal) {
		return nil
	}
	intent := h.pendingIntent
	h.pendingIntent = compactionHistoryIntent{}
	return m.replayCompactionHistoryIntent(intent)
}

// replayCompactionHistoryIntent reopens the interface a deferred request asked
// for.
func (m *Model) replayCompactionHistoryIntent(intent compactionHistoryIntent) tea.Cmd {
	switch intent.kind {
	case compactionHistoryIntentModel:
		return m.openModelSelectFor(intent.model)
	case compactionHistoryIntentRole:
		m.openRoleSelect()
	case compactionHistoryIntentMCP:
		m.openMCPSelect()
	case compactionHistoryIntentSkill:
		m.openSkillSelect()
	case compactionHistoryIntentSession:
		return m.openSessionSelect(intent.sessions, intent.prefetched)
	}
	return nil
}

// startCompactionHistoryScan lists the applied snapshots of the session in the
// background: the directory scan must not block the main loop.
func (m *Model) startCompactionHistoryScan() tea.Cmd {
	h := &m.compactionHistory
	if !h.active || !h.scanPending || h.scanGeneration != 0 {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.scanCancel = cancel
	generation := h.generation
	h.scanGeneration = generation
	h.scanPending = false
	dir := h.sessionDir
	root := h.sessionsRoot
	return func() tea.Msg {
		snapshots, issues, err := recovery.ScanAppliedCompactionHistory(ctx, root, dir)
		return compactionHistoryScannedMsg{generation: generation, snapshots: snapshots, issues: issues, err: err}
	}
}

// handleCompactionHistoryScanned installs the snapshot list and starts loading
// the newest snapshot.
func (m *Model) handleCompactionHistoryScanned(msg compactionHistoryScannedMsg) tea.Cmd {
	h := &m.compactionHistory
	if h.scanGeneration != 0 && h.scanGeneration == msg.generation {
		h.scanCancel()
		h.scanCancel = nil
		h.scanGeneration = 0
	}
	if !h.active || msg.generation != h.generation {
		return m.startCompactionHistoryScan()
	}
	if msg.err != nil {
		notice := describeCompactionHistoryError(msg.err)
		cmd := m.closeCompactionHistory()
		return tea.Batch(cmd, m.enqueueToast(notice, "warn"))
	}
	h.snapshots = msg.snapshots
	h.issues = msg.issues
	h.loading = false
	if len(h.pendingSources) > 0 {
		// The checkpoint named its sources; the reader picks among them
		// instead of being handed the newest snapshot of the session.
		return m.resolveCompactionHistorySources()
	}
	if len(h.snapshots) == 0 {
		if len(h.issues) > 0 {
			notice := describeCompactionHistoryError(h.issues[0].Err)
			cmd := m.closeCompactionHistory()
			return tea.Batch(cmd, m.enqueueToast(notice, "warn"))
		}
		// Nothing to browse: leave the live session exactly as it was and say
		// so, instead of opening an empty viewer.
		notice := compactionHistoryEmptyNotice(h.issues)
		cmd := m.closeCompactionHistory()
		return tea.Batch(cmd, m.enqueueToast(notice, "info"))
	}
	return m.requestCompactionHistorySnapshot(len(h.snapshots) - 1)
}

// requestCompactionHistorySnapshot starts loading the snapshot at the given
// position in the snapshot list. Only one load runs at a time; a newer request
// replaces the pending target and the running load is cancelled.
func (m *Model) requestCompactionHistorySnapshot(position int) tea.Cmd {
	h := &m.compactionHistory
	if !h.active || position < 0 || position >= len(h.snapshots) {
		return nil
	}
	h.pendingIndex = position
	h.generation++
	h.searchGeneration++
	h.searchPending = false
	h.loading = true
	h.progress = ""
	if h.cancel != nil {
		h.cancel()
	}
	m.invalidateDrawCaches()
	return m.startCompactionHistoryLoad()
}

// startCompactionHistoryLoad reads and decodes one sealed snapshot off the main
// loop. The read is cancellable, and a stale result is dropped by generation.
func (m *Model) startCompactionHistoryLoad() tea.Cmd {
	h := &m.compactionHistory
	if !h.active || h.loadGeneration != 0 || h.pendingIndex < 0 {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	generation := h.generation
	h.loadGeneration = generation
	index := h.snapshots[h.pendingIndex].Index
	dir := h.sessionDir
	root := h.sessionsRoot
	return func() tea.Msg {
		msgs, err := recovery.LoadAppliedCompactionSnapshot(ctx, root, dir, index)
		if err != nil {
			return compactionHistoryLoadedMsg{generation: generation, index: index, err: err}
		}
		return compactionHistoryLoadedMsg{generation: generation, index: index, messages: msgs}
	}
}

// handleCompactionHistoryLoaded converts the decoded snapshot into cards and
// starts the bounded installation of those cards.
func (m *Model) handleCompactionHistoryLoaded(msg compactionHistoryLoadedMsg) tea.Cmd {
	h := &m.compactionHistory
	if h.loadGeneration != 0 && h.loadGeneration == msg.generation {
		h.cancel()
		h.cancel = nil
		h.loadGeneration = 0
	}
	if !h.active || msg.generation != h.generation {
		return m.startCompactionHistoryLoad()
	}
	h.loading = false
	h.pendingIndex = -1
	if msg.err != nil {
		if h.current < 0 {
			notice := describeCompactionHistoryError(msg.err)
			cmd := m.closeCompactionHistory()
			return tea.Batch(cmd, m.enqueueToast(notice, "warn"))
		}
		if !errors.Is(msg.err, context.Canceled) {
			h.notice = describeCompactionHistoryError(msg.err)
		}
		m.invalidateDrawCaches()
		return m.startCompactionHistoryReflow()
	}
	position := -1
	for i, snapshot := range h.snapshots {
		if snapshot.Index == msg.index {
			position = i
			break
		}
	}
	if position < 0 {
		return nil
	}
	// A new snapshot supersedes any in-flight reflow: the cards it installs
	// belong to the new snapshot, not the one being re-wrapped.
	h.reflowActive = false
	h.reflowTargetWidth = 0
	h.reflowAnchorID = -1
	nextID := 0
	blocks := messagesToBlocks(msg.messages, &nextID)
	for _, block := range blocks {
		block.prepareHistoryAttachments()
	}
	// The decoded records are not needed anymore: the cards own their content.
	msg.messages = nil
	h.current = position
	h.focusedBlockID = -1
	h.selection = viewportSelection{}
	h.pendingBlocks = blocks
	h.installedCount = 0
	h.searchGeneration++
	h.searchBlockIndex = 0
	h.searchLineOffset = 0
	h.searchPending = false
	h.searchQuery = ""
	h.searchRestoreBlockID = nil
	ClearSearch(&h.search)
	h.viewport.ReplaceBlocks(nil)
	h.viewport.SetSize(m.compactionHistoryViewportWidth(), m.compactionHistoryContentHeight())
	h.viewport.ScrollToTop()
	m.invalidateDrawCaches()
	return m.scheduleCompactionHistoryInstall()
}

// startCompactionHistoryReflow reacts to a terminal width change without
// synchronously re-measuring the whole snapshot. It is called from the resize
// branches of Update, where a command can actually be returned.
//
// The old viewport is emptied first: recalcTotalLines then has no cards to
// measure, so SetSize at the new width is O(1). The saved cards are re-installed
// afterwards in the same bounded batches as a fresh snapshot; each batch only
// renders the newly added cards because the already-installed ones keep their
// new-width line cache. Live viewports never take this path.
// compactionHistoryViewportWidth is the single source of truth for the width
// of the history viewport. The resize branches of Update and the renderer must
// agree exactly: if they disagreed, the first render after a reflow would call
// SetSize again and re-measure the whole snapshot synchronously.
func (m *Model) compactionHistoryViewportWidth() int {
	return max(m.compactionHistoryArea().Dx(), 1)
}

func (m *Model) startCompactionHistoryReflow() tea.Cmd {
	h := &m.compactionHistory
	if !h.active || h.viewport == nil || h.sourceListActive {
		return nil
	}
	targetWidth := m.compactionHistoryViewportWidth()
	if targetWidth == h.viewport.width {
		return nil
	}
	if h.reflowActive && h.reflowTargetWidth == targetWidth {
		return nil
	}
	blocks := h.pendingBlocks
	if blocks == nil {
		blocks = h.viewport.visibleBlocks()
	}
	if len(blocks) == 0 {
		h.viewport.SetSize(targetWidth, m.compactionHistoryContentHeight())
		return nil
	}

	// Line numbers are meaningless after re-wrapping; keep the block the reader
	// was on and look up its new offset once the install finishes.
	if !h.reflowActive {
		h.reflowAnchorID = -1
		if anchor := h.viewport.GetBlockAtOffset(); anchor != nil {
			h.reflowAnchorID = anchor.ID
		}
	}

	// Supersede any in-flight install or search before reusing their state.
	h.installGeneration++
	h.searchGeneration++
	h.installPending = false
	h.searchPending = false
	h.searchBlockIndex = 0
	h.searchLineOffset = 0
	if match, ok := h.search.CurrentMatch(); ok {
		h.searchRestoreBlockID = new(match.BlockID)
	}
	ClearSearch(&h.search)
	h.selection = viewportSelection{}

	h.pendingBlocks = blocks
	h.installedCount = 0
	h.reflowActive = true
	h.reflowTargetWidth = targetWidth

	// ReplaceBlocks drops each card's viewport cache, so re-installing renders
	// them at the new width instead of reusing old-width line counts.
	h.viewport.ReplaceBlocks(nil)
	h.viewport.SetSize(targetWidth, m.compactionHistoryContentHeight())
	h.viewport.ScrollToTop()

	// Give the first frame real content. Without this the reader would stare
	// at an empty pane until the first asynchronous batch arrives; one bounded
	// batch costs the same as an ordinary install step (well under a frame).
	first := min(compactionHistoryInstallChunk, len(blocks))
	h.viewport.AppendBlocks(blocks[:first])
	h.installedCount = first
	h.progress = ""
	m.invalidateDrawCaches()
	return m.scheduleCompactionHistoryInstall()
}

// scheduleCompactionHistoryInstall queues the next installation batch.
func (m *Model) scheduleCompactionHistoryInstall() tea.Cmd {
	h := &m.compactionHistory
	h.installGeneration++
	generation := h.installGeneration
	h.installPending = true
	return tickCmd(0, func(time.Time) tea.Msg {
		return compactionHistoryInstallMsg{generation: generation}
	})
}

// handleCompactionHistoryInstall installs the next batch of cards. Each batch
// is bounded so the main loop stays responsive while a large snapshot is
// prepared.
func (m *Model) handleCompactionHistoryInstall(msg compactionHistoryInstallMsg) tea.Cmd {
	h := &m.compactionHistory
	if !h.active || msg.generation != h.installGeneration {
		return nil
	}
	h.installPending = false
	blocks := h.pendingBlocks
	end := min(h.installedCount+compactionHistoryInstallChunk, len(blocks))
	if h.installedCount < end {
		h.viewport.AppendBlocks(blocks[h.installedCount:end])
		h.installedCount = end
	}
	if h.installedCount < len(blocks) {
		if !h.reflowActive {
			h.progress = fmt.Sprintf("Preparing history cards %d/%d", h.installedCount, len(blocks))
		}
		m.invalidateDrawCaches()
		return m.scheduleCompactionHistoryInstall()
	}
	h.progress = ""
	h.pendingBlocks = nil
	if h.reflowActive {
		h.reflowActive = false
		h.reflowTargetWidth = 0
		// Restore the reader's place by the block they were on. The offset is
		// clamped because the re-wrapped snapshot has a different total height.
		if h.reflowAnchorID >= 0 {
			if line, ok := h.viewport.LineOffsetForBlockID(h.reflowAnchorID); ok {
				h.viewport.offset = line
				h.viewport.clampOffset()
			}
		}
		h.reflowAnchorID = -1
	}
	m.invalidateDrawCaches()
	// Resume a query deferred by installation, or rebuild its coordinates
	// after reflow without replacing the saved search match.
	if h.searchQuery != "" {
		return m.scanCompactionHistorySearch(h.searchQuery)
	}
	return nil
}

// compactionHistoryBlocks returns the cards of the displayed snapshot.
func (m *Model) compactionHistoryBlocks() []*Block {
	h := &m.compactionHistory
	if h.viewport == nil {
		return nil
	}
	return h.viewport.visibleBlocks()
}

// startCompactionHistorySearch begins a scan for the query. The scan runs in
// bounded main-loop steps and is superseded by a newer query.
func (m *Model) startCompactionHistorySearch(query string) tea.Cmd {
	m.compactionHistory.searchRestoreBlockID = nil
	return m.scanCompactionHistorySearch(query)
}

func (m *Model) scanCompactionHistorySearch(query string) tea.Cmd {
	h := &m.compactionHistory
	h.searchGeneration++
	h.searchQuery = query
	h.searchBlockIndex = 0
	h.searchLineOffset = 0
	h.searchPending = false
	h.search.Matches = nil
	h.search.Current = -1
	h.search.Query = query
	h.search.Active = query != ""
	if query == "" || h.installPending || h.reflowActive {
		return nil
	}
	return m.scheduleCompactionHistorySearchStep()
}

// scheduleCompactionHistorySearchStep queues the next search batch.
func (m *Model) scheduleCompactionHistorySearchStep() tea.Cmd {
	h := &m.compactionHistory
	generation := h.searchGeneration
	h.searchPending = true
	return tickCmd(0, func(time.Time) tea.Msg {
		return compactionHistorySearchStepMsg{generation: generation}
	})
}

// handleCompactionHistorySearchStep scans the next batch of cards for the
// active query.
func (m *Model) handleCompactionHistorySearchStep(msg compactionHistorySearchStepMsg) tea.Cmd {
	h := &m.compactionHistory
	if !h.active || msg.generation != h.searchGeneration {
		return nil
	}
	h.searchPending = false
	blocks := m.compactionHistoryBlocks()
	width := h.viewport.width
	end := min(h.searchBlockIndex+compactionHistorySearchChunk, len(blocks))
	for i := h.searchBlockIndex; i < end; i++ {
		matches, lines := findMatchesInBlockAtWidth(blocks[i], i, h.searchLineOffset, h.searchQuery, width)
		h.search.Matches = append(h.search.Matches, matches...)
		h.searchLineOffset += lines
	}
	h.searchBlockIndex = end
	if h.searchBlockIndex < len(blocks) {
		return m.scheduleCompactionHistorySearchStep()
	}
	if len(h.search.Matches) > 0 {
		h.search.Current = 0
		if h.searchRestoreBlockID != nil {
			for i, match := range h.search.Matches {
				if match.BlockID == *h.searchRestoreBlockID {
					h.search.Current = i
					break
				}
			}
		} else {
			m.scrollCompactionHistoryToMatch(h.search.Matches[0])
		}
	}
	h.searchRestoreBlockID = nil
	m.invalidateDrawCaches()
	return nil
}

// cancelCompactionHistorySearch drops the active query and its results.
func (m *Model) cancelCompactionHistorySearch() {
	h := &m.compactionHistory
	h.searchGeneration++
	h.searchPending = false
	h.searchQuery = ""
	h.searchRestoreBlockID = nil
	ClearSearch(&h.search)
	m.invalidateDrawCaches()
}

// scrollCompactionHistoryToMatch moves the history viewport to a match.
func (m *Model) scrollCompactionHistoryToMatch(match MatchPosition) {
	h := &m.compactionHistory
	if h.viewport == nil {
		return
	}
	offset := match.LineOffset + match.InnerOffset
	if match.InnerOffset > 0 {
		offset -= min(match.InnerOffset, max(0, h.viewport.height/3))
	}
	h.viewport.offset = max(offset, 0)
	h.viewport.clampOffset()
	h.viewport.SetSticky(h.viewport.atBottom())
	if match.BlockID >= 0 {
		m.setCompactionHistoryFocus(match.BlockID)
	}
}

// stepCompactionHistoryMatch moves to the next or previous match.
func (m *Model) stepCompactionHistoryMatch(forward bool) {
	h := &m.compactionHistory
	if len(h.search.Matches) == 0 {
		return
	}
	var match MatchPosition
	var ok bool
	if forward {
		match, ok = NextMatch(&h.search)
	} else {
		match, ok = PrevMatch(&h.search)
	}
	if !ok {
		return
	}
	m.scrollCompactionHistoryToMatch(match)
	m.invalidateDrawCaches()
}

// describeCompactionHistoryError turns a snapshot failure into a message that
// names the source, without collapsing different failures into one sentence.
func describeCompactionHistoryError(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, recovery.ErrCompactionSnapshotEmpty):
		return "The snapshot is empty; there is no history to show: " + err.Error()
	case errors.Is(err, recovery.ErrCompactionSnapshotParse):
		return "The snapshot is damaged; the history cannot be read in full: " + err.Error()
	case errors.Is(err, recovery.ErrCompactionSnapshotInvalid):
		return "The snapshot path is invalid or was replaced; refusing to read it: " + err.Error()
	case errors.Is(err, recovery.ErrCompactionSnapshotUnavailable):
		return "That snapshot is not available (never applied, or cleaned up): " + err.Error()
	case errors.Is(err, recovery.ErrCompactionSnapshotRead):
		return "Failed to read the snapshot: " + err.Error()
	default:
		return "Failed to read the snapshot: " + err.Error()
	}
}

// compactionHistoryEmptyNotice explains why no snapshot is available.
func compactionHistoryEmptyNotice(issues []recovery.CompactionHistoryScanIssue) string {
	if len(issues) == 0 {
		return "This session has no compaction history"
	}
	return fmt.Sprintf("This session has no compaction history (%d snapshot status records are damaged)", len(issues))
}

// newCompactionHistorySearchInput builds the query field of the history layer.
func newCompactionHistorySearchInput() textinput.Model {
	ti := textinput.New()
	ti.Prompt = "/"
	ti.Placeholder = "search this snapshot"
	ti.CharLimit = 256
	ti.Focus()
	return ti
}
