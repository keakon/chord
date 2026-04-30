package tui

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	"github.com/keakon/chord/internal/tools"
)

// SelectionRange represents a contiguous selection from (BlockID, Line, Col) to end.
type SelectionRange struct {
	StartBlockID int
	StartLine    int
	StartCol     int
	EndBlockID   int
	EndLine      int
	EndCol       int
}

// lineNumPrefixRe matches a copied tool line that still includes block indent
// plus a rendered line-number column. Submatch 1=digits, 2=suffix after the
// line number (still includes visual separator spaces, if any).
var lineNumPrefixRe = regexp.MustCompile(`^\s{2}\s*(\d+)(.*)$`)

// editDiffClipLineTab matches Edit tool diff rows after normalizeLineNumberPrefix
// (e.g. "4655\t-    # comment"). Submatch 1 = source code after the +/- marker.
var editDiffClipLineTab = regexp.MustCompile(`^\s*\d+\t([-+])(.*)$`)

// editDiffClipLineSpace matches the same rows before tab-normalization, with spaces
// between the line number and the diff marker (e.g. "  4655 -    # ...").
var editDiffClipLineSpace = regexp.MustCompile(`^\s*\d+ +([-+])\s*(.*)$`)

// ansiSGRRegex matches SGR styling sequences only (ending with 'm').
var ansiSGRRegex = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// GetBlockAndLineAt returns the block and the line index within that block for
// the given global line index (0-based over all visible blocks). Returns
// (nil, -1) if globalLine is out of range.
func (v *Viewport) GetBlockAndLineAt(globalLine int) (*Block, int) {
	blocks := v.visibleBlocks()
	starts := v.blockStarts()
	if globalLine < 0 {
		return nil, -1
	}
	for i, block := range blocks {
		if i >= len(starts) {
			break
		}
		lineStart := starts[i]
		leadingSpacing := v.blockLeadingSpacing(blocks, i)
		// Turn spacing lines are rendered as empty viewport background and are not
		// considered part of any block for mouse hit-testing/selection.
		if leadingSpacing > 0 && globalLine < lineStart+leadingSpacing {
			return nil, -1
		}
		lc := v.lineCount(block, v.width)
		lineEnd := lineStart + leadingSpacing + lc
		if globalLine < lineEnd {
			line := globalLine - lineStart - leadingSpacing
			return v.materialize(block), line
		}
	}
	return nil, -1
}

// GetLinePlain returns the plain (no ANSI) text and display width of the given line in the block.
func (v *Viewport) GetLinePlain(blockID, lineInBlock int) (plain string, width int) {
	blocks := v.visibleBlocks()
	for _, b := range blocks {
		if b.ID != blockID {
			continue
		}
		b = v.materialize(b)
		lines := b.Render(v.width, "")
		if lineInBlock < 0 || lineInBlock >= len(lines) {
			return "", 0
		}
		plain = stripANSI(lines[lineInBlock])
		width = selectionPlainTextWidth(plain)
		if b.renderSyntheticPrefixWidthsW == v.width && lineInBlock < len(b.renderSyntheticPrefixWidths) {
			adjust := b.renderSyntheticPrefixWidths[lineInBlock]
			if b.renderSoftWrapContinuations != nil && lineInBlock < len(b.renderSoftWrapContinuations) && !b.renderSoftWrapContinuations[lineInBlock] {
				adjust = 0
			}
			if adjust > 0 && adjust <= width {
				plain = extractPlainByColumns(plain, adjust, width)
				width -= adjust
			}
		}
		return plain, width
	}
	return "", 0
}

