package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	tea "github.com/keakon/bubbletea/v2"
)

type contentViewerState struct {
	title        string
	content      string
	prevMode     Mode
	scrollOffset int

	selecting              bool
	selStartLine           int
	selStartCol            int
	selEndLine             int
	selEndCol              int
	selEndInclusiveForCopy bool

	cachedLines []string
	cachedWidth int

	renderCacheWidth  int
	renderCacheHeight int
	renderCacheOffset int
	renderCacheTheme  string
	renderCacheText   string
}

func (m *Model) openContentViewer(title, content string) tea.Cmd {
	prevMode := m.mode
	m.clearActiveSearch()
	if prevMode == ModeInsert {
		m.input.Blur()
	}
	m.clearChordState()
	m.contentViewer = contentViewerState{
		title:        strings.TrimSpace(title),
		content:      content,
		prevMode:     prevMode,
		selStartLine: -1,
		selEndLine:   -1,
	}
	m.mode = ModeContentViewer
	m.recalcViewportSize()
	return nil
}

func (m *Model) closeContentViewer() tea.Cmd {
	if m.mode != ModeContentViewer {
		return nil
	}
	prevMode := m.contentViewer.prevMode
	m.contentViewer = contentViewerState{}
	cmd := m.restoreModeWithIME(prevMode)
	m.recalcViewportSize()
	if prevMode == ModeInsert {
		return tea.Batch(cmd, m.input.Focus())
	}
	return cmd
}

func contentViewerHorizontalMargin(width int) int {
	if width < 40 {
		return 1
	}
	return 2
}

func contentViewerVerticalMargin(height int) int {
	if height < 8 {
		return 0
	}
	return 1
}

func contentViewerInnerWidth(width int) int {
	margin := contentViewerHorizontalMargin(width)
	inner := width - margin*2
	if inner < 20 {
		inner = max(1, width)
	}
	return inner
}

func contentViewerBodyHeight(height int) int {
	margin := contentViewerVerticalMargin(height)
	body := height - margin*2
	if body < 1 {
		body = max(1, height)
	}
	return body
}

func (m *Model) cachedContentViewerLines(width int) []string {
	if width <= 0 {
		width = 80
	}
	innerWidth := contentViewerInnerWidth(width)
	if m.contentViewer.cachedWidth == innerWidth && m.contentViewer.cachedLines != nil {
		return m.contentViewer.cachedLines
	}
	contentWidth := innerWidth - 2
	if contentWidth < 20 {
		contentWidth = 20
	}
	lines := []string{
		centerHelpLine(m.contentViewer.title, innerWidth),
		"",
	}
	content := strings.TrimSpace(m.contentViewer.content)
	if content == "" {
		lines = append(lines, DimStyle.Render("(empty)"))
	} else {
		lines = append(lines, renderRichMarkdownContent(content, contentWidth, nil)...)
	}
	m.contentViewer.cachedLines = lines
	m.contentViewer.cachedWidth = innerWidth
	return lines
}

func (m *Model) contentViewerMaxScroll(width int) int {
	lines := m.cachedContentViewerLines(width)
	bodyHeight := contentViewerBodyHeight(m.viewport.height)
	if len(lines) <= bodyHeight {
		return 0
	}
	return len(lines) - bodyHeight
}

func (m *Model) clampContentViewerScroll(width int) {
	maxScroll := m.contentViewerMaxScroll(width)
	if m.contentViewer.scrollOffset < 0 {
		m.contentViewer.scrollOffset = 0
	}
	if m.contentViewer.scrollOffset > maxScroll {
		m.contentViewer.scrollOffset = maxScroll
	}
}

func (m *Model) scrollContentViewer(delta int) {
	m.contentViewer.scrollOffset += delta
	m.clampContentViewerScroll(m.viewport.width)
}

func (m *Model) contentViewerHasSelection() bool {
	return m.contentViewer.selStartLine >= 0 && m.contentViewer.selEndLine >= 0
}

func (m *Model) clearContentViewerSelection() {
	m.contentViewer.selecting = false
	m.contentViewer.selStartLine = -1
	m.contentViewer.selStartCol = -1
	m.contentViewer.selEndLine = -1
	m.contentViewer.selEndCol = -1
	m.contentViewer.selEndInclusiveForCopy = false
	m.contentViewer.renderCacheText = ""
}

func (m *Model) contentViewerSelectionRange() (startLine, startCol, endLine, endCol int, ok bool) {
	if !m.contentViewerHasSelection() {
		return 0, 0, 0, 0, false
	}
	startLine = m.contentViewer.selStartLine
	startCol = m.contentViewer.selStartCol
	endLine = m.contentViewer.selEndLine
	endCol = m.contentViewer.selEndCol
	if m.contentViewer.selEndInclusiveForCopy {
		if startLine < endLine || (startLine == endLine && startCol < endCol) {
			endCol++
		} else if endLine < startLine || (startLine == endLine && endCol < startCol) {
			startCol++
		}
	}
	if endLine < startLine || (startLine == endLine && endCol < startCol) {
		startLine, endLine = endLine, startLine
		startCol, endCol = endCol, startCol
	}
	return startLine, startCol, endLine, endCol, true
}

