package tui

import (
	"fmt"
	"image"
	"sort"
	"strings"

	tea "github.com/keakon/bubbletea/v2"
	"github.com/keakon/lipgloss/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/skill"
	"github.com/keakon/chord/internal/tui/overlaylist"
)

// ---------------------------------------------------------------------------
// Skill selector state (/skill)
// ---------------------------------------------------------------------------

type skillSelectState struct {
	states   []skill.InvocationState
	selector overlayListSelectorState
	prevMode Mode
	filter   string

	// searchCorpus holds one lowercase "name description" haystack per
	// states entry, built once when the selector opens. Filtering then
	// matches without re-lowercasing every candidate on each keystroke
	// (see buildSessionSearchCorpus for the session picker's equivalent).
	searchCorpus []string

	filterFocused bool
}

func skillSelectIdleHint() string {
	return hintLine(
		hint("j/k", "move"), hint("g/G", "jump"), hint("Enter", "fill command"),
		hint("/", "filter"), hint("Esc", "close"),
	)
}

// Skill state icons. Shape carries the state class and color carries the
// accent, so a skill row never needs a text suffix to say what it means:
//
//	○  idle — model-visible, nothing loaded, no discovery signal
//	◎  relevant — a project read matched the skill's paths and the model
//	   has (or is about to get) the name-and-description notice
//	●  loaded — the body is live in this agent's context
//	↻  reload — the body was loaded but compaction archived it; load it
//	   again to use it
//	◌  manual-only — the model never sees it; only an explicit /skill load
//	   brings the body in
//	⊘  denied — the role's ruleset refuses the skill
//
// The tool card uses ◌ for "receiving", so UI labels always keep the skill
// name next to the glyph instead of letting it read as progress.
const (
	skillIconIdle     = "○"
	skillIconRelevant = "◎"
	skillIconLoaded   = "●"
	skillIconReload   = "↻"
	skillIconManual   = "◌"
	skillIconDenied   = "⊘"
)

// skillStateIcon maps one invocation state onto its icon and icon color.
// Priority follows the lifecycle: a skill the user cannot load at all is
// marked denied, a manual-only skill never reaches the model, and the rest
// is ordered by how close the body is to being live. The "matched, waiting
// for the notice" and "notice recorded" states collapse into one icon: the
// notice publishes when the tool round closes, so telling them apart never
// changed what the user could do.
func skillStateIcon(st skill.InvocationState) (string, lipgloss.Style) {
	switch {
	case !st.UserLoadable:
		return skillIconDenied, currentStyles.InfoPanelDim
	case !st.ModelCallable:
		if st.BodyPresent {
			return skillIconManual, skillStateLoadedStyle()
		}
		return skillIconManual, currentStyles.InfoPanelDim
	case st.BodyPresent:
		return skillIconLoaded, skillStateLoadedStyle()
	case st.LoadedEarlier:
		return skillIconReload, currentStyles.InfoPanelValue.Foreground(lipgloss.Color(currentStyles.Palette.InfoPanelWarningFg))
	case st.NoticeRecorded, st.PathActivated:
		return skillIconRelevant, currentStyles.InfoPanelDim
	default:
		return skillIconIdle, currentStyles.InfoPanelDim
	}
}

