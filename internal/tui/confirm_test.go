package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/permission"
)

func TestWrapConfirmLiteralTextPrefersTokenAndPathBoundaries(t *testing.T) {
	text := "mv docs/plans/DELETE_MULTI_FILE_TOOL_PLAN.md docs/plans/archive/DELETE_MULTI_FILE_TOOL_PLAN.md"
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

func TestRenderConfirmSummarySoftWrapsLongBashCommandWithoutBreakingPathToken(t *testing.T) {
	m := NewModel(nil)
	m.width = 100
	m.confirm.request = &ConfirmRequest{ToolName: "Bash", ArgsJSON: `{"command":"mv docs/plans/DELETE_MULTI_FILE_TOOL_PLAN.md docs/plans/archive/DELETE_MULTI_FILE_TOOL_PLAN.md","workdir":"/tmp/project","timeout":30}`}

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

func TestRenderConfirmSummaryShowsStructuredBashFields(t *testing.T) {
	m := NewModel(nil)
	m.width = 100
	m.confirm.request = &ConfirmRequest{ToolName: "Bash", ArgsJSON: `{"command":"rm internal/tui/usage_stats_state.go","description":"Remove obsolete file","workdir":"/tmp/project","timeout":45}`}

	plain := stripANSI(m.renderConfirmDialog())
	if !strings.Contains(plain, "Tool: Bash") {
		t.Fatalf("expected tool line in confirm dialog, got:\n%s", plain)
	}
	if !strings.Contains(plain, "Action: Execute shell command") {
		t.Fatalf("expected action line in confirm dialog, got:\n%s", plain)
	}
	if !strings.Contains(plain, "Command:") || !strings.Contains(plain, "rm internal/tui/usage_stats_state.go") {
		t.Fatalf("expected command to be visible in summary view, got:\n%s", plain)
	}
	if strings.Contains(plain, "Tool: Bash({") {
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
	m.confirm.request = &ConfirmRequest{ToolName: "Bash", ArgsJSON: `{"command":"sleep 1","timeout":2400}`}

	plain := stripANSI(m.renderConfirmDialog())
	if !strings.Contains(plain, "Timeout: 600s") {
		t.Fatalf("expected confirm summary to show effective capped foreground timeout, got:\n%s", plain)
	}
	if !strings.Contains(plain, "Requested timeout 2400s capped to 600s") {
		t.Fatalf("expected confirm warning about capped timeout, got:\n%s", plain)
	}
}

func TestRenderConfirmSummaryDoesNotTreatBashAsBackground(t *testing.T) {
	m := NewModel(nil)
	m.width = 100
	m.confirm.request = &ConfirmRequest{ToolName: "Bash", ArgsJSON: `{"command":"npm run dev","description":"Frontend dev server","timeout":45}`}

	plain := stripANSI(m.renderConfirmDialog())
	if strings.Contains(plain, "Background:") || strings.Contains(plain, "Max runtime:") || strings.Contains(plain, "Mode:") {
		t.Fatalf("Bash confirm summary should not include background fields, got:\n%s", plain)
	}
}

func TestRenderConfirmSummaryShowsWriteFilePathAndPreview(t *testing.T) {
	m := NewModel(nil)
	m.width = 100
	m.confirm.request = &ConfirmRequest{ToolName: "Write", ArgsJSON: `{"path":"internal/tui/confirm_render.go","content":"line 1\nline 2\nline 3\nline 4"}`}

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

func TestRenderConfirmSummaryShowsDeleteFilePathAndReason(t *testing.T) {
	m := NewModel(nil)
	m.width = 100
	m.confirm.request = &ConfirmRequest{ToolName: "Delete", ArgsJSON: `{"paths":["internal/tui/obsolete.go"],"reason":"remove obsolete file"}`}

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
	m.confirm.request = &ConfirmRequest{ToolName: "Bash", ArgsJSON: `{"command":"rm"`}

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

func TestRenderConfirmDialogEditModeShowsMultilineTextareaAndHint(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	m.confirm.request = &ConfirmRequest{ToolName: "Delete", ArgsJSON: "{\n  \"a\": 1,\n  \"b\": 2\n}"}
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
	m.confirm.request = &ConfirmRequest{ToolName: "Delete", ArgsJSON: "{\n  \"a\": 1,\n  \"b\": 2\n}"}
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
	m.confirm.request = &ConfirmRequest{ToolName: "Delete", ArgsJSON: `{"paths":["a"],"reason":"cleanup"}`}
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
	m := NewModelWithSize(nil, 100, 18)
	m.confirm.request = &ConfirmRequest{
		ToolName:      "Delete",
		ArgsJSON:      `{"paths":["a","b","c","d","e","f","g","h","i","j","k","l"],"reason":"cleanup temporary review files"}`,
		NeedsApproval: []string{"/tmp/a", "/tmp/b", "/tmp/c", "/tmp/d", "/tmp/e", "/tmp/f", "/tmp/g", "/tmp/h", "/tmp/i"},
	}

	rendered := m.renderConfirmDialog()
	plain := stripANSI(rendered)

	if got, limit := lipgloss.Height(rendered), confirmDialogMaxHeight(m.height); got > limit {
		t.Fatalf("confirm dialog height = %d, want <= %d\n%s", got, limit, plain)
	}
	if !strings.Contains(plain, "[Y] Allow") || !strings.Contains(plain, "[N] Deny") || !strings.Contains(plain, "[E] Edit") {
		t.Fatalf("expected confirm actions to remain visible, got:\n%s", plain)
	}
	if !strings.Contains(plain, "more lines hidden") {
		t.Fatalf("expected truncation marker in constrained confirm dialog, got:\n%s", plain)
	}
}

func TestRenderConfirmOptionsIncludesDenyReason(t *testing.T) {
	m := NewModel(nil)
	m.width = 100
	m.confirm.request = &ConfirmRequest{ToolName: "Bash", ArgsJSON: `{"command":"echo hi"}`}

	plain := stripANSI(m.renderConfirmDialog())
	if !strings.Contains(plain, "[R] Deny+Reason") {
		t.Fatalf("expected [R] Deny+Reason option in confirm dialog, got:\n%s", plain)
	}
}

func TestHandleConfirmKeyREntersDenyReasonMode(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	m.confirm.request = &ConfirmRequest{ToolName: "Bash", ArgsJSON: `{"command":"echo hi"}`}

	_ = m.handleConfirmKey(tea.KeyPressMsg(tea.Key{Text: "r", Code: 'r'}))
	if !m.confirm.denyingWithReason {
		t.Fatal("expected denyingWithReason=true after pressing R")
	}
}

func TestRenderConfirmDenyReasonModeShowsHint(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	m.confirm.request = &ConfirmRequest{ToolName: "Bash", ArgsJSON: `{"command":"echo hi"}`}
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

func TestHandleConfirmDenyReasonKeyEnterSubmitsReason(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	m.confirmResultCh = make(chan ConfirmResult, 1)
	m.confirmCh = nil
	m.confirm.request = &ConfirmRequest{ToolName: "Bash", ArgsJSON: `{"command":"echo hi"}`}
	m.confirm.denyingWithReason = true
	m.confirm.denyReasonInput = newConfirmTextarea(m.width, m.height, "")
	m.confirm.denyReasonInput.SetValue("  not safe\nfor production  ")

	_ = m.handleConfirmDenyReasonKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	select {
	case result := <-m.confirmResultCh:
		if result.Action != ConfirmDeny {
			t.Fatalf("action = %v, want %v", result.Action, ConfirmDeny)
		}
		if result.DenyReason != "not safe for production" {
			t.Fatalf("deny reason = %q, want %q", result.DenyReason, "not safe for production")
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
	m.confirm.request = &ConfirmRequest{ToolName: "Bash", ArgsJSON: `{"command":"echo hi"}`}
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

func TestHandleConfirmDenyReasonKeyNormalizesLongReason(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	m.confirmResultCh = make(chan ConfirmResult, 1)
	m.confirmCh = nil
	m.confirm.request = &ConfirmRequest{ToolName: "Bash", ArgsJSON: `{"command":"echo hi"}`}
	m.confirm.denyingWithReason = true
	m.confirm.denyReasonInput = newConfirmTextarea(m.width, m.height, "")
	longReason := strings.Repeat("🙂", 210)
	m.confirm.denyReasonInput.SetValue(longReason)

	_ = m.handleConfirmDenyReasonKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	select {
	case result := <-m.confirmResultCh:
		if got := len([]rune(result.DenyReason)); got != 200 {
			t.Fatalf("deny reason rune length = %d, want 200", got)
		}
		if want := strings.Repeat("🙂", 200); result.DenyReason != want {
			t.Fatalf("deny reason should preserve rune boundaries")
		}
	default:
		t.Fatal("expected confirm result after pressing Enter")
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
		ToolName:  "Bash",
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

type confirmLegacyResolverAgentStub struct {
	sessionControlAgent
	resolveConfirmCalls int
	lastRequestID       string
}

func (s *confirmLegacyResolverAgentStub) ResolveConfirm(action, finalArgsJSON, editSummary, denyReason, requestID string) {
	s.resolveConfirmCalls++
	s.lastRequestID = requestID
}

func TestResolveConfirmRemoteWithRuleIntentFallsBackToLegacyResolver(t *testing.T) {
	backend := &confirmLegacyResolverAgentStub{
		sessionControlAgent: sessionControlAgent{events: make(chan agent.AgentEvent)},
	}
	m := NewModelWithSize(backend, 100, 30)
	m.confirm.request = &ConfirmRequest{
		ToolName:  "Bash",
		ArgsJSON:  `{"command":"git status"}`,
		RequestID: "req-2",
	}
	m.confirm.requestID = "req-2"

	cmd := m.resolveConfirm(ConfirmResult{
		Action: ConfirmAllow,
		RuleIntent: &ConfirmRuleIntent{
			Pattern: "git *",
			Scope:   permission.ScopeSession,
		},
	})

	if backend.resolveConfirmCalls != 1 {
		t.Fatalf("legacy ResolveConfirm calls = %d, want 1", backend.resolveConfirmCalls)
	}
	if backend.lastRequestID != "req-2" {
		t.Fatalf("request id = %q, want req-2", backend.lastRequestID)
	}
	if got := len(m.rules.rules); got != 0 {
		t.Fatalf("local /rules entries = %d, want 0 when backend lacks rule-intent support", got)
	}
	if cmd == nil {
		t.Fatal("expected warning toast cmd when backend lacks rule-intent support")
	}
	if msg := cmd(); msg == nil {
		t.Fatal("expected warning toast tick message")
	}
	if m.activeToast == nil || !strings.Contains(m.activeToast.Message, "does not support") {
		t.Fatalf("expected warning toast about unsupported backend, got %#v", m.activeToast)
	}
}
