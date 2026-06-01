package tui

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func TestWrapConfirmLiteralTextPrefersTokenAndPathBoundaries(t *testing.T) {
	text := "mv docs/plans/example-plan.md docs/plans/archive/example-plan.md"
	got := wrapConfirmLiteralText(text, 44)
	if len(got) < 2 {
		t.Fatalf("expected wrapped output, got %v", got)
	}
	for _, line := range got {
		if ansi.StringWidth(line) > 44 {
			t.Fatalf("line %q width = %d, want <= 44", line, ansi.StringWidth(line))
		}
	}
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "docs/plans/archive/") {
		t.Fatalf("expected wrap to keep path boundary visible, got %v", got)
	}
	if strings.Contains(joined, "DELETE_MULTI_FILE_TOO\nL_PLAN.md") {
		t.Fatalf("unexpected mid-token break in wrapped output: %v", got)
	}
}

func TestRenderConfirmSummarySoftWrapsLongShellCommandWithoutBreakingPathToken(t *testing.T) {
	m := NewModel(nil)
	m.width = 100
	m.confirm.request = &ConfirmRequest{ToolName: "shell", ArgsJSON: `{"command":"mv docs/plans/example-plan.md docs/plans/archive/example-plan.md","workdir":"/tmp/project","timeout":30}`}

	plain := stripANSI(m.renderConfirmDialog())
	if !strings.Contains(plain, "Command:") {
		t.Fatalf("expected command label in confirm dialog, got:\n%s", plain)
	}
	if !strings.Contains(plain, "docs/plans/archive/") {
		t.Fatalf("expected wrapped command to keep path boundary visible, got:\n%s", plain)
	}
	if strings.Contains(plain, "DELETE_MULTI_FILE_TOO\n  L_PLAN.md") {
		t.Fatalf("unexpected mid-token line break in confirm dialog:\n%s", plain)
	}
}

func TestRenderConfirmSummaryShowsStructuredShellFields(t *testing.T) {
	m := NewModel(nil)
	m.width = 100
	m.confirm.request = &ConfirmRequest{ToolName: "shell", ArgsJSON: `{"command":"rm internal/tui/example_obsolete.go","description":"Remove obsolete file","workdir":"/tmp/project","timeout":45}`}

	plain := stripANSI(m.renderConfirmDialog())
	if !strings.Contains(plain, "Tool: shell") {
		t.Fatalf("expected tool line in confirm dialog, got:\n%s", plain)
	}
	if !strings.Contains(plain, "Action: Execute shell command") {
		t.Fatalf("expected action line in confirm dialog, got:\n%s", plain)
	}
	if !strings.Contains(plain, "Command:") || !strings.Contains(plain, "rm internal/tui/example_obsolete.go") {
		t.Fatalf("expected command to be visible in summary view, got:\n%s", plain)
	}
	if strings.Contains(plain, "Tool: shell({") {
		t.Fatalf("summary view should not fall back to truncated JSON, got:\n%s", plain)
	}
	if !strings.Contains(plain, "Workdir: /tmp/project") {
		t.Fatalf("expected workdir in summary view, got:\n%s", plain)
	}
	if !strings.Contains(plain, "Timeout: 45s") {
		t.Fatalf("expected timeout in summary view, got:\n%s", plain)
	}
	if !strings.Contains(plain, "Description: Remove obsolete file") {
		t.Fatalf("expected description in summary view, got:\n%s", plain)
	}
	if strings.Index(plain, "Description: Remove obsolete file") > strings.Index(plain, "Command:") {
		t.Fatalf("expected description to appear before command under unified model, got:\n%s", plain)
	}
}

func TestRenderConfirmSummaryShowsEffectiveForegroundTimeoutWhenCapped(t *testing.T) {
	m := NewModel(nil)
	m.width = 100
	m.confirm.request = &ConfirmRequest{ToolName: "shell", ArgsJSON: `{"command":"sleep 1","timeout":2400}`}

	plain := stripANSI(m.renderConfirmDialog())
	if !strings.Contains(plain, "Timeout: 600s") {
		t.Fatalf("expected confirm summary to show effective capped foreground timeout, got:\n%s", plain)
	}
	if !strings.Contains(plain, "Requested timeout 2400s capped to 600s") {
		t.Fatalf("expected confirm warning about capped timeout, got:\n%s", plain)
	}
}

func TestRenderConfirmSummaryDoesNotTreatShellAsBackground(t *testing.T) {
	m := NewModel(nil)
	m.width = 100
	m.confirm.request = &ConfirmRequest{ToolName: "shell", ArgsJSON: `{"command":"npm run dev","description":"Frontend dev server","timeout":45}`}

	plain := stripANSI(m.renderConfirmDialog())
	if strings.Contains(plain, "Background:") || strings.Contains(plain, "Max runtime:") || strings.Contains(plain, "Mode:") {
		t.Fatalf("shell confirm summary should not include background fields, got:\n%s", plain)
	}
}

