package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/tui/terminaltext"
	uitheme "github.com/keakon/chord/internal/tui/theme"
)

// The compaction history layer is a foreground read-only browser, not a mode
// change. These tests pin the boundary between it and the live session: the
// live mode, the composer draft and the pending requests keep their owners,
// while presentation, focus and input are withheld until the reader comes
// back.

// newCompactionHistoryIsolationModel builds a model whose agent reports a
// session directory with one applied snapshot.
func newCompactionHistoryIsolationModel(t *testing.T) (*Model, *compactionHistorySessionAgent) {
	t.Helper()
	ApplyTheme(uitheme.Default())
	dir := compactionHistorySessionDir(t, 3)
	backend := &compactionHistorySessionAgent{sessionControlAgent: &sessionControlAgent{}, dir: dir}
	m := NewModelWithSize(backend, 100, 30)
	return &m, backend
}

func TestCompactionHistoryOpenKeepsLiveModeAndDraft(t *testing.T) {
	m, _ := newCompactionHistoryIsolationModel(t)
	m.mode = ModeInsert
	m.input.SetValue("unfinished draft")

	m.openCompactionHistory()

	if !m.compactionHistory.active {
		t.Fatal("the history layer did not open")
	}
	if m.mode != ModeInsert {
		t.Fatalf("live mode = %v, want ModeInsert: opening history must not switch mode", m.mode)
	}
	if got := m.input.Value(); got != "unfinished draft" {
		t.Fatalf("composer draft = %q, want the draft to survive opening history", got)
	}
}

func TestCompactionHistoryCloseRestoresDraftAndDropsState(t *testing.T) {
	m, _ := newCompactionHistoryIsolationModel(t)
	m.mode = ModeInsert
	m.input.SetValue("unfinished draft")
	m.openCompactionHistory()
	if m.compactionHistory.viewport == nil {
		t.Fatal("the history layer has no viewport of its own")
	}
	if m.input.Focused() {
		t.Fatal("the composer must lose focus while history owns the foreground")
	}

	cmd := m.closeCompactionHistory()
	if cmd != nil {
		// The blink command is produced on the main loop; running it here only
		// makes the focus transition observable to this test.
		_ = cmd()
	}
	if !m.input.Focused() {
		t.Fatal("closing history must hand focus back to the composer in ModeInsert")
	}

	if m.compactionHistory.active {
		t.Fatal("the history layer is still active after closing")
	}
	if m.compactionHistory.viewport != nil {
		t.Fatal("the history viewport was not released on close")
	}
	if m.compactionHistory.snapshots != nil || m.compactionHistory.sources != nil {
		t.Fatal("the history layer kept its snapshot data after closing")
	}
	if m.mode != ModeInsert || m.input.Value() != "unfinished draft" {
		t.Fatalf("live mode = %v, draft = %q; both must survive a round trip", m.mode, m.input.Value())
	}
}

func TestCompactionHistoryWithholdsPendingDialogs(t *testing.T) {
	m, _ := newCompactionHistoryIsolationModel(t)
	m.mode = ModeInsert
	m.pendingDialogs = append(m.pendingDialogs, pendingDialog{questionID: "q1", arrivedAt: time.Now()})
	m.openCompactionHistory()

	if cmd := m.tryPresentNextDialog(); cmd != nil {
		t.Fatal("a queued request must not be presented while history covers the screen")
	}
	if len(m.pendingDialogs) != 1 {
		t.Fatalf("pending dialogs = %d, want 1: the queue must stay intact", len(m.pendingDialogs))
	}
	m.finishDialog(ModeInsert)
	if len(m.pendingDialogs) != 1 {
		t.Fatalf("finishDialog drained the queue: pending dialogs = %d, want 1", len(m.pendingDialogs))
	}
	if m.mode != ModeInsert {
		t.Fatalf("mode = %v, want ModeInsert: hiding a dialog must not restore a hidden mode", m.mode)
	}
}

