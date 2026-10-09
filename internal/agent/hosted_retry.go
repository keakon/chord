package agent

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// runPool owns pool rounds; each target's client owns only its key traversal.
// A cooling target must not delay other ready targets in the same round.
func (b *hostedBackend) runPool(ctx context.Context, caller *hostedCaller, spec tools.HostedToolSpec, plan hostedRoutePlan, args map[string]any) (*message.HostedObservation, error) {
	targets := b.preferredTargets(plan, spec.Name)
	failures := make([]hostedTargetFailure, len(targets))
	readyAt := make([]time.Time, len(targets))
	attempted := make([]bool, len(targets))
	rounds := 1
	if spec.RetrySafe {
		rounds = hostedToolRetryRounds
	}
	for round := 0; round < rounds; round++ {
		pending := make([]bool, len(targets))
		for i := range targets {
			pending[i] = round == 0 || llm.HostedFailureAllowsRetryRound(failures[i].cause)
		}
		for {
			if err := ctx.Err(); err != nil {
				return nil, fmt.Errorf("%s cancelled: %w", spec.Name, err)
			}
			var next time.Time
			for i, target := range targets {
				if !pending[i] {
					continue
				}
				if time.Until(readyAt[i]) > 0 {
					if next.IsZero() || readyAt[i].Before(next) {
						next = readyAt[i]
					}
					continue
				}
				pending[i] = false
				caller.retryRound = attempted[i]
				key := hostedTargetHealthKey(plan, spec.Name, target)
				generation, probeErr := b.acquireHostedProbe(key)
				if probeErr != nil {
					failures[i] = hostedTargetFailure{target: hostedTargetRef(target), cause: probeErr}
					continue
				}
				before := caller.requests
				obs, err := b.runTarget(ctx, caller, target, spec, args)
				attempted[i] = attempted[i] || caller.requests > before
				b.finishHostedProbe(caller, key, generation, err)
				if err == nil {
					b.rememberTarget(caller, spec, plan, spec.Name, target)
					return obs, nil
				}
				if ctxErr := ctx.Err(); ctxErr != nil {
					return nil, fmt.Errorf("%s cancelled: %w", spec.Name, ctxErr)
				}
				if _, approval := errors.AsType[*hostedApprovalRequiredError](err); approval {
					return nil, err
				}
				if _, unknown := errors.AsType[*llm.HostedOutcomeUnknownError](err); unknown {
					return nil, err
				}
				if hostedInputLevelError(err) {
					return nil, fmt.Errorf("%s request was rejected before running: %w", spec.Name, err)
				}
				failures[i] = hostedTargetFailure{target: hostedTargetRef(target), cause: err}
				readyAt[i] = hostedRetryAt(target.ProviderConfig, err, round+1)
			}
			if next.IsZero() {
				break
			}
			timer := time.NewTimer(time.Until(next))
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, fmt.Errorf("%s cancelled: %w", spec.Name, ctx.Err())
			case <-timer.C:
			}
		}
	}
	return nil, newHostedAllTargetsFailedError(spec.Name, failures)
}

func hostedRetryAt(provider *llm.ProviderConfig, err error, round int) time.Time {
	if admission, ok := errors.AsType[*llm.HostedAdmissionError](err); ok {
		return admission.RetryAt
	}
	delay := provider.GetRetryDelay(round)
	// Jitter applies only to generated backoff, never to the server's deadline.
	delay = time.Duration(float64(delay) * (0.9 + rand.Float64()*0.2))
	if cooling, ok := errors.AsType[*llm.AllKeysCoolingError](err); ok {
		delay = max(delay, cooling.RetryAfter)
	} else if apiErr, ok := errors.AsType[*llm.APIError](err); ok {
		delay = max(delay, llm.RetryAfterForProvider(provider, apiErr))
	}
	return time.Now().Add(delay)
}
