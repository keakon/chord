package tui

import (
	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/tools"
)

// normalActivation keeps missing actions distinct from actions that cannot finish.
func (m *Model) normalActivation() tea.Cmd {
	if m.focusedBlockID >= 0 && (m.viewport.GetFocusedBlock(m.focusedBlockID) == nil || !m.viewport.FocusedBlockIsVisible(m.focusedBlockID)) {
		m.focusedBlockID = -1
		m.refreshBlockFocus()
	}
	block := m.normalActivationBlock()
	if block == nil {
		m.focusedBlockID = -1
		m.refreshBlockFocus()
		block = m.viewport.GetBlockAtOffset()
	}
	if block != nil {
		if block.ToolName == tools.NameDelegate {
			// An unresolved Delegate is still a task action, not an empty Enter target.
			if block.LinkedAgentID != "" {
				m.maybeSwitchToTaskAgent(block)
			}
			return nil
		}
		if part, ok := block.firstImagePart(m.viewport.width); ok && m.imageCaps.SupportsFullscreen {
			return m.openImageViewer(block.ID, part.Index)
		}
		if block.canToggleAtWidth(m.viewport.width) {
			m.viewport.ToggleBlockByID(block.ID)
			return nil
		}
		if block.Streaming {
			return nil
		}
	}
	return nil
}

func (m *Model) normalActivationBlock() *Block {
	var block *Block
	if m.focusedBlockID >= 0 {
		block = m.viewport.GetFocusedBlock(m.focusedBlockID)
		if block != nil && !m.viewport.FocusedBlockIsVisible(m.focusedBlockID) {
			block = nil
		}
	}
	if block == nil {
		block = m.viewport.GetBlockAtOffset()
	}
	return block
}

func (m *Model) nextEnterHint() string {
	if m.mode != ModeNormal || m.chord.active() {
		return ""
	}
	b := m.normalActivationBlock()
	if b != nil {
		if b.Streaming || b.Type == BlockStatus {
			return ""
		}
		if b.ToolName == tools.NameDelegate {
			return "open task"
		}
		if len(b.ImageParts) > 0 && m.imageCaps.SupportsFullscreen {
			return "view image"
		}
		// Tool disclosure can parse or render content; keep it out of status refreshes.
		if b.Type == BlockToolCall || b.Type == BlockToolResult {
			return ""
		}
		if b.canToggleAtWidth(m.viewport.width) {
			if b.Collapsed || b.Type == BlockAssistant && b.ThinkingCollapsed {
				return "expand card"
			}
			return "collapse card"
		}
	}
	return ""
}