func TestRenderConfirmSummaryShowsWriteFilePathAndPreview(t *testing.T) {
	m := NewModel(nil)
	m.width = 100
	m.confirm.request = &ConfirmRequest{ToolName: "write", ArgsJSON: `{"path":"internal/tui/confirm_render.go","content":"line 1\nline 2\nline 3\nline 4"}`}

	plain := stripANSI(m.renderConfirmDialog())
	if !strings.Contains(plain, "File: internal/tui/confirm_render.go") {
		t.Fatalf("expected file path in summary view, got:\n%s", plain)
	}
	if !strings.Contains(plain, "Content preview:") {
		t.Fatalf("expected content preview label, got:\n%s", plain)
	}
	if !strings.Contains(plain, "line 1") || !strings.Contains(plain, "line 2") {
		t.Fatalf("expected preview content in summary view, got:\n%s", plain)
	}
}

func TestBuildConfirmSummaryShowsStructuredEditFields(t *testing.T) {
	summary := buildConfirmSummary(tools.NameEdit, `{"path":"docs/README.md","patch":"@@\n-old\n+new\n"}`, nil, nil)

	if summary.Action != "Replace text in file" {
		t.Fatalf("edit action = %q, want structured Edit action", summary.Action)
	}
	if summary.Risk != confirmRiskMedium {
		t.Fatalf("edit risk = %v, want %v", summary.Risk, confirmRiskMedium)
	}
	if !slices.Contains(summary.Warnings, "Patches existing file content") {
		t.Fatalf("edit warnings = %v, want patch warning", summary.Warnings)
	}
	if !confirmSummaryHasField(summary, "File") {
		t.Fatalf("edit summary fields = %+v, want File field", summary.Fields)
	}
	if !confirmSummaryHasField(summary, "Patch preview") {
		t.Fatalf("edit summary fields = %+v, want Patch preview field", summary.Fields)
	}
}

func confirmSummaryHasField(summary confirmSummary, label string) bool {
	for _, field := range summary.Fields {
		if field.Label == label {
			return true
		}
	}
	return false
}

func TestRenderConfirmSummaryShowsDeleteFilePathAndReason(t *testing.T) {
	m := NewModel(nil)
	m.width = 100
	m.confirm.request = &ConfirmRequest{ToolName: "delete", ArgsJSON: `{"paths":["internal/tui/obsolete.go"],"reason":"remove obsolete file"}`}

	plain := stripANSI(m.renderConfirmDialog())
	if !strings.Contains(plain, "Action: Delete file") {
		t.Fatalf("expected delete action line, got:\n%s", plain)
	}
	if !strings.Contains(plain, "File: internal/tui/obsolete.go") {
		t.Fatalf("expected file path in summary view, got:\n%s", plain)
	}
	if !strings.Contains(plain, "Reason:") || !strings.Contains(plain, "remove obsolete file") {
		t.Fatalf("expected reason field in summary view, got:\n%s", plain)
	}
}

func TestRenderConfirmSummaryFallsBackToRawPayloadOnMalformedArgs(t *testing.T) {
	m := NewModel(nil)
	m.width = 100
	m.confirm.request = &ConfirmRequest{ToolName: "shell", ArgsJSON: `{"command":"rm"`}

	plain := stripANSI(m.renderConfirmDialog())
	if !strings.Contains(plain, "Unable to parse arguments") {
		t.Fatalf("expected malformed-args warning, got:\n%s", plain)
	}
	if !strings.Contains(plain, `Arguments (raw):`) {
		t.Fatalf("expected raw payload label, got:\n%s", plain)
	}
	if !strings.Contains(plain, `{"command":"rm"`) {
		t.Fatalf("expected raw payload in confirm dialog, got:\n%s", plain)
	}
}

func TestRenderDoneConfirmDialogShowsOnlyReportBody(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	m.confirm.request = &ConfirmRequest{
		ToolName:   "done",
		ArgsJSON:   `{"report":"## Completion status\nAll requested work is finished\n\n**Verification**: passed"}`,
		DoneReport: "## Completion status\nAll requested work is finished\n\n**Verification**: passed",
	}

	plain := stripANSI(m.renderConfirmDialog())
	if !strings.Contains(plain, "Confirmation Required") {
		t.Fatalf("expected confirmation badge in Done dialog, got:\n%s", plain)
	}
	if !strings.Contains(plain, "All requested work is finished") || !strings.Contains(plain, "Verification") {
		t.Fatalf("expected Done report body in confirm dialog, got:\n%s", plain)
	}
	if strings.Contains(plain, "Completion Report:") || strings.Contains(plain, "Reason:") || strings.Contains(plain, "Report:") || strings.Contains(plain, "Tool: Done") || strings.Contains(plain, "Action: Execute Done") {
		t.Fatalf("unexpected duplicate or hard-coded Done chrome in confirm dialog:\n%s", plain)
	}
}

