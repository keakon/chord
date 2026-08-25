package tui

import (
	"slices"
	"testing"

	"charm.land/lipgloss/v2"
)

// TestSplitStyleRenderParity verifies that appendStyledWrappedBody's per-line
// reuse of a precomputed SGR prefix/suffix produces byte-identical output to
// the per-line style.Render calls it replaces.
func TestSplitStyleRenderParity(t *testing.T) {
	ApplyTheme(DefaultTheme())
	styles := []struct {
		name  string
		style lipgloss.Style
	}{
		{"expanded", ToolResultExpandedStyle},
		{"error", ErrorStyle},
		{"dim", DimStyle},
	}
	indents := []string{"", "  ", "    "}
	// Wrapped plain-text lines are the only input the helper receives: no tabs,
	// no newlines, no pre-existing ANSI.
	samples := []string{
		"",
		"single",
		"with spaces and  trailing ",
		"width-exactly-100-columns-of-text-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	for _, st := range styles {
		for _, indent := range indents {
			for _, s := range samples {
				want := st.style.Render(indent + s)
				prefix, suffix := splitStyleRender(st.style)
				got := prefix + indent + s + suffix
				if got != want {
					t.Fatalf("%s indent=%q sample=%q:\n  got  %q\n  want %q", st.name, indent, s, got, want)
				}
			}
		}
	}
	// Multi-line bodies are wrapped first, so the helper only ever receives
	// single-line strings; verify the real pipeline (wrap output) line by line.
	for _, st := range styles {
		prefix, suffix := splitStyleRender(st.style)
		body := "first paragraph line with some words\n\nsecond paragraph with more words to wrap across the width boundary\nthird\n"
		for _, indent := range indents {
			for _, line := range toolExpandedTextLines(body, 80) {
				want := st.style.Render(indent + line)
				got := prefix + indent + line + suffix
				if got != want {
					t.Fatalf("%s indent=%q wrapped line %q:\n  got  %q\n  want %q", st.name, indent, line, got, want)
				}
			}
		}
	}

	for _, st := range styles {
		body := "first line\n\tindented second line\nthird line"
		want := make([]string, 0)
		for _, line := range toolExpandedTextLines(body, 80) {
			want = append(want, st.style.Render("    "+line))
		}
		got := make([]string, 0)
		appendStyledWrappedBody(&got, st.style, "    ", body, 80)
		if !slices.Equal(got, want) {
			t.Fatalf("%s tab-containing body differs:\n  got  %q\n  want %q", st.name, got, want)
		}
	}
}
