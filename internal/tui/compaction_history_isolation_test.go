package tui

import (
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

func TestCompactionHistoryMouseDuringTakeoverKeepsLiveSessionIsolated(t *testing.T) {
	for _, mode := range []struct {
		name string
		mode Mode
	}{{"normal", ModeNormal}, {"insert", ModeInsert}} {
		t.Run(mode.name, func(t *testing.T) {
			m := installedCompactionHistoryModel(t, []message.Message{{
				Role: message.RoleUser, Content: "alpha bravo charlie\n" + strings.Repeat("another line\n", 80),
			}})
			t.Cleanup(func() { _ = m.Close() })
			m.mode = mode.mode
			backend := m.agent.(*compactionHistorySessionAgent)
			backend.sessionSummary = &agent.SessionSummary{ID: "sample-session"}
			reply := make(chan error, 1)
			cmd := m.prepareSourceTakeover(agent.SessionTransferPreparingEvent{
				Identity: recovery.TransferIdentity{SessionID: "sample-session", TransferID: "sample-transfer"},
				Reply:    reply,
			})
			if cmd == nil || !m.takeoverInputFrozen() {
				t.Fatal("transfer did not freeze input")
			}
			m.Update(cmd())
			select {
			case err := <-reply:
				if err != nil {
					t.Fatal(err)
				}
			default:
				t.Fatal("preparation did not acknowledge")
			}
			m.scrollCompactionHistory(-1000)
			liveOffset, liveSelection := m.viewport.offset, m.mouseSelection
			start := compactionHistoryTextPoint(t, m, 0, "bravo")
			end := start
			end.X += len("bravo") - 1
			m.Update(tea.MouseClickMsg(start))
			if !m.compactionHistory.selection.dragging {
				t.Fatal("history did not start selection")
			}
			m.Update(tea.MouseMotionMsg(end))
			m.Update(tea.MouseReleaseMsg(end))
			h := &m.compactionHistory
			selection := h.selection.rangePtr()
			if selection == nil || h.viewport.ExtractSelectionText(*selection) != "bravo" || h.selection.dragging {
				t.Fatalf("history selection = %#v, dragging=%t", selection, h.selection.dragging)
			}
			offset := h.viewport.offset
			m.Update(tea.MouseWheelMsg{X: start.X, Y: start.Y, Button: tea.MouseWheelDown})
			if h.viewport.offset <= offset {
				t.Fatal("history did not scroll")
			}
			if m.viewport.offset != liveOffset || m.mouseSelection != liveSelection {
				t.Fatal("history mouse events changed the live viewport")
			}
			m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
			if m.compactionHistoryActive() || !m.takeoverInputFrozen() {
				t.Fatal("leaving history changed transfer admission")
			}
			m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
			if m.input.Value() != "" || len(backend.sentMessages) != 0 {
				t.Fatal("the live session accepted new input during transfer")
			}
		})
	}
}

func TestCompactionHistoryExitDuringTakeoverKeepsLiveSessionFrozen(t *testing.T) {
	for _, mode := range []struct {
		name string
		mode Mode
	}{
		{"normal", ModeNormal},
		{"insert", ModeInsert},
	} {
		for _, key := range []tea.KeyPressMsg{
			{Code: tea.KeyEscape},
			{Code: 'c', Mod: tea.ModCtrl},
		} {
			t.Run(mode.name+"/"+key.String(), func(t *testing.T) {
				m, backend := newCompactionHistoryIsolationModel(t)
				t.Cleanup(func() { _ = m.Close() })
				backend.sessionSummary = &agent.SessionSummary{ID: "sample-session"}
				m.mode = mode.mode
				driveCompactionHistoryCmd(t, m, m.handleKeyMsg(tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModAlt}))
				if !m.compactionHistoryActive() {
					t.Fatal("history did not open")
				}
				reply := make(chan error, 1)
				cmd := m.prepareSourceTakeover(agent.SessionTransferPreparingEvent{
					Identity: recovery.TransferIdentity{SessionID: "sample-session", TransferID: "sample-transfer"},
					Reply:    reply,
				})
				if cmd == nil || !m.takeoverInputFrozen() {
					t.Fatal("transfer did not freeze input")
				}
				m.Update(cmd())
				select {
				case err := <-reply:
					if err != nil {
						t.Fatal(err)
					}
				default:
					t.Fatal("preparation did not acknowledge")
				}
				m.activities["main"] = agent.AgentActivityEvent{AgentID: "main", Type: agent.ActivityStreaming}
				m.Update(key)
				if m.compactionHistoryActive() || backend.cancelCalls != 0 || m.quit.by != "" {
					t.Fatalf("history=%t cancelCalls=%d quit=%q", m.compactionHistoryActive(), backend.cancelCalls, m.quit.by)
				}
				if !m.takeoverInputFrozen() || m.mode != mode.mode {
					t.Fatal("leaving history changed takeover admission or the live mode")
				}
				m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
				if m.input.Value() != "" || len(backend.sentMessages) != 0 {
					t.Fatal("the live session accepted new input during transfer")
				}
			})
		}
	}
}

