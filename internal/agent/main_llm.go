package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/keakon/golog"
	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// contextLengthExceededPendingCompactionError indicates an LLM request failed
// due to context length exceeded while compaction is running. The caller
// should suspend the LLM call and wait for compaction to complete, then retry.
type contextLengthExceededPendingCompactionError struct {
	inner error
}

func (e *contextLengthExceededPendingCompactionError) Error() string {
	return fmt.Sprintf("context length exceeded (compaction in progress): %v", e.inner)
}

func (e *contextLengthExceededPendingCompactionError) Unwrap() error {
	return e.inner
}

// IsContextLengthExceededPendingCompaction reports whether err is a
// context-length-exceeded error that should be suspended pending compaction.
func IsContextLengthExceededPendingCompaction(err error) bool {
	var e *contextLengthExceededPendingCompactionError
	return errors.As(err, &e)
}

// modelNameFromRef extracts the model name from a provider/model reference.
// Examples: "meowoo/glm-5.1" → "glm-5.1", "qt/gpt-5.5" → "gpt-5.5",
// "glm-5.1" → "glm-5.1" (bare name returned as-is).
func modelNameFromRef(providerModelRef string) string {
	if idx := strings.LastIndex(providerModelRef, "/"); idx >= 0 && idx < len(providerModelRef)-1 {
		return providerModelRef[idx+1:]
	}
	return providerModelRef
}

// waitGitStatus blocks until the async git status fetch is done. The result
// is injected into the first user message by injectGitStatusIntoFirstUserMessage
// and is not part of the stable system prompt, so no system-prompt refresh is
// needed.
func (a *MainAgent) waitGitStatus(ctx context.Context) {
	if a.gitStatusReady == nil {
		return
	}
	select {
	case <-a.gitStatusReady:
		a.gitStatusReady = nil
	case <-ctx.Done():
	}
}

