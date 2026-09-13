package tui

import (
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func stableToolDisplayArgs(toolName, argsJSON, result string) string {
	toolName = toolNameKey(toolName)
	if toolName == tools.NameApplyPatch {
		if display := applyPatchToolDisplayArgs(argsJSON); display != "" {
			return display
		}
		return fileToolPathDisplayArgs(tools.ExtractEditPathFromArgs([]byte(argsJSON)))
	}
	if toolName == tools.NameEdit {
		path := tools.ExtractEditPathFromArgs([]byte(argsJSON))
		return fileToolPathDisplayArgs(path)
	}
	return eventToolDisplayArgs(toolName, argsJSON, result)
}

func streamingToolDisplayArgs(toolName, argsJSON, result string) string {
	toolName = toolNameKey(toolName)
	switch toolName {
	case tools.NameApplyPatch:
		if display := applyPatchToolDisplayArgs(argsJSON); display != "" {
			return display
		}
		if preview := applyPatchStreamingPreview(argsJSON); preview != "" {
			return preview
		}
		if path := tools.ExtractEditPathFromArgs([]byte(argsJSON)); path != "" {
			return fileToolPathDisplayArgs(path)
		}
		return ""
	case tools.NameEdit:
		path := tools.ExtractEditPathFromArgs([]byte(argsJSON))
		if path == "" {
			path = streamedFileToolPath(argsJSON)
		}
		return fileToolPathDisplayArgs(path)
	case tools.NameWrite:
		return fileToolPathDisplayArgs(streamedFileToolPath(argsJSON))
	default:
		return eventToolDisplayArgs(toolName, argsJSON, result)
	}
}

func applyPatchToolDisplayArgs(argsJSON string) string {
	targets, err := tools.ApplyPatchDisplayTargets(json.RawMessage(argsJSON))
	if err != nil || len(targets) == 0 {
		return ""
	}
	paths := make([]string, 0, len(targets))
	for _, target := range targets {
		path := target.SourcePath
		if target.TargetPath != "" {
			path += " → " + target.TargetPath
		} else if target.Kind == tools.MutationDelete {
			// Keep the compact path-only display for ordinary updates, but make
			// deletion explicit because its content diff is intentionally hidden.
			path = "D " + path
		}
		paths = append(paths, path)
	}
	b, err := json.Marshal(map[string]any{"paths": paths})
	if err != nil {
		return ""
	}
	return string(b)
}

func fileToolPathDisplayArgs(path string) string {
	if path == "" {
		return ""
	}
	b, err := json.Marshal(map[string]string{"path": path})
	if err != nil {
		return ""
	}
	return string(b)
}

func (m *Model) ensureToolCallBlock(id, name, argsJSON, agentID string, state agent.ToolCallExecutionState, includeArgProgress bool) (*Block, bool) {
	name = toolNameKey(name)
	if m == nil || m.viewport == nil || strings.TrimSpace(id) == "" {
		return nil, false
	}
	if block, ok := m.findToolBlockByToolID(id); ok {
		return block, false
	}
	displayArgs := stableToolDisplayArgs(name, argsJSON, "")
	if includeArgProgress {
		displayArgs = streamingToolDisplayArgs(name, argsJSON, "")
	}
	block := &Block{
		ID:                 m.nextBlockID,
		Type:               BlockToolCall,
		Content:            displayArgs,
		RawArgs:            argsJSON,
		ToolName:           name,
		ToolID:             id,
		AgentID:            agentID,
		ToolExecutionState: state,
		StartedAt:          time.Now(),
	}
	initToolCardFoldState(block, name)
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

// markToolArgsComplete moves a card out of the argument-streaming state now
// that its arguments are known to be complete: queued, with the transient
// char-count progress dropped. ToolQueuedByExecutionEvent stays false because
// the agent has not dispatched the call yet — only a real execution event earns
// the queued badge.
//
// An already-finished call keeps its state, since a fast tool can emit its
// result before the final args update arrives, but still loses the progress
// text, which means nothing once a result exists. Reports whether anything
// changed.
func markToolArgsComplete(block *Block) bool {
	if block == nil {
		return false
	}
	changed := false
	if !block.ResultDone && block.StartedAt.IsZero() &&
		(block.ToolExecutionState == "" ||
			block.ToolExecutionState == agent.ToolCallExecutionStateReceiving ||
			block.ToolExecutionState == agent.ToolCallExecutionStateRunning) {
		block.ToolExecutionState = agent.ToolCallExecutionStateQueued
		block.ToolQueuedByExecutionEvent = false
		changed = true
	}
	// Re-derive the body from the accumulated arguments. Streaming deltas are
	// throttled, so a card inferred-complete can still be showing its very first
	// frame — often a truncated JSON prefix — while RawArgs already holds the
	// whole argument object. Providers that emit a real args-end event refreshed
	// Content from those same args just before calling this, so this is a no-op
	// for them.
	if displayArgs := stableToolDisplayArgs(block.ToolName, block.RawArgs, block.ResultContent); displayArgs != "" && displayArgs != block.Content {
		block.Content = displayArgs
		changed = true
	}
	if block.ToolProgress != nil {
		block.ToolProgress = nil
		changed = true
	}
	return changed
}

// markPriorReceivingToolCallsComplete advances earlier tool cards that are
// still streaming arguments into the queued state once a newer tool call starts
// streaming. Chat-completions-style providers emit argument deltas in
// generation order with no per-call end event, so the start of a later call is
// the only reliable signal that every earlier call's arguments are fully
// received. The transition is display-only and drops the transient char-count
// progress; execution still waits for the finalized response, and late
// ArgsStreamingDone / execution-state events are idempotent against Queued.
func (m *Model) markPriorReceivingToolCallsComplete(agentID, newCallID string) {
	if m == nil || m.viewport == nil {
		return
	}
	// Candidates sit at the tail — a card is only receiving while its call is
	// still streaming — so walk backwards to reach them first. The scan still
	// covers the whole transcript: a card left receiving by a call that never
	// produced an args-end (a truncated response) has no other way back, and
	// the next tool call start is what rescues it.
	for _, block := range slices.Backward(m.viewport.blocks) {
		if block == nil || block.Type != BlockToolCall || block.ToolID == newCallID {
			continue
		}
		if block.AgentID != agentID || !block.toolArgumentsAreReceiving() {
			continue
		}
		if markToolArgsComplete(block) {
			block.InvalidateCache()
			m.updateViewportBlock(block)
		}
	}
}

func (m *Model) ensureToolResultBlock(evt agent.ToolResultEvent) *Block {
	evt.Name = toolNameKey(evt.Name)
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
	evt.Name = toolNameKey(evt.Name)
	if evt.Status == agent.ToolResultStatusError && !evt.FileState.HasChanges() {
		return false
	}
	switch evt.Name {
	case tools.NameWrite, tools.NameEdit, tools.NameApplyPatch, tools.NameDelete:
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

func (m *Model) addSidebarFileState(agentID string, state *message.ToolFileState) bool {
	if m == nil || state == nil {
		return false
	}
	if len(state.Changes) > 0 {
		for _, change := range state.Changes {
			switch {
			case change.TargetPath != "":
				m.sidebar.AddFileMove(agentID, change.Path, change.TargetPath, change.Added, change.Removed)
			case change.Deleted:
				m.sidebar.AddFileDelete(agentID, change.Path)
			case change.Added != 0 || change.Removed != 0:
				m.sidebar.AddFileEdit(agentID, change.Path, change.Added, change.Removed)
			default:
				m.sidebar.AddFileWrite(agentID, change.Path)
			}
		}
		return true
	}
	changed := false
	for _, deleted := range state.Deletes {
		m.sidebar.AddFileDelete(agentID, deleted.Path)
		changed = true
	}
	for _, write := range state.Writes {
		m.sidebar.AddFileWrite(agentID, write.Path)
		changed = true
	}
	return changed
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
	evt.Name = toolNameKey(evt.Name)
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
		displayArgsJSON := evt.ArgsJSON
		if block.RawArgs != "" {
			// Keep the model-generated arguments already captured by the start or
			// streaming events. evt.ArgsJSON is the effective execution payload
			// used for result processing and file attribution.
			displayArgsJSON = ""
		} else if evt.Audit != nil && strings.TrimSpace(evt.Audit.OriginalArgsJSON) != "" {
			displayArgsJSON = evt.Audit.OriginalArgsJSON
		}
		applyStableToolResultToBlock(block, transcriptToolResult{
			argsJSON:       displayArgsJSON,
			result:         evt.Result,
			status:         evt.Status,
			audit:          evt.Audit,
			diff:           evt.Diff,
			duration:       evt.Duration,
			doneReport:     evt.DoneReport,
			displayArgs:    stableToolDisplayArgs,
			imageParts:     imagePartsFromContentParts(evt.Parts),
			resetExecution: true,
			recoveryState:  evt.RecoveryState,
		})
		if toolNameKey(evt.Name) == tools.NameDone {
			if evt.Status == agent.ToolResultStatusSuccess && !doneResultIsRejected(evt.Result) {
				m.expectedAgentClose = true
			}
		}
		if shouldTrackSidebarFileEdit(evt.Name) && (evt.Status.IsSuccess() || evt.FileState.HasChanges()) {
			if !evt.Status.IsSuccess() {
				if m.addSidebarFileState(evt.AgentID, evt.FileState) {
					effects.refreshSidebar = true
					effects.invalidateUsage = true
				}
			} else if evt.Name == tools.NameDelete {
				groups := tools.ParseDeleteResult(evt.Result)
				for _, path := range groups.Deleted {
					m.sidebar.AddFileDelete(evt.AgentID, path)
					effects.refreshSidebar = true
					effects.invalidateUsage = true
				}
			} else if evt.Name == tools.NameApplyPatch {
				if m.addApplyPatchSidebarChanges(evt.AgentID, json.RawMessage(evt.ArgsJSON), evt.Result) {
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
			if handle, _, ok := parseTaskToolHandle(evt.Result); ok && handle.TaskID != "" && handle.AgentID != "" {
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
		block := &Block{ID: m.nextBlockID, Type: BlockToolResult, Content: toolExpandedResultContent(evt.Name, evt.Result), RawArgs: evt.ArgsJSON, ToolName: evt.Name, ToolID: evt.CallID, ResultContent: evt.Result, ResultPayload: evt.Payload, ResultNotes: append([]string(nil), evt.Notes...), ResultStatus: evt.Status, ResultDone: true, Collapsed: !toolCardAlwaysExpanded(evt.Name), AgentID: evt.AgentID, Audit: evt.Audit.Clone(), ImageParts: imagePartsFromContentParts(evt.Parts), RecoveryState: evt.RecoveryState}
		m.nextBlockID++
		m.appendViewportBlock(block)
		m.markBlockSettled(block)
	}
	m.setStreamRenderInvalidation(streamRenderInvalidateForce)
	effects.addFollowup(m.requestStreamBoundaryFlush())
	return effects
}

func editedFilePathFromToolResult(evt agent.ToolResultEvent) string {
	if evt.Name == tools.NameEdit {
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

func (m *Model) addApplyPatchSidebarChanges(agentID string, args json.RawMessage, result ...string) bool {
	if len(result) > 0 && strings.Contains(result[0], "No net file changes") {
		return false
	}
	targets, err := tools.ApplyPatchDisplayTargets(args)
	if err != nil {
		return false
	}
	changed := false
	for _, target := range targets {
		if target.TargetPath != "" {
			m.sidebar.AddFileMove(agentID, target.SourcePath, target.TargetPath, target.Added, target.Removed)
			changed = true
			continue
		}
		switch target.Kind {
		case tools.MutationDelete:
			m.sidebar.AddFileDelete(agentID, target.SourcePath)
			changed = true
		case tools.MutationAdd, tools.MutationUpdate:
			if target.Added != 0 || target.Removed != 0 {
				m.sidebar.AddFileEdit(agentID, target.SourcePath, target.Added, target.Removed)
				changed = true
			}
		}
	}
	return changed
}

func (m *Model) handleToolAgentEvent(event agent.AgentEvent) (bool, agentEventEffects) {
	var effects agentEventEffects
	switch evt := event.(type) {
	case agent.ToolCallStartEvent:
		evt.Name = toolNameKey(evt.Name)
		m.touchStreamDelta(evt.AgentID)
		m.finalizeAgentStreamForCard(evt.AgentID, true)
		m.markRequestProgressBaseline(evt.AgentID)
		_, created := m.ensureToolCallBlock(evt.ID, evt.Name, evt.ArgsJSON, evt.AgentID, agent.ToolCallExecutionStateReceiving, true)
		if created {
			if block, ok := m.findToolBlockByToolID(evt.ID); ok {
				block.StartedAt = time.Time{}
			}
			m.recordToolArgRender(evt.ID, evt.ArgsJSON, time.Now())
			m.markPriorReceivingToolCallsComplete(evt.AgentID, evt.ID)
		}
		if created && evt.Name == tools.NameDelegate && evt.AgentID == "" {
			m.sidebar.AddPendingTask()
			effects.refreshSidebar = true
			m.recalcViewportSize()
		}
		return true, effects
	case agent.ToolCallUpdateEvent:
		evt.Name = toolNameKey(evt.Name)
		m.touchStreamDelta(evt.AgentID)
		now := time.Now()
		// An update without a start event is a recovery path. Preserve its
		// historical running fallback until the stream explicitly ends; normal
		// start events already created the card in receiving state.
		initialState := agent.ToolCallExecutionStateRunning
		if evt.ArgsStreamingDone {
			initialState = agent.ToolCallExecutionStateQueued
		}
		block, created := m.ensureToolCallBlock(evt.ID, evt.Name, evt.ArgsJSON, evt.AgentID, initialState, !evt.ArgsStreamingDone)
		if created {
			if evt.ArgsStreamingDone {
				delete(m.toolArgRenderState, evt.ID)
				if block != nil {
					block.Content = stableToolDisplayArgs(evt.Name, evt.ArgsJSON, block.ResultContent)
					block.StartedAt = time.Time{}
					markToolArgsComplete(block)
					block.InvalidateCache()
					m.updateViewportBlock(block)
				}
			} else {
				m.recordToolArgRender(evt.ID, evt.ArgsJSON, now)
			}
			return true, effects
		}
		// Live apply_patch preview: a growing patch is the card's primary content
		// while the model streams it. The arg-render cadence exists to throttle
		// transient char-count updates for other tools, and dropping deltas that
		// way would drop patch text; the preview is throttled by content instead,
		// refreshing once the patch grows past the coalesce window rather than on
		// a timer.
		livePatchPreview := !evt.ArgsStreamingDone && block != nil && toolNameKey(block.ToolName) == tools.NameApplyPatch
		// Keep RawArgs current even when this delta is throttled below: it is the
		// card's only copy of the accumulated arguments, and the completion path
		// re-derives the body from it. Assigning the string does not mark the
		// block updated, so it stays off the re-measure path that the coalesce
		// window exists to avoid.
		rawArgsChanged := block != nil && evt.ArgsJSON != "" && evt.ArgsJSON != block.RawArgs
		if rawArgsChanged {
			block.RawArgs = evt.ArgsJSON
		}
		allowArgRenderUpdate := evt.ArgsStreamingDone || livePatchPreview || m.shouldRefreshToolArgRender(evt.ID, evt.ArgsJSON, now)
		if !allowArgRenderUpdate {
			m.markStreamRenderDirty()
			effects.addFollowup(m.scheduleStreamFlush(0))
			return true, effects
		}
		updated := false
		argsStreamingDone := evt.ArgsStreamingDone || (block != nil && !block.StartedAt.IsZero())
		var displayArgs string
		if !argsStreamingDone && evt.InputText != "" {
			// Freeform custom-tool input (Responses apply_patch): the raw text
			// arrives through the InputText channel; render it verbatim instead
			// of re-deriving it from the canonical ArgsJSON envelope.
			displayArgs = sanitizeToolDisplayText(evt.InputText)
		} else if livePatchPreview {
			// The growing patch is the card's primary content. The cached
			// variant keeps the per-delta extraction off the quadratic path,
			// and its coalesce window doubles as the preview's refresh gate.
			displayArgs = block.cachedApplyPatchStreamingArgs(evt.ArgsJSON)
		} else {
			displayArgs = streamingToolDisplayArgs(evt.Name, evt.ArgsJSON, block.ResultContent)
		}
		if argsStreamingDone {
			displayArgs = stableToolDisplayArgs(evt.Name, evt.ArgsJSON, block.ResultContent)
		}
		if rawArgsChanged {
			// A live patch preview must not count the raw args as a visible
			// change. RawArgs grows with every delta, and marking the block
			// updated re-measures the card — measureSpanLines renders it in
			// full, highlighting included, just to count lines — so a per-delta
			// update is linear in the patch and the stream as a whole is
			// quadratic. cachedApplyPatchStreamingArgs already coalesces the
			// preview text, so let that coalesced text below decide when the
			// card really changed; the card then re-renders from the newest
			// RawArgs at that point, and between those points it keeps showing
			// the preview it last measured.
			if !livePatchPreview {
				updated = true
			}
		}
		if displayArgs != "" && displayArgs != block.Content {
			m.recordTUIDiagnostic("tool-call-update", "tool=%s id=%s block=%d len=%d->%d", evt.Name, evt.ID, block.ID, len(block.Content), len(displayArgs))
			block.Content = displayArgs
			updated = true
		}
		if argsStreamingDone {
			delete(m.toolArgRenderState, evt.ID)
			// The live preview is over: drop the per-line render memo so a
			// finished card does not retain the streaming preview's rendered
			// lines for the rest of the session.
			block.clearApplyPatchPreviewMemo()
			// Args have finished streaming but the tool may not have been dispatched yet
			// (execution-state events arrive only after the model response finalizes).
			// Mark as queued so fully-formed cards (notably TodoWrite) stop animating
			// while we wait for execution to begin.
			if markToolArgsComplete(block) {
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
			if livePatchPreview {
				// Ensure the growing preview is redrawn promptly, not only on the
				// next unrelated frame.
				effects.addFollowup(m.scheduleStreamFlush(0))
			}
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
		evt.Name = toolNameKey(evt.Name)
		delete(m.toolArgRenderState, evt.ID)
		block, created := m.ensureToolCallBlock(evt.ID, evt.Name, evt.ArgsJSON, evt.AgentID, evt.State, false)
		if block != nil {
			switch evt.State {
			case agent.ToolCallExecutionStateQueued:
				block.ToolQueuedByExecutionEvent = true
			case agent.ToolCallExecutionStateRunning:
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
		// Execution-state events may carry effective arguments after a hook or
		// confirmation. The card already contains the model's original call;
		// update arguments only for a recovery event that created an empty card.
		if block.RawArgs == "" && evt.ArgsJSON != "" {
			block.RawArgs = evt.ArgsJSON
			if displayArgs := stableToolDisplayArgs(evt.Name, evt.ArgsJSON, block.ResultContent); displayArgs != "" {
				block.Content = displayArgs
			}
			updated = true
		}
		if block.ToolExecutionState != evt.State {
			block.ToolExecutionState = evt.State
			updated = true
		}
		switch evt.State {
		case agent.ToolCallExecutionStateQueued:
			block.ToolQueuedByExecutionEvent = true
		case agent.ToolCallExecutionStateRunning:
			block.ToolQueuedByExecutionEvent = false
		}
		if block.ToolProgress != nil {
			block.ToolProgress = nil
			updated = true
		}
		if evt.State == agent.ToolCallExecutionStateQueued && !toolCardAlwaysExpanded(block.ToolName) {
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
