package tui

import (
	"fmt"
	"image"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/keakon/bubbletea/v2"
	uv "github.com/keakon/ultraviolet"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

func TestCompactionHistoryCloseReleasesSpill(t *testing.T) {
	m := installedCompactionHistoryModel(t, reflowTestMessages(2))
	t.Cleanup(func() { _ = m.Close() })
	store := m.compactionHistory.viewport.spill
	if store == nil {
		t.Fatal("fixture has no history spill store")
	}
	path := store.path
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("Model.Close left history spill file: %v", err)
	}
}

func TestCompactionHistoryInvalidSourcesReportFailure(t *testing.T) {
	m, _ := newCompactionHistoryIsolationModel(t)
	t.Cleanup(func() { _ = m.Close() })
	m.openCompactionHistory()
	m.handleCompactionHistoryScanned(compactionHistoryScannedMsg{
		generation: m.compactionHistory.generation,
		issues:     []recovery.CompactionHistoryScanIssue{{Index: 3, Err: recovery.ErrCompactionSnapshotInvalid}},
	})
	if m.compactionHistory.active || m.activeToast == nil || m.activeToast.Level != "warn" ||
		!strings.Contains(m.activeToast.Message, recovery.ErrCompactionSnapshotInvalid.Error()) {
		t.Fatal("invalid history was reported as no history")
	}
}

func TestCompactionHistoryDrawCoversDialogs(t *testing.T) {
	m, _ := newCompactionHistoryIsolationModel(t)
	t.Cleanup(func() { _ = m.Close() })
	m.mode = ModeConfirm
	m.confirm.request = &ConfirmRequest{RequestID: "request-1", ToolName: tools.NameShell}
	m.layout = m.generateLayout(m.width, m.height)
	m.openCompactionHistory()
	if !m.compactionHistoryActive() {
		t.Fatal("history did not open above confirmation")
	}
	area := image.Rect(0, 0, m.width, m.height)
	scr := uv.NewScreenBuffer(m.width, m.height)
	uv.NewStyledString("LIVE DIALOG").Draw(scr, image.Rect(0, 15, m.width, 16))
	m.Draw(scr, area)
	if cell := scr.CellAt(0, 15); cell != nil && cell.Content == "L" {
		t.Fatal("history left a live dialog visible")
	}
}

func TestCompactionHistoryKeepsViewersOwnedByLiveMode(t *testing.T) {
	for _, mode := range []Mode{ModeFileViewer, ModeImageViewer} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			m, _ := newCompactionHistoryIsolationModel(t)
			t.Cleanup(func() { _ = m.Close() })
			m.mode = mode
			if mode == ModeFileViewer {
				m.openDeliveryFileList([]message.DeliveredFile{{ID: "a"}, {ID: "b"}, {ID: "c"}})
				m.fileViewer.selected, m.fileViewer.offset = 2, 17
			} else {
				m.imageViewer = imageViewerState{Open: true, Owner: m.imageViewerOwner(), Index: 2, Cursor: 3, Error: "sample preview"}
			}
			m.openCompactionHistory()
			m.Update(tea.FocusMsg{})
			m.closeCompactionHistory()
			if m.mode != mode {
				t.Fatal("history changed live viewer mode")
			}
			if mode == ModeFileViewer && (!m.fileViewer.open || m.fileViewer.selected != 2 || m.fileViewer.offset != 17) {
				t.Fatal("history reset file viewer position")
			}
			if mode == ModeImageViewer && (!m.imageViewer.Open || m.imageViewer.Index != 2 || m.imageViewer.Cursor != 3) {
				t.Fatal("history reset image viewer selection")
			}
		})
	}
}

func TestCompactionHistoryBlocksLiveImageResults(t *testing.T) {
	m := inlineImageTestModel(t, ImageBackendITerm2)
	t.Cleanup(func() { _ = m.Close() })
	m.agent = &compactionHistorySessionAgent{sessionControlAgent: &sessionControlAgent{}, dir: compactionHistorySessionDir(t, 3)}
	cmd := m.imageProtocolCmd()
	if cmd == nil {
		t.Fatal("fixture did not prepare an inline image")
	}
	loaded := cmd().(inlineImagesLoadedMsg)
	m.openCompactionHistory()
	if send := m.handleInlineImagesLoaded(loaded); send != nil {
		if _, ok := send().(tea.RawMsg); ok {
			t.Fatal("live image was sent over history")
		}
	}
}

