package tools

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// cleanJobOutputText renders job output for the model: terminal escape
// sequences are removed and carriage-return progress lines collapse to their
// final state. Only model-facing reads go through it — the in-memory window,
// the byte counters and the on-disk log keep the raw bytes so cursors, dropped
// accounting and post-mortem diagnosis stay exact.
//
// The cleaning sees one read chunk at a time and keeps no state between reads,
// so an escape sequence or a carriage-return redraw split across two read
// boundaries is not folded away. That is cosmetic (a stray fragment at worst),
// and avoiding it would require either withholding output at the window or a
// stateful scanner in front of the raw-byte invariant above.
func cleanJobOutputText(s string) string {
	if s == "" || !strings.ContainsAny(s, "\x1b\r") {
		return s
	}
	if strings.Contains(s, "\x1b") {
		s = ansi.Strip(s)
	}
	if !strings.Contains(s, "\r") {
		return s
	}
	// Normalize CRLF first: those carriage returns end a line, they do not
	// overwrite one.
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = collapseCarriageReturns(line)
	}
	return strings.Join(lines, "\n")
}

// collapseCarriageReturns keeps the last non-empty segment a line was redrawn
// into. A trailing carriage return (nothing after it) leaves the earlier text
// visible, so an empty segment never wins.
func collapseCarriageReturns(line string) string {
	if !strings.Contains(line, "\r") {
		return line
	}
	parts := strings.Split(line, "\r")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" {
			return parts[i]
		}
	}
	return ""
}
