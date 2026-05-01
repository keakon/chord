package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

func firstDisplayLine(s string) string {
	s = sanitizeToolDisplayText(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	if before, _, ok := strings.Cut(s, "\n"); ok {
		return before
	}
	return s
}

func continuationDisplayLines(s string) []string {
	s = sanitizeToolDisplayText(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	parts := strings.Split(s, "\n")
	if len(parts) <= 1 {
		return nil
	}
	return parts[1:]
}

func wrapLiteralText(text string, width int) []string {
	if width <= 0 {
		width = 80
	}
	if text == "" {
		return []string{""}
	}
	var result []string
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			result = append(result, "")
			continue
		}
		for ansi.StringWidth(line) > width {
			result = append(result, ansi.Cut(line, 0, width))
			line = ansi.Cut(line, width, ansi.StringWidth(line))
		}
		result = append(result, line)
	}
	if len(result) == 0 {
		return []string{""}
	}
	return result
}

func wrapIndentedText(text string, width int) []string {
	if width <= 0 {
		width = 80
	}
	if text == "" {
		return []string{""}
	}
	var result []string
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			result = append(result, "")
			continue
		}
		indentWidth := countLeadingWhitespace(line)
		indent := line[:indentWidth]
		rest := line[indentWidth:]
		available := width - ansi.StringWidth(indent)
		if available <= 0 {
			available = width
			indent = ""
		}
		wrapped := wrapLiteralText(rest, available)
		for _, w := range wrapped {
			result = append(result, indent+w)
		}
	}
	if len(result) == 0 {
		return []string{""}
	}
	return result
}

func appendQuestionAnswerLines(dst []string, text, firstPrefix, continuationPrefix string, width int) []string {
	text = sanitizeToolDisplayText(text)
	if width <= 0 {
		width = 80
	}
	first := true
	for _, logicalLine := range strings.Split(text, "\n") {
		prefix := continuationPrefix
		if first {
			prefix = firstPrefix
		}
		available := width - ansi.StringWidth(prefix)
		if available <= 0 {
			available = width
			prefix = ""
		}
		wrapped := wrapLiteralText(logicalLine, available)
		if len(wrapped) == 0 {
			wrapped = []string{""}
		}
		for idx, line := range wrapped {
			curPrefix := continuationPrefix
			if first && idx == 0 {
				curPrefix = firstPrefix
			}
			dst = append(dst, paramValStyle.Render(curPrefix+line))
		}
		first = false
	}
	if first {
		dst = append(dst, paramValStyle.Render(firstPrefix))
	}
	return dst
}
