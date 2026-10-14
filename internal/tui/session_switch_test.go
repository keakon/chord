package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/tui/overlaylist"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

func TestStartupRestoreRenormalizesSidebarFilesForUpdatedWorkDir(t *testing.T) {
	contentRoot := t.TempDir()
	editedPath := filepath.Join(contentRoot, "internal", "tui", "app.go")
	args, err := json.Marshal(map[string]string{
		"path":       editedPath,
		"old_string": "old",
		"new_string": "new",
	})
	if err != nil {
		t.Fatalf("marshal edit args: %v", err)
	}
	backend := &sessionControlAgent{
		resumePending:   true,
		startupResumeID: "123",
		contentRoot:     contentRoot,
		workDir:         contentRoot,
		messages: []message.Message{
			{Role: "assistant", ToolCalls: []message.ToolCall{{
				ID:   "call-edit-root-1",
				Name: tools.NameEdit,
				Args: args,
			}}},
			{
				Role:            "tool",
				ToolCallID:      "call-edit-root-1",
				ToolStatus:      string(message.ToolStatusSuccess),
				ToolDiff:        "diff",
				ToolDiffAdded:   2,
				ToolDiffRemoved: 1,
			},
		},
	}
	m := NewModelWithSize(backend, 120, 24)
	m.workingDir = t.TempDir()
	m.sidebar.SetWorkingDir(m.workingDir)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)

	edits := m.sidebar.CurrentAgentFiles()
	if got := len(edits); got != 1 {
		t.Fatalf("sidebar edits after startup restore = %d, want 1: %+v", got, edits)
	}
	want := filepath.Join("internal", "tui", "app.go")
	if edits[0].Path != want {
		t.Fatalf("sidebar path after startup restore = %q, want %q", edits[0].Path, want)
	}
}

func TestHandleInsertKeyRoutesNewSessionCommandViaControlAPI(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeInsert
	m.input.SetValue("/new")

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	if backend.newSessionCalls != 1 {
		t.Fatalf("NewSession() calls = %d, want 1", backend.newSessionCalls)
	}
	if got := len(backend.sentMessages); got != 0 {
		t.Fatalf("SendUserMessage() should not be used, got %d calls", got)
	}
	if got := len(m.viewport.visibleBlocks()); got != 0 {
		t.Fatalf("viewport should not append a user block for /new, got %d blocks", got)
	}
}

func TestHandleAgentEventLoopNoticeNearBottomScrollsToBottom(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 80, 8)
	for i := range 6 {
		m.viewport.AppendBlock(&Block{ID: i + 1, Type: BlockAssistant, Content: strings.Repeat("alpha\n", 2)})
	}
	m.viewport.ScrollToBottom()
	m.viewport.offset = max(0, m.viewport.offset-1)
	m.viewport.sticky = false

	_ = m.handleAgentEvent(agentEventMsg{event: agent.LoopNoticeEvent{
		Title: "LOOP",
		Text:  strings.Repeat("continue\n", 4),
	}})

	if !m.viewport.atBottom() {
		t.Fatalf("viewport should scroll to bottom after near-bottom loop notice append: offset=%d total=%d height=%d", m.viewport.offset, m.viewport.TotalLines(), m.viewport.height)
	}
	blocks := m.viewport.visibleBlocks()
	if len(blocks) == 0 || blocks[len(blocks)-1].Type != BlockStatus {
		t.Fatalf("last block = %#v, want trailing BlockStatus", blocks)
	}
}

func TestHandleInsertKeyRoutesResumeEnterViaControlAPI(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeInsert
	m.input.SetValue("/resume")

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	if backend.resumeCalls != 1 {
		t.Fatalf("ResumeSession() calls = %d, want 1", backend.resumeCalls)
	}
	if got := len(backend.resumeIDs); got != 0 {
		t.Fatalf("ResumeSessionID() calls = %+v, want none", backend.resumeIDs)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input value = %q, want empty", got)
	}
}

