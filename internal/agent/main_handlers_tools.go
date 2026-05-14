package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// failPendingToolCalls cancels all pending and streaming tool calls for a turn
// after a terminal error, emitting cancelled results to TUI.
func (a *MainAgent) failPendingToolCalls(turn *Turn, err error) {
	if turn == nil {
		return
	}

	// Extract completed speculative tool results before draining
	var completedResults map[string]*ToolResultPayload
	if turn.streamingToolExec != nil {
		completedResults = turn.streamingToolExec.DrainCompletedResults()
	}

	streaming := turn.drainStreamingToolCalls()
	failedExec := turn.cancelPendingToolCalls()
	merged := mergePendingToolCalls(streaming, failedExec)
	a.clearToolTraceForCalls(merged)
	if len(merged) == 0 {
		return
	}
	pending := turn.PendingToolCalls.Load()
	if turn.activeToolBatchCancel != nil {
		turn.activeToolBatchCancel()
		turn.activeToolBatchCancel = nil
	}
	turn.PendingToolCalls.Store(0)
	turn.TotalToolCalls.Store(0)
	turn.toolExecutionBatches = nil
	turn.nextToolBatch = 0

	// Separate tools into completed vs truly failed
	var reallyFailed []PendingToolCall
	completedCount := 0
	for _, call := range merged {
		if payload, ok := completedResults[call.CallID]; ok {
			// Tool completed execution: persist and emit immediately so the
			// result survives terminal turn failure and can be reused on resume.
			a.appendCompletedInterruptedToolResult(payload)
			completedCount++
		} else {
			// Tool truly failed
			reallyFailed = append(reallyFailed, call)
		}
	}

	log.Warnf("failing pending tool calls after terminal turn error turn_id=%v pending_tools=%v failed_tools=%v completed_tools=%v error=%v", turn.ID, pending, len(reallyFailed), completedCount, err)

	if len(reallyFailed) > 0 {
		persistedResults := a.persistInterruptedToolResults(reallyFailed, ToolResultStatusError, err)
		if persistedResults > 0 {
			log.Infof("persisted failed tool-call results after terminal turn error turn_id=%v count=%v", turn.ID, persistedResults)
		}
		emitFailedToolResults(a.emitToTUI, reallyFailed, err)
	}
}