func TestRenderDoneConfirmDialogLimitsHeightAndPreservesActions(t *testing.T) {
	m := NewModelWithSize(nil, 100, 18)
	reportLines := make([]string, 0, 40)
	for i := 1; i <= 40; i++ {
		reportLines = append(reportLines, fmt.Sprintf("- line %d", i))
	}
	report := strings.Join(reportLines, "\n")
	m.confirm.request = &ConfirmRequest{
		ToolName:   "done",
		ArgsJSON:   fmt.Sprintf(`{"report":%q}`, report),
		DoneReport: report,
	}

	rendered := m.renderConfirmDialog()
	plain := stripANSI(rendered)
	if got, limit := lipgloss.Height(rendered), confirmDialogMaxHeight(m.height); got > limit {
		t.Fatalf("Done confirm dialog height = %d, want <= %d\n%s", got, limit, plain)
	}
	if !strings.Contains(plain, "[Enter/A] Allow") || !strings.Contains(plain, "[Esc/R] Deny+Reason") {
		t.Fatalf("expected Done confirm actions to remain visible, got:\n%s", plain)
	}
	if !strings.Contains(plain, "more lines hidden") {
		t.Fatalf("expected truncation marker in constrained Done confirm dialog, got:\n%s", plain)
	}
}

func TestRenderConfirmDialogEditModeShowsMultilineTextareaAndHint(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	m.confirm.request = &ConfirmRequest{ToolName: "delete", ArgsJSON: "{\n  \"a\": 1,\n  \"b\": 2\n}"}
	m.confirm.editing = true
	m.confirm.editInput = newConfirmTextarea(m.width, m.height, m.confirm.request.ArgsJSON)

	plain := stripANSI(m.renderConfirmDialog())
	if !strings.Contains(plain, "\"a\": 1") || !strings.Contains(plain, "\"b\": 2") {
		t.Fatalf("expected multiline args to be visible in edit dialog, got:\n%s", plain)
	}
	if !strings.Contains(plain, "[Shift+Enter/Ctrl+J] New line") {
		t.Fatalf("expected multiline edit hint, got:\n%s", plain)
	}
}

func TestHandleConfirmEditKeyUpMovesWithinTextarea(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	m.confirm.request = &ConfirmRequest{ToolName: "delete", ArgsJSON: "{\n  \"a\": 1,\n  \"b\": 2\n}"}
	m.confirm.editing = true
	m.confirm.editInput = newConfirmTextarea(m.width, m.height, m.confirm.request.ArgsJSON)
	if got := m.confirm.editInput.Line(); got != 3 {
		t.Fatalf("initial cursor line = %d, want 3", got)
	}

	_ = m.handleConfirmEditKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))

	if got := m.confirm.editInput.Line(); got != 2 {
		t.Fatalf("cursor line after Up = %d, want 2", got)
	}
}

func TestHandleConfirmEditKeyRejectsInvalidJSONAndStaysEditing(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	m.confirm.request = &ConfirmRequest{ToolName: "delete", ArgsJSON: `{"paths":["a"],"reason":"cleanup"}`}
	m.confirm.editing = true
	m.confirm.editInput = newConfirmTextarea(m.width, m.height, `{"paths":[}`)

	cmd := m.handleConfirmEditKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if cmd != nil {
		t.Fatalf("cmd = %v, want nil on invalid JSON submit", cmd)
	}
	if !m.confirm.editing {
		t.Fatal("confirm edit mode should remain active after invalid JSON submit")
	}
	if !strings.Contains(m.confirm.editError, "valid JSON") {
		t.Fatalf("editError = %q, want valid JSON guidance", m.confirm.editError)
	}

	plain := stripANSI(m.renderConfirmDialog())
	if !strings.Contains(plain, "Arguments must be valid JSON before submission.") {
		t.Fatalf("expected inline validation error in confirm dialog, got:\n%s", plain)
	}
}

func TestRenderConfirmDialogLimitsHeightAndPreservesActions(t *testing.T) {
	m := NewModelWithSize(nil, 100, 12)
	m.confirm.request = &ConfirmRequest{
		ToolName:      "delete",
		ArgsJSON:      `{"paths":["a","b","c","d","e","f","g","h","i","j","k","l"],"reason":"cleanup temporary review files"}`,
		NeedsApproval: []string{"/tmp/a", "/tmp/b", "/tmp/c", "/tmp/d", "/tmp/e", "/tmp/f", "/tmp/g", "/tmp/h", "/tmp/i"},
	}

	rendered := m.renderConfirmDialog()
	plain := stripANSI(rendered)

	if got, limit := lipgloss.Height(rendered), confirmDialogMaxHeight(m.height); got > limit {
		t.Fatalf("confirm dialog height = %d, want <= %d\n%s", got, limit, plain)
	}
	if !strings.Contains(plain, "[Enter/A] Allow") || !strings.Contains(plain, "[Esc/D] Deny") || !strings.Contains(plain, "[E] Modify args") {
		t.Fatalf("expected confirm actions to remain visible, got:\n%s", plain)
	}
	if !strings.Contains(plain, "more lines hidden") {
		t.Fatalf("expected truncation marker in constrained confirm dialog, got:\n%s", plain)
	}
}

