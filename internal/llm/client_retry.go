package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

type visibleStreamTracker struct {
	inner          StreamCallback
	visible        bool
	onVisibleStart func() // called each time a visible streaming attempt begins; may be nil
	providerModel  string
	keySuffix      string
	keyAttempt     int
	keyCount       int
}

func (t *visibleStreamTracker) Callback(delta message.StreamDelta) {
	if t == nil {
		return
	}
	switch delta.Type {
	case "text", "thinking", "tool_use_start", "tool_use_delta", "tool_use_end":
		if !t.visible {
			log.Debugf("LLM first visible stream delta delta_type=%v model=%v key_suffix=%v key_attempt=%v key_total=%v", delta.Type, t.providerModel, t.keySuffix, t.keyAttempt, t.keyCount)
			t.visible = true
			if t.onVisibleStart != nil {
				t.onVisibleStart()
			}
		}
	case "rollback":
		if t.visible {
			reason := ""
			if delta.Rollback != nil {
				reason = delta.Rollback.Reason
			}
			log.Debugf("LLM visible stream rollback model=%v key_suffix=%v key_attempt=%v key_total=%v reason=%v", t.providerModel, t.keySuffix, t.keyAttempt, t.keyCount, reason)
		}
		t.visible = false
	}
	if t.inner != nil {
		t.inner(delta)
	}
}

func (t *visibleStreamTracker) EmitRollback(reason string) {
	if t == nil || !t.visible || t.inner == nil {
		return
	}
	t.inner(message.StreamDelta{
		Type: "rollback",
		Rollback: &message.RollbackDelta{
			Reason: reason,
		},
	})
	t.visible = false
}

const maxCoolingWait = 1 * time.Minute

func isAllKeysCoolingError(err error) bool {
	_, ok := errors.AsType[*AllKeysCoolingError](err)
	return ok
}

func shouldContinueRetry(retryCount, maxAttempts int, lastErr error) bool {
	return shouldContinueRetryMode(retryCount, maxAttempts, lastErr, false)
}

func shouldContinueRetryMode(retryCount, maxAttempts int, lastErr error, hardCap bool) bool {
	if maxAttempts <= 0 {
		return true
	}
	if retryCount < maxAttempts {
		return true
	}
	if hardCap {
		return false
	}
	return isAllKeysCoolingError(lastErr) || isConcurrentRequestLimit429(lastErr)
}

func clampCoolingWait(wait time.Duration) time.Duration {
	if wait <= 0 {
		return time.Second
	}
	if wait > maxCoolingWait {
		return maxCoolingWait
	}
	return wait
}

func mergeRoundWait(current, candidate time.Duration) time.Duration {
	candidate = clampCoolingWait(candidate)
	if current == 0 || candidate < current {
		return candidate
	}
	return current
}

func mergePendingRoundWait(current, candidate time.Duration) time.Duration {
	if candidate <= 0 {
		return current
	}
	return mergeRoundWait(current, candidate)
}

func roundRetryDelay(backoffDelay, pendingRoundWait time.Duration) time.Duration {
	if backoffDelay <= 0 {
		return pendingRoundWait
	}
	if pendingRoundWait > 0 && pendingRoundWait < backoffDelay {
		return pendingRoundWait
	}
	return backoffDelay
}

func nextRetryCount(current int, roundHadUsableReply bool) int {
	if roundHadUsableReply {
		return 0
	}
	return current + 1
}

func responseHasUsableOutput(resp *message.Response) bool {
	if resp == nil {
		return false
	}
	return strings.TrimSpace(resp.Content) != "" || len(resp.ToolCalls) > 0 || len(resp.ThinkingBlocks) > 0
}

type streamRetryTarget struct {
	provider     *ProviderConfig
	impl         Provider
	modelID      string
	maxTokens    int
	contextLimit int
	inputLimit   int
	tuning       RequestTuning
	variant      string
	isFallback   bool
}

