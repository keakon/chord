package tui

import (
	"strings"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

// assistantContentVisibility answers both questions renderAssistant asks about
// an assistant body: whether any visible text exists, and whether what remains
// is only the streaming placeholder ellipsis. They share one ANSI strip and one
// rune scan because the body is rescanned on every streaming render, so paying
// for two full passes made this check grow with message length.
func assistantContentVisibility(content string) (hasVisible, isPlaceholder bool) {
	content = removeTrailingCursorGlyph(content)
	content = terminaltext.StripANSI(content)
	if !message.HasVisibleText(content) {
		return false, true
	}
	dots := 0
	hasEllipsis := false
	for _, r := range content {
		switch {
		case r == '.':
			dots++
		case r == '…':
			hasEllipsis = true
		case !message.IsVisibleTextRune(r):
		default:
			return true, false
		}
	}
	return true, hasEllipsis || dots > 0
}

func assistantContentHasVisibleText(content string) bool {
	hasVisible, _ := assistantContentVisibility(content)
	return hasVisible
}

func visibleAssistantStreamContent(content string) string {
	content = removeTrailingCursorGlyph(content)
	content = terminaltext.StripANSI(content)
	if !message.HasVisibleText(content) {
		return ""
	}
	return strings.TrimSpace(content)
}

func assistantStreamContentIsPlaceholder(content string) bool {
	_, isPlaceholder := assistantContentVisibility(content)
	return isPlaceholder
}