// WordBoundsAtCol returns the display column range [startCol, endCol) of the word containing the given column.
func (v *Viewport) ExtractSelectionText(sel SelectionRange) string {
	if sel.StartBlockID < 0 || sel.EndBlockID < 0 {
		return ""
	}
	blocks, startIdx, endIdx := v.selectionBlocksInOrder(sel.StartBlockID, sel.EndBlockID)
	if len(blocks) == 0 || startIdx < 0 || endIdx < 0 {
		return ""
	}
	sIdx, eIdx := startIdx, endIdx
	sL, sC := sel.StartLine, sel.StartCol
	eL, eC := sel.EndLine, sel.EndCol
	if sIdx > eIdx || (sIdx == eIdx && (sL > eL || (sL == eL && sC > eC))) {
		sIdx, eIdx = eIdx, sIdx
		sL, eL = eL, sL
		sC, eC = eC, sC
	}
	var sb strings.Builder
	for i := sIdx; i <= eIdx; i++ {
		block := v.materialize(blocks[i])
		lines := block.Render(v.width, "")
		lineStart := 0
		lineEnd := len(lines) - 1
		if i == sIdx {
			lineStart = sL
		}
		if i == eIdx {
			lineEnd = eL
		}
		for lineIdx := lineStart; lineIdx <= lineEnd; lineIdx++ {
			if lineIdx >= len(lines) {
				continue
			}
			if placeholder := selectionImagePlaceholder(block, lineIdx); placeholder != "" {
				if sb.Len() > 0 {
					sb.WriteByte('\n')
				}
				sb.WriteString(placeholder)
				continue
			}
			line := lines[lineIdx]
			plain := stripANSI(line)
			lineWidth := selectionPlainTextWidth(plain)
			prefixAdjust := v.selectionLinePrefixWidth(block, lineIdx, plain)
			if prefixAdjust < 0 {
				prefixAdjust = 0
			}
			if prefixAdjust > lineWidth {
				prefixAdjust = lineWidth
			}
			absStart := prefixAdjust
			absEnd := lineWidth
			if i == sIdx && lineIdx == sL {
				absStart = sC
				if absStart < prefixAdjust {
					absStart = prefixAdjust
				}
				if absStart > lineWidth {
					absStart = lineWidth
				}
			}
			if i == eIdx && lineIdx == eL {
				absEnd = eC
				if absEnd < prefixAdjust {
					absEnd = prefixAdjust
				}
				if absEnd > lineWidth {
					absEnd = lineWidth
				}
			}
			if absStart < absEnd {
				segment := extractPlainByColumns(plain, absStart, absEnd)
				if i == sIdx && i == eIdx && lineIdx == sL && lineIdx == eL && segment != "" {
					if plainSegmentWidth := selectionPlainTextWidth(segment); absEnd-absStart > plainSegmentWidth {
						segment = extractPlainByColumns(plain, absStart, min(lineWidth, absEnd+1))
					}
				}
				segment = strings.TrimRight(segment, " ")
				if block.Type == BlockToolCall && block.ToolName == tools.NameEdit && block.Diff != "" {
					segment = stripEditDiffClipboardLine(segment)
				}
				if segment != "" {
					if sb.Len() > 0 {
						sb.WriteByte('\n')
					}
					sb.WriteString(segment)
				}
			}
		}
	}
	raw := normalizeLineNumberPrefix(sb.String())
	text := dedentLines(raw)
	text = dedentLinesSkipUnindented(text)
	return strings.TrimSpace(text)
}

func (v *Viewport) selectionLinePrefixWidth(block *Block, lineIdx int, plain string) int {
	if block == nil {
		return 0
	}
	prefix := selectionRailPrefixWidth(plain)
	baseInset := v.selectionCardInsetWidth(block)
	if block.renderSyntheticPrefixWidthsW == v.width && lineIdx >= 0 && lineIdx < len(block.renderSyntheticPrefixWidths) {
		if synthetic := block.renderSyntheticPrefixWidths[lineIdx]; synthetic > baseInset {
			baseInset = synthetic
		}
	}
	return prefix + max(0, baseInset)
}

func selectionRailPrefixWidth(plain string) int {
	if strings.HasPrefix(plain, "│") {
		return ansi.StringWidth("│")
	}
	return 0
}

func (v *Viewport) selectionCardInsetWidth(block *Block) int {
	if block == nil {
		return 0
	}
	switch block.Type {
	case BlockUser:
		return UserCardStyle.GetMarginLeft() + UserCardStyle.GetPaddingLeft()
	case BlockAssistant:
		base := AssistantCardStyle.GetMarginLeft() + AssistantCardStyle.GetPaddingLeft()
		thinkingInset := ThinkingCardStyle.GetMarginLeft() + ThinkingCardStyle.GetPaddingLeft()
		return max(base, thinkingInset)
	case BlockThinking:
		return ThinkingCardStyle.GetMarginLeft() + ThinkingCardStyle.GetPaddingLeft()
	case BlockToolCall, BlockToolResult:
		return ToolBlockStyle.GetMarginLeft() + ToolBlockStyle.GetPaddingLeft()
	case BlockError:
		return ErrorCardStyle.GetMarginLeft() + ErrorCardStyle.GetPaddingLeft()
	case BlockStatus, BlockCompactionSummary:
		return CompactionSummaryCardStyle.GetMarginLeft() + CompactionSummaryCardStyle.GetPaddingLeft()
	default:
		return 0
	}
}

func (v *Viewport) selectionBlocksInOrder(startBlockID, endBlockID int) ([]*Block, int, int) {
	blocks := v.visibleBlocks()
	startIdx, endIdx := -1, -1
	for i, b := range blocks {
		if b == nil {
			continue
		}
		if b.ID == startBlockID {
			startIdx = i
		}
		if b.ID == endBlockID {
			endIdx = i
		}
	}
	if startIdx >= 0 && endIdx >= 0 {
		return blocks, startIdx, endIdx
	}

	all := v.blocks
	startIdx, endIdx = -1, -1
	for i, b := range all {
		if b == nil {
			continue
		}
		if b.ID == startBlockID {
			startIdx = i
		}
		if b.ID == endBlockID {
			endIdx = i
		}
	}
	if startIdx < 0 || endIdx < 0 {
		return nil, -1, -1
	}
	return all, startIdx, endIdx
}

func selectionImagePlaceholder(block *Block, lineInBlock int) string {
	if block == nil || block.Type != BlockUser || len(block.ImageParts) == 0 {
		return ""
	}
	for _, part := range block.ImageParts {
		if part.RenderStartLine < 0 || part.RenderEndLine < part.RenderStartLine {
			continue
		}
		if lineInBlock < part.RenderStartLine || lineInBlock > part.RenderEndLine {
			continue
		}
		name := strings.TrimSpace(part.FileName)
		if name == "" {
			name = "image"
		}
		return "[image: " + name + "]"
	}
	return ""
}

