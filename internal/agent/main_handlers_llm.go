package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/keakon/golog/log"
	"strings"
	"time"

	"github.com/keakon/chord/internal/agent/agentdiff"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// maxMalformedToolCalls is the number of consecutive LLM rounds with abnormal
// tool call arguments (malformed sentinel or empty args for required-param
// tools) before the turn is aborted. Prevents infinite loops when the model
// cannot generate valid tool arguments due to output truncation or capability
// limits.
const maxMalformedToolCalls = 3

// sanitizeToolCallArgs replaces the malformed-args sentinel in tool calls with
// an empty object {} before the assistant message is stored in conversation
// history. This prevents the confusing error JSON from being replayed to the
// API on subsequent turns (improvement 2).
func sanitizeToolCallArgs(calls []message.ToolCall) []message.ToolCall {
	if len(calls) == 0 {
		return calls
	}
	out := make([]message.ToolCall, len(calls))
	for i, tc := range calls {
		if llm.IsMalformedArgs(tc.Args) {
			tc.Args = json.RawMessage("{}")
		}
		out[i] = tc
	}
	return out
}

// isMalformedToolCall reports whether a tool call has malformed or effectively
// empty arguments — either the sentinel from the streaming parser (invalid JSON)
// or an empty "{}" for tools that declare required parameters (truncation artifact).
func isMalformedToolCall(tc message.ToolCall, registry *tools.Registry) bool {
	if llm.IsMalformedArgs(tc.Args) {
		return true
	}
	if llm.IsEmptyArgs(tc.Args) {
		if tool, ok := registry.Get(tc.Name); ok {
			if req := llm.RequiredFields(tool.Parameters()); len(req) > 0 {
				return true
			}
		}
	}
	return false
}