func TestHandleInsertKeyRoutesResumeIDViaControlAPI(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeInsert
	m.input.SetValue("/resume 123")

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	if len(backend.resumeIDs) != 1 || backend.resumeIDs[0] != "123" {
		t.Fatalf("ResumeSessionID() calls = %+v, want [123]", backend.resumeIDs)
	}
	if got := len(backend.sentMessages); got != 0 {
		t.Fatalf("SendUserMessage() should not be used, got %d calls", got)
	}
	if !m.sessionSwitch.active() {
		t.Fatal("session switch indicator should be active after /resume <id>")
	}
	if m.sessionSwitch.kind != "resume" || m.sessionSwitch.sessionID != "123" {
		t.Fatalf("sessionSwitch = %+v, want kind=resume session=123", m.sessionSwitch)
	}
}

func TestHandleAgentEventLoopNoticeCreatesStatusCard(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 80, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.LoopNoticeEvent{
		Title: "LOOP",
		Text:  "Target:\n- Continue and finish all remaining tasks in the current session.",
	}})
	applyTestCmd(t, &m, cmd)

	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 1 {
		t.Fatalf("visible block count = %d, want 1", len(blocks))
	}
	if blocks[0].Type != BlockStatus {
		t.Fatalf("block type = %v, want BlockStatus", blocks[0].Type)
	}
	if blocks[0].StatusTitle != "LOOP" {
		t.Fatalf("StatusTitle = %q, want %q", blocks[0].StatusTitle, "LOOP")
	}
	if !blocks[0].Collapsed {
		t.Fatal("loop notices must start collapsed")
	}
	collapsed := terminaltext.StripANSI(strings.Join(blocks[0].Render(80, ""), "\n"))
	if !strings.Contains(collapsed, "LOOP") || !strings.Contains(collapsed, "▸") || strings.Contains(collapsed, "Target:") {
		t.Fatalf("rendered card = %q, want a collapsed LOOP badge with the body hidden", collapsed)
	}
	if !blocks[0].ToggleAtWidth(80) || blocks[0].Collapsed {
		t.Fatal("toggling must expand the notice")
	}
	plain := terminaltext.StripANSI(strings.Join(blocks[0].Render(80, ""), "\n"))
	if !strings.Contains(plain, "  Target:") || !strings.Contains(plain, "  • Continue and finish all remaining tasks in the current session.") {
		t.Fatalf("rendered card = %q, want indented LOOP body", plain)
	}
}

func TestLoopStateChangedEventRefreshesStatusBarDuringStreaming(t *testing.T) {
	backend := &sessionControlAgent{currentRole: "builder"}
	m := NewModelWithSize(backend, 120, 24)
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{AgentID: "main", Text: "partial"}})

	backend.loopState = agent.LoopStateExecuting
	backend.loopTarget = "finish current task"
	backend.loopIteration = 1
	backend.loopMaxIterations = 10
	_ = m.handleAgentEvent(agentEventMsg{event: agent.LoopStateChangedEvent{}})

	plain := terminaltext.StripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "LOOP 1/10") {
		t.Fatalf("status bar = %q, want immediate loop pill while streaming", plain)
	}
}

func TestHandleAgentEventLoopContinueStatusCardBodyIsIndented(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 80, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.LoopNoticeEvent{
		Title: "LOOP CONTINUE",
		Text:  "Unresolved work:\n- pending verification\n- remaining subagent",
	}})
	applyTestCmd(t, &m, cmd)

	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 1 {
		t.Fatalf("visible block count = %d, want 1", len(blocks))
	}
	if blocks[0].Type != BlockStatus {
		t.Fatalf("block type = %v, want BlockStatus", blocks[0].Type)
	}
	if blocks[0].StatusTitle != "LOOP CONTINUE" {
		t.Fatalf("StatusTitle = %q, want %q", blocks[0].StatusTitle, "LOOP CONTINUE")
	}
	card := blocks[0]
	if !card.Collapsed {
		t.Fatal("runtime loop notices must start collapsed")
	}
	if !card.ToggleAtWidth(80) || card.Collapsed {
		t.Fatal("toggling must expand the runtime loop notice")
	}
	plain := terminaltext.StripANSI(strings.Join(card.Render(80, ""), "\n"))
	for _, want := range []string{"  Unresolved work:", "  • pending verification", "  • remaining subagent"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("rendered card = %q, want %q in the full indented body", plain, want)
		}
	}
	if strings.Contains(plain, "more lines hidden.") {
		t.Fatalf("rendered card = %q, want no hidden-line hint", plain)
	}
}

