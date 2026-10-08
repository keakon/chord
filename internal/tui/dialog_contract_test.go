package tui

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func TestDialogActionsKeepFocusAndDangerSeparate(t *testing.T) {
	ApplyTheme(DefaultTheme())
	for _, chip := range []hintChip{hint("Esc", "Cancel"), primaryHint("Enter", "submit"), dangerHint("y", "Delete")} {
		line := renderHintChip(chip)
		cells := drawLineCells(t, line)
		for _, cell := range cells {
			if !cell.Style.Bg.IsZero() {
				t.Fatalf("action hint acquired a focus background: %q", ansi.Strip(line))
			}
		}
		key, action := cells[1], cells[len(cells)-1]
		if chip.primary || chip.danger {
			if !colorsEqual(key.Style.Fg, action.Style.Fg) {
				t.Fatal("action meaning was weaker than its key")
			}
		}
		if chip.danger && !colorsEqual(action.Style.Fg, colorOfTheme(currentTheme.DialogDangerFg)) {
			t.Fatal("destructive action has no danger color")
		}
		if !chip.danger && colorsEqual(action.Style.Fg, colorOfTheme(currentTheme.DialogDangerFg)) {
			t.Fatal("ordinary action has a danger color")
		}
	}
}

func TestDangerConfirmationsFitAndRetainDecisionsAcrossResize(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.sessionDeleteConfirm.session = &agent.SessionSummary{ID: "sample-session", FirstUserMessage: strings.Repeat("sample preview ", 100)}
	m.stopJobConfirm.jobID = "job-1"
	m.stopJobConfirm.command = strings.Repeat("sample-command ", 50)
	m.stopJobConfirm.tail = strings.Repeat("sample output\n", 30)
	for _, size := range [][2]int{{120, 40}, {30, 6}, {40, 12}, {80, 24}} {
		m.width, m.height = size[0], size[1]
		for _, dialog := range []string{m.renderSessionDeleteConfirmDialog(), m.renderStopJobConfirmDialog()} {
			if lipgloss.Width(dialog) > size[0]-1 || lipgloss.Height(dialog) > size[1] {
				t.Fatalf("dialog exceeds %dx%d: %dx%d", size[0], size[1], lipgloss.Width(dialog), lipgloss.Height(dialog))
			}
			plain := ansi.Strip(dialog)
			if !strings.Contains(plain, "[y]") || !strings.Contains(plain, "[n/esc] Cancel") {
				t.Fatalf("decisions lost at %dx%d:\n%s", size[0], size[1], plain)
			}
		}
	}
}

func TestSessionDeleteRequiresExplicitConfirmation(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 80, 24)
	m.mode = ModeSessionDeleteConfirm
	m.sessionDeleteConfirm.session = &agent.SessionSummary{ID: "sample-session"}
	m.handleSessionDeleteConfirmKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if len(backend.deleteSessionIDs) != 0 || m.mode != ModeSessionDeleteConfirm {
		t.Fatal("Enter implicitly confirmed deletion")
	}
	m.handleSessionDeleteConfirmKey(tea.KeyPressMsg(tea.Key{Text: "y", Code: 'y'}))
	if len(backend.deleteSessionIDs) != 1 {
		t.Fatal("explicit confirmation did not delete the session")
	}
}

func TestConfirmActionsRemainClickableInCompactLayouts(t *testing.T) {
	for _, size := range [][2]int{{30, 6}, {40, 12}, {80, 24}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			m := NewModelWithSize(nil, size[0], size[1])
			m.mode = ModeConfirm
			m.confirm.request = &ConfirmRequest{ToolName: "shell", ArgsJSON: `{"command":"sample-command"}`}
			dialog := m.renderConfirmDialog()
			if lipgloss.Width(dialog) > size[0]-1 || lipgloss.Height(dialog) > size[1] {
				t.Fatalf("confirmation exceeds bounds:\n%s", ansi.Strip(dialog))
			}
			lines := strings.Split(ansi.Strip(dialog), "\n")
			rect := m.overlayRect(dialog)
			for _, action := range []confirmDialogAction{confirmDialogAllow, confirmDialogDeny} {
				hit := confirmHitboxForTest(t, &m, action)
				row := hit.minY - rect.Min.Y
				if row < 0 || row >= len(lines) {
					t.Fatal("hit target is outside the rendered frame")
				}
				if action == confirmDialogAllow && !strings.Contains(lines[row], "Allow") || action == confirmDialogDeny && !strings.Contains(lines[row], "Deny") {
					t.Fatalf("hit target names a different row: %q", lines[row])
				}
			}
		})
	}
}

