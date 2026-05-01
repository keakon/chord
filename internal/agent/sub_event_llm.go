package agent

import (
	"encoding/json"
	"fmt"
	"github.com/keakon/golog/log"
	"strings"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func (s *SubAgent) handleLLMResponse(result *llmResult) {
	// Turn isolation: discard stale responses.
	if s.turn == nil || result.turnID != s.turn.ID {
		log.Debugf("SubAgent: discarding stale LLM response agent=%v result_turn=%v current_turn=%v", s.instanceID, result.turnID, s.currentTurnID())
		return
	}

	if result.err != nil {
		s.sendEvent(Event{
			Type:    EventAgentError,
			Payload: result.err,
		})
		return
	}

	resp := result.resp

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
			s.parent.discardSpeculativeStreamToolsAndClearToolTrace(s.turn)
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

		s.parent.discardSpeculativeStreamToolsAndClearToolTrace(s.turn)
		// Retry without storing the malformed response.
		messages := s.ctxMgr.Snapshot()
		s.asyncCallLLM(s.turn, messages)
		return
	}

	compatCfg := s.llmClient.ThinkingToolcallCompat()
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
			s.parent.discardSpeculativeStreamToolsAndClearToolTrace(s.turn)

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
			log.Warnf("SubAgent: thinking-toolcall format drift detected; could not parse pseudo tool calls, entering idle wait agent=%v compat_thinking_toolcall_enabled=%v thinking_toolcall_marker_hit=%v", s.instanceID, compatEnabled, resp.ThinkingToolcallMarkerHit)
			// Debug: log the raw reasoning content for troubleshooting
			if resp.ReasoningContent != "" {
				reasoningLen := len(resp.ReasoningContent)
				preview := resp.ReasoningContent
				if reasoningLen > 500 {
					preview = resp.ReasoningContent[:500] + "...(truncated)"
				}
				log.Debugf("SubAgent: unparseable reasoning content agent=%v reasoning_len=%v reasoning_preview=%v", s.instanceID, reasoningLen, preview)
			}
			s.sendEvent(Event{
				Type:    EventAgentLog,
				Payload: "SubAgent detected provider thinking pseudo tool-call drift but could not parse them; entered idle wait.",
			})
			s.parent.discardSpeculativeStreamToolsAndClearToolTrace(s.turn)
			s.resetIdleTimer()
			s.idleTimer = time.NewTimer(s.idleTimeout)
			return
		}
	}

	// Append assistant message to context with valid calls only.
	// Sanitize remaining calls as a safety net (no-op for valid calls).
	sanitizedCalls := sanitizeToolCallArgs(validCalls)
	if strings.TrimSpace(resp.Content) != "" || len(sanitizedCalls) > 0 || len(resp.ThinkingBlocks) > 0 {
		log.Debugf("subagent finalize assistant payload agent=%v turn_id=%v final_content_len=%v tool_calls=%v thinking_blocks=%v stop_reason=%v", s.instanceID, s.turn.ID, len(resp.Content), len(sanitizedCalls), len(resp.ThinkingBlocks), resp.StopReason)
	}
	if strings.TrimSpace(resp.Content) == "" && len(sanitizedCalls) > 0 {
		log.Warnf("subagent finalized without assistant text agent=%v turn_id=%v tool_calls=%v thinking_blocks=%v stop_reason=%v", s.instanceID, s.turn.ID, len(sanitizedCalls), len(resp.ThinkingBlocks), resp.StopReason)
	}
	s.ctxMgr.Append(message.Message{
		Role:       "assistant",
		Content:    resp.Content,
		ToolCalls:  sanitizedCalls,
		StopReason: resp.StopReason,
	})

	// Emit finalized assistant message event for control-plane consumers.
	s.parent.emitToTUI(AssistantMessageEvent{
		AgentID:   s.instanceID,
		Text:      resp.Content,
		ToolCalls: len(sanitizedCalls),
	})

	// Persist assistant message (with usage for session resume).
	persistMsg := message.Message{
		Role:       "assistant",
		Content:    resp.Content,
		ToolCalls:  sanitizedCalls,
		StopReason: resp.StopReason,
	}
	persistMsg.Usage = resp.Usage
	go func() {
		if err := s.recovery.PersistMessage(s.instanceID, persistMsg); err != nil {
			log.Warnf("SubAgent: failed to persist assistant message agent=%v error=%v", s.instanceID, err)
		}
	}()

	// Update token usage (no auto_compact, but tracks stats).
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
	var wakeMainCallID string
	var wakeMainReason string
	var wakeMainArgsJSON string
	for _, tc := range validCalls {
		if tc.Name == "Complete" {
			var args struct {
				Summary              string              `json:"summary"`
				FilesChanged         []string            `json:"files_changed,omitempty"`
				VerificationRun      []string            `json:"verification_run,omitempty"`
				RemainingLimitations []string            `json:"remaining_limitations,omitempty"`
				KnownRisks           []string            `json:"known_risks,omitempty"`
				FollowUpRecommended  []string            `json:"follow_up_recommended,omitempty"`
				Artifacts            []tools.ArtifactRef `json:"artifacts,omitempty"`
			}
			if err := json.Unmarshal(tc.Args, &args); err != nil {
				s.sendEvent(Event{
					Type:    EventAgentError,
					Payload: fmt.Errorf("invalid Complete args: %w", err),
				})
				return
			}
			if strings.TrimSpace(args.Summary) == "" {
				s.sendEvent(Event{
					Type:    EventAgentError,
					Payload: fmt.Errorf("invalid Complete args: summary is required"),
				})
				return
			}
			taskCompleteCallID = tc.ID
			taskComplete = &AgentResult{
				Summary: strings.TrimSpace(args.Summary),
				Envelope: normalizeCompletionEnvelope(&CompletionEnvelope{
					Summary:              args.Summary,
					FilesChanged:         args.FilesChanged,
					VerificationRun:      args.VerificationRun,
					RemainingLimitations: args.RemainingLimitations,
					KnownRisks:           args.KnownRisks,
					FollowUpRecommended:  args.FollowUpRecommended,
					Artifacts:            args.Artifacts,
				}),
			}
			break
		}
	}
	for _, tc := range validCalls {
		if tc.Name == "Escalate" {
			var args struct {
				Reason string `json:"reason"`
			}
			if err := json.Unmarshal(tc.Args, &args); err != nil {
				s.sendEvent(Event{
					Type:    EventAgentError,
					Payload: fmt.Errorf("invalid Escalate args: %w", err),
				})
				return
			}
			wakeMainCallID = tc.ID
			wakeMainReason = args.Reason
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
		if s.recovery != nil {
			go func(msg message.Message) {
				if err := s.recovery.PersistMessage(s.instanceID, msg); err != nil {
					log.Warnf("SubAgent: failed to persist Escalate tool result agent=%v error=%v", s.instanceID, err)
				}
			}(toolMsg)
		}
		s.turn.removeStreamingToolCall(wakeMainCallID)
		s.parent.emitToTUI(ToolResultEvent{
			CallID:   wakeMainCallID,
			Name:     "Escalate",
			ArgsJSON: wakeMainArgsJSON,
			Result:   resultContent,
			Status:   ToolResultStatusSuccess,
			AgentID:  s.instanceID,
		})
	}

	// Collect non-Complete valid tool calls for finalize-time batching.
	var regularToolCalls []message.ToolCall
	for _, tc := range validCalls {
		if tc.Name != "Complete" && tc.Name != "Escalate" {
			regularToolCalls = append(regularToolCalls, tc)
		}
	}

	// Pure text reply (no valid tool calls at all): LLM may be asking questions,
	// analysing, or expressing confusion. Start idle timer.
	if len(validCalls) == 0 {
		s.parent.discardSpeculativeStreamToolsAndClearToolTrace(s.turn)
		// Stop any previous idle timer to prevent leaking timers when
		// consecutive pure-text responses arrive (e.g. multi-turn Q&A).
		s.resetIdleTimer()
		s.idleTimer = time.NewTimer(s.idleTimeout)
		return
	}

	// Complete only, no other tools → trigger done immediately.
	if len(regularToolCalls) == 0 {
		s.parent.discardSpeculativeStreamToolsAndClearToolTrace(s.turn)
		if wakeMainCallID != "" {
			s.sendEvent(Event{
				Type:     EventEscalate,
				SourceID: s.instanceID,
				Payload:  wakeMainReason,
			})
			return
		}
		outstandingChildren := s.parent.outstandingJoinChildTaskIDs(s.taskID)
		if len(outstandingChildren) > 0 {
			s.appendCompleteToolResult(taskCompleteCallID, deferredCompleteResult(len(outstandingChildren)))
			s.setPendingCompleteIntent(taskComplete)
			s.enterWaitingDescendant(deferredCompleteResult(len(outstandingChildren)))
			return
		}
		s.clearPendingCompleteIntent()
		s.appendCompleteToolResult(taskCompleteCallID, taskComplete.Summary)
		s.sendEvent(Event{
			Type:    EventAgentDone,
			Payload: taskComplete,
		})
		return
	}

	// Has regular tool calls to execute in parallel.
	// If Complete was also in this batch, store it as pending.
	if taskCompleteCallID != "" {
		log.Infof("Complete co-returned with other tools; executing others first agent=%v other_tools=%v", s.instanceID, len(regularToolCalls))
		s.pendingComplete = taskComplete
		s.pendingCompleteCallID = taskCompleteCallID
	}
	if wakeMainCallID != "" {
		log.Infof("Escalate co-returned with other tools; executing others first agent=%v other_tools=%v", s.instanceID, len(regularToolCalls))
		s.pendingEscalate = wakeMainReason
	}

	// Dispatch concurrency-safe finalize-time batches.
	turn := s.turn
	batches := buildToolExecutionBatches(s.tools, regularToolCalls)
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
	if len(batches) > 0 {
		s.startNextToolBatch(turn)
	}
	streamingSnapshot := turn.snapshotStreamingToolCalls()
	validCallIDs := make(map[string]struct{}, len(regularToolCalls))
	for _, tc := range regularToolCalls {
		validCallIDs[tc.ID] = struct{}{}
	}
	finalizeStreamingToolCards(s.parent.emitToTUI, validCallIDs, s.turn)
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
}