func TestHandleInsertKeyRoutesLoopCommandsViaControlAPI(t *testing.T) {
	backend := &sessionControlAgent{loopState: agent.LoopStateAssessing}
	m := NewModel(backend)
	m.mode = ModeInsert
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}

	m.input.SetValue("/loop on finish current task")
	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m.input.SetValue("/loop off")
	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	if got := len(backend.sentMessages); got != 2 || backend.sentMessages[0] != "/loop on finish current task" || backend.sentMessages[1] != "/loop off" {
		t.Fatalf("sentMessages = %#v, want forwarded loop commands", backend.sentMessages)
	}
}

func TestHandleInsertKeyStartsLoopTargetImmediatelyWhenIdle(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeInsert
	m.input.SetValue("/loop on finish current task")

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	if got := len(backend.sentMessages); got != 1 || backend.sentMessages[0] != "/loop on finish current task" {
		t.Fatalf("sentMessages = %#v, want forwarded loop-on command", backend.sentMessages)
	}
	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 0 {
		t.Fatalf("visible blocks = %#v, want no user loop block; loop start should be shown via control card event", blocks)
	}
}

func TestHandleInsertKeyBareLoopEnterForwardsImmediately(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeInsert
	m.input.SetValue("/loop on")

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	if got := len(backend.sentMessages); got != 1 || backend.sentMessages[0] != "/loop on" {
		t.Fatalf("sentMessages = %#v, want bare /loop on forwarded to agent", backend.sentMessages)
	}
	if m.activeToast != nil {
		t.Fatalf("active toast = %#v, want none; usage should come from agent/runtime", m.activeToast)
	}
}

func TestHandleInsertKeyShowsLoopUsageForBareLoopOnWhenIdle(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeInsert
	m.input.SetValue("/loop on")

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	if len(backend.sentMessages) != 1 || backend.sentMessages[0] != "/loop on" {
		t.Fatalf("sentMessages = %#v, want bare /loop on forwarded to agent", backend.sentMessages)
	}
	if backend.loopTarget != "" {
		t.Fatalf("loopTarget = %q, want empty because TUI no longer handles /loop locally", backend.loopTarget)
	}
}

func TestHandleInsertHistoryUpLoadsLastUserMessageIntoComposer(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{{
		Role: "user",
		Parts: []message.ContentPart{
			{Type: "text", Text: "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk", DisplayText: "[Pasted text #1 +11 lines]"},
			{Type: "image", MimeType: "image/png", Data: []byte{1, 2, 3}, FileName: "shot.png"},
		},
	}}}
	m := NewModel(backend)
	m.mode = ModeInsert

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))

	if got := m.input.Value(); got != "[Pasted text #1 +11 lines][shot.png]" {
		t.Fatalf("input value = %q, want inline text placeholder followed by image token", got)
	}
	if !m.input.HasInlinePastes() {
		t.Fatal("expected inline paste metadata to be restored")
	}
	if got := len(m.attachments); got != 1 {
		t.Fatalf("len(attachments) = %d, want 1", got)
	}
	if got := m.attachments[0].FileName; got != "shot.png" {
		t.Fatalf("attachment filename = %q, want shot.png", got)
	}
}