func TestRenderConfirmOptionsIncludesDenyReason(t *testing.T) {
	m := NewModel(nil)
	m.width = 100
	m.confirm.request = &ConfirmRequest{ToolName: "shell", ArgsJSON: `{"command":"echo hi"}`}

	plain := stripANSI(m.renderConfirmDialog())
	if !strings.Contains(plain, "[R] Deny+Reason") {
		t.Fatalf("expected [R] Deny+Reason option in confirm dialog, got:\n%s", plain)
	}
}

func TestRenderConfirmOptionsIncludesAddRuleForDelete(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	m.confirm.request = &ConfirmRequest{ToolName: "delete", ArgsJSON: `{"path":"old.txt"}`}

	plain := stripANSI(m.renderConfirmDialog())
	if !strings.Contains(plain, "[M] Add rule…") {
		t.Fatalf("expected [M] Add rule option for Delete confirmation, got:\n%s", plain)
	}
}

func TestRenderConfirmDialogAddRuleKeyShowsRulePickerAfterCachedSummary(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	m.workingDir = "/tmp/project"
	m.confirm.request = &ConfirmRequest{ToolName: tools.NameEdit, ArgsJSON: `{"patch":"*** Begin Patch\n*** Update File: internal/tui/confirm_render.go\n@@\n-old\n+new\n*** End Patch\n"}`}

	summary := stripANSI(m.renderConfirmDialog())
	if !strings.Contains(summary, "[M] Add rule…") {
		t.Fatalf("expected add-rule option in summary dialog, got:\n%s", summary)
	}
	if !strings.Contains(summary, "⚠ Confirmation Required") {
		t.Fatalf("expected summary confirmation title before entering picker, got:\n%s", summary)
	}

	_ = m.handleConfirmKey(tea.KeyPressMsg(tea.Key{Text: "m", Code: 'm'}))

	if !m.confirm.pickingRule {
		t.Fatal("expected add-rule key to enter rule picker mode")
	}

	picker := stripANSI(m.renderConfirmDialog())
	if !strings.Contains(picker, "⚠ Add rule — edit") {
		t.Fatalf("expected rule picker title after pressing A, got:\n%s", picker)
	}
	if !strings.Contains(picker, "Pattern:") {
		t.Fatalf("expected rule picker pattern section, got:\n%s", picker)
	}
	if !strings.Contains(picker, "[Enter] add rule + allow") {
		t.Fatalf("expected rule picker enter hint, got:\n%s", picker)
	}
}

func TestConfirmEditAcceptsClipboardTextMsg(t *testing.T) {
	m := NewModelWithSize(nil, 100, 40)
	m.mode = ModeConfirm
	m.confirm.request = &ConfirmRequest{ToolName: "shell", ArgsJSON: `{"command":"echo old"}`}
	m.confirm.editing = true
	m.confirm.editInput = newConfirmTextarea(m.width, m.height, m.confirm.request.ArgsJSON)
	m.confirm.editInput.SetValue("")

	updated, _ := m.Update(clipboardTextMsg(`{"command":"echo pasted"}`))
	model := updated.(*Model)

	if got := model.confirm.editInput.Value(); got != `{"command":"echo pasted"}` {
		t.Fatalf("confirm edit input = %q", got)
	}
	if got := model.input.Value(); got != "" {
		t.Fatalf("main input should not receive confirm paste, got %q", got)
	}
}

func TestConfirmEditAcceptsPasteMsg(t *testing.T) {
	m := NewModelWithSize(nil, 100, 40)
	m.mode = ModeConfirm
	m.confirm.request = &ConfirmRequest{ToolName: "shell", ArgsJSON: `{"command":"echo old"}`}
	m.confirm.editing = true
	m.confirm.editInput = newConfirmTextarea(m.width, m.height, m.confirm.request.ArgsJSON)
	m.confirm.editInput.SetValue("")

	updated, _ := m.Update(tea.PasteMsg{Content: `{"command":"echo pasted"}`})
	model := updated.(*Model)

	if got := model.confirm.editInput.Value(); got != `{"command":"echo pasted"}` {
		t.Fatalf("confirm edit input = %q", got)
	}
}

func TestConfirmDenyReasonAcceptsClipboardTextMsg(t *testing.T) {
	m := NewModelWithSize(nil, 100, 40)
	m.mode = ModeConfirm
	m.confirm.request = &ConfirmRequest{ToolName: "shell", ArgsJSON: `{"command":"rm"}`}
	m.confirm.denyingWithReason = true
	m.confirm.denyReasonInput = newConfirmTextarea(m.width, m.height, "")
	m.confirm.denyReasonInput.SetValue("")

	updated, _ := m.Update(clipboardTextMsg("because pasted"))
	model := updated.(*Model)

	if got := model.confirm.denyReasonInput.Value(); got != "because pasted" {
		t.Fatalf("confirm deny reason input = %q", got)
	}
	if got := model.input.Value(); got != "" {
		t.Fatalf("main input should not receive confirm paste, got %q", got)
	}
}

