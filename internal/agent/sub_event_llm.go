package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func (s *SubAgent) handleLLMResponse(result *llmResult) {
	// Turn isolation: discard stale responses.
	if s.turn == nil || result.turnID != s.turn.ID {
		log.Debugf("SubAgent: discarding stale LLM response agent=%v result_turn=%v current_turn=%v", s.instanceID, result.turnID, s.currentTurnID())
		return
	}
	// A completed request is real worker progress: refresh the heartbeat and
	// clear the silence-watchdog recovery tally so a healthy round trip never
	// counts against future watchdog budgets.
	s.markActivity()
	s.llmSilenceRecoveries = 0

	// The request that produced this result has ended. Realign the displayed
	// worker identity with the sticky cursor before any recovery restart: an
	// attempt that never emitted visible output must not keep defining it.
	s.syncRunningModelRefToCursorHead()
	if result.err != nil {
		if s.recoverFromContextLength(result.err) {
			return
		}
		if llm.IsRoutingInvalidated(result.err) {
			// Model routing changed mid-request (e.g. this sub-agent's model pool
			// was switched while the request was in flight). Abandon the stale
			// request and restart with the latest client instead of failing the
			// turn. Mirrors MainAgent.handleAgentError's routing-invalidated path.
			log.Infof("SubAgent routing invalidated during active turn; restarting request agent=%v turn_id=%v", s.instanceID, result.turnID)
			s.parent.discardSpeculativeStreamToolsAndClearToolTrace(s.turn, "routing_invalidated")
			// The restart can land on a different model or backend, and a
			// finalized reasoning item only replays to the one that produced
			// it. The partial text is provider-neutral and stays.
			s.turn.drainPartialResponsesOutput()
			s.continueLLMWithPendingUserMessages()
			return
		}
		if !llm.IsContextLengthExceeded(result.err) && s.recoverTerminalResponse(s.interruptedRequestRecoveryInstruction(), result.err) {
			return
		}
		if isTransientSubAgentTransportError(result.err) {
			// Out of resume budget: the turn ends here, but the text the reply
			// already streamed still belongs in history rather than being
			// dropped on the floor. The condition matches the one that decides
			// a resume, so a given transport failure preserves the same way on
			// the last round as on the ones that resumed — a timeout used to
			// save its partial for three rounds and then discard the fourth.
			s.preserveInterruptedPartial()
		}
		s.sendEvent(Event{
			Type:    EventAgentError,
			Payload: result.err,
		})
		return
	}
	// A successful response contains the authoritative full content. Discard
	// the streaming accumulator so terminal recovery cannot append it twice.
	// The successful response also carries its own reasoning, so drop any
	// reasoning items the stream callback accumulated for this round.
	s.turn.drainPartialText()
	s.turn.drainPartialResponsesOutput()

	resp := result.resp
	resp.Content = message.NormalizeInvisibleText(resp.Content)
	if counts := sanitizeResponseZeroWidth(resp); len(counts) > 0 {
		log.Warnf("SubAgent: sanitized zero-width format characters from response agent=%v turn_id=%v fields=%v", s.instanceID, s.turn.ID, formatInvisibleCounts(counts))
	}
	if orphans := countOrphanVariationSelectors(resp); len(orphans) > 0 {
		log.Warnf("SubAgent: orphan variation selectors in response (kept verbatim, report-only): agent=%v turn_id=%v fields=%v", s.instanceID, s.turn.ID, formatInvisibleCounts(orphans))
	}

	// --- Classify tool calls as valid or malformed ---
	var validCalls, malformedCalls []message.ToolCall
	for _, tc := range resp.ToolCalls {
		if isMalformedToolCall(tc, s.tools) {
			malformedCalls = append(malformedCalls, tc)
		} else {
			validCalls = append(validCalls, tc)
		}
	}

	// --- Diagnostic logging (Fix 5) ---
	isTruncated := resp.StopReason == "max_tokens" || resp.StopReason == "length"
	if len(malformedCalls) > 0 {
		log.Warnf("SubAgent: malformed tool calls detected in LLM response agent=%v total_tool_calls=%v malformed_count=%v valid_count=%v stop_reason=%v last_input_tokens=%v", s.instanceID, len(resp.ToolCalls), len(malformedCalls), len(validCalls), resp.StopReason, s.ctxMgr.LastInputTokens())
	}

	// --- Break feedback loop (Fix 2) ---
	// If the response was truncated and has malformed calls, or if ALL calls
	// are malformed, discard the entire response and retry the LLM call.
	if len(malformedCalls) > 0 && (isTruncated || len(validCalls) == 0) {
		s.turn.MalformedCount++

		if isTruncated {
			log.Warnf("SubAgent: LLM output truncated with malformed tool calls; discarding response and retrying agent=%v malformed_count=%v valid_count=%v consecutive_rounds=%v stop_reason=%v", s.instanceID, len(malformedCalls), len(validCalls), s.turn.MalformedCount, resp.StopReason)
		} else {
			log.Warnf("SubAgent: all tool calls malformed; discarding response and retrying agent=%v malformed_count=%v consecutive_rounds=%v", s.instanceID, len(malformedCalls), s.turn.MalformedCount)
		}

		// Abort if too many consecutive retries.
		if s.turn.MalformedCount >= maxMalformedToolCalls {
			log.Warnf("SubAgent: aborting turn due to repeated malformed tool call args agent=%v count=%v threshold=%v", s.instanceID, s.turn.MalformedCount, maxMalformedToolCalls)
			s.parent.discardSpeculativeStreamToolsAndClearToolTrace(s.turn, "args_invalid")
			s.sendEvent(Event{
				Type: EventAgentError,
				Payload: fmt.Errorf(
					"SubAgent %s aborted: the model produced malformed tool call arguments "+
						"%d times in a row (output truncation)",
					s.instanceID, s.turn.MalformedCount,
				),
			})
			return
		}

		s.parent.discardSpeculativeStreamToolsAndClearToolTrace(s.turn, "args_invalid")
		// Retry without storing the malformed response.
		s.continueLLMWithPendingUserMessages()
		return
	}

	compatCfg := s.thinkingToolcallCompat()
	compatEnabled := compatCfg != nil && compatCfg.EnabledValue()
	driftDetected := compatEnabled &&
		resp.ThinkingToolcallMarkerHit &&
		len(validCalls) == 0 &&
		resp.StopReason == "stop"

	if driftDetected {
		parsed := parseThinkingToolcalls(resp.ReasoningContent)
		if len(parsed) > 0 {
			log.Warnf("SubAgent: thinking-toolcall format drift detected; parsed pseudo tool calls from reasoning agent=%v compat_thinking_toolcall_enabled=%v thinking_toolcall_marker_hit=%v parsed_tool_calls=%v", s.instanceID, compatEnabled, resp.ThinkingToolcallMarkerHit, len(parsed))
			validCalls = parsed
			s.parent.discardSpeculativeStreamToolsAndClearToolTrace(s.turn, "provider_drift")

			// Emit ToolCallStartEvent for each parsed tool call to update the TUI.
			// Standard tool calls emit this during streaming; pseudo calls must
			// emit it now after parsing completes.
			for _, tc := range validCalls {
				s.parent.emitToTUI(ToolCallStartEvent{
					ID:       tc.ID,
					Name:     tc.Name,
					ArgsJSON: string(tc.Args),
					AgentID:  s.instanceID,
				})
			}
		} else {
			log.Warnf("SubAgent: thinking-toolcall format drift detected; could not parse pseudo tool calls, entering bounded terminal recovery agent=%v compat_thinking_toolcall_enabled=%v thinking_toolcall_marker_hit=%v", s.instanceID, compatEnabled, resp.ThinkingToolcallMarkerHit)
			// Debug: log the raw reasoning content for troubleshooting
			if resp.ReasoningContent != "" {
				reasoningLen := len(resp.ReasoningContent)
				preview := llm.TruncateStringRunes(resp.ReasoningContent, 500, "...(truncated)")
				log.Debugf("SubAgent: unparseable reasoning content agent=%v reasoning_len=%v reasoning_preview=%v", s.instanceID, reasoningLen, preview)
			}
			s.sendEvent(Event{
				Type:    EventAgentLog,
				Payload: "SubAgent detected provider thinking pseudo tool-call drift but could not parse it; entering bounded terminal recovery.",
			})
		}
	}
	if len(validCalls) > maxToolCallsPerResponse {
		err := fmt.Errorf("SubAgent %s received %d tool calls; maximum per response is %d", s.instanceID, len(validCalls), maxToolCallsPerResponse)
		log.Warnf("SubAgent: rejecting oversized tool-call response agent=%v count=%v limit=%v", s.instanceID, len(validCalls), maxToolCallsPerResponse)
		if s.parent != nil {
			s.parent.discardSpeculativeStreamToolsAndClearToolTrace(s.turn, "tool_call_limit")
		}
		s.sendEvent(Event{Type: EventAgentError, Payload: err})
		return
	}

	// Append assistant message to context with valid calls only.
	// Sanitize remaining calls as a safety net (no-op for valid calls).
	sanitizedCalls := sanitizeToolCallArgs(validCalls)
	thinkingBlocks := resp.ThinkingBlocks
	responsesOutput := resp.ResponsesOutput
	geminiParts := resp.GeminiParts
	reasoningContent := resp.ReasoningContent
	if !toolCallTrajectoriesEqual(resp.ToolCalls, validCalls) {
		thinkingBlocks = nil
		responsesOutput = nil
		geminiParts = nil
		reasoningContent = ""
		for i := range sanitizedCalls {
			sanitizedCalls[i].ThoughtSignature = ""
		}
	}
	if strings.TrimSpace(resp.Content) != "" || len(sanitizedCalls) > 0 || len(resp.ThinkingBlocks) > 0 || strings.TrimSpace(resp.ReasoningContent) != "" {
		log.Debugf("subagent finalize assistant payload agent=%v turn_id=%v final_content_len=%v tool_calls=%v thinking_blocks=%v stop_reason=%v", s.instanceID, s.turn.ID, len(resp.Content), len(sanitizedCalls), len(resp.ThinkingBlocks), resp.StopReason)
	}
	if strings.TrimSpace(resp.Content) == "" && len(sanitizedCalls) > 0 {
		log.Warnf("subagent finalized without assistant text agent=%v turn_id=%v tool_calls=%v thinking_blocks=%v stop_reason=%v", s.instanceID, s.turn.ID, len(sanitizedCalls), len(resp.ThinkingBlocks), resp.StopReason)
	}
	s.ctxMgr.Append(message.Message{
		Role:             "assistant",
		Content:          resp.Content,
		ThinkingBlocks:   thinkingBlocks,
		ResponsesOutput:  responsesOutput,
		GeminiParts:      geminiParts,
		ReasoningContent: reasoningContent,
		ToolCalls:        sanitizedCalls,
		StopReason:       resp.StopReason,
		Provenance:       subAssistantProvenance(s),
	})

	// Emit finalized assistant message event for control-plane consumers.
	ownerAgentID := s.OwnerAgentID()
	s.parent.emitToTUI(AssistantMessageEvent{
		AgentID:       s.instanceID,
		TaskID:        s.taskID,
		AgentType:     s.agentDefName,
		ParentAgentID: controlPlaneAgentID(ownerAgentID),
		Text:          resp.Content,
		ToolCalls:     len(sanitizedCalls),
	})

	// Persist assistant message (with usage for session resume).
	persistMsg := message.Message{
		Role:             "assistant",
		Content:          resp.Content,
		ThinkingBlocks:   thinkingBlocks,
		ResponsesOutput:  responsesOutput,
		GeminiParts:      geminiParts,
		ReasoningContent: reasoningContent,
		ToolCalls:        sanitizedCalls,
		StopReason:       resp.StopReason,
		Provenance:       subAssistantProvenance(s),
		Usage:            resp.Usage,
	}
	persistBarrier, persistPending := s.persistMessageBarrier(persistMsg, "assistant message")

	// Update token usage (does not auto-compact, but tracks stats).
	if resp.Usage != nil {
		s.ctxMgr.UpdateFromUsage(*resp.Usage)
	}

	// ---------------------------------------------------------------
	// Complete/Escalate interception (on valid calls only)
	// ---------------------------------------------------------------
	// Complete is intercepted HERE, not in executeToolCall. It is an
	// internal control tool — SubAgent must always be able to call it.
	//
	// IMPORTANT: when Complete is co-returned with other tools, we
	// execute the other tools FIRST and defer EventAgentDone until they
	// all complete. This prevents the last batch of file edits from being
	// silently dropped.
	var taskCompleteCallID string
	var taskComplete *AgentResult
	// completeRejection carries a validation failure from the first Complete
	// call in this response. The failed call is reported to the model with a
	// "Completion rejected" tool result and one bounded follow-up (see
	// rejectInvalidCompleteArguments) instead of failing the task outright.
	// completeDegraded is the delivery that survives that rejection when only
	// the typed-result group was malformed; nil for every other failure.
	var completeRejection error
	var completeDegraded *AgentResult
	var wakeMainCallID string
	var wakeMainReason string
	var wakeMainRequest *tools.AgentRequestPayload
	var wakeMainArgsJSON string
	for _, tc := range validCalls {
		if tools.NormalizeName(tc.Name) == tools.NameComplete {
			var args struct {
				Summary              string              `json:"summary"`
				FilesChanged         []string            `json:"files_changed,omitempty"`
				RemainingLimitations []string            `json:"remaining_limitations,omitempty"`
				KnownRisks           []string            `json:"known_risks,omitempty"`
				FollowUpRecommended  []string            `json:"follow_up_recommended,omitempty"`
				Artifacts            []tools.ArtifactRef `json:"artifacts,omitempty"`
				ResultType           string              `json:"result_type,omitempty"`
				Result               json.RawMessage     `json:"result,omitempty"`
				ResultRef            *tools.ResultRef    `json:"result_ref,omitempty"`
			}
			if err := json.Unmarshal(tc.Args, &args); err != nil {
				completeRejection = fmt.Errorf("invalid Complete args: %w", err)
				taskCompleteCallID = tc.ID
				break
			}
			if strings.TrimSpace(args.Summary) == "" {
				completeRejection = fmt.Errorf("invalid Complete args: summary is required")
				taskCompleteCallID = tc.ID
				break
			}
			artifacts, err := tools.ValidateArtifactRefs(s.sessionDir, args.Artifacts)
			if err != nil {
				completeRejection = fmt.Errorf("invalid Complete args: %w", err)
				taskCompleteCallID = tc.ID
				break
			}
			resultType, result, resultRef, err := validateCompleteTypedResult(s.sessionDir, args.ResultType, args.Result, args.ResultRef)
			if err != nil {
				completeRejection = fmt.Errorf("invalid Complete args: %w", err)
				taskCompleteCallID = tc.ID
				// An incomplete typed-result group is the one rejection that
				// leaves a usable delivery behind: everything except the group
				// validated. Keep that delivery as the fallback the rejection
				// path settles once the correction budget is spent.
				if _, ok := errors.AsType[typedResultPairingError](err); ok {
					completeDegraded = &AgentResult{
						Summary: strings.TrimSpace(args.Summary),
						Envelope: normalizeCompletionEnvelope(&CompletionEnvelope{
							Summary:              args.Summary,
							FilesChanged:         args.FilesChanged,
							RemainingLimitations: append(append([]string(nil), args.RemainingLimitations...), droppedTypedResultLimitation),
							KnownRisks:           args.KnownRisks,
							FollowUpRecommended:  args.FollowUpRecommended,
							Artifacts:            artifacts,
						}),
					}
				}
				break
			}
			taskCompleteCallID = tc.ID
			taskComplete = &AgentResult{
				Summary: strings.TrimSpace(args.Summary),
				Envelope: normalizeCompletionEnvelope(&CompletionEnvelope{
					Summary:              args.Summary,
					FilesChanged:         args.FilesChanged,
					RemainingLimitations: args.RemainingLimitations,
					KnownRisks:           args.KnownRisks,
					FollowUpRecommended:  args.FollowUpRecommended,
					Artifacts:            artifacts,
					ResultType:           resultType,
					Result:               result,
					ResultRef:            resultRef,
				}),
			}
			break
		}
	}
	for _, tc := range validCalls {
		if tools.NormalizeName(tc.Name) == tools.NameEscalate {
			var args tools.AgentRequestPayload
			if err := json.Unmarshal(tc.Args, &args); err != nil {
				s.sendEvent(Event{
					Type:    EventAgentError,
					Payload: fmt.Errorf("invalid Escalate args: %w", err),
				})
				return
			}
			wakeMainCallID = tc.ID
			wakeMainReason = args.Reason
			wakeMainRequest = &args
			wakeMainArgsJSON = string(tc.Args)
			break
		}
	}
	if taskCompleteCallID != "" && wakeMainCallID != "" {
		s.sendEvent(Event{
			Type:    EventAgentError,
			Payload: fmt.Errorf("invalid control mix: Complete and Escalate cannot appear in the same response"),
		})
		return
	}

	if wakeMainCallID != "" {
		resultContent := "Escalation sent: " + wakeMainReason
		toolMsg := message.Message{
			Role:       "tool",
			ToolCallID: wakeMainCallID,
			Content:    resultContent,
		}
		s.ctxMgr.Append(toolMsg)
		s.persistMessageAsync(toolMsg, "Escalate tool result", nil)
		s.turn.removeStreamingToolCall(wakeMainCallID)
		s.parent.emitToTUI(ToolResultEvent{
			CallID:   wakeMainCallID,
			Name:     tools.NameEscalate,
			ArgsJSON: wakeMainArgsJSON,
			Result:   resultContent,
			Status:   ToolResultStatusSuccess,
			AgentID:  s.instanceID,
		})
	}

	// Collect non-Complete valid tool calls for finalize-time batching.
	var regularToolCalls []message.ToolCall
	for _, tc := range validCalls {
		name := tools.NormalizeName(tc.Name)
		if name != tools.NameComplete && name != tools.NameEscalate {
			tc.Name = name
			regularToolCalls = append(regularToolCalls, tc)
		}
	}

	// Pure text alone does not finish a delegated task. Give the model one
	// bounded recovery request to emit an explicit coordination tool.
	if len(validCalls) == 0 {
		s.parent.discardSpeculativeStreamToolsAndClearToolTrace(s.turn, "no_valid_calls")
		if s.continueLLMIfPendingUserMessages() {
			return
		}
		if s.recoverTerminalResponse("Do not stop after plain text. Finish coordination now: call Complete if the delegated task is done; otherwise call Escalate or Notify with the blocker, question, or progress that the parent must receive.", nil) {
			return
		}
		if s.turn.SubAgentTerminalRecoveryCount > 0 {
			s.sendEvent(Event{
				Type:    EventAgentError,
				Payload: fmt.Errorf("SubAgent stopped without a coordination tool after terminal recovery"),
			})
			return
		}
	}

	// Complete only, no other tools → trigger done immediately.
	if len(regularToolCalls) == 0 {
		s.parent.discardSpeculativeStreamToolsAndClearToolTrace(s.turn, "complete_only")
		if completeRejection != nil {
			s.rejectInvalidCompleteArguments(taskCompleteCallID, completeRejection, completeDegraded)
			return
		}
		if wakeMainCallID != "" {
			s.sendEvent(Event{
				Type:     EventEscalate,
				SourceID: s.instanceID,
				Payload:  *wakeMainRequest,
			})
			return
		}
		if err := s.finishCompletion(taskCompleteCallID, taskComplete); err != nil {
			s.appendCompleteToolResult(taskCompleteCallID, "Completion rejected: "+err.Error(), ToolResultStatusError)
		}
		return
	}

	// Has regular tool calls to execute in parallel.
	// If Complete was also in this batch, store it as pending.
	if taskCompleteCallID != "" {
		log.Infof("Complete co-returned with other tools; executing others first agent=%v other_tools=%v", s.instanceID, len(regularToolCalls))
		if completeRejection != nil {
			s.pendingRejectedCompleteCallID = taskCompleteCallID
			s.pendingRejectedCompleteErr = completeRejection
			s.pendingRejectedCompleteDegraded = completeDegraded
		} else {
			s.pendingComplete = taskComplete
			s.pendingCompleteCallID = taskCompleteCallID
		}
	}
	if wakeMainCallID != "" {
		log.Infof("Escalate co-returned with other tools; executing others first agent=%v other_tools=%v", s.instanceID, len(regularToolCalls))
		s.pendingEscalate = wakeMainReason
		if wakeMainRequest != nil {
			request := *wakeMainRequest
			s.pendingEscalateRequest = &request
		}
	}

	// Dispatching tools for parallel execution is real activity too: a long
	// tool batch is not a stall even though its result only arrives later.
	s.markActivity()

	// Dispatch concurrency-safe finalize-time batches.
	turn := s.turn
	batches := buildToolExecutionBatches(s.tools, regularToolCalls)

	// Intent barrier: the assistant message carrying these tool calls must be
	// process-crash durable before any tool body can produce side effects.
	if len(batches) > 0 {
		if err := s.waitPersistBarrier(persistBarrier, persistPending); err != nil {
			s.failIntentBarrier(regularToolCalls, err)
			return
		}
		turn.BarrierFailureRounds = 0
	}

	turn.noteDispatchedToolRound(regularToolCalls, false)
	turn.toolExecutionBatches = batches
	turn.nextToolBatch = 0
	turn.activeToolBatchCancel = nil
	turn.TotalToolCalls.Store(int32(len(regularToolCalls)))
	s.parent.emitActivity(s.instanceID, ActivityExecuting, fmt.Sprintf("%d tools", len(regularToolCalls)))

	// Track pending tools so cancellation can close their cards explicitly.
	for _, tc := range regularToolCalls {
		turn.recordPendingToolCall(PendingToolCall{CallID: tc.ID, Name: tc.Name, ArgsJSON: string(tc.Args), AgentID: s.instanceID})
	}
	if len(batches) > 1 {
		queued := make([]PendingToolCall, 0, len(regularToolCalls))
		for _, batch := range batches[1:] {
			for _, tc := range batch.Calls {
				queued = append(queued, PendingToolCall{CallID: tc.ID, Name: tc.Name, ArgsJSON: string(tc.Args), AgentID: s.instanceID})
			}
		}
		emitToolExecutionState(s.parent.emitToTUI, queued, ToolCallExecutionStateQueued)
	}
	streamingSnapshot := turn.snapshotStreamingToolCalls()
	validCallIDs := make(map[string]struct{}, len(regularToolCalls))
	for _, tc := range regularToolCalls {
		validCallIDs[tc.ID] = struct{}{}
	}
	var discardInfo map[string]StreamingToolDiscardInfo
	if len(streamingSnapshot) > 0 && turn.streamingToolExec != nil {
		discarded := turn.streamingToolExec.DiscardExceptInfo(validCallIDs, "filtered")
		logStreamingToolDiscardInfo("filtered", discarded)
		if len(discarded) > 0 {
			discardInfo = make(map[string]StreamingToolDiscardInfo, len(discarded))
			for _, it := range discarded {
				discardInfo[it.CallID] = it
			}
		}
	}
	finalizeStreamingToolCards(s.parent.emitToTUI, validCallIDs, discardInfo, s.turn)
	if len(streamingSnapshot) > 0 {
		orphans := make([]PendingToolCall, 0, len(streamingSnapshot))
		for _, c := range streamingSnapshot {
			if _, ok := validCallIDs[c.CallID]; ok {
				continue
			}
			orphans = append(orphans, c)
		}
		s.parent.clearToolTraceForCalls(orphans)
	}
	if len(batches) > 0 {
		s.startNextToolBatch(turn)
	}
}

