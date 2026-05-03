package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/agent/agentdiff"
	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func (s *SubAgent) startNextToolBatch(turn *Turn) {
	if s == nil || turn == nil || s.turn == nil || s.turn != turn {
		return
	}
	if turn.activeToolBatchCancel != nil {
		turn.activeToolBatchCancel()
		turn.activeToolBatchCancel = nil
	}
	if turn.nextToolBatch >= len(turn.toolExecutionBatches) {
		turn.PendingToolCalls.Store(0)
		return
	}
	batch := turn.toolExecutionBatches[turn.nextToolBatch]
	turn.nextToolBatch++
	turn.PendingToolCalls.Store(int32(len(batch.Calls)))
	batchCtx := turn.Ctx
	var batchCancel context.CancelFunc
	if batch.AbortSiblingsOnError {
		batchCtx, batchCancel = context.WithCancel(turn.Ctx)
		turn.activeToolBatchCancel = batchCancel
	}
	batchPending := make([]PendingToolCall, 0, len(batch.Calls))
	for _, tc := range batch.Calls {
		batchPending = append(batchPending, PendingToolCall{CallID: tc.ID, Name: tc.Name, ArgsJSON: string(tc.Args), AgentID: s.instanceID})
	}
	now := time.Now()
	for _, call := range batchPending {
		s.parent.logToolTraceExecutionRunning(call, now)
	}
	emitToolExecutionState(s.parent.emitToTUI, batchPending, ToolCallExecutionStateRunning)
	for _, tc := range batch.Calls {
		go func(tc message.ToolCall) {
			startedAt := time.Now()
			execResult, err := s.executeToolCall(batchCtx, tc)
			if batchCtx.Err() != nil && turn.Ctx.Err() != nil {
				return
			}
			var diff agentdiff.Summary
			if err == nil {
				effectiveCall := tc
				effectiveCall.Args = json.RawMessage(execResult.EffectiveArgsJSON)
				diff = agentdiff.GenerateToolDiff(effectiveCall, execResult.PreContent, execResult.PreFilePath)
			}
			if err != nil && batch.AbortSiblingsOnError {
				if batchCancel != nil {
					batchCancel()
				}
			}
			select {
			case s.toolCh <- &toolResult{
				CallID:      tc.ID,
				Name:        tc.Name,
				ArgsJSON:    execResult.EffectiveArgsJSON,
				Audit:       execResult.Audit,
				Result:      execResult.Result,
				Error:       err,
				TurnID:      turn.ID,
				Diff:        diff.Text,
				DiffAdded:   diff.Added,
				DiffRemoved: diff.Removed,
				FileCreated: tc.Name == tools.NameWrite && !execResult.PreExisted,
				LSPReviews:  append([]message.LSPReview(nil), execResult.LSPReviews...),
				Duration:    time.Since(startedAt),
			}:
			case <-s.parentCtx.Done():
			}
		}(tc)
	}
}

