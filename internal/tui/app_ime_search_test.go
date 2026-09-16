package tui

import (
	"errors"
	"os"
	"os/exec"
	"testing"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/tools"
)

// TestIMESwitchIfTransitionOnlyTriggersForNormalModes verifies that
// runIMESwitchIfTransition returns a Cmd only when transitioning INTO a
// Normal-like mode, and nil for same-mode or Insert transitions.
func TestQueryIMECurrentReadsStdoutWithoutConflict(t *testing.T) {
	helper := os.Getenv("CHORD_IME_QUERY_HELPER")
	if helper == "1" {
		_, _ = os.Stdout.WriteString("com.apple.inputmethod.SCIM.ITABC\n")
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestQueryIMECurrentReadsStdoutWithoutConflict")
	cmd.Env = append(os.Environ(), "CHORD_IME_QUERY_HELPER=1")
	out, err := queryIMECurrent(cmd)
	if err != nil {
		t.Fatalf("queryIMECurrent() error = %v", err)
	}
	if out != "com.apple.inputmethod.SCIM.ITABC" {
		t.Fatalf("queryIMECurrent() = %q, want trimmed output", out)
	}
}

func TestGetIMECurrentCmdUsesQueryOutput(t *testing.T) {
	m := NewModel(nil)
	orig := imeQueryCurrent
	defer func() { imeQueryCurrent = orig }()
	var queried bool
	imeQueryCurrent = func() (string, error) {
		queried = true
		return "zh-orig", nil
	}
	cmd := m.getIMECurrentCmd()
	if cmd == nil {
		t.Fatal("getIMECurrentCmd() returned nil")
	}
	msg := cmd()
	imsg, ok := msg.(imeCurrentMsg)
	if !ok {
		t.Fatalf("cmd() msg = %T, want imeCurrentMsg", msg)
	}
	if !queried {
		t.Fatal("imeQueryCurrent was not called")
	}
	if imsg.current != "zh-orig" {
		t.Fatalf("imeCurrentMsg.current = %q, want zh-orig", imsg.current)
	}
}

func TestGetIMECurrentCmdReturnsEmptyOnQueryError(t *testing.T) {
	m := NewModel(nil)
	orig := imeQueryCurrent
	defer func() { imeQueryCurrent = orig }()
	imeQueryCurrent = func() (string, error) {
		return "", errors.New("boom")
	}
	msg := m.getIMECurrentCmd()()
	imsg, ok := msg.(imeCurrentMsg)
	if !ok {
		t.Fatalf("cmd() msg = %T, want imeCurrentMsg", msg)
	}
	if imsg.current != "" {
		t.Fatalf("imeCurrentMsg.current = %q, want empty", imsg.current)
	}
}

func TestIMESwitchIfTransitionOnlyTriggersForNormalModes(t *testing.T) {
	m := NewModel(nil)
	m.ime.switchTarget = "com.apple.keylayout.ABC"
	m.terminalAppFocused = true

	cases := []struct {
		from, to Mode
		wantCmd  bool
	}{
		{ModeInsert, ModeNormal, true},
		{ModeInsert, ModeDirectory, true},
		{ModeInsert, ModeSessionSelect, true},
		{ModeInsert, ModeConfirm, true},
		{ModeInsert, ModeQuestion, true},
		{ModeInsert, ModeRules, true},
		{ModeNormal, ModeInsert, false}, // entering Insert must NOT trigger switch
		{ModeNormal, ModeNormal, false}, // same mode: no transition
		{ModeInsert, ModeInsert, false}, // same mode: no transition
	}
	for _, tc := range cases {
		m.mode = tc.from
		cmd := m.runIMESwitchIfTransition(tc.from, tc.to)
		got := cmd != nil
		if got != tc.wantCmd {
			t.Errorf("runIMESwitchIfTransition(%v→%v): cmd!=nil = %v, want %v", tc.from, tc.to, got, tc.wantCmd)
		}
	}
}

func TestIMESwitchIfTransitionSkippedWhenBackgrounded(t *testing.T) {
	m := NewModel(nil)
	m.ime.switchTarget = "com.apple.keylayout.ABC"
	m.terminalAppFocused = false

	if cmd := m.runIMESwitchIfTransition(ModeInsert, ModeNormal); cmd != nil {
		t.Fatalf("runIMESwitchIfTransition in background returned cmd %#v, want nil", cmd)
	}
}

// TestIMECurrentMsgIgnoredInInsertMode verifies Bug 1 fix:
// if imeCurrentMsg arrives after the user has already switched back to Insert,
// imeBeforeNormal must NOT be updated and IME must NOT be switched.
func TestIMECurrentMsgIgnoredInInsertMode(t *testing.T) {
	m := NewModel(nil)
	m.ime.switchTarget = "com.apple.keylayout.ABC"
	m.mode = ModeInsert // user already back in Insert before msg arrived
	m.ime.beforeNormal = "zh-orig"

	updated, _ := m.Update(imeCurrentMsg{seq: 0, current: "zh-new"})
	model := updated.(*Model)

	if model.ime.beforeNormal != "zh-orig" {
		t.Fatalf("imeBeforeNormal changed to %q in Insert mode, want zh-orig", model.ime.beforeNormal)
	}
}

// TestIMECurrentMsgSavesAndSwitchesInNormalMode verifies that imeCurrentMsg
// in an English-IME mode saves imeBeforeNormal (the actual switching is a goroutine
// side-effect we don't assert on, but state must be updated).
func TestIMECurrentMsgSavesAndSwitchesInNormalMode(t *testing.T) {
	m := NewModel(nil)
	m.ime.switchTarget = "com.apple.keylayout.ABC"
	m.mode = ModeNormal
	m.ime.beforeNormal = ""

	updated, _ := m.Update(imeCurrentMsg{seq: 0, current: "zh"})
	model := updated.(*Model)

	if model.ime.beforeNormal != "zh" {
		t.Fatalf("imeBeforeNormal = %q, want \"zh\"", model.ime.beforeNormal)
	}
}

func TestSwitchModeWithIMERestoresWhenEnteringInsert(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeNormal
	m.ime.beforeNormal = "zh-orig"
	preventIMEApplyInTests(&m)

	m.switchModeWithIME(ModeInsert)

	if m.ime.beforeNormal != "" {
		t.Fatalf("imeBeforeNormal = %q after switchModeWithIME(Insert), want empty", m.ime.beforeNormal)
	}
	if !m.ime.pending || m.ime.pendingTarget != "zh-orig" {
		t.Fatalf("pending IME restore = (%v, %q), want (true, zh-orig)", m.ime.pending, m.ime.pendingTarget)
	}
}

func TestHandleNormalKeyEnterInsertClearsActiveSearchSession(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeNormal
	m.search.State.Active = true
	m.search.State.Query = "grep"
	m.search.State.Matches = []MatchPosition{{BlockIndex: 0}}
	m.search.State.Current = 0
	m.ime.beforeNormal = "zh-orig"
	preventIMEApplyInTests(&m)

	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "i"}))

	if m.mode != ModeInsert {
		t.Fatalf("mode = %v, want ModeInsert", m.mode)
	}
	if m.search.State.Active || m.search.State.Query != "" {
		t.Fatalf("search state after entering insert = %+v, want cleared", m.search.State)
	}
}

