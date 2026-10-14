package tui

import (
	"strings"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

func assistantContentHasVisibleText(content string) bool {
	content = removeTrailingCursorGlyph(content)
	content = terminaltext.StripANSI(content)
	return message.HasVisibleText(content)
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
	content = removeTrailingCursorGlyph(content)
	content = terminaltext.StripANSI(content)
	if !message.HasVisibleText(content) {
		return true
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
			return false
		}
	}
	return hasEllipsis || dots > 0
}
