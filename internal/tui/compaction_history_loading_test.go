package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

func TestCompactionHistoryLoadsFileNumberAsListPosition(t *testing.T) {
	m, _ := newCompactionHistoryIsolationModel(t)
	t.Cleanup(func() { _ = m.Close() })
	scan := m.openCompactionHistory()
	load := m.handleCompactionHistoryScanned(scan().(compactionHistoryScannedMsg))
	driveCompactionHistoryCmd(t, m, m.handleCompactionHistoryLoaded(load().(compactionHistoryLoadedMsg)))
	if got := m.compactionHistory.current; got != 0 {
		t.Fatalf("snapshot #3 position = %d, want 0", got)
	}
	title := m.compactionHistoryModeLabel() + " " + m.compactionHistoryStatusText()
	if !strings.Contains(title, "1/1") || !strings.Contains(title, "snapshot #3") {
		t.Fatalf("title = %q, want list position and file number", title)
	}
	if cmd := m.stepCompactionHistorySnapshot(-1); cmd != nil {
		t.Fatal("the only snapshot must not have an earlier neighbour")
	}
}

func TestCompactionHistoryRendersSourcesAfterScan(t *testing.T) {
	dir := compactionHistorySessionDir(t, 3)
	m, block := compactionHistoryModel(t, dir, compactionCheckpointContent("/sample/history-2.md", "/sample/history-3.md"))
	t.Cleanup(func() { _ = m.Close() })
	scan := m.openCompactionSummaryHistory(block)
	m.handleCompactionHistoryScanned(scan().(compactionHistoryScannedMsg))
	frame := terminaltext.StripANSI(m.renderCompactionHistory(m.compactionHistoryArea()))
	for _, text := range []string{compactionHistorySourceHeader, "history-2.md", "history-3.md", compactionHistorySourceMissing, compactionHistorySourceAvailable} {
		if !strings.Contains(frame, text) {
			t.Fatalf("source list does not show %q:\n%s", text, frame)
		}
	}
}

func TestCompactionHistoryInitialFailureRestoresLiveDialog(t *testing.T) {
	for _, kind := range []string{"scan", "load"} {
		t.Run(kind, func(t *testing.T) {
			m, _ := newCompactionHistoryIsolationModel(t)
			t.Cleanup(func() { _ = m.Close() })
			m.mode = ModeConfirm
			m.input.SetValue("unfinished draft")
			request := &ConfirmRequest{ToolName: "sample-tool", ArgsJSON: `{"sample":"pending confirmation"}`}
			m.confirm.request = request
			m.openCompactionHistory()
			h := &m.compactionHistory
			if kind == "scan" {
				m.handleCompactionHistoryScanned(compactionHistoryScannedMsg{
					generation: h.generation, err: recovery.ErrCompactionSnapshotRead,
				})
			} else {
				h.snapshots = []recovery.CompactionHistorySnapshot{{Index: 3}}
				m.requestCompactionHistorySnapshot(0)
				m.handleCompactionHistoryLoaded(compactionHistoryLoadedMsg{
					generation: h.generation, index: 3, err: recovery.ErrCompactionSnapshotRead,
				})
			}
			if m.compactionHistory.active {
				t.Fatal("an initial history failure left the live dialog hidden")
			}
			if m.mode != ModeConfirm || m.confirm.request != request || m.input.Value() != "unfinished draft" {
				t.Fatal("an initial history failure changed the live dialog or draft")
			}
		})
	}
}