func TestHandleNormalKeyEnterInsertQueuesIMERestore(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeNormal
	m.ime.beforeNormal = "zh-orig"
	preventIMEApplyInTests(&m)

	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "i"}))

	if m.mode != ModeInsert {
		t.Fatalf("mode = %v, want ModeInsert", m.mode)
	}
	if !m.ime.pending || m.ime.pendingTarget != "zh-orig" {
		t.Fatalf("pending IME restore = (%v, %q), want (true, zh-orig)", m.ime.pending, m.ime.pendingTarget)
	}
}

func TestLoadQueuedDraftIntoComposerQueuesIMERestore(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeNormal
	m.ime.beforeNormal = "zh-orig"
	preventIMEApplyInTests(&m)
	draft := queuedDraft{Content: "hello"}

	_ = m.loadQueuedDraftIntoComposer(draft)

	if m.mode != ModeInsert {
		t.Fatalf("mode = %v, want ModeInsert", m.mode)
	}
	if !m.ime.pending || m.ime.pendingTarget != "zh-orig" {
		t.Fatalf("pending IME restore = (%v, %q), want (true, zh-orig)", m.ime.pending, m.ime.pendingTarget)
	}
}

func TestHandleCtrlCCancelSessionSelectRestoresInsertIME(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeSessionSelect
	m.sessionSelect.prevMode = ModeInsert
	m.ime.beforeNormal = "zh-orig"
	preventIMEApplyInTests(&m)

	_ = m.handleCtrlC()
	if m.mode != ModeInsert {
		t.Fatalf("mode = %v, want ModeInsert", m.mode)
	}
	if !m.ime.pending || m.ime.pendingTarget != "zh-orig" {
		t.Fatalf("pending IME restore = (%v, %q), want (true, zh-orig)", m.ime.pending, m.ime.pendingTarget)
	}
}