func (m *Model) contentViewerSelectionColRange(lineIdx int, line string) (int, int, bool) {
	startLine, startCol, endLine, endCol, ok := m.contentViewerSelectionRange()
	if !ok || lineIdx < startLine || lineIdx > endLine {
		return 0, 0, false
	}
	lineWidth := selectionStyledTextWidth(line)
	colStart := 0
	colEnd := lineWidth
	if lineIdx == startLine {
		colStart = startCol
	}
	if lineIdx == endLine {
		colEnd = endCol
	}
	if colStart < 0 {
		colStart = 0
	}
	if colEnd > lineWidth {
		colEnd = lineWidth
	}
	return colStart, colEnd, colStart < colEnd
}

func (m *Model) contentViewerSelectionPointAt(mouse tea.Mouse) (int, int, bool) {
	if m.layout.main.Dx() <= 0 || m.layout.main.Dy() <= 0 {
		return 0, 0, false
	}
	if mouse.X < m.layout.main.Min.X || mouse.X >= m.layout.main.Max.X || mouse.Y < m.layout.main.Min.Y || mouse.Y >= m.layout.main.Max.Y {
		return 0, 0, false
	}
	marginY := contentViewerVerticalMargin(m.viewport.height)
	row := mouse.Y - m.layout.main.Min.Y - marginY
	if row < 0 || row >= contentViewerBodyHeight(m.viewport.height) {
		return 0, 0, false
	}
	lineIdx := m.contentViewer.scrollOffset + row
	lines := m.cachedContentViewerLines(m.viewport.width)
	if lineIdx < 0 || lineIdx >= len(lines) {
		return 0, 0, false
	}
	marginX := contentViewerHorizontalMargin(m.viewport.width)
	col := mouse.X - m.layout.main.Min.X - marginX
	if col < 0 {
		col = 0
	}
	lineWidth := selectionStyledTextWidth(lines[lineIdx])
	if col > lineWidth {
		col = lineWidth
	}
	return lineIdx, col, true
}

func (m *Model) handleContentViewerSelectionClick(mouse tea.Mouse) bool {
	line, col, ok := m.contentViewerSelectionPointAt(mouse)
	if !ok {
		m.clearContentViewerSelection()
		return false
	}

	now := time.Now()
	if now.Sub(m.lastClickTime) <= doubleClickThreshold &&
		abs(mouse.X-m.lastClickX) <= mouseClickTolerance &&
		abs(mouse.Y-m.lastClickY) <= mouseClickTolerance {
		m.clickCount++
	} else {
		m.clickCount = 1
	}
	m.lastClickTime = now
	m.lastClickX = mouse.X
	m.lastClickY = mouse.Y

	lines := m.cachedContentViewerLines(m.viewport.width)
	if line < 0 || line >= len(lines) {
		m.clearContentViewerSelection()
		return false
	}
	plain := stripANSI(lines[line])

	if m.clickCount == 2 {
		sCol, eCol := WordBoundsAtCol(plain, col)
		if sCol < eCol {
			m.contentViewer.selecting = false
			m.contentViewer.selStartLine = line
			m.contentViewer.selStartCol = sCol
			m.contentViewer.selEndLine = line
			m.contentViewer.selEndCol = eCol
			m.contentViewer.selEndInclusiveForCopy = false
			m.contentViewer.renderCacheText = ""
		} else {
			m.startContentViewerSelectionAt(line, col)
		}
		return true
	}
	if m.clickCount >= 3 {
		m.clickCount = 0
		lineWidth := selectionPlainTextWidth(plain)
		if lineWidth > 0 {
			m.contentViewer.selecting = false
			m.contentViewer.selStartLine = line
			m.contentViewer.selStartCol = 0
			m.contentViewer.selEndLine = line
			m.contentViewer.selEndCol = lineWidth
			m.contentViewer.selEndInclusiveForCopy = false
			m.contentViewer.renderCacheText = ""
		} else {
			m.startContentViewerSelectionAt(line, col)
		}
		return true
	}

	m.startContentViewerSelectionAt(line, col)
	return true
}

func (m *Model) startContentViewerSelectionAt(line, col int) {
	m.contentViewer.selecting = true
	m.contentViewer.selStartLine = line
	m.contentViewer.selStartCol = col
	m.contentViewer.selEndLine = line
	m.contentViewer.selEndCol = col
	m.contentViewer.selEndInclusiveForCopy = true
	m.contentViewer.renderCacheText = ""
}

func (m *Model) updateContentViewerSelection(mouse tea.Mouse) bool {
	if !m.contentViewer.selecting {
		return false
	}
	line, col, ok := m.contentViewerSelectionPointAt(mouse)
	if !ok {
		return true
	}
	m.contentViewer.selEndLine = line
	m.contentViewer.selEndCol = col
	m.contentViewer.selEndInclusiveForCopy = true
	m.contentViewer.renderCacheText = ""
	return true
}

