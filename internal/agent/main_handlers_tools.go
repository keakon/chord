package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/analytics"
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
	tc := message.ToolCall{ID: call.CallID, Name: call.Name, Args: json.RawMessage(call.ArgsJSON)}
	if err := a.commitPromotedToolSideEffects(tc, payload); err != nil {
		payload.Error = fmt.Errorf("commit completed speculative tool side effects: %w", err)
	}
	// Tool completed execution: persist and emit immediately so the result
	// survives terminal turn failure/interruption and can be reused on resume.
	if a.walltime != nil {
		a.walltime.recordTarget(payload.walltimeTarget, analytics.WalltimePurposeTool, payload.Duration)
	}
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
			if a.recoveryManager() != nil {
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
		if !isFinishedTodoStatus(todo.Status) {
			return false
		}
	}
	return true
}

// buildToolResultMessage builds the durable tool message for a completed tool
// call. Every append path — the normal batch path, the interruption path, and
// the deferred model-driven / control-tool paths — constructs its message here
// so a restored transcript carries the same fields regardless of which path
// wrote it (payload/notes split, diffs, audit, LSP reviews, file state and
// provenance used to drift between hand-assembled copies).
//
// provenance is the attribution of the assistant message that declared the
// call; callers resolve it through the manager's read-locked backward scan
// (toolProvenanceFromContext) instead of copying the whole history.
func (a *MainAgent) buildToolResultMessage(payload *ToolResultPayload, contextResult string, parts []message.ContentPart, isError bool, provenance *message.MessageProvenance) message.Message {
	return message.Message{
		Role:       message.RoleTool,
		Content:    contextResult,
		Parts:      parts,
		ToolCallID: payload.CallID,
		// Content is the model-visible combination of payload and notes; both
		// are also stored apart so a restored transcript can show the tool's own
		// output without splitting the notes back out.
		ToolPayload:       payload.Payload,
		ToolNotes:         append([]string(nil), payload.Notes...),
		ToolDiff:          payload.Diff,
		ToolDiffAdded:     payload.DiffAdded,
		ToolDiffRemoved:   payload.DiffRemoved,
		ToolDurationMs:    payload.Duration.Milliseconds(),
		ToolStatus:        string(toolResultStatusFromError(isError)),
		Audit:             payload.Audit.Clone(),
		LSPReviews:        append([]message.LSPReview(nil), payload.LSPReviews...),
		FileState:         payload.FileState.Clone(),
		Provenance:        provenance,
		ToolRecoveryState: payload.RecoveryState,
	}
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
	parts := a.toolResultParts(contextResult, payload.Images)

	a.emitToTUI(ToolResultEvent{
		CallID:      payload.CallID,
		Name:        payload.Name,
		ArgsJSON:    payload.ArgsJSON,
		Audit:       payload.Audit.Clone(),
		Result:      displayResult,
		Payload:     payload.Payload,
		Notes:       append([]string(nil), payload.Notes...),
		Status:      toolResultStatusFromError(isError),
		Parts:       parts,
		Diff:        payload.Diff,
		DiffAdded:   payload.DiffAdded,
		DiffRemoved: payload.DiffRemoved,
		FileCreated: payload.FileCreated,
		FileState:   payload.FileState.Clone(),
		Duration:    payload.Duration,
	})

	a.queueLSPDiagnosticOverlayFromContext(payload)
	toolMsg := a.buildToolResultMessage(payload, contextResult, parts, isError, toolProvenanceFromContext(a.ctxMgr, payload.CallID))
	a.ctxMgr.Append(toolMsg)
	if a.recoveryManager() != nil {
		a.persistAsync(identity.MainAgentID, toolMsg)
	}
	a.recordEvidenceFromMessage(toolMsg)
}

// handleToolResult processes a single tool execution result. When all pending
// tool calls for the current turn have completed, a new LLM call is initiated
// to let the model decide what to do next.

func rawToolResultForVerification(payload *ToolResultPayload) string {
	if payload == nil {
		return ""
	}
	return payload.Result
}

