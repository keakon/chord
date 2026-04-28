package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

// failPendingToolCalls cancels all pending and streaming tool calls for a turn
// after a terminal error, emitting cancelled results to TUI.
func (a *MainAgent) failPendingToolCalls(turn *Turn, err error) {
	if turn == nil {
		return
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
	slog.Warn("failing pending tool calls after terminal turn error",
		"turn_id", turn.ID,
		"pending_tools", pending,
		"failed_tools", len(merged),
		"error", err,
	)
	persistedResults := a.persistInterruptedToolResults(failedExec, ToolResultStatusError, err)
	if persistedResults > 0 {
		slog.Info("persisted failed tool-call results after terminal turn error",
			"turn_id", turn.ID,
			"count", persistedResults,
		)
	}
	emitFailedToolResults(a.emitToTUI, merged, err)
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
		slog.Warn("skipping synthetic tool persistence for call_ids absent from assistant history",
			"dropped", orig-len(calls),
		)
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
			Audit:      call.Audit.Clone(),
		}
		a.ctxMgr.Append(toolMsg)
		if call.Name == "Skill" {
			a.MarkSkillInvokedByName(extractToolArgument(call.Name, json.RawMessage(call.ArgsJSON)))
		}
		if a.recovery != nil {
			a.persistAsync("main", toolMsg)
		}
		persisted++
	}
	return persisted
}