func TestConfirmDenyReasonAcceptsPasteMsg(t *testing.T) {
	m := NewModelWithSize(nil, 100, 40)
	m.mode = ModeConfirm
	m.confirm.request = &ConfirmRequest{ToolName: "shell", ArgsJSON: `{"command":"rm"}`}
	m.confirm.denyingWithReason = true
	m.confirm.denyReasonInput = newConfirmTextarea(m.width, m.height, "")
	m.confirm.denyReasonInput.SetValue("")

	updated, _ := m.Update(tea.PasteMsg{Content: "because pasted"})
	model := updated.(*Model)

	if got := model.confirm.denyReasonInput.Value(); got != "because pasted" {
		t.Fatalf("confirm deny reason input = %q", got)
	}
}

func TestConfirmCmdVPastesWhenEditing(t *testing.T) {
	m := NewModelWithSize(nil, 100, 40)
	m.mode = ModeConfirm
	m.confirm.request = &ConfirmRequest{ToolName: "shell", ArgsJSON: `{"command":"echo old"}`}
	m.confirm.editing = true
	m.confirm.editInput = newConfirmTextarea(m.width, m.height, m.confirm.request.ArgsJSON)

	cmd := m.handleKeyMsg(tea.KeyPressMsg(tea.Key{Code: 'v', Mod: tea.ModSuper}))
	if cmd == nil {
		t.Fatal("expected cmd+v in confirm edit mode to paste from clipboard")
	}
}

func TestHandleConfirmKeyREntersDenyReasonMode(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	m.confirm.request = &ConfirmRequest{ToolName: "shell", ArgsJSON: `{"command":"echo hi"}`}

	_ = m.handleConfirmKey(tea.KeyPressMsg(tea.Key{Text: "r", Code: 'r'}))
	if !m.confirm.denyingWithReason {
		t.Fatal("expected denyingWithReason=true after pressing R")
	}
}

func TestRenderConfirmDenyReasonModeShowsHint(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	m.confirm.request = &ConfirmRequest{ToolName: "shell", ArgsJSON: `{"command":"echo hi"}`}
	m.confirm.denyingWithReason = true
	m.confirm.denyReasonInput = newConfirmTextarea(m.width, m.height, "")

	plain := stripANSI(m.renderConfirmDialog())
	if !strings.Contains(plain, "deny with reason") {
		t.Fatalf("expected 'deny with reason' header in deny-reason dialog, got:\n%s", plain)
	}
	if !strings.Contains(plain, "[Enter] Deny") {
		t.Fatalf("expected '[Enter] Deny' hint in deny-reason dialog, got:\n%s", plain)
	}
	if !strings.Contains(plain, "[Esc] Back") {
		t.Fatalf("expected '[Esc] Back' hint in deny-reason dialog, got:\n%s", plain)
	}
}

func TestRenderConfirmDenyReasonInputHasNoPrompt(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	m.confirm.request = &ConfirmRequest{ToolName: "done", ArgsJSON: `{}`}
	m.confirm.denyingWithReason = true
	m.confirm.denyReasonInput = newConfirmTextarea(m.width, m.height, "")
	m.confirm.denyReasonInput.SetValue("first line\nsecond line")

	plain := stripANSI(m.renderConfirmDialog())
	if strings.Contains(plain, "> first line") {
		t.Fatalf("deny-reason input should not render a primary prompt, got:\n%s", plain)
	}
	var firstCol, secondCol = -1, -1
	for line := range strings.SplitSeq(plain, "\n") {
		if col := strings.Index(line, "first line"); col >= 0 {
			firstCol = col
		}
		if col := strings.Index(line, "second line"); col >= 0 {
			secondCol = col
		}
	}
	if firstCol < 0 || secondCol < 0 {
		t.Fatalf("expected deny-reason lines in dialog, got:\n%s", plain)
	}
	if firstCol != secondCol {
		t.Fatalf("deny-reason continuation line column = %d, want %d; dialog:\n%s", secondCol, firstCol, plain)
	}
}

func TestRenderConfirmDenyReasonInputAfterResizeUsesDialogContentWidth(t *testing.T) {
	m := NewModelWithSize(nil, 120, 30)
	m.confirm.request = &ConfirmRequest{ToolName: "done", ArgsJSON: `{}`}
	m.confirm.denyingWithReason = true
	m.confirm.denyReasonInput = newConfirmTextarea(m.width, m.height, "")
	m.confirm.denyReasonInput.SetValue("Please wait until the deployment notes mention the rollback plan and the dashboard checks that should be reviewed after release.")

	m.applyTerminalSize(80, 30, false)

	want := confirmDialogWidth(m.width) - DirectoryBorderStyle.GetHorizontalPadding() - DirectoryBorderStyle.GetHorizontalBorderSize()
	if got := m.confirm.denyReasonInput.Width(); got != want {
		t.Fatalf("deny-reason input width = %d, want dialog content width %d", got, want)
	}
}

