package tui

import (
	"fmt"
	"image"
	"sort"
	"strings"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/skill"
)

// ---------------------------------------------------------------------------
// Skill selector state (/skill)
// ---------------------------------------------------------------------------

type skillSelectState struct {
	states   []skill.InvocationState
	selector overlayListSelectorState
	prevMode Mode
	filter   string

	filterFocused bool
}

const (
	skillSelectIdleHint   = "j/k move  g/G jump  enter load  / filter  esc close"
	skillSelectFilterHint = "type to filter  enter keep  esc clear"
)

// skillVisibilityGlyph encodes model visibility in the glyph shape and leaves
// the load color to the caller: dashed ◌ marks a skill the model never sees
// (manual-only or ruleset-denied), while the solid ○/● pair marks a
// model-visible skill and whether its instructions are currently loaded. The
// tool card uses ◌ for "receiving", so UI labels always keep the skill name
// next to the glyph instead of letting it read as progress.
func skillVisibilityGlyph(notModelVisible, loaded bool) string {
	if notModelVisible {
		return "◌"
	}
	if loaded {
		return "●"
	}
	return "○"
}

// skillReasonText renders a machine-readable InvocationState.Reason for the
// selector. Denied-by-ruleset and missing entries are the only disabled ones,
// so a manual-only skill (loadable, just invisible to the model) never lands
// here.
func skillReasonText(reason string) string {
	switch reason {
	case skill.ReasonDeniedByRuleset:
		return "not allowed for this role"
	case skill.ReasonUnavailable:
		return "unavailable"
	default:
		return "not available"
	}
}

func skillSelectMatches(st skill.InvocationState, tokens []string) bool {
	if len(tokens) == 0 {
		return true
	}
	if st.Meta == nil {
		return false
	}
	haystack := strings.ToLower(strings.TrimSpace(st.Meta.Name) + " " + strings.TrimSpace(st.Meta.Description))
	for _, token := range tokens {
		if !strings.Contains(haystack, token) {
			return false
		}
	}
	return true
}

func skillSelectItemFor(st skill.InvocationState) OverlayListItem {
	meta := st.Meta
	name := strings.TrimSpace(meta.Name)
	// Shape follows ModelVisible rather than frontmatter alone so a
	// ruleset-denied skill does not render as model-visible.
	glyph := skillVisibilityGlyph(!st.ModelVisible, st.Loaded)
	label := glyph + " " + name
	if desc := strings.TrimSpace(meta.Description); desc != "" {
		label += "  " + desc
	}
	item := OverlayListItem{ID: name, Label: label}
	if !st.UserLoadable {
		// The reason replaces the description: a row the user cannot load is
		// explained by why, not by what the skill does.
		item.Disabled = true
		item.Label = glyph + " " + name + " — " + skillReasonText(st.Reason)
		return item
	}
	if st.Loaded {
		item.Label += "  (loaded)"
	}
	return item
}

// skillSelectItems groups the catalog into MANUAL and AVAILABLE sections, each
// sorted by name. Group headers carry no ID, so the list's cursor walk skips
// them and the current-selection helpers never read one as a skill.
func skillSelectItems(states []skill.InvocationState, filter string) []OverlayListItem {
	tokens := strings.Fields(strings.ToLower(filter))
	manual := make([]skill.InvocationState, 0, len(states))
	model := make([]skill.InvocationState, 0, len(states))
	for _, st := range states {
		if st.Meta == nil || strings.TrimSpace(st.Meta.Name) == "" {
			continue
		}
		if !skillSelectMatches(st, tokens) {
			continue
		}
		if st.Meta.DisableModelInvocation {
			manual = append(manual, st)
		} else {
			model = append(model, st)
		}
	}
	items := make([]OverlayListItem, 0, len(states)+2)
	appendGroup := func(header string, group []skill.InvocationState) {
		if len(group) == 0 {
			return
		}
		sort.Slice(group, func(i, j int) bool { return group[i].Meta.Name < group[j].Meta.Name })
		items = append(items, OverlayListItem{Header: true, Label: header})
		for _, st := range group {
			items = append(items, skillSelectItemFor(st))
		}
	}
	appendGroup("MANUAL", manual)
	appendGroup("AVAILABLE", model)
	return items
}

func (m *Model) skillSelectMaxVisible() int {
	return max(m.height/2-4, 3)
}

func (m *Model) openSkillSelect() {
	if m.agent == nil {
		return
	}
	sp, ok := m.agent.(agent.FocusedSkillInvocationStateProvider)
	if !ok {
		_ = m.enqueueToast("Skill state is not available", "warn")
		return
	}
	states := sp.FocusedSkillInvocationStates()
	if len(states) == 0 {
		_ = m.enqueueToast("No skills available for this agent", "info")
		return
	}

	m.clearChordState()
	m.skillSelect = skillSelectState{prevMode: m.mode, states: states}
	m.skillSelect.selector.list = NewOverlayList(skillSelectItems(states, ""), m.skillSelectMaxVisible())
	m.mode = ModeSkillSelect
	m.recalcViewportSize()
}

func (m *Model) closeSkillSelect() tea.Cmd {
	prevMode := m.skillSelect.prevMode
	m.skillSelect = skillSelectState{}
	cmd := m.restoreModeWithIME(prevMode)
	m.recalcViewportSize()
	if prevMode == ModeInsert {
		return tea.Batch(cmd, m.input.Focus())
	}
	return cmd
}

