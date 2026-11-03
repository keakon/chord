package tui

import (
	"slices"
	"strings"

	"github.com/keakon/chord/internal/tui/terminaltext"
)

// streamProseCache retains the last paragraph with its preceding separator.
// Re-rendering that small overlap preserves glamour's exact ANSI spacing.
// Constructs with document-wide or cross-paragraph meaning use the full renderer.
type streamProseCache struct {
	blocked   bool
	tailStart int
	tailLine  int
}

func independentStreamingProse(text string) bool {
	if strings.ContainsAny(text, "[<|\r") || terminaltext.Sanitize(text) != text {
		return false
	}
	for line := range strings.SplitSeq(text, "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case ' ', '\t', ':', '-', '+', '=', '>', '`', '~':
			return false
		case '*', '_':
			marker := line[0]
			n := 0
			for n < len(line) && line[n] == marker {
				n++
			}
			if n >= 3 || n == len(line) || line[n] == ' ' || line[n] == '\t' {
				return false
			}
		}
		n := 0
		for n < len(line) && line[n] >= '0' && line[n] <= '9' {
			n++
		}
		if n > 0 && n+1 < len(line) && (line[n] == '.' || line[n] == ')') && (line[n+1] == ' ' || line[n+1] == '\t') {
			return false
		}
	}
	return true
}

func (b *Block) renderSettledAssistant(raw string, width, continuation int) {
	cache := &b.streamProse
	appendOnly := b.streamSettledWidth == width && strings.HasPrefix(raw, b.streamSettledRaw)
	if !appendOnly {
		*cache = streamProseCache{}
	}
	delta := raw
	if appendOnly {
		delta = raw[len(b.streamSettledRaw):]
	}
	cache.blocked = cache.blocked || !independentStreamingProse(delta)
	var lines []string
	var synthetic []int
	var wraps []bool
	if appendOnly && !cache.blocked && b.streamSettledRaw != "" && strings.HasPrefix(delta, "\n") {
		tail, tailSynthetic, tailWraps := renderAssistantMarkdownContent(raw[cache.tailStart:], raw, width, continuation, &b.codeHL)
		lines = append(slices.Clone(b.streamSettledLines[:cache.tailLine]), tail...)
		synthetic = append(slices.Clone(b.streamSettledSyntheticPrefixWidths[:cache.tailLine]), tailSynthetic...)
		wraps = append(slices.Clone(b.streamSettledSoftWrapContinuations[:cache.tailLine]), tailWraps...)
	} else {
		lines, synthetic, wraps = renderAssistantMarkdownContent(raw, raw, width, continuation, &b.codeHL)
	}
	if !cache.blocked {
		start := strings.LastIndex(strings.TrimRight(raw, "\n"), "\n\n")
		if start < 0 {
			start = 0
		} else {
			start += 2
		}
		tail := contentMarkdown.Render(raw[start:], width)
		// The renderer can fall back after a malformed input. Only retain an
		// overlap whose bytes actually match the completed render.
		if len(tail) <= len(lines) && slices.Equal(tail, lines[len(lines)-len(tail):]) {
			cache.tailStart = start
			cache.tailLine = len(lines) - len(tail)
		} else {
			cache.blocked = true
		}
	}
	oldPrefixed := b.streamSettledPrefixedLines
	reusable := min(len(oldPrefixed), len(lines))
	if !appendOnly || !slices.Equal(b.streamSettledLines[:min(reusable, len(b.streamSettledLines))], lines[:reusable]) {
		reusable = 0
	}
	b.streamSettledPrefixedLines = oldPrefixed[:reusable]
	for _, line := range lines[reusable:] {
		b.streamSettledPrefixedLines = append(b.streamSettledPrefixedLines, "  "+line)
	}
	b.streamSettledRaw = raw
	b.streamSettledFrontier = len(raw)
	b.streamSettledWidth = width
	b.streamSettledLines = lines
	b.streamSettledSyntheticPrefixWidths = synthetic
	b.streamSettledSoftWrapContinuations = wraps
	b.hotBytesMemoValid = false
}

// thinkingRenderedTail applies the same seam rule as appendThinkingRenderedLines
// without assembling another copy of the settled line slice.
func thinkingRenderedTail(head, tail []string) []string {
	for len(head) > 0 && len(tail) > 0 && isBlankStyledLine(head[len(head)-1]) && isBlankStyledLine(tail[0]) {
		tail = tail[1:]
	}
	if len(head) > 0 && len(tail) > 0 && !isBlankStyledLine(head[len(head)-1]) && !isBlankStyledLine(tail[0]) {
		return append([]string{""}, tail...)
	}
	return tail
}

func styleThinkingTail(lines []string) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		if !isBlankStyledLine(line) {
			out[i] = "  " + currentStyles.ThinkingContentStyle.Render(line)
		}
	}
	return out
}