func stripEditDiffClipboardLine(line string) string {
	s := strings.TrimRight(line, " ")
	if s == "" {
		return s
	}
	if m := editDiffClipLineTab.FindStringSubmatch(s); len(m) == 3 {
		return m[2]
	}
	if m := editDiffClipLineSpace.FindStringSubmatch(s); len(m) == 3 {
		return m[2]
	}
	return line
}

func selectionColRange(blockID, lineInBlock int, sel *SelectionRange) (colStart, colEnd int, inRange bool) {
	sb, sl, sc := sel.StartBlockID, sel.StartLine, sel.StartCol
	eb, el, ec := sel.EndBlockID, sel.EndLine, sel.EndCol
	if posLess(eb, el, ec, sb, sl, sc) {
		sb, sl, sc, eb, el, ec = eb, el, ec, sb, sl, sc
	}
	if posLess(blockID, lineInBlock, 0, sb, sl, 0) || posLess(eb, el, 0, blockID, lineInBlock, 0) {
		return 0, 0, false
	}
	inRange = true
	if blockID == sb && lineInBlock == sl {
		colStart = sc
	} else {
		colStart = 0
	}
	if blockID == eb && lineInBlock == el {
		colEnd = ec
	} else {
		colEnd = 9999
	}
	return colStart, colEnd, inRange
}

func posLess(b1, l1, c1, b2, l2, c2 int) bool {
	if b1 != b2 {
		return b1 < b2
	}
	if l1 != l2 {
		return l1 < l2
	}
	return c1 < c2
}

func applyHighlightToLine(line string, colStart, colEnd int) string {
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
	const hiOn = "\x1b[7m"
	const hiOff = "\x1b[27m"
	highlighted := line[startByte:endByte]
	highlighted = ansiSGRRegex.ReplaceAllString(highlighted, "$0"+hiOn)
	return line[:startByte] + hiOn + highlighted + hiOff + line[endByte:]
}

func stripANSI(s string) string {
	return ansi.Strip(s)
}

// stripANSILines returns a copy of lines with ANSI escapes removed from each
// line. Useful for producing readable diagnostic output in test failure
// messages where we want to show what text was actually rendered.
func stripANSILines(lines []string) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = stripANSI(line)
	}
	return out
}

func skipANSISequence(s string, i int) int {
	if i >= len(s) || s[i] != '\x1b' {
		return i
	}
	i++
	if i >= len(s) {
		return i
	}
	ch := s[i]
	switch ch {
	case '[':
		i++
		for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
			i++
		}
		if i < len(s) {
			i++
		}
	case ']':
		i++
		for i < len(s) && s[i] != '\x07' {
			if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '\\' {
				i += 2
				break
			}
			i++
		}
		if i < len(s) && s[i] == '\x07' {
			i++
		}
	default:
		i++
	}
	return i
}

func decodeUTF8(s string) (rune, int) {
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError && size == 0 {
		return rune(s[0]), 1
	}
	return r, size
}

func truncateLineToDisplayWidth(s string, width int) string {
	if width <= 0 {
		return s
	}
	if tuiStringWidth(s) <= width {
		return s
	}
	out := tuiCut(s, 0, width)

	if len(out) < len(s) && strings.Contains(s, "\x1b[") {
		baseBg := firstSGRBackgroundSeq(s)
		out += "\x1b[49m"
		if baseBg != "" {
			out += baseBg
		}
		out += ansi.ResetStyle
	}
	return out
}

func firstSGRBackgroundSeq(s string) string {
	for i := 0; i < len(s); {
		if s[i] != '\x1b' {
			i++
			continue
		}
		if i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			if j < len(s) && s[j] == 'm' {
				if sgrSetsBackground(s[i+2 : j]) {
					return s[i : j+1]
				}
				i = j + 1
				continue
			}
		}
		i = skipANSISequence(s, i)
	}
	return ""
}

func sgrSetsBackground(params string) bool {
	if params == "" {
		return false
	}
	parts := strings.Split(params, ";")
	for idx := 0; idx < len(parts); idx++ {
		if parts[idx] != "48" {
			continue
		}
		if idx+2 >= len(parts) {
			continue
		}
		mode := parts[idx+1]
		switch mode {
		case "5":
			return true
		case "2":
			if idx+4 < len(parts) {
				return true
			}
		}
	}
	return false
}

func extractPlainByColumns(s string, startCol, endCol int) string {
	plain := stripANSI(s)
	if startCol < 0 {
		startCol = 0
	}
	if endCol < startCol {
		endCol = startCol
	}
	col := 0
	var b strings.Builder
	for _, r := range plain {
		w := selectionRuneWidthAtCol(r, col)
		nextCol := col + w
		if nextCol > startCol && col < endCol {
			b.WriteRune(r)
		}
		col = nextCol
		if col >= endCol {
			break
		}
	}
	return b.String()
}
