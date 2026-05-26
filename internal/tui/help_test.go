package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	tea "charm.land/bubbletea/v2"

	"github.com/keakon/chord/internal/buildinfo"
)

func TestKeyMapHelpGroupsRespectConfiguredKeys(t *testing.T) {
	km := KeyMapFromConfig(map[string][]string{
		"help_toggle":  {"f1"},
		"search_start": {"?"},
		"fast_mode":    {"ctrl+r"},
	})

	var foundHelp, foundSearch, foundFast bool
	for _, group := range km.HelpGroups() {
		if group.Title != "Normal Mode" {
			continue
		}
		for _, binding := range group.Bindings {
			switch binding.Help {
			case "open help":
				foundHelp = len(binding.Keys) == 1 && binding.Keys[0] == "f1"
			case "start search":
				foundSearch = len(binding.Keys) == 1 && binding.Keys[0] == "?"
			case "toggle fast responses for all agents":
				foundFast = len(binding.Keys) == 1 && binding.Keys[0] == "ctrl+r"
			}
		}
	}

	if !foundHelp {
		t.Fatal("expected help binding to use configured key")
	}
	if !foundSearch {
		t.Fatal("expected search binding to use configured key")
	}
	if !foundFast {
		t.Fatal("expected fast binding to use configured key")
	}
}

func TestNormalModeHelpListsCountedChordBindings(t *testing.T) {
	m := NewModel(nil)
	lines := m.helpLines(120)
	text := strings.Join(lines, "\n")
	for _, want := range []string{"[count]gg / [count]G", "[count]yy", "dd / [count]dd"} {
		if !strings.Contains(text, want) {
			t.Fatalf("help text should contain %q, got:\n%s", want, text)
		}
	}
}

func TestNormalQuestionMarkOpensHelp(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeNormal

	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "?", Code: '?'}))

	if m.mode != ModeHelp {
		t.Fatalf("mode = %v, want %v", m.mode, ModeHelp)
	}
	if m.help.prevMode != ModeNormal {
		t.Fatalf("help prevMode = %v, want %v", m.help.prevMode, ModeNormal)
	}
}

func TestInsertSlashHelpOpensHelp(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeInsert
	m.input.SetValue("/help")

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	if m.mode != ModeHelp {
		t.Fatalf("mode = %v, want %v", m.mode, ModeHelp)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input value = %q, want empty", got)
	}
	if m.help.prevMode != ModeInsert {
		t.Fatalf("help prevMode = %v, want %v", m.help.prevMode, ModeInsert)
	}
}

func TestSlashCompletionDropdownUsesRenderCache(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.mode = ModeInsert
	m.slashCompleteSelected = 1

	first := m.renderSlashCompletionDropdown("/")
	if first == "" {
		t.Fatal("expected slash completion dropdown")
	}
	if m.slashCache.text != first {
		t.Fatal("expected slash dropdown cache to store rendered text")
	}

	second := m.renderSlashCompletionDropdown("/")
	if second != first {
		t.Fatal("cached slash dropdown render changed unexpectedly")
	}

	m.slashCompleteSelected = 2
	third := m.renderSlashCompletionDropdown("/")
	if third == "" {
		t.Fatal("expected slash completion dropdown after selection change")
	}
	if m.slashCache.sel != 2 {
		t.Fatalf("slashCache.sel = %d, want 2", m.slashCache.sel)
	}
	if third == first {
		t.Fatal("selection change should produce a different cached dropdown render")
	}
}

func TestSlashCompletionDropdownDoesNotWrapModelsCommand(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.mode = ModeInsert
	m.slashCompleteSelected = 0

	drop := m.renderSlashCompletionDropdown("/mo")
	if drop == "" {
		t.Fatal("expected slash completion dropdown")
	}
	plain := ansi.Strip(drop)
	if strings.Contains(plain, "role\npool") || strings.Contains(plain, "role    │\n│ pool") {
		t.Fatalf("/models command wrapped unexpectedly:\n%s", plain)
	}
	if !strings.Contains(plain, "/models  switch current view pool") {
		t.Fatalf("expected /models command on one line, got:\n%s", plain)
	}
}

