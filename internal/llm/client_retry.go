package llm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/executiongate"
	"github.com/keakon/chord/internal/message"
)

type streamRoundAttemptSummary struct {
	attemptedTargets             int
	contextLengthExceededTargets int
	lastContextLengthErr         error
	retriableTargets             int
	deferredTargets              int
	retryWaitTargets             []roundRetryWaitTarget
	lastRelayRejectionErr        error
	lastRelayRejectionProvider   *ProviderConfig
	// Request defects prevent another round only after every target has failed.
	requestDefectTargets      int
	lastRequestDefectErr      error
	lastRequestDefectProvider *ProviderConfig
}

type roundRetryWaitTarget struct {
	index        int
	provider     *ProviderConfig
	observed     time.Time
	connectDelay time.Duration
	coolingWait  roundCoolingWait
	coolingCause *keyCooldownCause
}

type roundRetryWaitPlan struct {
	delay        time.Duration
	cooling      bool
	recovery     time.Duration
	coolingCause *keyCooldownCause
}

func (s *streamRoundAttemptSummary) record(result streamTargetAttemptResult) (terminalTarget bool) {
	if s == nil {
		return false
	}
	if !result.hadRequestAttempt {
		// A cooling target never reached the wire, but its sibling can recover
		// once the window passes; it still counts as a retriable sibling when
		// deciding whether to withhold a relay-scoped terminal rejection.
		if isAllKeysCoolingError(result.lastErr) {
			s.retriableTargets++
		}
		return false
	}
	s.attemptedTargets++
	if IsContextLengthExceeded(result.lastErr) {
		s.contextLengthExceededTargets++
		s.lastContextLengthErr = result.lastErr
		// Context length is request-scoped: the round-level all-attempted check
		// turns it into compaction recovery, and it is never a retriable sibling.
		return false
	}
	if result.lastErr == nil {
		return false
	}
	if !isTerminalModelPoolFailureForProvider(result.lastErrProvider, result.lastErr) {
		s.retriableTargets++
		return false
	}
	if requestDefect(result.lastErrProvider, result.lastErr) {
		s.requestDefectTargets++
		s.lastRequestDefectErr = result.lastErr
		s.lastRequestDefectProvider = result.lastErrProvider
	} else if relayScopedRequestRejection(result.lastErrProvider, result.lastErr) {
		s.lastRelayRejectionErr = result.lastErr
		s.lastRelayRejectionProvider = result.lastErrProvider
	} else {
		// A terminal endpoint failure must not suppress a sibling's retry.
		return true
	}
	return false
}

func (s streamRoundAttemptSummary) nextRetryWaitPlan(retryCount int, fallbackDelay time.Duration, retryAt map[int]time.Time) roundRetryWaitPlan {
	var plan roundRetryWaitPlan
	set := false
	now := time.Now()
	consider := func(delay, recovery time.Duration, cooling bool, cause *keyCooldownCause) {
		delay, recovery = max(0, delay), max(0, recovery)
		if !set || delay < plan.delay {
			plan.delay = delay
		}
		if !set || recovery < plan.recovery {
			plan.cooling, plan.recovery, plan.coolingCause = cooling, recovery, cause
		}
		set = true
	}
	// Previously deferred targets were not attempted in this round.
	for _, deadline := range retryAt {
		wait := deadline.Sub(now)
		consider(wait, wait, false, nil)
	}
	for _, target := range s.retryWaitTargets {
		if target.coolingWait.waiting() {
			elapsed := now.Sub(target.observed)
			connectWait := target.connectDelay - elapsed
			if connectWait > 0 {
				// A key may become usable early, but it cannot bypass this target's connect backoff.
				retryAt[target.index] = target.observed.Add(target.connectDelay)
			}
			consider(max(target.coolingWait.sleep-elapsed, connectWait), max(target.coolingWait.recovery-elapsed, connectWait), true, target.coolingCause)
			continue
		}
		deadline := target.observed.Add(max(target.connectDelay, target.provider.GetRetryDelay(retryCount)))
		retryAt[target.index] = deadline
		wait := deadline.Sub(now)
		consider(wait, wait, false, nil)
	}
	if !set {
		return roundRetryWaitPlan{delay: fallbackDelay}
	}
	return plan
}

