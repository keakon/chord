package llm

import (
	"regexp"
	"strings"
)

// GPT reasoning summaries are a sequence of generated section headings
// ("**Title**"), each with its own body. The Responses wire normally streams one
// section per reasoning summary part, but a backend may flatten a whole summary
// into a single part and concatenate the headings without any separator
// ("**First****Second**"). CommonMark then renders every heading as one run of
// text, so the sections stop being readable as sections.
//
// The pattern anchors a paragraph break in front of a heading that is glued to
// the text before it, covering the three shapes these backends emit: directly
// after the previous token, behind a single newline (which CommonMark treats as
// an inline soft break), and directly after the previous heading's closing "**".
// The rune after the opening "**" must look like a heading start (uppercase,
// digit, or CJK). The bold run must also span the rest of the line — the
// closing "**" is followed by a newline, the end of text, or the next glued
// heading — so an inline span such as "这是**重要**内容" or "**API**Returns"
// keeps its place. CJK headings are one unbroken run without spaces.
var reasoningSummaryHeadingBreakRE = regexp.MustCompile(
	`(?:([\p{L}\p{N}.!?)}\]'"。．！？）』」])(\*\*)|([^\n])\n(\*\*)|(\*\*)(\*\*))((?:[\p{Lu}0-9][A-Za-z0-9 .,'’:-]{2,}|[\x{4e00}-\x{9fff}\x{3040}-\x{30ff}\x{ac00}-\x{d7af}]+)\*\*(?:\n|$|\*\*))`,
)

// normalizeReasoningSummaryHeadings gives every generated section heading its own
// paragraph, so a summary that arrives with its sections glued together keeps the
// boundaries the backend dropped.
//
// Only this finalized text is normalized. The deltas that stream ahead of it are
// forwarded to the UI verbatim, so the live card still shows the glued headings
// until the stored block replaces them: a break cannot be inserted
// incrementally, because a backend may split a heading across deltas and the
// break would then belong behind text that was already sent.
func normalizeReasoningSummaryHeadings(text string) string {
	if text == "" || !strings.Contains(text, "**") {
		return text
	}
	// ReplaceAll stops at the end of each match, so "**A****B****C**" splits
	// only the first join. Repeat until a pass adds no break.
	for {
		next := reasoningSummaryHeadingBreakRE.ReplaceAllString(text, "${1}${3}${5}\n\n${2}${4}${6}${7}")
		if next == text {
			return text
		}
		text = next
	}
}
