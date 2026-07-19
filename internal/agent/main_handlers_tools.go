package agent

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// failPendingToolCalls cancels all pending and streaming tool calls for a turn
// after a terminal error. Calls absent from finalized assistant tool_calls are
// discarded from TUI instead of being shown as real cancelled/error results.
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
	merged = turn.filterCompletedToolCalls(merged)
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
			if a.handleCompletedInterruptedToolResult(call, payload, "not_in_context") {
				completedCount++
			}
		} else {
			// Tool truly failed
			reallyFailed = append(reallyFailed, call)
		}
	}

	log.Warnf("failing pending tool calls after terminal turn error turn_id=%v pending_tools=%v failed_tools=%v completed_tools=%v error=%v", turn.ID, pending, len(reallyFailed), completedCount, err)

	if len(reallyFailed) > 0 {
		declared, undeclared := splitPendingCallsByDeclaredTools(a.ctxMgr, reallyFailed)
		if len(undeclared) > 0 {
			log.Warnf("discarding synthetic tool failures for call_ids absent from assistant history dropped=%v", len(undeclared))
			emitToolCallDiscards(a.emitToTUI, undeclared, "not_in_context")
		}
		persistedResults := a.persistInterruptedToolResults(declared, ToolResultStatusError, err)
		if persistedResults > 0 {
			log.Infof("persisted failed tool-call results after terminal turn error turn_id=%v count=%v", turn.ID, persistedResults)
		}
		emitFailedToolResults(a.emitToTUI, declared, err)
	}
}

// filterPendingCallsForDeclaredTools drops pending tool metadata whose CallID is
// not declared on any assistant message in the context. Prevents persisting
// orphan tool rows after stream failures that never produced a matching
// assistant tool_calls entry.
func filterPendingCallsForDeclaredTools(m *ctxmgr.Manager, calls []PendingToolCall) []PendingToolCall {
	declared, _ := splitPendingCallsByDeclaredTools(m, calls)
	return declared
}

func splitPendingCallsByDeclaredTools(m *ctxmgr.Manager, calls []PendingToolCall) (declared, undeclared []PendingToolCall) {
	if m == nil || len(calls) == 0 {
		return calls, nil
	}
	for _, c := range calls {
		if m.AnyAssistantDeclaresToolCallID(c.CallID) {
			declared = append(declared, c)
		} else {
			undeclared = append(undeclared, c)
		}
	}
	return declared, undeclared
}

func (a *MainAgent) handleCompletedInterruptedToolResult(call PendingToolCall, payload *ToolResultPayload, discardReason string) bool {
	if a == nil || payload == nil {
		return false
	}
	if a.ctxMgr != nil && !a.ctxMgr.AnyAssistantDeclaresToolCallID(call.CallID) {
		if strings.TrimSpace(discardReason) == "" {
			discardReason = "not_in_context"
		}
		emitToolCallDiscards(a.emitToTUI, []PendingToolCall{call}, discardReason)
		return false
	}
	// Tool completed execution: persist and emit immediately so the result
	// survives terminal turn failure/interruption and can be reused on resume.
	a.appendCompletedInterruptedToolResult(payload)
	return true
}

// finalizeInterruptedToolCalls is the shared tail of every turn cancel / fail /
// replacement / shutdown path on both MainAgent and SubAgent. It splits calls
// into the declared subset (present in assistant history) and the rest,
// persists synthetic terminal results for the declared ones via persist, and
// emits-or-discards the matching UI events. It returns the number persisted so
// the caller can log it with its own site-specific fields.
//
// Keeping split → persist → emit together in one place guarantees persistence
// and the UI always agree on the same declared/undeclared partition; previously
// this trio was open-coded at six call sites, each one place a future edit could
// update persistence without updating the UI (or vice versa).
func finalizeInterruptedToolCalls(
	ctxMgr *ctxmgr.Manager,
	emit func(AgentEvent),
	persist func(calls []PendingToolCall, status ToolResultStatus, cause error) int,
	calls []PendingToolCall,
	status ToolResultStatus,
	cause error,
) int {
	declared, undeclared := splitPendingCallsByDeclaredTools(ctxMgr, calls)
	persisted := persist(declared, status, cause)
	emitInterruptedToolResultsOrDiscards(emit, declared, undeclared, status, cause, "not_in_context")
	return persisted
}

