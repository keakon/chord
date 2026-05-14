package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
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
	log.Warnf("failing pending tool calls after terminal turn error turn_id=%v pending_tools=%v failed_tools=%v error=%v", turn.ID, pending, len(merged), err)
	persistedResults := a.persistInterruptedToolResults(failedExec, ToolResultStatusError, err)
	if persistedResults > 0 {
		log.Infof("persisted failed tool-call results after terminal turn error turn_id=%v count=%v", turn.ID, persistedResults)
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
	displayResult, contextResult, _, isError := composeToolResultTexts(rawResult, payload.Error)
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
		ToolStatus:      string(toolResultStatusFromError(isError)),
		Audit:           payload.Audit.Clone(),
		LSPReviews:      append([]message.LSPReview(nil), payload.LSPReviews...),
		FileState:       payload.FileState.Clone(),
	}
	a.ctxMgr.Append(toolMsg)
	if a.recovery != nil {
		a.persistAsync("main", toolMsg)
	}

	remaining := a.turn.PendingToolCalls.Add(-1)
	if remaining < 0 {
		log.Warnf("PendingToolCalls went negative after tool result turn_id=%v call_id=%v", a.turn.ID, payload.CallID)
		a.turn.PendingToolCalls.Store(0)
		remaining = 0
	}
	if remaining == 0 {
		a.turn.TotalToolCalls.Store(0)
		a.turn.activeToolBatchCancel = nil
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