func (c *Client) buildStreamRetryTargets(
	startProvider *ProviderConfig,
	startImpl Provider,
	startModelID string,
	startMaxTokens int,
	startTuning RequestTuning,
	variantForStart string,
	outputCapSetting int,
	fallbackEnabled bool,
	fallbackModels []FallbackModel,
) []streamRetryTarget {
	if c.FastMode() {
		startTuning = fastModeTuning(startTuning)
	}
	targets := []streamRetryTarget{
		{
			provider:     startProvider,
			impl:         startImpl,
			modelID:      startModelID,
			maxTokens:    startMaxTokens,
			contextLimit: c.ContextLimitForModelRef(providerModelRef(startProvider, startModelID)),
			inputLimit:   c.InputLimitForModelRef(providerModelRef(startProvider, startModelID)),
			tuning:       startTuning,
			variant:      variantForStart,
			isFallback:   false,
		},
	}
	if !fallbackEnabled {
		return targets
	}
	for _, fb := range fallbackModels {
		var fbTuning RequestTuning
		fbVariantUsed := ""
		fbContextLimit := fb.ContextLimit
		fbInputLimit := resolveFallbackInputLimit(fb, outputCapSetting)
		if m, ok := fb.ProviderConfig.GetModel(fb.ModelID); ok {
			fbTuning = tuningFromModel(m, fb.ProviderConfig.Preset())
			if fbContextLimit <= 0 {
				fbContextLimit = m.Limit.Context
			}
			if fb.Variant != "" {
				if v, ok := m.Variants[fb.Variant]; ok {
					fbTuning = mergeVariantTuning(fbTuning, v)
					fbVariantUsed = fb.Variant
				}
			}
		}
		if fbInputLimit <= 0 {
			fbInputLimit = fbContextLimit
		}
		if c.FastMode() {
			fbTuning = fastModeTuning(fbTuning)
		}
		targets = append(targets, streamRetryTarget{
			provider:     fb.ProviderConfig,
			impl:         fb.ProviderImpl,
			modelID:      fb.ModelID,
			maxTokens:    fb.MaxTokens,
			contextLimit: fbContextLimit,
			inputLimit:   fbInputLimit,
			tuning:       fbTuning,
			variant:      fbVariantUsed,
			isFallback:   true,
		})
	}
	return targets
}

func (t streamRetryTarget) displayRef() string {
	displayRef := providerModelRef(t.provider, t.modelID)
	if t.variant != "" {
		displayRef += "@" + t.variant
	}
	return displayRef
}

func emitStreamStatus(cb StreamCallback, typ, detail string) {
	if cb == nil {
		return
	}
	cb(message.StreamDelta{
		Type: "status",
		Status: &message.StatusDelta{
			Type:   typ,
			Detail: detail,
		},
	})
}

func emitStreamStatusDelta(cb StreamCallback, status message.StatusDelta) {
	if cb == nil {
		return
	}
	cb(message.StreamDelta{Type: "status", Status: &status})
}

func newStreamAttemptTracker(cb StreamCallback, target streamRetryTarget, apiKey, modelRef, attemptReason string, keyAttempt, keyCount int) *visibleStreamTracker {
	keySuffixValue := keySuffix(apiKey)
	return &visibleStreamTracker{
		inner:         cb,
		providerModel: modelRef,
		keySuffix:     keySuffixValue,
		keyAttempt:    keyAttempt + 1,
		keyCount:      keyCount,
		onVisibleStart: func() {
			target.provider.MarkKeySuccess(apiKey)
			log.Debugf("LLM emitting streaming status after visible output provider=%v model=%v key_suffix=%v key_attempt=%v key_total=%v", target.provider.Name(), modelRef, keySuffixValue, keyAttempt+1, keyCount)
			if cb == nil {
				return
			}
			emitStreamStatus(cb, "streaming", "")
			// key_confirmed must carry the effective model ref so the agent/UI can
			// confirm routing decisions (fallback/key switch) only after the model
			// actually begins emitting visible output.
			cb(message.StreamDelta{
				Type: "key_confirmed",
				Status: &message.StatusDelta{
					ModelRef: modelRef,
					Reason:   attemptReason,
				},
			})
		},
	}
}

func updateSuccessfulCallStatus(status *CallStatus, target streamRetryTarget) {
	if status == nil {
		return
	}
	status.RunningModelRef = target.displayRef()
	status.RunningContextLimit = target.contextLimit
	status.RunningInputLimit = target.inputLimit
}