// interruptedRequestRecoveryInstruction picks the recovery instruction for a
// transient transport failure. When the interrupted request had already
// streamed body text, that text is saved as an interrupted assistant message
// directly above this instruction, so the sub-agent should pick the work back
// up rather than wrap up — telling it to finish coordination there would throw
// away the reply it just produced. With nothing streamed there is no work in
// flight to resume, so the bounded wrap-up instruction stays correct.
func (s *SubAgent) interruptedRequestRecoveryInstruction() string {
	if s != nil && s.turn != nil && strings.TrimSpace(s.turn.peekPartialText()) != "" {
		return "System note: the previous model request was interrupted by a transient transport error, and the reply it had already produced is preserved above as an interrupted assistant message. Continue that reply directly from where it stopped without apology or recap, then finish the delegated task. Do not restart the analysis and do not repeat text that is already preserved."
	}
	return "The previous model request was interrupted by a transient transport error. Re-check the task state and finish coordination now. If the task is complete, call Complete with a concise summary. If blocked or parent input is required, call Escalate or Notify instead of stopping after plain text."
}

// maxSubAgentStreamResumes bounds how many times one sub-agent turn restarts
// after a preserved stream interruption. Unlike the main agent — where the user
// watches the reply and can cancel — a sub-agent runs unattended and often in
// parallel with siblings, so an unbounded loop would multiply across the fleet.
// The client's transport cooldown paces each restart, so a handful is enough to
// ride out a flaky gateway without turning one delegated task into a cost sink.
const maxSubAgentStreamResumes = 3

