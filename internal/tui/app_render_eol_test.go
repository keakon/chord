package tui

import (
	"strings"
	"testing"
)

func TestEraseToEOLPerLine(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ansiEraseToEOL},
		{name: "single_line", in: "abc", want: "abc" + ansiEraseToEOL},
		{name: "two_lines", in: "a\nb", want: "a" + ansiEraseToEOL + "\n" + "b" + ansiEraseToEOL},
		{name: "trailing_newline", in: "a\n", want: "a" + ansiEraseToEOL + "\n"},
		{name: "only_newline", in: "\n", want: ansiEraseToEOL + "\n"},
		{name: "double_newline", in: "a\n\n", want: "a" + ansiEraseToEOL + "\n" + ansiEraseToEOL + "\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := eraseToEOLPerLine(tc.in)
			if got != tc.want {
				t.Fatalf("eraseToEOLPerLine(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestModelViewAddsEraseToEOLWhenFocusResizeFreezeEnabled(t *testing.T) {
	ApplyTheme(DefaultTheme())
	m := NewModelWithSize(nil, 60, 12)
	m.useFocusResizeFreeze = true
	v := m.View()
	if !strings.Contains(v.Content, ansiEraseToEOL) {
		t.Fatalf("expected View() output to contain %q when focus-resize freeze is enabled", ansiEraseToEOL)
	}

	m2 := NewModelWithSize(nil, 60, 12)
	m2.useFocusResizeFreeze = false
	v2 := m2.View()
	if strings.Contains(v2.Content, ansiEraseToEOL) {
		t.Fatalf("expected View() output to not contain %q when focus-resize freeze is disabled", ansiEraseToEOL)
	}
}