func TestRulePickerKeepsFocusedCandidateAndScopeVisibleAfterResize(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.confirm.request = &ConfirmRequest{ToolName: "shell", ArgsJSON: `{}`}
	m.confirm.pickingRule = true
	m.confirm.scopes = []permission.RuleScope{permission.ScopeSession, permission.ScopeProject}
	for i := range 20 {
		m.confirm.candidates = append(m.confirm.candidates, PatternCandidate{Pattern: fmt.Sprintf("sample-%02d", i)})
	}
	m.renderConfirmDialog()
	for range 19 {
		m.handleConfirmKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	}
	m.handleConfirmKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	for _, size := range [][2]int{{30, 6}, {40, 12}, {80, 24}} {
		m.width, m.height = size[0], size[1]
		dialog := m.renderConfirmDialog()
		plain := ansi.Strip(dialog)
		if lipgloss.Width(dialog) > size[0]-1 || lipgloss.Height(dialog) > size[1] {
			t.Fatalf("rule picker exceeds %dx%d:\n%s", size[0], size[1], plain)
		}
		for _, want := range []string{"sample-19", "Scope: project", "[Enter]", "[Esc] back"} {
			if !strings.Contains(plain, want) {
				t.Fatalf("rule picker lost %q at %dx%d:\n%s", want, size[0], size[1], plain)
			}
		}
	}
}

func TestModalWheelMovesDialogWithoutMovingConversation(t *testing.T) {
	for _, mode := range []Mode{ModeRules, ModeUsageStats, ModeErrorPanel, ModeHelp} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			m := NewModelWithSize(&sessionControlAgent{}, 80, 12)
			m.mode = mode
			for i := range 20 {
				m.rules.rules = append(m.rules.rules, permission.AddedRule{Rule: permission.Rule{Permission: "shell", Pattern: fmt.Sprintf("sample-%d", i)}})
				m.recordAgentError("main", fmt.Errorf("sample error %d", i), "", "", "", "", "", false)
				m.viewport.AppendBlock(&Block{ID: i + 1, Type: BlockAssistant, Content: strings.Repeat("sample ", 40)})
			}
			m.layout = m.generateLayout(m.width, m.height)
			start := m.viewport.offset
			cmd, handled := m.handleModalMouseMsg(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
			if !handled || cmd != nil || m.viewport.offset != start || m.pendingScrollDelta != 0 {
				t.Fatal("modal wheel changed the conversation")
			}
			moved := false
			switch mode {
			case ModeRules:
				moved = m.rules.cursor > 0
			case ModeUsageStats:
				moved = m.usageStats.scrollOffset > 0
			case ModeErrorPanel:
				moved = m.errorPanel.scrollOffset > 0
			case ModeHelp:
				moved = m.help.scrollOffset > 0
			}
			if !moved {
				t.Fatal("modal wheel did not move its own content")
			}
		})
	}
}

func TestDialogEditorsKeepFocusedInputAndDecisionsInSmallTerminal(t *testing.T) {
	m := NewModelWithSize(nil, 30, 6)
	m.confirm.request = &ConfirmRequest{ToolName: "shell", ArgsJSON: `{}`}
	m.confirm.editing = true
	m.confirm.editInput = newConfirmTextarea(m.width, m.height, "first\nlast-input")
	m.confirm.editError = "Invalid JSON"
	plain := ansi.Strip(m.renderConfirmDialog())
	for _, want := range []string{"last-input", "[Enter] allow", "[Esc] back", "Invalid JSON"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("argument editor lost %q:\n%s", want, plain)
		}
	}
	m.confirm.editing = false
	m.mode = ModeRules
	m.startAddRule()
	m.rules.addField = rulesAddFieldPattern
	m.rules.addToolInput.Blur()
	m.rules.addPatInput.Focus()
	m.rules.addPatInput.SetValue("sample-pattern")
	m.rules.addError = "Pattern rejected"
	dialog := m.renderRulesList()
	plain = ansi.Strip(dialog)
	if lipgloss.Width(dialog) > 29 || lipgloss.Height(dialog) > 6 {
		t.Fatalf("rule editor exceeds terminal bounds:\n%s", plain)
	}
	for _, want := range []string{"sample-pattern", "[Enter] add", "[Esc] back", "Pattern rejected"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("rule editor lost %q:\n%s", want, plain)
		}
	}
	before := m.rules.addPatInput.Value()
	m.handleModalMouseMsg(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if m.rules.addPatInput.Value() != before {
		t.Fatal("wheel changed the active rule field")
	}
}

func TestSubmittingQuestionDropsCachedSubmitHint(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.question.request = &QuestionRequest{Item: tools.QuestionItem{
		Header: "Choice", Question: "Choose a format", Multiple: true,
		Options: []tools.QuestionOption{{ID: "plain", Label: "Plain"}},
	}}
	m.question.selected = map[int]bool{0: true}
	before := ansi.Strip(m.renderQuestionDialog())
	if !strings.Contains(before, "[Enter] submit") {
		t.Fatalf("initial submit hint missing:\n%s", before)
	}
	if !strings.Contains(before, "[Esc] decline") || strings.Contains(before, "hide") || strings.Contains(before, "[Ctrl+D]") {
		t.Fatalf("question hints do not match current refusal bindings:\n%s", before)
	}
	m.resolveQuestion([]string{"plain"}, false)
	during := ansi.Strip(m.renderQuestionDialog())
	if !strings.Contains(during, "Submitting") || !strings.Contains(during, "[Ctrl+W] withdraw") || strings.Contains(during, "[Enter]") || strings.Contains(during, "[Esc]") {
		t.Fatalf("submitting question retained stale actions:\n%s", during)
	}
	m.handleQuestionKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if !m.question.submitting || m.question.request == nil {
		t.Fatal("Escape changed an in-flight submission")
	}
}
