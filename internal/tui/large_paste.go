package tui

import (
	"strings"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tui/composer"
)

const maxInlineImageAttachments = 5

// userBlockTextFromParts builds the full user message text for transcript USER
// cards: it concatenates each non-file-ref text part's Text field and ignores
// DisplayText placeholders used in the composer for large pastes.
func userBlockTextFromParts(parts []message.ContentPart, fallback string) string {
	if len(parts) == 0 {
		return fallback
	}
	var b strings.Builder
	for _, part := range parts {
		if part.Type != message.ContentPartText || message.IsFileRefContent(part.Text) {
			continue
		}
		b.WriteString(part.Text)
	}
	out := b.String()
	if out == "" {
		return fallback
	}
	return out
}

func composerTextFromParts(parts []message.ContentPart, fallback string) string {
	return composer.DisplayTextFromParts(parts, fallback)
}

func draftListDisplayText(parts []message.ContentPart, fallback string) string {
	return composer.DisplayTextFromParts(parts, fallback)
}
