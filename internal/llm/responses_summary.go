package llm

import (
	"fmt"
	"regexp"
	"strings"
)

// GPT reasoning summaries are a sequence of generated section headings
// ("**Title**"), each with its own body. The Responses wire normally streams one
// section per reasoning summary part, but a backend may flatten a whole summary
// into a single part and concatenate the headings without any separator
// ("**First****Second**"), or publish the headings on the raw reasoning_text
// channel instead of the summary channel. CommonMark then renders every heading
// as one run of text, so the sections stop being readable as sections.
//
// The pattern anchors a paragraph break in front of a heading that is glued to
// the text before it, covering the three shapes these backends emit: directly
// after the previous token, behind a single newline (which CommonMark treats as
// an inline soft break), and directly after the previous heading's closing "**".
// The rune after the opening "**" must look like a heading start (uppercase,
// digit, or CJK). The bold run must also span the rest of the line — the
// closing "**" is followed by a newline, the end of text, or the next glued
// heading — so an inline span such as "这是**重要**内容" or "**API**Returns"
// keeps its place. A Latin heading body takes any rune except a newline or
// another "*": real headings carry underscores, parentheses, slashes and code
// spans, and narrowing the class to plain words drops those breaks entirely.
// CJK headings are one unbroken run without spaces.
const reasoningSummaryHeadingBreakPattern = `(?:([\p{L}\p{N}.!?)}\]'"。．！？）』」])(\*\*)|([^\n])\n(\*\*)|(\*\*)(\*\*))((?:[\p{Lu}0-9][^\n*]{2,}|[\x{4e00}-\x{9fff}\x{3040}-\x{30ff}\x{ac00}-\x{d7af}]+)\*\*(?:\n|\*\*%s))`

// reasoningSummaryHeadingBreakRE accepts the end of text as the heading
// terminator: the text is complete, so a bold run that closes it spans the
// rest of its line.
var reasoningSummaryHeadingBreakRE = regexp.MustCompile(fmt.Sprintf(reasoningSummaryHeadingBreakPattern, "|$"))

// streamingReasoningSummaryHeadingBreakRE only accepts a terminator that is
// already present. While the text is still growing, a bold run that closes it
// may be an inline span whose continuation has not arrived yet ("检查**配置**"
// followed by "文件"), so the end of text proves nothing.
var streamingReasoningSummaryHeadingBreakRE = regexp.MustCompile(fmt.Sprintf(reasoningSummaryHeadingBreakPattern, ""))

// NormalizeReasoningSummaryHeadings gives every generated section heading of a
// complete reasoning text its own paragraph, so text that arrives with its
// sections glued together keeps the boundaries the backend dropped.
//
// It is a display transform: renderers apply it to the text they show, and the
// raw reasoning_text channel is stored exactly as the backend sent it so replay
// forwards the original.
func NormalizeReasoningSummaryHeadings(text string) string {
	return normalizeReasoningSummaryHeadings(text, reasoningSummaryHeadingBreakRE)
}

// NormalizeStreamingReasoningSummaryHeadings is the variant for reasoning text
// that is still streaming. It never breaks in front of a heading whose
// terminator has not arrived, so appending text only inserts breaks inside the
// trailing paragraph: a break is placed right before a heading that has no
// blank line after it, and an earlier paragraph boundary is never taken back.
// Once the text is complete, NormalizeReasoningSummaryHeadings adds the break
// in front of a heading that ends it.
func NormalizeStreamingReasoningSummaryHeadings(text string) string {
	return normalizeReasoningSummaryHeadings(text, streamingReasoningSummaryHeadingBreakRE)
}

func normalizeReasoningSummaryHeadings(text string, re *regexp.Regexp) string {
	if text == "" || !strings.Contains(text, "**") {
		return text
	}
	// ReplaceAll stops at the end of each match, and a match consumes the
	// following heading's opening "**" as its terminator, so "**First****Second****Third**"
	// splits only the first join per pass. Repeat until a pass adds no break.
	// This converges: a replacement keeps every "**" and leaves "\n\n" in
	// front of the heading it broke, which no alternative accepts in front of
	// an opening "**", so each "**" opens at most one break and every pass
	// that changes the text adds at least one.
	for {
		next := re.ReplaceAllString(text, "${1}${3}${5}\n\n${2}${4}${6}${7}")
		if next == text {
			return text
		}
		text = next
	}
}