func (a *MainAgent) toolResultParts(text string, images []message.ContentPart) []message.ContentPart {
	a.llmMu.RLock()
	client := a.llmClient
	modelName := a.modelName
	a.llmMu.RUnlock()
	parts, dropped := toolResultPartsForCapability(text, images, client)
	if dropped.any() && a.unsupportedPartToast.first(modelName, toastCategoryToolResult, dropped.summary()) {
		a.emitToTUI(ToastEvent{Message: "The current model does not support " + dropped.summary() + " tool-result attachments; attachments were ignored", Level: "warn"})
	}
	return parts
}

// toolCallSkillNameFromContext is the Snapshot-free form of
// toolCallSkillName: it resolves the skill name through the manager's
// read-locked backward scan.
func (a *MainAgent) toolCallSkillNameFromContext(callID, fallbackArgsJSON string) string {
	callID = strings.TrimSpace(callID)
	if a == nil || a.ctxMgr == nil || callID == "" {
		return toolCallSkillName(nil, callID, fallbackArgsJSON)
	}
	var name string
	a.ctxMgr.ScanBackward(func(msg *message.Message) bool {
		if msg.Role != message.RoleAssistant || len(msg.ToolCalls) == 0 {
			return false
		}
		for _, tc := range msg.ToolCalls {
			if tc.ID == callID && tools.NormalizeName(tc.Name) == tools.NameSkill {
				if parsed := parseSkillToolCallName(tc.Args); parsed != "" {
					name = parsed
					return true
				}
			}
		}
		return false
	})
	if name != "" {
		return name
	}
	return toolCallSkillName(nil, callID, fallbackArgsJSON)
}

// assistantContentForToolCall returns the content of the assistant message
// that declared callID, resolved through the read-locked backward scan.
func (a *MainAgent) assistantContentForToolCall(callID string) string {
	callID = strings.TrimSpace(callID)
	if a == nil || a.ctxMgr == nil || callID == "" {
		return ""
	}
	var content string
	found := false
	a.ctxMgr.ScanBackward(func(msg *message.Message) bool {
		if msg.Role != message.RoleAssistant || len(msg.ToolCalls) == 0 {
			return false
		}
		for _, tc := range msg.ToolCalls {
			if tc.ID == callID {
				content = msg.Content
				found = true
				return true
			}
		}
		return false
	})
	if !found {
		return ""
	}
	return content
}

