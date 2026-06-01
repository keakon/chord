package tui

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

type overlayRulesSource interface {
	AddedOverlayRules() []permission.AddedRule
	AddOverlayRule(rule permission.Rule, scope permission.RuleScope) error
	RemoveOverlayAddedRule(index int) error
}

type rulesAddField int

const (
	rulesAddFieldTool rulesAddField = iota
	rulesAddFieldPattern
)

var rulesAddScopes = []permission.RuleScope{
	permission.ScopeSession,
	permission.ScopeProject,
	permission.ScopeUserGlobal,
}

var rulesAddActions = []permission.Action{
	permission.ActionAllow,
	permission.ActionAsk,
	permission.ActionDeny,
}

// rulesState holds transient state for the /rules overlay.
type rulesState struct {
	rules     []permission.AddedRule
	cursor    int
	prevMode  Mode
	fromAgent bool

	adding       bool
	addField     rulesAddField
	addToolInput textinput.Model
	addPatInput  textinput.Model
	addScopeIdx  int
	addActionIdx int
	addError     string
}

func (m *Model) rulesSource() overlayRulesSource {
	if m == nil || m.agent == nil {
		return nil
	}
	src, ok := m.agent.(overlayRulesSource)
	if !ok {
		return nil
	}
	return src
}

// handleRulesKey processes key events while in ModeRules.
func (m *Model) handleRulesKey(msg tea.KeyMsg) tea.Cmd {
	if m.rules.adding {
		return m.handleRulesAddKey(msg)
	}
	switch msg.String() {
	case "esc", "q":
		m.mode = m.rules.prevMode
		m.rules.prevMode = ModeInsert
		m.recalcViewportSize()
		return nil

	case "up", "k":
		if m.rules.cursor > 0 {
			m.rules.cursor--
			m.recalcViewportSize()
		}
		return nil

	case "down", "j":
		if m.rules.cursor < len(m.rules.rules)-1 {
			m.rules.cursor++
			m.recalcViewportSize()
		}
		return nil

	case "a", "A":
		return m.startAddRule()

	case "d", "D":
		return m.deleteCurrentRule()

	case "o", "O":
		return m.openCurrentRuleFile()
	}
	return nil
}

func newRuleTextInput(prompt, placeholder, value string) textinput.Model {
	ti := textinput.New()
	ti.Prompt = prompt
	ti.Placeholder = placeholder
	ti.CharLimit = 512
	ti.SetValue(value)
	ti.CursorEnd()
	ti.Focus()
	return ti
}

func (m *Model) startAddRule() tea.Cmd {
	m.rules.adding = true
	m.rules.addField = rulesAddFieldTool
	m.rules.addToolInput = newRuleTextInput("Tool: ", tools.NameShell, tools.NameShell)
	m.rules.addPatInput = newRuleTextInput("Pattern: ", "git *", "")
	m.rules.addPatInput.Blur()
	m.rules.addScopeIdx = 0
	m.rules.addActionIdx = 0
	m.rules.addError = ""
	m.recalcViewportSize()
	return textinput.Blink
}

func (m *Model) handleRulesAddKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.rules.adding = false
		m.rules.addError = ""
		m.recalcViewportSize()
		return nil
	case "tab", "shift+tab":
		if m.rules.addField == rulesAddFieldTool {
			m.rules.addField = rulesAddFieldPattern
			m.rules.addToolInput.Blur()
			m.rules.addPatInput.Focus()
		} else {
			m.rules.addField = rulesAddFieldTool
			m.rules.addPatInput.Blur()
			m.rules.addToolInput.Focus()
		}
		m.recalcViewportSize()
		return nil
	case "ctrl+s":
		m.rules.addScopeIdx = (m.rules.addScopeIdx + 1) % len(rulesAddScopes)
		m.recalcViewportSize()
		return nil
	case "ctrl+a":
		m.rules.addActionIdx = (m.rules.addActionIdx + 1) % len(rulesAddActions)
		m.recalcViewportSize()
		return nil
	case "enter":
		return m.submitAddRule()
	}
	if m.rules.addError != "" {
		m.rules.addError = ""
	}
	var cmd tea.Cmd
	if m.rules.addField == rulesAddFieldTool {
		m.rules.addToolInput, cmd = m.rules.addToolInput.Update(msg)
	} else {
		m.rules.addPatInput, cmd = m.rules.addPatInput.Update(msg)
	}
	return cmd
}

