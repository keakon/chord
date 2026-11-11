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
	lastRelayRejectionErr        error
	lastRelayRejectionProvider   *ProviderConfig
	// Request defects prevent another round only after every target has failed.
	requestDefectTargets      int
	lastRequestDefectErr      error
	lastRequestDefectProvider *ProviderConfig
	// connectPhaseFailure records that some target of the round failed before
	// reaching the server (DNS resolution, dial, TLS verification).
	connectPhaseFailure bool
	// reachedServer includes responses from earlier keys and internal retries,
	// even when the target's final outcome was a connection failure.
	reachedServer bool
}

func (s *streamRoundAttemptSummary) record(result streamTargetAttemptResult) {
	if s == nil {
		return
	}
	s.connectPhaseFailure = s.connectPhaseFailure || result.connectPhaseFailure
	s.reachedServer = s.reachedServer || result.reachedServer
	if !result.hadRequestAttempt {
		// A cooling target never reached the wire, but its sibling can recover
		// once the window passes; it still counts as a retriable sibling when
		// deciding whether to withhold a relay-scoped terminal rejection.
		if isAllKeysCoolingError(result.lastErr) {
			s.retriableTargets++
		}
		return
	}
	s.attemptedTargets++
	if IsContextLengthExceeded(result.lastErr) {
		s.contextLengthExceededTargets++
		s.lastContextLengthErr = result.lastErr
		// Context length is request-scoped: the round-level all-attempted check
		// turns it into compaction recovery, and it is never a retriable sibling.
		return
	}
	if result.lastErr == nil {
		return
	}
	if !isTerminalModelPoolFailureForProvider(result.lastErrProvider, result.lastErr) {
		s.retriableTargets++
		return
	}
	if requestDefect(result.lastErrProvider, result.lastErr) {
		s.requestDefectTargets++
		s.lastRequestDefectErr = result.lastErr
		s.lastRequestDefectProvider = result.lastErrProvider
	} else if relayScopedRequestRejection(result.lastErrProvider, result.lastErr) {
		s.lastRelayRejectionErr = result.lastErr
		s.lastRelayRejectionProvider = result.lastErrProvider
	}
}