type streamTargetAttemptResult struct {
	resp                *message.Response
	lastErr             error
	pendingRoundWait    time.Duration
	hadRequestAttempt   bool
	roundHadUsableReply bool
	skipProvider        bool
}

func (c *Client) completeStreamTarget(
	ctx context.Context,
	t streamRetryTarget,
	round int,
	messages []message.Message,
	tools []message.ToolDefinition,
	cb StreamCallback,
	fallbackEnabled bool,
	fallbackModels []FallbackModel,
	currentRoundWait time.Duration,
	hasNextTarget bool,
	status *CallStatus,
	systemPrompt string,
	outputCapSetting int,
	lastInputTokens int,
	abortIfCancelled func() error,
	oversizeSeen *oversizeRegistry,
) (streamTargetAttemptResult, int, error) {
	result := streamTargetAttemptResult{}
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
			emitStreamStatusDelta(cb, message.StatusDelta{
				Type:     "retrying",
				Detail:   fmt.Sprintf("fallback: %s", t.modelID),
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
	targetMessages := messages
	targetMessages, _ = normalizeMessagesForPoolTarget(targetMessages, FallbackModel{
		ProviderConfig: t.provider,
		ProviderImpl:   t.impl,
		ModelID:        t.modelID,
		MaxTokens:      t.maxTokens,
		ContextLimit:   t.contextLimit,
		Variant:        t.variant,
	}, t.tuning)
	effectiveMaxTokens := t.maxTokens
	if m, ok := t.provider.GetModel(t.modelID); ok {
		effectiveMaxTokens = clampEffectiveMaxTokens(
			m,
			effectiveMaxTokens,
			outputCapSetting,
			t.tuning,
			systemPrompt,
			targetMessages,
			tools,
			lastInputTokens,
		)
	}
	var apiKey string
	var resp *message.Response
	var err error
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
				cb(message.StreamDelta{Type: "key_switched"})
			}
			t.provider.WakeCodexRateLimitPolling()
		}
		if err != nil {
			if _, ok := errors.AsType[*NoUsableKeysError](err); ok {
				result.lastErr = err
				break
			}
			if cooling, ok := errors.AsType[*AllKeysCoolingError](err); ok {
				result.lastErr = err
				result.pendingRoundWait = mergeRoundWait(result.pendingRoundWait, cooling.RetryAfter)
				if hasNextTarget {
					log.Infof("all API keys cooling; trying next model provider=%v model=%v", t.provider.Name(), t.modelID)
				} else {
					wait := mergeRoundWait(currentRoundWait, cooling.RetryAfter)
					log.Infof("all API keys cooling; waiting before retry provider=%v model=%v attempt=%v retry_after=%v", t.provider.Name(), t.modelID, round+1, wait)
					if cb != nil {
						if err := abortIfCancelled(); err != nil {
							return result, lastInputTokens, err
						}
						emitStreamStatus(cb, "cooling", wait.Round(time.Second).String())
					}
				}
			} else {
				result.lastErr = err
			}
			break
		}

		if modelDone {
			break
		}

		log.Debugf("LLM request provider=%v model=%v key_suffix=%v max_tokens=%v", t.provider.Name(), t.modelID, keySuffix(apiKey), effectiveMaxTokens)
		modelRef := t.displayRef()
		attemptReason := ""
		if t.isFallback && status != nil {
			attemptReason = status.FallbackReason
		}
		tracker := newStreamAttemptTracker(cb, t, apiKey, modelRef, attemptReason, keyAttempt, keyCount)

		result.hadRequestAttempt = true
		resp, err = t.impl.CompleteStream(
			ctx,
			apiKey,
			t.modelID,
			systemPrompt,
			targetMessages,
			tools,
			effectiveMaxTokens,
			t.tuning,
			tracker.Callback,
		)
		if err == nil {
			if responseHasUsableOutput(resp) {
				result.roundHadUsableReply = true
			}
			t.provider.MarkKeySuccess(apiKey)
			t.provider.WakeCodexRateLimitPolling()
			inputTok, outputTok := 0, 0
			if resp.Usage != nil {
				inputTok = resp.Usage.InputTokens
				outputTok = resp.Usage.OutputTokens
				lastInputTokens = resp.Usage.InputTokens
				c.setLastInputTokens(resp.Usage.InputTokens)
			}
			log.Debugf("LLM request completed provider=%v model=%v input_tokens=%v output_tokens=%v stop_reason=%v", t.provider.Name(), t.modelID, inputTok, outputTok, resp.StopReason)
			updateSuccessfulCallStatus(status, t)
			modelDone = true
			break
		}

		if err := abortIfCancelled(); err != nil {
			return result, lastInputTokens, err
		}

		result.lastErr = err
		visibleStarted := tracker.visible
		tracker.EmitRollback(err.Error())
		if err := abortIfCancelled(); err != nil {
			return result, lastInputTokens, err
		}
		if !visibleStarted {
			fallbackEligible := shouldFallback(err)
			cooldownResult := markKeyCooldown(ctx, t.provider, apiKey, result.lastErr)
			if err := abortIfCancelled(); err != nil {
				return result, lastInputTokens, err
			}
			if (cooldownResult.expiredAccountID != "" || cooldownResult.expiredEmail != "") && cb != nil {
				cb(message.StreamDelta{Type: "key_expired", AccountID: cooldownResult.expiredAccountID, Email: cooldownResult.expiredEmail})
			}
			if (cooldownResult.deactivatedAccountID != "" || cooldownResult.deactivatedEmail != "") && cb != nil {
				cb(message.StreamDelta{Type: "key_deactivated", AccountID: cooldownResult.deactivatedAccountID, Email: cooldownResult.deactivatedEmail})
			}
			if (cooldownResult.invalidatedAccountID != "" || cooldownResult.invalidatedEmail != "") && cb != nil {
				cb(message.StreamDelta{Type: "key_invalidated", AccountID: cooldownResult.invalidatedAccountID, Email: cooldownResult.invalidatedEmail})
			}
			if c.isTerminalAPIStatusError(err) && !cooldownResult.oauthRefreshed {
				log.Errorf("terminal API error, giving up provider=%v model=%v key_suffix=%v error=%v", t.provider.Name(), t.modelID, keySuffix(apiKey), err)
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
			if IsContextLengthExceeded(err) {
				result.lastErr = err
				oversizeSeen.mark(t.provider.Name(), t.modelID, t.variant)
				modelDone = true
				break
			}

			retriable := isRetriable(err)
			var apiErrPtr *APIError
			if errors.As(err, &apiErrPtr) && apiErrPtr != nil && (apiErrPtr.StatusCode == 401 || apiErrPtr.StatusCode == 403) {
				retriable = true
			}
			if !retriable && isTimeoutLikeError(err) {
				log.Warnf("invisible timeout before visible output; skipping remaining provider targets provider=%v model=%v key_suffix=%v error=%v", t.provider.Name(), t.modelID, keySuffix(apiKey), err)
				result.skipProvider = true
			}

			if !retriable {
				log.Errorf("non-retriable LLM error provider=%v model=%v key_suffix=%v error=%v", t.provider.Name(), t.modelID, keySuffix(apiKey), err)
				if fallbackEligible && fallbackEnabled && len(fallbackModels) > 0 {
					modelDone = true
					break
				}
				modelDone = true
				break
			}

			if !cooldownApplied && keyForRotationCooldown != "" {
				t.provider.MarkRecovering(keyForRotationCooldown)
			}

			log.Warnf("retriable LLM error, trying next key provider=%v model=%v key_suffix=%v error=%v", t.provider.Name(), t.modelID, keySuffix(apiKey), err)
			if cb != nil && keyAttempt+1 < keyCount {
				if err := abortIfCancelled(); err != nil {
					return result, lastInputTokens, err
				}
				emitStreamStatus(cb, "retrying_key", fmt.Sprintf("%d/%d", keyAttempt+2, keyCount))
			}
			continue
		}

		if status != nil && !t.isFallback && shouldFallback(err) && status.FallbackReason == "" {
			status.FallbackReason = classifyFallbackReason(err)
		}
		if IsContextLengthExceeded(err) {
			log.Warnf("context length exceeded; trying next model provider=%v model=%v key_suffix=%v input_tokens_est=%v context_limit=%v input_limit=%v error=%v", t.provider.Name(), t.modelID, keySuffix(apiKey), estimateRequestInputTokens(systemPrompt, targetMessages, tools), t.contextLimit, t.inputLimit, err)
			result.lastErr = err
			oversizeSeen.mark(t.provider.Name(), t.modelID, t.variant)
			modelDone = true
			break
		}
		log.Warnf("stream interrupted after visible output; retrying current key provider=%v model=%v key_suffix=%v error=%v", t.provider.Name(), t.modelID, keySuffix(apiKey), err)
		if cb != nil {
			if err := abortIfCancelled(); err != nil {
				return result, lastInputTokens, err
			}
			emitStreamStatus(cb, "retrying", "same key")
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
			log.Warnf("model returned empty response, trying next key provider=%v model=%v key_suffix=%v stop_reason=%v", t.provider.Name(), t.modelID, keySuffix(apiKey), resp.StopReason)
			result.lastErr = emptyErr
			t.provider.MarkRecovering(apiKey)
			if cb != nil {
				emitStreamStatus(cb, "retrying_key", "next")
			}
			return result, lastInputTokens, nil
		}
		result.resp = resp
	}
	return result, lastInputTokens, nil
}