func TestHandleInsertHistoryUpSkipsSyntheticUserMessages(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{
		{Role: message.RoleUser, Content: "real prompt"},
		{Role: message.RoleUser, Content: "[Context Summary]\narchived history", IsCompactionSummary: true},
		{Role: message.RoleUser, Content: "job finished", Kind: message.KindBackgroundResult},
		{Role: message.RoleUser, Content: "mailbox delivery", Kind: message.KindSubAgentMailbox},
		{Role: message.RoleUser, Content: "loop notice", Kind: message.KindLoopNotice},
		{Role: message.RoleUser, Content: "context notice", Kind: message.KindContextNotice},
		{Role: message.RoleUser, Content: "stream continue", Kind: message.KindStreamContinue},
		{Role: message.RoleUser, Content: "hook feedback", Kind: message.KindHookFeedback},
	}}
	m := NewModel(backend)
	m.mode = ModeInsert

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))

	if got := m.input.Value(); got != "real prompt" {
		t.Fatalf("input value = %q, want last user-authored prompt", got)
	}
}

func TestHandleInsertHistoryUpWithoutUserAuthoredMessagesUsesTypedHistory(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{
		{Role: message.RoleUser, Content: "[Context Summary]\narchived history", IsCompactionSummary: true},
	}}
	m := NewModel(backend)
	m.mode = ModeInsert
	m.input.AddHistory("typed earlier")

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))

	if got := m.input.Value(); got != "typed earlier" {
		t.Fatalf("input value = %q, want typed input history entry", got)
	}
}

func TestRenderStatusBarShowsSessionSwitchProgress(t *testing.T) {
	backend := &sessionControlAgent{providerModelRef: "openai/gpt-5.5"}
	m := NewModelWithSize(backend, 120, 24)
	m.beginSessionSwitch("resume", "123")

	plain := terminaltext.StripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "Resuming 123...") {
		t.Fatalf("status bar = %q, want resume progress", plain)
	}
}

func TestViewShowsSessionSwitchOverlayAfterDelay(t *testing.T) {
	backend := &sessionControlAgent{providerModelRef: "openai/gpt-5.5"}
	m := NewModelWithSize(backend, 120, 24)
	m.beginSessionSwitch("resume", "123")
	m.sessionSwitch.startedAt = time.Now().Add(-sessionSwitchOverlayDelay - 10*time.Millisecond)
	m.layout = m.generateLayout(m.width, m.height)

	plain := terminaltext.StripANSI(m.View().Content)
	if !strings.Contains(plain, "Resuming session") {
		t.Fatalf("View() = %q, want session switch overlay title", plain)
	}
	if !strings.Contains(plain, "123") {
		t.Fatalf("View() = %q, want session switch overlay session badge", plain)
	}
}

func TestViewDelaysSessionSwitchOverlayToAvoidFlash(t *testing.T) {
	backend := &sessionControlAgent{providerModelRef: "openai/gpt-5.5"}
	m := NewModelWithSize(backend, 120, 24)
	m.beginSessionSwitch("resume", "123")
	m.layout = m.generateLayout(m.width, m.height)

	plain := terminaltext.StripANSI(m.View().Content)
	if strings.Contains(plain, "Resuming session") {
		t.Fatalf("View() = %q, should not show delayed session switch overlay yet", plain)
	}
}

func TestRenderDisabledInputAreaPreservesTrailingNewline(t *testing.T) {
	rendered := renderDisabledInputArea("line 1\nline 2\n")
	if !strings.HasSuffix(rendered, "\n") {
		t.Fatalf("rendered = %q, want trailing newline preserved", rendered)
	}
	if !strings.Contains(rendered, "\x1b[") {
		t.Fatalf("rendered = %q, want ANSI styling for dimmed disabled input", rendered)
	}
}

func TestViewHidesInputCursorWhileSessionSwitchInteractionSuppressed(t *testing.T) {
	backend := &sessionControlAgent{providerModelRef: "openai/gpt-5.5"}
	m := NewModelWithSize(backend, 120, 24)
	m.input.Focus()
	m.beginSessionSwitch("resume", "123")
	m.layout = m.generateLayout(m.width, m.height)

	view := m.View()
	if view.Cursor != nil {
		t.Fatalf("View().Cursor = %#v, want nil while interaction is suppressed", view.Cursor)
	}
}

