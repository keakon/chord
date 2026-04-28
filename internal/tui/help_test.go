package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestKeyMapHelpGroupsRespectConfiguredKeys(t *testing.T) {
	km := KeyMapFromConfig(map[string][]string{
		"help_toggle":  {"f1"},
		"search_start": {"?"},
	})

	var foundHelp, foundSearch bool
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
			}
		}
	}

	if !foundHelp {
		t.Fatal("expected help binding to use configured key")
	}
	if !foundSearch {
		t.Fatal("expected search binding to use configured key")
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
	if m.renderSlashCacheText != first {
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
	if m.renderSlashCacheSel != 2 {
		t.Fatalf("renderSlashCacheSel = %d, want 2", m.renderSlashCacheSel)
	}
	if third == first {
		t.Fatal("selection change should produce a different cached dropdown render")
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
