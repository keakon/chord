package composer

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/keakon/chord/internal/message"
)

const largePasteInlineMaxLines = 10

const InlineImageTokenMarker = "image"

type TokenKind string

const (
	TokenPaste TokenKind = "large_paste"
	TokenImage TokenKind = "image"
)

type Token struct {
	Seq         int
	Kind        TokenKind
	RawContent  string
	DisplayText string
	Start       int // rune index, inclusive
	End         int // rune index, exclusive
}

func TextLineCount(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

func FormatPasteLabel(seq, lines int) string {
	if seq < 1 {
		seq = 1
	}
	if lines < 1 {
		lines = 1
	}
	return fmt.Sprintf("[Pasted text #%d +%d lines]", seq, lines)
}

func newInlineLargePaste(content string, seq int) *Token {
	lines := TextLineCount(content)
	if lines <= largePasteInlineMaxLines {
		return nil
	}
	return &Token{Seq: seq, Kind: TokenPaste, RawContent: content, DisplayText: FormatPasteLabel(seq, lines)}
}

func newInlineImagePlaceholder(index int, displayText string) *Token {
	if index < 1 {
		index = 1
	}
	displayText = strings.TrimSpace(displayText)
	if displayText == "" {
		displayText = InlineImagePlaceholderDisplay
	}
	return &Token{Kind: TokenImage, RawContent: ImagePlaceholder(index), DisplayText: displayText}
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
			name = fmt.Sprintf("document%d%s", max(1, ordinal), AttachmentExtForMimeType(part.MimeType))
		default:
			name = ImagePartDisplayName(part.FileName, part.ImagePath, part.MimeType, max(1, ordinal))
		}
	}
	return "[" + name + "]"
}

func copyInlineLargePastes(src []Token) []Token {
	if len(src) == 0 {
		return nil
	}
	out := make([]Token, len(src))
	copy(out, src)
	return out
}

func inlineTokenMarker(kind TokenKind) string {
	switch kind {
	case TokenImage:
		return InlineImageTokenMarker
	default:
		return ""
	}
}

func inlineTokenKindFromContentPart(part message.ContentPart) TokenKind {
	switch {
	case part.InlineToken == InlineImageTokenMarker:
		return TokenImage
	case part.DisplayText == InlineImagePlaceholderDisplay:
		if _, ok := InlineImagePlaceholderIndex(part.Text); ok {
			return TokenImage
		}
	}
	return ""
}

func DisplayTextFromParts(parts []message.ContentPart, fallback string) string {
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

func inlineLargePastesFromParts(parts []message.ContentPart) []Token {
	if len(parts) == 0 {
		return nil
	}
	var (
		out        []Token
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
		case inlineTokenKindFromContentPart(part) == TokenImage:
			displayText := part.DisplayText
			if displayText == "" || displayText == InlineImagePlaceholderDisplay {
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
			token := Token{Seq: nextSeq, Kind: TokenPaste, RawContent: part.Text, DisplayText: part.DisplayText}
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

func contentPartsWithInlinePastes(display string, pastes []Token) []message.ContentPart {
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

func DisplayTextAndInlinePastes(parts []message.ContentPart, fallback string) (string, []Token) {
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
		case inlineTokenKindFromContentPart(part) == TokenImage:
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
