package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func TestRenderHintChipStylesKeyAndDimsAction(t *testing.T) {
	ApplyTheme(DefaultTheme())
	chip := renderHintChip(hint("Enter", "submit"))
	if got, want := ansi.Strip(chip), "[Enter] submit"; got != want {
		t.Fatalf("chip plain text = %q, want %q", got, want)
	}
	if !strings.Contains(chip, KeyHintStyle.Render("[Enter]")) {
		t.Fatalf("key token should use KeyHintStyle: %q", chip)
	}
	if !strings.Contains(chip, DimStyle.Render("submit")) {
		t.Fatalf("action should be dim: %q", chip)
	}
}

func TestRenderHintChipProseHasNoKeyToken(t *testing.T) {
	ApplyTheme(DefaultTheme())
	chip := renderHintChip(hintText("(auto servers are read-only)"))
	if chip != DimStyle.Render("(auto servers are read-only)") {
		t.Fatalf("prose chip = %q, want dim text", chip)
	}
}

func TestHintLineJoinsChipsAndAppendsCounter(t *testing.T) {
	ApplyTheme(DefaultTheme())
	line := hintLine(hint("j/k", "scroll"), hint("Esc", "close"))
	if got, want := ansi.Strip(line), "[j/k] scroll  [Esc] close"; got != want {
		t.Fatalf("hintLine = %q, want %q", got, want)
	}
	if got, want := ansi.Strip(appendHintText(line, "12/40")), "[j/k] scroll  [Esc] close  12/40"; got != want {
		t.Fatalf("appendHintText = %q, want %q", got, want)
	}
	if got := appendHintText(line, ""); got != line {
		t.Fatalf("empty counter changed the hint: %q", got)
	}
	if got, want := ansi.Strip(appendHintChip("", hint("a", "apply"))), "[a] apply"; got != want {
		t.Fatalf("appendHintChip on empty hint = %q, want %q", got, want)
	}
}

func TestWrapHintLinesKeepsChipsWhole(t *testing.T) {
	ApplyTheme(DefaultTheme())
	line := hintLine(hint("j/k", "move"), hint("Enter", "select"), hint("Esc", "close"))
	lines := wrapHintLines(line, 20)
	if len(lines) < 2 {
		t.Fatalf("expected the chips to wrap, got %q", lines)
	}
	plain := ansi.Strip(strings.Join(lines, "\n"))
	for _, want := range []string{"[j/k] move", "[Enter] select", "[Esc] close"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("wrapped hint lost chip %q:\n%s", want, plain)
		}
	}
	for _, wrapped := range lines {
		if width := ansi.StringWidth(wrapped); width > 20 {
			t.Fatalf("wrapped line width = %d > 20: %q", width, ansi.Strip(wrapped))
		}
	}
}

func TestWrapHintLinesTruncatesOversizedChip(t *testing.T) {
	ApplyTheme(DefaultTheme())
	lines := wrapHintLines(hintLine(hint("Shift+Enter/Ctrl+J", "new line")), 6)
	if len(lines) != 1 {
		t.Fatalf("lines = %q, want a single truncated chip", lines)
	}
	plain := ansi.Strip(lines[0])
	if width := ansi.StringWidth(plain); width > 6 {
		t.Fatalf("truncated width = %d, want <= 6", width)
	}
	if !strings.HasSuffix(plain, "…") {
		t.Fatalf("truncated chip = %q, want a trailing ellipsis", plain)
	}
}

func TestWrapHintLinesEmptyInput(t *testing.T) {
	if lines := wrapHintLines("", 40); lines != nil {
		t.Fatalf("empty hint = %q, want nil", lines)
	}
	if lines := wrapHintLines("chip", 0); lines != nil {
		t.Fatalf("zero width = %q, want nil", lines)
	}
}

func TestRenderTabRowHighlightsActiveTab(t *testing.T) {
	ApplyTheme(DefaultTheme())
	row := renderTabRow([]string{"[Pending]", "[History]"}, 1)
	want := TabStyle.Render("[Pending]") + " " + TabActiveStyle.Render("[History]")
	if row != want {
		t.Fatalf("tab row = %q, want %q", row, want)
	}
}

