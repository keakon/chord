package tui

import (
	"fmt"
	"strings"

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
	// A focus switch rebuilds the base from the agent's durable transcript and
	// drops settled live cards, so live-only notify cards recorded for this
	// view would vanish; replay the anchors that belong to this view. Base
	// blocks take fresh consecutive sequences after the merge, so the replayed
	// cards slot into the view's label counters.
	blocks = m.replayNotifyAnchorsForView(blocks, msgs, agentID)
	// Number the base rows before merging retained live blocks: the base is
	// one agent's transcript and gets fresh consecutive sequences under that
	// agent's counter, while live blocks kept from another agent's view are
	// numbered under their own agent and must never inflate this view's
	// label counters.
	m.setTranscriptDisplaySequences(blocks, agentID)
	baseCount := len(blocks)
	blocks = m.mergeFocusedViewportLiveBlocks(blocks, currentBlocks)
	m.continueFocusedLiveDisplaySequences(blocks[baseCount:], agentID)
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
// block, still receiving its deltas, when the user switches back.
//
// Two classes of live cards are not carried over:
//   - A live tool call whose call row is already part of the rebuilt base (the
//     response committed while the call was still running) folds its runtime
//     state into that row's block instead of duplicating the card; when the
//     committed row is already complete, the stale live card is dropped so the
//     running state is never copied onto a finished row.
//   - A live subagent thinking/assistant block whose stream finished while the
//     user watched another agent has its committed counterpart among the base
//     rows (subagent deltas and end events are suppressed at the source while
//     unfocused, so the card never settled here). Keeping it would duplicate
//     the committed card and re-retain it on every rebuild, so it is dropped
//     and detached from its stream state.
func (m *Model) mergeFocusedViewportLiveBlocks(base, current []*Block) []*Block {
	if len(current) == 0 {
		return base
	}
	baseToolBlocks := make(map[string]*Block, len(base))
	for _, block := range base {
		if block != nil && block.ToolID != "" {
			baseToolBlocks[block.ToolID] = block
		}
	}
	var droppedStale []*Block
	for _, block := range current {
		if block == nil {
			continue
		}
		if block.Type == BlockToolCall && !block.ResultDone && block.ToolID != "" {
			if existing, ok := baseToolBlocks[block.ToolID]; ok {
				if existing.ResultDone {
					continue
				}
				mergeFocusedToolBlockRuntimeState(existing, block)
				continue
			}
		}
		if !isFocusedViewportLiveBlock(block) {
			continue
		}
		if staleCommittedStreamLiveBlock(base, block) {
			droppedStale = append(droppedStale, block)
			continue
		}
		base = append(base, block)
	}
	m.discardStaleLiveStreamState(droppedStale)
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

// staleCommittedStreamLiveBlock reports whether a live subagent stream block
// already has its committed counterpart among base. Subagent thinking/assistant
// deltas and their end events are suppressed at the agent source while the user
// watches another agent, so a stream that finished during that time never
// reaches its settling event in the TUI and would stay Streaming forever. Its
// committed transcript row is the authoritative card; keeping both would
// duplicate the card and re-retain the never-settling block on every rebuild.
// Main-agent blocks are exempt: main streams are not focus-suppressed, so a
// still-live main card is never a stale duplicate of a committed row.
//
// A stream commits at the tail of its agent's transcript, so only that agent's
// last committed row of the same card type can be the counterpart of the live
// card. Matching every committed row would drop a card whose stream is still
// in flight whenever an older message shares its opening (a follow-up that
// re-states an earlier answer): the card disappears and its stream state is
// detached, so the already-streamed content jumps when deltas resume. The
// content check accepts both shapes of a genuinely committed stream: the
// committed row extending the frozen partial (append-only deltas), and the
// frozen partial overrunning a shorter committed final (the final payload can
// be cleaned before commit — scrubbed markers or regenerated text — so it is
// not always a pure extension of what already streamed).
func staleCommittedStreamLiveBlock(base []*Block, block *Block) bool {
	if block == nil || block.AgentID == "" {
		return false
	}
	if !block.Streaming || (block.Type != BlockThinking && block.Type != BlockAssistant) {
		return false
	}
	liveText := strings.TrimSpace(block.streamAccumulatedContent())
	if liveText == "" {
		return false
	}
	for i := len(base) - 1; i >= 0; i-- {
		row := base[i]
		if row == nil || row == block || row.Type != block.Type || row.AgentID != block.AgentID {
			continue
		}
		committed := strings.TrimSpace(row.Content)
		if committed == "" {
			return false
		}
		return strings.HasPrefix(committed, liveText) || strings.HasPrefix(liveText, committed)
	}
	return false
}

// discardStaleLiveStreamState detaches stream state from live blocks that a
// rebuild dropped because their committed counterpart exists, so later deltas
// or a new thinking round start a fresh card instead of resuming the orphan.
func (m *Model) discardStaleLiveStreamState(stale []*Block) {
	for _, block := range stale {
		if block == nil || block.AgentID == "" {
			continue
		}
		state, ok := m.subAgentStreamStates[block.AgentID]
		if !ok {
			continue
		}
		if state.assistant == block {
			state.assistant = nil
			state.assistantAppended = false
		}
		if state.thinking == block {
			state.thinking = nil
			state.thinkingAppended = false
		}
		if state.assistant == nil && state.thinking == nil {
			delete(m.subAgentStreamStates, block.AgentID)
		} else {
			m.subAgentStreamStates[block.AgentID] = state
		}
	}
}

// continueFocusedLiveDisplaySequences numbers the focused agent's retained live
// blocks (merged after its freshly rebuilt base) so the visible label counters
// continue from the base without gaps. Live blocks retained from other agents
// keep the sequence numbers they were assigned when appended, and the other
// agents' counters are left alone, so a hidden block never inflates or
// renumbers the focused view.
func (m *Model) continueFocusedLiveDisplaySequences(tail []*Block, agentID string) {
	if len(tail) == 0 || m.lastDisplaySequence == nil {
		return
	}
	focusKey := displaySequenceAgentKey(agentID)
	sequence := m.lastDisplaySequence[focusKey]
	for _, block := range tail {
		if block == nil || displaySequenceAgentKey(block.AgentID) != focusKey {
			continue
		}
		sequence++
		block.DisplaySequence = sequence
	}
	m.lastDisplaySequence[focusKey] = sequence
}

func mergeFocusedToolBlockRuntimeState(dst, src *Block) {
	if dst == nil || src == nil {
		return
	}
	dst.Content = src.Content
	dst.RawArgs = src.RawArgs
	dst.Streaming = src.Streaming
	dst.ToolExecutionState = src.ToolExecutionState
	dst.ToolQueuedByExecutionEvent = src.ToolQueuedByExecutionEvent
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
	if err := m.agent.SwitchRole(next); err != nil {
		// The role did not change, so the draw caches stay valid.
		return m.enqueueToast(err.Error(), "error")
	}
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