func (m *Model) submitAddRule() tea.Cmd {
	toolName := tools.NormalizeName(m.rules.addToolInput.Value())
	pattern := strings.TrimSpace(m.rules.addPatInput.Value())
	if toolName == "" {
		m.rules.addError = "Tool is required."
		m.recalcViewportSize()
		return nil
	}
	if pattern == "" {
		m.rules.addError = "Pattern is required."
		m.recalcViewportSize()
		return nil
	}
	rule := permission.Rule{Permission: toolName, Pattern: pattern, Action: rulesAddActions[m.rules.addActionIdx]}
	scope := rulesAddScopes[m.rules.addScopeIdx]
	if src := m.rulesSource(); src != nil {
		if err := src.AddOverlayRule(rule, scope); err != nil {
			m.rules.addError = err.Error()
			m.recalcViewportSize()
			return nil
		}
		m.rules.fromAgent = true
		m.rules.rules = src.AddedOverlayRules()
	} else {
		m.rules.addError = "Rule backend unavailable."
		m.recalcViewportSize()
		return nil
	}
	m.rules.adding = false
	m.rules.addError = ""
	if len(m.rules.rules) > 0 {
		m.rules.cursor = len(m.rules.rules) - 1
	}
	m.recalcViewportSize()
	return m.enqueueToast("Rule added", "info")
}

func (m *Model) deleteCurrentRule() tea.Cmd {
	if len(m.rules.rules) == 0 {
		return nil
	}
	idx := m.rules.cursor
	if idx < 0 || idx >= len(m.rules.rules) {
		return nil
	}

	if m.rules.fromAgent {
		src := m.rulesSource()
		if src == nil {
			return m.enqueueToast("Rule backend unavailable", "warn")
		}
		if err := src.RemoveOverlayAddedRule(idx); err != nil {
			return m.enqueueToast(fmt.Sprintf("Failed to remove rule: %v", err), "error")
		}
		m.rules.rules = src.AddedOverlayRules()
	} else {
		m.rules.rules = append(m.rules.rules[:idx], m.rules.rules[idx+1:]...)
	}

	if m.rules.cursor >= len(m.rules.rules) {
		m.rules.cursor = len(m.rules.rules) - 1
	}
	if m.rules.cursor < 0 {
		m.rules.cursor = 0
	}
	m.recalcViewportSize()
	return m.enqueueToast("Rule removed", "info")
}

func (m *Model) openCurrentRuleFile() tea.Cmd {
	if len(m.rules.rules) == 0 {
		return nil
	}
	idx := m.rules.cursor
	if idx < 0 || idx >= len(m.rules.rules) {
		return nil
	}
	r := m.rules.rules[idx]
	path := strings.TrimSpace(r.Path)
	if path == "" {
		role := strings.TrimSpace(r.Role)
		if role == "" && m.agent != nil {
			role = strings.TrimSpace(m.agent.CurrentRole())
		}
		path = resolveRuleScopePath(r.Scope, m.usageStatsProjectRoot(), m.homeDir, role)
	}
	if path == "" {
		return m.enqueueToast("This rule has no backing file", "warn")
	}
	if err := openPathInOS(path); err != nil {
		return m.enqueueToast(fmt.Sprintf("Failed to open rule file: %v", err), "error")
	}
	return m.enqueueToast(fmt.Sprintf("Opened rule file: %s", path), "info")
}

func openPathInOS(path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	return cmd.Start()
}

// addSessionRule tracks a rule in the local /rules overlay list.
// In local mode this list is refreshed from agent overlay; in remote mode this
// acts as fallback so /rules can still show picker-added entries.
func (m *Model) addSessionRule(perm, pattern string, scope permission.RuleScope) {
	m.addRuleToLocalList(permission.Rule{Permission: perm, Pattern: pattern, Action: permission.ActionAllow}, scope)
}

func (m *Model) addRuleToLocalList(rule permission.Rule, scope permission.RuleScope) {
	role := ""
	if m.agent != nil {
		role = strings.TrimSpace(m.agent.CurrentRole())
	}
	path := resolveRuleScopePath(scope, m.usageStatsProjectRoot(), m.homeDir, role)
	m.rules.rules = append(m.rules.rules, permission.AddedRule{
		Role:    role,
		Rule:    rule,
		Scope:   scope,
		Path:    path,
		AddedAt: time.Now(),
	})
}

