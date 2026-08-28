package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func jumpTypeTestModel() Model {
	m := NewModelWithSize(nil, 80, 10)
	m.mode = ModeNormal
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, Content: "u1"})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockAssistant, Content: strings.Repeat("a\n", 4)})
	m.viewport.AppendBlock(&Block{ID: 3, Type: BlockToolCall, ToolName: "shell", Content: `{"command":"go test"}`})
	m.viewport.AppendBlock(&Block{ID: 4, Type: BlockUser, Content: "u2"})
	m.viewport.AppendBlock(&Block{ID: 5, Type: BlockAssistant, Content: strings.Repeat("b\n", 4)})
	m.viewport.AppendBlock(&Block{ID: 6, Type: BlockToolCall, ToolName: "edit", Content: `{"path":"a.go","old_string":"x","new_string":"y"}`})
	m.viewport.AppendBlock(&Block{ID: 7, Type: BlockUser, Content: "u3"})
	m.viewport.AppendBlock(&Block{ID: 8, Type: BlockAssistant, Content: strings.Repeat("c\n", 4)})
	m.viewport.ScrollToTop()
	return m
}

func TestNormalModeBraceJumpsOnlyUserCards(t *testing.T) {
	m := jumpTypeTestModel()
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "}", Code: '}'})); cmd != nil {
		t.Fatalf("} should move synchronously, got %#v", cmd)
	}
	if m.focusedBlockID != 4 {
		t.Fatalf("focusedBlockID after } = %d, want 4 (next user card from top)", m.focusedBlockID)
	}
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "}", Code: '}'})); cmd != nil {
		t.Fatalf("} should move synchronously, got %#v", cmd)
	}
	if m.focusedBlockID != 7 {
		t.Fatalf("focusedBlockID after second } = %d, want 7", m.focusedBlockID)
	}
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "}", Code: '}'})); cmd != nil {
		t.Fatalf("} at boundary should be a no-op, got %#v", cmd)
	}
	if m.focusedBlockID != 7 {
		t.Fatalf("focusedBlockID after out-of-range } = %d, want 7", m.focusedBlockID)
	}

	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "{", Code: '{'})); cmd != nil {
		t.Fatalf("{ should move synchronously, got %#v", cmd)
	}
	if m.focusedBlockID != 4 {
		t.Fatalf("focusedBlockID after { = %d, want 4", m.focusedBlockID)
	}
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "{", Code: '{'})); cmd != nil {
		t.Fatalf("{ should move synchronously, got %#v", cmd)
	}
	if m.focusedBlockID != 1 {
		t.Fatalf("focusedBlockID after second { = %d, want 1", m.focusedBlockID)
	}
}

func TestNormalModeParenJumpsOnlyAssistantCards(t *testing.T) {
	m := jumpTypeTestModel()
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: ")", Code: ')'})); cmd != nil {
		t.Fatalf(") should move synchronously, got %#v", cmd)
	}
	if m.focusedBlockID != 2 {
		t.Fatalf("focusedBlockID after ) = %d, want 2 (first assistant card from top)", m.focusedBlockID)
	}
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: ")", Code: ')'})); cmd != nil {
		t.Fatalf(") should move synchronously, got %#v", cmd)
	}
	if m.focusedBlockID != 5 {
		t.Fatalf("focusedBlockID after second ) = %d, want 5", m.focusedBlockID)
	}
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "(", Code: '('})); cmd != nil {
		t.Fatalf("( should move synchronously, got %#v", cmd)
	}
	if m.focusedBlockID != 2 {
		t.Fatalf("focusedBlockID after ( = %d, want 2", m.focusedBlockID)
	}
}

func TestNormalModeBracketJumpsSameTypeFromFocusedCard(t *testing.T) {
	m := jumpTypeTestModel()
	m.focusedBlockID = 2
	m.refreshBlockFocus()
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "]", Code: ']'})); cmd != nil {
		t.Fatalf("] should move synchronously, got %#v", cmd)
	}
	if m.focusedBlockID != 5 {
		t.Fatalf("focusedBlockID after ] from assistant card = %d, want 5", m.focusedBlockID)
	}
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "[", Code: '['})); cmd != nil {
		t.Fatalf("[ should move synchronously, got %#v", cmd)
	}
	if m.focusedBlockID != 2 {
		t.Fatalf("focusedBlockID after [ = %d, want 2", m.focusedBlockID)
	}
}

