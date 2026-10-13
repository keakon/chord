package agent

import (
	"fmt"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

// settleMainLLMResponse persists a completed response and settles its tools.
// Recovery decisions belong to the caller; this path never replaces a response.
func (a *MainAgent) settleMainLLMResponse(payload *LLMResponsePayload, validCalls, malformedCalls []message.ToolCall, isTruncated, wasLengthRecovery bool) {
	// Append the assistant message to the context.
	// The caller selects the final trajectory. Paused requests retain even
	// malformed calls so every intent receives an explicit terminal result.
	// Replace malformed argument sentinels with replayable JSON.
	sanitizedToolCalls := sanitizeToolCallArgs(validCalls)
	thinkingBlocks := payload.ThinkingBlocks
	responsesOutput := payload.ResponsesOutput
	geminiParts := payload.GeminiParts
	reasoningContent := payload.ReasoningContent
	if !toolCallTrajectoriesEqual(payload.ToolCalls, validCalls) {
		// Provider-native reasoning/signature payloads describe the complete tool
		// trajectory. Replaying them after filtering, truncating, or replacing a
		// call would claim that an unexecuted call is still part of the history.
		thinkingBlocks = nil
		responsesOutput = nil
		geminiParts = nil
		reasoningContent = ""
		for i := range sanitizedToolCalls {
			sanitizedToolCalls[i].ThoughtSignature = ""
		}
	}
	assistantMsg := message.Message{
		NativeTools:      payload.NativeTools,
		Role:             "assistant",
		FileLinkBaseDir:  a.effectiveToolBaseDir(),
		Content:          payload.Content,
		ThinkingBlocks:   thinkingBlocks,
		ResponsesOutput:  responsesOutput,
		GeminiParts:      geminiParts,
		ReasoningContent: reasoningContent,
		ToolCalls:        sanitizedToolCalls,
		StopReason:       payload.StopReason,
		RequestBatch:     payload.RequestBatch,
		Provenance:       mainAssistantProvenance(a),
		// Usage is retained on the in-memory message for external-session export;
		// session restore reads usage.jsonl as the sole analytics source.
		Usage: payload.Usage,
	}
	a.attachQuestionConsumption(&assistantMsg, payload.QuestionResults)
	a.ctxMgr.Append(assistantMsg)

	// Emit finalized assistant message event for control-plane consumers.
	a.emitToTUI(AssistantMessageEvent{FileLinkBaseDir: a.effectiveToolBaseDir(),
		Text:      payload.Content,
		ToolCalls: len(sanitizedToolCalls),
	})

	// Persist assistant message for crash recovery, and capture its write result
	// so the intent barrier below can gate tool dispatch on it.
	persistBarrier := make(chan error, 1)
	persistPending := false
	if a.recoveryManager() != nil {
		nativeJournal := recovery.NativeRequestJournal{SessionDir: a.SessionDir(), AgentID: identity.MainAgentID}
		nativeEpoch := a.sessionEpoch
		nativeBarrier := newNativeReceiptPersistence(assistantMsg)
		if nativeBarrier != nil {
			a.nativeReceipt.Store(nativeBarrier)
		}
		persistPending = a.persistAsyncAfter(identity.MainAgentID, assistantMsg, func(err error) {
			if err == nil {
				err = nativeJournal.Acknowledge(assistantMsg.NativeTools)
			}
			a.notePersistenceFailure(err)
			nativeBarrier.finish(err)
			persistBarrier <- err
			if err == nil && assistantMsg.NativeTools != nil {
				a.sendEvent(Event{Type: EventNativeReceipt, Payload: nativeReceiptPayload{epoch: nativeEpoch, receipt: assistantMsg.NativeTools}})
			}
			if err == nil && len(payload.QuestionResults) > 0 {
				a.sendEvent(Event{Type: EventQuestionConsumed, Payload: payload.QuestionResults})
			}
		})
		if !persistPending {
			nativeBarrier.finish(errPersistenceQueueUnavailable)
		}
	} else {
		a.consumeQuestionResults(payload.QuestionResults)
	}

	// Thinking translation is a best-effort post-processing enhancement. It is
	// scheduled only after the assistant message is finalized and persisted. The
	// TUI applies it only to settled thinking cards, so it must not affect the main
	// response or streaming display.
	a.maybeTranslateLatestThinkingAfterIdle(a.turn.ID)

	// Paused responses stay owned by this turn until speculative work and
	// persistence drain. No completion or recovery heuristic may discard them.
	if a.sessionExecutionPaused() && len(validCalls) == 0 {
		a.discardSpeculativeStreamToolsAndClearToolTrace(a.turn, "no_valid_calls")
		return
	}

	// No valid tool calls → agent is idle, waiting for the next user message.
	if len(validCalls) == 0 {
		// This response owns no tool calls, so the request window it belongs to
		// is already over and a deferred model-pool switch may land here. A
		// response that does carry tool calls keeps the switch pending until the
		// next request is prepared (beginMainLLMAfterPreparation): those calls
		// were emitted against the running model's tool surface and stay
		// validated against it for their whole execution.
		a.applyPendingModelPoolSwitchesAtRequestBoundary()
		a.discardSpeculativeStreamToolsAndClearToolTrace(a.turn, "no_valid_calls")
		switch {
		case payload.StopReason == "tool_calls":
			log.Warnf("LLM response stop_reason=tool_calls but no tool calls parsed; going idle total_tool_calls=%v malformed_count=%v", len(payload.ToolCalls), len(malformedCalls))
		case isTruncated:
			// Reached only with visible text: the empty-content case retried or
			// aborted earlier. The text is kept because it is partial work the
			// user may still want, but it is not a complete reply.
			log.Warnf("LLM response truncated after producing text; going idle with partial reply stop_reason=%v content_len=%v", payload.StopReason, len(payload.Content))
			a.emitToTUI(ToastEvent{
				Message: "Response hit the output limit; the reply above is incomplete",
				Level:   "warn",
			})
		default:
			log.Debug("LLM response has no tool calls, agent going idle")
		}
		if a.parkForRequiredQuestions() {
			return
		}
		if assessment := a.nextLoopAssessmentFromAssistant(assistantMsg); assessment != nil {
			a.rememberIdleTurn(a.turn.ID)
			a.clearPendingThinkingReplay()
			a.turn = nil
			a.pendingModelDriven = nil
			a.queueLoopEvent(Event{Type: EventLoopAssessment, Payload: assessment})
			return
		}
		a.closeCompletedQuestions()
		a.emitActivity("main", ActivityIdle, "")
		a.setIdleAndDrainPending()
		return
	}

	// Execute finalized tool calls in concurrency-safe batches.
	batches := a.toolExecutionPipeline().buildToolExecutionBatches(validCalls)

	// Intent barrier: the assistant message carrying these tool calls must be
	// process-crash durable before any tool body can produce side effects.
	if len(batches) > 0 && a.recoveryManager() != nil {
		if err := a.waitPersistBarrier(persistBarrier, persistPending); err != nil {
			a.failIntentBarrier(validCalls, err)
			return
		}
		a.turn.BarrierFailureRounds = 0
		a.markPersistenceRecoveredAfterBarrier()
		a.consumeQuestionResults(payload.QuestionResults)
	}

	a.turn.noteDispatchedToolRound(validCalls, wasLengthRecovery)
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
	streamingSnapshot := a.turn.snapshotStreamingToolCalls()
	validCallIDs := make(map[string]struct{}, len(validCalls))
	for _, tc := range validCalls {
		validCallIDs[tc.ID] = struct{}{}
	}
	var discardInfo map[string]StreamingToolDiscardInfo
	if len(streamingSnapshot) > 0 && a.turn.streamingToolExec != nil {
		discarded := a.turn.streamingToolExec.DiscardExceptInfo(validCallIDs, "filtered")
		logStreamingToolDiscardInfo("filtered", discarded)
		if len(discarded) > 0 {
			discardInfo = make(map[string]StreamingToolDiscardInfo, len(discarded))
			for _, it := range discarded {
				discardInfo[it.CallID] = it
			}
		}
	}
	finalizeStreamingToolCards(a.emitToTUI, validCallIDs, discardInfo, a.turn)
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
	if len(batches) > 0 {
		a.startNextToolBatch(a.turn)
	}
}