// completeStreamWithRetry walks the model pool (cursor-start entry + optional
// remaining entries) and, for each model, loops over keys until success, a permanent failure, or
// keys exhausted. Retriable API errors rotate keys; 401/403 force key rotation
// (OAuth refresh when possible). Timeouts before any visible output do not
// rotate to another key on the same provider; they skip sibling targets on that
// provider for the current round and advance to the next provider/model. Visible
// stream interruptions are retried by the caller on the same key. Exponential
// backoff applies between full rounds, not between keys.
func (c *Client) completeStreamWithRetry(
	ctx context.Context,
	startProvider *ProviderConfig,
	startImpl Provider,
	startModelID string,
	startMaxTokens int,
	startTuning RequestTuning,
	startVariant string,
	messages []message.Message,
	tools []message.ToolDefinition,
	cb StreamCallback,
	fallbackEnabled bool,
	fallbackModels []FallbackModel,
	maxAttempts int,
	status *CallStatus,
	startRoutingGeneration uint64,
	routingChangedCh <-chan struct{},
) (*message.Response, error) {
	var lastErr error
	var pendingRoundWait time.Duration
	abortIfCancelled := func() error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("LLM request aborted: %w", err)
		}
		if currentGeneration, invalidated := c.routingInvalidated(startRoutingGeneration); invalidated {
			return &RoutingInvalidatedError{StartedGeneration: startRoutingGeneration, CurrentGeneration: currentGeneration}
		}
		return nil
	}

	hardCap := false
	if maxAttempts < 0 {
		hardCap = true
		maxAttempts = -maxAttempts
	}
	if maxAttempts <= 0 {
		maxAttempts = 0
	}

	lastInputTokens := c.getLastInputTokens()
	outputCapSetting := c.getOutputTokenMax()
	systemPrompt := c.getSystemPrompt()
	variantForStart := strings.TrimSpace(startVariant)

	retryCount := 0

	// Public CompleteStream defaults to unlimited full-round retries. Callers can
	// pass a positive maxAttempts for the historical soft cap (cooling /
	// concurrent-request 429 may continue past it), or a negative value to apply
	// a hard cap that stops after that many rounds regardless of error class.
	for round := 0; shouldContinueRetryMode(retryCount, maxAttempts, lastErr, hardCap); round++ {
		// skippedProviders only applies within the current round. Provider-level
		// failures (including pre-visible timeouts) should skip sibling targets on
		// the same provider for this round, but the next round must probe again.
		skippedProviders := map[*ProviderConfig]string{}
		oversizeSeen := newOversizeRegistry()
		if err := abortIfCancelled(); err != nil {
			return nil, err
		}
		// Apply backoff delay only between full retry rounds.
		if round > 0 {
			delay := roundRetryDelay(startProvider.GetRetryDelay(retryCount), pendingRoundWait)
			log.Infof("retrying LLM request round attempt=%v retry_count=%v delay=%v error=%v", round+1, retryCount, delay, lastErr)
			if cb != nil {
				if err := abortIfCancelled(); err != nil {
					return nil, err
				}
				detail := fmt.Sprintf("round %d", round+1)
				cb(message.StreamDelta{
					Type: "status",
					Status: &message.StatusDelta{
						Type:   "retrying",
						Detail: detail,
					},
				})
			}
			if delay > 0 {
				select {
				case <-time.After(delay):
				case <-routingChangedCh:
					if err := abortIfCancelled(); err != nil {
						return nil, err
					}
				case <-ctx.Done():
					return nil, fmt.Errorf("context cancelled during retry backoff: %w", ctx.Err())
				}
			}
		}
		pendingRoundWait = 0
		// roundHadRequestAttempt tracks whether this round reached the provider API
		// at least once. If every target fails key selection with NoUsableKeysError,
		// a full-round retry cannot make progress without external state changes.
		roundHadRequestAttempt := false
		roundHadUsableReply := false

		// Define the list of models to try in this round.
		// Models are tried in order: current cursor-start entry first, then the
		// remaining pool entries. This implements sticky-cursor failover: the
		// current successful entry stays pinned until it fails.
		targets := c.buildStreamRetryTargets(
			startProvider,
			startImpl,
			startModelID,
			startMaxTokens,
			startTuning,
			variantForStart,
			outputCapSetting,
			fallbackEnabled,
			fallbackModels,
		)
		for ti := 0; ti < len(targets); ti++ {
			if err := abortIfCancelled(); err != nil {
				return nil, err
			}
			t := targets[ti]
			if skipReason, ok := skippedProviders[t.provider]; ok {
				log.Infof("skipping model: provider skipped for current round provider=%v model=%v reason=%v", t.provider.Name(), t.modelID, skipReason)
				continue
			}
			if oversizeSeen.seen(t.provider.Name(), t.modelID, t.variant) {
				log.Infof("skipping model: oversize already confirmed for this provider/model target in this round provider=%v model=%v variant=%v", t.provider.Name(), t.modelID, t.variant)
				continue
			}
			if targetResult, updatedLastInputTokens, err := c.completeStreamTarget(
				ctx,
				t,
				round,
				messages,
				tools,
				cb,
				fallbackEnabled,
				fallbackModels,
				pendingRoundWait,
				ti < len(targets)-1,
				status,
				systemPrompt,
				outputCapSetting,
				lastInputTokens,
				abortIfCancelled,
				oversizeSeen,
			); err != nil {
				return nil, err
			} else {
				lastInputTokens = updatedLastInputTokens
				lastErr = targetResult.lastErr
				pendingRoundWait = mergePendingRoundWait(pendingRoundWait, targetResult.pendingRoundWait)
				if targetResult.hadRequestAttempt {
					roundHadRequestAttempt = true
				}
				if targetResult.roundHadUsableReply {
					roundHadUsableReply = true
				}
				if targetResult.skipProvider {
					skippedProviders[t.provider] = providerSkipReason(lastErr)
				}
				if targetResult.resp != nil {
					return targetResult.resp, nil
				}
			}
			// Stop immediately only for permanent failures (auth, permission, malformed request).
			if isPermanentFailure(lastErr) {
				log.Errorf("LLM permanent failure, giving up provider=%v model=%v error=%v", startProvider.Name(), startModelID, lastErr)
				return nil, lastErr
			}
		} // end targets loop
		// Track that the full model pool was tried and all failed.
		if fallbackEnabled && status != nil && status.FallbackTriggered {
			status.FallbackExhausted = true
		}
		if IsContextLengthExceeded(lastErr) {
			if len(targets) > 1 {
				log.Infof("context length exceeded after model pool exhausted; returning for compaction recovery provider=%v model=%v input_tokens_est=%v", startProvider.Name(), startModelID, estimateRequestInputTokens(systemPrompt, messages, tools))
			}
			emitStreamStatusDelta(cb, message.StatusDelta{
				Type:   "retrying",
				Detail: "pool exhausted; compacting context",
				Reason: "context_length_exceeded",
			})
			return nil, lastErr
		}
		if _, ok := errors.AsType[*NoUsableKeysError](lastErr); ok {
			if fallbackEnabled && len(targets) > 1 && roundHadRequestAttempt {
				log.Warnf("model pool exhausted with no usable keys after at least one request attempt; retrying full pool provider=%v model=%v error=%v", startProvider.Name(), startModelID, lastErr)
			} else {
				return nil, lastErr
			}
		}
		if isTerminalModelPoolFailure(lastErr) {
			return nil, lastErr
		}
		retryCount = nextRetryCount(retryCount, roundHadUsableReply)
	} // end attempts loop

	log.Errorf("LLM all retries exhausted max_attempts=%v provider=%v model=%v error=%v", maxAttempts, startProvider.Name(), startModelID, lastErr)
	return nil, lastErr
}