func TestNewConfirmTextareaUsesDialogContentWidth(t *testing.T) {
	m := NewModelWithSize(nil, 180, 30)
	m.confirm.request = &ConfirmRequest{ToolName: "done", ArgsJSON: `{}`}
	m.confirm.denyingWithReason = true
	m.confirm.denyReasonInput = newConfirmTextarea(m.width, m.height, "")
	m.confirm.denyReasonInput.SetValue("The summary is missing the migration risk and the follow-up owner, so this should be revised before it is sent.")

	want := confirmDialogWidth(m.width) - DirectoryBorderStyle.GetHorizontalPadding() - DirectoryBorderStyle.GetHorizontalBorderSize()
	if got := m.confirm.denyReasonInput.Width(); got != want {
		t.Fatalf("deny-reason input width = %d, want dialog content width %d", got, want)
	}
}

func TestHandleConfirmDenyReasonKeyEnterSubmitsReason(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	m.confirmResultCh = make(chan ConfirmResult, 1)
	m.confirmCh = nil
	m.confirm.request = &ConfirmRequest{ToolName: "shell", ArgsJSON: `{"command":"echo hi"}`}
	m.confirm.denyingWithReason = true
	m.confirm.denyReasonInput = newConfirmTextarea(m.width, m.height, "")
	longTail := strings.Repeat("long", 250) + " tail"
	m.confirm.denyReasonInput.SetValue("  not safe\nfor production\n" + longTail + "  ")

	_ = m.handleConfirmDenyReasonKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	select {
	case result := <-m.confirmResultCh:
		if result.Action != ConfirmDeny {
			t.Fatalf("action = %v, want %v", result.Action, ConfirmDeny)
		}
		wantReason := "not safe\nfor production\n" + longTail
		if result.DenyReason != wantReason {
			t.Fatalf("deny reason = %q, want %q", result.DenyReason, wantReason)
		}
	default:
		t.Fatal("expected confirm result after pressing Enter")
	}

	if m.confirm.request != nil {
		t.Fatal("expected confirm state to reset after submit")
	}
}

func TestHandleConfirmDenyReasonKeyEscGoesBack(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	m.confirmCh = nil
	m.confirm.request = &ConfirmRequest{ToolName: "shell", ArgsJSON: `{"command":"echo hi"}`}
	m.confirm.denyingWithReason = true
	m.confirm.denyReasonInput = newConfirmTextarea(m.width, m.height, "")
	m.confirm.denyReasonInput.SetValue("some reason")

	cmd := m.handleConfirmDenyReasonKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc}))
	if cmd != nil {
		t.Fatal("expected nil cmd on Esc from deny-reason mode")
	}
	if m.confirm.denyingWithReason {
		t.Fatal("expected denyingWithReason=false after pressing Esc")
	}
}

func TestRenderConfirmDialogForDoneOnlyShowsAllowAndDenyReason(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	m.confirm.request = &ConfirmRequest{ToolName: "done", ArgsJSON: `{}`}

	plain := stripANSI(m.renderConfirmDialog())
	if !strings.Contains(plain, "[Enter/A] Allow") || !strings.Contains(plain, "[Esc/R] Deny+Reason") {
		t.Fatalf("Done confirm options missing expected actions:\n%s", plain)
	}
	if strings.Contains(plain, "[E] Modify args") || strings.Contains(plain, "[M] Add rule") || strings.Contains(plain, "Press E") {
		t.Fatalf("Done confirm should not show generic actions or edit hints:\n%s", plain)
	}
}

func TestRenderConfirmDialogForceDenyOnlyShowsDenyReason(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	m.confirm.request = &ConfirmRequest{ToolName: "done", ArgsJSON: `{}`, ForceDenyReason: true}

	plain := stripANSI(m.renderConfirmDialog())
	if !strings.Contains(plain, "[Esc/R] Deny+Reason required") {
		t.Fatalf("forced deny confirm options missing required deny action:\n%s", plain)
	}
	if strings.Contains(plain, "[Enter/A] Allow") || strings.Contains(plain, "[E] Modify args") || strings.Contains(plain, "[M] Add rule") {
		t.Fatalf("forced deny confirm should not show allow or generic actions:\n%s", plain)
	}
}