// persistInterruptedToolResultsInto is the shared core behind both
// MainAgent.persistInterruptedToolResults and its SubAgent counterpart. It
// appends a synthetic terminal tool message for the declared subset of calls to
// ctxMgr and durably writes each via persist, returning the number counted as
// persisted (persist reports per-message whether it should count). logSkip is
// invoked once with the number of calls dropped for being absent from the
// assistant history. Keeping this logic in one place means the subtle bits
// (declared-only filtering, Cancelled vs failure text, Audit cloning) cannot
// drift between the two agent kinds.
func persistInterruptedToolResultsInto(
	ctxMgr *ctxmgr.Manager,
	calls []PendingToolCall,
	status ToolResultStatus,
	cause error,
	logSkip func(dropped int),
	persist func(message.Message) bool,
) int {
	if len(calls) == 0 {
		return 0
	}
	orig := len(calls)
	calls = filterPendingCallsForDeclaredTools(ctxMgr, calls)
	if len(calls) < orig && logSkip != nil {
		logSkip(orig - len(calls))
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
		ctxMgr.Append(toolMsg)
		if persist(toolMsg) {
			persisted++
		}
	}
	return persisted
}

func (a *MainAgent) persistInterruptedToolResults(calls []PendingToolCall, status ToolResultStatus, cause error) int {
	return persistInterruptedToolResultsInto(a.ctxMgr, calls, status, cause,
		func(dropped int) {
			log.Warnf("skipping synthetic tool persistence for call_ids absent from assistant history dropped=%v", dropped)
		},
		func(toolMsg message.Message) bool {
			if a.recovery != nil {
				a.persistAsync(identity.MainAgentID, toolMsg)
			}
			return true
		},
	)
}