func TestResolveConfirmRestoresInsertModeWithIMERestore(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeConfirm
	m.confirm = confirmState{
		request:  &ConfirmRequest{ToolName: tools.NameEdit},
		prevMode: ModeInsert,
	}
	m.ime.beforeNormal = "zh-orig"
	preventIMEApplyInTests(&m)

	cmd := m.resolveConfirm(ConfirmResult{Action: ConfirmDeny})
	if cmd == nil {
		t.Fatal("resolveConfirm() returned nil cmd")
	}
	if m.mode != ModeInsert {
		t.Fatalf("mode = %v, want ModeInsert", m.mode)
	}
	if !m.ime.pending || m.ime.pendingTarget != "zh-orig" {
		t.Fatalf("pending IME restore = (%v, %q), want (true, zh-orig)", m.ime.pending, m.ime.pendingTarget)
	}
}

func TestResolveQuestionRestoresInsertModeWithIMERestore(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeQuestion
	m.question = questionState{
		request:  &QuestionRequest{Questions: []tools.QuestionItem{{Header: "name", Question: "who?"}}},
		prevMode: ModeInsert,
	}
	m.ime.beforeNormal = "zh-orig"
	preventIMEApplyInTests(&m)

	cmd := m.resolveQuestion(QuestionResult{Err: errors.New("cancelled")})
	if cmd == nil {
		t.Fatal("resolveQuestion() returned nil cmd")
	}
	if m.mode != ModeInsert {
		t.Fatalf("mode = %v, want ModeInsert", m.mode)
	}
	if !m.ime.pending || m.ime.pendingTarget != "zh-orig" {
		t.Fatalf("pending IME restore = (%v, %q), want (true, zh-orig)", m.ime.pending, m.ime.pendingTarget)
	}
}

func TestHandleNormalKeyNonSearchKeyClearsActiveSearchSession(t *testing.T) {
	m := NewModelWithSize(nil, 120, 24)
	m.mode = ModeNormal
	m.search.State.Active = true
	m.search.State.Query = "grep"
	m.search.State.Matches = []MatchPosition{{BlockIndex: 0}}
	m.search.State.Current = 0
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "hello"})

	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "j", Code: 'j'}))

	if m.search.State.Active || m.search.State.Query != "" {
		t.Fatalf("search state after normal key = %+v, want cleared", m.search.State)
	}
}

func TestHandleNormalKeySearchNavigationPreservesActiveSearchSession(t *testing.T) {
	m := NewModelWithSize(nil, 120, 24)
	m.mode = ModeNormal
	m.search.State.Active = true
	m.search.State.Query = "grep"
	m.search.State.Matches = []MatchPosition{{BlockIndex: 0}, {BlockIndex: 1}}
	m.search.State.Current = 0
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "grep one"})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockAssistant, Content: "grep two"})

	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "n", Code: 'n'}))

	if !m.search.State.Active || m.search.State.Query != "grep" {
		t.Fatalf("search state after n = %+v, want preserved", m.search.State)
	}
	if m.search.State.Current != 1 {
		t.Fatalf("search current after n = %d, want 1", m.search.State.Current)
	}
}

func TestExecuteSearchStartsFromFocusedBlock(t *testing.T) {
	m := NewModelWithSize(nil, 120, 24)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "grep first"})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockAssistant, Content: "grep second"})
	m.viewport.AppendBlock(&Block{ID: 3, Type: BlockAssistant, Content: "grep third"})
	m.focusedBlockID = 2

	m.search = NewSearchModel(ModeNormal)
	m.executeSearchAgainstCurrentTranscript("grep")

	if m.search.State.Current != 1 {
		t.Fatalf("search current = %d, want 1 for focused block anchor", m.search.State.Current)
	}
	match, ok := m.search.State.CurrentMatch()
	if !ok {
		t.Fatal("expected current search match")
	}
	if match.BlockID != 2 {
		t.Fatalf("current match block = %d, want 2", match.BlockID)
	}
}