// skillStateLoadedStyle is the accent for a body that is live: the only
// state the panel paints in the success color.
func skillStateLoadedStyle() lipgloss.Style {
	return currentStyles.InfoPanelValue.Foreground(lipgloss.Color(currentStyles.Palette.InfoPanelSuccessFg))
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

func skillSelectMatches(haystack string, tokens []string) bool {
	if len(tokens) == 0 {
		return true
	}
	for _, token := range tokens {
		if !strings.Contains(haystack, token) {
			return false
		}
	}
	return true
}

// buildSkillSelectSearchCorpus precomputes the lowercase haystack per states
// entry so filtering never re-lowercases the catalog. Entries without metadata
// get an empty haystack and never match a non-empty filter.
func buildSkillSelectSearchCorpus(states []skill.InvocationState) []string {
	corpus := make([]string, 0, len(states))
	for _, st := range states {
		if st.Meta == nil {
			corpus = append(corpus, "")
			continue
		}
		corpus = append(corpus, strings.ToLower(strings.TrimSpace(st.Meta.Name)+" "+strings.TrimSpace(st.Meta.Description)))
	}
	return corpus
}

// sortSkillSelectStates orders states into display order once: manual-only
// skills first, each group by name. Filtering preserves relative order, so the
// per-keystroke rebuild never re-sorts; the grouping below only partitions.
func sortSkillSelectStates(states []skill.InvocationState) {
	sort.SliceStable(states, func(i, j int) bool {
		mi, mj := states[i].Meta, states[j].Meta
		if mi == nil || mj == nil {
			return mj != nil
		}
		if mi.DisableModelInvocation != mj.DisableModelInvocation {
			return mi.DisableModelInvocation
		}
		return mi.Name < mj.Name
	})
}

func skillSelectItemFor(st skill.InvocationState) overlaylist.Item {
	meta := st.Meta
	name := strings.TrimSpace(meta.Name)
	// The icon shape follows the full invocation state rather than the
	// frontmatter alone, so a ruleset-denied skill does not render as
	// model-visible and no row carries a status suffix.
	glyph, _ := skillStateIcon(st)
	label := glyph + " " + name
	if desc := strings.TrimSpace(meta.Description); desc != "" {
		label += "  " + desc
	}
	item := overlaylist.Item{ID: name, Label: label}
	if !st.UserLoadable {
		// The reason replaces the description: a row the user cannot load is
		// explained by why, not by what the skill does.
		item.Disabled = true
		item.Label = glyph + " " + name + " — " + skillReasonText(st.Reason)
		return item
	}
	return item
}

// skillSelectItems groups the catalog into MANUAL and AVAILABLE sections.
// States must arrive in display order (see sortSkillSelectStates): filtering
// preserves it, so this path never sorts. Group headers carry no ID, so the
// list's cursor walk skips them and the current-selection helpers never read
// one as a skill.
func skillSelectItems(states []skill.InvocationState, corpus []string, filter string) []overlaylist.Item {
	if len(corpus) != len(states) {
		corpus = buildSkillSelectSearchCorpus(states)
	}
	tokens := strings.Fields(strings.ToLower(filter))
	manual := make([]skill.InvocationState, 0, len(states))
	model := make([]skill.InvocationState, 0, len(states))
	for i, st := range states {
		if st.Meta == nil || strings.TrimSpace(st.Meta.Name) == "" {
			continue
		}
		if !skillSelectMatches(corpus[i], tokens) {
			continue
		}
		if st.Meta.DisableModelInvocation {
			manual = append(manual, st)
		} else {
			model = append(model, st)
		}
	}
	items := make([]overlaylist.Item, 0, len(states)+2)
	appendGroup := func(header string, group []skill.InvocationState) {
		if len(group) == 0 {
			return
		}
		items = append(items, overlaylist.Item{Header: true, Label: header})
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
	sortSkillSelectStates(states)
	m.skillSelect = skillSelectState{prevMode: m.mode, states: states, searchCorpus: buildSkillSelectSearchCorpus(states)}
	m.skillSelect.selector.list = overlaylist.New(skillSelectItems(states, m.skillSelect.searchCorpus, ""), m.skillSelectMaxVisible())
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
	items := skillSelectItems(m.skillSelect.states, m.skillSelect.searchCorpus, m.skillSelect.filter)
	if m.skillSelect.selector.list == nil {
		m.skillSelect.selector.list = overlaylist.New(items, m.skillSelectMaxVisible())
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
	if !ok {
		return nil
	}
	return m.applySkillSelection(item)
}

func (m *Model) applySkillSelection(item overlaylist.Item) tea.Cmd {
	if item.Header {
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
	return renderFilterLine(m.skillSelect.filter, m.skillSelect.filterFocused, "", innerWidth)
}

func (m *Model) renderSkillSelectDialog() string {
	if m.skillSelect.selector.list == nil {
		return ""
	}

	hints := skillSelectIdleHint()
	if m.skillSelect.filterFocused {
		hints = selectorFilterHint()
	}
	overlayCfg := OverlayConfig{
		Title: "Skills",
		Hint:  hints,

		CompactHint: hintLine(hint("Enter", "fill command"), hint("Esc", "close")),
		MaxWidth:    70,
	}
	if m.skillSelect.filterFocused {
		overlayCfg.CompactHint = hintLine(hint("Enter", "keep"), hint("Esc", "clear"))
	}
	if _, ok := m.skillSelect.selector.list.SelectedItem(); !ok {
		overlayCfg.Hint = hintLine(hint("/", "filter"), hint("Esc", "close"))
		if m.skillSelect.filterFocused {
			overlayCfg.Hint = hintLine(hint("type", "filter"), hint("Enter", "keep"), hint("Esc", "clear"))
		}
		overlayCfg.CompactHint = overlayCfg.Hint
	}
	area := image.Rect(0, 0, m.width, m.height)
	overlayCfg = normalizeOverlayConfig(overlayCfg, area)
	contentWidth := max(dialogContentWidth(overlayCfg.MaxWidth), 1)
	filterLine := m.renderSkillSelectFilterLine(contentWidth)

	return m.skillSelect.selector.Render(
		m,
		overlayCfg,
		filterLine,
		0,
		m.skillSelectMaxVisible(),
		filterLine+"\x00"+hints,
		nil,
		area,
	)
}

func (m *Model) skillSelectOptionIndexAt(x, y int) (int, bool) {
	dialog := m.renderSkillSelectDialog()
	return m.skillSelect.selector.IndexAt(m, dialog, x, y)
}
