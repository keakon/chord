package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestCompactionHistorySearchTracksCardToggle(t *testing.T) {
	for _, expanded := range []bool{false, true} {
		for _, pending := range []bool{false, true} {
			t.Run(fmt.Sprintf("expanded=%v/pending=%v", expanded, pending), func(t *testing.T) {
				const query = "unique-target"
				msgs := []message.Message{
					{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{
						ID: "call-1", Name: tools.NameShell, Args: []byte(`{"command":"sample"}`),
					}}},
					{Role: message.RoleTool, ToolCallID: "call-1", ToolStatus: message.ToolStatusSuccess,
						Content: strings.Repeat("output line\n", 80)},
					{Role: message.RoleUser, Content: query},
				}
				msgs = append(msgs, reflowTestMessages(2*compactionHistorySearchChunk)...)
				m := installedCompactionHistoryModel(t, msgs)
				t.Cleanup(func() { _ = m.Close() })
				h := &m.compactionHistory
				var tool *Block
				for _, block := range m.compactionHistoryBlocks() {
					if block.Type == BlockToolCall {
						tool = block
						break
					}
				}
				if tool == nil || !tool.Collapsed {
					t.Fatal("fixture needs a collapsed tool card")
				}
				toggle := func() tea.Cmd {
					m.setCompactionHistoryFocus(tool.ID)
					return m.handleCompactionHistoryKey(tea.KeyPressMsg{Text: "o", Code: 'o'})
				}
				if expanded {
					driveCompactionHistoryCmd(t, m, toggle())
				}
				search := m.startCompactionHistorySearch(query)
				var stale tea.Msg
				if pending {
					next, handled := m.handleCompactionHistoryMessage(search())
					if !handled || next == nil || !h.searchPending {
						t.Fatal("fixture needs another search batch")
					}
					stale = next()
				} else {
					driveCompactionHistoryCmd(t, m, search)
				}
				if len(h.search.Matches) != 1 {
					t.Fatalf("initial matches = %v, want one", h.search.Matches)
				}
				oldOffset := h.search.Matches[0].LineOffset
				refresh := toggle()
				if stale != nil {
					if next, _ := m.handleCompactionHistoryMessage(stale); next != nil {
						t.Fatal("stale search batch continued after the layout changed")
					}
				}
				driveCompactionHistoryCmd(t, m, refresh)
				fresh := FindMatchesAtWidth(m.compactionHistoryBlocks(), query, h.viewport.width)
				if len(fresh) != 1 || fresh[0].LineOffset == oldOffset {
					t.Fatalf("fixture did not move the target: old=%d fresh=%v", oldOffset, fresh)
				}
				if !slices.Equal(h.search.Matches, fresh) {
					t.Fatalf("cached matches = %v, want current layout %v", h.search.Matches, fresh)
				}
				for _, key := range []rune{'n', 'N'} {
					h.viewport.ScrollToTop()
					m.handleCompactionHistoryKey(tea.KeyPressMsg{Text: string(key), Code: key})
					target := fresh[0].LineOffset + fresh[0].InnerOffset
					if target < h.viewport.offset || target >= h.viewport.offset+h.viewport.height {
						t.Fatalf("%c left the target offscreen: target=%d viewport=%d height=%d",
							key, target, h.viewport.offset, h.viewport.height)
					}
				}
			})
		}
	}
}