func TestNormalModeBracketUsesViewportTopCardAsTemplateWithoutFocus(t *testing.T) {
	m := jumpTypeTestModel()
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "]", Code: ']'})); cmd != nil {
		t.Fatalf("] should move synchronously, got %#v", cmd)
	}
	// No focus: template is the block at the viewport top (user card 1), so ]
	// lands on the next user card, not the adjacent assistant card.
	if m.focusedBlockID != 4 {
		t.Fatalf("focusedBlockID after ] without focus = %d, want 4", m.focusedBlockID)
	}
}

func TestNormalModeCountedTypeJump(t *testing.T) {
	m := jumpTypeTestModel()
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "3", Code: '3'})); cmd == nil {
		t.Fatal("3 should start a count prefix")
	}
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "}", Code: '}'})); cmd != nil {
		t.Fatalf("3} should move synchronously, got %#v", cmd)
	}
	if m.chord.active() {
		t.Fatal("3} should clear chord state")
	}
	// 1 -> 4 -> 7 -> (no more) so it lands on the last user card.
	if m.focusedBlockID != 7 {
		t.Fatalf("focusedBlockID after 3} = %d, want 7", m.focusedBlockID)
	}

	m2 := jumpTypeTestModel()
	m2.focusedBlockID = 2
	m2.refreshBlockFocus()
	if cmd := m2.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "2", Code: '2'})); cmd == nil {
		t.Fatal("2 should start a count prefix")
	}
	if cmd := m2.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: ")", Code: ')'})); cmd != nil {
		t.Fatalf("2) should move synchronously, got %#v", cmd)
	}
	if m2.focusedBlockID != 8 {
		t.Fatalf("focusedBlockID after 2) from card 2 = %d, want 8", m2.focusedBlockID)
	}
}

func TestNormalModeTypeJumpBoundaryKeepsFocusAndOffset(t *testing.T) {
	m := jumpTypeTestModel()
	m.focusedBlockID = 8
	m.refreshBlockFocus()
	offsetBefore := m.viewport.offset
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: ")", Code: ')'})); cmd != nil {
		t.Fatalf(") with no next assistant card should be a no-op, got %#v", cmd)
	}
	if m.focusedBlockID != 8 {
		t.Fatalf("focusedBlockID after out-of-range ) = %d, want 8", m.focusedBlockID)
	}
	if m.viewport.offset != offsetBefore {
		t.Fatalf("offset changed on no-match jump: %d -> %d", offsetBefore, m.viewport.offset)
	}

	m.focusedBlockID = 7
	m.refreshBlockFocus()
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "}", Code: '}'})); cmd != nil {
		t.Fatalf("} with no next user card should be a no-op, got %#v", cmd)
	}
	if m.focusedBlockID != 7 {
		t.Fatalf("focusedBlockID after out-of-range } = %d, want 7", m.focusedBlockID)
	}
}

func TestNormalModeBraceNoLongerAliasesJK(t *testing.T) {
	m := jumpTypeTestModel()
	m.focusedBlockID = 2
	m.refreshBlockFocus()
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "}", Code: '}'})); cmd != nil {
		t.Fatalf("} should move synchronously, got %#v", cmd)
	}
	if m.focusedBlockID != 4 {
		t.Fatalf("} from assistant card = %d, want 4 (next user card, skipping tool/assistant cards)", m.focusedBlockID)
	}

	m2 := jumpTypeTestModel()
	m2.focusedBlockID = 2
	m2.refreshBlockFocus()
	if cmd := m2.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "j", Code: 'j'})); cmd != nil {
		t.Fatalf("j should move synchronously, got %#v", cmd)
	}
	if m2.focusedBlockID != 3 {
		t.Fatalf("j from assistant card = %d, want 3 (adjacent tool card)", m2.focusedBlockID)
	}
}

func TestNormalModeJKRegression(t *testing.T) {
	m := jumpTypeTestModel()
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "j", Code: 'j'})); cmd != nil {
		t.Fatalf("j should move synchronously, got %#v", cmd)
	}
	if m.focusedBlockID != 2 {
		t.Fatalf("focusedBlockID after j = %d, want 2", m.focusedBlockID)
	}
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "k", Code: 'k'})); cmd != nil {
		t.Fatalf("k should move synchronously, got %#v", cmd)
	}
	if m.focusedBlockID != 1 {
		t.Fatalf("focusedBlockID after k = %d, want 1", m.focusedBlockID)
	}
}