func TestCompactionHistoryKeepsAttachmentsAsMetadata(t *testing.T) {
	fixture := inlineImageTestModel(t, ImageBackendKitty)
	t.Cleanup(func() { _ = fixture.Close() })
	m, _ := newCompactionHistoryIsolationModel(t)
	t.Cleanup(func() { _ = m.Close() })
	m.openCompactionHistory()
	m.compactionHistory.snapshots = []recovery.CompactionHistorySnapshot{{Index: 3}}
	setCurrentTerminalImageCapabilities(TerminalImageCapabilities{Backend: ImageBackendKitty, SupportsInline: true})
	install := m.handleCompactionHistoryLoaded(compactionHistoryLoadedMsg{
		generation: m.compactionHistory.generation, index: 3,
		messages: []message.Message{{
			Role: message.RoleUser,
			Parts: []message.ContentPart{{
				Type: message.ContentPartImage, FileName: "sample.jpg", MimeType: "image/jpeg",
				Data: fixture.viewport.blocks[0].ImageParts[0].Data,
			}},
		}},
	})
	driveCompactionHistoryCmd(t, m, install)
	part := m.compactionHistoryBlocks()[0].ImageParts[0]
	if len(part.Data) != 0 || part.RenderImageID != 0 || !part.MetadataOnly {
		t.Fatal("history attachment retained data or generated a terminal image")
	}
}

func TestCompactionHistorySerializesScansAcrossReopen(t *testing.T) {
	m, _ := newCompactionHistoryIsolationModel(t)
	t.Cleanup(func() { _ = m.Close() })
	first := m.openCompactionHistory()
	m.closeCompactionHistory()
	if cmd := m.openCompactionHistory(); cmd != nil {
		t.Fatal("reopen started a scan before cancellation settled")
	}
	next := m.handleCompactionHistoryScanned(first().(compactionHistoryScannedMsg))
	if next == nil || len(m.compactionHistory.snapshots) != 0 {
		t.Fatal("cancelled scan installed data or failed to start latest scan")
	}
	driveCompactionHistoryCmd(t, m, next)
	if m.compactionHistory.current != 0 || m.compactionHistory.loading {
		t.Fatal("latest scan did not load the snapshot")
	}
}

func TestCompactionHistoryDefersSelectorUntilDialogEnds(t *testing.T) {
	m, _ := newCompactionHistoryIsolationModel(t)
	t.Cleanup(func() { _ = m.Close() })
	m.mode = ModeNormal
	m.handleConfirmRequest(confirmRequestMsg{request: ConfirmRequest{ToolName: tools.NameShell, RequestID: "request-1"}})
	m.openCompactionHistory()
	m.compactionHistory.pendingIntent = compactionHistoryIntent{kind: compactionHistoryIntentSession}
	m.closeCompactionHistory()
	if m.mode != ModeConfirm || m.compactionHistory.pendingIntent.kind != compactionHistoryIntentSession {
		t.Fatal("returning from history displaced a live confirmation")
	}
	m.resolveConfirm(ConfirmResult{Action: ConfirmDeny})
	m.Update(tea.FocusMsg{})
	if m.mode != ModeSessionSelect || m.compactionHistory.pendingIntent.kind != compactionHistoryIntentNone {
		t.Fatal("deferred selector did not resume after the confirmation ended")
	}
}

func TestCompactionHistorySessionSwitchDiscardsSelector(t *testing.T) {
	m, _ := newCompactionHistoryIsolationModel(t)
	t.Cleanup(func() { _ = m.Close() })
	m.mode = ModeNormal
	m.openCompactionHistory()
	m.compactionHistory.pendingIntent = compactionHistoryIntent{kind: compactionHistoryIntentSession}
	m.handleSessionAgentEvent(agent.SessionSwitchStartedEvent{SessionID: "session-2"})
	if m.compactionHistory.active || m.compactionHistory.viewport != nil ||
		m.compactionHistory.pendingIntent.kind != compactionHistoryIntentNone || m.mode != ModeNormal {
		t.Fatal("switching session replayed or retained the outgoing browser")
	}
}

