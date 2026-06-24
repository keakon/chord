package tui

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
)

func liveToolDisplayArgs(toolName, argsJSON, result string) string {
	if toolName == tools.NameEdit || toolName == tools.NamePatch {
		path := tools.ExtractEditPathFromArgs([]byte(argsJSON))
		if path == "" {
			return ""
		}
		b, err := json.Marshal(map[string]string{"path": path})
		if err == nil {
			return string(b)
		}
	}
	return eventToolDisplayArgs(toolName, argsJSON, result)
}

func (m *Model) ensureToolCallBlock(id, name, argsJSON, agentID string, state agent.ToolCallExecutionState, includeArgProgress bool) (*Block, bool) {
	name = tools.NormalizeName(name)
	if m == nil || m.viewport == nil || strings.TrimSpace(id) == "" {
		return nil, false
	}
	if block, ok := m.findToolBlockByToolID(id); ok {
		return block, false
	}
	block := &Block{
		ID:                 m.nextBlockID,
		Type:               BlockToolCall,
		Content:            liveToolDisplayArgs(name, argsJSON, ""),
		RawArgs:            argsJSON,
		ToolName:           name,
		ToolID:             id,
		Collapsed:          true,
		AgentID:            agentID,
		ToolExecutionState: state,
		StartedAt:          time.Now(),
	}
	if includeArgProgress {
		if progress := inferToolArgProgress(name, argsJSON); progress != nil {
			cp := *progress
			block.ToolProgress = &cp
		}
	}
	m.nextBlockID++
	m.appendViewportBlock(block)
	return block, true
}

func (m *Model) ensureToolResultBlock(evt agent.ToolResultEvent) *Block {
	evt.Name = tools.NormalizeName(evt.Name)
	if m == nil || m.viewport == nil {
		return nil
	}
	if block, ok := m.findToolBlockByToolID(evt.CallID); ok {
		return block
	}
	if block, ok := m.findLastPendingToolBlockByName(evt.Name); ok {
		if strings.TrimSpace(block.ToolID) == "" {
			block.ToolID = evt.CallID
			block.InvalidateCache()
			m.updateViewportBlock(block)
		}
		return block
	}
	return nil
}

func shouldRefreshGitStatusAfterToolResult(evt agent.ToolResultEvent) bool {
	evt.Name = tools.NormalizeName(evt.Name)
	if evt.Status == agent.ToolResultStatusError {
		return false
	}
	switch evt.Name {
	case tools.NameWrite, tools.NameEdit, tools.NamePatch, tools.NameDelete:
		return true
	case tools.NameShell:
		var args struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal([]byte(evt.ArgsJSON), &args); err != nil {
			return false
		}
		return shellCommandMayRunGit(args.Command)
	default:
		return false
	}
}

func shellCommandMayRunGit(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	if strings.HasPrefix(command, "git ") || command == "git" {
		return true
	}
	for _, sep := range []string{"&&", ";", "||", "\n"} {
		needle := sep + " git"
		if strings.Contains(command, needle+" ") || strings.HasSuffix(command, needle) {
			return true
		}
		if sep == "\n" && (strings.Contains(command, "\ngit ") || strings.HasSuffix(command, "\ngit")) {
			return true
		}
	}
	return false
}