func TestCompactionHistorySerializesLoadsAndKeepsNewestTarget(t *testing.T) {
	for _, oldErr := range []error{nil, context.Canceled} {
		name := "completed"
		if oldErr != nil {
			name = "cancelled"
		}
		t.Run(name, func(t *testing.T) {
			m, _ := newCompactionHistoryIsolationModel(t)
			t.Cleanup(func() { _ = m.Close() })
			m.openCompactionHistory()
			h := &m.compactionHistory
			h.snapshots = []recovery.CompactionHistorySnapshot{{Index: 1}, {Index: 2}, {Index: 3}}
			first := m.requestCompactionHistorySnapshot(0)
			if first == nil {
				t.Fatal("the first request did not start a load")
			}
			firstGeneration := h.generation
			for _, position := range []int{1, 2} {
				if cmd := m.requestCompactionHistorySnapshot(position); cmd != nil {
					t.Fatal("a second load started before the running load returned")
				}
			}
			next := m.handleCompactionHistoryLoaded(compactionHistoryLoadedMsg{
				generation: firstGeneration, index: 1, err: oldErr,
				messages: []message.Message{{Role: message.RoleUser, Content: "first snapshot"}},
			})
			if next == nil || h.current != -1 || h.pendingIndex != 2 {
				t.Fatalf("stale result did not release the slot for the newest target: current=%d pending=%d", h.current, h.pendingIndex)
			}
			loaded := next().(compactionHistoryLoadedMsg)
			if loaded.index != 3 || loaded.err != nil {
				t.Fatalf("newest load = %#v, want snapshot #3", loaded)
			}
			driveCompactionHistoryCmd(t, m, m.handleCompactionHistoryLoaded(loaded))
			if h.current != 2 || h.loading || h.loadGeneration != 0 {
				t.Fatalf("load did not settle at the newest position: current=%d loading=%v running=%d", h.current, h.loading, h.loadGeneration)
			}
			if cmd := m.handleCompactionHistoryLoaded(compactionHistoryLoadedMsg{generation: firstGeneration, index: 1}); cmd != nil {
				t.Fatal("duplicate stale result restarted a load")
			}
			if h.current != 2 {
				t.Fatal("a duplicate stale result replaced the newest snapshot")
			}
		})
	}
}

func TestCompactionHistoryReopenWaitsForPreviousLoad(t *testing.T) {
	m, _ := newCompactionHistoryIsolationModel(t)
	t.Cleanup(func() { _ = m.Close() })
	scan := m.openCompactionHistory()
	load := m.handleCompactionHistoryScanned(scan().(compactionHistoryScannedMsg))
	previous := load().(compactionHistoryLoadedMsg)
	m.closeCompactionHistory()
	scan = m.openCompactionHistory()
	if next := m.handleCompactionHistoryScanned(scan().(compactionHistoryScannedMsg)); next != nil {
		t.Fatal("reopening started a load before the previous command returned")
	}
	h := &m.compactionHistory
	if h.generation <= previous.generation {
		t.Fatal("the reopened browser reused the previous generation")
	}
	next := m.handleCompactionHistoryLoaded(previous)
	if next == nil || h.current != -1 || len(h.pendingBlocks) != 0 {
		t.Fatal("the previous browser's result was installed or did not release the load slot")
	}
	driveCompactionHistoryCmd(t, m, m.handleCompactionHistoryLoaded(next().(compactionHistoryLoadedMsg)))
	if h.current != 0 {
		t.Fatalf("reopened snapshot position = %d, want 0", h.current)
	}
}

func TestCompactionHistoryResizeDuringInstallKeepsAllCards(t *testing.T) {
	m, _ := newCompactionHistoryIsolationModel(t)
	t.Cleanup(func() { _ = m.Close() })
	m.layout = m.generateLayout(m.width, m.height)
	m.openCompactionHistory()
	h := &m.compactionHistory
	h.snapshots = []recovery.CompactionHistorySnapshot{{Index: 3}}
	install := m.handleCompactionHistoryLoaded(compactionHistoryLoadedMsg{
		generation: h.generation, index: 3, messages: reflowTestMessages(120),
	})
	staleInstall, _ := m.handleCompactionHistoryMessage(install())
	m.width -= 20
	firstReflow := m.startCompactionHistoryReflow()
	m.width -= 10
	latestReflow := m.startCompactionHistoryReflow()
	if cmd, _ := m.handleCompactionHistoryMessage(staleInstall()); cmd != nil {
		t.Fatal("an old install continued after resize")
	}
	if cmd, _ := m.handleCompactionHistoryMessage(firstReflow()); cmd != nil {
		t.Fatal("an old reflow continued after another resize")
	}
	driveCompactionHistoryCmd(t, m, latestReflow)
	if blocks := m.compactionHistoryBlocks(); len(blocks) != 120 || blocks[119].ID != 119 {
		t.Fatalf("resize lost or reordered cards: count=%d", len(blocks))
	}
	if h.viewport.width != m.compactionHistoryViewportWidth() || h.reflowActive {
		t.Fatal("the final reflow did not settle at the current terminal width")
	}
}