// preserveInterruptedPartial saves whatever body text the interrupted request
// had already streamed as a durable interrupted assistant message. It is the
// single place that honours the "produced text is never discarded" contract for
// sub-agents, so it must run on the give-up path too, not only when a resume is
// still available. Finalized reasoning items are preserved alongside the text
// so the message keeps its required preceding reasoning item on replay.
func (s *SubAgent) preserveInterruptedPartial() {
	if s == nil || s.turn == nil {
		return
	}
	// The sidebar identity realigns to the sticky cursor when a request ends, so
	// it cannot name the producer of a partial reply; the turn's confirmed
	// producer can, and it must be read before draining drops it.
	producingRef := s.turn.producingModelRef()
	partial := strings.TrimSpace(s.turn.drainPartialText())
	reasoning := s.turn.drainPartialResponsesOutput()
	if partial == "" {
		return
	}
	msg := message.Message{
		Role:            "assistant",
		Content:         partial,
		StopReason:      "interrupted",
		ResponsesOutput: interruptedAssistantResponsesOutput(reasoning, partial),
		Provenance:      subAssistantProvenanceForRunningRef(s, producingRef),
	}
	s.ctxMgr.Append(msg)
	s.persistMessageAsync(msg, "interrupted assistant message", nil)
}

func (s *SubAgent) recoverTerminalResponse(instruction string, cause error) bool {
	if s == nil || s.turn == nil {
		return false
	}
	if cause != nil && !isTransientSubAgentTransportError(cause) {
		return false
	}
	// A transport interruption and a reply that stopped at plain text are
	// different failures with different budgets; charge the right one.
	if cause != nil {
		if s.turn.SubAgentStreamResumeCount >= maxSubAgentStreamResumes {
			return false
		}
		s.turn.SubAgentStreamResumeCount++
	} else {
		if s.turn.SubAgentTerminalRecoveryCount >= 1 {
			return false
		}
		s.turn.SubAgentTerminalRecoveryCount++
	}
	s.preserveInterruptedPartial()
	s.parent.discardSpeculativeStreamToolsAndClearToolTrace(s.turn, "terminal_recovery")
	s.appendPendingUserMessage(pendingUserMessage{Content: instruction})
	s.asyncCallLLMWithFlightMarked(s.turn, s.ctxMgr.Snapshot())
	return true
}