func TestHandleConfirmDImmediatelyDeniesGenericConfirm(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	m.confirmResultCh = make(chan ConfirmResult, 1)
	m.mode = ModeConfirm
	m.confirm.request = &ConfirmRequest{ToolName: "shell", ArgsJSON: `{"command":"echo hi"}`}

	cmd := m.handleConfirmKey(tea.KeyPressMsg(tea.Key{Text: "d", Code: 'd'}))
	if cmd == nil {
		t.Fatal("expected immediate deny command from D")
	}
	select {
	case result := <-m.confirmResultCh:
		if result.Action != ConfirmDeny {
			t.Fatalf("action = %v, want %v", result.Action, ConfirmDeny)
		}
	default:
		t.Fatal("expected confirm result after pressing D")
	}
	if m.confirm.request != nil {
		t.Fatal("expected confirm state to reset after immediate deny")
	}
}

func TestHandleConfirmDoneIgnoresDenyShortcut(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	m.confirmResultCh = make(chan ConfirmResult, 1)
	m.mode = ModeConfirm
	m.confirm.request = &ConfirmRequest{ToolName: "done", ArgsJSON: `{}`}

	cmd := m.handleConfirmKey(tea.KeyPressMsg(tea.Key{Text: "d", Code: 'd'}))
	if cmd != nil {
		t.Fatalf("Done dialog should not expose hidden D deny shortcut, got %T", cmd)
	}
	select {
	case result := <-m.confirmResultCh:
		t.Fatalf("unexpected confirm result from hidden D shortcut: %#v", result)
	default:
	}
	if m.confirm.denyingWithReason {
		t.Fatal("D should not enter deny-with-reason mode for Done dialog")
	}
}

func TestHandleConfirmDoneForceDenyIgnoresDenyShortcut(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	m.confirmResultCh = make(chan ConfirmResult, 1)
	m.mode = ModeConfirm
	m.confirm.request = &ConfirmRequest{ToolName: "done", ArgsJSON: `{}`, ForceDenyReason: true}

	cmd := m.handleConfirmKey(tea.KeyPressMsg(tea.Key{Text: "d", Code: 'd'}))
	if cmd != nil {
		t.Fatalf("force-deny Done dialog should not expose hidden D shortcut, got %T", cmd)
	}
	if m.confirm.denyingWithReason {
		t.Fatal("D should not enter deny-with-reason mode for force-deny Done dialog")
	}
}

func TestHandleConfirmForceDenyIgnoresAllowShortcut(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	m.confirmResultCh = make(chan ConfirmResult, 1)
	m.mode = ModeConfirm
	m.confirm.request = &ConfirmRequest{ToolName: "done", ArgsJSON: `{}`, ForceDenyReason: true}

	cmd := m.handleConfirmKey(tea.KeyPressMsg(tea.Key{Text: "a", Code: 'a'}))
	if cmd != nil {
		t.Fatalf("forced deny allow shortcut should be ignored, got %T", cmd)
	}
	select {
	case result := <-m.confirmResultCh:
		t.Fatalf("unexpected confirm result from forced deny allow shortcut: %#v", result)
	default:
	}
	if m.confirm.editError != "" {
		t.Fatalf("editError = %q, want no prompt for ignored allow shortcut", m.confirm.editError)
	}
}

func TestHandleConfirmDoneViewCopiesReportParsedFromArgs(t *testing.T) {
	origWrite := clipboardWriteAll
	var copied string
	clipboardWriteAll = func(text string) error {
		copied = text
		return nil
	}
	defer func() { clipboardWriteAll = origWrite }()

	report := "# Finished\n\nAll done.\n\n- verified"
	m := NewModelWithSize(nil, 100, 30)
	m.mode = ModeConfirm
	m.confirm.request = &ConfirmRequest{ToolName: "done", ArgsJSON: `{"report":"# Finished\n\nAll done.\n\n- verified"}`}

	cmd := m.handleConfirmKey(tea.KeyPressMsg(tea.Key{Text: "v", Code: 'v'}))
	if cmd != nil {
		_ = cmd()
	}
	if m.mode != ModeContentViewer {
		t.Fatalf("mode after Done view = %v, want ModeContentViewer", m.mode)
	}
	if m.contentViewer.content != report {
		t.Fatalf("viewer content = %q, want %q", m.contentViewer.content, report)
	}

	_ = m.handleContentViewerKey(tea.KeyPressMsg(tea.Key{Text: "y", Code: 'y'}))
	cmd = m.handleContentViewerKey(tea.KeyPressMsg(tea.Key{Text: "y", Code: 'y'}))
	if cmd == nil {
		t.Fatal("Done view yy should return clipboard command")
	}
	msg := cmd()
	v := reflect.ValueOf(msg)
	if v.Kind() != reflect.Slice || v.Len() != 2 {
		t.Fatalf("clipboard command msg = %T, want 2-command sequence", msg)
	}
	second := v.Index(1).Call(nil)[0].Interface().(clipboardWriteResultMsg)
	if second.success != "View content copied to clipboard" {
		t.Fatalf("clipboard success = %q", second.success)
	}
	if copied != report {
		t.Fatalf("copied content = %q, want %q", copied, report)
	}
}