func (m *Model) handleToolResultEvent(evt agent.ToolResultEvent) agentEventEffects {
	var effects agentEventEffects
	evt.Name = tools.NormalizeName(evt.Name)
	if evt.Name == tools.NameDelegate && evt.AgentID == "" {
		m.sidebar.ResolvePendingTask()
		effects.refreshSidebar = true
		m.recalcViewportSize()
	}
	if block := m.ensureToolResultBlock(evt); block != nil {
		delete(m.toolArgRenderState, evt.CallID)
		if block.ResultDone && block.ResultStatus == evt.Status && block.ResultContent == evt.Result && strings.TrimSpace(block.ToolID) == strings.TrimSpace(evt.CallID) {
			return effects
		}
		if block.ResultDone && block.ResultStatus == agent.ToolResultStatusSuccess && evt.Status != agent.ToolResultStatusSuccess {
			return effects
		}
		m.recordTUIDiagnostic("tool-result", "tool=%s call=%s block=%d status=%s result_len=%d had_diff=%t", evt.Name, evt.CallID, block.ID, evt.Status, len(evt.Result), evt.Diff != "")
		applyStableToolResultToBlock(block, transcriptToolResult{
			argsJSON:       evt.ArgsJSON,
			result:         evt.Result,
			status:         evt.Status,
			audit:          evt.Audit,
			diff:           evt.Diff,
			doneReport:     evt.DoneReport,
			displayArgs:    liveToolDisplayArgs,
			imageParts:     imagePartsFromContentParts(evt.Parts),
			resetExecution: true,
		})
		if toolNameKey(evt.Name) == tools.NameDone {
			if evt.Status == agent.ToolResultStatusSuccess && !doneResultIsRejected(evt.Result) {
				m.expectedAgentClose = true
			}
		}
		if shouldTrackSidebarFileEdit(evt.Name) && evt.Status != agent.ToolResultStatusError {
			if evt.Name == tools.NameDelete {
				groups := tools.ParseDeleteResult(evt.Result)
				for _, path := range groups.Deleted {
					m.sidebar.AddFileDelete(evt.AgentID, path)
					effects.refreshSidebar = true
					effects.invalidateUsage = true
				}
			} else if path := editedFilePathFromToolResult(evt); path != "" {
				m.sidebar.AddFileEdit(evt.AgentID, path, evt.DiffAdded, evt.DiffRemoved)
				effects.refreshSidebar = true
				effects.invalidateUsage = true
			}
		}
		if shouldRefreshGitStatusAfterToolResult(evt) {
			effects.addFollowup(m.requestGitStatusRefresh())
		}
		if evt.Name == tools.NameNotify && evt.Status != agent.ToolResultStatusError && evt.Result != "" {
			if handle, ok := parseTaskToolHandle(evt.Result); ok && handle.TaskID != "" && handle.AgentID != "" {
				if taskBlock, ok := m.findBlockByLinkedTask(handle.TaskID); ok {
					taskBlock.LinkedAgentID = handle.AgentID
					taskBlock.LinkedTaskID = handle.TaskID
					taskBlock.InvalidateCache()
					m.updateViewportBlock(taskBlock)
				}
			}
		}
		block.InvalidateCache()
		m.updateViewportBlock(block)
		m.markBlockSettled(block)
	} else {
		block := &Block{ID: m.nextBlockID, Type: BlockToolResult, Content: toolExpandedResultContent(evt.Name, evt.Result), RawArgs: evt.ArgsJSON, ToolName: evt.Name, ToolID: evt.CallID, ResultContent: evt.Result, ResultStatus: evt.Status, ResultDone: true, Collapsed: true, AgentID: evt.AgentID, ImageParts: imagePartsFromContentParts(evt.Parts)}
		m.nextBlockID++
		m.appendViewportBlock(block)
		m.markBlockSettled(block)
	}
	m.setStreamRenderInvalidation(streamRenderInvalidateForce)
	effects.addFollowup(m.requestStreamBoundaryFlush())
	return effects
}

func editedFilePathFromToolResult(evt agent.ToolResultEvent) string {
	if evt.Name == tools.NameEdit || evt.Name == tools.NamePatch {
		return tools.ExtractEditPathFromArgs(json.RawMessage(evt.ArgsJSON))
	}
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(evt.ArgsJSON), &args); err != nil {
		return ""
	}
	return strings.TrimSpace(args.Path)
}