func (s streamRoundAttemptSummary) allAttemptedTargetsContextLengthExceeded() bool {
	return s.attemptedTargets > 0 && s.contextLengthExceededTargets == s.attemptedTargets && s.lastContextLengthErr != nil
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
	beforeFallback func(context.Context, []message.Message, FallbackModel) ([]message.Message, error),
	beforeRetryRound func(context.Context, []message.Message) ([]message.Message, error),
	prepareAttempt func(context.Context, RequestAttempt) (context.Context, error),
) (resp *message.Response, err error) {
	var lastErr error
	var lastErrProvider *ProviderConfig
	var pendingRoundWait roundCoolingWait
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
	// consecutiveConnectFailures counts consecutive rounds that kept failing
	// before reaching the server; any round that reached the wire restarts
	// the connect-failure ladder from its first rung.
	consecutiveConnectFailures := 0
	// lastRoundConnectFailed is the previous round's ladder verdict — the
	// round failed connect-phase and no target reached the server. The next
	// round's pacing reads it before this round's targets run; the verdict is
	// round-level because the last target's error cannot represent the round
	// (a sibling that never went out would otherwise mask it either way).
	lastRoundConnectFailed := false
	replayEchoRejectedTargets := make(map[replayEchoTarget]struct{})
	continuationExhaustedTargets := make(map[replayEchoTarget]struct{})

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
			waitingForCooling := isAllKeysCoolingError(lastErr) && pendingRoundWait.waiting()
			delay := roundRetryDelay(startProvider.GetRetryDelay(retryCount), pendingRoundWait.sleep)
			if waitingForCooling {
				delay = pendingRoundWait.sleep
			} else if lastRoundConnectFailed {
				// The previous round never reached the server, so its failure
				// carries no new information and the condition behind it
				// (network down, resolver down) changes on the scale of
				// seconds. Pace the re-probe on the connect ladder regardless
				// of provider backoff: a none or fixed 0 setting would
				// otherwise spin at whatever speed failures return.
				if laddered := connectFailureRetryDelay(consecutiveConnectFailures); laddered > delay {
					delay = laddered
				}
			}
			if withheldTerminalErr != nil {
				delay = min(delay, max(0, withheldTerminalFailureBudget-time.Since(withheldTerminalSince)))
			}
			if waitingForCooling {
				recovery := pendingRoundWait.recovery
				log.Infof("waiting for API keys to cool before next LLM request round attempt=%v delay=%v recovery_in=%v error=%v", round+1, delay, recovery, lastErr)
				if cb != nil {
					if err := abortIfCancelled(); err != nil {
						return nil, err
					}
					// The countdown must promise when a request can really go
					// out, not when this round wakes up to re-probe the pool.
					emitStreamStatusDelta(cb, message.StatusDelta{Type: message.StatusDeltaCooling, Detail: recovery.Round(time.Second).String(), ModelRef: startDisplayRef, Deadline: time.Now().Add(recovery)})
					if cooling, ok := errors.AsType[*AllKeysCoolingError](lastErr); ok {
						reportCoolingCause(cb, cooling.cause)
					}
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
					if delay > 0 {
						status.Deadline = time.Now().Add(delay)
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
		pendingRoundWait = roundCoolingWait{}
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
			if _, exhausted := continuationExhaustedTargets[replayTarget]; exhausted {
				continue
			}
			if _, rejected := replayEchoRejectedTargets[replayTarget]; rejected {
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
			// The target is fixed before request preparation and its status events.
			c.noteAttemptModelRef(t.displayRef())
			if !retryRoundPrepared && round > 0 && beforeRetryRound != nil {
				retryRoundPrepared = true
				updatedMessages, hookErr := beforeRetryRound(ctx, messages)
				if hookErr != nil {
					return nil, fmt.Errorf("update request before retry round: %w", hookErr)
				}
				if updatedMessages != nil {
					messages = updatedMessages
				}
			}
			if t.isFallback && beforeFallback != nil {
				updatedMessages, err := beforeFallback(ctx, messages, t.fallbackModel())
				if err != nil {
					return nil, fmt.Errorf("update request before fallback: %w", err)
				}
				if updatedMessages != nil {
					messages = updatedMessages
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
				currentRoundWait: pendingRoundWait,
				hasNextTarget:    ti < len(targets)-1,
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
				if _, repeatedEcho := errors.AsType[*ReplayEvidenceEchoError](targetResult.lastErr); repeatedEcho {
					replayEchoRejectedTargets[replayTarget] = struct{}{}
				}
				if targetResult.continuationExhausted {
					continuationExhaustedTargets[replayTarget] = struct{}{}
				}
				lastInputTokens = updatedLastInputTokens
				lastErr = targetResult.lastErr
				lastErrProvider = targetResult.lastErrProvider
				pendingRoundWait = mergePendingCoolingWait(pendingRoundWait, targetResult.pendingRoundWait)
				roundAttemptSummary.record(targetResult)
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
			_, exhausted := continuationExhaustedTargets[key]
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
		if isTerminalModelPoolFailureForProvider(lastErrProvider, lastErr) && !relayScopedRequestRejection(lastErrProvider, lastErr) {
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
		// A transient-only later round must not erase an earlier relay rejection
		// or restart its recovery budget.
		if withheldTerminalErr != nil {
			if time.Since(withheldTerminalSince) >= withheldTerminalFailureBudget {
				return nil, withheldTerminalErr
			}
			log.Infof("relay rejection withheld while sibling recovery budget remains provider=%v error=%v budget_left=%v", providerLogName(withheldTerminalProvider), withheldTerminalErr, withheldTerminalFailureBudget-time.Since(withheldTerminalSince))
		}
		// Advance the connect-failure ladder only while the round kept failing
		// before the server; a round that reached the wire — whatever its
		// outcome — restarts the ladder from the first rung. A round with no
		// wire evidence either way (every target failed key selection or is
		// cooling) holds the ladder: it neither advances nor restarts it.
		switch {
		case roundAttemptSummary.reachedServer:
			consecutiveConnectFailures = 0
		case roundAttemptSummary.connectPhaseFailure:
			consecutiveConnectFailures++
		}
		lastRoundConnectFailed = !roundAttemptSummary.reachedServer && roundAttemptSummary.connectPhaseFailure
		retryCount = nextRetryCount(retryCount, roundHadRequestAttempt, roundHadUsableReply, lastErr, hardCap)
	} // end attempts loop

	log.Errorf("LLM all retries exhausted max_attempts=%v provider=%v model=%v error=%v", maxAttempts, startProvider.Name(), startModelID, lastErr)
	return nil, lastErr
}
