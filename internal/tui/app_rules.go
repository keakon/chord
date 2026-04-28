package tui

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/keakon/chord/internal/permission"
)

type overlayRulesSource interface {
	AddedOverlayRules() []permission.AddedRule
	RemoveOverlayAddedRule(index int) error
}

// rulesState holds transient state for the /rules overlay.
type rulesState struct {
	rules     []permission.AddedRule
	cursor    int
	prevMode  Mode
	fromAgent bool
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

	case "d", "D":
		return m.deleteCurrentRule()

	case "o", "O":
		return m.openCurrentRuleFile()
	}
	return nil
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
	role := ""
	if m.agent != nil {
		role = strings.TrimSpace(m.agent.CurrentRole())
	}
	path := resolveRuleScopePath(scope, m.usageStatsProjectRoot(), m.homeDir, role)
	m.rules.rules = append(m.rules.rules, permission.AddedRule{
		Role: role,
		Rule: permission.Rule{
			Permission: perm,
			Pattern:    pattern,
			Action:     permission.ActionAllow,
		},
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
	if len(m.rules.rules) == 0 {
		m.rules.cursor = 0
		return m.enqueueToast("No rules added this session", "info")
	}
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
	maxWidth := m.width - 4
	if maxWidth > 100 {
		maxWidth = 100
	}
	if maxWidth < 40 {
		maxWidth = 40
	}

	title := fmt.Sprintf("Session Rules (%d added)", len(m.rules.rules))
	sep := ConfirmSeparatorStyle.Render(title)
	lines := []string{sep}

	if len(m.rules.rules) == 0 {
		lines = append(lines, "")
		lines = append(lines, DimStyle.Render("  No rules added this session."))
	} else {
		for i, r := range m.rules.rules {
			prefix := "  "
			if i == m.rules.cursor {
				prefix = "> "
			}
			scopeStr := scopeLabelStr(r.Scope)
			line := fmt.Sprintf("%s[%s] %s \"%s\"", prefix, scopeStr, r.Rule.Permission, r.Rule.Pattern)
			if i == m.rules.cursor {
				lines = append(lines, ConfirmAllowStyle.Render(line))
			} else {
				lines = append(lines, line)
			}
			if strings.TrimSpace(r.Path) != "" {
				pathLine := fmt.Sprintf("    -> %s", r.Path)
				if i == m.rules.cursor {
					lines = append(lines, DimStyle.Render(pathLine))
				} else {
					lines = append(lines, DimStyle.Render(pathLine))
				}
			}
		}
	}

	lines = append(lines, "")
	lines = append(lines, DimStyle.Render("[↑↓] Move  [D] Delete  [O] Open file  [Esc/Q] Close"))

	body := strings.Join(lines, "\n")
	return DirectoryBorderStyle.Width(maxWidth).Render(body)
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