// handleToolResult processes a single tool execution result. When all pending
// tool calls for the current turn have completed, it either sends
// EventAgentDone (if pendingComplete is set) or continues the LLM loop.
func (s *SubAgent) handleToolResult(result *toolResult) {
	// Turn isolation: discard stale results.
	if s.turn == nil || result.TurnID != s.turn.ID {
		log.Debugf("SubAgent: discarding stale tool result agent=%v result_turn=%v current_turn=%v", s.instanceID, result.TurnID, s.currentTurnID())
		return
	}

	rawResult := result.Result
	displayResult, contextResult, errorText, isError := composeToolResultTexts(rawResult, result.Error)
	contextResult = applyToolArgsAuditToContextResult(contextResult, result.Audit)

	hookResult, hookErr := s.fireHook(s.turn.Ctx, hook.OnBeforeToolResultAppend, s.turn.ID, buildBeforeToolResultAppendData(
		result.Name,
		result.ArgsJSON,
		rawResult,
		displayResult,
		contextResult,
		result.Error,
		result.Audit,
	))
	if hookErr != nil {
		log.Warnf("SubAgent on_before_tool_result_append hook error agent=%v error=%v", s.instanceID, hookErr)
	} else if hookResult != nil {
		switch hookResult.Action {
		case hook.ActionBlock:
			log.Warnf("SubAgent on_before_tool_result_append returned block; ignoring agent=%v", s.instanceID)
		case hook.ActionModify:
			displayResult, contextResult = applyBeforeToolResultAppendHook(displayResult, contextResult, hookResult)
		}
	}

	s.fireHookBackground(s.turn.Ctx, hook.OnToolResult, s.turn.ID, buildToolResultHookData(
		result.Name,
		result.ArgsJSON,
		contextResult,
		result.Error,
		result.Diff,
		result.Audit,
	))

	// Emit tool result to TUI so the tool call block shows its result.
	s.turn.resolvePendingToolCall(result.CallID)
	s.parent.emitToTUI(ToolResultEvent{
		CallID:      result.CallID,
		Name:        result.Name,
		ArgsJSON:    result.ArgsJSON,
		Audit:       result.Audit.Clone(),
		Result:      displayResult,
		Status:      toolResultStatusFromError(isError),
		AgentID:     s.instanceID,
		Diff:        result.Diff,
		DiffAdded:   result.DiffAdded,
		DiffRemoved: result.DiffRemoved,
		FileCreated: result.FileCreated,
	})

	toolMsg := message.Message{
		Role:            "tool",
		ToolCallID:      result.CallID,
		Content:         contextResult,
		ToolDiff:        result.Diff,
		ToolDiffAdded:   result.DiffAdded,
		ToolDiffRemoved: result.DiffRemoved,
		ToolDurationMs:  result.Duration.Milliseconds(),
		LSPReviews:      append([]message.LSPReview(nil), result.LSPReviews...),
		Audit:           result.Audit.Clone(),
	}
	s.ctxMgr.Append(toolMsg)

	// Persist tool result.
	go func() {
		if err := s.recovery.PersistMessage(s.instanceID, toolMsg); err != nil {
			log.Warnf("SubAgent: failed to persist tool result agent=%v error=%v", s.instanceID, err)
		}
	}()

	s.turn.CompletedToolCalls = append(s.turn.CompletedToolCalls, map[string]any{
		"call_id":    result.CallID,
		"tool_name":  result.Name,
		"args":       json.RawMessage(result.ArgsJSON),
		"args_audit": toolArgsAuditHookData(result.Audit),
		"result":     contextResult,
		"error":      errorText,
		"diff":       result.Diff,
		"path":       extractHookFilePath(json.RawMessage(result.ArgsJSON)),
	})
	if changed := changedFileSummary(&ToolResultPayload{
		CallID:      result.CallID,
		Name:        result.Name,
		ArgsJSON:    result.ArgsJSON,
		Audit:       result.Audit,
		Result:      contextResult,
		Diff:        result.Diff,
		DiffAdded:   result.DiffAdded,
		DiffRemoved: result.DiffRemoved,
	}); changed != nil {
		s.turn.ChangedFiles = append(s.turn.ChangedFiles, changed)
	}

	s.turn.PendingToolCalls.Add(-1)
	// Track malformed and empty-args calls (improvement 3 + 4).
	if llm.IsMalformedArgs(json.RawMessage(result.ArgsJSON)) {
		s.turn.malformedInBatch++
	} else if llm.IsEmptyArgs(json.RawMessage(result.ArgsJSON)) {
		if tool, ok := s.tools.Get(result.Name); ok {
			if req := llm.RequiredFields(tool.Parameters()); len(req) > 0 {
				s.turn.malformedInBatch++
			}
		}
	}

	if s.turn.PendingToolCalls.Load() > 0 {
		return // still waiting for tool calls in this batch
	}
	if s.turn.activeToolBatchCancel != nil {
		s.turn.activeToolBatchCancel()
		s.turn.activeToolBatchCancel = nil
	}
	if s.turn.nextToolBatch < len(s.turn.toolExecutionBatches) {
		s.startNextToolBatch(s.turn)
		return
	}

	// All tool calls in this batch complete.
	// Update the consecutive-malformed-round counter and abort if threshold hit.
	abnormalInBatch := s.turn.malformedInBatch
	s.turn.toolExecutionBatches = nil
	s.turn.nextToolBatch = 0
	if abnormalInBatch > 0 {
		s.turn.MalformedCount++
		log.Warnf("SubAgent: batch contained abnormal tool call arguments agent=%v abnormal_count=%v consecutive_rounds=%v", s.instanceID, abnormalInBatch, s.turn.MalformedCount)
	} else {
		s.turn.MalformedCount = 0
	}
	s.turn.malformedInBatch = 0

	if s.turn.MalformedCount >= maxMalformedToolCalls {
		log.Warnf("SubAgent: aborting turn due to repeated malformed tool call args agent=%v count=%v threshold=%v", s.instanceID, s.turn.MalformedCount, maxMalformedToolCalls)
		s.sendEvent(Event{
			Type: EventAgentError,
			Payload: fmt.Errorf(
				"SubAgent %s aborted: the model produced malformed tool call arguments "+
					"%d times in a row",
				s.instanceID, s.turn.MalformedCount,
			),
		})
		return
	}

	if results, err := s.runToolBatchHooks(s.turn.Ctx, s.turn); err != nil {
		log.Warnf("SubAgent on_tool_batch_complete hook error agent=%v error=%v", s.instanceID, err)
	} else {
		for _, job := range results {
			if shouldAppendAutomationResult(job.Hook, job.Result) {
				s.appendHookFeedback(formatAutomationFeedback(job.Hook, job.Result))
			}
			if job.Result.Notify || job.Hook.Result == hook.ResultNotifyOnly {
				msg := job.Result.Summary
				if msg == "" {
					msg = fmt.Sprintf("Hook %s finished with status %s", job.Hook.Name, job.Result.Status)
				}
				s.parent.emitToTUI(ToastEvent{
					Message: msg,
					Level:   hookToastLevel(job.Result),
					AgentID: s.instanceID,
				})
			}
		}
	}
	s.turn.CompletedToolCalls = nil
	s.turn.ChangedFiles = nil

	outstandingJoinChildren := s.parent.outstandingJoinChildTaskIDs(s.taskID)
	if len(outstandingJoinChildren) > 0 {
		if s.pendingComplete != nil {
			s.appendCompleteToolResult(s.pendingCompleteCallID, deferredCompleteResult(len(outstandingJoinChildren)))
			s.setPendingCompleteIntent(s.pendingComplete)
			s.pendingComplete = nil
			s.pendingCompleteCallID = ""
		}
		if s.pendingEscalate != "" {
			reason := s.pendingEscalate
			s.pendingEscalate = ""
			s.sendEvent(Event{
				Type:     EventEscalate,
				SourceID: s.instanceID,
				Payload:  reason,
			})
			return
		}
		s.enterWaitingDescendant(deferredCompleteResult(len(outstandingJoinChildren)))
		return
	}
	s.clearPendingCompleteIntent()

	// If Complete was co-returned, trigger EventAgentDone now.
	if s.pendingComplete != nil {
		complete := s.pendingComplete
		s.appendCompleteToolResult(s.pendingCompleteCallID, complete.Summary)
		s.pendingComplete = nil
		s.pendingCompleteCallID = ""
		s.clearPendingCompleteIntent()
		s.sendEvent(Event{
			Type:    EventAgentDone,
			Payload: complete,
		})
		return
	}
	if s.pendingEscalate != "" {
		reason := s.pendingEscalate
		s.pendingEscalate = ""
		s.sendEvent(Event{
			Type:     EventEscalate,
			SourceID: s.instanceID,
			Payload:  reason,
		})
		return
	}
	if s.State() == SubAgentStateWaitingPrimary {
		return
	}

	// Normal: continue LLM conversation.
	messages := s.ctxMgr.Snapshot()
	s.asyncCallLLM(s.turn, messages)
}