// filterPendingCallsForDeclaredTools drops pending tool metadata whose CallID is
// not declared on any assistant message in the context. Prevents persisting
// orphan tool rows after stream failures that never produced a matching
// assistant tool_calls entry.
func filterPendingCallsForDeclaredTools(m *ctxmgr.Manager, calls []PendingToolCall) []PendingToolCall {
	if m == nil || len(calls) == 0 {
		return calls
	}
	out := make([]PendingToolCall, 0, len(calls))
	for _, c := range calls {
		if m.AnyAssistantDeclaresToolCallID(c.CallID) {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (a *MainAgent) persistInterruptedToolResults(calls []PendingToolCall, status ToolResultStatus, cause error) int {
	if len(calls) == 0 {
		return 0
	}
	orig := len(calls)
	calls = filterPendingCallsForDeclaredTools(a.ctxMgr, calls)
	if len(calls) < orig {
		log.Warnf("skipping synthetic tool persistence for call_ids absent from assistant history dropped=%v", orig-len(calls))
	}
	if len(calls) == 0 {
		return 0
	}
	msgText := toolCallFailureMessage(cause)
	if status == ToolResultStatusCancelled {
		msgText = "Cancelled"
	}

	persisted := 0
	for _, call := range calls {
		toolMsg := message.Message{
			Role:       "tool",
			ToolCallID: call.CallID,
			Content:    msgText,
			ToolStatus: string(status),
			Audit:      call.Audit.Clone(),
		}
		a.ctxMgr.Append(toolMsg)
		if a.recovery != nil {
			a.persistAsync("main", toolMsg)
		}
		persisted++
	}
	return persisted
}

// appendCompletedInterruptedToolResult persists a fully completed tool result
// during turn interruption paths (cancel/replace/terminal error), without
// driving normal turn continuation.
func (a *MainAgent) appendCompletedInterruptedToolResult(payload *ToolResultPayload) {
	if a == nil || payload == nil {
		return
	}
	rawResult := payload.Result
	displayResult, contextResult, _, isError := composeToolResultTexts(rawResult, payload.Error)
	contextResult = applyToolArgsAuditToContextResult(contextResult, payload.Audit)

	a.emitToTUI(ToolResultEvent{
		CallID:      payload.CallID,
		Name:        payload.Name,
		ArgsJSON:    payload.ArgsJSON,
		Audit:       payload.Audit.Clone(),
		Result:      displayResult,
		Status:      toolResultStatusFromError(isError),
		Diff:        payload.Diff,
		DiffAdded:   payload.DiffAdded,
		DiffRemoved: payload.DiffRemoved,
		FileCreated: payload.FileCreated,
	})

	snapshot := a.ctxMgr.Snapshot()
	a.queueLSPDiagnosticOverlay(snapshot, payload)
	toolMsg := message.Message{
		Role:            "tool",
		Content:         contextResult,
		ToolCallID:      payload.CallID,
		ToolDiff:        payload.Diff,
		ToolDiffAdded:   payload.DiffAdded,
		ToolDiffRemoved: payload.DiffRemoved,
		ToolDurationMs:  payload.Duration.Milliseconds(),
		ToolStatus:      string(toolResultStatusFromError(isError)),
		Audit:           payload.Audit.Clone(),
		LSPReviews:      append([]message.LSPReview(nil), payload.LSPReviews...),
		FileState:       payload.FileState.Clone(),
		Provenance:      toolProvenanceForCall(snapshot, payload.CallID),
	}
	a.ctxMgr.Append(toolMsg)
	if a.recovery != nil {
		a.persistAsync("main", toolMsg)
	}
	a.recordEvidenceFromMessage(toolMsg)
}

// handleToolResult processes a single tool execution result. When all pending
// tool calls for the current turn have completed, a new LLM call is initiated
// to let the model decide what to do next.
func findAssistantMessageForToolCall(msgs []message.Message, callID string) (message.Message, bool) {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return message.Message{}, false
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		msg := msgs[i]
		if msg.Role != "assistant" || len(msg.ToolCalls) == 0 {
			continue
		}
		for _, tc := range msg.ToolCalls {
			if tc.ID == callID {
				return msg, true
			}
		}
	}
	return message.Message{}, false
}

func rawToolResultForVerification(payload *ToolResultPayload) string {
	if payload == nil {
		return ""
	}
	return payload.Result
}

func toolCallSkillName(msgs []message.Message, callID, fallbackArgsJSON string) string {
	callID = strings.TrimSpace(callID)
	parse := func(args []byte) string {
		if len(args) == 0 {
			return ""
		}
		var parsed struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(args, &parsed); err != nil {
			return ""
		}
		return strings.TrimSpace(parsed.Name)
	}
	if callID != "" {
		for i := len(msgs) - 1; i >= 0; i-- {
			msg := msgs[i]
			if msg.Role != "assistant" || len(msg.ToolCalls) == 0 {
				continue
			}
			for _, tc := range msg.ToolCalls {
				if tc.ID == callID && strings.EqualFold(tc.Name, "Skill") {
					if name := parse(tc.Args); name != "" {
						return name
					}
				}
			}
		}
	}
	return parse([]byte(fallbackArgsJSON))
}

func (a *MainAgent) handleToolResult(evt Event) {
	if a.turn == nil || evt.TurnID != a.turn.ID {
		log.Debugf("discarding stale tool result event_turn=%v current_turn=%v", evt.TurnID, a.currentTurnID())
		return
	}

	payload, ok := evt.Payload.(*ToolResultPayload)
	if !ok {
		log.Errorf("handleToolResult: invalid payload type payload_type=%v", fmt.Sprintf("%T", evt.Payload))
		return
	}
	a.turn.resolvePendingToolCall(payload.CallID)
	a.loopState.markProgress()
	if payload.Error == nil {
		if isVerificationLikeToolResult(payload, rawToolResultForVerification(payload)) {
			a.loopState.markVerificationProgress()
		}
		if payload.Name == "Skill" {
			if skillName := toolCallSkillName(a.ctxMgr.Snapshot(), payload.CallID, payload.ArgsJSON); skillName != "" {
				a.MarkSkillInvokedByName(skillName)
			}
		}
	}

	rawResult := payload.Result
	displayResult, contextResult, errorText, isError := composeToolResultTexts(rawResult, payload.Error)
	contextResult = applyToolArgsAuditToContextResult(contextResult, payload.Audit)

	hookResult, hookErr := a.fireHook(a.turn.Ctx, hook.OnBeforeToolResultAppend, a.turn.ID, buildBeforeToolResultAppendData(
		payload.Name,
		payload.ArgsJSON,
		rawResult,
		displayResult,
		contextResult,
		payload.Error,
		payload.Audit,
	))
	if hookErr != nil {
		log.Warnf("on_before_tool_result_append hook error error=%v", hookErr)
	} else if hookResult != nil {
		switch hookResult.Action {
		case hook.ActionBlock:
			log.Warn("on_before_tool_result_append returned block; ignoring")
		case hook.ActionModify:
			displayResult, contextResult = applyBeforeToolResultAppendHook(displayResult, contextResult, hookResult)
		}
	}

	a.fireHookBackground(a.turn.Ctx, hook.OnToolResult, a.turn.ID, buildToolResultHookData(
		payload.Name,
		payload.ArgsJSON,
		contextResult,
		payload.Error,
		payload.Diff,
		payload.Audit,
	))

	if payload.Name == "Handoff" && payload.Error == nil {
		var pcData struct {
			PlanPath string `json:"plan_path"`
		}
		if err := json.Unmarshal([]byte(payload.Result), &pcData); err != nil {
			log.Errorf("handleToolResult: failed to parse Handoff result error=%v", err)
		} else {
			log.Infof("Handoff result received; deferring until sibling tools complete plan_path=%v pending=%v", pcData.PlanPath, a.turn.PendingToolCalls.Load()-1)
			a.pendingHandoff = &HandoffResult{PlanPath: pcData.PlanPath}
		}
	}
	if payload.Name == tools.NameDone && payload.Error == nil && a.loopState.Enabled {
		a.loopState.advanceIteration()
		a.emitLoopStateChanged()
		assistantMsg, ok := findAssistantMessageForToolCall(a.ctxMgr.Snapshot(), payload.CallID)
		assistantContent := ""
		if ok {
			assistantContent = assistantMsg.Content
		}
		a.pendingLoopExitResult = &loopExitResult{CallID: payload.CallID, Reason: strings.TrimSpace(contextResult), AssistantContent: assistantContent, TurnID: a.turn.ID, ArgsJSON: payload.ArgsJSON}
	}

	a.emitToTUI(ToolResultEvent{
		CallID:      payload.CallID,
		Name:        payload.Name,
		ArgsJSON:    payload.ArgsJSON,
		Audit:       payload.Audit.Clone(),
		Result:      displayResult,
		Status:      toolResultStatusFromError(isError),
		Diff:        payload.Diff,
		DiffAdded:   payload.DiffAdded,
		DiffRemoved: payload.DiffRemoved,
		FileCreated: payload.FileCreated,
	})

	a.queueLSPDiagnosticOverlay(a.ctxMgr.Snapshot(), payload)
	toolMsg := message.Message{
		Role:            "tool",
		Content:         contextResult,
		ToolCallID:      payload.CallID,
		ToolDiff:        payload.Diff,
		ToolDiffAdded:   payload.DiffAdded,
		ToolDiffRemoved: payload.DiffRemoved,
		ToolDurationMs:  payload.Duration.Milliseconds(),
		ToolStatus:      string(toolResultStatusFromError(isError)),
		Audit:           payload.Audit.Clone(),
		LSPReviews:      append([]message.LSPReview(nil), payload.LSPReviews...),
		FileState:       payload.FileState.Clone(),
		Provenance:      toolProvenanceForCall(a.ctxMgr.Snapshot(), payload.CallID),
	}
	a.ctxMgr.Append(toolMsg)
	if a.recovery != nil {
		a.persistAsync("main", toolMsg)
	}
	a.recordEvidenceFromMessage(toolMsg)

	a.turn.CompletedToolCalls = append(a.turn.CompletedToolCalls, toolResultSummary(payload, contextResult, errorText))
	if changed := changedFileSummary(payload); changed != nil {
		a.loopState.markProgress()
		a.turn.ChangedFiles = append(a.turn.ChangedFiles, changed)
	}
	if isVerificationLikeToolResult(payload, contextResult) {
		a.loopState.markVerificationProgress()
	}

	// Track malformed and empty-args calls. Both malformed sentinel args and
	// empty "{}" args for tools with required parameters count as abnormal.
	if llm.IsMalformedArgs(json.RawMessage(payload.ArgsJSON)) {
		a.turn.malformedInBatch++
	} else if llm.IsEmptyArgs(json.RawMessage(payload.ArgsJSON)) {
		if tool, ok := a.tools.Get(payload.Name); ok {
			if req := llm.RequiredFields(tool.Parameters()); len(req) > 0 {
				a.turn.malformedInBatch++
			}
		}
	}

	remaining := a.turn.PendingToolCalls.Add(-1)
	if remaining < 0 {
		log.Warnf("PendingToolCalls went negative after tool result turn_id=%v call_id=%v", a.turn.ID, payload.CallID)
		a.turn.PendingToolCalls.Store(0)
		remaining = 0
	}
	if remaining == 0 {
		log.Debugf("tool result processed name=%v call_id=%v is_error=%v pending=%v malformed_in_batch=%v", payload.Name, payload.CallID, isError, a.turn.PendingToolCalls.Load(), a.turn.malformedInBatch)
		a.turn.TotalToolCalls.Store(0)
		a.turn.activeToolBatchCancel = nil
		if a.turn.nextToolBatch < len(a.turn.toolExecutionBatches) {
			a.startNextToolBatch(a.turn)
			return
		}
		abnormalInBatch := a.turn.malformedInBatch
		a.turn.toolExecutionBatches = nil
		a.turn.nextToolBatch = 0
		if abnormalInBatch > 0 {
			a.turn.MalformedCount++
			log.Warnf("batch contained abnormal tool call arguments abnormal_count=%v consecutive_rounds=%v", abnormalInBatch, a.turn.MalformedCount)
		} else {
			a.turn.MalformedCount = 0
		}
		a.turn.malformedInBatch = 0
		if a.turn.MalformedCount >= maxMalformedToolCalls {
			log.Warnf("aborting turn: too many consecutive malformed tool call rounds count=%v threshold=%v", a.turn.MalformedCount, maxMalformedToolCalls)
			a.emitToTUI(ErrorEvent{
				Err: fmt.Errorf(
					"turn aborted: the model produced malformed tool call arguments "+
						"%d times in a row. This usually indicates a model capability "+
						"issue or context overflow. Please start a new conversation. "+
						"You can also increase max_output_tokens in config to allow longer outputs",
					a.turn.MalformedCount,
				),
			})
			a.setIdleAndDrainPending()
			return
		}
		if results, err := a.runToolBatchHooks(a.turn.Ctx, a.turn); err != nil {
			log.Warnf("on_tool_batch_complete hook error error=%v", err)
		} else {
			for _, job := range results {
				if shouldAppendAutomationResult(job.Hook, job.Result) {
					a.appendHookFeedback(formatAutomationFeedback(job.Hook, job.Result))
				}
				if job.Result.Notify || job.Hook.Result == hook.ResultNotifyOnly {
					msg := job.Result.Summary
					if msg == "" {
						msg = fmt.Sprintf("Hook %s finished with status %s", job.Hook.Name, job.Result.Status)
					}
					a.emitToTUI(ToastEvent{
						Message: msg,
						Level:   hookToastLevel(job.Result),
					})
				}
			}
		}
		a.turn.CompletedToolCalls = nil
		a.turn.ChangedFiles = nil
		if a.pendingHandoff != nil {
			pc := a.pendingHandoff
			a.pendingHandoff = nil
			log.Infof("all sibling tools complete; finalizing Handoff plan_path=%v", pc.PlanPath)
			a.lastPlanPath = pc.PlanPath
			a.emitToTUI(HandoffEvent{PlanPath: pc.PlanPath})
			return
		}
		if a.pendingLoopExitResult != nil {
			pending := a.pendingLoopExitResult
			a.pendingLoopExitResult = nil
			if a.loopExitConditionsSatisfied(pending.AssistantContent) {
				resp, err := a.awaitLoopExitConfirmation(a.turn.Ctx, pending)
				if err != nil {
					log.Warnf("loop exit confirmation failed error=%v", err)
					a.appendLoopContinuationAndContinue(pending.CallID, pending.ArgsJSON, "Done rejected: exit confirmation failed. Continue the loop and keep working.")
				} else if resp.Approved {
					a.loopState.State = LoopStateCompleted
					a.emitLoopStateChanged()
					a.loopState.disable()
					a.refreshSystemPrompt()
					a.emitLoopStateChanged()
					a.emitActivity("main", ActivityIdle, "")
					a.setIdleAndDrainPending()
					return
				} else {
					reason := normalizeDenyReason(resp.DenyReason)
					if reason == "" {
						reason = "User rejected loop exit and requires more work before Done."
					}
					a.appendLoopContinuationAndContinue(pending.CallID, pending.ArgsJSON, "Done rejected: "+reason)
				}
			} else {
				a.appendLoopContinuationAndContinue(pending.CallID, pending.ArgsJSON, a.loopExitRejectionToolResult())
			}
		}

		log.Debugf("all tool calls complete, calling LLM again turn_id=%v", a.turn.ID)
		a.prepareSubAgentMailboxBatchForTurnContinuation()
		a.processPendingUserMessagesBeforeLLMInTurn()
		turnID := a.turn.ID
		turnCtx := a.turn.Ctx
		a.beginMainLLMAfterPreparation(turnCtx, turnID, "")
	}
}