func (m *Model) rebuildSkillSelectItems(resetCursor bool) {
	items := skillSelectItems(m.skillSelect.states, m.skillSelect.filter)
	if m.skillSelect.selector.list == nil {
		m.skillSelect.selector.list = NewOverlayList(items, m.skillSelectMaxVisible())
	} else {
		m.skillSelect.selector.list.SetItems(items)
	}
	if resetCursor && len(items) > 0 {
		m.skillSelect.selector.list.SetCursor(0)
	}
	m.skillSelect.selector.renderCacheText = ""
}

func (m *Model) setSkillSelectFilterFocused(focused bool) {
	if m.skillSelect.filterFocused == focused {
		return
	}
	m.skillSelect.filterFocused = focused
	m.skillSelect.selector.renderCacheText = ""
}

// skillSelectApplyAtCursor backfills the composer with `/skill <name> ` instead
// of dispatching the load itself. The user appends args and submits, so the
// execution path stays the single ordinary user-message route.
func (m *Model) skillSelectApplyAtCursor() tea.Cmd {
	if m.skillSelect.selector.list == nil {
		return nil
	}
	item, ok := m.skillSelect.selector.list.SelectedItem()
	if !ok || item.Header {
		return nil
	}
	if item.Disabled {
		return m.enqueueToast(fmt.Sprintf("Skill %q is not available to this agent", item.ID), "info")
	}
	name := strings.TrimSpace(item.ID)
	if name == "" {
		return nil
	}
	m.input.SetValue("/skill " + name + " ")
	return m.closeSkillSelect()
}

func (m *Model) handleSkillSelectKey(msg tea.KeyMsg) tea.Cmd {
	if m.skillSelect.filterFocused {
		return m.handleSkillSelectFilterKey(msg)
	}
	key := msg.String()
	if keyMatches(key, m.keyMap.InsertEscape) || key == "esc" {
		return m.closeSkillSelect()
	}
	switch key {
	case "j", "down":
		if m.skillSelect.selector.list != nil {
			m.skillSelect.selector.list.CursorDown()
		}
		return nil
	case "k", "up":
		if m.skillSelect.selector.list != nil {
			m.skillSelect.selector.list.CursorUp()
		}
		return nil
	case "g":
		if m.skillSelect.selector.list != nil {
			m.skillSelect.selector.list.CursorToTop()
		}
		return nil
	case "G":
		if m.skillSelect.selector.list != nil {
			m.skillSelect.selector.list.CursorToBottom()
		}
		return nil
	case "/":
		m.setSkillSelectFilterFocused(true)
		return nil
	case "enter":
		return m.skillSelectApplyAtCursor()
	}
	return nil
}

func (m *Model) handleSkillSelectFilterKey(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()
	if keyMatches(key, m.keyMap.InsertEscape) || key == "esc" {
		hadFilter := m.skillSelect.filter != ""
		m.skillSelect.filter = ""
		if hadFilter {
			m.rebuildSkillSelectItems(true)
		}
		m.setSkillSelectFilterFocused(false)
		return nil
	}
	if key == "ctrl+u" {
		if m.skillSelect.filter == "" {
			return nil
		}
		m.skillSelect.filter = ""
		m.rebuildSkillSelectItems(true)
		return nil
	}

	switch msg.Key().Code {
	case tea.KeyEnter:
		m.setSkillSelectFilterFocused(false)
		return nil
	case tea.KeyUp:
		if m.skillSelect.selector.list != nil {
			m.skillSelect.selector.list.CursorUp()
		}
		return nil
	case tea.KeyDown:
		if m.skillSelect.selector.list != nil {
			m.skillSelect.selector.list.CursorDown()
		}
		return nil
	case tea.KeyBackspace:
		if m.skillSelect.filter == "" {
			return nil
		}
		runes := []rune(m.skillSelect.filter)
		m.skillSelect.filter = string(runes[:len(runes)-1])
		m.rebuildSkillSelectItems(true)
		return nil
	case tea.KeySpace:
		m.skillSelect.filter += " "
		m.rebuildSkillSelectItems(true)
		return nil
	}

	if text := msg.Key().Text; text != "" {
		m.skillSelect.filter += text
		m.rebuildSkillSelectItems(true)
	}
	return nil
}

func (m *Model) renderSkillSelectFilterLine(innerWidth int) string {
	if innerWidth <= 0 {
		return ""
	}
	hintMode := !m.skillSelect.filterFocused && strings.TrimSpace(m.skillSelect.filter) == ""
	text := "filter: "
	if hintMode {
		text += "(press / to search)"
	} else {
		text += m.skillSelect.filter
		if m.skillSelect.filterFocused {
			text += "_"
		}
	}
	if hintMode {
		return DimStyle.Render(truncateOneLine(text, innerWidth))
	}
	return truncateOneLine(text, innerWidth)
}

func (m *Model) renderSkillSelectDialog() string {
	if m.skillSelect.selector.list == nil {
		return ""
	}

	hint := skillSelectIdleHint
	if m.skillSelect.filterFocused {
		hint = skillSelectFilterHint
	}
	overlayCfg := OverlayConfig{
		Title:    "Skills",
		Hint:     hint,
		MinWidth: 30,
		MaxWidth: 70,
	}
	area := image.Rect(0, 0, m.width, m.height)
	overlayCfg = normalizeOverlayConfig(overlayCfg, area)
	contentWidth := overlayCfg.MaxWidth - 4
	filterLine := m.renderSkillSelectFilterLine(contentWidth)

	return m.skillSelect.selector.Render(
		m,
		overlayCfg,
		filterLine,
		0,
		m.skillSelectMaxVisible(),
		filterLine+"\x00"+hint,
		nil,
		area,
	)
}

func (m *Model) skillSelectOptionIndexAt(x, y int) (int, bool) {
	dialog := m.renderSkillSelectDialog()
	return m.skillSelect.selector.IndexAt(m, dialog, x, y)
}
