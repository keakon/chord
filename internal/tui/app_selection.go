package tui

import (
	"fmt"
	"strings"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/convformat"
	"github.com/keakon/chord/internal/tools"
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
	return isSelectableBlockType(t) || t == BlockError
}

func isFocusableBlockType(t BlockType) bool {
	return isCopyableBlockType(t)
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
	if idx >= 0 && blocks[idx] != nil && isFocusableBlockType(blocks[idx].Type) {
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
		if block := m.viewport.GetFocusedBlock(m.focusedBlockID); block != nil && isFocusableBlockType(block.Type) {
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
	m.setFocusedBlockFromViewportByType(isSelectableBlockType)
}

func (m *Model) setCopyFocusedBlockFromViewport() {
	m.setFocusedBlockFromViewportByType(isCopyableBlockType)
}

func (m *Model) setFocusedBlockFromViewportByType(allowed func(BlockType) bool) {
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
		if candidate == nil || !allowed(candidate.Type) {
			continue
		}
		m.focusedBlockID = candidate.ID
		m.refreshBlockFocus()
		return
	}
	for i := idx - 1; i >= 0; i-- {
		candidate := blocks[i]
		if candidate == nil || !allowed(candidate.Type) {
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

func (m *Model) focusDirectoryEntryBlock(entries []DirectoryEntry, index, dir int) {
	if len(entries) == 0 {
		m.clearFocusedBlock()
		return
	}
	if index < 0 {
		index = 0
	}
	if index >= len(entries) {
		index = len(entries) - 1
	}
	if dir == 0 {
		dir = 1
	}
	for i := index; i >= 0 && i < len(entries); i += dir {
		entry := entries[i]
		if isSelectableBlockType(entry.Type) {
			m.focusedBlockID = entry.BlockID
			m.refreshBlockFocus()
			return
		}
	}
	for i := index - dir; i >= 0 && i < len(entries); i -= dir {
		entry := entries[i]
		if isSelectableBlockType(entry.Type) {
			m.focusedBlockID = entry.BlockID
			m.refreshBlockFocus()
			return
		}
	}
	m.clearFocusedBlock()
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
	entry := entries[ordinal-1]
	m.viewport.offset = entry.LineOffset
	m.viewport.clampOffset()
	m.focusDirectoryEntryBlock(entries, ordinal-1, 1)
	m.viewport.sticky = m.viewport.atBottom()
	return m.refreshInlineImagesIfViewportMoved(prevOffset)
}

func (m *Model) jumpToLastVisibleBlock() tea.Cmd {
	prevOffset := m.viewport.offset
	if m.hasDeferredStartupTranscript() {
		m.maybeSwitchStartupDeferredTranscriptWindow(startupTranscriptWindowTail, "jump_bottom")
	}
	entries := m.viewport.MessageDirectory()
	if len(entries) == 0 {
		m.clearFocusedBlock()
		return nil
	}
	m.viewport.ScrollToBottom()
	m.focusDirectoryEntryBlock(entries, len(entries)-1, -1)
	m.viewport.ScrollToBottom()
	m.viewport.sticky = true
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
		return writeClipboardCmd(blockCopyContent(b), "Message card copied to clipboard")
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
	copied := 0
	for i := startIdx; i < endIdx; i++ {
		b := m.viewport.GetFocusedBlock(blocks[i].ID)
		if b == nil || !isCopyableBlockType(b.Type) {
			continue
		}
		c := blockCopyContent(b)
		if c == "" {
			continue
		}
		copied++
		parts = append(parts, c)
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
func blockPlainContent(b *Block) string {
	switch b.Type {
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
		content := appendImagePlaceholderLabels(strings.TrimSpace(b.Content), b)
		if b.ResultContent != "" {
			content += "\n\nResult:\n" + b.ResultContent
		}
		if b.Diff != "" {
			content += "\n\nDiff:\n" + b.Diff
		}
		return content
	}
}

// appendImagePlaceholderLabels keeps copied cards loss-free about attached
// images: rendered previews cannot survive plain-text clipboard, so each image
// part is represented by its stable [image: name] placeholder.
func appendImagePlaceholderLabels(content string, b *Block) string {
	if b == nil || len(b.ImageParts) == 0 || !blockSupportsImagePreview(b) {
		return content
	}
	labels := make([]string, 0, len(b.ImageParts))
	for _, part := range b.ImageParts {
		name := strings.TrimSpace(part.FileName)
		if name == "" {
			name = "image"
		}
		labels = append(labels, fmt.Sprintf("[image: %s]", name))
	}
	imageText := strings.Join(labels, "\n")
	if strings.TrimSpace(content) == "" {
		return imageText
	}
	return content + "\n\n" + imageText
}

func blockCopyContent(b *Block) string {
	if b == nil {
		return ""
	}
	if b.IsUserLocalShell() {
		return convformat.BlockString(convformat.LabelLocalShell, userLocalShellCopyBody(b))
	}
	switch b.Type {
	case BlockUser:
		return convformat.BlockString(convformat.LabelUser, blockPlainContent(b))
	case BlockAssistant:
		return convformat.BlockString(convformat.LabelAssistant, blockPlainContent(b))
	case BlockThinking:
		return convformat.BlockString(convformat.LabelThinking, strings.TrimSpace(b.Content))
	case BlockToolCall:
		if tools.NormalizeName(b.ToolName) == tools.NameSkill {
			return skillToolCopyContent(b.Content, b.ResultContent)
		}
		return appendImagePlaceholderLabels(toolCallMarkdownContent(b), b)
	case BlockToolResult:
		content := convformat.ToolResultMarkdown(b.ToolName, toolExpandedResultContent(b.ToolName, b.toolResultContentForCopy()), b.Diff)
		return appendImagePlaceholderLabels(content, b)
	case BlockError:
		return convformat.BlockString(convformat.LabelError, strings.TrimSpace(b.Content))
	case BlockBoundaryMarker:
		return convformat.BlockString(convformat.LabelBoundary, strings.TrimSpace(b.Content))
	case BlockStatus:
		if b.BackgroundCopyContent != "" {
			return b.BackgroundCopyContent
		}
		if title := strings.TrimSpace(b.StatusTitle); title != "" {
			return convformat.BlockString(title+":", blockPlainContent(b))
		}
	}
	return blockPlainContent(b)
}

func (b *Block) toolResultContentForCopy() string {
	if b == nil {
		return ""
	}
	if strings.TrimSpace(b.ResultContent) != "" {
		return b.ResultContent
	}
	return b.Content
}

func toolCallMarkdownContent(b *Block) string {
	if b == nil {
		return ""
	}
	toolName := strings.TrimSpace(b.ToolName)
	if toolName == "" {
		toolName = "unknown"
	}
	if toolNameKey(toolName) == tools.NameDone {
		return convformat.DoneToolCallMarkdown(b.DoneReport, b.ResultContent)
	}
	if toolNameKey(toolName) == tools.NameEdit || toolNameKey(toolName) == tools.NameApplyPatch {
		return fileDiffToolCallMarkdownContent(b)
	}

	return convformat.ToolCallMarkdown(b.ToolName, b.Content, toolExpandedResultContent(b.ToolName, b.ResultContent), b.Diff)
}

func fileDiffToolCallMarkdownContent(b *Block) string {
	toolName := toolNameKey(b.ToolName)
	if toolName != tools.NameEdit && toolName != tools.NameApplyPatch {
		toolName = tools.NameEdit
	}
	parts := []string{"# Tool call: " + toolName}
	if toolName != tools.NameApplyPatch {
		if path := strings.TrimSpace(b.diffToolFilePath()); path != "" {
			parts = append(parts, "## Path\n\n"+path)
		}
	}
	argsJSON := b.editPatchArgsJSON()
	// The edit tool changes text via exact old_string/new_string replacements,
	// which read more clearly than a unified diff when the card is shared.
	showReplace := false
	if toolName == tools.NameEdit {
		if args, ok := parseReplaceEditArgs(argsJSON); ok {
			parts = append(parts, markdownFencedSection("old_string", args.OldString))
			parts = append(parts, markdownFencedSection("new_string", args.NewString))
			if args.ReplaceAll != nil && *args.ReplaceAll {
				parts = append(parts, "## replace_all\n\ntrue")
			}
			showReplace = true
		}
	}
	diff := strings.TrimSpace(b.Diff)
	applyPatchNoChanges := b.ToolName == tools.NameApplyPatch && b.ResultDone && !b.toolResultIsError() && !b.toolResultIsCancelled() && strings.Contains(b.ResultContent, "No net file changes")
	if !showReplace {
		if diff == "" && !applyPatchNoChanges {
			diff = editPatchFromArgs(argsJSON)
		}
		if diff == "" && applyPatchNoChanges {
			diff = "No changes"
		}
		if diff != "" {
			parts = append(parts, "## Diff\n\n```diff\n"+diff+"\n```")
		}
	}
	if result := strings.TrimSpace(toolExpandedResultContent(b.ToolName, b.ResultContent)); result != "" {
		parts = append(parts, "## Result\n\n"+result)
	}
	return strings.Join(parts, "\n\n")
}

// markdownFencedSection fences a copied code payload. old_string and new_string
// are matched byte for byte by the edit tool, so leading whitespace and lines
// beginning with #, -, or > must survive a paste into a Markdown reader intact.
// The fence grows past the longest backtick run in the payload so content that
// itself contains a fence cannot close the section early.
func markdownFencedSection(title, code string) string {
	fence := strings.Repeat("`", max(3, longestBacktickRun(code)+1))
	if !strings.HasSuffix(code, "\n") {
		code += "\n"
	}
	return "## " + title + "\n\n" + fence + "\n" + code + fence
}

func longestBacktickRun(s string) int {
	longest, current := 0, 0
	for _, r := range s {
		if r != '`' {
			current = 0
			continue
		}
		current++
		longest = max(longest, current)
	}
	return longest
}

func (m *Model) handleSuperCopy() tea.Cmd {
	if m.mode == ModeContentViewer {
		if m.contentViewerHasSelection() {
			return m.copyContentViewerSelection()
		}
		return m.copyContentViewerAll()
	}
	// In confirm sub-modes, the focused textarea isn't m.input, so copy from it.
	if m.mode == ModeConfirm {
		if input, label, ok := m.activeConfirmTextarea(); ok {
			if v := input.Value(); v != "" {
				return writeClipboardCmd(v, label+" copied to clipboard")
			}
			return nil
		}
	}

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
				return writeClipboardCmd(blockCopyContent(b), "Message card copied to clipboard")
			}
		}
	}
	if v := m.input.Value(); v != "" {
		return writeClipboardCmd(v, "Input copied to clipboard")
	}
	return nil
}
