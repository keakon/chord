package tui

import (
	"charm.land/lipgloss/v2"
)

func WordBoundsAtCol(plain string, col int) (startCol, endCol int) {
	width := selectionPlainTextWidth(plain)
	if width == 0 || col < 0 {
		return 0, 0
	}
	if col >= width {
		col = width - 1
	}
	curCol := 0
	var wordStart, wordEnd int
	inWord := false
	for _, r := range plain {
		w := selectionRuneWidthAtCol(r, curCol)
		isSpace := r == ' ' || r == '\t' || r == '\n' || r == '\r'
		if isSpace {
			if inWord {
				wordEnd = curCol
				inWord = false
				if col < wordEnd {
					return wordStart, wordEnd
				}
			}
			curCol += w
			continue
		}
		if !inWord {
			wordStart = curCol
			inWord = true
		}
		curCol += w
	}
	if inWord {
		wordEnd = curCol
		if col >= wordStart && col < wordEnd {
			return wordStart, wordEnd
		}
	}
	return 0, 0
}

// ExtractSelectionText returns the plain text of the given selection.
func applySearchMatchToLine(line string, colStart, colEnd int) string {
	if colStart >= colEnd {
		return line
	}
	startByte, endByte := findColumnByteOffsets(line, colStart, colEnd)
	if startByte < 0 {
		return line
	}
	if endByte < 0 {
		endByte = len(line)
	}
	if startByte >= endByte {
		return line
	}
	hiOn := lipgloss.NewStyle().
		Foreground(SearchMatchStyle.GetForeground()).
		Background(SearchMatchStyle.GetBackground()).
		Bold(SearchMatchStyle.GetBold()).
		Underline(SearchMatchStyle.GetUnderline()).
		Reverse(SearchMatchStyle.GetReverse()).
		Render("")
	if hiOn == "" {
		return line
	}
	hiOff := "\x1b[0m"
	highlighted := line[startByte:endByte]
	highlighted = ansiSGRRegex.ReplaceAllString(highlighted, "$0"+hiOn)
	return line[:startByte] + hiOn + highlighted + hiOff + line[endByte:]
}

func selectionPlainTextWidth(plain string) int {
	col := 0
	for _, r := range plain {
		col += selectionRuneWidthAtCol(r, col)
	}
	return col
}

func selectionStyledTextWidth(s string) int {
	col := 0
	for i := 0; i < len(s); {
		if s[i] == '\x1b' {
			next := skipANSISequence(s, i)
			if next <= i {
				next = i + 1
			}
			i = next
			continue
		}
		r, size := rune(s[i]), 1
		if r >= 0x80 {
			r, size = decodeUTF8(s[i:])
		}
		col += selectionRuneWidthAtCol(r, col)
		i += size
	}
	return col
}

func selectionRuneWidthAtCol(r rune, col int) int {
	if r == '\t' {
		spaces := preformattedTabWidth - (col % preformattedTabWidth)
		if spaces <= 0 {
			spaces = preformattedTabWidth
		}
		return spaces
	}
	return tuiStringWidth(string(r))
}

func findColumnByteOffsets(s string, colStart, colEnd int) (startByte, endByte int) {
	col := 0
	startByte = -1
	endByte = -1
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' {
			i = skipANSISequence(s, i)
			continue
		}
		r, size := rune(s[i]), 1
		if r >= 0x80 {
			r, size = decodeUTF8(s[i:])
		}
		w := selectionRuneWidthAtCol(r, col)
		if col >= colEnd {
			endByte = i
			break
		}
		if col < colStart {
			col += w
			i += size
			continue
		}
		if startByte < 0 {
			startByte = i
		}
		col += w
		endByte = i + size
		i += size
	}
	for i < len(s) && s[i] == '\x1b' {
		endByte = skipANSISequence(s, i)
		i = endByte
	}
	return startByte, endByte
}
