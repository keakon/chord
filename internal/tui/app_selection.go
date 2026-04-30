package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/keakon/chord/internal/convformat"
)

func isSelectableBlockType(t BlockType) bool {
	switch t {
	case BlockError:
		return false
	default:
		return true
	}
}

func isCopyableBlockType(t BlockType) bool {
	return isSelectableBlockType(t)
}

func normalizeFocusedBlockID(blocks []*Block, currentID int) int {
	if len(blocks) == 0 {
		return -1
	}
	idx := -1
	for i, b := range blocks {
		if b != nil && b.ID == currentID {
			idx = i
			break
		}
	}
	if idx >= 0 && blocks[idx] != nil && isSelectableBlockType(blocks[idx].Type) {
		return currentID
	}
	for _, b := range blocks {
		if b != nil && isSelectableBlockType(b.Type) {
			return b.ID
		}
	}
	return -1
}

func focusNextSelectableBlockID(blocks []*Block, currentID, dir int) int {
	if len(blocks) == 0 {
		return -1
	}
	if dir == 0 {
		dir = 1
	}
	currentIdx := -1
	for i, b := range blocks {
		if b != nil && b.ID == currentID {
			currentIdx = i
			break
		}
	}
	start := -1
	if currentIdx >= 0 {
		start = currentIdx + dir
	} else {
		if dir > 0 {
			start = 0
		} else {
			start = len(blocks) - 1
		}
	}
	if start < 0 {
		start = 0
	}
	if start >= len(blocks) {
		start = len(blocks) - 1
	}
	for i := start; i >= 0 && i < len(blocks); i += dir {
		b := blocks[i]
		if b == nil || !isSelectableBlockType(b.Type) {
			continue
		}
		return b.ID
	}
	return -1
}

func indexOfBlockID(blocks []*Block, id int) int {
	for i, b := range blocks {
		if b != nil && b.ID == id {
			return i
		}
	}
	return -1
}

func (m *Model) navigateFocusedBlock(dir int) {
	blocks := m.viewport.visibleBlocks()
	if len(blocks) == 0 {
		return
	}
	nextID := focusNextSelectableBlockID(blocks, m.focusedBlockID, dir)
	if nextID < 0 {
		m.focusedBlockID = -1
		m.refreshBlockFocus()
		return
	}
	m.focusedBlockID = nextID
	m.refreshBlockFocus()
	if m.hasDeferredStartupTranscript() {
		if lineOffset, ok := m.viewport.LineOffsetForBlockID(m.focusedBlockID); ok {
			m.viewport.offset = lineOffset
			m.viewport.clampOffset()
			return
		}
	}
	idx := indexOfBlockID(blocks, m.focusedBlockID)
	if idx < 0 {
		return
	}
	entries := m.viewport.MessageDirectory()
	for _, entry := range entries {
		if entry.BlockIndex == idx {
			m.viewport.offset = entry.LineOffset
			m.viewport.clampOffset()
			break
		}
	}
}

func (m *Model) revalidateFocusedBlock() {
	if m == nil || m.viewport == nil {
		return
	}
	if m.focusedBlockID < 0 {
		m.refreshBlockFocus()
		return
	}
	if m.focusedBlockID >= 0 {
		if block := m.viewport.GetFocusedBlock(m.focusedBlockID); block != nil && isSelectableBlockType(block.Type) {
			m.refreshBlockFocus()
			return
		}
	}
	m.focusedBlockID = normalizeFocusedBlockID(m.viewport.visibleBlocks(), m.focusedBlockID)
	m.refreshBlockFocus()
}

func (m *Model) firstVisibleCompactionSummaryBlock() *Block {
	if m == nil || m.viewport == nil {
		return nil
	}
	blocks := m.viewport.visibleBlocks()
	if len(blocks) == 0 {
		return nil
	}
	starts := m.viewport.blockStarts()
	windowStart := m.viewport.offset
	windowEnd := windowStart + m.viewport.height
	if m.viewport.height <= 0 {
		windowEnd = windowStart + 1
	}
	for i, block := range blocks {
		if block == nil || block.Type != BlockCompactionSummary {
			continue
		}
		if i >= len(starts) {
			break
		}
		blockStart := starts[i]
		blockEnd := blockStart + m.viewport.blockSpanAt(blocks, i, block)
		if blockEnd > windowStart && blockStart < windowEnd {
			return block
		}
	}
	return nil
}