func TestHandleConfirmDoneViewOpensContentViewer(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	m.mode = ModeConfirm
	m.confirm.request = &ConfirmRequest{ToolName: "done", DoneReport: "# Finished\n\nAll done."}

	cmd := m.handleConfirmKey(tea.KeyPressMsg(tea.Key{Text: "v", Code: 'v'}))
	if cmd != nil {
		_ = cmd()
	}
	if m.mode != ModeContentViewer {
		t.Fatalf("mode after Done view = %v, want ModeContentViewer", m.mode)
	}
	if m.contentViewer.prevMode != ModeConfirm {
		t.Fatalf("viewer prevMode = %v, want ModeConfirm", m.contentViewer.prevMode)
	}
	if !strings.Contains(m.contentViewer.content, "All done.") {
		t.Fatalf("viewer content = %q", m.contentViewer.content)
	}
}

func TestHandleConfirmDoneIgnoresEditShortcut(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	m.mode = ModeConfirm
	m.confirm.request = &ConfirmRequest{ToolName: "done", ArgsJSON: `{"report":"ok"}`}

	cmd := m.handleConfirmKey(tea.KeyPressMsg(tea.Key{Text: "e", Code: 'e'}))
	if cmd != nil {
		t.Fatalf("Done confirm edit shortcut should be ignored, got %T", cmd)
	}
	if m.confirm.editing {
		t.Fatal("Done confirm should not enter edit mode on E")
	}
}

func TestHandleConfirmDoneDenyRequiresReason(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	m.confirmResultCh = make(chan ConfirmResult, 1)
	m.confirm.request = &ConfirmRequest{ToolName: "done", ArgsJSON: `{}`}
	m.confirm.denyingWithReason = true
	m.confirm.denyReasonInput = newConfirmTextarea(m.width, m.height, "")

	_ = m.handleConfirmDenyReasonKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	select {
	case <-m.confirmResultCh:
		t.Fatal("unexpected confirm result without deny reason")
	default:
	}
	if m.confirm.editError != "Done rejection requires a reason." {
		t.Fatalf("editError = %q", m.confirm.editError)
	}
}

type confirmRuleIntentAgentStub struct {
	sessionControlAgent

	resolveConfirmCalls           int
	resolveConfirmWithIntentCalls int
	lastAction                    string
	lastFinalArgs                 string
	lastRequestID                 string
	lastRuleIntent                *agent.ConfirmRuleIntent
}

func (s *confirmRuleIntentAgentStub) ResolveConfirm(action, finalArgsJSON, editSummary, denyReason, requestID string) {
	s.resolveConfirmCalls++
	s.lastAction = action
	s.lastFinalArgs = finalArgsJSON
	s.lastRequestID = requestID
}

func (s *confirmRuleIntentAgentStub) ResolveConfirmWithRuleIntent(action, finalArgsJSON, editSummary, denyReason, requestID string, ruleIntent *agent.ConfirmRuleIntent) {
	s.resolveConfirmWithIntentCalls++
	s.lastAction = action
	s.lastFinalArgs = finalArgsJSON
	s.lastRequestID = requestID
	if ruleIntent != nil {
		intentCopy := *ruleIntent
		s.lastRuleIntent = &intentCopy
	}
}

func TestResolveConfirmRemoteWithRuleIntentUsesExtendedResolver(t *testing.T) {
	backend := &confirmRuleIntentAgentStub{
		sessionControlAgent: sessionControlAgent{events: make(chan agent.AgentEvent)},
	}
	m := NewModelWithSize(backend, 100, 30)
	m.confirm.request = &ConfirmRequest{
		ToolName:  "shell",
		ArgsJSON:  `{"command":"git status"}`,
		RequestID: "req-1",
	}
	m.confirm.requestID = "req-1"

	_ = m.resolveConfirm(ConfirmResult{
		Action: ConfirmAllow,
		RuleIntent: &ConfirmRuleIntent{
			Pattern: "git *",
			Scope:   permission.ScopeProject,
		},
	})

	if backend.resolveConfirmWithIntentCalls != 1 {
		t.Fatalf("ResolveConfirmWithRuleIntent calls = %d, want 1", backend.resolveConfirmWithIntentCalls)
	}
	if backend.resolveConfirmCalls != 0 {
		t.Fatalf("ResolveConfirm calls = %d, want 0 when extended resolver is available", backend.resolveConfirmCalls)
	}
	if backend.lastRequestID != "req-1" {
		t.Fatalf("request id = %q, want req-1", backend.lastRequestID)
	}
	if backend.lastRuleIntent == nil {
		t.Fatal("expected rule intent forwarded to extended resolver")
	}
	if backend.lastRuleIntent.Pattern != "git *" || backend.lastRuleIntent.Scope != int(permission.ScopeProject) {
		t.Fatalf("rule intent = %#v, want pattern=git * scope=%d", backend.lastRuleIntent, int(permission.ScopeProject))
	}
}
