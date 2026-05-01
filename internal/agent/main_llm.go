package agent

import (
	"context"
	"errors"
	"fmt"
	"github.com/keakon/golog/log"
	"strings"
	"time"

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
// needed. See docs/architecture/prompt-and-context-engineering.md §5.
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

	for _, ch := range []chan struct{}{a.agentsMDReady, a.skillsReady, a.mcpReady} {
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
	a.pendingMCPTools = nil
	a.mcpServersPromptMu.Unlock()

	for _, t := range pendingMCPTools {
		a.tools.Register(t)
	}

	a.refreshSystemPrompt()
	a.refreshSessionContextReminder()
	a.freezeToolSurface()
	a.sessionBuilt.Store(true)
	return nil
}

func (a *MainAgent) resetSessionBuildState() {
	a.sessionBuilt.Store(false)
	a.sessionReminderInjected.Store(false)
	a.clearSessionContextReminder()
	a.clearFrozenToolSurface()
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

	// On the first LLM call, prepend git status to the first user message so
	// the model has repository context without polluting the stable system prompt.
	a.injectGitStatusIntoFirstUserMessage(messages)

	// Inject the <system-reminder> meta user message carrying AGENTS.md +
	// currentDate before the first user message. This is a per-request overlay;
	// it never enters ctxMgr or the session jsonl. See
	// docs/architecture/prompt-and-context-engineering.md §4.
	messages = a.injectSessionContextReminder(messages)

	// Assemble per-turn overlays (SubAgent mailbox, bug triage hint, loop
	// continuation) and prepend them before the first user message. Overlays
	// never modify the stable system prompt. See §5 of the doc above.
	messages = injectTurnOverlays(messages, a.buildTurnOverlayMessages())

	// Emit early activity event so the TUI shows "connecting" immediately,
	// before the HTTP request starts.
	a.emitActivity("main", ActivityConnecting, "")

	// Snapshot llmClient and modelName under a brief read-lock so that a
	// concurrent SwapLLMClient cannot produce an inconsistent pair (e.g.
	// old client with new model name). All subsequent reads in this
	// function use the snapshot variables.
	a.llmMu.RLock()
	llmClient := a.llmClient
	modelName := a.modelName
	selectedRef := a.providerModelRef
	prevRunningRef := a.runningModelRef
	a.llmMu.RUnlock()
	compatCfg := llmClient.ThinkingToolcallCompat() // use snapshot for consistency with llmClient
	scrubThinkingMarkers := compatCfg != nil && compatCfg.EnabledValue()

	toolDefs := a.mainLLMToolDefinitions()

	// The stream callback runs on the goroutine that owns the HTTP response
	// reader, so text/thinking deltas should remain best-effort. A small set of
	// low-frequency visibility events (for example speculative tool-card start)
	// may still use the reliable output path because dropping them would leave
	// the UI in an unrecoverable state.
	//
	// Both text and thinking deltas are batched before emission to avoid
	// flooding the output channel when a proxy delivers the entire response
	// in a single burst (all chunks arriving within a few milliseconds).
	// Text is flushed every ~20ms; thinking every ~150ms. A final flush
	// happens after CompleteStream returns so no content is lost.
	var (
		textAccum    strings.Builder
		textLastEmit time.Time

		thinkingAccum    strings.Builder
		thinkingActive   bool
		thinkingLastEmit time.Time

		pendingKeySwitch      bool
		pendingSwitchBack     = prevRunningRef != "" && prevRunningRef != selectedRef && modelNameFromRef(prevRunningRef) != modelNameFromRef(selectedRef)
		pendingFallbackRef    string
		pendingFallbackReason string
		streamingPromoted     bool
		requestProgressBytes  int64
		requestProgressEvents int64
	)
	const (
		textFlushInterval     = 20 * time.Millisecond
		thinkingFlushInterval = 150 * time.Millisecond
	)

	flushTextDelta := func() {
		if textAccum.Len() > 0 {
			text := textAccum.String()
			textAccum.Reset()
			a.emitToTUI(StreamTextEvent{Text: text})
		}
		textLastEmit = time.Now()
	}

	flushThinkingDelta := func() {
		if thinkingAccum.Len() > 0 {
			delta := thinkingAccum.String()
			thinkingAccum.Reset()
			if scrubThinkingMarkers {
				delta = scrubThinkingToolcallMarkers(delta)
			}
			if strings.TrimSpace(delta) != "" {
				a.emitToTUI(StreamThinkingDeltaEvent{Text: delta, AgentID: ""})
			}
		}
		thinkingLastEmit = time.Now()
	}
	promoteStreamingActivity := func(source string) {
		if streamingPromoted {
			return
		}
		streamingPromoted = true
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
		if pendingFallbackRef != "" && confirmedRef != "" && confirmedRef != pendingFallbackRef {
			pendingFallbackRef = ""
			pendingFallbackReason = ""
		}
		switch {
		case pendingFallbackRef != "" && confirmedRef != "" && confirmedRef == pendingFallbackRef:
			reason := strings.TrimSpace(pendingFallbackReason)
			if reason == "" {
				reason = strings.TrimSpace(confirmedReason)
			}
			msg := fmt.Sprintf("Switched to fallback model: %s", confirmedRef)
			if reason != "" {
				msg = fmt.Sprintf("Switched to fallback model (%s): %s", reason, confirmedRef)
			}

			a.emitToTUI(ToastEvent{Message: msg, Level: "warn"})
		case pendingSwitchBack && confirmedRef != "" && modelNameFromRef(confirmedRef) == modelNameFromRef(selectedRef):
			msg := "Switched back to selected model"

			a.emitToTUI(ToastEvent{Message: msg, Level: "info"})
		case pendingKeySwitch:
			a.emitToTUI(ToastEvent{Message: "Switched key", Level: "info"})
		default:
			return
		}

		pendingKeySwitch = false
		// Only clear switch-back after it is actually confirmed on a visible token.
		if pendingFallbackRef != "" && confirmedRef == pendingFallbackRef {
			pendingFallbackRef = ""
			pendingFallbackReason = ""
		} else if pendingSwitchBack && confirmedRef != "" && modelNameFromRef(confirmedRef) == modelNameFromRef(selectedRef) {
			pendingSwitchBack = false
		}
	}

	callback := func(delta message.StreamDelta) {
		if delta.Progress != nil {
			requestProgressBytes = delta.Progress.Bytes
			requestProgressEvents = delta.Progress.Events
			a.emitToTUI(RequestProgressEvent{AgentID: a.instanceID, Bytes: requestProgressBytes, Events: requestProgressEvents})
		}
		switch delta.Type {
		case "text":
			textAccum.WriteString(delta.Text)
			if a.turn != nil {
				a.turn.appendPartialText(delta.Text)
			}
			if textLastEmit.IsZero() {
				// Emit the first delta immediately for perceived responsiveness.
				flushTextDelta()
			} else if time.Since(textLastEmit) >= textFlushInterval {
				flushTextDelta()
			}
		case "thinking":
			flushTextDelta() // flush any pending text before switching to thinking
			if !thinkingActive {
				thinkingActive = true
				a.emitToTUI(ThinkingStartedEvent{})
				thinkingLastEmit = time.Now()
			}
			thinkingAccum.WriteString(delta.Text)
			if time.Since(thinkingLastEmit) >= thinkingFlushInterval {
				flushThinkingDelta()
			}
		case "thinking_end":
			// Flush any remaining thinking content.
			flushThinkingDelta()
			thinkingActive = false
			// Send the complete thinking block as commit signal for TUI.
			a.emitToTUI(StreamThinkingEvent{AgentID: ""})
		case "status":
			if delta.Status != nil {
				// When a fallback model is being tried, emit RunningModelChangedEvent
				// immediately so the info panel updates without waiting for the response.
				if delta.Status.Type == "retrying" && delta.Status.ModelRef != "" {
					if lim := llmClient.ContextLimitForModelRef(delta.Status.ModelRef); lim > 0 {
						a.ctxMgr.SetMaxTokens(lim)
					}
					a.llmMu.Lock()
					a.runningModelRef = delta.Status.ModelRef
					provRef := a.providerModelRef
					a.llmMu.Unlock()
					a.emitToTUI(RunningModelChangedEvent{
						AgentID:          a.instanceID,
						ProviderModelRef: provRef,
						RunningModelRef:  delta.Status.ModelRef,
					})
					// Only treat as fallback if the model name differs from selected.
					// Same model name with different provider is effectively a key switch.
					if modelNameFromRef(delta.Status.ModelRef) != modelNameFromRef(selectedRef) {
						pendingFallbackRef = delta.Status.ModelRef
						pendingFallbackReason = delta.Status.Reason
					}
				}
				if delta.Status.Type == string(ActivityStreaming) {
					promoteStreamingActivity("llm_status")
					return
				}
				a.emitActivity("main", ActivityType(delta.Status.Type), delta.Status.Detail)
			}
		case "tool_use_start":
			flushTextDelta() // flush any pending text before the tool call block
			promoteStreamingActivity("tool_use_start")
			if delta.ToolCall != nil {
				if a.turn != nil {
					a.turn.recordStreamingToolCall(PendingToolCall{
						CallID:   delta.ToolCall.ID,
						Name:     delta.ToolCall.Name,
						ArgsJSON: delta.ToolCall.Input,
					})
				}
				a.emitToTUI(ToolCallStartEvent{
					ID:       delta.ToolCall.ID,
					Name:     delta.ToolCall.Name,
					ArgsJSON: delta.ToolCall.Input,
				})
			}
		case "tool_use_delta":
			if delta.ToolCall != nil && a.turn != nil && delta.ToolCall.ID != "" && delta.ToolCall.Input != "" {
				promoteStreamingActivity("tool_use_delta")
				accumulated := a.turn.appendStreamingToolCallInput(delta.ToolCall.ID, delta.ToolCall.Name, delta.ToolCall.Input, "")
				if accumulated != "" {
					a.emitToTUI(ToolCallUpdateEvent{
						ID:       delta.ToolCall.ID,
						Name:     delta.ToolCall.Name,
						ArgsJSON: accumulated,
					})
				}
			}
		case "tool_use_end":
			if delta.ToolCall != nil && a.turn != nil && delta.ToolCall.ID != "" {
				callID := delta.ToolCall.ID
				callName := strings.TrimSpace(delta.ToolCall.Name)
				argsJSON := ""
				if call, ok := a.turn.getStreamingToolCall(callID); ok {
					if callName == "" {
						callName = call.Name
					}
					argsJSON = call.ArgsJSON
				}
				a.recordToolTraceToolUseEnd(callID, callName, "", time.Now())
				a.emitToTUI(ToolCallUpdateEvent{
					ID:                callID,
					Name:              callName,
					ArgsJSON:          argsJSON,
					ArgsStreamingDone: true,
				})
			}
		case "rate_limits":
			if delta.RateLimit != nil {
				a.updateRateLimitSnapshot(delta.RateLimit)
			}
		case "key_switched":
			a.clearCurrentRateLimitSnapshot()
			pendingKeySwitch = true
			a.emitToTUI(KeyPoolChangedEvent{})
		case "key_deactivated":
			var ident string
			switch {
			case delta.Email != "" && delta.AccountID != "":
				ident = fmt.Sprintf("%s (%s)", delta.Email, delta.AccountID)
			case delta.Email != "":
				ident = delta.Email
			case delta.AccountID != "":
				ident = delta.AccountID
			default:
				ident = "unknown"
			}
			a.emitToTUI(ToastEvent{Message: fmt.Sprintf("Account deactivated: %s", ident), Level: "error"})
			a.emitToTUI(KeyPoolChangedEvent{})
		case "key_confirmed":
			// First visible token received on the current key: update key availability now.
			a.emitToTUI(KeyPoolChangedEvent{})
			confirmedRef := ""
			confirmedReason := ""
			if delta.Status != nil {
				confirmedRef = delta.Status.ModelRef
				confirmedReason = delta.Status.Reason
			}
			// Ensure the sidebar reflects the model that actually produced the first visible token.
			updateRunningModelRef(confirmedRef)
			// Confirmed toasts must be keyed off the model that actually emitted output.
			emitConfirmedSwitchToast(confirmedRef, confirmedReason)
		case "error":
			log.Warnf("LLM stream error delta text=%v instance=%v", delta.Text, a.instanceID)
		case "rollback":
			// Provider requested rollback of the currently streamed assistant
			// output (e.g. incremental chain invalid). Drop speculative tool cards
			// and ask TUI to remove in-flight assistant/thinking blocks.
			if a.turn != nil {
				a.turn.drainPartialText() // discard rolled-back text
				a.discardSpeculativeStreamToolsAndClearToolTrace(a.turn)
			}
			reason := ""
			if delta.Rollback != nil {
				reason = delta.Rollback.Reason
			}
			a.emitToTUI(StreamRollbackEvent{Reason: reason, AgentID: ""})
		}
	}

	// Hook: on_before_llm_call (before LLM call).
	hookResult, hookErr := a.fireHook(ctx, hook.OnBeforeLLMCall, a.currentTurnID(), map[string]any{
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
	a.recordToolTraceCallLLMReturned(a.turn, completeStreamReturnedAt)
	if err == nil && a.turn != nil {
		a.recordTurnStreamingToolUseEnd(a.turn, time.Now())
	}
	// Final flush: emit any text accumulated in the last batch window.
	// This is the critical path for burst-delivery proxies that send all
	// chunks within a few milliseconds — without this flush the last
	// portion of the response would be dropped from the TUI display even
	// though it is already persisted to the JSONL log.
	flushTextDelta()
	a.emitToTUI(RequestProgressEvent{AgentID: a.instanceID, Bytes: requestProgressBytes, Events: requestProgressEvents, Done: true})
	if err != nil {
		// Skip fallback exhausted toast for context cancellation (user CancelCurrentTurn).
		// All cancel-path errors use %w wrapping around ctx.Err(), so errors.Is
		// reliably detects user-initiated cancellation regardless of retry state.
		if errors.Is(err, context.Canceled) {
			return nil, fmt.Errorf("LLM stream failed: %w", err)
		}
		callStatus := llmClient.LastCallStatus()
		if callStatus.FallbackTriggered && callStatus.FallbackExhausted {
			a.emitToTUI(ToastEvent{
				Message: "Fallback chain exhausted",
				Level:   "error",
			})
		}
		// Per plan §4: if oversize + compaction running, suspend and wait for
		// compaction to apply, then retry instead of surfacing an immediate error.
		if llm.IsContextLengthExceeded(err) && a.IsCompactionRunning() {
			log.Infof("LLM context length exceeded while compaction running; suspending LLM call error=%v", err)
			return nil, &contextLengthExceededPendingCompactionError{inner: err}
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
	if callStatus.RunningContextLimit > 0 {
		a.ctxMgr.SetMaxTokens(callStatus.RunningContextLimit)
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
	if a.ctxMgr.ShouldAutoCompact() {
		a.autoCompactRequested.Store(true)
	}

	a.recordUsage("main", "main", a.currentAgentName(), "chat", selectedRef, callStatus.RunningModelRef, a.currentTurnID(), resp.Usage)

	// Hook: on_after_llm_call (after LLM call).
	inputTok, outputTok := 0, 0
	if resp.Usage != nil {
		inputTok = resp.Usage.InputTokens
		outputTok = resp.Usage.OutputTokens
	}
	turnID := a.currentTurnID()
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
			hookResult, hookErr := a.fireHook(context.Background(), hook.OnAfterLLMCall, turnID, hookCtxData)
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
			if hookResult, hookErr := a.fireHook(context.Background(), hook.OnAfterLLMCall, turnID, hookCtxData); hookErr != nil {
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
	a.llmMu.RLock()
	client := a.llmClient
	a.llmMu.RUnlock()
	if client == nil {
		return nil
	}
	return client.ThinkingToolcallCompat()
}
