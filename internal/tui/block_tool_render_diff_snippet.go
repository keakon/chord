package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

func mergeDiffSnippetWindows(windows []diffSnippetWindow) []diffSnippetWindow {
	if len(windows) == 0 {
		return nil
	}
	merged := []diffSnippetWindow{windows[0]}
	for _, win := range windows[1:] {
		last := &merged[len(merged)-1]
		if win.StartCol <= last.EndCol {
			if win.EndCol > last.EndCol {
				last.EndCol = win.EndCol
			}
			continue
		}
		merged = append(merged, win)
	}
	return merged
}

func changeClusters(spans []diffSegmentSpan, gapCols int) []diffSnippetWindow {
	if len(spans) == 0 {
		return nil
	}
	clusters := make([]diffSnippetWindow, 0, len(spans))
	for _, span := range spans {
		if len(clusters) == 0 {
			clusters = append(clusters, diffSnippetWindow{StartCol: span.StartCol, EndCol: span.EndCol})
			continue
		}
		last := &clusters[len(clusters)-1]
		if span.StartCol-last.EndCol <= gapCols {
			if span.EndCol > last.EndCol {
				last.EndCol = span.EndCol
			}
			continue
		}
		clusters = append(clusters, diffSnippetWindow{StartCol: span.StartCol, EndCol: span.EndCol})
	}
	return clusters
}

func expandSnippetWindows(clusters []diffSnippetWindow, lineWidth, contextCols int) []diffSnippetWindow {
	if len(clusters) == 0 {
		return nil
	}
	windows := make([]diffSnippetWindow, 0, len(clusters))
	for _, cluster := range clusters {
		start := max(0, cluster.StartCol-contextCols)
		end := min(lineWidth, cluster.EndCol+contextCols)
		windows = append(windows, diffSnippetWindow{StartCol: start, EndCol: end})
	}
	return mergeDiffSnippetWindows(windows)
}

func snippetWindowsDisplayWidth(windows []diffSnippetWindow, lineWidth int) int {
	if len(windows) == 0 {
		return 0
	}
	width := 0
	for _, win := range windows {
		width += win.EndCol - win.StartCol
	}
	for i, win := range windows {
		if i > 0 || win.StartCol > 0 {
			width++
		}
	}
	if windows[len(windows)-1].EndCol < lineWidth {
		width++
	}
	return width
}

func fitSnippetWindows(lineWidth int, changeSpans []diffSegmentSpan, maxWidth, maxClusters int) ([]diffSnippetWindow, int, bool) {
	if lineWidth <= maxWidth {
		return []diffSnippetWindow{{StartCol: 0, EndCol: lineWidth}}, 0, true
	}
	clusters := changeClusters(changeSpans, diffSnippetMergeGapCols)
	if len(clusters) == 0 {
		return nil, 0, false
	}
	visible := min(len(clusters), maxClusters)
	for visible >= 1 {
		subset := clusters[:visible]
		hidden := len(clusters) - visible
		for ctx := diffSnippetContextCols; ctx >= diffSnippetMinContextCols; ctx-- {
			windows := expandSnippetWindows(subset, lineWidth, ctx)
			if snippetWindowsDisplayWidth(windows, lineWidth) <= maxWidth {
				return windows, hidden, true
			}
		}
		visible--
	}
	first := clusters[0]
	start := max(0, first.StartCol-diffSnippetMinContextCols)
	end := min(lineWidth, start+maxWidth)
	if end-start < maxWidth {
		start = max(0, end-maxWidth)
	}
	if end <= start {
		return nil, 0, false
	}
	return []diffSnippetWindow{{StartCol: start, EndCol: end}}, len(clusters) - 1, true
}

func joinANSISnippetWindows(rendered string, windows []diffSnippetWindow, lineWidth int) string {
	if len(windows) == 0 {
		return rendered
	}
	var buf strings.Builder
	for i, win := range windows {
		if i == 0 {
			if win.StartCol > 0 {
				buf.WriteString(DimStyle.Render("…"))
			}
		} else {
			buf.WriteString(DimStyle.Render("…"))
		}
		startByte, endByte := findColumnByteOffsets(rendered, win.StartCol, win.EndCol)
		if startByte >= 0 && endByte >= startByte {
			buf.WriteString(rendered[startByte:endByte])
		}
	}
	if windows[len(windows)-1].EndCol < lineWidth {
		buf.WriteString(DimStyle.Render("…"))
	}
	return buf.String()
}

func renderHighlightedSnippetLine(line string, changeSpans []diffSegmentSpan, diffWidth int, hl *codeHighlighter, bgTerm string) string {
	if diffWidth <= 0 {
		return ""
	}
	lineWidth := ansi.StringWidth(line)
	if lineWidth <= diffWidth {
		return hl.highlightLine(line, bgTerm)
	}
	windows, hidden, ok := fitSnippetWindows(lineWidth, changeSpans, diffWidth, maxTwoLineSnippetClusters)
	if !ok {
		return ansi.Truncate(hl.highlightLine(line, bgTerm), diffWidth, "…")
	}
	var buf strings.Builder
	for i, win := range windows {
		if i == 0 {
			if win.StartCol > 0 {
				buf.WriteString(DimStyle.Render("…"))
			}
		} else {
			buf.WriteString(DimStyle.Render("…"))
		}
		piece := extractPlainByColumns(line, win.StartCol, win.EndCol)
		buf.WriteString(hl.highlightLine(piece, bgTerm))
	}
	if windows[len(windows)-1].EndCol < lineWidth {
		buf.WriteString(DimStyle.Render("…"))
	}
	if hidden > 0 {
		summary := DimStyle.Render(fmt.Sprintf("(+%d)", hidden))
		if ansi.StringWidth(summary) <= diffWidth-defaultSnippetSummaryMinWidth {
			buf.WriteString(summary)
		}
	}
	return buf.String()
}
