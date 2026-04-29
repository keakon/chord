package tui

import (
	tea "charm.land/bubbletea/v2"

	agentrt "github.com/keakon/chord/internal/agent"
)

func (m *Model) setFocusedAgent(id string) {
	prev := m.focusedAgentID
	if prev != id {
		m.saveComposerStateForAgent(prev)
	}
	m.focusedAgentID = id
	if prev != id {
		m.restoreComposerStateForAgent(id)
	}
	m.sidebar.focusedID = id
	viewportFilter := id
	if id == "" {
		viewportFilter = "main"
	}
	if m.agent != nil {
		m.agent.SwitchFocus(id)
	}
	if m.viewport != nil {
		if m.shouldRebuildFocusedViewport(prev, id) {
			m.rebuildFocusedViewport(id, viewportFilter)
		} else {
			m.viewport.SetFilter(viewportFilter)
		}
		m.viewport.ScrollToBottom()
	}
	m.recalcViewportSize()
	m.invalidateDrawCaches()
}

func (m *Model) shouldRebuildFocusedViewport(prevID, nextID string) bool {
	if m == nil || m.viewport == nil || m.agent == nil || prevID == nextID {
		return false
	}
	if m.startupDeferredTranscript != nil {
		return true
	}
	return !m.viewport.HasBlocksForAgent(nextID)
}

func (m *Model) rebuildFocusedViewport(agentID, viewportFilter string) {
	if m == nil || m.viewport == nil || m.agent == nil {
		return
	}
	currentBlocks := append([]*Block(nil), m.viewport.blocks...)
	if state := m.startupDeferredTranscript; state != nil {
		m.restoreStartupDeferredTranscriptRetention(state)
		m.logStartupDeferredTranscriptExit(state, "focus_switch", viewportFilter)
		m.startupDeferredTranscript = nil
		m.startupDeferredPreheatGeneration++
	}
	msgs := m.agent.GetMessages()
	var nextID int
	blocks := messagesToBlocks(msgs, &nextID)
	for _, block := range blocks {
		if block != nil {
			block.displayWorkingDir = m.workingDir
		}
	}
	clearBlocksTiming(blocks)
	assignFocusedViewportBlockIDs(blocks, agentID, &m.nextBlockID)
	blocks = mergeFocusedViewportLiveBlocks(blocks, currentBlocks, agentID, &m.nextBlockID)
	m.viewport.SetFilter(viewportFilter)
	m.viewport.SetWorkingDir(m.workingDir)
	m.viewport.ReplaceBlocks(blocks)
	m.revalidateFocusedBlock()
	m.recalcViewportSize()
	if agentID == "" {
		m.syncVisibleMainUserBlockMsgIndexes()
		m.maybeFocusVisibleCompactionSummary(false)
	}
}

func assignFocusedViewportBlockIDs(blocks []*Block, agentID string, nextID *int) {
	for _, block := range blocks {
		if block == nil {
			continue
		}
		block.AgentID = agentID
		block.ID = *nextID
		*nextID++
	}
}

func mergeFocusedViewportLiveBlocks(base, current []*Block, agentID string, nextID *int) []*Block {
	if len(current) == 0 {
		return base
	}
	baseToolBlocks := make(map[string]*Block, len(base))
	for _, block := range base {
		if block != nil && block.ToolID != "" {
			baseToolBlocks[block.ToolID] = block
		}
	}
	for _, block := range current {
		if !blockBelongsToFocusedAgent(block, agentID) {
			continue
		}
		if block.Type == BlockToolCall && !block.ResultDone {
			if existing, ok := baseToolBlocks[block.ToolID]; ok && block.ToolID != "" {
				mergeFocusedToolBlockRuntimeState(existing, block)
				continue
			}
		}
		if !isFocusedViewportLiveBlock(block) {
			continue
		}
		clone := cloneBlockForDeferredSource(block)
		clone.AgentID = agentID
		clone.ID = *nextID
		*nextID++
		base = append(base, clone)
	}
	return base
}