func todoWriteArgsAllDone(argsJSON string) bool {
	var payload struct {
		Todos []struct {
			Status string `json:"status"`
		} `json:"todos"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &payload); err != nil || len(payload.Todos) == 0 {
		return false
	}
	for _, todo := range payload.Todos {
		switch strings.ToLower(strings.TrimSpace(todo.Status)) {
		case "completed", "cancelled":
		default:
			return false
		}
	}
	return true
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
	contextResult = appendModelContextNote(contextResult, payload.ModelContextNote)
	parts := a.toolResultParts(contextResult, payload.Images)

	a.emitToTUI(ToolResultEvent{
		CallID:      payload.CallID,
		Name:        payload.Name,
		ArgsJSON:    payload.ArgsJSON,
		Audit:       payload.Audit.Clone(),
		Result:      displayResult,
		Status:      toolResultStatusFromError(isError),
		Parts:       parts,
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
		Parts:           parts,
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
		a.persistAsync(identity.MainAgentID, toolMsg)
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
	for _, msg := range slices.Backward(msgs) {

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

func (a *MainAgent) toolResultParts(text string, images []message.ContentPart) []message.ContentPart {
	a.llmMu.RLock()
	client := a.llmClient
	a.llmMu.RUnlock()
	parts, dropped := toolResultPartsForCapability(text, images, client)
	if dropped.any() {
		a.emitToTUI(ToastEvent{Message: "The current model does not support " + dropped.summary() + " tool-result attachments; attachments were ignored", Level: "warn"})
	}
	return parts
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
		for _, msg := range slices.Backward(msgs) {

			if msg.Role != "assistant" || len(msg.ToolCalls) == 0 {
				continue
			}
			for _, tc := range msg.ToolCalls {
				if tc.ID == callID && tools.NormalizeName(tc.Name) == tools.NameSkill {
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
	a.turn.markToolCallCompleted(payload.CallID)
	a.loopState.markProgress()
	if payload.Error == nil {
		if isVerificationLikeToolResult(payload, rawToolResultForVerification(payload)) {
			a.loopState.markProgress()
		}
		if tools.NormalizeName(payload.Name) == tools.NameTodoWrite && todoWriteArgsAllDone(payload.ArgsJSON) {
			a.beginContextReductionWrapUpGrace()
		}
		if tools.NormalizeName(payload.Name) == tools.NameSkill {
			if skillName := toolCallSkillName(a.ctxMgr.Snapshot(), payload.CallID, payload.ArgsJSON); skillName != "" {
				a.MarkSkillInvokedByName(skillName)
			}
		}
	}

	rawResult := payload.Result
	displayResult, contextResult, errorText, isError := composeToolResultTexts(rawResult, payload.Error)
	contextResult = applyToolArgsAuditToContextResult(contextResult, payload.Audit)
	contextResult = appendModelContextNote(contextResult, payload.ModelContextNote)

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

	if payload.Name == tools.NameHandoff && payload.Error == nil {
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
	if payload.Name == tools.NameDone && payload.Error == nil {
		assistantMsg, ok := findAssistantMessageForToolCall(a.ctxMgr.Snapshot(), payload.CallID)
		assistantContent := ""
		if ok {
			assistantContent = assistantMsg.Content
		}
		a.pendingLoopExitResults = append(a.pendingLoopExitResults, &loopExitResult{CallID: payload.CallID, Reason: strings.TrimSpace(contextResult), AssistantContent: assistantContent, TurnID: a.turn.ID, ArgsJSON: payload.ArgsJSON})
	}

	deferToolResultEmission := payload.Name == tools.NameDone && payload.Error == nil
	parts := a.toolResultParts(contextResult, payload.Images)
	if !deferToolResultEmission {
		a.emitToTUI(ToolResultEvent{
			CallID:      payload.CallID,
			Name:        payload.Name,
			ArgsJSON:    payload.ArgsJSON,
			Audit:       payload.Audit.Clone(),
			Result:      displayResult,
			Status:      toolResultStatusFromError(isError),
			Parts:       parts,
			Diff:        payload.Diff,
			DiffAdded:   payload.DiffAdded,
			DiffRemoved: payload.DiffRemoved,
			FileCreated: payload.FileCreated,
		})
	}

	a.queueLSPDiagnosticOverlay(a.ctxMgr.Snapshot(), payload)
	if !deferToolResultEmission {
		toolMsg := message.Message{
			Role:            "tool",
			Content:         contextResult,
			Parts:           parts,
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
			a.persistAsync(identity.MainAgentID, toolMsg)
		}
		a.recordEvidenceFromMessage(toolMsg)
	}

	a.turn.CompletedToolCalls = append(a.turn.CompletedToolCalls, toolResultSummary(payload, contextResult, errorText))
	if changed := changedFileSummary(payload); changed != nil {
		a.loopState.markProgress()
		a.turn.ChangedFiles = append(a.turn.ChangedFiles, changed)
	}
	// Track malformed and empty-args calls. Both malformed sentinel args and
	// empty "{}" args for tools with required parameters count as abnormal.
	if isAbnormalToolArgs(a.tools, payload.Name, json.RawMessage(payload.ArgsJSON)) {
		a.turn.malformedInBatch++
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
			a.emitActivity("main", ActivityIdle, "")
			a.pausePendingUserDrainOnce = true
			a.setIdleAndDrainPending()
			return
		}
		if len(a.pendingLoopExitResults) > 0 {
			pendingResults := a.pendingLoopExitResults
			a.pendingLoopExitResults = nil
			if len(pendingResults) > 1 {
				for _, skipped := range pendingResults[:len(pendingResults)-1] {
					rejection := "Done rejected: only one Done call can be handled in a batch; keep a single final Done call after the remaining tool work is complete."
					a.persistLoopDoneToolResult(skipped.CallID, rejection)
					a.emitToTUI(ToolCallUpdateEvent{ID: skipped.CallID, Name: tools.NameDone, ArgsJSON: skipped.ArgsJSON, ArgsStreamingDone: true, AgentID: "main"})
					a.emitToTUI(ToolResultEvent{CallID: skipped.CallID, Name: tools.NameDone, ArgsJSON: skipped.ArgsJSON, Result: rejection, Status: ToolResultStatusSuccess})
				}
			}
			pending := pendingResults[len(pendingResults)-1]
			if a.loopState.Enabled {
				if a.loopExitConditionsSatisfied() {
					resp, err := a.awaitDoneConfirmation(a.turn.Ctx, pending.Reason, pending.ArgsJSON, pending.AssistantContent)
					if err != nil {
						log.Warnf("loop exit confirmation failed error=%v", err)
						a.emitToTUI(ToolCallUpdateEvent{ID: pending.CallID, Name: tools.NameDone, ArgsJSON: pending.ArgsJSON, ArgsStreamingDone: true, AgentID: "main"})
						a.emitToTUI(ToolResultEvent{CallID: pending.CallID, Name: tools.NameDone, ArgsJSON: pending.ArgsJSON, Result: a.loopExitInterceptLimitResult(), Status: ToolResultStatusSuccess})
						a.markLoopExitDecisionRequired()
						return
					} else if resp.Approved {
						report := strings.TrimSpace(pending.AssistantContent)
						if parsed, err := tools.ParseDoneArgs(json.RawMessage(pending.ArgsJSON)); err == nil {
							report = parsed.Report
						}
						if report == "" {
							report = "Done approved"
						}
						a.persistLoopDoneToolResult(pending.CallID, "Done approved")
						a.emitToTUI(ToolCallUpdateEvent{ID: pending.CallID, Name: tools.NameDone, ArgsJSON: pending.ArgsJSON, ArgsStreamingDone: true, AgentID: "main"})
						a.emitToTUI(ToolResultEvent{CallID: pending.CallID, Name: tools.NameDone, ArgsJSON: pending.ArgsJSON, Result: "Done approved", DoneReport: report, Status: ToolResultStatusSuccess})
						a.loopState.State = LoopStateCompleted
						a.emitLoopStateChanged()
						a.loopState.disable()
						a.emitLoopStateChanged()
						a.emitActivity("main", ActivityIdle, "")
						a.setIdleAndDrainPending()
						return
					} else {
						reason := normalizeDenyReason(resp.DenyReason)
						if reason == "" {
							reason = "User rejected loop exit and requires more work before Done."
						}
						a.loopState.Iteration = 0
						a.appendLoopContinuationAndContinue(pending.CallID, pending.ArgsJSON, "Done rejected: "+reason)
					}
				} else {
					if !a.autoRejectLoopExitAndContinue(pending.CallID, pending.ArgsJSON, a.loopExitRejectionToolResult()) {
						a.emitToTUI(ToolCallUpdateEvent{ID: pending.CallID, Name: tools.NameDone, ArgsJSON: pending.ArgsJSON, ArgsStreamingDone: true, AgentID: "main"})
						a.emitToTUI(ToolResultEvent{CallID: pending.CallID, Name: tools.NameDone, ArgsJSON: pending.ArgsJSON, Result: a.loopExitInterceptLimitResult(), Status: ToolResultStatusSuccess})
						a.markLoopExitDecisionRequired()
						return
					}
				}
			} else {
				// Non-loop mode: Done is a direct completion signal.
				// Show the report as the tool result and stop without confirmation UI.
				report := strings.TrimSpace(pending.AssistantContent)
				if parsed, err := tools.ParseDoneArgs(json.RawMessage(pending.ArgsJSON)); err == nil {
					report = parsed.Report
				}
				if report == "" {
					report = "Done"
				}
				a.persistLoopDoneToolResult(pending.CallID, report)
				a.emitToTUI(ToolCallUpdateEvent{ID: pending.CallID, Name: tools.NameDone, ArgsJSON: pending.ArgsJSON, ArgsStreamingDone: true, AgentID: "main"})
				a.emitToTUI(ToolResultEvent{CallID: pending.CallID, Name: tools.NameDone, ArgsJSON: pending.ArgsJSON, Result: report, DoneReport: report, Status: ToolResultStatusSuccess})
				a.emitActivity("main", ActivityIdle, "")
				a.setIdleAndDrainPending()
				return
			}
		}

		if a.turn == nil {
			return
		}

		log.Debugf("all tool calls complete, calling LLM again turn_id=%v", a.turn.ID)
		a.prepareSubAgentMailboxBatchForTurnContinuation()
		a.processPendingUserMessagesBeforeLLMInTurn()
		turnID := a.turn.ID
		turnCtx := a.turn.Ctx
		a.beginMainLLMAfterPreparation(turnCtx, turnID, "")
	}
}