func TestRenderFilterLineStates(t *testing.T) {
	ApplyTheme(DefaultTheme())
	idle := ansi.Strip(renderFilterLine("", false, "", 40))
	if !strings.Contains(idle, "filter: ") || !strings.Contains(idle, filterPlaceholder) {
		t.Fatalf("idle filter line = %q, want label and placeholder", idle)
	}
	if got, want := ansi.Strip(renderFilterLine("beta", true, "", 40)), "filter: beta_"; got != want {
		t.Fatalf("focused filter line = %q, want %q", got, want)
	}
	counted := renderFilterLine("beta", false, "2/7", 40)
	if plain := ansi.Strip(counted); !strings.Contains(plain, "filter: beta") || !strings.HasSuffix(plain, "2/7") {
		t.Fatalf("counted filter line = %q, want query and right-aligned counter", plain)
	}
	if width := ansi.StringWidth(counted); width != 40 {
		t.Fatalf("counted filter line width = %d, want 40", width)
	}
}

func TestRenderFilterLineIdleCounterFitsNarrowWidth(t *testing.T) {
	ApplyTheme(DefaultTheme())
	line := renderFilterLine("", false, "1/2", 25)
	if width := ansi.StringWidth(line); width > 25 {
		t.Fatalf("idle filter width = %d, want <= 25: %q", width, ansi.Strip(line))
	}
}

func TestRenderFilterLineKeepsLongQueryTail(t *testing.T) {
	ApplyTheme(DefaultTheme())
	line := ansi.Strip(renderFilterLine("abcdefghijklmnopqrstuvwxyz", false, "", 20))
	if !strings.HasSuffix(line, "vwxyz") {
		t.Fatalf("long query = %q, want the tail visible next to the caret", line)
	}
	if width := ansi.StringWidth(line); width > 20 {
		t.Fatalf("long query width = %d, want <= 20", width)
	}
}

func TestMemoryPanelTabRowShowsViews(t *testing.T) {
	m, _ := memoryPanelTestModel()
	wide := ansi.Strip(m.renderMemoryPanel())
	if !strings.Contains(wide, ansi.Strip(renderTabRow([]string{"Project memories", "Session applied", "Suggestions"}, 0))) {
		t.Fatalf("memory panel should render the shared tab row:\n%s", wide)
	}
	if !strings.Contains(wide, "Report style") {
		t.Fatalf("first view should list project memories:\n%s", wide)
	}
	m.handleMemoryPanelKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	if m.memoryPanel.tab != 1 {
		t.Fatalf("tab key did not switch the view: tab=%d", m.memoryPanel.tab)
	}
	// The narrow layout collapses the strip to the active view name.
	m.width = 50
	narrow := ansi.Strip(m.renderMemoryPanel())
	if !strings.Contains(narrow, "Session applied") || strings.Contains(narrow, "Project memories") {
		t.Fatalf("narrow layout should label only the active view:\n%s", narrow)
	}
	if !strings.Contains(narrow, "[Tab] views") {
		t.Fatalf("narrow layout should advertise the view tab as a key chip:\n%s", narrow)
	}
}

func TestRulePickerRendersSharedMarkers(t *testing.T) {
	ApplyTheme(DefaultTheme())
	m := NewModelWithSize(nil, 100, 30)
	m.confirm.request = &ConfirmRequest{ToolName: tools.NameEdit, ArgsJSON: `{"path":"sample.go","patch":"@@\n-old\n+new\n"}`}
	m.confirm.pickingRule = true
	m.confirm.candidates = []PatternCandidate{{Pattern: "edit:sample.go"}, {Pattern: "edit:*.go"}}
	m.confirm.patternIdx = 1
	m.confirm.selectedPatterns = map[int]struct{}{1: {}}
	m.confirm.scopes = []permission.RuleScope{permission.ScopeSession, permission.ScopeProject}
	m.confirm.scopeIdx = 0

	plain := stripANSI(m.renderConfirmDialog())
	if !strings.Contains(plain, "[x] edit:*.go") {
		t.Fatalf("selected pattern should use the shared check mark:\n%s", plain)
	}
	if !strings.Contains(plain, "❯") {
		t.Fatalf("pattern cursor should use the shared marker:\n%s", plain)
	}
	if !strings.Contains(plain, "Scope: session") {
		t.Fatalf("current scope should be independent of pattern focus:\n%s", plain)
	}
	m.handleConfirmRulePickerKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	if !strings.Contains(stripANSI(m.renderConfirmDialog()), "Scope: project") {
		t.Fatal("Tab did not update the visible scope")
	}
	if !strings.Contains(plain, "[↑↓] pattern") || !strings.Contains(plain, "[Enter] remember + allow") {
		t.Fatalf("rule picker should render key chips:\n%s", plain)
	}
}
