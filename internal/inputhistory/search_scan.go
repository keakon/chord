package inputhistory

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"
)

const searchScanBytes = 16 << 10

// NormalizeQuery applies the same bounded query to the editor and matcher.
func NormalizeQuery(value string) string {
	var b strings.Builder
	count, words := 0, 0
	inWord := false
	for _, r := range value {
		if count >= 256 {
			break
		}
		if r == '\n' || r == '\r' || r == '\t' {
			r = ' '
		}
		space := unicode.IsSpace(r)
		if !space && !inWord {
			words++
			if words > 16 {
				break
			}
		}
		inWord = !space
		b.WriteRune(r)
		count++
	}
	return b.String()
}

func containsHistoryText(ctx context.Context, text, needle string) bool {
	if needle == "" {
		return true
	}
	for start := 0; start < len(text); start += searchScanBytes {
		if ctx.Err() != nil {
			return false
		}
		end := min(len(text), start+searchScanBytes+len(needle)-1)
		if strings.Contains(text[start:end], needle) {
			return true
		}
	}
	return false
}

// Stop counting as soon as fuzzy matching is ruled out.
func historyRuneLimit(ctx context.Context, text string, limit int) bool {
	count, checked := 0, 0
	for offset := 0; offset < len(text); {
		if offset >= checked {
			if ctx.Err() != nil {
				return false
			}
			checked = offset + searchScanBytes
		}
		_, size := utf8.DecodeRuneInString(text[offset:])
		offset += size
		count++
		if count > limit {
			return false
		}
	}
	return true
}