// handleLLMResponse processes a completed LLM response. If the response
// contains tool calls they are executed in parallel; otherwise an IdleEvent is
// emitted.
//
// Malformed tool calls (sentinel args or empty args for required-param tools)
// are separated from valid ones. If the response was truncated (max_tokens/length)
// and contains malformed calls, the entire response is discarded and the LLM is
// retried without storing anything in conversation history. This breaks the
// positive feedback loop where malformed args → error → bigger context → more
// truncation.
func (a *MainAgent) handleLLMResponse(evt Event) {
	// Turn isolation: discard stale responses.
	if a.turn == nil || evt.TurnID != a.turn.ID {
		log.Debugf("discarding stale LLM response event_turn=%v current_turn=%v", evt.TurnID, a.currentTurnID())
		return
	}

	payload, ok := evt.Payload.(*LLMResponsePayload)
	if !ok {
		log.Errorf("handleLLMResponse: invalid payload type payload_type=%v", fmt.Sprintf("%T", evt.Payload))
		return
	}
	handledAt := time.Now()
	a.recordToolTraceLLMResponseHandled(payload, handledAt)
	// Finalized response received: snapshot any streamed partial text first for
	// diagnostics, then clear it so later replacement/cancel paths do not persist
	// a duplicate interrupted assistant message for a round that already reached
	// finalize.
	streamedText := a.turn.drainPartialText()
	if strings.TrimSpace(streamedText) != "" || strings.TrimSpace(payload.Content) != "" {
		log.Debugf("main finalize assistant payload turn_id=%v final_content_len=%v streamed_text_len=%v tool_calls=%v thinking_blocks=%v stop_reason=%v", evt.TurnID, len(payload.Content), len(streamedText), len(payload.ToolCalls), len(payload.ThinkingBlocks), payload.StopReason)
	}
	if strings.TrimSpace(streamedText) != "" && strings.TrimSpace(payload.Content) == "" {
		log.Warnf("main finalize lost streamed assistant text turn_id=%v streamed_text_len=%v tool_calls=%v thinking_blocks=%v stop_reason=%v", evt.TurnID, len(streamedText), len(payload.ToolCalls), len(payload.ThinkingBlocks), payload.StopReason)
	}

	// --- Classify tool calls as valid or malformed ---
	var validCalls, malformedCalls []message.ToolCall
	for _, tc := range payload.ToolCalls {
		if isMalformedToolCall(tc, a.tools) {
			malformedCalls = append(malformedCalls, tc)
		} else {
			validCalls = append(validCalls, tc)
		}
	}
	if a.turn.InLengthRecovery && len(validCalls) > 1 {
		log.Warnf("length recovery response returned multiple tool calls; forcing another recovery round tool_call_count=%v recovery_attempt=%v", len(validCalls), a.turn.LengthRecoveryCount)
		malformedCalls = append(malformedCalls, validCalls[1:]...)
		validCalls = validCalls[:1]
	}

	// --- Diagnostic logging (Fix 5) ---
	isTruncated := payload.StopReason == "max_tokens" || payload.StopReason == "length"
	malformedToolNames := make([]string, 0, len(malformedCalls))
	for _, tc := range malformedCalls {
		malformedToolNames = append(malformedToolNames, tc.Name)
	}
	if len(malformedCalls) > 0 {
		log.Warnf("malformed tool calls detected in LLM response total_tool_calls=%v malformed_count=%v valid_count=%v stop_reason=%v last_input_tokens=%v instance=%v hint=%v", len(payload.ToolCalls), len(malformedCalls), len(validCalls), payload.StopReason, a.ctxMgr.LastInputTokens(), a.instanceID, "see prior LLM log line with raw_args or partial_args for the invalid payload")
	}

	// --- Break feedback loop (Fix 2) ---
	// If the response was truncated and has malformed calls, or if ALL calls
	// are malformed, discard the entire response and retry the LLM call.
	// This prevents the context from growing with useless malformed entries
	// that cause further truncation on the next round.
	if len(malformedCalls) > 0 && (isTruncated || len(validCalls) == 0) {
		if isTruncated {
			a.turn.LastTruncatedToolName = truncatedToolName(malformedToolNames)
			if a.turn.LengthRecoveryCount < maxLengthRecoveryAttempts {
				a.turn.LengthRecoveryCount++
				log.Warnf("LLM output truncated during tool argument generation; retrying with recovery prompt tool=%v recovery_attempt=%v max_attempts=%v stop_reason=%v", a.turn.LastTruncatedToolName, a.turn.LengthRecoveryCount, maxLengthRecoveryAttempts, payload.StopReason)
				a.emitToTUI(ToastEvent{
					Message: "Response hit the output limit; retrying with a smaller next step",
					Level:   "warn",
				})
				turnID := a.turn.ID
				turnCtx := a.turn.Ctx
				a.beginLengthRecoveryRetry(a.turn.LastTruncatedToolName, turnID, turnCtx)
				return
			}
			a.turn.MalformedCount++

			if isTruncated {
				log.Warnf("LLM output truncated with malformed tool calls; discarding response and retrying malformed_count=%v valid_count=%v consecutive_rounds=%v stop_reason=%v", len(malformedCalls), len(validCalls), a.turn.MalformedCount, payload.StopReason)
			} else {
				log.Warnf("all tool calls malformed; discarding response and retrying malformed_count=%v consecutive_rounds=%v", len(malformedCalls), a.turn.MalformedCount)
			}

			// Abort the turn if too many consecutive retries.
			if a.turn.MalformedCount >= maxMalformedToolCalls {
				if isTruncated && a.scheduleCompactionForLengthRecovery() {
					a.emitToTUI(ToastEvent{Message: "Response still hit the output limit; compacting context before retry", Level: "warn"})
					a.discardSpeculativeStreamToolsAndClearToolTrace(a.turn)
					return
				}
				log.Warnf("aborting turn: too many consecutive malformed tool call rounds count=%v threshold=%v", a.turn.MalformedCount, maxMalformedToolCalls)
				compactHint := ""
				if a.autoCompactRequested.Load() || a.compactionTriggerForMainLLM().needed() {
					compactHint = " Try /compact before continuing."
				} else {
					compactHint = " Consider starting a new session or running /compact before continuing."
				}
				a.emitToTUI(ErrorEvent{
					Err: fmt.Errorf(
						"turn aborted: the model produced malformed tool call arguments "+
							"%d times in a row (output truncation). This usually indicates "+
							"the context is too large. Please start a new conversation or "+
							"reduce the complexity of your request. You can also increase "+
							"max_output_tokens in config to allow longer outputs.%s",
						a.turn.MalformedCount,
						compactHint,
					),
				})
				a.emitToTUI(ToastEvent{Message: strings.TrimSpace(compactHint), Level: "warn"})
				a.discardSpeculativeStreamToolsAndClearToolTrace(a.turn)
				a.setIdleAndDrainPending()
				return
			}

		}
		a.discardSpeculativeStreamToolsAndClearToolTrace(a.turn)
		a.prepareSubAgentMailboxBatchForTurnContinuation()
		// Retry LLM call without storing the malformed response.
		turnID := a.turn.ID
		turnCtx := a.turn.Ctx
		a.beginMainLLMAfterPreparation(turnCtx, turnID, "")
		return
	}
	a.turn.InLengthRecovery = false
	a.turn.LengthRecoveryCount = 0
	a.turn.LastTruncatedToolName = ""

	compatCfg := a.thinkingToolcallCompat()
	compatEnabled := compatCfg != nil && compatCfg.EnabledValue()
	driftDetected := compatEnabled &&
		payload.ThinkingToolcallMarkerHit &&
		len(validCalls) == 0 &&
		payload.StopReason == "stop"

	// Detect provider drift: pseudo tool-call templates in thinking, but no
	// structured tool_calls returned. Try to parse them from reasoning text.
	if driftDetected {
		parsed := parseThinkingToolcalls(payload.ReasoningContent)
		if len(parsed) > 0 {
			log.Warnf("thinking-toolcall format drift detected; parsed pseudo tool calls from reasoning compat_thinking_toolcall_enabled=%v thinking_toolcall_marker_hit=%v parsed_tool_calls=%v", compatEnabled, payload.ThinkingToolcallMarkerHit, len(parsed))
			// Replace validCalls with parsed pseudo calls so they proceed
			// through the normal execution path below.
			validCalls = parsed
			a.discardSpeculativeStreamToolsAndClearToolTrace(a.turn)

			// Emit ToolCallStartEvent for each parsed tool call to update the TUI.
			// Standard tool calls emit this during streaming; pseudo calls must
			// emit it now after parsing completes.
			for _, tc := range validCalls {
				a.emitToTUI(ToolCallStartEvent{
					ID:       tc.ID,
					Name:     tc.Name,
					ArgsJSON: string(tc.Args),
				})
			}
		} else {
			log.Warnf("thinking-toolcall format drift detected; could not parse pseudo tool calls, falling back to idle compat_thinking_toolcall_enabled=%v thinking_toolcall_marker_hit=%v", compatEnabled, payload.ThinkingToolcallMarkerHit)
			// Debug: log the raw reasoning content for troubleshooting
			if payload.ReasoningContent != "" {
				reasoningLen := len(payload.ReasoningContent)
				preview := payload.ReasoningContent
				if reasoningLen > 500 {
					preview = payload.ReasoningContent[:500] + "...(truncated)"
				}
				log.Debugf("unparseable reasoning content reasoning_len=%v reasoning_preview=%v", reasoningLen, preview)
			}
			a.emitToTUI(InfoEvent{
				Message: "Detected provider thinking pseudo tool-call templates but could not parse them. Please retry or switch model.",
			})
			a.discardSpeculativeStreamToolsAndClearToolTrace(a.turn)
			a.setIdleAndDrainPending()
			return
		}
	}

	// Append the assistant message to the context.
	// Use only valid tool calls — malformed ones are dropped from history
	// so the context doesn't grow with useless entries. Sanitize remaining
	// calls as a safety net (no-op for valid calls, but defensive).
	sanitizedToolCalls := sanitizeToolCallArgs(validCalls)
	assistantMsg := message.Message{
		Role:           "assistant",
		Content:        payload.Content,
		ThinkingBlocks: payload.ThinkingBlocks,
		ToolCalls:      sanitizedToolCalls,
		StopReason:     payload.StopReason,
	}
	assistantMsg.Usage = payload.Usage
	a.ctxMgr.Append(assistantMsg)

	// Emit finalized assistant message event for control-plane consumers.
	a.emitToTUI(AssistantMessageEvent{
		Text:      payload.Content,
		ToolCalls: len(sanitizedToolCalls),
	})

	// Persist assistant message for crash recovery (including usage for session resume).
	if a.recovery != nil {
		a.persistAsync("main", assistantMsg)
	}

	// No valid tool calls → agent is idle, waiting for the next user message.
	if len(validCalls) == 0 {
		a.discardSpeculativeStreamToolsAndClearToolTrace(a.turn)
		if payload.StopReason == "tool_calls" {
			log.Warnf("LLM response stop_reason=tool_calls but no tool calls parsed; going idle total_tool_calls=%v malformed_count=%v", len(payload.ToolCalls), len(malformedCalls))
		} else {
			log.Debug("LLM response has no tool calls, agent going idle")
		}
		if assessment := a.nextLoopAssessmentFromAssistant(assistantMsg); assessment != nil {
			a.turn = nil
			a.sendEvent(Event{Type: EventLoopAssessment, Payload: assessment})
			return
		}
		a.emitActivity("main", ActivityIdle, "")
		a.setIdleAndDrainPending()
		return
	}

	// Execute finalized tool calls in concurrency-safe batches.
	batches := buildToolExecutionBatches(a.tools, validCalls)
	a.turn.toolExecutionBatches = batches
	a.turn.nextToolBatch = 0
	a.turn.activeToolBatchCancel = nil
	a.turn.TotalToolCalls.Store(int32(len(validCalls)))
	a.emitActivity("main", ActivityExecuting, fmt.Sprintf("%d tools", len(validCalls)))
	turnID := a.turn.ID

	log.Debugf("executing tool calls count=%v batches=%v turn_id=%v", len(validCalls), len(batches), turnID)

	for _, tc := range validCalls {
		a.turn.recordPendingToolCall(PendingToolCall{CallID: tc.ID, Name: tc.Name, ArgsJSON: string(tc.Args)})
	}
	if len(batches) > 1 {
		queued := make([]PendingToolCall, 0, len(validCalls))
		for _, batch := range batches[1:] {
			for _, tc := range batch.Calls {
				queued = append(queued, PendingToolCall{CallID: tc.ID, Name: tc.Name, ArgsJSON: string(tc.Args)})
			}
		}
		emitToolExecutionState(a.emitToTUI, queued, ToolCallExecutionStateQueued)
	}
	if len(batches) > 0 {
		a.startNextToolBatch(a.turn)
	}
	streamingSnapshot := a.turn.snapshotStreamingToolCalls()
	validCallIDs := make(map[string]struct{}, len(validCalls))
	for _, tc := range validCalls {
		validCallIDs[tc.ID] = struct{}{}
	}
	finalizeStreamingToolCards(a.emitToTUI, validCallIDs, a.turn)
	if len(streamingSnapshot) > 0 {
		// Drop traces for speculative tool calls that didn't survive validation.
		orphans := make([]PendingToolCall, 0, len(streamingSnapshot))
		for _, c := range streamingSnapshot {
			if _, ok := validCallIDs[c.CallID]; ok {
				continue
			}
			orphans = append(orphans, c)
		}
		a.clearToolTraceForCalls(orphans)
	}
}