// estimateInputTokens provides a rough token estimate from messages when no
// API-reported usage is available (e.g. the cursor-start model failed on every attempt).
// Uses the heuristic of ~3 characters per token (conservative for English).
func estimateInputTokens(messages []message.Message) int {
	total := 0
	for _, msg := range messages {
		n := len(msg.Content)
		for _, tc := range msg.ToolCalls {
			n += len(tc.Args)
		}
		for _, tb := range msg.ThinkingBlocks {
			n += len(tb.Thinking)
		}
		total += n
	}
	if total == 0 {
		return 0
	}
	return total / 3
}

func estimateToolDefinitionTokens(tools []message.ToolDefinition) int {
	if len(tools) == 0 {
		return 0
	}
	total := 0
	for _, tool := range tools {
		n := len(tool.Name) + len(tool.Description)
		if schemaBytes, err := json.Marshal(tool.InputSchema); err == nil {
			n += len(schemaBytes)
		}
		total += n
	}
	if total == 0 {
		return 0
	}
	return total / 3
}

func estimateRequestInputTokens(systemPrompt string, messages []message.Message, tools []message.ToolDefinition) int {
	total := estimateInputTokens(messages) + estimateToolDefinitionTokens(tools)
	if systemPrompt != "" {
		total += len(systemPrompt) / 3
	}
	if total < 1 {
		return 1
	}
	return total
}