func TestNormalModeSameTypeJumpNoopOnErrorTemplate(t *testing.T) {
	m := NewModelWithSize(nil, 80, 10)
	m.mode = ModeNormal
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockError, Content: "e1"})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockUser, Content: "u2"})
	m.viewport.AppendBlock(&Block{ID: 3, Type: BlockAssistant, Content: strings.Repeat("a\n", 4)})
	m.viewport.ScrollToTop()
	m.focusedBlockID = 1
	m.refreshBlockFocus()
	offsetBefore := m.viewport.offset
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "]", Code: ']'})); cmd != nil {
		t.Fatalf("] on error template should be a no-op, got %#v", cmd)
	}
	if m.focusedBlockID != 1 {
		t.Fatalf("focusedBlockID after ] on error template = %d, want 1", m.focusedBlockID)
	}
	if m.viewport.offset != offsetBefore {
		t.Fatalf("offset changed on error-template jump: %d -> %d", offsetBefore, m.viewport.offset)
	}
}

func TestDirectoryCursorLandsOnCurrentCard(t *testing.T) {
	m := jumpTypeTestModel()
	// Focused entry: cursor must land on the focused card.
	m.focusedBlockID = 5
	m.refreshBlockFocus()
	if cmd := m.handleNormalKey(ctrlKeyPress('t')); cmd != nil {
		t.Fatalf("ctrl+t should open directory synchronously, got %#v", cmd)
	}
	if m.mode != ModeDirectory {
		t.Fatalf("mode = %v, want ModeDirectory", m.mode)
	}
	idx := m.dirList.CursorAt()
	if idx < 0 || idx >= len(m.dirEntries) || m.dirEntries[idx].BlockID != 5 {
		t.Fatalf("directory cursor at entry %d (block %d), want focused block 5", idx, m.dirEntries[idx].BlockID)
	}

	// Scroll-only entry (no focus): cursor must land on the viewport-top card.
	if cmd := m.handleDirectoryKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape})); cmd != nil {
		t.Fatalf("esc should close directory synchronously, got %#v", cmd)
	}
	if m.mode != ModeNormal {
		t.Fatalf("mode after esc = %v, want ModeNormal", m.mode)
	}
	m.focusedBlockID = -1
	m.refreshBlockFocus()
	m.viewport.ScrollToTop()
	if cmd := m.handleNormalKey(ctrlKeyPress('t')); cmd != nil {
		t.Fatalf("ctrl+t should open directory synchronously, got %#v", cmd)
	}
	wantID := m.viewport.GetBlockAtOffset().ID
	idx = m.dirList.CursorAt()
	if m.dirEntries[idx].BlockID != wantID {
		t.Fatalf("directory cursor block = %d, want viewport-top block %d", m.dirEntries[idx].BlockID, wantID)
	}
}

func TestDeferredTypeJumpSwitchesWindowToUserCard(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+130)
	for i := range startupTranscriptWindowMinBlocks + 130 {
		role := message.RoleAssistant
		if i == 0 {
			role = message.RoleUser
		}
		messages = append(messages, message.Message{Role: role, Content: fmt.Sprintf("message-%03d", i)})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)
	m.mode = ModeNormal

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred for type jump")
	}
	state := m.startupDeferredTranscript
	// The only user card is at index 0, outside the tail window, so a single {
	// must switch the window to cover it.
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "{", Code: '{'})); cmd != nil {
		t.Fatalf("{ in deferred mode should move synchronously, got %#v", cmd)
	}
	if state.windowStart != 0 {
		t.Fatalf("window after { = [%d,%d), want it to start at 0 to cover the user card", state.windowStart, state.windowEnd)
	}
	if m.focusedBlockID != state.allBlocks[0].ID {
		t.Fatalf("focusedBlockID = %d, want user card %d", m.focusedBlockID, state.allBlocks[0].ID)
	}
}

