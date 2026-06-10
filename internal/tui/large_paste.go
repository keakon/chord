package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/keakon/chord/internal/message"
)

const maxInlineImageAttachments = 5

const largePasteInlineMaxLines = 10

const inlineImageTokenMarker = "image"

type inlineTokenKind string

const (
	inlineTokenLargePaste inlineTokenKind = "large_paste"
	inlineTokenImage      inlineTokenKind = "image"
)

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

type inlineLargePaste struct {
	Seq         int
	Kind        inlineTokenKind
	RawContent  string
	DisplayText string
	Start       int // rune index, inclusive
	End         int // rune index, exclusive
}

func textLineCount(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

func formatInlineLargePasteDisplay(seq, lines int) string {
	if seq < 1 {
		seq = 1
	}
	if lines < 1 {
		lines = 1
	}
	return fmt.Sprintf("[Pasted text #%d +%d lines]", seq, lines)
}

func newInlineLargePaste(content string, seq int) *inlineLargePaste {
	lines := textLineCount(content)
	if lines <= largePasteInlineMaxLines {
		return nil
	}
	return &inlineLargePaste{Seq: seq, Kind: inlineTokenLargePaste, RawContent: content, DisplayText: formatInlineLargePasteDisplay(seq, lines)}
}

func newInlineImagePlaceholder(index int, displayText string) *inlineLargePaste {
	if index < 1 {
		index = 1
	}
	displayText = strings.TrimSpace(displayText)
	if displayText == "" {
		displayText = inlineImagePlaceholderDisplay
	}
	return &inlineLargePaste{Kind: inlineTokenImage, RawContent: imagePlaceholder(index), DisplayText: displayText}
}

func binaryPartReferenceText(part message.ContentPart, ordinal int) string {
	name := strings.TrimSpace(part.FileName)
	if name == "" {
		base := filepath.Base(strings.TrimSpace(part.ImagePath))
		if base != "" && base != "." && base != string(filepath.Separator) {
			name = base
		}
	}
	if name == "" {
		switch part.Type {
		case message.ContentPartPDF:
			name = fmt.Sprintf("document%d%s", max(1, ordinal), attachmentExtForMimeType(part.MimeType))
		default:
			name = imagePartDisplayName(part.FileName, part.ImagePath, part.MimeType, max(1, ordinal))
		}
	}
	return "[" + name + "]"
}

func copyInlineLargePastes(src []inlineLargePaste) []inlineLargePaste {
	if len(src) == 0 {
		return nil
	}
	out := make([]inlineLargePaste, len(src))
	copy(out, src)
	return out
}

func inlineTokenMarker(kind inlineTokenKind) string {
	switch kind {
	case inlineTokenImage:
		return inlineImageTokenMarker
	default:
		return ""
	}
}

func inlineTokenKindFromContentPart(part message.ContentPart) inlineTokenKind {
	switch {
	case part.InlineToken == inlineImageTokenMarker:
		return inlineTokenImage
	case part.DisplayText == inlineImagePlaceholderDisplay:
		if _, ok := inlineImagePlaceholderIndex(part.Text); ok {
			return inlineTokenImage
		}
	}
	return ""
}

func displayTextFromParts(parts []message.ContentPart, fallback string) string {
	if len(parts) == 0 {
		return fallback
	}
	var b strings.Builder
	for _, part := range parts {
		if part.Type != message.ContentPartText || message.IsFileRefContent(part.Text) {
			continue
		}
		if part.DisplayText != "" {
			b.WriteString(part.DisplayText)
			continue
		}
		b.WriteString(part.Text)
	}
	if b.Len() == 0 {
		return fallback
	}
	return b.String()
}

func composerTextFromParts(parts []message.ContentPart, fallback string) string {
	return displayTextFromParts(parts, fallback)
}

func draftListDisplayText(parts []message.ContentPart, fallback string) string {
	return displayTextFromParts(parts, fallback)
}

func inlineLargePastesFromParts(parts []message.ContentPart) []inlineLargePaste {
	if len(parts) == 0 {
		return nil
	}
	var (
		out        []inlineLargePaste
		offset     int
		nextSeq    = 1
		imageIndex = 1
	)
	for _, part := range parts {
		switch {
		case part.Type == message.ContentPartImage:
			token := newInlineImagePlaceholder(imageIndex, binaryPartReferenceText(part, imageIndex))
			displayRunes := []rune(token.DisplayText)
			token.Start = offset
			token.End = offset + len(displayRunes)
			out = append(out, *token)
			offset += len(displayRunes)
			imageIndex++
		case part.Type != message.ContentPartText || message.IsFileRefContent(part.Text):
			continue
		case inlineTokenKindFromContentPart(part) == inlineTokenImage:
			displayText := part.DisplayText
			if displayText == "" || displayText == inlineImagePlaceholderDisplay {
				displayText = part.Text
			}
			token := newInlineImagePlaceholder(imageIndex, displayText)
			displayRunes := []rune(token.DisplayText)
			token.Start = offset
			token.End = offset + len(displayRunes)
			out = append(out, *token)
			offset += len(displayRunes)
			imageIndex++
		case part.DisplayText == "":
			offset += len([]rune(part.Text))
		default:
			token := inlineLargePaste{Seq: nextSeq, Kind: inlineTokenLargePaste, RawContent: part.Text, DisplayText: part.DisplayText}
			displayRunes := []rune(token.DisplayText)
			token.Start = offset
			token.End = offset + len(displayRunes)
			out = append(out, token)
			offset += len(displayRunes)
			nextSeq++
		}
	}
	return out
}

func contentPartsWithInlinePastes(display string, pastes []inlineLargePaste) []message.ContentPart {
	if display == "" && len(pastes) == 0 {
		return nil
	}
	if len(pastes) == 0 {
		return []message.ContentPart{{Type: message.ContentPartText, Text: display}}
	}
	runes := []rune(display)
	var parts []message.ContentPart
	cursor := 0
	for _, paste := range pastes {
		if paste.Start > cursor {
			segment := string(runes[cursor:paste.Start])
			if segment != "" {
				parts = append(parts, message.ContentPart{Type: message.ContentPartText, Text: segment})
			}
		}
		parts = append(parts, message.ContentPart{Type: message.ContentPartText, Text: paste.RawContent, DisplayText: paste.DisplayText, InlineToken: inlineTokenMarker(paste.Kind)})
		cursor = paste.End
	}
	if cursor < len(runes) {
		segment := string(runes[cursor:])
		if segment != "" {
			parts = append(parts, message.ContentPart{Type: message.ContentPartText, Text: segment})
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return parts
}

func displayTextAndInlinePastes(parts []message.ContentPart, fallback string) (string, []inlineLargePaste) {
	if len(parts) == 0 {
		return fallback, nil
	}
	var (
		b             strings.Builder
		binaryOrdinal = 1
	)
	for _, part := range parts {
		switch {
		case part.IsBinary():
			b.WriteString(binaryPartReferenceText(part, binaryOrdinal))
			binaryOrdinal++
		case part.Type != message.ContentPartText || message.IsFileRefContent(part.Text):
			continue
		case inlineTokenKindFromContentPart(part) == inlineTokenImage:
			if part.DisplayText != "" {
				b.WriteString(part.DisplayText)
			} else {
				b.WriteString(part.Text)
			}
		case part.DisplayText != "":
			b.WriteString(part.DisplayText)
		default:
			b.WriteString(part.Text)
		}
	}
	text := b.String()
	if text == "" {
		text = fallback
	}
	return text, inlineLargePastesFromParts(parts)
}
