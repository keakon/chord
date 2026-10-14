package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/keakon/chord/internal/inputhistory"
	"github.com/keakon/chord/internal/tui/composer"
)

func historyTextParts(entry composer.HistoryEntry, budget int64) ([]inputhistory.TextPart, error) {
	if !utf8.ValidString(entry.Display) {
		return nil, fmt.Errorf("invalid composer UTF-8")
	}
	var parts []inputhistory.TextPart
	var size int64
	add := func(kind inputhistory.PartKind, text string) error {
		if text == "" {
			return nil
		}
		size += int64(len(text))
		if size > budget || len(parts) >= inputhistory.MaxParts {
			return inputhistory.ErrTooLarge
		}
		if !utf8.ValidString(text) {
			return fmt.Errorf("invalid paste UTF-8")
		}
		if kind == inputhistory.Text && len(parts) > 0 && parts[len(parts)-1].Kind == kind {
			parts[len(parts)-1].Text += text
		} else {
			parts = append(parts, inputhistory.TextPart{Kind: kind, Text: text})
		}
		return nil
	}
	bytePos, runePos, previous := 0, 0, 0
	advance := func(target int) bool {
		for runePos < target && bytePos < len(entry.Display) {
			_, n := utf8.DecodeRuneInString(entry.Display[bytePos:])
			bytePos += n
			runePos++
		}
		return runePos == target
	}
	for _, p := range entry.InlinePastes {
		if p.Start < runePos || p.End <= p.Start || !advance(p.Start) {
			return nil, fmt.Errorf("invalid composer object range")
		}
		if err := add(inputhistory.Text, entry.Display[previous:bytePos]); err != nil {
			return nil, err
		}
		start := bytePos
		if !advance(p.End) || entry.Display[start:bytePos] != p.DisplayText {
			return nil, fmt.Errorf("invalid composer object display")
		}
		switch p.Kind {
		case composer.TokenPaste:
			if err := add(inputhistory.Paste, p.RawContent); err != nil {
				return nil, err
			}
		case composer.TokenImage:
		default:
			return nil, fmt.Errorf("invalid composer object kind")
		}
		previous = bytePos
	}
	if err := add(inputhistory.Text, entry.Display[previous:]); err != nil {
		return nil, err
	}
	return parts, nil
}
func historyRestoreEntry(r inputhistory.Record) composer.HistoryEntry {
	entry := composer.HistoryEntry{BangMode: r.Kind == inputhistory.ShellCommand}
	var b strings.Builder
	pos := 0
	for _, p := range r.TextParts {
		if p.Kind == inputhistory.Paste || len(p.Text) > 8192 || strings.Count(p.Text, "\n") >= 32 {
			entry.NextPasteSeq++
			display := composer.FormatPasteLabel(entry.NextPasteSeq, composer.TextLineCount(p.Text))
			end := pos + utf8.RuneCountInString(display)
			entry.InlinePastes = append(entry.InlinePastes, composer.Token{Seq: entry.NextPasteSeq, Kind: composer.TokenPaste, RawContent: p.Text, DisplayText: display, Start: pos, End: end})
			b.WriteString(display)
			pos = end
		} else {
			b.WriteString(p.Text)
			pos += utf8.RuneCountInString(p.Text)
		}
	}
	entry.Display = b.String()
	return entry
}
