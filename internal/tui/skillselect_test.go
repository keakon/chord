package tui

import (
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/skill"
)

func newSkillSelectTestModel(t *testing.T) (Model, *infoPanelAgent) {
	t.Helper()
	backend := newInfoPanelAgent()
	backend.availableSkills = []*skill.Meta{
		{Name: "go-expert", Description: "Go language development expert", Discovered: true},
		{Name: "py-expert", Description: "Python development expert", Discovered: true},
		{Name: "manual-only", Description: "Manual workflow", Discovered: true, DisableModelInvocation: true},
	}
	return NewModelWithSize(backend, 100, 30), backend
}

func skillSelectRowIDs(list *OverlayList) []string {
	ids := make([]string, 0, list.Len())
	for _, item := range list.items {
		if item.Header {
			ids = append(ids, "#"+item.Label)
			continue
		}
		ids = append(ids, item.ID)
	}
	return ids
}

func TestOpenSkillSelectGroupsManualBeforeAvailable(t *testing.T) {
	m, _ := newSkillSelectTestModel(t)
	m.mode = ModeNormal

	m.openSkillSelect()

	if m.mode != ModeSkillSelect {
		t.Fatalf("mode after open = %v, want ModeSkillSelect", m.mode)
	}
	list := m.skillSelect.selector.list
	if list == nil {
		t.Fatal("skill overlay list missing")
	}
	got := skillSelectRowIDs(list)
	want := []string{"#MANUAL", "manual-only", "#AVAILABLE", "go-expert", "py-expert"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	// The cursor must start on a skill, never on a group header.
	selected, ok := list.SelectedItem()
	if !ok || selected.Header || selected.ID != "manual-only" {
		t.Fatalf("initial selection = %#v (ok=%v), want manual-only", selected, ok)
	}
}

func TestOpenSkillSelectWithoutSkillsToasts(t *testing.T) {
	backend := newInfoPanelAgent()
	m := NewModelWithSize(backend, 100, 30)
	m.mode = ModeNormal

	m.openSkillSelect()

	if m.mode != ModeNormal {
		t.Fatalf("mode = %v, want unchanged ModeNormal", m.mode)
	}
	if m.activeToast == nil {
		t.Fatal("empty catalog should toast instead of opening an empty overlay")
	}
}

func TestSkillSelectEnterBackfillsComposer(t *testing.T) {
	m, _ := newSkillSelectTestModel(t)
	m.mode = ModeInsert

	m.openSkillSelect()
	m.handleSkillSelectKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	if m.mode != ModeInsert {
		t.Fatalf("mode after enter = %v, want ModeInsert", m.mode)
	}
	if got := m.input.Value(); got != "/skill manual-only " {
		t.Fatalf("composer value = %q, want %q", got, "/skill manual-only ")
	}
	if m.skillSelect.selector.list != nil {
		t.Fatal("applying a skill should clear the selector state")
	}
}

func TestSkillSelectEscapeClosesOverlay(t *testing.T) {
	m, _ := newSkillSelectTestModel(t)
	m.mode = ModeNormal

	m.openSkillSelect()
	m.handleSkillSelectKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))

	if m.mode != ModeNormal {
		t.Fatalf("mode after esc = %v, want ModeNormal", m.mode)
	}
	if m.skillSelect.selector.list != nil {
		t.Fatal("esc should clear the selector state")
	}
}

func TestSkillSelectDisabledRowExplainsDenial(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.availableSkills = []*skill.Meta{
		{Name: "open-skill", Discovered: true},
		{Name: "closed-skill", Discovered: true},
	}
	backend.skillRuleset = permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionAllow},
		{Permission: "skill", Pattern: "closed-skill", Action: permission.ActionDeny},
	}
	m := NewModelWithSize(backend, 100, 30)
	m.mode = ModeInsert

	m.openSkillSelect()
	list := m.skillSelect.selector.list
	if list == nil {
		t.Fatal("skill overlay list missing")
	}
	deniedIdx := -1
	for i, item := range list.items {
		if item.ID != "closed-skill" {
			continue
		}
		deniedIdx = i
		if !item.Disabled {
			t.Fatalf("ruleset-denied skill should be disabled: %#v", item)
		}
		if !strings.Contains(stripANSI(item.Label), skillReasonText(skill.ReasonDeniedByRuleset)) {
			t.Fatalf("denied row label = %q, want reason %q", item.Label, skillReasonText(skill.ReasonDeniedByRuleset))
		}
	}
	if deniedIdx < 0 {
		t.Fatal("denied skill missing from the selector catalog")
	}

	// SetCursor skips unselectable rows, so aim the cursor at the denied row
	// directly to exercise the refusal branch.
	list.cursor = deniedIdx
	m.handleSkillSelectKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	if m.mode != ModeSkillSelect {
		t.Fatalf("mode after denied enter = %v, want ModeSkillSelect", m.mode)
	}
	if m.activeToast == nil {
		t.Fatal("a denied row should explain itself with a toast")
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("composer after denied enter = %q, want empty", got)
	}
}