func TestExecuteSearchStartsFromViewportTopWhenNoFocusedBlock(t *testing.T) {
	m := NewModelWithSize(nil, 120, 3)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "grep first"})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockAssistant, Content: "grep second"})
	m.viewport.AppendBlock(&Block{ID: 3, Type: BlockAssistant, Content: "grep third"})
	if lineOffset, ok := m.viewport.LineOffsetForBlockID(2); ok {
		m.viewport.offset = lineOffset
	}

	m.search = NewSearchModel(ModeNormal)
	m.executeSearchAgainstCurrentTranscript("grep")

	if m.search.State.Current != 1 {
		t.Fatalf("search current = %d, want 1 for viewport-top anchor", m.search.State.Current)
	}
	match, ok := m.search.State.CurrentMatch()
	if !ok {
		t.Fatal("expected current search match")
	}
	if match.BlockID != 2 {
		t.Fatalf("current match block = %d, want 2", match.BlockID)
	}
}

func TestSwitchModeWithIMEEnteringInsertClearsActiveSearchSession(t *testing.T) {
	m := NewModelWithSize(nil, 120, 24)
	m.mode = ModeNormal
	m.search.State.Active = true
	m.search.State.Query = "grep"
	m.search.State.Matches = []MatchPosition{{BlockIndex: 0}}
	m.search.State.Current = 0

	_ = m.switchModeWithIME(ModeInsert)

	if m.mode != ModeInsert {
		t.Fatalf("mode = %v, want ModeInsert", m.mode)
	}
	if m.search.State.Active || m.search.State.Query != "" {
		t.Fatalf("search state after switchModeWithIME(Insert) = %+v, want cleared", m.search.State)
	}
}

func TestLoadQueuedDraftIntoComposerClearsActiveSearchSession(t *testing.T) {
	m := NewModelWithSize(nil, 120, 24)
	m.mode = ModeNormal
	m.search.State.Active = true
	m.search.State.Query = "grep"
	m.search.State.Matches = []MatchPosition{{BlockIndex: 0}}
	m.search.State.Current = 0
	draft := queuedDraft{ID: "draft-1", Content: "hello"}

	_ = m.loadQueuedDraftIntoComposer(draft)

	if m.mode != ModeInsert {
		t.Fatalf("mode = %v, want ModeInsert", m.mode)
	}
	if m.search.State.Active || m.search.State.Query != "" {
		t.Fatalf("search state after loadQueuedDraftIntoComposer = %+v, want cleared", m.search.State)
	}
}

func TestMouseClickInputZoneClearsActiveSearchSession(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	m.mode = ModeNormal
	m.search.State.Active = true
	m.search.State.Query = "grep"
	m.search.State.Matches = []MatchPosition{{BlockIndex: 0}}
	m.search.State.Current = 0

	m.width = 140
	m.height = 24
	m.layout = m.generateLayout(m.width, m.height)
	_ = m.renderStatusBar()
	clickX := m.layout.input.Min.X + inputPromptWidth
	clickY := m.layout.input.Min.Y + 1
	updated, _ := m.Update(tea.MouseClickMsg{X: clickX, Y: clickY, Button: tea.MouseLeft})
	model := updated.(*Model)
	if model.mode != ModeInsert {
		t.Fatalf("mode = %v, want ModeInsert", model.mode)
	}
	if model.search.State.Active || model.search.State.Query != "" {
		t.Fatalf("search state after input-zone click = %+v, want cleared", model.search.State)
	}
}

func TestOpenHandoffSelectClearsActiveSearchSession(t *testing.T) {
	backend := &sessionControlAgent{availableAgents: []string{"builder"}}
	m := NewModelWithSize(backend, 120, 24)
	m.mode = ModeNormal
	m.search.State.Active = true
	m.search.State.Query = "grep"
	m.search.State.Matches = []MatchPosition{{BlockIndex: 0}}
	m.search.State.Current = 0

	m.openHandoffSelect("plan.md", "req-1", identity.MainAgentID, m.mode)

	if m.mode != ModeHandoffSelect {
		t.Fatalf("mode = %v, want ModeHandoffSelect", m.mode)
	}
	if m.search.State.Active || m.search.State.Query != "" {
		t.Fatalf("search state after openHandoffSelect = %+v, want cleared", m.search.State)
	}
}