func TestCompactionHistoryReflowPreservesSearchPosition(t *testing.T) {
	for _, repeat := range []string{"none", "install", "search"} {
		for _, scroll := range []bool{false, true} {
			t.Run(fmt.Sprintf("repeat=%s/scroll=%t", repeat, scroll), func(t *testing.T) {
				m := installedCompactionHistoryModel(t, reflowTestMessages(120))
				t.Cleanup(func() { _ = m.Close() })
				h := &m.compactionHistory
				driveCompactionHistoryCmd(t, m, m.startCompactionHistorySearch("message"))
				for range 35 {
					m.stepCompactionHistoryMatch(true)
				}
				match, ok := h.search.CurrentMatch()
				if !ok || match.BlockID != 35 {
					t.Fatalf("initial match = %#v, want block 35", match)
				}
				if scroll {
					m.scrollCompactionHistory(30)
				}
				anchorID := h.viewport.GetBlockAtOffset().ID
				m.width -= 20
				cmd := m.startCompactionHistoryReflow()
				if repeat == "search" {
					for i := 0; h.installPending; i++ {
						if i >= 100 || cmd == nil {
							t.Fatal("reflow installation did not finish")
						}
						cmd, _ = m.handleCompactionHistoryMessage(cmd())
					}
					cmd, _ = m.handleCompactionHistoryMessage(cmd())
					if !h.searchPending {
						t.Fatal("expected another search batch")
					}
				}
				if repeat != "none" {
					stale := cmd()
					m.width -= 10
					cmd = m.startCompactionHistoryReflow()
					if next, _ := m.handleCompactionHistoryMessage(stale); next != nil {
						t.Fatal("superseded reflow continued")
					}
				}
				driveCompactionHistoryCmd(t, m, cmd)
				current, ok := h.search.CurrentMatch()
				if !ok || current.BlockID != match.BlockID {
					t.Fatalf("match after reflow = %#v, want block %d", current, match.BlockID)
				}
				if got := h.viewport.GetBlockAtOffset().ID; got != anchorID {
					t.Fatalf("reading anchor = %d, want %d", got, anchorID)
				}
				if h.focusedBlockID != match.BlockID || !h.viewport.GetFocusedBlock(match.BlockID).Focused {
					t.Fatal("reflow cleared the focused card")
				}
				fresh := FindMatchesAtWidth(m.compactionHistoryBlocks(), "message", h.viewport.width)
				if !slices.Equal(h.search.Matches, fresh) {
					t.Fatal("reflow left search coordinates at the previous width")
				}
				for _, direction := range []bool{true, false} {
					m.stepCompactionHistoryMatch(direction)
					current, ok = h.search.CurrentMatch()
					want := match.BlockID
					if direction {
						want++
					}
					if !ok || current.BlockID != want {
						t.Fatalf("search navigation after reflow = %#v, want block %d", current, want)
					}
				}
			})
		}
	}
}

func TestCompactionHistoryReflowSearchCanBeReplacedOrCancelled(t *testing.T) {
	for _, action := range []string{"replace", "cancel", "close"} {
		t.Run(action, func(t *testing.T) {
			m := installedCompactionHistoryModel(t, reflowTestMessages(120))
			t.Cleanup(func() { _ = m.Close() })
			h := &m.compactionHistory
			driveCompactionHistoryCmd(t, m, m.startCompactionHistorySearch("message"))
			for range 35 {
				m.stepCompactionHistoryMatch(true)
			}
			m.width -= 20
			cmd := m.startCompactionHistoryReflow()
			switch action {
			case "replace":
				if next := m.startCompactionHistorySearch("message 2:"); next != nil {
					t.Fatal("replacement query started before installation finished")
				}
			case "cancel":
				m.cancelCompactionHistorySearch()
			case "close":
				m.closeCompactionHistory()
			}
			driveCompactionHistoryCmd(t, m, cmd)
			switch action {
			case "replace":
				match, ok := h.search.CurrentMatch()
				if !ok || match.BlockID != 2 || h.viewport.GetBlockAtOffset().ID != 2 {
					t.Fatalf("replacement query did not navigate to its result: %#v", match)
				}
			case "cancel":
				if h.search.Active || len(h.search.Matches) != 0 || h.searchPending {
					t.Fatal("reflow revived the cancelled search")
				}
			case "close":
				if h.active || h.viewport != nil {
					t.Fatal("reflow revived the closed browser")
				}
			}
		})
	}
}