func TestSessionSwitchStartedEventUpdatesStatusBarAndRestoredClearsIt(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{{Role: "assistant", Content: "restored"}}, providerModelRef: "openai/gpt-5.5"}
	m := NewModelWithSize(backend, 120, 24)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.SessionSwitchStartedEvent{Kind: "resume", SessionID: "123"}})
	plain := terminaltext.StripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "Resuming 123...") {
		t.Fatalf("status bar after start = %q, want resume progress", plain)
	}

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if m.sessionSwitch.active() {
		t.Fatal("session switch indicator should clear after SessionRestoredEvent")
	}
	plain = terminaltext.StripANSI(m.renderStatusBar())
	if strings.Contains(plain, "Resuming 123...") {
		t.Fatalf("status bar after restore = %q, should not show stale resume progress", plain)
	}
}

func TestSessionRestoredEventClearsEditedFilesForEmptyNewSession(t *testing.T) {
	backend := &sessionControlAgent{
		messages: []message.Message{{Role: "assistant", Content: "old message"}},
	}
	m := NewModelWithSize(backend, 120, 24)
	m.refreshSidebar()

	// simulate previous session had edited files
	m.sidebar.AddFileEdit("main", "handler.go", 1, 0)
	m.sidebar.AddFileEdit("main", "main.go", 2, 1)
	if got := len(m.sidebar.agents[0].EditedFiles); got != 2 {
		t.Fatalf("seed: edited files count = %d, want 2", got)
	}

	// simulate /new: agent resets session to empty messages
	backend.messages = nil
	m.beginSessionSwitch("new", "")

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)

	if got := len(m.sidebar.agents[0].EditedFiles); got != 0 {
		t.Fatalf("edited files count after new session restore = %d, want 0 (files=%v)", got, m.sidebar.agents[0].EditedFiles)
	}
}

func TestSessionSwitchIndicatorClearsOnError(t *testing.T) {
	m := NewModelWithSize(nil, 120, 24)
	m.beginSessionSwitch("resume", "123")

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ErrorEvent{Err: errors.New("resume failed")}})

	if m.sessionSwitch.active() {
		t.Fatal("session switch indicator should clear on ErrorEvent")
	}
}

func TestSelectSessionAtCursorStartsStatusIndicator(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 100, 30)
	m.mode = ModeSessionSelect
	m.sessionSelect = sessionSelectState{
		options: []agent.SessionSummary{{ID: "123"}, {ID: "456"}},
	}
	m.sessionSelect.selector.list = overlaylist.New(nil, 5)
	m.rebuildSessionSelectFilteredView(false)
	m.sessionSelect.selector.list.SetCursor(1)

	_ = m.selectSessionAtCursor()

	if len(backend.resumeIDs) != 1 || backend.resumeIDs[0] != "456" {
		t.Fatalf("ResumeSessionID() calls = %+v, want [456]", backend.resumeIDs)
	}
	if !m.sessionSwitch.active() || m.sessionSwitch.kind != "resume" || m.sessionSwitch.sessionID != "456" {
		t.Fatalf("sessionSwitch = %+v, want active resume/456", m.sessionSwitch)
	}
}

func TestHandleSessionSelectKeyDeleteOpensConfirmOverlay(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 100, 30)
	m.mode = ModeSessionSelect
	m.sessionSelect = sessionSelectState{
		options: []agent.SessionSummary{{ID: "123"}, {ID: "456", ForkedFrom: "100"}},
	}
	m.sessionSelect.selector.list = overlaylist.New(nil, 5)
	m.rebuildSessionSelectFilteredView(false)
	m.sessionSelect.selector.list.SetCursor(1)

	_ = m.handleSessionSelectKey(tea.KeyPressMsg(tea.Key{Code: 'd'}))

	if m.mode != ModeSessionDeleteConfirm {
		t.Fatalf("mode = %v, want ModeSessionDeleteConfirm", m.mode)
	}
	if m.sessionDeleteConfirm.session == nil || m.sessionDeleteConfirm.session.ID != "456" {
		t.Fatalf("sessionDeleteConfirm = %+v, want selected session 456", m.sessionDeleteConfirm)
	}
}