func (m *Model) selectedContentViewerText() string {
	startLine, startCol, endLine, endCol, ok := m.contentViewerSelectionRange()
	if !ok {
		return ""
	}
	lines := m.cachedContentViewerLines(m.viewport.width)
	if startLine < 0 {
		startLine = 0
	}
	if endLine >= len(lines) {
		endLine = len(lines) - 1
	}
	var sb strings.Builder
	for lineIdx := startLine; lineIdx <= endLine; lineIdx++ {
		plain := stripANSI(lines[lineIdx])
		lineWidth := selectionPlainTextWidth(plain)
		absStart := 0
		absEnd := lineWidth
		if lineIdx == startLine {
			absStart = startCol
		}
		if lineIdx == endLine {
			absEnd = endCol
		}
		if absStart < 0 {
			absStart = 0
		}
		if absEnd > lineWidth {
			absEnd = lineWidth
		}
		if absStart >= absEnd {
			continue
		}
		segment := strings.TrimRight(extractPlainByColumns(plain, absStart, absEnd), " ")
		if segment == "" {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(segment)
	}
	return strings.TrimSpace(sb.String())
}

func (m *Model) copyContentViewerSelection() tea.Cmd {
	content := m.selectedContentViewerText()
	if content == "" {
		return nil
	}
	m.clearContentViewerSelection()
	return writeClipboardCmd(content, "Selection copied to clipboard")
}

func (m *Model) copyContentViewerAll() tea.Cmd {
	content := m.contentViewer.content
	if strings.TrimSpace(content) == "" {
		return m.enqueueToast("View content is empty", "info")
	}
	m.clearContentViewerSelection()
	return writeClipboardCmd(content, "View content copied to clipboard")
}

func (m *Model) handleContentViewerKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "q":
		m.clearChordState()
		return m.closeContentViewer()
	case "y", "Y":
		if m.contentViewerHasSelection() {
			cmd := m.copyContentViewerSelection()
			if cmd == nil {
				return m.startChordOp(chordY)
			}
			return tea.Batch(cmd, m.startChordOp(chordY))
		}
		if m.chord.op == chordY {
			m.clearChordState()
			return m.copyContentViewerAll()
		}
		m.clearChordState()
		return m.startChordOp(chordY)
	case "j", "down":
		m.scrollContentViewer(1)
	case "k", "up":
		m.scrollContentViewer(-1)
	case "ctrl+f", "pgdown":
		m.scrollContentViewer(max(1, m.viewport.height-3))
	case "ctrl+b", "pgup":
		m.scrollContentViewer(-max(1, m.viewport.height-3))
	case "g", "home":
		m.contentViewer.scrollOffset = 0
	case "G", "end":
		m.contentViewer.scrollOffset = m.contentViewerMaxScroll(m.viewport.width)
	}
	m.clampContentViewerScroll(m.viewport.width)
	return nil
}

func (m *Model) renderContentViewer() string {
	width := m.viewport.width
	height := m.viewport.height
	if width <= 0 || height <= 0 {
		return ""
	}
	offset := m.contentViewer.scrollOffset
	if offset < 0 {
		offset = 0
	}
	lines := m.cachedContentViewerLines(width)
	if offset > len(lines) {
		offset = len(lines)
	}
	if m.contentViewer.renderCacheText != "" &&
		m.contentViewer.renderCacheWidth == width &&
		m.contentViewer.renderCacheHeight == height &&
		m.contentViewer.renderCacheOffset == offset &&
		m.contentViewer.renderCacheTheme == m.theme.Name {
		return m.contentViewer.renderCacheText
	}

	visibleHeight := contentViewerBodyHeight(height)
	visible := lines[offset:]
	if len(visible) > visibleHeight {
		visible = visible[:visibleHeight]
	}
	for len(visible) < visibleHeight {
		visible = append(visible, "")
	}

	marginX := contentViewerHorizontalMargin(width)
	marginY := contentViewerVerticalMargin(height)
	innerWidth := contentViewerInnerWidth(width)
	blank := strings.Repeat(" ", width)
	rendered := make([]string, 0, height)
	for range marginY {
		rendered = append(rendered, blank)
	}
	for i, line := range visible {
		lineIdx := offset + i
		if colStart, colEnd, ok := m.contentViewerSelectionColRange(lineIdx, line); ok {
			line = applyHighlightToLine(line, colStart, colEnd)
		}
		line = ansi.Truncate(line, innerWidth, "…")
		line = padLineToDisplayWidth(line, innerWidth)
		if marginX > 0 && innerWidth < width {
			line = strings.Repeat(" ", marginX) + line
			line = padLineToDisplayWidth(line, width)
		}
		rendered = append(rendered, line)
	}
	for len(rendered) < height {
		rendered = append(rendered, blank)
	}
	out := strings.Join(rendered, "\n")
	m.contentViewer.renderCacheWidth = width
	m.contentViewer.renderCacheHeight = height
	m.contentViewer.renderCacheOffset = offset
	m.contentViewer.renderCacheTheme = m.theme.Name
	m.contentViewer.renderCacheText = out
	return out
}
