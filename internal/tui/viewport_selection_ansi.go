package tui

import (
	"strings"
)

func searchMatchHighlightTokens() (hiOn, hiOff string, ok bool) {
	const marker = "x"
	sample := SearchMatchStyle.Render(marker)
	before, after, ok0 := strings.Cut(sample, marker)
	if !ok0 {
		return "", "", false
	}
	hiOn = before
	hiOff = after
	if hiOn == "" || hiOff == "" {
		return "", "", false
	}
	return hiOn, hiOff, true
}

func searchMatchColumnRangeInLine(line, query string) (colStart, colEnd int, ok bool) {
	query = strings.TrimSpace(query)
	if query == "" {
		return 0, 0, false
	}
	plain := stripANSI(line)
	if plain == "" {
		return 0, 0, false
	}
	plainRunes := []rune(plain)
	queryRunes := []rune(query)
	if len(queryRunes) == 0 || len(queryRunes) > len(plainRunes) {
		return 0, 0, false
	}
	prefixCols := make([]int, len(plainRunes)+1)
	for i, r := range plainRunes {
		prefixCols[i+1] = prefixCols[i] + selectionRuneWidthAtCol(r, prefixCols[i])
	}
	for i := 0; i+len(queryRunes) <= len(plainRunes); i++ {
		if strings.EqualFold(string(plainRunes[i:i+len(queryRunes)]), query) {
			return prefixCols[i], prefixCols[i+len(queryRunes)], true
		}
	}
	return 0, 0, false
}

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
	hiOn, hiOff, ok := searchMatchHighlightTokens()
	if !ok {
		return line
	}
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