func TestHandleSessionDeleteConfirmKeyDeletesSelectedSessionAndUpdatesList(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 100, 30)
	m.mode = ModeSessionDeleteConfirm
	m.sessionSelect = sessionSelectState{
		options: []agent.SessionSummary{{ID: "123"}, {ID: "456"}},
	}
	m.sessionSelect.selector.list = overlaylist.New(nil, 5)
	m.rebuildSessionSelectFilteredView(false)
	m.sessionSelect.selector.list.SetCursor(1)
	m.sessionDeleteConfirm = sessionDeleteConfirmState{
		session:  &agent.SessionSummary{ID: "456"},
		prevMode: ModeSessionSelect,
	}

	cmd := m.handleSessionDeleteConfirmKey(tea.KeyPressMsg(tea.Key{Text: "y", Code: 'y'}))

	if cmd == nil {
		t.Fatal("expected delete confirm command")
	}
	if got := backend.deleteSessionIDs; len(got) != 1 || got[0] != "456" {
		t.Fatalf("DeleteSession() calls = %+v, want [456]", got)
	}
	if m.mode != ModeSessionSelect {
		t.Fatalf("mode = %v, want ModeSessionSelect", m.mode)
	}
	if len(m.sessionSelect.options) != 1 || m.sessionSelect.options[0].ID != "123" {
		t.Fatalf("sessionSelect.options = %+v, want only session 123 remaining", m.sessionSelect.options)
	}
	if m.activeToast == nil || !strings.Contains(m.activeToast.Message, "Deleted session 456") {
		t.Fatalf("activeToast = %+v, want delete success toast", m.activeToast)
	}
}

func TestHandleSessionDeleteConfirmKeyShowsErrorAndKeepsListOnFailure(t *testing.T) {
	backend := &sessionControlAgent{deleteSessionErr: errors.New("locked")}
	m := NewModelWithSize(backend, 100, 30)
	m.mode = ModeSessionDeleteConfirm
	m.sessionSelect = sessionSelectState{
		options: []agent.SessionSummary{{ID: "123"}, {ID: "456"}},
	}
	m.sessionSelect.selector.list = overlaylist.New(nil, 5)
	m.rebuildSessionSelectFilteredView(false)
	m.sessionSelect.selector.list.SetCursor(1)
	m.sessionDeleteConfirm = sessionDeleteConfirmState{
		session:  &agent.SessionSummary{ID: "456"},
		prevMode: ModeSessionSelect,
	}

	cmd := m.handleSessionDeleteConfirmKey(tea.KeyPressMsg(tea.Key{Text: "y", Code: 'y'}))

	if cmd == nil {
		t.Fatal("expected delete error toast command")
	}
	if len(m.sessionSelect.options) != 2 {
		t.Fatalf("sessionSelect.options = %+v, want unchanged list", m.sessionSelect.options)
	}
	if m.activeToast == nil || !strings.Contains(m.activeToast.Message, "locked") {
		t.Fatalf("activeToast = %+v, want delete error toast", m.activeToast)
	}
}

func TestHandleInsertDiagnosticsShortcutStaysLocal(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeInsert
	m.input.SetValue("draft")

	cmd := m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))

	if cmd == nil {
		t.Fatal("expected diagnostics shortcut command")
	}
	if got := len(backend.sentMessages); got != 0 {
		t.Fatalf("SendUserMessage() calls = %d, want 0", got)
	}
	if got := len(backend.resumeIDs); got != 0 {
		t.Fatalf("ResumeSessionID() calls = %d, want 0", got)
	}
	if got := len(m.viewport.visibleBlocks()); got != 0 {
		t.Fatalf("viewport should remain unchanged, got %d blocks", got)
	}
	if got := m.input.Value(); got != "draft" {
		t.Fatalf("input value = %q, want draft preserved", got)
	}
}