func TestCompactionHistoryWithholdsQuestionPresented(t *testing.T) {
	m, _ := newCompactionHistoryIsolationModel(t)
	m.mode = ModeQuestion
	m.question.request = &QuestionRequest{Item: tools.QuestionItem{
		Header:   "Choice",
		Question: "continue?",
		Options:  []tools.QuestionOption{{ID: "yes", Label: "Yes"}},
	}}
	m.question.requestID = "q1"
	m.question.visibleBodyHeight = 5
	m.questionRecords = map[string]agent.QuestionSnapshot{
		"q1": {
			ID:      "q1",
			Version: 1,
			Timer:   agent.QuestionTimerAwaiting,
			Item:    tools.QuestionItem{Question: "continue?"},
		},
	}

	// Baseline: without the layer this request really would report Presented.
	if cmd := m.confirmQuestionPresentation(); cmd == nil {
		t.Fatal("the baseline question state must be eligible for Presented")
	}

	m.openCompactionHistory()
	if cmd := m.confirmQuestionPresentation(); cmd != nil {
		t.Fatal("a question hidden behind the history layer must not report Presented")
	}
}

func TestCompactionHistoryKeysNeverReachTheLiveSession(t *testing.T) {
	m, backend := newCompactionHistoryIsolationModel(t)
	m.mode = ModeInsert
	m.input.SetValue("unfinished draft")
	m.openCompactionHistory()

	for _, key := range []tea.KeyMsg{
		tea.KeyPressMsg(tea.Key{Text: "x", Code: 'x'}),
		tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}),
		tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}),
	} {
		m.handleKeyMsg(key)
	}
	if got := m.input.Value(); got != "unfinished draft" {
		t.Fatalf("composer draft = %q, want it untouched by history keys", got)
	}
	if len(backend.submittedInputs) != 0 {
		t.Fatalf("%d inputs were submitted while browsing history", len(backend.submittedInputs))
	}

	// A paste must not reach the composer either.
	m.handleNonKeyInputMsg(tea.PasteMsg{Content: "pasted text"})
	if got := m.input.Value(); got != "unfinished draft" {
		t.Fatalf("composer draft = %q, want paste to be dropped", got)
	}
}

func TestCompactionHistoryCtrlCLeavesTheLayerOnly(t *testing.T) {
	m, _ := newCompactionHistoryIsolationModel(t)
	m.mode = ModeInsert
	m.openCompactionHistory()

	m.handleKeyMsg(tea.KeyPressMsg(tea.Key{Code: 3}))

	if m.compactionHistory.active {
		t.Fatal("Ctrl+C must leave the history layer")
	}
	if m.mode != ModeInsert {
		t.Fatalf("live mode = %v, want ModeInsert", m.mode)
	}
}

func TestCompactionHistorySearchKeysBelongToTheQuery(t *testing.T) {
	m, _ := newCompactionHistoryIsolationModel(t)
	m.openCompactionHistory()

	m.handleCompactionHistoryKey(tea.KeyPressMsg(tea.Key{Text: "/", Code: '/'}))
	if !m.compactionHistory.searching {
		t.Fatal("/ did not open the history search field")
	}
	m.handleCompactionHistoryKey(tea.KeyPressMsg(tea.Key{Text: "q", Code: 'q'}))
	if got := m.compactionHistory.searchInput.Value(); got != "q" {
		t.Fatalf("query = %q, want q: q is a query character in the search field", got)
	}
	for _, key := range []tea.KeyMsg{
		tea.KeyPressMsg(tea.Key{Code: tea.KeyLeft}),
		tea.KeyPressMsg(tea.Key{Code: tea.KeyRight}),
	} {
		m.handleCompactionHistoryKey(key)
	}
	if got := m.compactionHistory.searchInput.Value(); got != "q" {
		t.Fatalf("query = %q after arrow keys, want the text unchanged", got)
	}

	m.handleCompactionHistoryKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if m.compactionHistory.searching {
		t.Fatal("the first Esc must leave the search field")
	}
	if !m.compactionHistory.active {
		t.Fatal("the first Esc must not leave the history layer")
	}

	m.handleCompactionHistoryKey(tea.KeyPressMsg(tea.Key{Code: 3}))
	if m.compactionHistory.active {
		t.Fatal("Ctrl+C must leave the whole history layer from the search state")
	}
}