func (m *Model) maybeFocusVisibleCompactionSummary(force bool) {
	if m == nil || m.viewport == nil {
		return
	}
	if !force && m.focusedBlockID >= 0 {
		return
	}
	block := m.firstVisibleCompactionSummaryBlock()
	if block == nil {
		return
	}
	if !isSelectableBlockType(block.Type) {
		return
	}
	m.focusedBlockID = block.ID
	m.refreshBlockFocus()
}

// viewportResolveMouse maps viewport-relative (line, col) to the block and
// line index within that block. Returns (nil, -1) if out of range.
func (m *Model) viewportResolveMouse(viewportLine int) (*Block, int) {
	globalLine := m.viewport.offset + viewportLine
	return m.viewport.GetBlockAndLineAt(globalLine)
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func clampCol(col, maxWidth int) int {
	if col < 0 {
		return 0
	}
	if maxWidth <= 0 {
		return col
	}
	if col > maxWidth {
		return maxWidth
	}
	return col
}

func (m *Model) hasMouseSelection() bool {
	return m.selStartBlockID >= 0 && m.selEndBlockID >= 0
}

func (m *Model) mouseSelectionRange() SelectionRange {
	r := SelectionRange{
		StartBlockID: m.selStartBlockID,
		StartLine:    m.selStartLine,
		StartCol:     m.selStartCol,
		EndBlockID:   m.selEndBlockID,
		EndLine:      m.selEndLine,
		EndCol:       m.selEndCol,
	}
	if !m.selEndInclusiveForCopy {
		return r
	}
	if posLess(r.StartBlockID, r.StartLine, r.StartCol, r.EndBlockID, r.EndLine, r.EndCol) {
		r.EndCol++
		return r
	}
	if posLess(r.EndBlockID, r.EndLine, r.EndCol, r.StartBlockID, r.StartLine, r.StartCol) {
		r.StartCol++
	}
	return r
}

func (m *Model) clearMouseSelection() {
	m.mouseDown = false
	m.selStartBlockID = -1
	m.selStartLine = -1
	m.selStartCol = -1
	m.selEndBlockID = -1
	m.selEndLine = -1
	m.selEndCol = -1
	m.selEndInclusiveForCopy = false
}

func (m *Model) statusPathContainsPoint(x, y int) bool {
	return m.layout.status.Dy() > 0 &&
		y >= m.layout.status.Min.Y &&
		y < m.layout.status.Max.Y &&
		m.statusPath.display != "" &&
		x >= m.statusPath.startX &&
		x < m.statusPath.endX
}

func (m *Model) statusSessionContainsPoint(x, y int) bool {
	return m.layout.status.Dy() > 0 &&
		y >= m.layout.status.Min.Y &&
		y < m.layout.status.Max.Y &&
		m.statusSession.display != "" &&
		x >= m.statusSession.startX &&
		x < m.statusSession.endX
}

// viewportSelectionPtr returns a pointer to the current selection range for
// viewport rendering (highlight), or nil if not selecting.
func (m *Model) viewportSelectionPtr() *SelectionRange {
	if m.selStartBlockID < 0 || m.selEndBlockID < 0 {
		return nil
	}
	r := m.mouseSelectionRange()
	return &r
}

// setFocusedBlockFromViewport sets focusedBlockID to the block at the current
// viewport offset (e.g. after { / }) and refreshes block Focused state.
func (m *Model) setFocusedBlockFromViewport() {
	blocks := m.viewport.visibleBlocks()
	if len(blocks) == 0 {
		return
	}
	b := m.viewport.GetBlockAtOffset()
	if b == nil {
		return
	}
	idx := indexOfBlockID(blocks, b.ID)
	if idx < 0 {
		return
	}
	for i := idx; i < len(blocks); i++ {
		candidate := blocks[i]
		if candidate == nil || !isSelectableBlockType(candidate.Type) {
			continue
		}
		m.focusedBlockID = candidate.ID
		m.refreshBlockFocus()
		return
	}
	for i := idx - 1; i >= 0; i-- {
		candidate := blocks[i]
		if candidate == nil || !isSelectableBlockType(candidate.Type) {
			continue
		}
		m.focusedBlockID = candidate.ID
		m.refreshBlockFocus()
		return
	}
	m.focusedBlockID = -1
	m.refreshBlockFocus()
}

// refreshBlockFocus updates each block's Focused flag to match focusedBlockID.
func (m *Model) refreshBlockFocus() {
	for _, block := range m.viewport.blocks {
		newFocused := block.ID == m.focusedBlockID
		if block.Focused != newFocused {
			m.recordTUIDiagnostic("focus-block", "block=%d type=%s focused=%t->%t", block.ID, debugBlockTypeString(block.Type), block.Focused, newFocused)
			block.Focused = newFocused
			block.InvalidateCache()
		}
	}
}

func (m *Model) clearFocusedBlock() {
	if m.focusedBlockID < 0 {
		return
	}
	m.focusedBlockID = -1
	m.refreshBlockFocus()
}

func (m *Model) jumpToVisibleBlockOrdinal(ordinal int) tea.Cmd {
	prevOffset := m.viewport.offset
	if m.hasDeferredStartupTranscript() {
		if m.maybeJumpDeferredStartupTranscriptOrdinal(ordinal, "jump_ordinal") {
			return m.refreshInlineImagesIfViewportMoved(prevOffset)
		}
	}
	entries := m.viewport.MessageDirectory()
	if len(entries) == 0 {
		m.clearFocusedBlock()
		return nil
	}
	if ordinal < 1 {
		ordinal = 1
	}
	if ordinal > len(entries) {
		ordinal = len(entries)
	}
	m.clearFocusedBlock()
	m.viewport.offset = entries[ordinal-1].LineOffset
	m.viewport.clampOffset()
	m.viewport.sticky = m.viewport.atBottom()
	return m.refreshInlineImagesIfViewportMoved(prevOffset)
}

// copyFocusedBlock copies the focused block to clipboard and returns the toast tick cmd.
func (m *Model) copyFocusedBlock() tea.Cmd {
	if m.focusedBlockID < 0 {
		return m.enqueueToast("Select a message card, then y to copy", "info")
	}
	blocks := m.viewport.visibleBlocks()
	for _, b := range blocks {
		if b.ID != m.focusedBlockID {
			continue
		}
		b = m.viewport.GetFocusedBlock(b.ID)
		if b == nil {
			return nil
		}
		if !isCopyableBlockType(b.Type) {
			return m.enqueueToast("This card type cannot be copied", "info")
		}
		if b.IsUserLocalShell() {
			body := userLocalShellCopyBody(b)
			return writeClipboardCmd(convformat.BlockString(convformat.LabelUser, body), "Message card copied to clipboard")
		}
		return writeClipboardCmd(blockPlainContent(b), "Message card copied to clipboard")
	}
	return nil
}

// copyFocusedBlocks copies count blocks starting from the focused block to clipboard.
func (m *Model) copyFocusedBlocks(count int) tea.Cmd {
	if m.focusedBlockID < 0 {
		return m.enqueueToast("Select a message card, then y to copy", "info")
	}
	blocks := m.viewport.visibleBlocks()
	startIdx := -1
	for i, b := range blocks {
		if b.ID == m.focusedBlockID {
			startIdx = i
			break
		}
	}
	if startIdx < 0 {
		return nil
	}
	endIdx := min(startIdx+count, len(blocks))
	var parts []string
	multiBlock := countCopyableFocusedBlocks(blocks, startIdx, endIdx) > 1
	copied := 0
	for i := startIdx; i < endIdx; i++ {
		b := m.viewport.GetFocusedBlock(blocks[i].ID)
		if b == nil || !isCopyableBlockType(b.Type) {
			continue
		}
		c := blockPlainContent(b)
		if c == "" {
			continue
		}
		copied++
		if multiBlock {
			label := blockCopyLabel(b)
			if b.Type == BlockThinking && strings.HasPrefix(c, "▸ thinking\n\n") {
				c = strings.TrimPrefix(c, "▸ thinking\n\n")
			}
			parts = append(parts, convformat.BlockString(label, c))
		} else {
			parts = append(parts, c)
		}
	}
	if len(parts) == 0 {
		return m.enqueueToast("This card type cannot be copied", "info")
	}
	if copied == 1 {
		return writeClipboardCmd(parts[0], "Message card copied to clipboard")
	}
	msg := fmt.Sprintf("%d message cards copied to clipboard", copied)
	return writeClipboardCmd(convformat.JoinBlocks(parts), msg)
}

func countCopyableFocusedBlocks(blocks []*Block, startIdx, endIdx int) int {
	count := 0
	for i := startIdx; i < endIdx; i++ {
		if i < 0 || i >= len(blocks) {
			continue
		}
		b := blocks[i]
		if b == nil || !isCopyableBlockType(b.Type) {
			continue
		}
		count++
	}
	return count
}

func blockCopyLabel(b *Block) string {
	switch b.Type {
	case BlockUser:
		return convformat.LabelUser
	case BlockAssistant:
		return convformat.LabelAssistant
	case BlockThinking:
		return convformat.LabelThinking
	case BlockToolCall:
		return convformat.ToolCallLabel(b.ToolName)
	case BlockBoundaryMarker:
		return convformat.LabelBlock
	default:
		return convformat.LabelBlock
	}
}

func blockPlainContent(b *Block) string {
	switch b.Type {
	case BlockThinking:
		return "▸ thinking\n\n" + strings.TrimSpace(b.Content)
	case BlockCompactionSummary:
		raw := strings.TrimSpace(b.CompactionSummaryRaw)
		if raw != "" {
			return raw
		}
		return strings.TrimSpace(b.Content)
	default:
		if b.Type == BlockUser && b.UserLocalShellCmd != "" {
			return userLocalShellCopyBody(b)
		}
		if b.Type == BlockToolCall && b.ToolName == "Skill" {
			return skillToolCopyContent(b.Content, b.ResultContent)
		}
		content := strings.TrimSpace(b.Content)
		if b.Type == BlockUser && len(b.ImageParts) > 0 {
			var imageLabels []string
			for _, part := range b.ImageParts {
				name := strings.TrimSpace(part.FileName)
				if name == "" {
					name = "image"
				}
				imageLabels = append(imageLabels, fmt.Sprintf("[image: %s]", name))
			}
			imageText := strings.Join(imageLabels, "\n")
			if content == "" {
				content = imageText
			} else {
				content += "\n\n" + imageText
			}
		}
		if b.ResultContent != "" {
			content += "\n\nResult:\n" + b.ResultContent
		}
		if b.Diff != "" {
			content += "\n\nDiff:\n" + b.Diff
		}
		return content
	}
}

func (m *Model) handleSuperCopy() tea.Cmd {
	if m.hasMouseSelection() {
		text := m.viewport.ExtractSelectionText(m.mouseSelectionRange())
		if text != "" {
			return writeClipboardCmd(text, "Selection copied to clipboard")
		}
	}
	if text := m.input.SelectionText(); text != "" {
		return writeClipboardCmd(text, "Selection copied to clipboard")
	}
	if m.focusedBlockID >= 0 {
		for _, b := range m.viewport.visibleBlocks() {
			if b.ID == m.focusedBlockID {
				if !isCopyableBlockType(b.Type) {
					return m.enqueueToast("This card type cannot be copied", "info")
				}
				return writeClipboardCmd(blockPlainContent(b), "Message card copied to clipboard")
			}
		}
	}
	if v := m.input.Value(); v != "" {
		return writeClipboardCmd(v, "Input copied to clipboard")
	}
	return nil
}