func TestDeferredDirectoryCursorLandsOnCurrentCard(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+40)
	for i := range startupTranscriptWindowMinBlocks + 40 {
		role := message.RoleAssistant
		if i%5 == 0 {
			role = message.RoleUser
		}
		messages = append(messages, message.Message{Role: role, Content: fmt.Sprintf("message-%03d", i)})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)
	m.mode = ModeNormal

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred for directory cursor test")
	}
	state := m.startupDeferredTranscript
	target := state.allBlocks[state.windowEnd-1]
	m.focusedBlockID = target.ID
	m.refreshBlockFocus()

	if cmd := m.handleNormalKey(ctrlKeyPress('t')); cmd != nil {
		t.Fatalf("ctrl+t should open deferred directory synchronously, got %#v", cmd)
	}
	if m.mode != ModeDirectory {
		t.Fatalf("mode = %v, want ModeDirectory", m.mode)
	}
	idx := m.dirList.CursorAt()
	if idx < 0 || idx >= len(m.dirEntries) || m.dirEntries[idx].BlockID != target.ID {
		t.Fatalf("deferred directory cursor at entry %d (block %d), want block %d", idx, m.dirEntries[idx].BlockID, target.ID)
	}
}

func TestToolCallSummaryIncludesPrimaryArg(t *testing.T) {
	cases := []struct {
		name        string
		block       *Block
		wantPrefix  string
		wantContent string
	}{
		{"shell command", &Block{Type: BlockToolCall, ToolName: tools.NameShell, Content: `{"command":"go test ./internal/tui -count=1"}`}, "Tool: shell", "go test ./internal/tui -count=1"},
		{"shell description", &Block{Type: BlockToolCall, ToolName: tools.NameShell, Content: `{"command":"true","description":"run the build"}`}, "Tool: shell", "run the build"},
		{"edit path", &Block{Type: BlockToolCall, ToolName: tools.NameEdit, Content: `{"path":"internal/tui/app.go","old_string":"a","new_string":"b"}`}, "Tool: edit", "app.go"},
		{"write path", &Block{Type: BlockToolCall, ToolName: tools.NameWrite, Content: `{"path":"notes.md","content":"x"}`}, "Tool: write", "notes.md"},
		{"read path", &Block{Type: BlockToolCall, ToolName: tools.NameRead, Content: `{"path":"docs/usage.md"}`}, "Tool: read", "docs/usage.md"},
		{"no args", &Block{Type: BlockToolCall, ToolName: tools.NameGrep, Content: `{}`}, "Tool: grep", ""},
		{"glob array control chars", &Block{Type: BlockToolCall, ToolName: tools.NameGlob, Content: `{"patterns":["**/*.go\u001b[31m"]}`}, "Tool: glob", `**/*.go\x1b[31m`},
		{"lsp operation control chars", &Block{Type: BlockToolCall, ToolName: tools.NameLsp, Content: `{"operation":"go to definition\u001b[31m","path":"app.go"}`}, "Tool: lsp", `go to definition\x1b[31m`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.block.Summary()
			if !strings.HasPrefix(got, tc.wantPrefix) {
				t.Fatalf("Summary() = %q, want prefix %q", got, tc.wantPrefix)
			}
			if tc.wantContent != "" && !strings.Contains(got, tc.wantContent) {
				t.Fatalf("Summary() = %q, want it to contain %q", got, tc.wantContent)
			}
			if strings.ContainsRune(got, '\x1b') {
				t.Fatalf("Summary() = %q, want no ANSI escapes", got)
			}
		})
	}
}

func TestNormalModeBracketUsesViewportTopCardAsTemplateWhenFocusStale(t *testing.T) {
	m := NewModelWithSize(nil, 80, 10)
	m.mode = ModeNormal
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "a1"})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockUser, Content: "u2"})
	m.viewport.AppendBlock(&Block{ID: 3, Type: BlockAssistant, Content: "a3"})
	m.viewport.AppendBlock(&Block{ID: 4, Type: BlockUser, Content: "u4"})
	m.viewport.ScrollToTop()
	// Stale focus: ID 99 is not in the viewport, so both the template and the
	// jump start fall back to the card at the viewport top (assistant 1) and ]
	// skips it to land on the next assistant card, exactly like the no-focus
	// path.
	m.focusedBlockID = 99
	m.refreshBlockFocus()
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "]", Code: ']'})); cmd != nil {
		t.Fatalf("] with stale focus should move synchronously, got %#v", cmd)
	}
	if m.focusedBlockID != 3 {
		t.Fatalf("focusedBlockID after ] with stale focus = %d, want 3 (next assistant card after the viewport-top template)", m.focusedBlockID)
	}
}