func TestCompactionHistoryResizeAndReadFailureKeepPreviousSnapshot(t *testing.T) {
	m := installedCompactionHistoryModel(t, reflowTestMessages(120))
	t.Cleanup(func() { _ = m.Close() })
	h := &m.compactionHistory
	h.viewport.ScrollDown(120)
	anchorID := h.viewport.GetBlockAtOffset().ID
	m.requestCompactionHistorySnapshot(0)
	generation := h.generation
	m.width -= 20
	first := m.startCompactionHistoryReflow()
	m.width -= 8
	last := m.startCompactionHistoryReflow()
	m.handleCompactionHistoryLoaded(compactionHistoryLoadedMsg{
		generation: generation, index: 3, err: recovery.ErrCompactionSnapshotRead,
	})
	m.handleCompactionHistoryMessage(first())
	driveCompactionHistoryCmd(t, m, last)
	if !h.active || h.notice == "" || h.loading {
		t.Fatalf("read failure hid the previous snapshot: active=%v notice=%q loading=%v", h.active, h.notice, h.loading)
	}
	if len(m.compactionHistoryBlocks()) != 120 || h.viewport.GetBlockAtOffset().ID != anchorID {
		t.Fatal("resizing while reading lost previous cards or the scroll anchor")
	}
	if h.viewport.width != m.compactionHistoryViewportWidth() {
		t.Fatal("previous cards did not reflow at the final width")
	}
}

func TestCompactionHistoryInstallPreservesReaderInteraction(t *testing.T) {
	for _, selectText := range []bool{false, true} {
		name := "scroll"
		if selectText {
			name = "selection"
		}
		t.Run(name, func(t *testing.T) {
			m, _ := newCompactionHistoryIsolationModel(t)
			t.Cleanup(func() { _ = m.Close() })
			m.openCompactionHistory()
			h := &m.compactionHistory
			h.snapshots = []recovery.CompactionHistorySnapshot{{Index: 3}}
			first := m.handleCompactionHistoryLoaded(compactionHistoryLoadedMsg{
				generation: h.generation, index: 3, messages: reflowTestMessages(120),
			})
			next, handled := m.handleCompactionHistoryMessage(first())
			if !handled || next == nil || h.installedCount != compactionHistoryInstallChunk {
				t.Fatal("expected more cards after the first installation batch")
			}
			m.scrollCompactionHistory(12)
			if h.viewport.offset != 12 {
				t.Fatalf("initial offset = %d, want 12", h.viewport.offset)
			}
			if selectText {
				m.renderCompactionHistory(m.compactionHistoryArea())
				m.handleCompactionHistoryMouse(tea.MouseClickMsg{X: 8, Y: 3, Button: tea.MouseLeft})
				m.handleCompactionHistoryMouse(tea.MouseReleaseMsg{X: 18, Y: 3, Button: tea.MouseLeft})
				if h.selection.rangePtr() == nil || h.focusedBlockID < 0 {
					t.Fatal("mouse interaction did not select a focused card")
				}
			}
			offset, focus, selection := h.viewport.offset, h.focusedBlockID, h.selection
			driveCompactionHistoryCmd(t, m, next)
			if h.viewport.offset != offset {
				t.Fatalf("installation moved the reader from offset %d to %d", offset, h.viewport.offset)
			}
			if h.focusedBlockID != focus || h.selection != selection {
				t.Fatal("installation cleared the reader's focus or selection")
			}
			if selectText && !h.viewport.GetFocusedBlock(focus).Focused {
				t.Fatal("installation cleared the focused card's highlight")
			}
		})
	}
}