func TestHandleNormalDiagnosticsShortcutReturnsCmd(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeNormal

	cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))
	if cmd == nil {
		t.Fatal("expected diagnostics shortcut command")
	}
}

func TestNewModelShowsStartupResumeStatusAndKeepsInteractionSuppressedUntilRestored(t *testing.T) {
	backend := &sessionControlAgent{
		resumePending:   true,
		startupResumeID: "123",
		messages: []message.Message{
			{Role: "assistant", Content: "restored assistant"},
		},
	}

	m := NewModelWithSize(backend, 120, 24)

	if !m.startupRestorePending {
		t.Fatal("startupRestorePending should remain true until SessionRestoredEvent arrives")
	}
	if !m.sessionSwitch.active() || m.sessionSwitch.kind != "resume" || m.sessionSwitch.sessionID != "123" {
		t.Fatalf("sessionSwitch = %+v, want active resume/123", m.sessionSwitch)
	}
	plain := terminaltext.StripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "Resuming 123...") {
		t.Fatalf("status bar = %q, want startup resume progress", plain)
	}
	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 0 {
		t.Fatalf("visibleBlocks() = %#v, want startup loading placeholder before SessionRestoredEvent", blocks)
	}

	updated, cmd := m.Update(tea.KeyPressMsg(tea.Key{Text: "i", Code: 'i'}))
	if cmd != nil {
		t.Fatal("suppressed interaction should not return a command")
	}
	got, ok := updated.(*Model)
	if !ok {
		t.Fatalf("updated model type = %T, want *Model", updated)
	}
	if got.mode != ModeInsert {
		t.Fatalf("mode = %v, want insert unchanged while startup restore pending", got.mode)
	}

	cmd = m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	if m.startupRestorePending {
		t.Fatal("startupRestorePending should clear after SessionRestoredEvent")
	}
	blocks = m.viewport.visibleBlocks()
	if len(blocks) != 1 || blocks[0].Content != "restored assistant" {
		t.Fatalf("visibleBlocks() = %#v, want rebuilt restored transcript after SessionRestoredEvent", blocks)
	}
}

func TestFocusedAgentSwitchSmallTranscriptRemainsFullyVisible(t *testing.T) {
	subMessages := make([]message.Message, 0, startupTranscriptWindowMinBlocks-1)
	for i := range startupTranscriptWindowMinBlocks - 1 {
		subMessages = append(subMessages, message.Message{Role: "assistant", Content: fmt.Sprintf("sub-%03d", i)})
	}
	backend := &sessionControlAgent{messagesByFocus: map[string][]message.Message{"agent-1": subMessages}}
	m := NewModelWithSize(backend, 120, 24)

	m.setFocusedAgent("agent-1")

	blocks := m.viewport.visibleBlocks()
	if len(blocks) != len(subMessages) {
		t.Fatalf("len(visibleBlocks()) = %d, want full small transcript %d", len(blocks), len(subMessages))
	}
	if m.hasDeferredStartupTranscript() {
		t.Fatal("small focus switch transcript should not be windowed")
	}
}