func (a *MainAgent) startNextToolBatch(turn *Turn) {
	if a == nil || turn == nil || a.turn == nil || a.turn != turn {
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
	turnID := turn.ID
	batchPending := make([]PendingToolCall, 0, len(batch.Calls))
	for _, tc := range batch.Calls {
		batchPending = append(batchPending, PendingToolCall{CallID: tc.ID, Name: tc.Name, ArgsJSON: string(tc.Args)})
	}
	now := time.Now()
	for _, call := range batchPending {
		a.logToolTraceExecutionRunning(call, now)
	}
	emitToolExecutionState(a.emitToTUI, batchPending, ToolCallExecutionStateRunning)
	for _, tc := range batch.Calls {
		go func(tc message.ToolCall) {
			startedAt := time.Now()
			execResult, err := a.executeToolCall(batchCtx, tc)
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
			a.sendEvent(Event{
				Type:   EventToolResult,
				TurnID: turnID,
				Payload: &ToolResultPayload{
					CallID:      tc.ID,
					Name:        tc.Name,
					ArgsJSON:    execResult.EffectiveArgsJSON,
					Audit:       execResult.Audit,
					Result:      execResult.Result,
					Error:       err,
					TurnID:      turnID,
					Duration:    time.Since(startedAt),
					Diff:        diff.Text,
					DiffAdded:   diff.Added,
					DiffRemoved: diff.Removed,
					FileCreated: tc.Name == tools.NameWrite && !execResult.PreExisted,
					LSPReviews:  append([]message.LSPReview(nil), execResult.LSPReviews...),
				},
			})
		}(tc)
	}
}

// savePartialAssistantMsgForTurn drains any accumulated streaming text from the
// provided turn and, if non-empty, appends a partial assistant message to the
// context so the model can see what it had already written when it resumes.
// Only pure-text content is saved; incomplete tool calls are intentionally
// dropped because dangling tool_use blocks would cause API errors.
func (a *MainAgent) savePartialAssistantMsgForTurn(turn *Turn) {
	if turn == nil {
		return
	}
	text := turn.drainPartialText()
	if strings.TrimSpace(text) == "" {
		return
	}
	msg := message.Message{
		Role:       "assistant",
		Content:    text,
		StopReason: "interrupted",
	}
	a.ctxMgr.Append(msg)
	if a.recovery != nil {
		a.persistAsync("main", msg)
	}
	log.Debugf("saved partial assistant message after stream interruption len=%v turn_id=%v", len(text), turn.ID)
}

// savePartialAssistantMsg drains any accumulated streaming text from the
// current turn and, if non-empty, appends a partial assistant message to the
// context so the model can see what it had already written when it resumes.
// Only pure-text content is saved; incomplete tool calls are intentionally
// dropped because dangling tool_use blocks would cause API errors.
func (a *MainAgent) savePartialAssistantMsg() {
	a.savePartialAssistantMsgForTurn(a.turn)
}