func TestCompactionHistoryLoadingFrameHidesLiveDialog(t *testing.T) {
	m, _ := newCompactionHistoryIsolationModel(t)
	t.Cleanup(func() { _ = m.Close() })
	m.mode = ModeConfirm
	m.confirm.request = &ConfirmRequest{ToolName: "sample-tool", ArgsJSON: `{"sample":"pending confirmation"}`}
	m.View()
	m.openCompactionHistory()
	view := m.View()
	plain := terminaltext.StripANSI(view.Content)
	if strings.Contains(plain, "pending confirmation") {
		t.Fatal("the live confirmation is visible through the loading history layer")
	}
	if !strings.Contains(plain, "Reading the compaction history") {
		t.Fatalf("the loading layer was not drawn:\n%s", plain)
	}
	if view.Cursor != nil {
		t.Fatal("the hidden live session supplied the history cursor")
	}
}

func TestCompactionHistoryRejectsDelayedComposerFocus(t *testing.T) {
	m, _ := newCompactionHistoryIsolationModel(t)
	t.Cleanup(func() { _ = m.Close() })
	m.openCompactionHistory()
	m.Update(focusInputMsg{})
	if m.input.Focused() || m.imeSwitchAllowed() {
		t.Fatal("a delayed focus command reactivated the hidden composer or IME switching")
	}
}

func TestCompactionHistoryRejectsLateLiveImageTransport(t *testing.T) {
	m := inlineImageTestModel(t, ImageBackendITerm2)
	t.Cleanup(func() { _ = m.Close() })
	m.agent = &compactionHistorySessionAgent{
		sessionControlAgent: &sessionControlAgent{}, dir: compactionHistorySessionDir(t, 3),
	}
	load := m.imageProtocolCmd()
	if load == nil {
		t.Fatal("the live image did not start loading")
	}
	loaded := load().(inlineImagesLoadedMsg)
	if loaded.sequence == "" {
		t.Fatal("the live image transport was not prepared")
	}
	m.openCompactionHistory()
	if send := m.handleInlineImagesLoaded(loaded); send != nil {
		t.Fatal("a late live image transport was emitted over the history layer")
	}
	if m.inlineImagesAllowed() || m.imageProtocolCmd() != nil {
		t.Fatal("live image rendering is still enabled while browsing history")
	}
	restore := m.closeCompactionHistory()
	if !m.inlineImagesAllowed() || restore == nil {
		t.Fatal("live image rendering was not restored after closing history")
	}
}

func TestCompactionHistoryAttachmentMetadataSurvivesSpill(t *testing.T) {
	m := inlineImageTestModel(t, ImageBackendKitty)
	t.Cleanup(func() { _ = m.Close() })
	m.agent = &compactionHistorySessionAgent{
		sessionControlAgent: &sessionControlAgent{}, dir: compactionHistorySessionDir(t, 3),
	}
	data := m.viewport.blocks[0].ImageParts[0].Data
	caps := currentImageCapabilities()
	m.openCompactionHistory()
	m.compactionHistory.snapshots = []recovery.CompactionHistorySnapshot{{Index: 3}}
	driveCompactionHistoryCmd(t, &m, m.handleCompactionHistoryLoaded(compactionHistoryLoadedMsg{
		generation: m.compactionHistory.generation, index: 3,
		messages: []message.Message{{
			Role: message.RoleUser,
			Parts: []message.ContentPart{{
				Type: message.ContentPartImage, FileName: "sample.jpg", MimeType: "image/jpeg", Data: data, ImagePath: "/sample/image.jpg",
			}},
		}},
	}))
	block := m.compactionHistoryBlocks()[0]
	part := block.ImageParts[0]
	if !part.MetadataOnly || part.RenderImageID != 0 || part.RenderRows > 1 || len(part.Data) != 0 || part.ImagePath != "" {
		t.Fatalf("history attachment retained image rendering state: %#v", part)
	}
	if currentImageCapabilities() != caps {
		t.Fatal("history attachment rendering changed global terminal capabilities")
	}
	store, err := newViewportSpillStore()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ref, err := store.Append(block)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := store.Load(ref)
	if err != nil {
		t.Fatal(err)
	}
	rendered := terminaltext.StripANSI(strings.Join(restored.Render(80, ""), "\n"))
	if !restored.ImageParts[0].MetadataOnly || restored.ImageParts[0].RenderImageID != 0 || !strings.Contains(rendered, "sample.jpg") {
		t.Fatalf("spilled attachment did not remain metadata:\n%s", rendered)
	}
}