func TestDeferredTypeJumpNoMatchStaysInPlace(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+130)
	for i := range startupTranscriptWindowMinBlocks + 130 {
		role := message.RoleAssistant
		if i == 3 {
			role = message.RoleUser
		}
		messages = append(messages, message.Message{Role: role, Content: fmt.Sprintf("message-%03d", i)})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)
	m.mode = ModeNormal

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred")
	}
	state := m.startupDeferredTranscript
	userIdx := -1
	for i, block := range state.allBlocks {
		if block != nil && block.Type == BlockUser {
			userIdx = i
			break
		}
	}
	if userIdx <= 0 {
		t.Fatalf("expected the user card below the transcript start, found index %d", userIdx)
	}
	// Move the window to the transcript start so the current card sits above
	// the first user card and { has nothing to jump to.
	if !m.maybeSwitchStartupDeferredTranscriptWindow(startupTranscriptWindowTop, "test") {
		t.Fatal("failed to switch the deferred window to the top")
	}
	offsetBefore := m.viewport.offset

	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "{", Code: '{'})); cmd != nil {
		t.Fatalf("{ above the first user card should move synchronously, got %#v", cmd)
	}
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("{ at the top must not hydrate the deferred transcript")
	}
	if m.focusedBlockID != -1 {
		t.Fatalf("focusedBlockID after boundary { = %d, want -1 (unchanged)", m.focusedBlockID)
	}
	if m.viewport.offset != offsetBefore {
		t.Fatalf("offset after boundary { = %d, want %d", m.viewport.offset, offsetBefore)
	}
}

func TestDeferredCountedTypeJumpSaturatesAtFirstMatch(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+130)
	for i := range startupTranscriptWindowMinBlocks + 130 {
		role := message.RoleAssistant
		content := fmt.Sprintf("message-%03d", i)
		if i == 3 {
			role = message.RoleUser
			content = "user-message-003"
		}
		messages = append(messages, message.Message{Role: role, Content: content})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)
	m.mode = ModeNormal

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred")
	}
	state := m.startupDeferredTranscript
	userIdx := 3
	if state.allBlocks[userIdx].Type != BlockUser {
		t.Fatal("test setup: expected a user card at index 3")
	}

	// 99{ from the tail must land on the first (and only) user card and then
	// saturate: the out-of-range iterations must not hydrate or move the view.
	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "9", Code: '9'}))
	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "9", Code: '9'}))
	cmd = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "{", Code: '{'}))
	applyTestCmd(t, &m, cmd)

	if !m.hasDeferredStartupTranscript() {
		t.Fatal("99{ must not hydrate the deferred transcript")
	}
	if m.focusedBlockID != state.allBlocks[userIdx].ID {
		t.Fatalf("focusedBlockID after 99{ = %d, want first user card %d", m.focusedBlockID, state.allBlocks[userIdx].ID)
	}
	if got := m.viewport.GetBlockAtOffset(); got == nil || got.Content != "user-message-003" {
		t.Fatalf("block at viewport offset after 99{ = %#v, want the first user card", got)
	}
	offsetBefore := m.viewport.offset

	// A further { past the boundary must be a strict no-op.
	cmd = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "{", Code: '{'}))
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("boundary { must not hydrate the deferred transcript")
	}
	if m.focusedBlockID != state.allBlocks[userIdx].ID {
		t.Fatalf("focusedBlockID after boundary { = %d, want unchanged", m.focusedBlockID)
	}
	if m.viewport.offset != offsetBefore {
		t.Fatalf("offset after boundary { = %d, want unchanged %d", m.viewport.offset, offsetBefore)
	}
}