func isTransientSubAgentTransportError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	// The client escalates preservable stream interruptions instead of retrying
	// them internally, so the caller now owns their recovery. They must be
	// matched before the status-code branch: an in-band stream error event
	// carries no HTTP status, and neither InterruptedResponseError nor
	// ChunkTimeoutError is an *APIError. Without this an interrupted SubAgent
	// reply fails the turn outright and the text it already streamed is lost,
	// because only this path drains and saves the partial reply.
	if llm.IsPreservableStreamInterruption(err) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	if _, ok := errors.AsType[*net.OpError](err); ok {
		return true
	}
	if apiErr, ok := errors.AsType[*llm.APIError](err); ok && apiErr != nil {
		if apiErr.StatusCode == 408 || apiErr.StatusCode == 429 || apiErr.StatusCode >= 500 {
			return true
		}
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "connection reset") || strings.Contains(msg, "broken pipe") ||
		(strings.Contains(msg, "stream") && strings.Contains(msg, "interrupt"))
}

// failIntentBarrier handles a SubAgent intent-barrier failure: no regular tool
// body runs. It synthesizes not_started terminal results for the calls that
// would have run and closes speculative streaming cards, then lets the normal
// tool-result accounting drive the next LLM round. Persistence health was
// already degraded by persistMessageBarrier.
func (s *SubAgent) failIntentBarrier(calls []message.ToolCall, cause error) {
	turn := s.turn
	if turn == nil || len(calls) == 0 {
		return
	}
	turn.BarrierFailureRounds++
	validCallIDs := make(map[string]struct{}, len(calls))
	for _, tc := range calls {
		validCallIDs[tc.ID] = struct{}{}
	}
	emit := func(AgentEvent) {}
	clearTrace := func([]PendingToolCall) {}
	if s.parent != nil {
		emit = s.parent.emitToTUI
		clearTrace = s.parent.clearToolTraceForCalls
	}
	// No tool body may run: discard every speculative execution (valid IDs
	// included — they are reported not_started below, so committed speculative
	// writes must roll back), then close the streaming cards. Snapshot before
	// finalize: finalize drains the streaming list.
	streamingSnapshot := turn.snapshotStreamingToolCalls()
	var discardInfo map[string]StreamingToolDiscardInfo
	if turn.streamingToolExec != nil {
		discarded := turn.streamingToolExec.DiscardExceptInfo(nil, "barrier_failed")
		logStreamingToolDiscardInfo("barrier_failed", discarded)
		if len(discarded) > 0 {
			discardInfo = make(map[string]StreamingToolDiscardInfo, len(discarded))
			for _, it := range discarded {
				discardInfo[it.CallID] = it
			}
		}
	}
	finalizeStreamingToolCards(emit, validCallIDs, discardInfo, turn)
	if len(streamingSnapshot) > 0 {
		orphans := make([]PendingToolCall, 0, len(streamingSnapshot))
		for _, c := range streamingSnapshot {
			if _, ok := validCallIDs[c.CallID]; ok {
				continue
			}
			orphans = append(orphans, c)
		}
		clearTrace(orphans)
	}

	barrierErr := fmt.Errorf("tool not executed: session persistence failed before the tool started: %w", cause)
	turn.PendingToolCalls.Store(int32(len(calls)))
	turn.TotalToolCalls.Store(int32(len(calls)))
	turn.toolExecutionBatches = nil
	turn.nextToolBatch = 0
	turn.activeToolBatchCancel = nil
	for _, tc := range calls {
		s.enqueuePromotedToolResult(&toolResult{
			CallID:        tc.ID,
			Name:          tc.Name,
			ArgsJSON:      string(tc.Args),
			Error:         barrierErr,
			TurnID:        turn.ID,
			RecoveryState: message.ToolRecoveryStateNotStarted,
		})
	}
}