func TestSkillSelectFilterNarrowsRows(t *testing.T) {
	m, _ := newSkillSelectTestModel(t)
	m.mode = ModeInsert

	m.openSkillSelect()
	m.handleSkillSelectKey(tea.KeyPressMsg(tea.Key{Code: '/', Text: "/"}))
	if !m.skillSelect.filterFocused {
		t.Fatal("pressing / should focus the filter")
	}
	for _, r := range "py" {
		m.handleSkillSelectKey(tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)}))
	}

	got := skillSelectRowIDs(m.skillSelect.selector.list)
	want := []string{"#AVAILABLE", "py-expert"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("filtered rows = %v, want %v", got, want)
	}

	// The first enter leaves the filter, the second applies the row.
	m.handleSkillSelectKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if m.skillSelect.filterFocused {
		t.Fatal("enter should leave filter focus")
	}
	m.handleSkillSelectKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if m.mode != ModeInsert {
		t.Fatalf("mode after apply = %v, want ModeInsert", m.mode)
	}
	if got := m.input.Value(); got != "/skill py-expert " {
		t.Fatalf("composer = %q, want %q", got, "/skill py-expert ")
	}
}

func TestSkillSelectEventOpensOverlay(t *testing.T) {
	m, _ := newSkillSelectTestModel(t)
	m.mode = ModeNormal
	m.inflightDraft = &queuedDraft{ID: "draft-1", AgentID: "main"}

	handled, _ := m.handleSessionAgentEvent(agent.SkillSelectEvent{})

	if !handled {
		t.Fatal("SkillSelectEvent was not handled")
	}
	if m.mode != ModeSkillSelect {
		t.Fatalf("mode after event = %v, want ModeSkillSelect", m.mode)
	}
	if m.inflightDraft != nil {
		t.Fatal("opening the selector should clear the inflight draft marker")
	}
}

func TestSkillSelectModalMouseClickBackfillsComposer(t *testing.T) {
	m, _ := newSkillSelectTestModel(t)
	m.mode = ModeInsert

	m.openSkillSelect()
	m.layout = m.generateLayout(m.width, m.height)
	_ = m.renderSkillSelectDialog()
	dialogRect := m.overlayRect(m.renderSkillSelectDialog())
	clickX := dialogRect.Min.X + 2
	// Rows: MANUAL header, manual-only, AVAILABLE header, go-expert, py-expert.
	clickY := dialogRect.Min.Y + 1 + m.skillSelect.selector.listBaseRow + 4

	cmd, handled := m.handleModalMouseMsg(tea.MouseClickMsg{X: clickX, Y: clickY, Button: tea.MouseLeft})
	if !handled {
		t.Fatal("skill select click was not handled")
	}
	_ = cmd
	if m.mode != ModeInsert {
		t.Fatalf("mode after click = %v, want ModeInsert", m.mode)
	}
	if got := m.input.Value(); got != "/skill py-expert " {
		t.Fatalf("composer after click = %q, want %q", got, "/skill py-expert ")
	}
}

func TestSkillSelectMouseWheelMovesCursor(t *testing.T) {
	m, _ := newSkillSelectTestModel(t)
	m.mode = ModeInsert

	m.openSkillSelect()
	m.layout = m.generateLayout(m.width, m.height)

	cmd, handled := m.handleModalMouseMsg(tea.MouseWheelMsg{X: 1, Y: 1, Button: tea.MouseWheelDown})
	if !handled {
		t.Fatal("skill select wheel was not handled")
	}
	if cmd != nil {
		t.Fatalf("wheel returned cmd %#v, want nil", cmd)
	}
	if got := m.skillSelect.selector.list.CursorAt(); got != 4 {
		t.Fatalf("cursor after wheel down = %d, want 4 (py-expert, skipping both headers)", got)
	}
}