func TestOpenImageViewerClearsActiveSearchSession(t *testing.T) {
	m := NewModelWithSize(nil, 120, 24)
	m.mode = ModeNormal
	m.imageCaps.Backend = ImageBackendKitty
	m.imageCaps.SupportsFullscreen = true
	m.search.State.Active = true
	m.search.State.Query = "grep"
	m.search.State.Matches = []MatchPosition{{BlockIndex: 0}}
	m.search.State.Current = 0
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, ImageParts: []BlockImagePart{{FileName: "shot.png", ImagePath: "testdata/shot.png"}}})
	m.focusedBlockID = 1

	m.openImageViewer(-1, 0)

	if m.mode != ModeImageViewer {
		t.Fatalf("mode = %v, want ModeImageViewer", m.mode)
	}
	if m.search.State.Active || m.search.State.Query != "" {
		t.Fatalf("search state after openImageViewer = %+v, want cleared", m.search.State)
	}
}

func TestOpenModelSelectClearsActiveSearchSession(t *testing.T) {
	backend := &sessionControlAgent{poolNamesByFocus: map[string][]string{"": {"default"}}}
	m := NewModelWithSize(backend, 120, 24)
	m.mode = ModeNormal
	m.search.State.Active = true
	m.search.State.Query = "grep"
	m.search.State.Matches = []MatchPosition{{BlockIndex: 0}}
	m.search.State.Current = 0

	m.openModelSelect()

	if m.mode != ModeModelSelect {
		t.Fatalf("mode = %v, want ModeModelSelect", m.mode)
	}
	if m.search.State.Active || m.search.State.Query != "" {
		t.Fatalf("search state after openModelSelect = %+v, want cleared", m.search.State)
	}
}

func TestMouseSelectionRangeConvertsInclusiveDragEndpointToExclusive(t *testing.T) {
	m := NewModel(nil)
	m.selStartBlockID = 1
	m.selStartLine = 0
	m.selStartCol = 4
	m.selEndBlockID = 1
	m.selEndLine = 0
	m.selEndCol = 13
	m.selEndInclusiveForCopy = true

	sel := m.mouseSelectionRange()
	if sel.StartCol != 4 || sel.EndCol != 14 {
		t.Fatalf("mouseSelectionRange() = (%d, %d), want (4, 14)", sel.StartCol, sel.EndCol)
	}
}

func TestMouseSelectionRangePreservesExclusiveWordSelection(t *testing.T) {
	m := NewModel(nil)
	m.selStartBlockID = 1
	m.selStartLine = 0
	m.selStartCol = 4
	m.selEndBlockID = 1
	m.selEndLine = 0
	m.selEndCol = 14
	m.selEndInclusiveForCopy = false

	sel := m.mouseSelectionRange()
	if sel.StartCol != 4 || sel.EndCol != 14 {
		t.Fatalf("mouseSelectionRange() = (%d, %d), want unchanged (4, 14)", sel.StartCol, sel.EndCol)
	}
}

func TestOpenUsageStatsClearsActiveSearchSession(t *testing.T) {
	m := NewModelWithSize(nil, 120, 24)
	m.mode = ModeNormal
	m.search.State.Active = true
	m.search.State.Query = "grep"
	m.search.State.Matches = []MatchPosition{{BlockIndex: 0}}
	m.search.State.Current = 0

	m.openUsageStats()

	if m.mode != ModeUsageStats {
		t.Fatalf("mode = %v, want ModeUsageStats", m.mode)
	}
	if m.search.State.Active || m.search.State.Query != "" {
		t.Fatalf("search state after openUsageStats = %+v, want cleared", m.search.State)
	}
}

func TestOpenHelpClearsActiveSearchSession(t *testing.T) {
	m := NewModelWithSize(nil, 120, 24)
	m.mode = ModeNormal
	m.search.State.Active = true
	m.search.State.Query = "grep"
	m.search.State.Matches = []MatchPosition{{BlockIndex: 0}}
	m.search.State.Current = 0

	_ = m.openHelp()

	if m.mode != ModeHelp {
		t.Fatalf("mode = %v, want ModeHelp", m.mode)
	}
	if m.search.State.Active || m.search.State.Query != "" {
		t.Fatalf("search state after openHelp = %+v, want cleared", m.search.State)
	}
}