func (m *Model) handleToolAgentEvent(event agent.AgentEvent) (bool, agentEventEffects) {
	var effects agentEventEffects
	switch evt := event.(type) {
	case agent.ToolCallStartEvent:
		evt.Name = tools.NormalizeName(evt.Name)
		m.touchStreamDelta(evt.AgentID)
		m.finalizeAssistantBlock()
		m.markRequestProgressBaseline(evt.AgentID)
		_, created := m.ensureToolCallBlock(evt.ID, evt.Name, evt.ArgsJSON, evt.AgentID, agent.ToolCallExecutionStateRunning, true)
		if created {
			if block, ok := m.findToolBlockByToolID(evt.ID); ok {
				block.StartedAt = time.Time{}
			}
			m.recordToolArgRender(evt.ID, evt.ArgsJSON, time.Now())
		}
		if created && evt.Name == tools.NameDelegate && evt.AgentID == "" {
			m.sidebar.AddPendingTask()
			effects.refreshSidebar = true
			m.recalcViewportSize()
		}
		return true, effects
	case agent.ToolCallUpdateEvent:
		evt.Name = tools.NormalizeName(evt.Name)
		m.touchStreamDelta(evt.AgentID)
		now := time.Now()
		block, created := m.ensureToolCallBlock(evt.ID, evt.Name, evt.ArgsJSON, evt.AgentID, agent.ToolCallExecutionStateRunning, !evt.ArgsStreamingDone)
		if created {
			if evt.ArgsStreamingDone {
				delete(m.toolArgRenderState, evt.ID)
				if block != nil {
					block.StartedAt = time.Time{}
					if block.ToolExecutionState == "" || block.ToolExecutionState == agent.ToolCallExecutionStateRunning {
						block.ToolExecutionState = agent.ToolCallExecutionStateQueued
						block.ToolQueuedByExecutionEvent = false
					}
					if block.ToolProgress != nil {
						block.ToolProgress = nil
					}
					block.InvalidateCache()
					m.updateViewportBlock(block)
				}
			} else {
				m.recordToolArgRender(evt.ID, evt.ArgsJSON, now)
			}
			return true, effects
		}
		allowArgRenderUpdate := evt.ArgsStreamingDone || m.shouldRefreshToolArgRender(evt.ID, evt.ArgsJSON, now)
		if !allowArgRenderUpdate {
			m.markStreamRenderDirty()
			effects.addFollowup(m.scheduleStreamFlush(0))
			return true, effects
		}
		updated := false
		argsStreamingDone := evt.ArgsStreamingDone || (block != nil && !block.StartedAt.IsZero())
		displayArgs := liveToolDisplayArgs(evt.Name, evt.ArgsJSON, block.ResultContent)
		if evt.ArgsJSON != "" && evt.ArgsJSON != block.RawArgs {
			block.RawArgs = evt.ArgsJSON
			updated = true
		}
		if displayArgs != "" && displayArgs != block.Content {
			m.recordTUIDiagnostic("tool-call-update", "tool=%s id=%s block=%d len=%d->%d", evt.Name, evt.ID, block.ID, len(block.Content), len(displayArgs))
			block.Content = displayArgs
			updated = true
		}
		if argsStreamingDone {
			delete(m.toolArgRenderState, evt.ID)
			// Args have finished streaming but the tool may not have been dispatched yet
			// (execution-state events arrive only after the model response finalizes).
			// Mark as queued so fully-formed cards (notably TodoWrite) stop animating
			// while we wait for execution to begin.
			//
			// Do not downgrade already-finished tool calls. Fast tools can emit
			// ToolResultEvent before the final ArgsStreamingDone update arrives.
			if !block.ResultDone && block.StartedAt.IsZero() && (block.ToolExecutionState == "" || block.ToolExecutionState == agent.ToolCallExecutionStateRunning) {
				block.ToolExecutionState = agent.ToolCallExecutionStateQueued
				block.ToolQueuedByExecutionEvent = false
				updated = true
			}
			if block.ToolProgress != nil {
				block.ToolProgress = nil
				updated = true
			}
		} else {
			if progress := inferToolArgProgress(evt.Name, evt.ArgsJSON); progress != nil {
				if block.ToolProgress == nil || *block.ToolProgress != *progress {
					cp := *progress
					block.ToolProgress = &cp
					updated = true
				}
			}
		}
		if updated {
			if !argsStreamingDone {
				m.recordToolArgRender(evt.ID, evt.ArgsJSON, now)
			}
			block.InvalidateCache()
			m.updateViewportBlock(block)
		}
		return true, effects
	case agent.ToolCallDiscardEvent:
		delete(m.toolArgRenderState, evt.ID)
		block, ok := m.findToolBlockByToolID(evt.ID)
		if !ok {
			return false, effects
		}
		m.removeViewportBlockByID(block.ID)
		return true, effects
	case agent.ToolCallExecutionEvent:
		evt.Name = tools.NormalizeName(evt.Name)
		delete(m.toolArgRenderState, evt.ID)
		block, created := m.ensureToolCallBlock(evt.ID, evt.Name, evt.ArgsJSON, evt.AgentID, evt.State, false)
		if block != nil {
			if evt.State == agent.ToolCallExecutionStateQueued {
				block.ToolQueuedByExecutionEvent = true
			} else if evt.State == agent.ToolCallExecutionStateRunning {
				block.ToolQueuedByExecutionEvent = false
			}
		}
		if evt.State == agent.ToolCallExecutionStateRunning && block != nil && block.StartedAt.IsZero() {
			block.StartedAt = time.Now()
			m.markRequestProgressBaseline(evt.AgentID)
		}
		if created {
			return true, effects
		}
		updated := false
		displayArgs := liveToolDisplayArgs(evt.Name, evt.ArgsJSON, block.ResultContent)
		if evt.ArgsJSON != "" && evt.ArgsJSON != block.RawArgs {
			block.RawArgs = evt.ArgsJSON
			updated = true
		}
		if displayArgs != "" && displayArgs != block.Content {
			block.Content = displayArgs
			updated = true
		}
		if block.ToolExecutionState != evt.State {
			block.ToolExecutionState = evt.State
			updated = true
		}
		if evt.State == agent.ToolCallExecutionStateQueued {
			block.ToolQueuedByExecutionEvent = true
		} else if evt.State == agent.ToolCallExecutionStateRunning {
			block.ToolQueuedByExecutionEvent = false
		}
		if block.ToolProgress != nil {
			block.ToolProgress = nil
			updated = true
		}
		if evt.State == agent.ToolCallExecutionStateQueued {
			block.Collapsed = true
		}
		if updated {
			block.InvalidateCache()
			m.updateViewportBlock(block)
		}
		return true, effects
	case agent.ToolProgressEvent:
		if block, ok := m.findToolBlockByToolID(evt.CallID); ok {
			if block.ResultDone || block.ToolExecutionState == agent.ToolCallExecutionStateQueued {
				return true, effects
			}
			progress := evt.Progress
			if progress.Label == "" && progress.Current == 0 && progress.Total == 0 && strings.TrimSpace(progress.Text) == "" {
				if block.ToolProgress != nil {
					block.ToolProgress = nil
					block.InvalidateCache()
					m.updateViewportBlock(block)
				}
				return true, effects
			}
			if block.ToolProgress == nil || *block.ToolProgress != progress {
				cp := progress
				block.ToolProgress = &cp
				block.InvalidateCache()
				m.updateViewportBlock(block)
			}
		}
		return true, effects
	case agent.ToolResultEvent:
		effects = m.handleToolResultEvent(evt)
		return true, effects
	default:
		return false, effects
	}
}

func (m *Model) shouldRefreshToolArgRender(callID, argsJSON string, now time.Time) bool {
	if strings.TrimSpace(callID) == "" {
		return true
	}
	state, ok := m.toolArgRenderState[callID]
	if !ok {
		return true
	}
	currentBytes := len(argsJSON)
	if currentBytes <= state.lastBytes {
		return false
	}
	cadence := m.currentCadence()
	delay := cadence.visualAnimDelay
	if delay <= 0 {
		delay = cadence.contentFlushDelay
	}
	if delay <= 0 {
		return false
	}
	return now.Sub(state.lastAt) >= delay
}

func (m *Model) recordToolArgRender(callID, argsJSON string, now time.Time) {
	if strings.TrimSpace(callID) == "" {
		return
	}
	if m.toolArgRenderState == nil {
		m.toolArgRenderState = make(map[string]toolArgRenderState)
	}
	m.toolArgRenderState[callID] = toolArgRenderState{
		lastBytes: len(argsJSON),
		lastAt:    now,
	}
}