func (s streamRoundAttemptSummary) allAttemptedTargetsContextLengthExceeded() bool {
	return s.attemptedTargets > 0 &&
		s.contextLengthExceededTargets == s.attemptedTargets &&
		s.retriableTargets == 0 &&
		s.lastContextLengthErr != nil
}

// completeStreamWithRetry walks the model pool (cursor-start entry + optional
// remaining entries) and, for each model, loops over keys until success, a permanent failure, or
// keys exhausted. Retriable API errors rotate keys; 401/403/429 force key
// rotation (OAuth refresh when possible for auth errors). Timeouts before any
// visible output do not rotate to another key on the same provider; they skip
// sibling targets on that provider for the current round and advance to the
// next provider/model. Visible stream interruptions are retried by the caller
// on the same key, except auth and 429 errors, which cool the key down and
// rotate to the next one. Provider-configured pacing applies between full
// rounds, not between keys.
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
	mcpDeclarations []message.ToolDefinition,
	cb StreamCallback,
	fallbackEnabled bool,
	fallbackModels []FallbackModel,
	maxAttempts int,
	status *CallStatus,
	startRoutingGeneration uint64,
	routingChangedCh <-chan struct{},
	beforeFallback func(context.Context, []message.Message, FallbackModel) (*RequestSnapshot, error),
	beforeRetryRound func(context.Context, []message.Message) (*RequestSnapshot, error),
	prepareAttempt func(context.Context, RequestAttempt) (context.Context, error),
) (resp *message.Response, err error) {
	var lastErr error
	var lastTerminalTargetErr error
	var pendingRoundPlan roundRetryWaitPlan
	retryAt := make(map[int]time.Time)
	connectFailureCounts := make(map[int]int)
	// withheldTerminalSince is set when a relay-scoped terminal rejection was
	// withheld to give a retriable sibling time to recover, so the wait stays
	// bounded by withheldTerminalFailureBudget across rounds.
	var withheldTerminalSince time.Time
	var withheldTerminalErr error
	var withheldTerminalProvider *ProviderConfig
	// pendingRollbackReason is non-empty while preserved partial output is
	// still on screen across targets and rounds. On any non-cancel exit the
	// preserved text must leave the screen: the turn failed, so a dangling
	// card would diverge from the session record. User cancellation
	// deliberately keeps the partial (the turn persists it as the interrupted
	// assistant message).
	var pendingRollback string
	// Every request body cached during this walk is released on the way out.
	// The cache only has to spare the key-attempt loop inside one target a
	// rebuild, so it must outlive a round (a later round retries the same
	// target) but not the call: providers are shared by every client using
	// that provider name, and keeping the entry would pin the last request's
	// full body, its compressed copy and the whole messages backing array for
	// as long as the provider lives.
	var bodyCachePins []bodyCachePin
	defer func() {
		for _, pin := range bodyCachePins {
			if releaser, ok := pin.impl.(BodyCacheReleaser); ok {
				releaser.ReleaseBodyCache(pin.msgs, pin.msgsLen)
			}
		}
	}()
	defer func() {
		if pendingRollback == "" || cb == nil || err == nil || resp != nil {
			return
		}
		if errors.Is(err, context.Canceled) || executiongate.IsPaused(err) {
			return
		}
		cb(message.StreamDelta{
			Type: message.StreamDeltaRollback,
			Rollback: &message.RollbackDelta{
				Reason: err.Error(),
			},
		})
	}()
	abortIfCancelled := func() error {
		if err := executiongate.Check(ctx); err != nil {
			return err
		}
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
	// The default (uncapped) path lets cooling/429 retries run indefinitely, but
	// a deterministic upstream stream interruption must not burn time forever.
	// When the caller set no explicit cap, stop after upstreamStreamFailureRetryRounds
	// retry rounds once the failure is an upstream stream interruption.
	defaultUncapped := maxAttempts == 0 && !hardCap

	lastInputTokens := c.getLastInputTokens()
	outputCapSetting := c.getOutputTokenMax()
	systemPrompt := c.getSystemPrompt()
	variantForStart := validVariantForModel(startProvider, startModelID, startVariant)

	retryCount := 0
	replayEchoRejectedTargets := make(map[replayEchoTarget]struct{})
	exhaustedTargets := make(map[replayEchoTarget]struct{})

	// Public CompleteStream defaults to unlimited full-round retries. Callers can
	// pass a positive maxAttempts for the historical soft cap (cooling /
	// concurrent-request 429 may continue past it), or a negative value to apply
	// a hard cap that stops after that many rounds regardless of error class.
	for round := 0; shouldContinueRetryMode(retryCount, maxAttempts, lastErr, hardCap) &&
		!(defaultUncapped && lastErr != nil && IsUpstreamStreamFailure(lastErr) && round > upstreamStreamFailureRetryRounds); round++ {
		// skippedProviders only applies within the current round. Provider-level
		// failures (including pre-visible timeouts) should skip sibling targets on
		// the same provider for this round, but the next round must probe again.
		skippedProviders := map[*ProviderConfig]string{}
		oversizeSeen := newOversizeRegistry()
		if err := abortIfCancelled(); err != nil {
			return nil, err
		}
		if withheldTerminalErr != nil && time.Since(withheldTerminalSince) >= withheldTerminalFailureBudget {
			return nil, withheldTerminalErr
		}
		// Publish the upcoming target before waiting: the preceding round's last
		// failed target is no longer the model this request is preparing to use.
		startDisplayRef := providerModelRef(startProvider, startModelID)
		if variantForStart != "" {
			startDisplayRef += "@" + variantForStart
		}
		c.noteAttemptModelRef(startDisplayRef)
		// Apply backoff delay only between full retry rounds.
		if round > 0 {
			waitingForCooling := pendingRoundPlan.cooling
			delay := pendingRoundPlan.delay
			if withheldTerminalErr != nil {
				delay = min(delay, max(0, withheldTerminalFailureBudget-time.Since(withheldTerminalSince)))
			}
			if waitingForCooling {
				recovery := pendingRoundPlan.recovery
				log.Infof("waiting for API keys to cool before next LLM request round attempt=%v delay=%v recovery_in=%v error=%v", round+1, delay, recovery, lastErr)
				if cb != nil {
					if err := abortIfCancelled(); err != nil {
						return nil, err
					}
					// The countdown must promise when a request can really go
					// out, not when this round wakes up to re-probe the pool.
					emitStreamStatusDelta(cb, message.StatusDelta{Type: message.StatusDeltaCooling, Detail: recovery.Round(time.Second).String(), ModelRef: startDisplayRef, Deadline: time.Now().Add(recovery)})
					reportCoolingCause(cb, pendingRoundPlan.coolingCause)
				}
			} else {
				log.Infof("retrying LLM request round attempt=%v retry_count=%v delay=%v error=%v", round+1, retryCount, delay, lastErr)
				if cb != nil {
					if err := abortIfCancelled(); err != nil {
						return nil, err
					}
					status := message.StatusDelta{
						Type:     message.StatusDeltaRetrying,
						Detail:   fmt.Sprintf("round %d", round+1),
						ModelRef: startDisplayRef,
					}
					// The round sleeps a known delay before the next attempt goes
					// out (provider backoff, or the cooling window a target that
					// did not end the round reported). Attach it so the status bar
					// counts down to that attempt instead of showing only how long
					// the retry streak has run. A zero-delay round re-probes
					// immediately and has no deadline to promise.
					if wait := max(delay, pendingRoundPlan.recovery); wait > 0 {
						status.Deadline = time.Now().Add(wait)
					}
					emitStreamStatusDelta(cb, status)
				}
			}
			if err := abortIfCancelled(); err != nil {
				return nil, err
			}
			if withheldTerminalErr != nil && time.Since(withheldTerminalSince) >= withheldTerminalFailureBudget {
				return nil, withheldTerminalErr
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
				case <-executiongate.FromContext(ctx).Frozen():
					return nil, &executiongate.ErrSessionPaused{}
				}
			}
			if err := abortIfCancelled(); err != nil {
				return nil, err
			}
			if withheldTerminalErr != nil && time.Since(withheldTerminalSince) >= withheldTerminalFailureBudget {
				return nil, withheldTerminalErr
			}
		}
		// roundHadRequestAttempt tracks whether this round reached the provider API
		// at least once. If every target fails key selection with NoUsableKeysError,
		// a full-round retry cannot make progress without external state changes.
		roundHadRequestAttempt := false
		roundHadUsableReply := false
		roundAttemptSummary := streamRoundAttemptSummary{}

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
		retryRoundPrepared := false
		for ti := range targets {
			if err := abortIfCancelled(); err != nil {
				return nil, err
			}
			t := targets[ti]
			replayTarget := replayEchoTarget{provider: t.provider, modelID: t.modelID, variant: t.variant}
			if _, exhausted := exhaustedTargets[replayTarget]; exhausted {
				delete(retryAt, ti)
				continue
			}
			if _, rejected := replayEchoRejectedTargets[replayTarget]; rejected {
				delete(retryAt, ti)
				log.Infof("skipping model: target repeated replay evidence in this request provider=%v model=%v variant=%v", t.provider.Name(), t.modelID, t.variant)
				continue
			}
			if skipReason, ok := skippedProviders[t.provider]; ok {
				log.Infof("skipping model: provider skipped for current round provider=%v model=%v reason=%v", t.provider.Name(), t.modelID, skipReason)
				continue
			}
			if oversizeSeen.seen(t.provider.Name(), t.modelID, t.variant) {
				log.Infof("skipping model: oversize already confirmed for this provider/model target in this round provider=%v model=%v variant=%v", t.provider.Name(), t.modelID, t.variant)
				continue
			}
			if deadline, ok := retryAt[ti]; ok {
				if time.Now().Before(deadline) {
					roundAttemptSummary.retriableTargets++
					roundAttemptSummary.deferredTargets++
					continue
				}
				delete(retryAt, ti)
			}
			// The target is fixed before request preparation and its status events.
			c.noteAttemptModelRef(t.displayRef())
			if !retryRoundPrepared && round > 0 && beforeRetryRound != nil {
				retryRoundPrepared = true
				snapshot, hookErr := beforeRetryRound(ctx, messages)
				if hookErr != nil {
					return nil, fmt.Errorf("update request before retry round: %w", hookErr)
				}
				if snapshot != nil {
					messages, mcpDeclarations = snapshot.Messages, snapshot.MCPDeclarations
					t.tuning.Anthropic.CacheBoundary = AnthropicCacheBoundary{}
					t.tuning.Anthropic.CacheLatestBoundary = AnthropicCacheBoundary{}
				}
			}
			if t.isFallback && beforeFallback != nil {
				snapshot, err := beforeFallback(ctx, messages, t.fallbackModel())
				if err != nil {
					return nil, fmt.Errorf("update request before fallback: %w", err)
				}
				if snapshot != nil {
					messages, mcpDeclarations = snapshot.Messages, snapshot.MCPDeclarations
					t.tuning.Anthropic.CacheBoundary = AnthropicCacheBoundary{}
					t.tuning.Anthropic.CacheLatestBoundary = AnthropicCacheBoundary{}
				}
			}
			targetResult, updatedLastInputTokens, targetErr := c.completeStreamTarget(streamTargetAttempt{
				ctx:              ctx,
				target:           t,
				round:            round,
				messages:         messages,
				tools:            tools,
				mcpDeclarations:  mcpDeclarations,
				cb:               cb,
				fallbackEnabled:  fallbackEnabled,
				fallbackModels:   fallbackModels,
				status:           status,
				systemPrompt:     systemPrompt,
				outputCapSetting: outputCapSetting,
				lastInputTokens:  lastInputTokens,
				abortIfCancelled: abortIfCancelled,
				oversizeSeen:     oversizeSeen,
				pendingRollback:  pendingRollback,
				prepareAttempt:   prepareAttempt,
			})
			// Collect the target's cache entries before branching: a failed
			// target may have cached a body on the way to its error.
			bodyCachePins = append(bodyCachePins, targetResult.bodyCachePins...)
			if targetErr != nil {
				// An escalated preserved interruption carries the partial text
				// forward: the caller saves it as the interrupted assistant
				// message and resumes it. A pendingRollback still armed by an
				// earlier target (a thinking-only interruption that retried
				// silently) would make the deferred rollback above wipe that
				// text on the way out, so disarm it here.
				if IsPreservableStreamInterruption(targetErr) {
					pendingRollback = ""
				}
				if executiongate.IsPaused(targetErr) {
					return targetResult.resp, targetErr
				}
				if unknown, ok := errors.AsType[*HostedOutcomeUnknownError](targetErr); ok {
					return unknown.Response, targetErr
				}
				return nil, targetErr
			} else {
				var connectDelay time.Duration
				if targetResult.reachedServer {
					delete(connectFailureCounts, ti)
				} else if targetResult.connectPhaseFailure {
					connectFailureCounts[ti]++
					connectDelay = connectFailureRetryDelay(connectFailureCounts[ti])
				}
				lastInputTokens = updatedLastInputTokens
				lastErr = targetResult.lastErr
				previous := roundAttemptSummary.retriableTargets
				terminalTarget := roundAttemptSummary.record(targetResult)
				if terminalTarget {
					lastTerminalTargetErr = lastErr
				}
				if _, repeatedEcho := errors.AsType[*ReplayEvidenceEchoError](lastErr); repeatedEcho {
					replayEchoRejectedTargets[replayTarget] = struct{}{}
				} else if terminalTarget || targetResult.continuationExhausted {
					exhaustedTargets[replayTarget] = struct{}{}
				}
				if roundAttemptSummary.retriableTargets > previous && !targetResult.continuationExhausted {
					roundAttemptSummary.retryWaitTargets = append(roundAttemptSummary.retryWaitTargets, roundRetryWaitTarget{
						index: ti, provider: t.provider, observed: time.Now(), connectDelay: connectDelay,
						coolingWait: targetResult.pendingRoundWait, coolingCause: targetResult.coolingCause,
					})
				}
				if targetResult.hadRequestAttempt {
					roundHadRequestAttempt = true
				}
				if targetResult.roundHadUsableReply {
					roundHadUsableReply = true
				}
				if targetResult.skipProvider {
					skippedProviders[t.provider] = providerSkipReason(lastErr)
				}
				pendingRollback = targetResult.pendingRollbackReason
				if targetResult.resp != nil {
					return targetResult.resp, nil
				}
			}
		} // end targets loop
		allExhausted := len(targets) > 0
		for _, target := range targets {
			key := replayEchoTarget{provider: target.provider, modelID: target.modelID, variant: target.variant}
			_, exhausted := exhaustedTargets[key]
			_, rejected := replayEchoRejectedTargets[key]
			if !exhausted && !rejected {
				allExhausted = false
				break
			}
		}
		if allExhausted {
			if fallbackEnabled && status != nil && status.FallbackTriggered {
				status.FallbackExhausted = true
			}
			return nil, lastErr
		}

		// Track that the full model pool was tried and all failed.
		if fallbackEnabled && status != nil && status.FallbackTriggered {
			status.FallbackExhausted = true
		}
		if roundAttemptSummary.allAttemptedTargetsContextLengthExceeded() {
			if len(targets) > 1 {
				log.Infof("context length exceeded after model pool exhausted; returning for compaction recovery provider=%v model=%v input_tokens_est=%v", startProvider.Name(), startModelID, estimateRequestInputTokens(systemPrompt, messages, tools))
			}
			emitStreamStatusDelta(cb, message.StatusDelta{
				Type:   message.StatusDeltaRetrying,
				Detail: "pool exhausted; compacting context",
				Reason: "context_length_exceeded",
			})
			return nil, &AllAttemptedCandidatesContextLengthExceededError{Inner: roundAttemptSummary.lastContextLengthErr}
		}
		if _, ok := errors.AsType[*NoUsableKeysError](lastErr); ok {
			if !roundHadRequestAttempt && len(targets) <= 1 {
				return nil, lastErr
			}
			log.Warnf("model pool exhausted with no usable keys; retrying full pool provider=%v model=%v had_request_attempt=%v error=%v", startProvider.Name(), startModelID, roundHadRequestAttempt, lastErr)
		}
		if roundAttemptSummary.requestDefectTargets > 0 {
			log.Infof("request defect ended the round despite retriable siblings provider=%v error=%v request_defect_targets=%v retriable_targets=%v", providerLogName(roundAttemptSummary.lastRequestDefectProvider), roundAttemptSummary.lastRequestDefectErr, roundAttemptSummary.requestDefectTargets, roundAttemptSummary.retriableTargets)
			return nil, roundAttemptSummary.lastRequestDefectErr
		}
		if IsContextLengthExceeded(lastErr) && roundAttemptSummary.deferredTargets == 0 {
			return nil, lastErr
		}
		if roundAttemptSummary.lastRelayRejectionErr != nil {
			withheldTerminalErr = roundAttemptSummary.lastRelayRejectionErr
			withheldTerminalProvider = roundAttemptSummary.lastRelayRejectionProvider
			if !terminalPoolFailureKeepsRetrying(withheldTerminalProvider, withheldTerminalErr, roundAttemptSummary.retriableTargets) {
				return nil, withheldTerminalErr
			}
			if withheldTerminalSince.IsZero() {
				withheldTerminalSince = time.Now()
			}
		}
		// Key selection can overwrite the endpoint error without providing
		// another retry candidate. Preserve the error that exhausted the target.
		if lastTerminalTargetErr != nil && len(retryAt) == 0 && len(roundAttemptSummary.retryWaitTargets) == 0 {
			return nil, lastTerminalTargetErr
		}
		// A transient-only later round must not erase an earlier relay rejection
		// or restart its recovery budget.
		if withheldTerminalErr != nil {
			if time.Since(withheldTerminalSince) >= withheldTerminalFailureBudget {
				return nil, withheldTerminalErr
			}
			log.Infof("relay rejection withheld while sibling recovery budget remains provider=%v error=%v budget_left=%v", providerLogName(withheldTerminalProvider), withheldTerminalErr, withheldTerminalFailureBudget-time.Since(withheldTerminalSince))
		}
		retryCount = nextRetryCount(retryCount, roundHadRequestAttempt, roundHadUsableReply, lastErr, hardCap)
		pendingRoundPlan = roundAttemptSummary.nextRetryWaitPlan(retryCount, startProvider.GetRetryDelay(retryCount), retryAt)
	} // end attempts loop

	log.Errorf("LLM all retries exhausted max_attempts=%v provider=%v model=%v error=%v", maxAttempts, startProvider.Name(), startModelID, lastErr)
	return nil, lastErr
}