func (a *MainAgent) ensureSessionBuilt(ctx context.Context) error {
	if a.sessionBuilt.Load() {
		return nil
	}

	a.sessionInitMu.Lock()
	defer a.sessionInitMu.Unlock()

	if a.sessionBuilt.Load() {
		return nil
	}

	a.waitGitStatus(ctx)

	a.mcpReadyMu.Lock()
	mcpReady := a.mcpReady
	a.mcpReadyMu.Unlock()

	for _, ch := range []chan struct{}{a.agentsMDReady, a.skillsReady, mcpReady} {
		if ch == nil {
			continue
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	a.mcpServersPromptMu.Lock()
	pendingMCPTools := append([]tools.Tool(nil), a.pendingMCPTools...)
	pendingMCPReplace := a.pendingMCPReplace
	a.pendingMCPTools = nil
	a.pendingMCPReplace = false
	a.mcpServersPromptMu.Unlock()

	if pendingMCPReplace && a.tools != nil {
		_ = a.tools.UnregisterPrefix("mcp_")
	}
	for _, t := range pendingMCPTools {
		a.tools.Register(t)
	}

	if a.shouldFreezeLLMContextSurface() {
		a.sessionBuilt.Store(true)
		return nil
	}

	if !a.surfaceDirty.Load() {
		a.refreshSystemPrompt()
		a.refreshSessionContextReminder()
		a.freezeToolSurface()
		a.sessionBuilt.Store(true)
		return nil
	}

	candidatePrompt := a.currentSystemPromptCandidate()
	candidateTools := llmToolDefinitionsFromVisibleTools(a.mainVisibleLLMTools())
	if a.currentLLMContextSurfaceMatches(candidatePrompt, candidateTools) {
		a.sessionBuilt.Store(true)
		a.surfaceDirty.Store(false)
		return nil
	}
	a.installSystemPrompt(candidatePrompt)
	a.refreshSessionContextReminder()
	a.freezeToolSurfaceFromDefinitions(candidateTools)
	a.surfaceDirty.Store(false)
	a.sessionBuilt.Store(true)
	return nil
}

func (a *MainAgent) resetSessionBuildState() {
	a.sessionBuilt.Store(false)
	if a.shouldFreezeLLMContextSurface() {
		return
	}
	a.sessionReminderInjected.Store(false)
	a.clearSessionContextReminder()
	a.clearFrozenToolSurface()
}

func (a *MainAgent) markRuntimeSurfaceDirty() {
	a.sessionBuilt.Store(false)
	a.surfaceDirty.Store(true)
}

func (a *MainAgent) currentSystemPromptCandidate() string {
	a.llmMu.RLock()
	override := a.systemPromptOverride
	a.llmMu.RUnlock()
	if override != "" {
		return override
	}
	return a.buildSystemPrompt()
}

func (a *MainAgent) currentLLMContextSurfaceMatches(prompt string, defs []message.ToolDefinition) bool {
	a.llmMu.RLock()
	installed := a.installedSysPrompt
	a.llmMu.RUnlock()
	if prompt != installed {
		return false
	}
	frozen := a.frozenToolDefs.Load()
	if frozen == nil {
		return len(defs) == 0
	}
	return reflect.DeepEqual(*frozen, defs)
}

func (a *MainAgent) ensureMainModelPolicy() error {
	if !a.mainModelPolicyDirty.Load() {
		return nil
	}
	a.mainModelPolicyMu.Lock()
	if !a.mainModelPolicyDirty.Load() {
		a.mainModelPolicyMu.Unlock()
		return nil
	}
	if waitCh := a.mainModelPolicyBuild; waitCh != nil {
		a.mainModelPolicyMu.Unlock()
		// Never block the request path indefinitely on a background prewarm build.
		// If it takes too long, keep serving with the current client and let a later
		// turn retry policy refresh.
		select {
		case <-waitCh:
		case <-time.After(2 * time.Second):
			log.Warn("main model policy build wait timed out; proceeding with current client")
			return nil
		}
		a.mainModelPolicyMu.Lock()
		err := a.mainModelPolicyErr
		a.mainModelPolicyMu.Unlock()
		return err
	}
	waitCh := make(chan struct{})
	a.mainModelPolicyBuild = waitCh
	a.mainModelPolicyMu.Unlock()

	err := a.buildMainModelPolicy()

	a.mainModelPolicyMu.Lock()
	a.mainModelPolicyBuild = nil
	a.mainModelPolicyErr = err
	close(waitCh)
	a.mainModelPolicyMu.Unlock()
	return err
}

func (a *MainAgent) buildMainModelPolicy() error {
	if a.modelSwitchFactory == nil {
		return nil
	}

	a.llmMu.RLock()
	selectedRef := a.providerModelRef
	a.llmMu.RUnlock()
	if strings.TrimSpace(selectedRef) == "" {
		return nil
	}

	client, modelName, ctxLimit, err := a.modelSwitchFactory(selectedRef)
	if err != nil {
		return fmt.Errorf("prepare main-agent model policy for %q: %w", selectedRef, err)
	}
	a.swapLLMClientWithRef(client, modelName, ctxLimit, selectedRef)
	a.mainModelPolicyDirty.Store(false)
	return nil
}

// PrewarmModelPolicy prepares the current main-agent model policy in the
// background so the first real LLM request doesn't pay the setup cost.
func (a *MainAgent) PrewarmModelPolicy() error {
	return a.ensureMainModelPolicy()
}

type mainLLMStreamState struct {
	pendingKeySwitch      bool
	pendingSwitchBack     bool
	pendingFallbackRef    string
	pendingFallbackReason string
	streamingPromoted     bool
	requestProgressBytes  int64
	requestProgressEvents int64
}

func (a *MainAgent) newMainLLMStreamReducer(llmClient *llm.Client, selectedRef, prevRunningRef string, turn *Turn, scrubThinkingMarkers bool, state *mainLLMStreamState) *llmStreamReducer {
	if state == nil {
		state = &mainLLMStreamState{}
	}
	state.pendingSwitchBack = prevRunningRef != "" && prevRunningRef != selectedRef && modelNameFromRef(prevRunningRef) != modelNameFromRef(selectedRef)

	promoteStreamingActivity := func(source string) {
		if state.streamingPromoted {
			return
		}
		state.streamingPromoted = true
		a.emitActivity("main", ActivityStreaming, "")
	}

	updateRunningModelRef := func(confirmedRef string) {
		confirmedRef = strings.TrimSpace(confirmedRef)
		if confirmedRef == "" {
			return
		}
		a.llmMu.Lock()
		prev := a.runningModelRef
		a.runningModelRef = confirmedRef
		provRef := a.providerModelRef
		a.llmMu.Unlock()
		if confirmedRef != prev {
			a.emitToTUI(RunningModelChangedEvent{
				AgentID:          a.instanceID,
				ProviderModelRef: provRef,
				RunningModelRef:  confirmedRef,
			})
		}
	}

	emitConfirmedSwitchToast := func(confirmedRef, confirmedReason string) {
		confirmedRef = strings.TrimSpace(confirmedRef)
		// If we had queued a fallback candidate but the first visible token came
		// from a different model, discard the stale candidate to avoid emitting
		// a misleading toast on a later key_confirmed.
		if state.pendingFallbackRef != "" && confirmedRef != "" && confirmedRef != state.pendingFallbackRef {
			state.pendingFallbackRef = ""
			state.pendingFallbackReason = ""
		}
		switch {
		case state.pendingFallbackRef != "" && confirmedRef != "" && confirmedRef == state.pendingFallbackRef:
			reason := strings.TrimSpace(state.pendingFallbackReason)
			if reason == "" {
				reason = strings.TrimSpace(confirmedReason)
			}
			msg := fmt.Sprintf("Switched to fallback model: %s", confirmedRef)
			if reason == "context_length_exceeded" {
				msg = fmt.Sprintf("Current model context exceeded; switched to fallback model: %s", confirmedRef)
			} else if reason != "" {
				msg = fmt.Sprintf("Switched to fallback model (%s): %s", reason, confirmedRef)
			}

			a.emitToTUI(ToastEvent{Message: msg, Level: "warn"})
		case state.pendingSwitchBack && confirmedRef != "" && modelNameFromRef(confirmedRef) == modelNameFromRef(selectedRef):
			msg := "Switched back to selected model"

			a.emitToTUI(ToastEvent{Message: msg, Level: "info"})
		case state.pendingKeySwitch:
			a.emitToTUI(ToastEvent{Message: "Switched key", Level: "info"})
		default:
			return
		}

		state.pendingKeySwitch = false
		// Only clear switch-back after it is actually confirmed on a visible token.
		if state.pendingFallbackRef != "" && confirmedRef == state.pendingFallbackRef {
			state.pendingFallbackRef = ""
			state.pendingFallbackReason = ""
		} else if state.pendingSwitchBack && confirmedRef != "" && modelNameFromRef(confirmedRef) == modelNameFromRef(selectedRef) {
			state.pendingSwitchBack = false
		}
	}

	streamingThinkingMessageIndex := len(a.ctxMgr.Snapshot())
	streamingThinkingBlockIndex := 0

	streamReducer := &llmStreamReducer{}
	streamReducer.content = streamContentReducer{
		agentID: "",
		emit:    a.emitToTUI,
		appendPartialText: func(text string) {
			if turn != nil {
				turn.appendPartialText(text)
			}
		},
		scrubThinkingDelta:      scrubThinkingMarkers,
		scrubThinkingFinal:      scrubThinkingMarkers,
		emitThinkingStarted:     true,
		ignoreThinkingAfterText: true,
		closeThinkingOnText:     true,
		closeThinkingOnFinish:   true,
		thinkingCommitMode:      streamContentCommitEmpty,
		onThinkingBlockClosed: func(agentID, text string) {
			_ = agentID
			blockIndex := streamingThinkingBlockIndex
			streamingThinkingBlockIndex++
			a.scheduleStreamingThinkingTranslation(streamingThinkingMessageIndex, blockIndex, text)
		},
		textFlushInterval:     defaultStreamTextFlushInterval,
		thinkingFlushInterval: defaultStreamThinkingFlushInterval,
	}
	streamReducer.tool = streamToolDeltaReducer{
		agentID:                  "",
		turn:                     turn,
		registry:                 a.tools,
		ruleset:                  a.effectiveRuleset,
		emit:                     a.emitToTUI,
		flushBeforeTool:          streamReducer.content.flushTextDelta,
		promoteStreamingActivity: promoteStreamingActivity,
		recordToolUseEnd:         a.recordToolTraceToolUseEnd,
		discardSpeculativeOnRollback: func(turn *Turn, reason string) {
			a.discardSpeculativeStreamToolsAndClearToolTrace(turn, reason)
		},
		drainPartialTextOnRollback: true,
	}
	streamReducer.emitActivity = func(activity ActivityType, detail string) {
		a.emitActivity("main", activity, detail)
	}
	streamReducer.promoteStreamingActivity = promoteStreamingActivity
	streamReducer.onProgress = func(progress *message.StreamProgressDelta) {
		state.requestProgressBytes = progress.Bytes
		state.requestProgressEvents = progress.Events
		a.emitToTUI(RequestProgressEvent{AgentID: a.instanceID, Bytes: state.requestProgressBytes, Events: state.requestProgressEvents})
	}
	streamReducer.beforeStatus = func(status *message.StatusDelta) {
		// Any status carrying ModelRef means the retry loop is actively attempting
		// that target. Reflect it immediately so sidebar errors/toasts and MODEL
		// stay aligned even before the target emits visible output.
		if status.ModelRef != "" {
			if llmClient != nil {
				if lim := llmClient.ContextLimitForModelRef(status.ModelRef); lim > 0 {
					a.ctxMgr.SetTokenBudgets(lim, llmClient.InputLimitForModelRef(status.ModelRef), a.effectiveCompactionReservedInput())
				}
			}
			updateRunningModelRef(status.ModelRef)
			// Only treat as fallback if the model name differs from selected.
			// Same model name with different provider is effectively a key switch.
			if status.Type == "retrying" && modelNameFromRef(status.ModelRef) != modelNameFromRef(selectedRef) {
				state.pendingFallbackRef = status.ModelRef
				state.pendingFallbackReason = status.Reason
			}
		}
	}
	streamReducer.onRateLimits = func(delta message.StreamDelta) {
		if delta.RateLimit != nil {
			a.updateRateLimitSnapshot(delta.RateLimit)
		}
	}
	streamReducer.onKeySwitched = func() {
		a.clearCurrentRateLimitSnapshot()
		a.noteContextSurfaceIdentityChanged()
		state.pendingKeySwitch = true
		a.emitToTUI(KeyPoolChangedEvent{})
	}
	streamReducer.onKeyDeactivated = func(email, accountID string) {
		a.emitToTUI(ToastEvent{Message: fmt.Sprintf("Account deactivated: %s", streamKeyIdentity(email, accountID)), Level: "error"})
		a.emitToTUI(KeyPoolChangedEvent{})
	}
	streamReducer.onKeyInvalidated = func(email, accountID string) {
		a.emitToTUI(ToastEvent{Message: fmt.Sprintf("Account invalidated: %s. Please sign in again.", streamKeyIdentity(email, accountID)), Level: "error"})
		a.emitToTUI(KeyPoolChangedEvent{})
	}
	streamReducer.onKeyExpired = func(email, accountID string) {
		a.emitToTUI(ToastEvent{Message: fmt.Sprintf("OAuth refresh token invalid: %s. Please sign in again.", streamKeyIdentity(email, accountID)), Level: "error"})
		a.emitToTUI(KeyPoolChangedEvent{})
	}
	streamReducer.onKeyConfirmed = func(status *message.StatusDelta) {
		// First visible token received on the current key: update key availability now.
		a.emitToTUI(KeyPoolChangedEvent{})
		confirmedRef := ""
		confirmedReason := ""
		if status != nil {
			confirmedRef = status.ModelRef
			confirmedReason = status.Reason
		}
		// Ensure the sidebar reflects the model that actually produced the first visible token.
		updateRunningModelRef(confirmedRef)
		// Confirmed toasts must be keyed off the model that actually emitted output.
		emitConfirmedSwitchToast(confirmedRef, confirmedReason)
	}
	streamReducer.onError = func(text string) {
		log.Warnf("LLM stream error delta text=%v instance=%v", text, a.instanceID)
	}
	return streamReducer
}

// callLLM invokes the LLM with the given message history. Streaming deltas are
// forwarded to the TUI in real-time via emitToTUI. Token usage is recorded in
// the context manager.
func (a *MainAgent) callLLM(ctx context.Context, messages []message.Message) (*message.Response, error) {
	// Build the session's stable tool/system surface before the first real request.
	if err := a.ensureSessionBuilt(ctx); err != nil {
		return nil, err
	}
	if err := a.ensureMainModelPolicy(); err != nil {
		return nil, err
	}
	messages = a.prepareMessagesForLLM(messages)
	messages = a.injectCompactionFileContext(messages)
	if repaired, dropped := message.RepairOrphanToolResults(messages); dropped > 0 {
		log.Warnf("dropping orphan tool result messages before LLM request dropped=%v", dropped)
		messages = repaired
	}

	// On the first LLM call, prepend git status to the first user message so
	// the model has repository context without polluting the stable system prompt.
	a.injectGitStatusIntoFirstUserMessage(messages)
	a.rememberPreparedLLMRequest(a.currentTurnID(), messages)
	a.consumeContextSurfaceRefreshAllowance()

	// Inject the <system-reminder> meta user message carrying AGENTS.md +
	// currentDate before the first user message. This is a per-request overlay;
	// it never enters ctxMgr or the session jsonl.
	messages = a.injectSessionContextReminder(messages)

	// Assemble per-turn overlays (SubAgent mailbox, bug triage hint, loop
	// continuation) and prepend them before the first user message. Overlays
	// never modify the stable system prompt.
	messages = injectTurnOverlays(messages, a.buildTurnOverlayMessages())

	// Emit early activity event so the TUI shows "connecting" immediately,
	// before the HTTP request starts.
	a.emitActivity("main", ActivityConnecting, "")

	// Snapshot llmClient and modelName under a brief read-lock so that a
	// concurrent SwapLLMClient cannot produce an inconsistent pair (e.g.
	// old client with new model name). All subsequent reads in this
	// function use the snapshot variables.
	llmClient, modelName, selectedRef, prevRunningRef := a.llmSnapshot()
	compatCfg := llmClient.ThinkingToolcallCompat() // use snapshot for consistency with llmClient
	scrubThinkingMarkers := compatCfg != nil && compatCfg.EnabledValue()

	// Snapshot the turn pointer once so the stream callback never races with
	// setIdleAndDrainPending() clearing a.turn. The callback should only operate
	// on this snapshot (which may be nil).
	turn := a.currentTurn()
	turnID := uint64(0)
	if turn != nil {
		turnID = turn.ID
	}

	toolDefs := a.mainLLMToolDefinitions()

	// The stream callback runs on the goroutine that owns the HTTP response
	// reader, so high-volume deltas stay best-effort while durable/structural
	// events are emitted through the shared reducer.
	streamState := &mainLLMStreamState{}
	streamReducer := a.newMainLLMStreamReducer(llmClient, selectedRef, prevRunningRef, turn, scrubThinkingMarkers, streamState)
	callback := streamReducer.Handle

	// Request-context telemetry helps diagnose oversized prompts without changing the request surface.
	if log.IsEnabledFor(golog.DebugLevel) {
		contributors := topContextContributors(messages, 5)
		labels := make([]string, 0, len(contributors))
		for _, c := range contributors {
			labels = append(labels, contextContributorLabel(c))
		}
		stats := a.GetContextReductionStats()
		a.llmMu.RLock()
		installedPrompt := a.installedSysPrompt
		a.llmMu.RUnlock()
		log.Debugf("LLM request context contributors messages=%v estimated_tokens=%v reduction_messages=%v reduction_bytes=%v reduction_tokens_before=%v reduction_tokens_after=%v reduction_tokens_saved=%v reduction_protected=%v reduction_reused_stable=%v reduction_by_tool_rule=%v top=%v", len(messages), llm.EstimateRequestInputTokens(installedPrompt, messages, toolDefs), stats.Messages, stats.Bytes, stats.TokensBefore, stats.TokensAfter, stats.TokensSaved, stats.Protected, stats.ReusedStable, stats.ByToolAndRule, labels)
	}

	// Hook: on_before_llm_call (before LLM call).
	hookResult, hookErr := a.fireHook(ctx, hook.OnBeforeLLMCall, turnID, map[string]any{
		"model":         modelName,
		"message_count": len(messages),
	})
	if hookErr != nil {
		log.Warnf("on_before_llm_call hook error error=%v", hookErr)
	} else if hookResult != nil {
		switch hookResult.Action {
		case hook.ActionBlock:
			msg := "blocked by on_before_llm_call hook"
			if hookResult.Message != "" {
				msg = hookResult.Message
			}
			return nil, fmt.Errorf("LLM request %s", msg)
		case hook.ActionModify:
			log.Warn("on_before_llm_call hook returned modify action; modifying LLM requests is not supported, continuing")
		}
	}

	resp, err := llmClient.CompleteStream(ctx, messages, toolDefs, callback)
	completeStreamReturnedAt := time.Now()
	a.recordToolTraceCallLLMReturned(turn, completeStreamReturnedAt)
	if err == nil && turn != nil {
		a.recordTurnStreamingToolUseEnd(turn, time.Now())
	}
	// Final flush: emit any text accumulated in the last batch window.
	// This is the critical path for burst-delivery proxies that send all
	// chunks within a few milliseconds — without this flush the last
	// portion of the response would be dropped from the TUI display even
	// though it is already persisted to the JSONL log. If the stream ends
	// while still thinking, close that block before flushing any following
	// text so the viewport order stays stable.
	streamReducer.Finish()
	a.emitToTUI(RequestProgressEvent{AgentID: a.instanceID, Bytes: streamState.requestProgressBytes, Events: streamState.requestProgressEvents, Done: true})
	if err != nil {
		// Skip fallback exhausted toast for context cancellation (user CancelCurrentTurn).
		// All cancel-path errors use %w wrapping around ctx.Err(), so errors.Is
		// reliably detects user-initiated cancellation regardless of retry state.
		if errors.Is(err, context.Canceled) {
			return nil, fmt.Errorf("LLM stream failed: %w", err)
		}
		callStatus := llmClient.LastCallStatus()
		// If the context is oversized, suspend behind an in-flight compaction or
		// proactively start oversize-driven compaction when auto compact is enabled.
		if llm.IsContextLengthExceeded(err) {
			if a.IsCompactionRunning() {
				log.Infof("LLM context length exceeded while compaction running; suspending LLM call error=%v", err)
				return nil, &contextLengthExceededPendingCompactionError{inner: err}
			}
			if a.turn != nil && a.turn.OversizeRecoveryCount >= maxOversizeRecoveryAttempts {
				a.recordOversizeRecoveryAnalyticsEvent(
					"abort_retry_limit",
					"main_llm_error",
					selectedRef,
					callStatus.RunningModelRef,
					map[string]string{"trigger": "oversize_driven", "attempts": fmt.Sprintf("%d", a.turn.OversizeRecoveryCount), "action": "abort"},
				)
				return nil, fmt.Errorf("LLM stream failed: automatic context compaction already retried %d times and the context still exceeds all available models; try /compact or reduce the active context", a.turn.OversizeRecoveryCount)
			}
			if a.ensureOversizeDrivenCompaction() {
				a.recordOversizeRecoveryAnalyticsEvent(
					"trigger_compaction",
					"main_llm_error",
					selectedRef,
					callStatus.RunningModelRef,
					map[string]string{"trigger": "oversize_driven"},
				)
				a.emitToTUI(InfoEvent{Message: "All candidate models exceeded the current context; compacting context before retry"})
				log.Infof("LLM context length exceeded; started oversize-driven compaction and suspending LLM call error=%v", err)
				return nil, &contextLengthExceededPendingCompactionError{inner: err}
			}
		}
		if callStatus.FallbackTriggered && callStatus.FallbackExhausted {
			a.emitToTUI(ToastEvent{
				Message: "Fallback chain exhausted",
				Level:   "error",
			})
		}
		return nil, fmt.Errorf("LLM stream failed: %w", err)
	}

	callStatus := llmClient.LastCallStatus()
	if callStatus.RunningModelRef == "" {
		callStatus.RunningModelRef = selectedRef
	}
	if callStatus.RunningContextLimit <= 0 {
		callStatus.RunningContextLimit = llmClient.ContextLimitForModelRef(callStatus.RunningModelRef)
	}
	if callStatus.RunningInputLimit <= 0 {
		callStatus.RunningInputLimit = llmClient.InputLimitForModelRef(callStatus.RunningModelRef)
	}
	if callStatus.RunningContextLimit > 0 {
		a.ctxMgr.SetTokenBudgets(callStatus.RunningContextLimit, callStatus.RunningInputLimit, a.effectiveCompactionReservedInput())
	}
	a.llmMu.Lock()
	a.runningModelRef = callStatus.RunningModelRef
	a.llmMu.Unlock()

	if callStatus.RunningModelRef != prevRunningRef {
		a.emitToTUI(RunningModelChangedEvent{
			AgentID:          a.instanceID,
			ProviderModelRef: selectedRef,
			RunningModelRef:  callStatus.RunningModelRef,
		})
	}

	if callStatus.FallbackTriggered && callStatus.RunningModelRef != "" &&
		callStatus.RunningModelRef != selectedRef && callStatus.RunningModelRef != prevRunningRef {
		reason := callStatus.FallbackReason
		if reason == "" {
			reason = "error"
		}
		log.Warnf("switched to fallback model reason=%v from=%v to=%v", reason, selectedRef, callStatus.RunningModelRef)
	}
	if prevRunningRef != "" && prevRunningRef != selectedRef && callStatus.RunningModelRef == selectedRef {
		log.Infof("switched back to selected model model=%v", selectedRef)
	}

	// Record token usage for context compression decisions.
	if resp.Usage != nil {
		a.ctxMgr.UpdateFromUsage(*resp.Usage)
	}
	decision := a.ctxMgr.AutoCompactDecision()
	if decision.ShouldCompact {
		log.Infof("automatic context compaction requested last_input_tokens=%v threshold_tokens=%v input_budget=%v reserved_input=%v usable_input_budget=%v threshold=%v selected_model=%v running_model=%v turn_id=%v", decision.LastInputTokens, decision.ThresholdTokens, decision.InputBudget, decision.ReservedInput, decision.UsableInputBudget, decision.Threshold, selectedRef, callStatus.RunningModelRef, turnID)
		a.autoCompactRequested.Store(true)
	} else {
		log.Debugf("automatic context compaction not requested last_input_tokens=%v threshold_tokens=%v input_budget=%v reserved_input=%v usable_input_budget=%v threshold=%v selected_model=%v running_model=%v turn_id=%v", decision.LastInputTokens, decision.ThresholdTokens, decision.InputBudget, decision.ReservedInput, decision.UsableInputBudget, decision.Threshold, selectedRef, callStatus.RunningModelRef, turnID)
	}

	a.recordUsage("main", "main", a.currentAgentName(), "chat", selectedRef, callStatus.RunningModelRef, turnID, resp.Usage, callStatus.ServiceTier)

	// Hook: on_after_llm_call (after LLM call).
	inputTok, outputTok := 0, 0
	if resp.Usage != nil {
		inputTok = resp.Usage.InputTokens
		outputTok = resp.Usage.OutputTokens
	}
	hookCtxData := map[string]any{
		"model":         modelName,
		"input_tokens":  inputTok,
		"output_tokens": outputTok,
		"tool_calls":    len(resp.ToolCalls),
	}
	if len(resp.ToolCalls) > 0 {
		callIDs := make([]string, 0, len(resp.ToolCalls))
		for _, tc := range resp.ToolCalls {
			if strings.TrimSpace(tc.ID) != "" {
				callIDs = append(callIDs, tc.ID)
			}
		}
		go func(callIDs []string) {
			hookResult, hookErr := a.fireHook(a.parentCtx, hook.OnAfterLLMCall, turnID, hookCtxData)
			finishedAt := time.Now()
			for _, callID := range callIDs {
				a.recordToolTraceOnAfterLLMCallDone(callID, finishedAt)
			}
			if hookErr != nil {
				log.Warnf("on_after_llm_call hook error error=%v", hookErr)
				return
			}
			if hookResult == nil {
				return
			}
			switch hookResult.Action {
			case hook.ActionBlock:
				log.Warnf("on_after_llm_call hook returned block; ignored in background execution message=%v", hookResult.Message)
			case hook.ActionModify:
				log.Warn("on_after_llm_call hook returned modify action; modifying LLM responses is not supported, continuing")
			}
		}(callIDs)
	} else {
		go func() {
			if hookResult, hookErr := a.fireHook(a.parentCtx, hook.OnAfterLLMCall, turnID, hookCtxData); hookErr != nil {
				log.Warnf("on_after_llm_call hook error error=%v", hookErr)
			} else if hookResult != nil {
				switch hookResult.Action {
				case hook.ActionBlock:
					log.Warnf("on_after_llm_call hook returned block; ignored in background execution message=%v", hookResult.Message)
				case hook.ActionModify:
					log.Warn("on_after_llm_call hook returned modify action; modifying LLM responses is not supported, continuing")
				}
			}
		}()
	}

	// Notify C/S server to push context_usage so sidebar updates after each
	// LLM round (including tool-call rounds), not only on IdleEvent.
	a.emitToTUI(UsageUpdatedEvent{})

	return resp, nil
}

func (a *MainAgent) thinkingToolcallCompat() *config.ThinkingToolcallCompatConfig {
	client, _, _, _ := a.llmSnapshot()
	if client == nil {
		return nil
	}
	return client.ThinkingToolcallCompat()
}

// llmSnapshot returns a consistent point-in-time view of the LLM fields under
// llmMu so concurrent SwapLLMClient / SwitchModel cannot tear apart a reader.
func (a *MainAgent) llmSnapshot() (client *llm.Client, modelName, providerRef, runningRef string) {
	if a == nil {
		return nil, "", "", ""
	}
	a.llmMu.RLock()
	defer a.llmMu.RUnlock()
	return a.llmClient, a.modelName, a.providerModelRef, a.runningModelRef
}