func TestCompactionHistoryDropsStaleAsyncResults(t *testing.T) {
	m, _ := newCompactionHistoryIsolationModel(t)
	m.openCompactionHistory()
	h := &m.compactionHistory
	h.snapshots = []recovery.CompactionHistorySnapshot{{
		Index: 3, JSONLName: "main.pre-compress-3.jsonl", StatusName: "history-3.status.json",
	}}
	h.loading = false

	for _, msg := range []tea.Msg{
		compactionHistoryScannedMsg{
			generation: h.generation - 1,
			snapshots:  []recovery.CompactionHistorySnapshot{{Index: 99}},
		},
		compactionHistoryLoadedMsg{generation: h.generation - 1, index: 99, messages: nil},
		compactionHistoryInstallMsg{generation: h.installGeneration - 1},
		compactionHistorySearchStepMsg{generation: h.searchGeneration - 1},
	} {
		m.handleCompactionHistoryMessage(msg)
	}

	if len(h.snapshots) != 1 || h.snapshots[0].Index != 3 {
		t.Fatalf("snapshots = %#v, want the original snapshot #3 only", h.snapshots)
	}
	if h.current != -1 {
		t.Fatalf("current = %d, want -1: a stale result must not select a snapshot", h.current)
	}
	if h.loading {
		t.Fatal("a stale result must not restart loading")
	}
}

func TestCompactionHistoryNoSnapshotsLeavesLiveSessionUntouched(t *testing.T) {
	m, _ := newCompactionHistoryIsolationModel(t)
	m.mode = ModeInsert
	m.input.SetValue("unfinished draft")
	m.openCompactionHistory()

	m.handleCompactionHistoryScanned(compactionHistoryScannedMsg{generation: m.compactionHistory.generation})

	if m.compactionHistory.active {
		t.Fatal("a session with no snapshot must not leave an empty viewer open")
	}
	if m.mode != ModeInsert {
		t.Fatalf("live mode = %v, want ModeInsert", m.mode)
	}
	if got := m.input.Value(); got != "unfinished draft" {
		t.Fatalf("composer draft = %q, want the draft preserved", got)
	}
}

func TestCompactionHistoryStatusBarHasNoRightSideEntry(t *testing.T) {
	m, _ := newCompactionHistoryIsolationModel(t)
	t.Cleanup(func() { _ = m.Close() })
	for _, history := range []bool{false, true, false} {
		if history {
			driveCompactionHistoryCmd(t, m, m.openCompactionHistory())
		} else {
			m.closeCompactionHistory()
		}
		for range 2 {
			m.View()
			status := terminaltext.StripANSI(m.renderStatusBar())
			if strings.Contains(status, "History") || strings.Contains(status, "Esc Live") {
				t.Fatalf("history=%t: status bar has a right-side entry: %q", history, status)
			}
			m.handleMouseMsg(tea.MouseClickMsg{X: m.width - 3, Y: m.layout.status.Min.Y, Button: tea.MouseLeft})
			if m.compactionHistoryActive() != history {
				t.Fatal("status bar click changed history visibility")
			}
		}
	}
}

// --- Resize / reflow -------------------------------------------------------
// A width change used to re-measure every card in one synchronous SetSize.
// These tests pin the replacement: the viewport is emptied, re-sized cheaply,
// and the same cards are re-installed in bounded batches.