func TestConfirmRequestMsgSwitchesIMEWhenEnteringConfirm(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeInsert
	m.ime.switchTarget = "com.apple.keylayout.ABC"

	updated, cmd := m.Update(confirmRequestMsg{request: ConfirmRequest{ToolName: tools.NameEdit}})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if model.mode != ModeConfirm {
		t.Fatalf("mode = %v, want ModeConfirm", model.mode)
	}
	if cmd == nil {
		t.Fatal("confirmRequestMsg should trigger IME query command when entering Confirm from Insert")
	}
}

func TestQuestionRequestMsgSwitchesIMEWhenEnteringQuestion(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeInsert
	m.ime.switchTarget = "com.apple.keylayout.ABC"

	updated, cmd := m.Update(questionRequestMsg{request: QuestionRequest{Questions: []tools.QuestionItem{{Header: "name", Question: "who?", Options: []tools.QuestionOption{{Label: "alice"}}}}}})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if model.mode != ModeQuestion {
		t.Fatalf("mode = %v, want ModeQuestion", model.mode)
	}
	if cmd == nil {
		t.Fatal("questionRequestMsg should trigger IME query command when entering Question from Insert")
	}
}

func TestFocusMsgReappliesEnglishIMEForConfirmAndQuestionModes(t *testing.T) {
	cases := []struct {
		name string
		mode Mode
	}{
		{name: "normal", mode: ModeNormal},
		{name: "confirm", mode: ModeConfirm},
		{name: "question", mode: ModeQuestion},
		{name: "rules", mode: ModeRules},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModel(nil)
			m.mode = tc.mode
			m.ime.switchTarget = "com.apple.keylayout.ABC"
			m.terminalAppFocused = false
			preventIMEApplyInTests(&m)

			updated, cmd := m.Update(tea.FocusMsg{})
			model, ok := updated.(*Model)
			if !ok {
				t.Fatalf("Update returned %T, want *Model", updated)
			}
			if !model.terminalAppFocused {
				t.Fatal("FocusMsg should mark terminal app focused")
			}
			if !model.ime.pending || model.ime.pendingTarget != "com.apple.keylayout.ABC" {
				t.Fatalf("pending IME apply = (%v, %q), want (true, com.apple.keylayout.ABC)", model.ime.pending, model.ime.pendingTarget)
			}
			if cmd != nil {
				t.Fatalf("FocusMsg should not schedule command without an IME apply, got %#v", cmd)
			}
		})
	}
}

func TestIMECurrentMsgIgnoredWhenBackgrounded(t *testing.T) {
	m := NewModel(nil)
	m.ime.switchTarget = "com.apple.keylayout.ABC"
	m.mode = ModeNormal
	m.terminalAppFocused = false
	m.ime.beforeNormal = "zh-orig"

	updated, _ := m.Update(imeCurrentMsg{seq: 0, current: "zh-new"})
	model := updated.(*Model)

	if model.ime.beforeNormal != "zh-orig" {
		t.Fatalf("imeBeforeNormal changed to %q while backgrounded, want zh-orig", model.ime.beforeNormal)
	}
	if model.ime.pending {
		t.Fatalf("background imeCurrentMsg should not queue apply, got pending target %q", model.ime.pendingTarget)
	}
}

// TestIMERestoreClearsBeforeNormal verifies Bug 2 fix:
// runIMERestoreIfNeeded must clear imeBeforeNormal after scheduling restore,
// so a stale imeCurrentMsg that arrives later cannot re-clobber Insert-mode IME.
func TestIMERestoreClearsBeforeNormal(t *testing.T) {
	m := NewModel(nil)
	m.ime.switchTarget = "com.apple.keylayout.ABC"
	m.ime.beforeNormal = "zh"

	m.runIMERestoreIfNeeded()

	if m.ime.beforeNormal != "" {
		t.Fatalf("imeBeforeNormal = %q after restore, want empty", m.ime.beforeNormal)
	}
}

// TestIMERestoreNoopWhenEmpty verifies that runIMERestoreIfNeeded is a no-op
// when imeBeforeNormal is empty (first Insert entry, nothing to restore).
func TestIMERestoreNoopWhenEmpty(t *testing.T) {
	m := NewModel(nil)
	m.ime.beforeNormal = ""

	// Should not panic and imeBeforeNormal stays empty.
	m.runIMERestoreIfNeeded()

	if m.ime.beforeNormal != "" {
		t.Fatalf("imeBeforeNormal = %q, want empty", m.ime.beforeNormal)
	}
}
