package tui

import (
	"encoding/json"
	"strings"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tui/markdownutil"
)

// wrapCompactionTypedStateForDisplay rewrites the typed-state payload bullet
// into a fenced code block for the card's Markdown render. The payload is a
// single-line JSON document; as a list item it is wrapped at arbitrary
// character boundaries with no continuation indent, so machine state reads as
// reflowed prose. The fence keeps the exact payload in one code block and gives
// wrapped continuations the code-block indent.
//
// The rewrite is display-only and deliberately narrow: it needs the heading on
// its own column-0 line followed by a bullet whose value is a complete JSON
// document, so prose that merely quotes the heading is left untouched. The
// stored message keeps the machine payload verbatim for the carry parser.
func wrapCompactionTypedStateForDisplay(body string) string {
	if !strings.Contains(body, message.CompactionTypedStateHeading) {
		return body
	}
	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines)+2)
	var open markdownutil.Fence
	inFence := false
	changed := false
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		out = append(out, line)
		if inFence {
			if markdownutil.IsFenceClose(line, open) {
				inFence = false
			}
			continue
		}
		if fence, ok := markdownutil.ParseFenceLine(line); ok {
			open, inFence = fence, true
			continue
		}
		if line != message.CompactionTypedStateHeading {
			continue
		}
		payload := i + 1
		for payload < len(lines) && strings.TrimSpace(lines[payload]) == "" {
			payload++
		}
		if payload >= len(lines) {
			continue
		}
		value, ok := strings.CutPrefix(lines[payload], "- ")
		value = strings.TrimSpace(value)
		if !ok || !json.Valid([]byte(value)) {
			continue
		}
		out = append(out, lines[i+1:payload]...)
		out = append(out, "```json", value, "```")
		i = payload
		changed = true
	}
	if !changed {
		return body
	}
	return strings.Join(out, "\n")
}