// driveCompactionHistoryCmd runs a command chain until the history layer stops
// asking for another main-loop step.
func driveCompactionHistoryCmd(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	for i := 0; cmd != nil; i++ {
		if i >= 10000 {
			t.Fatalf("compaction history command chain did not settle: installed=%d pending=%d reflow=%v",
				m.compactionHistory.installedCount, len(m.compactionHistory.pendingBlocks), m.compactionHistory.reflowActive)
		}
		msg := cmd()
		next, handled := m.handleCompactionHistoryMessage(msg)
		if !handled {
			return
		}
		cmd = next
	}
}

// installedCompactionHistoryModel opens one snapshot with the given messages
// and drives its installation to completion.
func installedCompactionHistoryModel(t *testing.T, msgs []message.Message) *Model {
	t.Helper()
	ApplyTheme(uitheme.Default())
	dir := compactionHistorySessionDir(t, 3)
	backend := &compactionHistorySessionAgent{sessionControlAgent: &sessionControlAgent{}, dir: dir}
	m := NewModelWithSize(backend, 100, 30)
	m.mode = ModeNormal
	m.layout = m.generateLayout(m.width, m.height)
	m.openCompactionHistory()
	m.handleCompactionHistoryScanned(compactionHistoryScannedMsg{
		generation: m.compactionHistory.generation,
		snapshots: []recovery.CompactionHistorySnapshot{{
			Index: 3, JSONLName: "main.pre-compress-3.jsonl", StatusName: "history-3.status.json",
		}},
	})
	installCmd, handled := m.handleCompactionHistoryMessage(compactionHistoryLoadedMsg{
		generation: m.compactionHistory.generation,
		index:      3,
		messages:   msgs,
	})
	if !handled || installCmd == nil {
		t.Fatal("the snapshot did not schedule an installation")
	}
	driveCompactionHistoryCmd(t, &m, installCmd)
	if m.compactionHistory.reflowActive {
		t.Fatal("the initial install left a reflow active")
	}
	return &m
}

func reflowTestMessages(n int) []message.Message {
	msgs := make([]message.Message, 0, n)
	for i := range n {
		msgs = append(msgs, message.Message{
			Role:    "user",
			Content: fmt.Sprintf("message %d: a line long enough that a narrower viewport has to wrap it differently", i),
		})
	}
	return msgs
}

func TestCompactionHistoryReflowKeepsCardsAndRendersFirstFrame(t *testing.T) {
	m := installedCompactionHistoryModel(t, reflowTestMessages(120))
	h := &m.compactionHistory
	before := m.compactionHistoryBlocks()
	if len(before) == 0 {
		t.Fatal("the snapshot installed no cards")
	}
	wantIDs := make([]int, len(before))
	for i, b := range before {
		wantIDs[i] = b.ID
	}
	oldWidth := h.viewport.width

	m.width -= 20
	cmd := m.startCompactionHistoryReflow()
	if cmd == nil {
		t.Fatal("a width change did not start a reflow")
	}
	if !h.reflowActive {
		t.Fatal("the reflow state was not set")
	}
	if h.viewport.width == oldWidth {
		t.Fatal("the viewport was not re-sized")
	}
	if h.installedCount == 0 {
		t.Fatal("the first frame was left empty: no card was installed synchronously")
	}
	// The first frame must show real content, not a blank pane.
	frame := m.renderCompactionHistory(m.compactionHistoryArea())
	if strings.TrimSpace(terminaltext.StripANSI(frame)) == "" {
		t.Fatal("the first frame after a resize is blank")
	}
	if !strings.Contains(terminaltext.StripANSI(frame), "message 0") {
		t.Fatalf("the first frame does not show the first card:\n%s", frame)
	}

	driveCompactionHistoryCmd(t, m, cmd)
	if h.reflowActive {
		t.Fatal("the reflow never finished")
	}
	after := m.compactionHistoryBlocks()
	if len(after) != len(wantIDs) {
		t.Fatalf("cards = %d after reflow, want %d", len(after), len(wantIDs))
	}
	for i, b := range after {
		if b.ID != wantIDs[i] {
			t.Fatalf("card %d has ID %d, want %d: reflow changed the card order", i, b.ID, wantIDs[i])
		}
	}
	if h.progress != "" {
		t.Fatalf("progress text was not cleared: %q", h.progress)
	}
}

