package tui

import (
	"strconv"
	"testing"
)

// Message-card background slots participating in the "no collision" contract.
// (Focus is indicated by the rail, so only resting surfaces matter.)
type cardSlot struct {
	name string
	bg   string
}

func cardSurfaces(theme Theme) []cardSlot {
	return []cardSlot{
		{"UserCardBg", theme.UserCardBg},
		{"AssistantCardBg", theme.AssistantCardBg},
		{"ToolCallBg", theme.ToolCallBg},
		{"ThinkingCardBg", theme.ThinkingCardBg},
	}
}

func assertUnique(t *testing.T, label string, slots []cardSlot) {
	t.Helper()
	seen := make(map[string]string, len(slots))
	for _, s := range slots {
		if s.bg == "" {
			t.Errorf("%s: %s is empty", label, s.name)
			continue
		}
		if other, ok := seen[s.bg]; ok {
			t.Errorf("%s collision: %s and %s both = %q", label, other, s.name, s.bg)
		}
		seen[s.bg] = s.name
	}
}

func TestCardSurfacesDistinct(t *testing.T) {
	theme := DefaultTheme()
	assertUnique(t, theme.Name+" surfaces", cardSurfaces(theme))
}

// TestDarkThemeUserSurfaceNotPureBlack guards the specific regression that
// motivated this test file: UserCardBg = "232" (#080808) is indistinguishable
// from iTerm2's default dark profile background (#000000), and the USER card
// "disappears" into the terminal. Anything from 233 onward is acceptable.
func TestDarkThemeUserSurfaceNotPureBlack(t *testing.T) {
	if got := DefaultTheme().UserCardBg; got == "232" {
		t.Fatalf("DefaultTheme UserCardBg = %q: ANSI 232 (#080808) is indistinguishable from pure-black terminal backgrounds; use 233 or lighter", got)
	}
}

func TestCodeSurfacesDistinctFromCardSurfaces(t *testing.T) {
	theme := DefaultTheme()
	surfaces := cardSurfaces(theme)
	for _, s := range surfaces {
		if theme.InlineCodeBg == s.bg {
			t.Errorf("InlineCodeBg (%q) collides with %s (%q); inline code may become visually indistinct", theme.InlineCodeBg, s.name, s.bg)
		}
		if theme.CodeBlockBg == s.bg {
			t.Errorf("CodeBlockBg (%q) collides with %s (%q); fenced code blocks may become visually indistinct", theme.CodeBlockBg, s.name, s.bg)
		}
	}
}

// TestRailColorsAllPresent verifies that all rail color slots are populated
// in the default dark theme.
func TestRailColorsAllPresent(t *testing.T) {
	theme := DefaultTheme()
	type slot struct {
		name  string
		color string
	}
	slots := []slot{
		{"RailUserFg", theme.RailUserFg},
		{"RailAssistantFg", theme.RailAssistantFg},
		{"RailToolFg", theme.RailToolFg},
		{"RailThinkingFg", theme.RailThinkingFg},
		{"RailErrorFg", theme.RailErrorFg},
		{"RailUserFocusedFg", theme.RailUserFocusedFg},
		{"RailAssistantFocusedFg", theme.RailAssistantFocusedFg},
		{"RailToolFocusedFg", theme.RailToolFocusedFg},
		{"RailThinkingFocusedFg", theme.RailThinkingFocusedFg},
		{"RailErrorFocusedFg", theme.RailErrorFocusedFg},
	}
	for _, s := range slots {
		if s.color == "" {
			t.Errorf("%s is empty; rail colors must be set", s.name)
		}
	}
}

// TestRailColorsDistinctFromEachOther ensures rail colors are distinguishable.
func TestRailColorsDistinctFromEachOther(t *testing.T) {
	theme := DefaultTheme()
	colors := map[string]string{
		"user":            theme.RailUserFg,
		"assistant":       theme.RailAssistantFg,
		"tool":            theme.RailToolFg,
		"thinking":        theme.RailThinkingFg,
		"error":           theme.RailErrorFg,
		"user-focus":      theme.RailUserFocusedFg,
		"assistant-focus": theme.RailAssistantFocusedFg,
		"tool-focus":      theme.RailToolFocusedFg,
		"thinking-focus":  theme.RailThinkingFocusedFg,
		"error-focus":     theme.RailErrorFocusedFg,
	}
	seen := make(map[string]string)
	for kind, c := range colors {
		if prev, ok := seen[c]; ok {
			t.Errorf("rail color %q used for both %q and %q", c, prev, kind)
		}
		seen[c] = kind
	}
}

func TestFocusedRailColorsDifferFromBase(t *testing.T) {
	theme := DefaultTheme()
	pairs := []struct {
		kind        string
		base, focus string
	}{
		{"user", theme.RailUserFg, theme.RailUserFocusedFg},
		{"assistant", theme.RailAssistantFg, theme.RailAssistantFocusedFg},
		{"tool", theme.RailToolFg, theme.RailToolFocusedFg},
		{"thinking", theme.RailThinkingFg, theme.RailThinkingFocusedFg},
		{"error", theme.RailErrorFg, theme.RailErrorFocusedFg},
	}
	for _, p := range pairs {
		if p.base == p.focus {
			t.Errorf("%s rail focus color should differ from base: %q", p.kind, p.base)
		}
		if _, err := strconv.Atoi(p.base); err != nil {
			t.Errorf("%s base rail color should be numeric ANSI index, got %q", p.kind, p.base)
		}
		if _, err := strconv.Atoi(p.focus); err != nil {
			t.Errorf("%s focused rail color should be numeric ANSI index, got %q", p.kind, p.focus)
		}
	}
}