// openRules opens the /rules overlay.
func (m *Model) openRules() tea.Cmd {
	m.rules.prevMode = m.mode
	if src := m.rulesSource(); src != nil {
		m.rules.rules = src.AddedOverlayRules()
		m.rules.fromAgent = true
	} else {
		m.rules.fromAgent = false
	}
	m.rules.adding = false
	m.rules.addError = ""
	if m.rules.cursor >= len(m.rules.rules) {
		m.rules.cursor = len(m.rules.rules) - 1
	}
	if m.rules.cursor < 0 {
		m.rules.cursor = 0
	}
	m.mode = ModeRules
	m.recalcViewportSize()
	return nil
}

// renderRulesList renders the /rules overlay.
func (m *Model) renderRulesList() string {
	maxWidth := min(m.width-4, 100)
	if maxWidth < 40 {
		maxWidth = 40
	}

	if m.rules.adding {
		return m.renderRulesAdd(maxWidth)
	}

	title := fmt.Sprintf("Permission Rules (%d added)", len(m.rules.rules))
	sep := ConfirmSeparatorStyle.Render(title)
	lines := []string{sep}

	if len(m.rules.rules) == 0 {
		lines = append(lines, "")
		lines = append(lines, DimStyle.Render("  No remembered rules yet."))
		lines = append(lines, DimStyle.Render("  Press A to add one manually, or use M in a confirmation dialog."))
	} else {
		for i, r := range m.rules.rules {
			prefix := "  "
			if i == m.rules.cursor {
				prefix = "> "
			}
			scopeStr := scopeLabelStr(r.Scope)
			line := fmt.Sprintf("%s[%s] %s %s \"%s\"", prefix, scopeStr, r.Rule.Permission, r.Rule.Action, r.Rule.Pattern)
			if i == m.rules.cursor {
				lines = append(lines, ConfirmAllowStyle.Render(line))
			} else {
				lines = append(lines, line)
			}
			if strings.TrimSpace(r.Path) != "" {
				pathLine := fmt.Sprintf("    -> %s", r.Path)
				lines = append(lines, DimStyle.Render(pathLine))
			}
		}
	}

	lines = append(lines, "")
	lines = append(lines, DimStyle.Render("[A] Add  [↑↓] Move  [D] Delete  [O] Open file  [Esc/Q] Close"))

	body := strings.Join(lines, "\n")
	return DirectoryBorderStyle.Width(maxWidth).Render(body)
}

func (m *Model) renderRulesAdd(maxWidth int) string {
	lines := []string{ConfirmSeparatorStyle.Render("Add Permission Rule"), ""}
	toolLine := m.rules.addToolInput.View()
	patternLine := m.rules.addPatInput.View()
	if m.rules.addField == rulesAddFieldTool {
		toolLine = ConfirmAllowStyle.Render(toolLine)
	} else {
		toolLine = DimStyle.Render(toolLine)
	}
	if m.rules.addField == rulesAddFieldPattern {
		patternLine = ConfirmAllowStyle.Render(patternLine)
	} else {
		patternLine = DimStyle.Render(patternLine)
	}
	lines = append(lines, toolLine, patternLine, "")
	lines = append(lines, fmt.Sprintf("Scope: %s", ConfirmAllowStyle.Render(scopeLabelStr(rulesAddScopes[m.rules.addScopeIdx]))))
	lines = append(lines, fmt.Sprintf("Action: %s", ConfirmAllowStyle.Render(string(rulesAddActions[m.rules.addActionIdx]))))
	if m.rules.addError != "" {
		lines = append(lines, "", ConfirmDenyStyle.Render(m.rules.addError))
	}
	lines = append(lines, "")
	lines = append(lines, DimStyle.Render("[Tab] field  [Ctrl+S] scope  [Ctrl+A] action  [Enter] add  [Esc] back"))
	return DirectoryBorderStyle.Width(maxWidth).Render(strings.Join(lines, "\n"))
}

func scopeLabelStr(scope permission.RuleScope) string {
	switch scope {
	case permission.ScopeSession:
		return "session"
	case permission.ScopeProject:
		return "project"
	case permission.ScopeUserGlobal:
		return "global"
	default:
		return "unknown"
	}
}