func TestCompactionHistoryReflowRestoresScrollAnchorByBlock(t *testing.T) {
	m := installedCompactionHistoryModel(t, reflowTestMessages(120))
	h := &m.compactionHistory
	h.viewport.ScrollDown(120)
	anchor := h.viewport.GetBlockAtOffset()
	if anchor == nil {
		t.Fatal("scrolling did not put a block under the offset")
	}
	wantID := anchor.ID

	m.width -= 24
	cmd := m.startCompactionHistoryReflow()
	driveCompactionHistoryCmd(t, m, cmd)

	if got := h.viewport.GetBlockAtOffset(); got == nil || got.ID != wantID {
		var id int
		if got != nil {
			id = got.ID
		}
		t.Fatalf("the reader landed on block %d after reflow, want %d", id, wantID)
	}
}

func TestCompactionHistoryResizeEventStartsReflow(t *testing.T) {
	m := installedCompactionHistoryModel(t, reflowTestMessages(120))
	// A large growth is applied immediately by handleWindowSizeUpdate, so the
	// same Update must also start the reflow.
	_, _ = m.Update(tea.WindowSizeMsg{Width: m.width + 30, Height: m.height})
	if !m.compactionHistory.reflowActive {
		t.Fatal("the resize branch of Update did not start a reflow")
	}
}

func TestCompactionHistoryNewSnapshotCancelsReflow(t *testing.T) {
	m := installedCompactionHistoryModel(t, reflowTestMessages(120))
	m.width -= 20
	cmd := m.startCompactionHistoryReflow()
	if cmd == nil || !m.compactionHistory.reflowActive {
		t.Fatal("the reflow did not start")
	}
	m.handleCompactionHistoryMessage(compactionHistoryLoadedMsg{
		generation: m.compactionHistory.generation,
		index:      3,
		messages:   reflowTestMessages(5),
	})
	if m.compactionHistory.reflowActive {
		t.Fatal("loading a new snapshot did not cancel the in-flight reflow")
	}
}

func TestCompactionHistoryDebouncedResizeStartsReflow(t *testing.T) {
	m := installedCompactionHistoryModel(t, reflowTestMessages(120))
	// A small shrink is debounced: it must not re-flow on the first event.
	_, _ = m.Update(tea.WindowSizeMsg{Width: m.width - 3, Height: m.height})
	if m.compactionHistory.reflowActive {
		t.Fatal("a small shrink re-flowed before the debounced apply")
	}
	_, _ = m.Update(applyResizeMsg{version: m.resizeVersion})
	if !m.compactionHistory.reflowActive {
		t.Fatal("the debounced resize did not start a reflow")
	}
}

// newCompactionHistoryOpenModel opens the browser over the snapshots with the
// given file numbers and processes the scan, leaving the newest snapshot
// pending. The tests deliver the loaded message themselves.
func newCompactionHistoryOpenModel(t *testing.T, indexes ...int) *Model {
	t.Helper()
	ApplyTheme(uitheme.Default())
	dir := compactionHistorySessionDir(t, indexes...)
	backend := &compactionHistorySessionAgent{sessionControlAgent: &sessionControlAgent{}, dir: dir}
	m := NewModelWithSize(backend, 100, 30)
	m.mode = ModeNormal
	scan := m.openCompactionHistory()
	m.handleCompactionHistoryScanned(scan().(compactionHistoryScannedMsg))
	return &m
}

func loadCompactionHistorySnapshot(m *Model, index int, body string) {
	m.handleCompactionHistoryLoaded(compactionHistoryLoadedMsg{
		generation: m.compactionHistory.generation,
		index:      index,
		messages:   []message.Message{{Role: "user", Content: body}},
	})
}