func TestNormalModeBraceSkipsLocalShellCards(t *testing.T) {
	m := NewModelWithSize(nil, 80, 10)
	m.mode = ModeNormal
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, Content: "u1"})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockUser, Content: "!ls", UserLocalShellCmd: "ls", MsgIndex: -1})
	m.viewport.AppendBlock(&Block{ID: 3, Type: BlockUser, Content: "!", UserLocalShell: true, MsgIndex: -1})
	m.viewport.AppendBlock(&Block{ID: 4, Type: BlockUser, Content: "u2"})
	m.viewport.ScrollToTop()
	// Local !shell cards are BlockUser but never begin a turn: }/{ must skip
	// them and stop only on real user messages.
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "}", Code: '}'})); cmd != nil {
		t.Fatalf("} should move synchronously, got %#v", cmd)
	}
	if m.focusedBlockID != 4 {
		t.Fatalf("focusedBlockID after } = %d, want 4 (skipping non-empty and empty local shell cards)", m.focusedBlockID)
	}
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "{", Code: '{'})); cmd != nil {
		t.Fatalf("{ should move synchronously, got %#v", cmd)
	}
	if m.focusedBlockID != 1 {
		t.Fatalf("focusedBlockID after { = %d, want 1 (skipping the local shell card)", m.focusedBlockID)
	}
}

func TestDeferredDirectoryJumpToFirstEntry(t *testing.T) {
	messages := make([]message.Message, 0, startupTranscriptWindowMinBlocks+130)
	for i := range startupTranscriptWindowMinBlocks + 130 {
		messages = append(messages, message.Message{Role: message.RoleAssistant, Content: fmt.Sprintf("message-%03d", i)})
	}
	backend := &sessionControlAgent{resumePending: true, startupResumeID: "123", messages: messages}
	m := NewModelWithSize(backend, 120, 24)
	m.mode = ModeNormal

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("startup transcript should remain deferred")
	}
	// The first transcript block has BlockID 0; entering the directory on the
	// first entry must jump through the deferred locator, not apply its
	// full-transcript LineOffset to the window-relative viewport.
	if cmd := m.handleNormalKey(ctrlKeyPress('t')); cmd != nil {
		t.Fatalf("ctrl+t should open deferred directory synchronously, got %#v", cmd)
	}
	if m.mode != ModeDirectory {
		t.Fatalf("mode = %v, want ModeDirectory", m.mode)
	}
	if m.dirEntries[0].BlockID != 0 {
		t.Fatalf("first directory entry BlockID = %d, want 0", m.dirEntries[0].BlockID)
	}
	m.dirList.SetCursor(0)
	if cmd := m.handleDirectoryKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})); cmd != nil {
		t.Fatalf("enter should jump synchronously, got %#v", cmd)
	}
	if !m.hasDeferredStartupTranscript() {
		t.Fatal("directory jump must not hydrate the deferred transcript")
	}
	if state := m.startupDeferredTranscript; state.windowStart != 0 {
		t.Fatalf("window after jumping to first entry = [%d,%d), want it to start at 0", state.windowStart, state.windowEnd)
	}
	if got := m.viewport.GetBlockAtOffset(); got == nil || got.Content != "message-000" {
		t.Fatalf("block at viewport offset after first-entry jump = %#v, want message-000", got)
	}
}

func TestDirectoryCursorFallsBackToViewportTopWhenFocusStale(t *testing.T) {
	m := NewModelWithSize(nil, 80, 10)
	m.mode = ModeNormal
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "a1"})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockUser, Content: "u2"})
	m.viewport.AppendBlock(&Block{ID: 3, Type: BlockAssistant, Content: "a3"})
	m.viewport.AppendBlock(&Block{ID: 4, Type: BlockUser, Content: "u4"})
	m.viewport.ScrollToTop()
	// A stale focusedBlockID must not pin the directory cursor to the first
	// entry; it falls back to the viewport-top card like the no-focus path.
	m.focusedBlockID = 99
	m.refreshBlockFocus()
	if cmd := m.handleNormalKey(ctrlKeyPress('t')); cmd != nil {
		t.Fatalf("ctrl+t should open directory synchronously, got %#v", cmd)
	}
	wantID := m.viewport.GetBlockAtOffset().ID
	idx := m.dirList.CursorAt()
	if m.dirEntries[idx].BlockID != wantID {
		t.Fatalf("directory cursor block = %d, want viewport-top block %d", m.dirEntries[idx].BlockID, wantID)
	}
}