func blockBelongsToFocusedAgent(block *Block, agentID string) bool {
	if block == nil {
		return false
	}
	if agentID == "" {
		return block.AgentID == ""
	}
	return block.AgentID == agentID
}

func isFocusedViewportLiveBlock(block *Block) bool {
	if block == nil {
		return false
	}
	if block.Type == BlockToolCall && !block.ResultDone {
		return true
	}
	if block.Streaming {
		return true
	}
	return block.Type == BlockUser && block.UserLocalShellPending
}

func mergeFocusedToolBlockRuntimeState(dst, src *Block) {
	if dst == nil || src == nil {
		return
	}
	dst.Content = src.Content
	dst.Streaming = src.Streaming
	dst.ToolExecutionState = src.ToolExecutionState
	if src.ToolProgress != nil {
		progress := *src.ToolProgress
		dst.ToolProgress = &progress
	} else {
		dst.ToolProgress = nil
	}
	dst.Audit = src.Audit.Clone()
	if !src.StartedAt.IsZero() {
		dst.StartedAt = src.StartedAt
	}
	dst.ReadContentExpanded = src.ReadContentExpanded
	dst.ToolCallDetailExpanded = src.ToolCallDetailExpanded
	dst.Collapsed = src.Collapsed
	dst.InvalidateCache()
}

func (m *Model) handleSwitchRole() {
	if m.agent == nil {
		return
	}
	roles := m.agent.AvailableRoles()
	if len(roles) == 0 {
		return
	}
	current := m.agent.CurrentRole()
	nextIdx := 0
	for i, r := range roles {
		if r == current {
			nextIdx = (i + 1) % len(roles)
			break
		}
	}
	m.agent.SwitchRole(roles[nextIdx])
	m.invalidateDrawCaches()
}

func (m *Model) isViewingReadOnlySubAgent() bool {
	if m.focusedAgentID == "" {
		return false
	}
	status, ok := m.sidebar.FindStatus(m.focusedAgentID)
	if !ok {
		return false
	}
	if status == "error" || status == "cancelled" {
		return true
	}
	if m.agent != nil && m.agent.FocusedAgentID() != m.focusedAgentID {
		return true
	}
	return false
}

func (m *Model) maybeSwitchToTaskAgent(block *Block) {
	if block == nil || block.Type != BlockToolCall || block.ToolName != "Delegate" || block.LinkedAgentID == "" {
		return
	}
	m.setFocusedAgent(block.LinkedAgentID)
	m.recalcViewportSize()
	m.viewport.ScrollToBottom()
}

func (m *Model) refreshSidebar() {
	if m.agent == nil {
		return
	}
	subAgents := m.agent.GetSubAgents()
	focusedID := m.focusedAgentID
	if focusedID == "" {
		focusedID = "main"
	}
	mainRole := m.agent.CurrentRole()
	m.sidebar.Update(subAgents, focusedID, mainRole)
	if local, ok := m.agent.(*agentrt.MainAgent); ok {
		if cfg := local.CurrentRoleConfig(); cfg != nil {
			for i := range m.sidebar.agents {
				if m.sidebar.agents[i].ID == "main" {
					m.sidebar.agents[i].Color = cfg.Color
					break
				}
			}
		}
	}
	m.invalidateStatusBarAgentSnapshot()
}

func (m *Model) handleSwitchAgent() tea.Cmd {
	allIDs := m.sidebar.AgentIDs()
	ids := make([]string, 0, len(allIDs))
	ids = append(ids, allIDs...)
	if len(ids) <= 1 {
		return nil
	}
	current := m.focusedAgentID
	if current == "" {
		current = "main"
	}
	nextIdx := 0
	for i, id := range ids {
		if id == current {
			nextIdx = (i + 1) % len(ids)
			break
		}
	}
	nextID := ids[nextIdx]
	if nextID == "main" {
		nextID = ""
	}
	m.setFocusedAgent(nextID)
	m.recalcViewportSize()
	m.viewport.ScrollToBottom()
	return m.restartStatusBarTick()
}