func TestCompactionHistoryNavigationTracksListPositionNotFileNumber(t *testing.T) {
	// Two snapshots with skipping file numbers 2 and 5: the newest one sits at
	// list position 1, and stepping back must reach position 0.
	m := newCompactionHistoryOpenModel(t, 2, 5)
	h := &m.compactionHistory
	if h.pendingIndex != 1 {
		t.Fatalf("pending snapshot = %d, want 1: the scan must start the newest snapshot", h.pendingIndex)
	}

	loadCompactionHistorySnapshot(m, 5, "newest snapshot body")
	title := m.compactionHistoryModeLabel() + " " + m.compactionHistoryStatusText()
	if !strings.Contains(title, "2/2") {
		t.Fatalf("title = %q, want it to report position 2 of 2", title)
	}
	if !strings.Contains(title, "snapshot #5") {
		t.Fatalf("title = %q, want it to report the file number 5", title)
	}

	cmd := m.stepCompactionHistorySnapshot(-1)
	if cmd == nil {
		t.Fatal("stepping to the earlier snapshot did not start a load: the file number was used as a list position")
	}
	if h.pendingIndex != 0 {
		t.Fatalf("pending snapshot = %d, want 0", h.pendingIndex)
	}

	loadCompactionHistorySnapshot(m, 2, "earliest snapshot body")
	title = m.compactionHistoryModeLabel() + " " + m.compactionHistoryStatusText()
	if !strings.Contains(title, "1/2") {
		t.Fatalf("title = %q, want it to report position 1 of 2", title)
	}
	if !strings.Contains(title, "snapshot #2") {
		t.Fatalf("title = %q, want it to report the file number 2", title)
	}
}

func TestCompactionHistoryReferencedSourceMarksTheStatus(t *testing.T) {
	m := newCompactionHistoryOpenModel(t, 2, 5)
	// Re-open through a checkpoint that names only archive 5, so the layer
	// opens it directly and must mark it in the title.
	m.closeCompactionHistory()
	scan := m.openCompactionHistoryWithSources([]message.CompactionArchiveRef{{
		Index: 5,
		Path:  "/tmp/session/history-5.md",
	}})
	m.handleCompactionHistoryScanned(scan().(compactionHistoryScannedMsg))
	h := &m.compactionHistory
	if h.sourceListActive {
		t.Fatal("a single verified archive must open directly, without a source list")
	}
	if h.referencedPosition != 1 {
		t.Fatalf("referenced position = %d, want 1", h.referencedPosition)
	}

	loadCompactionHistorySnapshot(m, 5, "referenced snapshot body")
	title := m.compactionHistoryStatusText()
	if !strings.Contains(title, "referenced") {
		t.Fatalf("title = %q, want the referenced snapshot to be marked", title)
	}
}

// --- Search over a snapshot that is still installing ------------------------
// A search submitted while the bounded install loop is still filling the
// viewport is deferred until the install finishes, so it never reports
// matches over a partial snapshot. That behavior is pinned by
// TestCompactionHistoryProgressShowsCardsAndSearchWaitsForTail.

// --- Viewport width at open -------------------------------------------------
// compactionHistoryViewportWidth is the single width source shared with the
// renderer and the resize branches, so opening the layer must size the
// viewport with it even on a terminal narrower than the old fallback.

func TestCompactionHistoryOpenUsesTheSingleViewportWidthSource(t *testing.T) {
	ApplyTheme(uitheme.Default())
	dir := compactionHistorySessionDir(t, 3)
	backend := &compactionHistorySessionAgent{sessionControlAgent: &sessionControlAgent{}, dir: dir}
	m := NewModelWithSize(backend, 18, 30)
	m.mode = ModeNormal

	m.openCompactionHistory()

	if m.compactionHistory.viewport == nil {
		t.Fatal("the history layer has no viewport of its own")
	}
	want := m.compactionHistoryViewportWidth()
	if got := m.compactionHistory.viewport.width; got != want {
		t.Fatalf("history viewport width = %d, want %d: the open path must use the single width source", got, want)
	}
}