// EstimateRequestInputTokens returns the same rough request-side token estimate
// used by clampEffectiveMaxTokens. It includes system prompt, messages, and
// tool schema overhead.
func EstimateRequestInputTokens(systemPrompt string, messages []message.Message, tools []message.ToolDefinition) int {
	return estimateRequestInputTokens(systemPrompt, messages, tools)
}

func clampEffectiveMaxTokens(
	model config.ModelConfig,
	effectiveMaxTokens int,
	outputCapSetting int,
	tuning RequestTuning,
	systemPrompt string,
	messages []message.Message,
	tools []message.ToolDefinition,
	lastInputTokens int,
) int {
	if model.Limit.Output > 0 && model.Limit.Output < effectiveMaxTokens {
		effectiveMaxTokens = model.Limit.Output
	}

	outputCap := outputCapSetting
	if outputCap <= 0 {
		outputCap = DefaultOutputTokenMax
	}
	if outputCap < effectiveMaxTokens {
		minForThinking := 0
		if tuning.Anthropic.ThinkingBudget > 0 {
			minForThinking = tuning.Anthropic.ThinkingBudget + 1024
		}
		if outputCap < minForThinking {
			outputCap = minForThinking
		}
		if outputCap < effectiveMaxTokens {
			effectiveMaxTokens = outputCap
		}
	}

	inputEstimate := max(estimateRequestInputTokens(systemPrompt, messages, tools), lastInputTokens)
	if model.Limit.Context > 0 {
		buffer := max(model.Limit.Context/100, 256)
		contextCap := max(model.Limit.Context-inputEstimate-buffer, 1)
		if contextCap < effectiveMaxTokens {
			effectiveMaxTokens = contextCap
		}
	}
	return effectiveMaxTokens
}
