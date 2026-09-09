package tui

import (
	"fmt"

	tea "github.com/keakon/bubbletea/v2"

	agentrt "github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
)

func (m *Model) setFocusedAgent(id string) {
	prev := m.focusedAgentID
	if prev != id {
		m.cancelClipboardAttachmentPaste()
		m.saveComposerStateForAgent(prev)
		m.clearRunningModelDisplay("")
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
		if prev != id && m.agent != nil {
			m.rebuildFocusedViewport(id, viewportFilter)
		} else {
			m.viewport.SetFilter(viewportFilter)
		}
		m.viewport.ScrollToBottom()
	}
	m.recalcViewportSize()
	m.invalidateDrawCaches()
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
	blocks = mergeFocusedViewportLiveBlocks(blocks, currentBlocks)
	m.setTranscriptDisplaySequences(blocks, agentID)
	blocks = m.maybeWindowStartupTranscript("focus_switch", blocks)
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

// mergeFocusedViewportLiveBlocks carries live blocks across a focus-switch
// rebuild. Settled transcript cards are re-derived from the focused agent's
// messages; live cards exist only in the viewport and are referenced directly
// — assistant/thinking stream state holds their block pointers and tool
// updates look cards up by tool ID — so dropping them, or cloning them under
// fresh IDs, would orphan those references and freeze whatever copy the view
// keeps. Live cards therefore survive the rebuild in place, no matter which
// agent they belong to: the viewport filter hides other agents' blocks, and a
// stream that was in flight when the user switched away is still the same
// block, still receiving its deltas, when the user switches back. A live tool
// call whose call row is already part of the rebuilt base — the response
// committed while the call was still running — folds its runtime state into
// that row's block instead of duplicating the card.
func mergeFocusedViewportLiveBlocks(base, current []*Block) []*Block {
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
		if block == nil {
			continue
		}
		if block.Type == BlockToolCall && !block.ResultDone && block.ToolID != "" {
			if existing, ok := baseToolBlocks[block.ToolID]; ok {
				mergeFocusedToolBlockRuntimeState(existing, block)
				continue
			}
		}
		if !isFocusedViewportLiveBlock(block) {
			continue
		}
		base = append(base, block)
	}
	return base
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
	dst.RawArgs = src.RawArgs
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
	dst.ToolCallDetailExpanded = src.ToolCallDetailExpanded
	dst.Collapsed = src.Collapsed
	dst.InvalidateCache()
}

// handleSwitchRole cycles the main agent's role and reports the switch.
//
// A role change rebuilds the permission ruleset, marks the prompt and tool
// surface dirty (one prompt-cache miss), may apply a different model, and is
// persisted to the recovery snapshot right away — so an accidental press has
// to be visible rather than only showing up as a changed sidebar label.
func (m *Model) handleSwitchRole() tea.Cmd {
	if m.agent == nil {
		return nil
	}
	roles := m.agent.AvailableRoles()
	if len(roles) == 0 {
		return nil
	}
	current := m.agent.CurrentRole()
	nextIdx := 0
	for i, r := range roles {
		if r == current {
			nextIdx = (i + 1) % len(roles)
			break
		}
	}
	next := roles[nextIdx]
	// A single configured role cycles back to itself. Switching would still pay
	// for a ruleset rebuild and a snapshot write, so stop before the no-op.
	if next == current {
		return nil
	}
	m.agent.SwitchRole(next)
	m.invalidateDrawCaches()
	return m.enqueueToast(fmt.Sprintf("role: %s → %s", current, next), "info")
}

func (m *Model) maybeSwitchToTaskAgent(block *Block) {
	if block == nil || block.Type != BlockToolCall || block.ToolName != tools.NameDelegate || block.LinkedAgentID == "" {
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
	ids := m.sidebar.AgentIDs()
	current := m.focusedAgentID
	if current == "" {
		current = "main"
	}
	if len(ids) == 1 && current == "main" {
		return nil
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