// handleToolResult processes a single tool execution result. When all pending
// tool calls for the current turn have completed, a new LLM call is initiated
// to let the model decide what to do next.
func (a *MainAgent) handleToolResult(evt Event) {
	// Turn isolation.
	if a.turn == nil || evt.TurnID != a.turn.ID {
		slog.Debug("discarding stale tool result",
			"event_turn", evt.TurnID,
			"current_turn", a.currentTurnID(),
		)
		return
	}

	payload, ok := evt.Payload.(*ToolResultPayload)
	if !ok {
		slog.Error("handleToolResult: invalid payload type",
			"payload_type", fmt.Sprintf("%T", evt.Payload),
		)
		return
	}
	a.turn.resolvePendingToolCall(payload.CallID)
	a.loopState.markProgress()

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
		slog.Warn("on_before_tool_result_append hook error", "error", hookErr)
	} else if hookResult != nil {
		switch hookResult.Action {
		case hook.ActionBlock:
			slog.Warn("on_before_tool_result_append returned block; ignoring")
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

	// Detect Handoff tool result during plan generation.
	// Defer the role switch + confirm flow until all sibling tool calls in this
	// batch have completed. This prevents switchRole from clearing conversation
	// state while other tools are still running (e.g. a co-returned Write call).
	if payload.Name == "Handoff" && payload.Error == nil {
		var pcData struct {
			PlanPath string `json:"plan_path"`
		}
		if err := json.Unmarshal([]byte(payload.Result), &pcData); err != nil {
			slog.Error("handleToolResult: failed to parse Handoff result", "error", err)
		} else {
			slog.Info("Handoff result received; deferring until sibling tools complete",
				"plan_path", pcData.PlanPath,
				"pending", a.turn.PendingToolCalls.Load()-1,
			)
			a.pendingHandoff = &HandoffResult{
				PlanPath: pcData.PlanPath,
			}
		}
		// Fall through: record the tool result, decrement counter, and check
		// completion below — do NOT return early.
	}

	// Emit to TUI.
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

	// Record the tool result in the conversation.
	toolMsg := message.Message{
		Role:            "tool",
		Content:         contextResult,
		ToolCallID:      payload.CallID,
		ToolDiff:        payload.Diff,
		ToolDiffAdded:   payload.DiffAdded,
		ToolDiffRemoved: payload.DiffRemoved,
		ToolDurationMs:  payload.Duration.Milliseconds(),
		LSPReviews:      append([]message.LSPReview(nil), payload.LSPReviews...),
		Audit:           payload.Audit.Clone(),
	}
	a.ctxMgr.Append(toolMsg)
	if payload.Name == "Skill" {
		a.MarkSkillInvokedByName(extractToolArgument(payload.Name, json.RawMessage(payload.ArgsJSON)))
	}
	a.recordEvidenceFromMessage(toolMsg)

	// Persist tool result for crash recovery.
	if a.recovery != nil {
		a.persistAsync("main", toolMsg)
	}

	a.turn.CompletedToolCalls = append(a.turn.CompletedToolCalls, toolResultSummary(payload, contextResult, errorText))
	if changed := changedFileSummary(payload); changed != nil {
		a.loopState.markProgress()
		a.turn.ChangedFiles = append(a.turn.ChangedFiles, changed)
	}
	if isVerificationLikeToolResult(payload, contextResult) {
		a.loopState.markVerificationProgress()
	}

	// Decrement pending counter and track malformed args across rounds
	// (improvement 3: abort turn if the model repeatedly produces malformed args).
	a.turn.PendingToolCalls.Add(-1)
	// Track malformed and empty-args calls (improvement 3 + 4).
	// Both sentinel malformed args and empty "{}" args for tools with
	// required parameters count toward the consecutive-malformed threshold.
	if llm.IsMalformedArgs(json.RawMessage(payload.ArgsJSON)) {
		a.turn.malformedInBatch++
	} else if llm.IsEmptyArgs(json.RawMessage(payload.ArgsJSON)) {
		if tool, ok := a.tools.Get(payload.Name); ok {
			if req := llm.RequiredFields(tool.Parameters()); len(req) > 0 {
				a.turn.malformedInBatch++
			}
		}
	}

	slog.Debug("tool result processed",
		"name", payload.Name,
		"call_id", payload.CallID,
		"is_error", isError,
		"pending", a.turn.PendingToolCalls.Load(),
		"malformed_in_batch", a.turn.malformedInBatch,
	)

	// When all tool results are in, either start the next finalize-time batch or continue.
	if a.turn.PendingToolCalls.Load() <= 0 {
		if a.turn.activeToolBatchCancel != nil {
			a.turn.activeToolBatchCancel()
			a.turn.activeToolBatchCancel = nil
		}
		if a.turn.nextToolBatch < len(a.turn.toolExecutionBatches) {
			a.startNextToolBatch(a.turn)
			return
		}
		abnormalInBatch := a.turn.malformedInBatch
		a.turn.toolExecutionBatches = nil
		a.turn.nextToolBatch = 0

		// Update the consecutive-malformed-round counter.
		if abnormalInBatch > 0 {
			a.turn.MalformedCount++
			// Partial batch degradation warning: some tool calls in this
			// batch had empty or malformed arguments. This often indicates
			// the model's output was truncated near max_tokens.
			slog.Warn("batch contained abnormal tool call arguments",
				"abnormal_count", abnormalInBatch,
				"consecutive_rounds", a.turn.MalformedCount,
			)
		} else {
			a.turn.MalformedCount = 0
		}
		a.turn.malformedInBatch = 0

		// Abort the turn when the model has produced malformed args for too
		// many consecutive LLM rounds. This prevents an infinite loop where
		// the model keeps calling tools with unparseable JSON arguments.
		if a.turn.MalformedCount >= maxMalformedToolCalls {
			slog.Warn("aborting turn: too many consecutive malformed tool call rounds",
				"count", a.turn.MalformedCount,
				"threshold", maxMalformedToolCalls,
			)
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
			slog.Warn("on_tool_batch_complete hook error", "error", err)
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
		if a.turn.activeToolBatchCancel != nil {
			a.turn.activeToolBatchCancel()
			a.turn.activeToolBatchCancel = nil
		}
		a.turn.CompletedToolCalls = nil
		a.turn.ChangedFiles = nil

		// If PlanComplete was deferred, finalize it now that all sibling
		// tools have finished.
		if a.pendingHandoff != nil {
			pc := a.pendingHandoff
			a.pendingHandoff = nil

			slog.Info("all sibling tools complete; finalizing Handoff",
				"plan_path", pc.PlanPath)

			a.lastPlanPath = pc.PlanPath

			// Notify TUI to prompt the user to select a target agent.
			// The TUI will call ExecutePlan(planPath, agentName) with the chosen agent,
			// or do nothing if the user cancels.
			a.emitToTUI(HandoffEvent{
				PlanPath: pc.PlanPath,
			})
			return
		}

		slog.Debug("all tool calls complete, calling LLM again", "turn_id", a.turn.ID)

		// Let urgent/decision mailbox updates join the next automatic main-agent
		// continuation once the current tool batch has fully settled.
		a.prepareSubAgentMailboxBatchForTurnContinuation()

		// Merge any user messages queued while tools were running into this round
		// so the model sees tool results and new user input in one request.
		a.processPendingUserMessagesBeforeLLMInTurn()

		turnID := a.turn.ID
		turnCtx := a.turn.Ctx
		// Pre-request gate (durable compaction) runs inside beginMainLLMAfterPreparation.
		a.beginMainLLMAfterPreparation(turnCtx, turnID, "")
	}
}
