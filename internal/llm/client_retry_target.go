package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/executiongate"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/modelcompat"
)

type streamTargetAttemptResult struct {
	continuationExhausted bool
	resp                  *message.Response
	lastErr               error
	lastErrProvider       *ProviderConfig
	pendingRoundWait      roundCoolingWait
	hadRequestAttempt     bool
	roundHadUsableReply   bool
	skipProvider          bool
	// pendingRollbackReason is non-empty when partial streamed output is still
	// rendered and its rollback is deferred until the retry regenerates
	// content or the attempt chain ends; the value is the rollback reason.
	pendingRollbackReason string
}

func (r *streamTargetAttemptResult) setLastErr(provider *ProviderConfig, err error) {
	r.lastErr = err
	if err == nil {
		r.lastErrProvider = nil
		return
	}
	r.lastErrProvider = provider
}

func (c *Client) completeStreamTarget(
	ctx context.Context,
	t streamRetryTarget,
	round int,
	messages []message.Message,
	tools []message.ToolDefinition,
	mcpDeclarations []message.ToolDefinition,
	cb StreamCallback,
	fallbackEnabled bool,
	fallbackModels []FallbackModel,
	currentRoundWait roundCoolingWait,
	hasNextTarget bool,
	status *CallStatus,
	systemPrompt string,
	outputCapSetting int,
	lastInputTokens int,
	abortIfCancelled func() error,
	oversizeSeen *oversizeRegistry,
	pendingRollbackIn string,
	prepareAttempt func(context.Context, RequestAttempt) (context.Context, error),
) (result streamTargetAttemptResult, finalLastInputTokens int, outErr error) {
	result = streamTargetAttemptResult{}
	// pendingRollback carries the rollback reason while preserved partial
	// output from an earlier attempt is still on screen. It stays armed until
	// a tracker actually emits the deferred rollback (or an immediate
	// rollback clears the card); every exit reports it back so the caller can
	// arm the next attempt or discard the preserved text on terminal failure.
	pendingRollback := pendingRollbackIn
	defer func() { result.pendingRollbackReason = pendingRollback }()
	if t.isFallback {
		log.Infof("trying fallback model in retry rotation provider=%v model=%v attempt=%v", t.provider.Name(), t.modelID, round+1)
		if cb != nil {
			reason := ""
			if status != nil {
				reason = status.FallbackReason
			}
			if err := abortIfCancelled(); err != nil {
				return result, lastInputTokens, err
			}
			detail := fmt.Sprintf("fallback: %s", t.modelID)
			if reason != "" {
				detail = fmt.Sprintf("fallback: %s (%s)", t.modelID, reason)
			}
			emitStreamStatusDelta(cb, message.StatusDelta{
				Type:     message.StatusDeltaRetrying,
				Detail:   detail,
				ModelRef: t.displayRef(),
				Reason:   reason,
			})
		}
		if status != nil {
			status.FallbackTriggered = true
		}
	}

	keyCount := t.provider.KeyCount()
	if keyCount == 0 {
		keyCount = 1
	}
	targetMessages := message.ProjectToolDiscoveryHistory(messages)
	poolTarget := FallbackModel{
		ProviderConfig: t.provider,
		ProviderImpl:   t.impl,
		ModelID:        t.modelID,
		MaxTokens:      t.maxTokens,
		ContextLimit:   t.contextLimit,
		Variant:        t.variant,
	}
	replayTurnMark := lastUserMessageIndex(messages)
	replayLevel := c.replayCompatLevelFor(t.provider.Name(), t.modelID, t.variant, replayTurnMark)
	replayLevel = max(replayLevel, minimumReplayLevelForTarget(messages, poolTarget))
	if t.tuning.ReplayCompat != nil && *t.tuning.ReplayCompat > replayLevel {
		replayLevel = min(*t.tuning.ReplayCompat, modelcompat.ReplayCompatStrict)
	}
	var normalizeReport modelcompat.NormalizeReport
	targetMessages, normalizeReport = normalizeMessagesForPoolTargetWithOptions(targetMessages, poolTarget, t.tuning, replayLevel)
	logNormalizeReport(t.provider.Name(), t.modelID, reasoningReplayPolicy(t.provider, t.modelID), replayLevel, len(messages), len(targetMessages), normalizeReport)
	// A target that does not accept the dynamic MCP declaration shape still
	// has to receive a legal standard request: fold the declarations into the
	// top-level tools array for this target only. The selected target decides
	// the mount shape; every other pool member sees the same tool set in the
	// shape it accepts, so mixed pools do not force a top-level rebuild.
	inlineMCPDeclarations := !targetAcceptsMCPDeclarations(poolTarget)
	if inlineMCPDeclarations {
		targetMessages, tools = inlineMCPToolDeclarations(targetMessages, tools, mcpDeclarations)
	}
	requestTuning := replayCompatibleRequestTuning(t.tuning, targetMessages, poolTarget)
	if requestTuning.DisableReasoning && !t.tuning.DisableReasoning {
		log.Infof("disabling reasoning for replay-incompatible request provider=%v model=%v replay_level=%v", t.provider.Name(), t.modelID, replayLevel)
	}
	effectiveMaxTokens := t.maxTokens
	inputFits := true
	inputEstimate := lastInputTokens
	modelCfg, hasModelCfg := t.provider.GetModel(t.modelID)
	if hasModelCfg {
		effectiveMaxTokens, inputEstimate, inputFits = clampEffectiveMaxTokens(
			modelCfg,
			effectiveMaxTokens,
			outputCapSetting,
			requestTuning,
			systemPrompt,
			targetMessages,
			tools,
			lastInputTokens,
		)
	}
	if t.provider.Type() == config.ProviderTypeGenerateContent {
		effectiveMaxTokens = constrainGenerateContentOutputBudget(effectiveMaxTokens, t.provider.GenerateContentCompat(t.modelID))
	}
	if !inputFits {
		// The estimated request input already fills the context window, so no
		// positive output budget is left: the provider would reject the request
		// or answer with a one-token reply. Refuse it locally and report the
		// target as oversize so the pool-exhaustion path starts oversize-driven
		// compaction (or surfaces the compaction-unavailable error) instead of
		// spending a doomed round trip on the wire.
		oversizeErr := &ContextLengthExceededError{
			ProviderMessage: fmt.Sprintf("estimated request input %d tokens exceeds the %d-token context window", inputEstimate, modelCfg.Limit.Context),
		}
		if status != nil && !t.isFallback && status.FallbackReason == "" {
			status.FallbackReason = classifyFallbackReason(oversizeErr)
		}
		result.setLastErr(t.provider, oversizeErr)
		// Counted as an attempted target: this candidate could have served the
		// request and refused it for size. No wire request was sent for it.
		result.hadRequestAttempt = true
		oversizeSeen.mark(t.provider.Name(), t.modelID, t.variant)
		log.Infof("skipping request: estimated input exceeds the model context window provider=%v model=%v variant=%v input_tokens_est=%v context_limit=%v", t.provider.Name(), t.modelID, t.variant, inputEstimate, modelCfg.Limit.Context)
		return result, lastInputTokens, nil
	}
	var apiKey string
	var resp *message.Response
	var err error
	var tracker *visibleStreamTracker
	replayEchoRetried := false
	ambiguousReplayRetried := false
	modelDone := false
	for keyAttempt := 0; keyAttempt < keyCount; keyAttempt++ {
		if err := abortIfCancelled(); err != nil {
			return result, lastInputTokens, err
		}
		var keySwitched bool
		apiKey, keySwitched, err = t.provider.SelectKeyWithContext(ctx)
		if err := abortIfCancelled(); err != nil {
			return result, lastInputTokens, err
		}
		if keySwitched {
			if cb != nil {
				// Carry the attempt target: the rotation invalidates that
				// provider's key-scoped snapshots, not the provider the sidebar
				// still shows while this attempt has not produced output yet.
				cb(message.StreamDelta{Type: message.StreamDeltaKeySwitched, ModelRef: t.displayRef()})
			}
			t.provider.WakeCodexRateLimitPolling()
		}
		if err != nil {
			if _, ok := errors.AsType[*NoUsableKeysError](err); ok {
				result.setLastErr(t.provider, err)
				break
			}
			if cooling, ok := errors.AsType[*AllKeysCoolingError](err); ok {
				waitCap := coolingWaitCap(fallbackEnabled, fallbackModels)
				result.setLastErr(t.provider, err)
				result.pendingRoundWait = mergeCoolingWindow(result.pendingRoundWait, cooling.RetryAfter, waitCap)
				if hasNextTarget {
					log.Infof("all API keys cooling; trying next model provider=%v model=%v", t.provider.Name(), t.modelID)
				} else {
					wait := mergeCoolingWindow(currentRoundWait, cooling.RetryAfter, waitCap)
					log.Infof("all API keys cooling; waiting before retry provider=%v model=%v attempt=%v sleep=%v recovery_in=%v", t.provider.Name(), t.modelID, round+1, wait.sleep, wait.recovery)
					if cb != nil {
						if err := abortIfCancelled(); err != nil {
							return result, lastInputTokens, err
						}
						// Detail and Deadline describe when a request can
						// really go out, not when this round wakes up to
						// re-probe the pool.
						emitStreamStatusDelta(cb, message.StatusDelta{
							Type:     message.StatusDeltaCooling,
							Detail:   wait.recovery.Round(time.Second).String(),
							Deadline: time.Now().Add(wait.recovery),
						})
					}
					reportCoolingCause(cb, cooling.cause)
				}
			} else {
				result.setLastErr(t.provider, err)
			}
			break
		}

		if modelDone {
			break
		}

		log.Debugf("LLM request provider=%v model=%v key_id=%v max_tokens=%v", t.provider.Name(), t.modelID, keyLogID(apiKey), effectiveMaxTokens)
		modelRef := t.displayRef()
		attemptReason := ""
		if t.isFallback && status != nil {
			attemptReason = status.FallbackReason
		}
		tracker = newStreamAttemptTracker(cb, t, apiKey, modelRef, attemptReason, keyAttempt, keyCount)
		tracker.pendingRollback = pendingRollback

		hadEarlierAttempt := result.hadRequestAttempt
		result.hadRequestAttempt = true
		attemptStartedAt := time.Now()
		attemptCtx := ctx
		if prepareAttempt != nil {
			inputBudget := t.inputLimit
			if hasModelCfg {
				inputBudget = modelCfg.Limit.EffectiveInputBudget(effectiveMaxTokens, DefaultOutputTokenMax)
			}
			attemptCtx, err = prepareAttempt(ctx, RequestAttempt{Model: poolTarget, Messages: targetMessages, Tools: tools, SystemPrompt: systemPrompt, InputBudget: inputBudget})
			if err != nil {
				if _, budgetError := errors.AsType[*RequestInputBudgetError](err); budgetError {
					result.setLastErr(t.provider, err)
					oversizeSeen.mark(t.provider.Name(), t.modelID, t.variant)
					return result, lastInputTokens, nil
				}
				return result, lastInputTokens, err
			}
			if attemptCtx == nil {
				return result, lastInputTokens, fmt.Errorf("prepare request attempt returned nil context")
			}
		}
		if observer, _ := attemptCtx.Value(diagnosticsContextKey{}).(*RequestDiagnostics); observer != nil {
			attemptCtx = context.WithValue(attemptCtx, diagnosticTargetContextKey{}, modelRef)
		}
		attemptCtx = context.WithValue(attemptCtx, continuationTargetKey{}, continuationTarget{ref: modelRef, tier: t.serviceTier})
		resp, err = completeWithNativeTools(
			attemptCtx,
			t.impl,
			t.provider,
			apiKey,
			t.modelID,
			systemPrompt,
			targetMessages,
			tools,
			effectiveMaxTokens,
			requestTuning,
			t.serviceTier,
			tracker.Callback,
		)
		if executiongate.IsPaused(err) {
			result.resp = resp
			return result, lastInputTokens, err
		}
		if _, redirected := errors.AsType[*RequestRedirectError](err); redirected {
			return result, lastInputTokens, err
		}
		if continuationErr, exhausted := errors.AsType[*GeminiContinuationError](err); exhausted {
			if ctx.Err() != nil {
				return result, lastInputTokens, ctx.Err()
			}
			if apiErr, ok := errors.AsType[*APIError](continuationErr.Cause); ok && (apiErr.StatusCode == 401 || apiErr.StatusCode == 403 || apiErr.StatusCode == 429) {
				emitKeyCooldownDeltas(cb, markKeyCooldown(ctx, t.provider, apiKey, t.modelID, continuationErr.Cause))
			}
			result.continuationExhausted = true
			result.setLastErr(t.provider, err)
			if status != nil && status.FallbackReason == "" {
				status.FallbackReason = "native_continuation_failed"
			}
			emitRetryErrorForKey(cb, err, t.provider, t.modelID, apiKey)
			return result, lastInputTokens, nil
		}
		if tracker.rollbackFired {
			// The preserved partial left the screen when this attempt's first
			// visible delta arrived; the card now shows this attempt's output.
			pendingRollback = ""
		}
		if _, terminal := errors.AsType[*NativeToolError](err); terminal {
			return result, lastInputTokens, err
		}
		if resp == nil || resp.NativeTools == nil {
			normalizeResponseUsage(t.provider, resp)
		}
		if requestTuning.HostedTool != nil && resp != nil && resp.Hosted != nil && resp.Hosted.RequiresApproval {
			// Approval is an execution boundary even if its stream ends early.
			// Let the hosted backend surface it; never retry or switch credentials.
			updateSuccessfulCallStatus(status, t)
			result.resp = resp
			return result, lastInputTokens, nil
		}
		if _, local := errors.AsType[*HostedAdmissionError](err); local {
			result.hadRequestAttempt = hadEarlierAttempt
			result.setLastErr(t.provider, err)
			return result, lastInputTokens, nil
		}
		if ht := requestTuning.HostedTool; ht != nil && !ht.RetrySafe && ctx.Err() == nil && ((err != nil && !hostedRequestRejected(err)) || (resp != nil && resp.StopReason == "interrupted")) {
			if err == nil {
				err = fmt.Errorf("hosted stream interrupted")
			}
			updateSuccessfulCallStatus(status, t)
			return result, lastInputTokens, &HostedOutcomeUnknownError{Cause: err, Response: resp}
		}
		if err == nil {
			if resp != nil && resp.NativeTools == nil && modelcompat.IsReplayEvidenceEcho(resp.Content, targetMessages) {
				echoErr := &ReplayEvidenceEchoError{}
				if tracker.EmitRollback(echoErr.Error()) {
					pendingRollback = ""
				}
				log.Warnf("model echoed request-only replay evidence provider=%v model=%v key_id=%v retry=%v", t.provider.Name(), t.modelID, keyLogID(apiKey), !replayEchoRetried)
				if !replayEchoRetried {
					replayEchoRetried = true
					targetMessages = reinforceReplayContinuation(targetMessages)
					requestTuning = replayCompatibleRequestTuning(t.tuning, targetMessages, poolTarget)
					keyAttempt--
					continue
				}
				result.setLastErr(t.provider, echoErr)
				if status != nil && !t.isFallback && status.FallbackReason == "" {
					status.FallbackReason = "replay_evidence_echo"
				}
				emitRetryErrorForKey(cb, echoErr, t.provider, t.modelID, apiKey)
				resp = nil
				err = echoErr
				modelDone = true
				break
			}
			// A response discarded below as StopReason "interrupted" is a
			// failed attempt: counting it as a usable reply would reset the
			// retry counter every round and defeat an explicit retry cap.
			// The same reasoning applies to the key's health: marking the key
			// successful here would clear CooldownCount, so the cooldown the
			// interrupted branch applies below would restart at its base wait
			// every round and never grow for a gateway that keeps truncating.
			if resp.StopReason != "interrupted" {
				if responseHasUsableOutput(resp) {
					result.roundHadUsableReply = true
				}
				t.provider.MarkKeySuccess(apiKey)
				// The reply ran to completion, so the credential is not the
				// one truncating: drop the transport backoff. First visible
				// output (where the stream tracker marks the key successful)
				// is deliberately not enough — that is exactly what a gateway
				// which truncates every reply produces.
				t.provider.ClearTransportCooldown(apiKey)
			}
			t.provider.WakeCodexRateLimitPolling()
			inputTok, outputTok := 0, 0
			if resp.Usage != nil {
				inputTok = resp.Usage.InputTokens
				outputTok = resp.Usage.OutputTokens
				lastInputTokens, _ = ctxmgr.ContextPromptTokens(*resp.Usage)
				c.setLastInputTokens(lastInputTokens)
			}
			log.Debugf("LLM request completed provider=%v model=%v input_tokens=%v output_tokens=%v stop_reason=%v", t.provider.Name(), t.modelID, inputTok, outputTok, resp.StopReason)
			updateSuccessfulCallStatus(status, t)
			if status != nil {
				status.RunningAttemptAt = attemptStartedAt
			}
			modelDone = true
			break
		}

		if err := abortIfCancelled(); err != nil {
			return result, lastInputTokens, err
		}

		result.setLastErr(t.provider, err)
		visibleStarted := tracker.visible
		// preservablePartial records that this attempt streamed resumable
		// assistant body text without a tool card, so a later resumable failure
		// can escalate to the caller instead of silently retrying the whole
		// request.
		preservablePartial := false
		// A stream interrupted after visible output keeps that output on
		// screen: the preserved partial leaves the screen only when the retry
		// regenerates output, so the reply never mixes two attempts. Attempts
		// that already streamed a tool call, and failures that carry no usable
		// output (bad request shape, context length, replay rejections), still
		// roll back immediately.
		if !isInterruptedStreamError(err) || !tracker.MarkInterruptedVisibleOutput() {
			if tracker.EmitRollback(err.Error()) {
				pendingRollback = ""
			}
		} else {
			// Only body text is resumable. A thinking-only attempt is visible
			// but leaves the caller with no partial reply to continue, so it
			// keeps the old silent retry — escalating it would let the turn
			// fail instead of recovering, which is the opposite of the intent.
			preservablePartial = tracker.HadTextDelta()
			pendingRollback = err.Error()
		}
		if err := abortIfCancelled(); err != nil {
			return result, lastInputTokens, err
		}
		// Context length is independent of replay compatibility. Handle it before
		// the replay ladder so an oversized request cannot spend extra attempts or
		// permanently downgrade a target merely because it also contains foreign
		// native replay items.
		if IsContextLengthExceeded(err) {
			if status != nil && !t.isFallback && shouldFallback(err) && status.FallbackReason == "" {
				status.FallbackReason = classifyFallbackReason(err)
			}
			result.setLastErr(t.provider, err)
			oversizeSeen.mark(t.provider.Name(), t.modelID, t.variant)
			emitRetryErrorForKey(cb, err, t.provider, t.modelID, apiKey)
			if visibleStarted {
				log.Warnf("context length exceeded; trying next model provider=%v model=%v key_id=%v input_tokens_est=%v context_limit=%v input_limit=%v error=%v", t.provider.Name(), t.modelID, keyLogID(apiKey), estimateRequestInputTokens(systemPrompt, targetMessages, tools), t.contextLimit, t.inputLimit, err)
			}
			modelDone = true
			break
		}

		// Only an explicit replay rejection may persist a degraded compatibility
		// level. A bare HTTP 400 or status-less stream event is ambiguous: retry
		// the unchanged request once, then allow a request-local probe with a
		// distinct portable shape. A successful probe recovers this request but
		// does not claim that replay incompatibility caused the original failure.
		// Server retry advice must enter key rotation before any such probe.
		explicitReplayRejection := isReasoningReplayRejection(err)
		ambiguousReplayRecovery := !visibleStarted && serverDirectedRetryCooldown(t.provider, err) == 0 && isAmbiguousReplayRecoveryCandidate(err, t.provider, normalizeReport)
		if ambiguousReplayRecovery && !ambiguousReplayRetried {
			ambiguousReplayRetried = true
			log.Warnf("ambiguous provider failure with replay-sensitive input; retrying unchanged request before compatibility probe provider=%v model=%v key_id=%v replay_level=%v origin=%v error=%v", t.provider.Name(), t.modelID, keyLogID(apiKey), replayLevel, apiErrorOrigin(err), err)
			keyAttempt--
			continue
		}
		if replayLevel < modelcompat.ReplayCompatStrict && (explicitReplayRejection || ambiguousReplayRecovery) {
			nextLevel, nextMessages, nextReport, ok := nextDistinctReplayRequest(messages, targetMessages, poolTarget, t.tuning, replayLevel, inlineMCPDeclarations, mcpDeclarations)
			if ok {
				replayLevel = nextLevel
				if explicitReplayRejection {
					c.setReplayCompatLevelFor(t.provider.Name(), t.modelID, t.variant, replayTurnMark, replayLevel)
					log.Warnf("target explicitly rejected replayed trajectory; degrading replay compatibility provider=%v model=%v key_id=%v level=%v error=%v", t.provider.Name(), t.modelID, keyLogID(apiKey), replayLevel, err)
				} else {
					log.Warnf("probing request-local replay compatibility after repeated ambiguous failure provider=%v model=%v key_id=%v level=%v origin=%v error=%v", t.provider.Name(), t.modelID, keyLogID(apiKey), replayLevel, apiErrorOrigin(err), err)
				}
				targetMessages = nextMessages
				if inlineMCPDeclarations {
					// The rebuilt messages carry the declaration messages again,
					// so the probe must re-fold them or it would ship both the
					// declaration messages and the already-merged top-level tools.
					targetMessages, tools = inlineMCPToolDeclarations(targetMessages, tools, mcpDeclarations)
				}
				normalizeReport = nextReport
				requestTuning = replayCompatibleRequestTuning(t.tuning, targetMessages, poolTarget)
				logNormalizeReport(t.provider.Name(), t.modelID, reasoningReplayPolicy(t.provider, t.modelID), replayLevel, len(messages), len(targetMessages), nextReport)
				keyAttempt--
				continue
			}
		}
		if !visibleStarted {
			fallbackEligible := shouldFallback(err)
			cooldownResult := markKeyCooldown(ctx, t.provider, apiKey, t.modelID, result.lastErr)
			if err := abortIfCancelled(); err != nil {
				return result, lastInputTokens, err
			}
			emitKeyCooldownDeltas(cb, cooldownResult)
			if cb != nil {
				// The overlay reached the user through this callback; a later
				// request must not replay it from the pending queue.
				t.provider.markKeyCooldownCauseDeltasEmitted(result.lastErr)
			}
			if c.isTerminalAPIStatusError(err) && !cooldownResult.oauthRefreshed {
				log.Errorf("terminal API error, giving up provider=%v model=%v key_id=%v error=%v", t.provider.Name(), t.modelID, keyLogID(apiKey), err)
				markTerminalCooldownCauseReported(cb, t.provider, err)
				return result, lastInputTokens, err
			}
			cooldownApplied := cooldownResult.cooldownApplied
			keyForRotationCooldown := apiKey
			if cooldownResult.refreshedKey != "" {
				keyForRotationCooldown = cooldownResult.refreshedKey
			}
			if status != nil && !t.isFallback && shouldFallback(err) && status.FallbackReason == "" {
				status.FallbackReason = classifyFallbackReason(err)
			}
			retriable := isRetriable(err)
			apiErrPtr, ok := errors.AsType[*APIError](err)
			if ok && apiErrPtr != nil {
				if apiErrPtr.StatusCode == 401 || apiErrPtr.StatusCode == 403 {
					retriable = true
				}
				if apiErrPtr.StatusCode == 400 {
					// Check for protocol/model incompatibility errors first.
					// These should trigger immediate fallback, not key rotation.
					if hasTerminalNonRetriable400Signal(apiErrPtr) {
						retriable = false
						if fallbackEligible && fallbackEnabled && len(fallbackModels) > 0 {
							log.Warnf("model incompatible 400, trying fallback provider=%v model=%v key_id=%v error=%v", t.provider.Name(), t.modelID, keyLogID(apiKey), err)
							emitRetryErrorForKey(cb, err, t.provider, t.modelID, apiKey)
							modelDone = true
							break
						}
						markTerminalCooldownCauseReported(cb, t.provider, err)
						return result, lastInputTokens, err
					}
					// For compatible gateways, other 400s are retriable (may be overload).
					retriable = !providerTrustsHTTP400(t.provider)
				}
			}
			if !retriable && isTimeoutLikeError(err) {
				t.provider.MarkRecovering(apiKey)
				log.Warnf("invisible timeout before visible output; marking current key recovering and skipping remaining provider targets provider=%v model=%v key_id=%v error=%v", t.provider.Name(), t.modelID, keyLogID(apiKey), err)
				result.skipProvider = true
			}

			if !retriable {
				log.Warnf("non-key-retriable LLM error provider=%v model=%v key_id=%v error=%v", t.provider.Name(), t.modelID, keyLogID(apiKey), err)
				// Use the narrower request/parameter check here: the broader terminal
				// 400 handling above already routes model/protocol incompatibility to
				// fallback when available, while official API 400s still stop directly.
				if apiErrPtr != nil && apiErrPtr.StatusCode == 400 && (providerTrustsHTTP400(t.provider) || isRequestOrParamError(apiErrPtr)) {
					return result, lastInputTokens, err
				}
				if fallbackEligible && fallbackEnabled && len(fallbackModels) > 0 {
					emitRetryErrorForKey(cb, err, t.provider, t.modelID, apiKey)
					modelDone = true
					break
				}
				emitRetryErrorForKey(cb, err, t.provider, t.modelID, apiKey)
				modelDone = true
				break
			}

			if !cooldownApplied && keyForRotationCooldown != "" {
				t.provider.MarkRecovering(keyForRotationCooldown)
			}

			log.Warnf("retriable LLM error, trying next key provider=%v model=%v key_id=%v error=%v", t.provider.Name(), t.modelID, keyLogID(apiKey), err)
			emitRetryErrorForKey(cb, err, t.provider, t.modelID, apiKey)
			if cb != nil && keyAttempt+1 < keyCount {
				if err := abortIfCancelled(); err != nil {
					return result, lastInputTokens, err
				}
				emitStreamStatus(cb, message.StatusDeltaRetryingKey, fmt.Sprintf("%d/%d", keyAttempt+2, keyCount))
			}
			continue
		}

		if isAuthAPIStatusError(err) || isRateLimitAPIStatusError(err) || serverDirectedRetryCooldown(t.provider, err) > 0 {
			cooldownResult := markKeyCooldown(ctx, t.provider, apiKey, t.modelID, result.lastErr)
			if err := abortIfCancelled(); err != nil {
				return result, lastInputTokens, err
			}
			emitKeyCooldownDeltas(cb, cooldownResult)
			if cb != nil {
				// The overlay reached the user through this callback; a later
				// request must not replay it from the pending queue.
				t.provider.markKeyCooldownCauseDeltasEmitted(result.lastErr)
			}
			if c.isTerminalAPIStatusError(err) && !cooldownResult.oauthRefreshed {
				log.Errorf("terminal API error after visible output, giving up provider=%v model=%v key_id=%v error=%v", t.provider.Name(), t.modelID, keyLogID(apiKey), err)
				markTerminalCooldownCauseReported(cb, t.provider, err)
				return result, lastInputTokens, err
			}
			keyForRotationCooldown := apiKey
			if cooldownResult.refreshedKey != "" {
				keyForRotationCooldown = cooldownResult.refreshedKey
			}
			if status != nil && !t.isFallback && shouldFallback(err) && status.FallbackReason == "" {
				status.FallbackReason = classifyFallbackReason(err)
			}
			if !cooldownResult.cooldownApplied && keyForRotationCooldown != "" {
				t.provider.MarkRecovering(keyForRotationCooldown)
			}
			log.Warnf("key-scoped API error after visible output, trying next key provider=%v model=%v key_id=%v error=%v", t.provider.Name(), t.modelID, keyLogID(apiKey), err)
			emitRetryErrorForKey(cb, err, t.provider, t.modelID, apiKey)
			if cb != nil && keyAttempt+1 < keyCount {
				if err := abortIfCancelled(); err != nil {
					return result, lastInputTokens, err
				}
				emitStreamStatus(cb, message.StatusDeltaRetryingKey, fmt.Sprintf("%d/%d", keyAttempt+2, keyCount))
			}
			continue
		}

		if status != nil && !t.isFallback && shouldFallback(err) && status.FallbackReason == "" {
			status.FallbackReason = classifyFallbackReason(err)
		}
		// This attempt streamed resumable body text without a tool card and
		// the failure is a stream interruption. preservablePartial already
		// implies both, so it is the only gate needed here. The partial text
		// stays on screen: cool the key so the caller's restart (a fresh
		// request through the normal key/fallback rotation) skips it or waits
		// out the cooldown instead of immediately reusing the same key that
		// just truncated — MarkCooldown grows the wait for the key's
		// consecutive failures, throttling the restart cadence without a round
		// cap — and escalate the error so the caller can save the partial
		// reply and resume it with a continuation prompt. Silently retrying
		// the same key here would regenerate the whole reply and discard
		// every attempt's partial output. Keep pendingRollback empty so the
		// retry-layer deferred rollback does not wipe the preserved text when
		// this error unwinds.
		if preservablePartial {
			t.provider.MarkTransportCooldown(apiKey, preservedInterruptionCooldownBase, preservedInterruptionCooldownMax)
			log.Warnf("interrupted visible stream with preserved partial text; cooling credential and escalating for continuation provider=%v model=%v key_id=%v error=%v", t.provider.Name(), t.modelID, keyLogID(apiKey), err)
			// Record the cause like an ordinary silent retry so the failure is
			// visible in the sidebar: the continuation itself only shows a
			// resumed reply, which on a prefill-capable pool renders nothing
			// at all.
			emitRetryErrorForKey(cb, err, t.provider, t.modelID, apiKey)
			pendingRollback = ""
			return result, lastInputTokens, err
		}
		log.Warnf("stream interrupted after visible output; retrying current key provider=%v model=%v key_id=%v error=%v", t.provider.Name(), t.modelID, keyLogID(apiKey), err)
		emitRetryErrorForKey(cb, err, t.provider, t.modelID, apiKey)
		if cb != nil {
			if err := abortIfCancelled(); err != nil {
				return result, lastInputTokens, err
			}
			emitStreamStatus(cb, message.StatusDeltaRetrying, "same key")
		}
	}
	if !modelDone {
		modelDone = true
	}
	result.skipProvider = result.skipProvider || skipRemainingModelsOnProvider(result.lastErr)
	if resp != nil && err == nil {
		if !responseHasUsableOutput(resp) {
			emptyErr := error(&EmptyResponseError{})
			if resp.StopReason == "length" || resp.StopReason == "max_tokens" {
				emptyErr = &EmptyTruncationError{}
			}
			log.Warnf("model returned empty response, trying next key provider=%v model=%v key_id=%v stop_reason=%v", t.provider.Name(), t.modelID, keyLogID(apiKey), resp.StopReason)
			result.setLastErr(t.provider, emptyErr)
			t.provider.MarkRecovering(apiKey)
			if tracker.EmitRollback(emptyErr.Error()) {
				pendingRollback = ""
			}
			emitRetryErrorForKey(cb, emptyErr, t.provider, t.modelID, apiKey)
			if cb != nil {
				emitStreamStatus(cb, message.StatusDeltaRetryingKey, "next")
			}
			return result, lastInputTokens, nil
		}
		if resp.StopReason == "interrupted" {
			// An interrupted response is a failed attempt, not a success.
			// When it already streamed assistant text (no tool card), the
			// partial output stays on screen and the failure is escalated to
			// the caller so it can save the partial reply and resume it with a
			// continuation prompt — the text is never discarded and never
			// silently regenerated. A tool-call preview cannot be preserved (a
			// retry would emit a second tool card for the same call), so those
			// fall through to the silent retry loop, rolling the preview back
			// first.
			interruptedErr := &InterruptedResponseError{StopReason: "interrupted"}
			result.setLastErr(t.provider, interruptedErr)
			if !tracker.MarkInterruptedVisibleOutput() || strings.TrimSpace(resp.Content) == "" {
				if tracker.EmitRollback(interruptedErr.Error()) {
					pendingRollback = ""
				}
				// Tool-call previews (would duplicate the card on retry) and
				// interruptions without streamed text (nothing worth resuming)
				// keep the old silent-retry behavior.
				return result, lastInputTokens, nil
			}
			// Preservable partial text is on screen: cool the key so the
			// caller's restart (a fresh request through the normal key/fallback
			// rotation) skips it or waits out the cooldown instead of
			// immediately reusing the same key that just truncated —
			// MarkCooldown grows the wait for the key's consecutive failures,
			// throttling the restart cadence without a round cap — and keep
			// pendingRollback empty so the retry-layer deferred rollback does
			// not wipe the preserved text when this error unwinds.
			t.provider.MarkTransportCooldown(apiKey, preservedInterruptionCooldownBase, preservedInterruptionCooldownMax)
			log.Warnf("interrupted response with preserved partial text; cooling credential and escalating for continuation provider=%v model=%v key_id=%v content_len=%v", t.provider.Name(), t.modelID, keyLogID(apiKey), len(resp.Content))
			emitRetryErrorForKey(cb, interruptedErr, t.provider, t.modelID, apiKey)
			pendingRollback = ""
			return result, lastInputTokens, interruptedErr
		}
		result.resp = resp
	}
	return result, lastInputTokens, nil
}
