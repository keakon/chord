package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// backgroundOnTrailingSpaces reports the active 256-color background index while
// emitting any trailing pad spaces (spaces after the final non-space printable
// character). It returns "" when the background is in the default state.
func backgroundOnTrailingSpaces(line string) (bg string, ok bool) {
	plain := stripANSI(line)
	contentEnd := len(strings.TrimRight(plain, " \t"))
	if contentEnd == 0 {
		return "", false
	}
	bg = ""
	plainCol := 0
	for i := 0; i < len(line); {
		if line[i] == 0x1b {
			// Parse CSI SGR sequences to track background.
			if i+1 < len(line) && line[i+1] == '[' {
				j := i + 2
				for j < len(line) && line[j] != 'm' {
					j++
				}
				if j < len(line) && line[j] == 'm' {
					bg = applySGRCodesForBackground(line[i+2:j], bg)
					i = j + 1
					continue
				}
			}
			// Fallback: skip the whole ANSI sequence.
			i = skipANSISequence(line, i)
			continue
		}
		if line[i] == ' ' && plainCol >= contentEnd {
			return bg, true
		}
		// This test helper is only used with ASCII inputs.
		plainCol += ansi.StringWidth(line[i : i+1])
		i++
	}
	return "", false
}

func applySGRCodesForBackground(codes, bg string) string {
	if codes == "" {
		return ""
	}
	parts := strings.Split(codes, ";")
	for idx := 0; idx < len(parts); idx++ {
		c := parts[idx]
		switch c {
		case "", "0":
			bg = ""
			continue
		case "49":
			bg = ""
			continue
		case "48":
			// Extended background colour: 48;5;N or 48;2;r;g;b
			if idx+1 >= len(parts) {
				continue
			}
			mode := parts[idx+1]
			switch mode {
			case "5":
				if idx+2 < len(parts) {
					bg = parts[idx+2]
					idx += 2
				}
			case "2":
				if idx+4 < len(parts) {
					bg = "rgb"
					idx += 4
				}
			default:
				// Unknown mode; ignore.
			}
			continue
		}
	}
	return bg
}

func TestTruncateLineToDisplayWidthClosesInlineBGAndRestoresBaseBGForPadding(t *testing.T) {
	cardBgNum := "235"
	cardBgSeq := "\x1b[48;5;" + cardBgNum + "m"
	inlineBg := "\x1b[48;5;240m"

	// Base card background is set first. Inline background begins later and is
	// intentionally left unclosed so truncation happens mid-span.
	input := cardBgSeq + "prefix " + inlineBg + strings.Repeat("X", 20)

	truncated := truncateLineToDisplayWidth(input, 12)
	padded := padLineToDisplayWidth(truncated, 20)

	bg, ok := backgroundOnTrailingSpaces(padded)
	if !ok {
		t.Fatalf("expected trailing pad spaces, got none: %q", padded)
	}
	if bg != cardBgNum {
		t.Fatalf("padding background = %q, want base card bg %q; line=%q", bg, cardBgNum, padded)
	}
}

func TestTruncateLineToDisplayWidthLeavesPlainTextUnchanged(t *testing.T) {
	input := strings.Repeat("A", 20)
	got := truncateLineToDisplayWidth(input, 5)
	if got != input[:5] {
		t.Fatalf("truncateLineToDisplayWidth(plain)=%q, want %q", got, input[:5])
	}
}