// TestNormalModeTypeJumpClearsStickyWhenLeavingBottom pins that a structural
// jump away from the tail must drop the sticky tail-follow flag, so streaming
// content appended afterwards does not yank the viewport back to the bottom.
func TestNormalModeTypeJumpClearsStickyWhenLeavingBottom(t *testing.T) {
	m := jumpTypeTestModel()
	m.viewport.ScrollToBottom()
	m.focusedBlockID = 5
	m.refreshBlockFocus()
	if !m.viewport.sticky {
		t.Fatal("setup: expected sticky at the bottom")
	}
	// ( from the assistant card at the bottom jumps back to the previous
	// assistant card, far above the bottom edge.
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "(", Code: '('})); cmd != nil {
		t.Fatalf("( should move synchronously, got %#v", cmd)
	}
	if m.focusedBlockID != 2 {
		t.Fatalf("focusedBlockID after ( = %d want 2", m.focusedBlockID)
	}
	if m.viewport.sticky {
		t.Fatal("structural jump away from the bottom must clear sticky")
	}
	if m.viewport.atBottom() {
		t.Fatal("jump target should be above the bottom")
	}
	// Streaming content appended while off-the-bottom must leave the offset alone.
	offsetAfterJump := m.viewport.offset
	m.viewport.AppendBlock(&Block{ID: 9, Type: BlockAssistant, Content: "d1"})
	if m.viewport.offset != offsetAfterJump {
		t.Fatalf("append while off-the-bottom moved the offset: %d -> %d", offsetAfterJump, m.viewport.offset)
	}
	if m.viewport.sticky {
		t.Fatal("append must not re-enable sticky while off-the-bottom")
	}
}

// TestNormalModeTypeJumpKeepsStickyWhenTranscriptFits pins the short-transcript
// case: a jump whose target clamps to the bottom (the whole transcript is
// shorter than the viewport) must keep sticky, so new content keeps flowing
// down and follows the tail once it fills the viewport.
func TestNormalModeTypeJumpKeepsStickyWhenTranscriptFits(t *testing.T) {
	m := NewModelWithSize(nil, 80, 10)
	m.mode = ModeNormal
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, Content: "u1"})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockUser, Content: "u2"})
	m.focusedBlockID = 1
	m.refreshBlockFocus()
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "}", Code: '}'})); cmd != nil {
		t.Fatalf("} should move synchronously, got %#v", cmd)
	}
	if m.focusedBlockID != 2 {
		t.Fatalf("focusedBlockID after } = %d want 2", m.focusedBlockID)
	}
	if !m.viewport.sticky {
		t.Fatal("jump that stays at the bottom (transcript fits the viewport) must keep sticky")
	}
	if !m.viewport.atBottom() {
		t.Fatal("expected the viewport at the bottom after the fitted-transcript jump")
	}
	// Content beyond the viewport height must keep auto-scrolling to the tail.
	m.viewport.AppendBlock(&Block{ID: 3, Type: BlockAssistant, Content: strings.Repeat("d\n", 20)})
	if !m.viewport.sticky {
		t.Fatal("sticky must survive appends once at the bottom")
	}
	if !m.viewport.atBottom() {
		t.Fatalf("viewport should keep following the tail: offset=%d total=%d height=%d", m.viewport.offset,
			m.viewport.TotalLines(), m.viewport.height)
	}
}

// TestNormalModeJKClearsStickyWhenLeavingBottom pins that j/k navigation away
// from the tail follows the same sticky discipline as structural jumps.
func TestNormalModeJKClearsStickyWhenLeavingBottom(t *testing.T) {
	m := jumpTypeTestModel()
	m.viewport.ScrollToBottom()
	if !m.viewport.sticky {
		t.Fatal("setup: expected sticky at the bottom")
	}
	m.focusedBlockID = 5
	m.refreshBlockFocus()
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "k", Code: 'k'})); cmd != nil {
		t.Fatalf("k should move synchronously, got %#v", cmd)
	}
	if m.focusedBlockID != 4 {
		t.Fatalf("focusedBlockID after k = %d want 4", m.focusedBlockID)
	}
	if m.viewport.sticky {
		t.Fatal("k away from the bottom must clear sticky")
	}
	if m.viewport.atBottom() {
		t.Fatal("k target should be above the bottom")
	}
	offsetAfterK := m.viewport.offset
	m.viewport.AppendBlock(&Block{ID: 9, Type: BlockAssistant, Content: "d1"})
	if m.viewport.offset != offsetAfterK {
		t.Fatalf("append while off-the-bottom moved the offset: %d -> %d", offsetAfterK, m.viewport.offset)
	}
	if m.viewport.sticky {
		t.Fatal("append must not re-enable sticky while off-the-bottom")
	}
}
