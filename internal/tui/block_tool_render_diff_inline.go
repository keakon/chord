package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/keakon/chord/internal/tools"
)

func buildDiffSegmentSpans(segs []tools.InlineSegment) []diffSegmentSpan {
	spans := make([]diffSegmentSpan, 0, len(segs))
	col := 0
	bytePos := 0
	for _, seg := range segs {
		w := ansi.StringWidth(seg.Text)
		span := diffSegmentSpan{Text: seg.Text, Kind: seg.Kind, StartCol: col, EndCol: col + w, StartByte: bytePos, EndByte: bytePos + len(seg.Text)}
		spans = append(spans, span)
		col += w
		bytePos += len(seg.Text)
	}
	return spans
}

func filterDiffSpansByKind(spans []diffSegmentSpan, kind string) []diffSegmentSpan {
	out := make([]diffSegmentSpan, 0, len(spans))
	for _, span := range spans {
		if span.Kind == kind && span.EndCol > span.StartCol {
			out = append(out, span)
		}
	}
	return out
}

func oneSidedSpanFromLines(oldLine, newLine string) (diffOneSidedSpan, bool) {
	oldRunes := []rune(oldLine)
	newRunes := []rune(newLine)
	prefix := 0
	for prefix < len(oldRunes) && prefix < len(newRunes) && oldRunes[prefix] == newRunes[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(oldRunes)-prefix && suffix < len(newRunes)-prefix && oldRunes[len(oldRunes)-1-suffix] == newRunes[len(newRunes)-1-suffix] {
		suffix++
	}
	oldMidLen := len(oldRunes) - prefix - suffix
	newMidLen := len(newRunes) - prefix - suffix
	if oldMidLen > 0 && newMidLen > 0 {
		return diffOneSidedSpan{}, false
	}
	if oldMidLen == 0 && newMidLen == 0 {
		return diffOneSidedSpan{}, false
	}
	lineRunes := newRunes
	change := newRunes[prefix : len(newRunes)-suffix]
	if oldMidLen > 0 {
		lineRunes = oldRunes
		change = oldRunes[prefix : len(oldRunes)-suffix]
	}
	prefixText := string(lineRunes[:prefix])
	suffixText := string(lineRunes[len(lineRunes)-suffix:])
	changeText := string(change)
	return diffOneSidedSpan{Prefix: prefixText, Change: changeText, Suffix: suffixText, StartCol: ansi.StringWidth(prefixText), EndCol: ansi.StringWidth(prefixText) + ansi.StringWidth(changeText), LineWidth: ansi.StringWidth(string(lineRunes))}, true
}

func wordTokenRanges(s string) []diffByteRange {
	var ranges []diffByteRange
	inToken := false
	tokenStart := 0
	for i, r := range s {
		if isWordRune(r) {
			if !inToken {
				inToken = true
				tokenStart = i
			}
			continue
		}
		if inToken {
			ranges = append(ranges, diffByteRange{Start: tokenStart, End: i})
			inToken = false
		}
	}
	if inToken {
		ranges = append(ranges, diffByteRange{Start: tokenStart, End: len(s)})
	}
	return ranges
}

func hasMultiRunWordTokenChange(line string, changeSpans []diffSegmentSpan) bool {
	tokens := wordTokenRanges(line)
	if len(tokens) == 0 || len(changeSpans) <= 1 {
		return false
	}
	for _, tok := range tokens {
		clusters := 0
		lastEnd := -1
		for _, span := range changeSpans {
			if span.EndByte <= tok.Start || span.StartByte >= tok.End {
				continue
			}
			start := max(span.StartByte, tok.Start)
			end := min(span.EndByte, tok.End)
			if end <= start {
				continue
			}
			if clusters == 0 || start > lastEnd {
				clusters++
				if clusters > 1 {
					return true
				}
				lastEnd = end
				continue
			}
			if end > lastEnd {
				lastEnd = end
			}
		}
	}
	return false
}

func buildInlineContentANSI(segs []tools.InlineSegment, changeKind string, changeStyle lipgloss.Style) string {
	var buf strings.Builder
	for _, seg := range segs {
		if seg.Kind == changeKind {
			buf.WriteString(changeStyle.Render(seg.Text))
			continue
		}
		buf.WriteString(seg.Text)
	}
	return buf.String()
}

func renderInlineDiffLine(oldLine, newLine string, diffWidth int) []string {
	oldSegs, newSegs := tools.InlineDiff(oldLine, newLine)
	oldSpans := buildDiffSegmentSpans(oldSegs)
	newSpans := buildDiffSegmentSpans(newSegs)
	deleteSpans := filterDiffSpansByKind(oldSpans, "delete")
	insertSpans := filterDiffSpansByKind(newSpans, "insert")
	if len(deleteSpans) > 0 && len(insertSpans) > 0 {
		return nil
	}
	if max(ansi.StringWidth(oldLine), ansi.StringWidth(newLine)) > singleLineDiffColumnsLimit {
		return nil
	}
	if len(insertSpans) > 0 && len(deleteSpans) == 0 {
		if span, ok := oneSidedSpanFromLines(oldLine, newLine); ok {
			content := span.Prefix + DiffAddInlineStyle.Render(span.Change) + span.Suffix
			if span.LineWidth+1 <= diffWidth {
				return []string{"+" + content}
			}
			windows, _, ok := fitSnippetWindows(span.LineWidth, []diffSegmentSpan{{StartCol: span.StartCol, EndCol: span.EndCol}}, diffWidth-1, maxInlineSnippetClusters)
			if !ok {
				return nil
			}
			return []string{"+" + joinANSISnippetWindows(content, windows, span.LineWidth)}
		}
		if hasMultiRunWordTokenChange(newLine, insertSpans) {
			return nil
		}
		content := buildInlineContentANSI(newSegs, "insert", DiffAddInlineStyle)
		if ansi.StringWidth(content)+1 <= diffWidth {
			return []string{"+" + content}
		}
		windows, _, ok := fitSnippetWindows(ansi.StringWidth(newLine), insertSpans, diffWidth-1, maxInlineSnippetClusters)
		if !ok {
			return nil
		}
		return []string{"+" + joinANSISnippetWindows(content, windows, ansi.StringWidth(newLine))}
	}
	if len(deleteSpans) > 0 && len(insertSpans) == 0 {
		if span, ok := oneSidedSpanFromLines(oldLine, newLine); ok {
			content := span.Prefix + DiffDelInlineStyle.Render(span.Change) + span.Suffix
			if span.LineWidth+1 <= diffWidth {
				return []string{"-" + content}
			}
			windows, _, ok := fitSnippetWindows(span.LineWidth, []diffSegmentSpan{{StartCol: span.StartCol, EndCol: span.EndCol}}, diffWidth-1, maxInlineSnippetClusters)
			if !ok {
				return nil
			}
			return []string{"-" + joinANSISnippetWindows(content, windows, span.LineWidth)}
		}
		if hasMultiRunWordTokenChange(oldLine, deleteSpans) {
			return nil
		}
		content := buildInlineContentANSI(oldSegs, "delete", DiffDelInlineStyle)
		if ansi.StringWidth(content)+1 <= diffWidth {
			return []string{"-" + content}
		}
		windows, _, ok := fitSnippetWindows(ansi.StringWidth(oldLine), deleteSpans, diffWidth-1, maxInlineSnippetClusters)
		if !ok {
			return nil
		}
		return []string{"-" + joinANSISnippetWindows(content, windows, ansi.StringWidth(oldLine))}
	}
	return nil
}