func TestFocusedAgentContinueStagesUserBlockBeforeAsyncSend(t *testing.T) {
	backend := &sessionControlAgent{
		focused: "agent-1",
		messagesByFocus: map[string][]message.Message{
			"agent-1": {{Role: message.RoleAssistant, Content: "done", StopReason: "stop"}},
		},
	}
	m := NewModelWithSize(backend, 120, 24)
	m.focusedAgentID = "agent-1"
	m.viewport.SetFilter("agent-1")

	msgs := runCmdTree(m.tryContinue())
	var action focusedContinueActionMsg
	for _, msg := range msgs {
		if candidate, ok := msg.(focusedContinueActionMsg); ok {
			action = candidate
			break
		}
	}
	if action.action == 0 {
		t.Fatalf("tryContinue messages = %#v, want focused continue action", msgs)
	}
	model, next := m.Update(action)
	updated := model.(*Model)

	blocks := updated.viewport.visibleBlocks()
	if len(blocks) != 1 || blocks[0].Type != BlockUser || blocks[0].Content != "continue" || blocks[0].AgentID != "agent-1" {
		t.Fatalf("staged focused continue block = %#v", blocks)
	}
	if updated.inflightDraft == nil || updated.inflightDraft.AgentID != "agent-1" || updated.inflightDraft.Content != "continue" {
		t.Fatalf("inflight focused continue draft = %#v", updated.inflightDraft)
	}
	if len(backend.sentMessages) != 0 {
		t.Fatalf("messages sent before async command = %#v", backend.sentMessages)
	}
	runCmdTree(next)
	if len(backend.sentMessages) != 1 || backend.sentMessages[0] != "continue" {
		t.Fatalf("async focused continue messages = %#v", backend.sentMessages)
	}
}

func TestFocusedContinueActionsKeepCapturedTarget(t *testing.T) {
	tests := []struct {
		name     string
		last     message.Message
		wantTail []string
	}{
		{name: "continue", last: message.Message{Role: message.RoleUser, Content: "work"}, wantTail: []string{"continue:agent-1/task-1"}},
		{name: "thinking-only continue", last: message.Message{Role: message.RoleAssistant, ThinkingBlocks: []message.ThinkingBlock{{Thinking: "partial"}}}, wantTail: []string{"continue:agent-1/task-1"}},
		{name: "send continue", last: message.Message{Role: message.RoleAssistant, Content: "done", StopReason: "stop"}, wantTail: []string{"send:agent-1/task-1:continue"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := &targetedConversationAgent{
				focused:        "agent-1",
				subAgents:      []agent.SubAgentInfo{{InstanceID: "agent-1", TaskID: "task-1"}},
				messagesByTask: map[string][]message.Message{"task-1": {tt.last}},
			}
			m := NewModelWithSize(backend, 120, 24)
			m.focusedAgentID = "agent-1"
			m.viewport.SetFilter("agent-1")

			var action focusedContinueActionMsg
			for _, msg := range runCmdTree(m.tryContinue()) {
				if candidate, ok := msg.(focusedContinueActionMsg); ok {
					action = candidate
					break
				}
			}
			if action.action == 0 || action.target.AgentID != "agent-1" || action.target.TaskID != "task-1" {
				t.Fatalf("captured action = %#v", action)
			}

			_, cmd := m.Update(action)
			m.focusedAgentID = ""
			backend.focused = ""
			runCmdTree(cmd)

			want := append([]string{"read:agent-1/task-1"}, tt.wantTail...)
			if !slices.Equal(backend.targetedCalls, want) {
				t.Fatalf("targeted calls = %#v, want %#v", backend.targetedCalls, want)
			}
		})
	}
}

func TestFocusedAgentContinueWithEmptyTranscriptStopsPrestartedAnimation(t *testing.T) {
	backend := &sessionControlAgent{focused: "agent-1", messagesByFocus: map[string][]message.Message{"agent-1": nil}}
	m := NewModelWithSize(backend, 120, 24)
	m.focusedAgentID = "agent-1"
	m.activities["agent-1"] = agent.AgentActivityEvent{AgentID: "agent-1", Type: agent.ActivityIdle}

	msgs := runCmdTree(m.tryContinue())
	var action focusedContinueActionMsg
	for _, msg := range msgs {
		if candidate, ok := msg.(focusedContinueActionMsg); ok {
			action = candidate
			break
		}
	}
	model, cmd := m.Update(action)
	updated := model.(*Model)
	if cmd != nil {
		t.Fatal("empty focused transcript should not continue")
	}
	if updated.animRunning {
		t.Fatal("empty focused transcript left animation running")
	}
}