func TestCompactionHistoryHiddenDoneExpiryKeepsNextRequestQueued(t *testing.T) {
	m, _ := newCompactionHistoryIsolationModel(t)
	t.Cleanup(func() { _ = m.Close() })
	m.mode = ModeConfirm
	m.confirm = confirmState{
		request:  &ConfirmRequest{ToolName: tools.NameDone, DoneReport: "# Complete", RequestID: "done-1"},
		prevMode: ModeNormal, deadline: time.Now().Add(time.Minute),
	}
	m.handleConfirmKey(tea.KeyPressMsg{Text: "v", Code: 'v'})
	if m.mode != ModeContentViewer {
		t.Fatal("fixture did not open the report viewer")
	}
	m.openCompactionHistory()
	m.handleConfirmRequest(confirmRequestMsg{request: ConfirmRequest{ToolName: tools.NameShell, RequestID: "request-2"}})
	m.confirm.deadline = time.Now().Add(-time.Second)
	m.handleConfirmTimeoutTick()
	if !m.compactionHistory.active || m.confirm.request != nil || len(m.pendingDialogs) != 1 ||
		m.contentViewer.content != "" || m.mode != ModeNormal {
		t.Fatal("hidden expiry restored a stale viewer or presented the next dialog")
	}
	m.closeCompactionHistory()
	if m.confirm.request == nil || m.confirm.request.RequestID != "request-2" {
		t.Fatal("returning from history failed to present the queued request")
	}
}

func TestCompactionHistoryProgressShowsCardsAndSearchWaitsForTail(t *testing.T) {
	m, _ := newCompactionHistoryIsolationModel(t)
	t.Cleanup(func() { _ = m.Close() })
	m.layout = m.generateLayout(m.width, m.height)
	m.openCompactionHistory()
	h := &m.compactionHistory
	h.loading = false
	h.snapshots = []recovery.CompactionHistorySnapshot{{Index: 3}}
	msgs := reflowTestMessages(120)
	msgs[119].Content = "unique-search-tail"
	install := m.handleCompactionHistoryLoaded(compactionHistoryLoadedMsg{
		generation: h.generation, index: 3, messages: msgs,
	})
	next, _ := m.handleCompactionHistoryMessage(install())
	frame := terminaltext.StripANSI(m.renderCompactionHistory(m.compactionHistoryArea()))
	if !strings.Contains(frame, "message 0") || h.installedCount >= len(msgs) {
		t.Fatal("first batch is not visible before the full snapshot is ready")
	}
	if cmd := m.startCompactionHistorySearch("unique-search-tail"); cmd != nil {
		t.Fatal("search started over an incomplete prefix")
	}
	driveCompactionHistoryCmd(t, m, next)
	if len(h.search.Matches) != 1 || h.search.Matches[0].BlockID != 119 {
		t.Fatalf("search missed the final card: %v", h.search.Matches)
	}
}

func TestCompactionHistorySmallWindowKeepsSourceAndExitVisible(t *testing.T) {
	dir := compactionHistorySessionDir(t, 3)
	m, block := compactionHistoryModel(t, dir, compactionCheckpointContent("/sample/history-2.md", "/sample/history-3.md"))
	t.Cleanup(func() { _ = m.Close() })
	m.width, m.height = 28, 5
	m.layout = m.generateLayout(m.width, m.height)
	scan := m.openCompactionSummaryHistory(block)
	m.handleCompactionHistoryScanned(scan().(compactionHistoryScannedMsg))
	m.handleCompactionHistorySourceKey(tea.KeyPressMsg{Code: tea.KeyEnd}, "end")
	frame := terminaltext.StripANSI(m.View().Content)
	for _, text := range []string{"HISTORY", "esc ⇢ live", "> history-3.md"} {
		if !strings.Contains(frame, text) {
			t.Fatalf("small frame lacks %q: %s", text, frame)
		}
	}
	if index, ok := m.compactionHistorySourceRowAt(2); !ok || index != 1 {
		t.Fatalf("scrolled source hit = %d, %v", index, ok)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.compactionHistoryActive() {
		t.Fatal("Esc did not exit history in a small window")
	}
}