func parseSkillToolCallName(args []byte) string {
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

func toolCallSkillName(msgs []message.Message, callID, fallbackArgsJSON string) string {
	callID = strings.TrimSpace(callID)
	parse := parseSkillToolCallName
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

// composedToolResultTexts carries the loop-finalized display/context texts of
// a tool result.
type composedToolResultTexts struct {
	Display   string
	Context   string
	ErrorText string
	IsError   bool
}

// finalizeToolResultTexts composes the model/display-facing texts of a tool
// result and runs the sync on_before_tool_result_append hook. It runs on the
// tool-execution goroutine that produced the result: the hook may block for
// its full timeout, and running it here delays only this result's delivery,
// never event-loop dispatch. The efficiency note is consumed here as well (the
// state is mutex-guarded), so the hook sees the same context text it always
// has; the loop-side advice appended after the hook (retry guidance, protocol
// guards) keeps its post-hook position.
func finalizeToolResultTexts(ctx context.Context, turn *Turn, fireHook func(context.Context, string, uint64, map[string]any) (*hook.Result, error), callID, name, argsJSON, rawResult string, resultErr error, audit *message.ToolArgsAudit, fileState *message.ToolFileState) *composedToolResultTexts {
	displayResult, contextResult, errorText, isError := composeToolResultTexts(rawResult, resultErr)
	contextResult = applyToolArgsAuditToContextResult(contextResult, audit)
	contextResult = appendModelContextNote(contextResult, turn.efficiencyNoteForToolResult(callID, name, argsJSON, rawResult, isError))

	hookResult, hookErr := fireHook(ctx, hook.OnBeforeToolResultAppend, turn.ID, buildBeforeToolResultAppendData(
		name,
		argsJSON,
		rawResult,
		displayResult,
		contextResult,
		resultErr,
		audit,
		fileState,
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
	return &composedToolResultTexts{Display: displayResult, Context: contextResult, ErrorText: errorText, IsError: isError}
}

// composeToolResultTextsOffLoop runs the composition pipeline (including the
// sync on_before_tool_result_append hook) for a result that reached the event
// loop without composed texts, then re-delivers the finished payload. Synthetic
// results — persistence-barrier failures and batch cancellations — are the only
// ones that arrive this way with sync hooks configured, and they must not stall
// event-loop dispatch behind a hook that can block for its full timeout.
func (a *MainAgent) composeToolResultTextsOffLoop(turnID uint64, payload *ToolResultPayload) {
	turn := a.turn
	go func() {
		payload.composedTexts = finalizeToolResultTexts(turn.Ctx, turn, a.fireHook, payload.CallID, payload.Name, payload.ArgsJSON, payload.Result, payload.Error, payload.Audit, payload.FileState)
		a.sendEvent(Event{Type: EventToolResult, TurnID: turnID, Payload: payload})
	}()
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
	if payload.composedTexts == nil && a.syncToolHooksConfigured() {
		// Synthetic results (persistence-barrier failures, batch cancellations)
		// are built without composed texts. Composing one here would run the
		// sync on_before_tool_result_append hook on the event loop, so compose it
		// on a goroutine and re-deliver the finished payload; the composed branch
		// below then owns the bookkeeping exactly once.
		a.composeToolResultTextsOffLoop(evt.TurnID, payload)
		return
	}
	if a.walltime != nil {
		a.walltime.recordTarget(payload.walltimeTarget, analytics.WalltimePurposeTool, payload.Duration)
	}
	a.turn.markToolCallCompleted(payload.CallID)
	a.loopState.markProgress()
	if payload.Error == nil {
		if isVerificationLikeToolResult(payload, rawToolResultForVerification(payload)) {
			a.loopState.markProgress()
		}
		if tools.NormalizeName(payload.Name) == tools.NameTodoWrite && todoWriteArgsAllDone(payload.ArgsJSON) {
			a.beginContextReductionWrapUpGrace()
			a.stageCompletionCandidateTurnID = a.turn.ID
			a.stageCompletionCandidatePending = true
			a.stageCompletionCandidatePromptDelivered = false
			a.recordCompactionLifecycleEvent("stage_candidate", map[string]string{
				"turn_id": strconv.FormatUint(a.turn.ID, 10),
				"source":  tools.NameTodoWrite,
			})
		}
		if tools.NormalizeName(payload.Name) == tools.NameSkill {
			if skillName := a.toolCallSkillNameFromContext(payload.CallID, payload.ArgsJSON); skillName != "" {
				a.MarkSkillInvokedByName(skillName)
			}
		}
	}

	rawResult := payload.Result
	var displayResult, contextResult, errorText string
	var isError bool
	if composed := payload.composedTexts; composed != nil {
		// The execution goroutine already composed the texts and ran the sync
		// append hook off the event loop.
		displayResult, contextResult, errorText, isError = composed.Display, composed.Context, composed.ErrorText, composed.IsError
	} else {
		// Promoted speculative results and synthetic results finalize here.
		// Without user hooks this is a fast path; hook-configured runs have no
		// promoted speculative results (they skip speculative reuse), so the
		// blocking hook only ever runs off the event loop.
		displayResult, contextResult, errorText, isError = composeToolResultTexts(rawResult, payload.Error)
		contextResult = applyToolArgsAuditToContextResult(contextResult, payload.Audit)
		contextResult = appendModelContextNote(contextResult, a.turn.efficiencyNoteForToolResult(payload.CallID, payload.Name, payload.ArgsJSON, rawResult, isError))

		hookResult, hookErr := a.fireHook(a.turn.Ctx, hook.OnBeforeToolResultAppend, a.turn.ID, buildBeforeToolResultAppendData(
			payload.Name,
			payload.ArgsJSON,
			rawResult,
			displayResult,
			contextResult,
			payload.Error,
			payload.Audit,
			payload.FileState,
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
	}

	a.fireHookBackground(a.turn.Ctx, hook.OnToolResult, a.turn.ID, buildToolResultHookData(
		payload.Name,
		payload.ArgsJSON,
		contextResult,
		payload.Error,
		payload.Diff,
		payload.Audit,
		payload.FileState,
	))

	// Model-facing advisory for repeated approximate-match failures, added
	// after the hooks above so user-configured transformations are not
	// overwritten. Only contextResult changes; displayResult (and the TUI)
	// stays untouched.
	toolBaseDir := a.effectiveToolBaseDir()
	a.applyPatchRetry.observeResult(payload.Name, payload.ArgsJSON, toolBaseDir, payload.Error)
	contextResult = appendEditRetryAdvice(&a.editMatchFailStreak, contextResult, payload.Name, payload.ArgsJSON, toolBaseDir, payload.Error, isError)
	// Bounded stop-loss for the notify response-protocol error family: the
	// second consecutive failure appends a reinforced corrective note, and the
	// third flags the turn to pause at the batch closeout (see
	// notify_protocol_guard.go). The model-visible context result changes, the
	// display result stays untouched, and every tool error keeps its own
	// terminal text — a response is never silently downgraded into a plain
	// notification.
	if note, _ := a.observeNotifyProtocolFailure(payload.Name, payload.ArgsJSON, payload.Error); note != "" {
		contextResult = appendModelContextNote(contextResult, note)
	}

	if payload.Name == tools.NameHandoff && payload.Error == nil {
		var pcData struct {
			PlanPath string `json:"plan_path"`
		}
		if err := json.Unmarshal([]byte(payload.Result), &pcData); err != nil {
			log.Errorf("handleToolResult: failed to parse Handoff result error=%v", err)
		} else {
			log.Infof("Handoff result received; deferring until sibling tools complete plan_path=%v pending=%v", pcData.PlanPath, a.turn.PendingToolCalls.Load()-1)
			a.setPendingHandoff(&HandoffResult{
				PlanPath: pcData.PlanPath,
				ArgsJSON: payload.ArgsJSON,
				CallID:   payload.CallID,
				Result:   contextResult,
				Duration: payload.Duration,
			})
		}
	}
	if payload.Name == tools.NameDone && payload.Error == nil {
		assistantContent := a.assistantContentForToolCall(payload.CallID)
		a.pendingLoopExitResults = append(a.pendingLoopExitResults, &loopExitResult{CallID: payload.CallID, Reason: strings.TrimSpace(contextResult), AssistantContent: assistantContent, TurnID: a.turn.ID, ArgsJSON: payload.ArgsJSON})
	}

	// Model-driven checkpoint: validate and arm the pending request BEFORE the
	// tool message is appended. A validation failure becomes an ordinary error
	// result and never arms a barrier; success writes the canonical "accepted"
	// text so a crash between acceptance and apply cannot read as a reset.
	modelDrivenAccepted := false
	if payload.Name == tools.NameCompactContext && payload.Error == nil {
		if result, err := a.tryArmModelDrivenCheckpoint(payload.CallID, payload.ArgsJSON); err != nil {
			log.Warnf("compact_context rejected call_id=%v error=%v", payload.CallID, err)
			rawResult = fmt.Sprintf("Context checkpoint rejected: %v", err)
			displayResult, contextResult, errorText, isError = composeToolResultTexts(rawResult, fmt.Errorf("%v", err))
			contextResult = applyToolArgsAuditToContextResult(contextResult, payload.Audit)
		} else {
			rawResult = result
			displayResult, contextResult, errorText, isError = composeToolResultTexts(rawResult, nil)
			contextResult = applyToolArgsAuditToContextResult(contextResult, payload.Audit)
			modelDrivenAccepted = true
		}
	}

	// A rejected compact_context result is still a real tool result the model
	// must see; only an accepted (or errored-at-execution) call defers emission
	// to the batch-end barrier.
	deferToolResultEmission := payload.Error == nil && (payload.Name == tools.NameDone || payload.Name == tools.NameHandoff || (payload.Name == tools.NameCompactContext && modelDrivenAccepted))
	parts := a.toolResultParts(contextResult, payload.Images)
	if !deferToolResultEmission {
		a.emitToTUI(ToolResultEvent{
			CallID:        payload.CallID,
			Name:          payload.Name,
			ArgsJSON:      payload.ArgsJSON,
			Audit:         payload.Audit.Clone(),
			Result:        displayResult,
			Payload:       payload.Payload,
			Notes:         append([]string(nil), payload.Notes...),
			Status:        toolResultStatusFromError(isError),
			Parts:         parts,
			Diff:          payload.Diff,
			DiffAdded:     payload.DiffAdded,
			DiffRemoved:   payload.DiffRemoved,
			FileCreated:   payload.FileCreated,
			FileState:     payload.FileState.Clone(),
			Duration:      payload.Duration,
			RecoveryState: payload.RecoveryState,
		})
	}

	a.queueLSPDiagnosticOverlayFromContext(payload)
	if !deferToolResultEmission {
		toolMsg := a.buildToolResultMessage(payload, contextResult, parts, isError, toolProvenanceFromContext(a.ctxMgr, payload.CallID))
		a.ctxMgr.Append(toolMsg)
		if a.recoveryManager() != nil {
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
		if a.turn.BarrierFailureRounds >= maxIntentBarrierFailureRounds {
			log.Warnf("aborting turn: repeated intent-barrier persistence failures rounds=%v threshold=%v", a.turn.BarrierFailureRounds, maxIntentBarrierFailureRounds)
			a.emitToTUI(ErrorEvent{
				Err: fmt.Errorf(
					"turn aborted: session persistence failed %d rounds in a row, so tool "+
						"execution stays paused to avoid unrecoverable repeated side effects. "+
						"Check disk space or restart the session",
					a.turn.BarrierFailureRounds,
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
		if a.pendingModelDriven != nil {
			// Tool-batch barrier for a model-driven checkpoint: append the
			// accepted tool result so it is part of the archived head, emit the
			// terminal TUI event, then hand control to the compaction worker.
			// The worker snapshot is taken after this append, so the declaring
			// tool message and its result are always archived together.
			if modelDrivenAccepted {
				a.appendDeferredModelDrivenToolResult(payload, contextResult, parts, isError)
			}
			if a.maybeStartModelDrivenBarrier() {
				return
			}
		}
		if a.pendingHandoff != nil {
			pc := a.pendingHandoff
			a.lastPlanPath = pc.PlanPath
			pc.RequestID = makeRequestID()
			a.interaction.openHandoff(pc.RequestID, a.walltime.captureAt(identity.MainAgentID, a.agentNameForInteraction(identity.MainAgentID), a.currentTurnID()))
			log.Infof("all sibling tools complete; finalizing Handoff plan_path=%v request_id=%v", pc.PlanPath, pc.RequestID)
			a.emitToTUI(HandoffEvent{PlanPath: pc.PlanPath, CallID: pc.CallID, ArgsJSON: pc.ArgsJSON, RequestID: pc.RequestID, AgentID: identity.MainAgentID})
			a.emitToTUI(NotificationEvent{Reason: NotificationReasonUserInputRequired, Message: "Chord: Handoff requires your decision"})
			a.markControlAction()
			a.emitActivity("main", ActivityIdle, "")
			a.suspendPendingUserDrain()
			a.setIdleAndDrainPending()
			return
		}
		if len(a.pendingLoopExitResults) > 0 {
			pendingResults := a.pendingLoopExitResults
			a.pendingLoopExitResults = nil
			if len(pendingResults) > 1 {
				for _, skipped := range pendingResults[:len(pendingResults)-1] {
					rejection := "Done rejected: only one `done` call can be handled in a batch; keep a single final `done` call after the remaining tool work is complete."
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

		// Repeated notify response-protocol failures pause the automatic retry
		// at the tool-batch closeout: instead of starting the next LLM round,
		// end the turn and ask the user to correct the intended notify. The
		// flag is only set by observeNotifyProtocolFailure on the third
		// consecutive failure, so a single isolated error never reaches here.
		if a.consumeNotifyProtocolPause() {
			return
		}

		log.Debugf("all tool calls complete, calling LLM again turn_id=%v", a.turn.ID)
		a.mergePendingInputsForTurnContinuation()
		turnID := a.turn.ID
		turnCtx := a.turn.Ctx
		a.beginMainLLMAfterPreparation(turnCtx, turnID, "")
	}
}