func TestSlashCompletionDropdownScrollsSelectedCommandIntoView(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.mode = ModeInsert
	m.SetCustomCommands([]CustomCommand{
		{Cmd: "/zz-one", Desc: "custom one"},
		{Cmd: "/zz-two", Desc: "custom two"},
	})
	m.slashCompleteSelected = len(m.getSlashCompletions("/")) - 1

	drop := m.renderSlashCompletionDropdown("/")
	if drop == "" {
		t.Fatal("expected slash completion dropdown")
	}
	plain := ansi.Strip(drop)
	if strings.Contains(plain, "/compact") {
		t.Fatalf("expected dropdown to scroll past first command, got:\n%s", plain)
	}
	if !strings.Contains(plain, "▸ /zz-two") {
		t.Fatalf("expected selected /zz-two command to be visible, got:\n%s", plain)
	}
}

func TestHelpLinesUseColumnsWhenWide(t *testing.T) {
	m := NewModel(nil)

	narrow := m.helpLines(80)
	wide := m.helpLines(120)

	if len(wide) >= len(narrow) {
		t.Fatalf("wide help should use fewer lines than narrow help: wide=%d narrow=%d", len(wide), len(narrow))
	}

	var foundCombinedTitles bool
	for _, line := range wide {
		if strings.Contains(line, "Insert Mode") && strings.Contains(line, "Normal Mode") {
			foundCombinedTitles = true
			break
		}
	}
	if !foundCombinedTitles {
		t.Fatal("expected wide help layout to place multiple groups on the same row")
	}

	for _, line := range narrow {
		if strings.Contains(line, "Insert Mode") && strings.Contains(line, "Normal Mode") {
			t.Fatal("narrow help layout should remain single-column")
		}
	}
}

func TestHelpLinesIncludeCenteredBuildVersion(t *testing.T) {
	m := NewModel(nil)
	lines := m.helpLines(120)
	text := strings.Join(lines, "\n")

	want := "Chord " + buildinfo.Current().Short()
	if !strings.Contains(lines[0], want) {
		t.Fatalf("first help line should contain %q, got %q", want, lines[0])
	}
	if !strings.HasPrefix(lines[0], " ") {
		t.Fatalf("first help line should be centered with leading padding, got %q", lines[0])
	}
	for _, notWant := range []string{"About", "Commit:", "Build time:", "VCS time:", "Platform:"} {
		if strings.Contains(text, notWant) {
			t.Fatalf("help text should not contain %q, got:\n%s", notWant, text)
		}
	}
}

func TestCenterHelpLine(t *testing.T) {
	if got := centerHelpLine("Chord v1", 14); got != "   Chord v1" {
		t.Fatalf("centerHelpLine = %q, want %q", got, "   Chord v1")
	}
	if got := centerHelpLine("Chord v1", 4); got != "Chord v1" {
		t.Fatalf("centerHelpLine should not truncate, got %q", got)
	}
}

func TestHelpViewShowsVersionOnShortFirstScreen(t *testing.T) {
	m := NewModelWithSize(nil, 80, 8)
	m.mode = ModeHelp
	m.help = helpState{prevMode: ModeNormal}

	plain := ansi.Strip(m.renderHelpView())
	for _, want := range []string{"Chord", buildinfo.Current().Short(), "Press Esc, q, or ?"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("short help first screen should contain %q, got:\n%s", want, plain)
		}
	}
}

func TestFindMatchesAtWidthUsesRenderedBlockOffsets(t *testing.T) {
	blocks := []*Block{
		{Type: BlockUser, Content: "first"},
		{Type: BlockAssistant, Content: "reply"},
		{Type: BlockUser, Content: "needle"},
	}

	matches := FindMatchesAtWidth(blocks, "needle", 80)
	if len(matches) != 1 {
		t.Fatalf("matches = %d, want 1", len(matches))
	}

	wantOffset := blocks[0].LineCount(80) + blocks[1].LineCount(80)
	if matches[0].LineOffset != wantOffset {
		t.Fatalf("line offset = %d, want %d", matches[0].LineOffset, wantOffset)
	}
}
