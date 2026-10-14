package terminaltext

import "testing"

func TestCountIndentPreservesBorderAndResetRules(t *testing.T) {
	for _, tc := range []struct {
		text string
		want int
	}{
		{"", 0},
		{" \ttext", 2},
		{"│  text", 2},
		{"┃\x1b[0m\t text", 2},
		{"|▏┃\x1b[m   text", 3},
		{"\x1b[31m  text", 0},
		{"│text  ", 0},
	} {
		if got := CountIndent(tc.text); got != tc.want {
			t.Errorf("CountIndent(%q) = %d, want %d", tc.text, got, tc.want)
		}
	}
}
