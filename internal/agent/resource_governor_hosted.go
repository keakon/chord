package agent

import (
	"context"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
)

type hostedRequestStart struct {
	waiter *llmRequestWaiter
	at     time.Time
}

// All fields are protected by resourceGovernor.llmMu. Pending reservations
// prevent oversubscription; dispatched failures still count in rate windows.
type hostedAdmission struct {
	activeLimits  map[string]int
	requestLimits map[string]int
	retryLimits   map[string]int
	active        map[string]int
	requests      map[string][]hostedRequestStart
	retries       map[string][]hostedRequestStart
}

func newHostedAdmission(cfg config.OrchestrationConfig) hostedAdmission {
	return hostedAdmission{
		activeLimits:  normalizedProviderLimits(cfg.ProviderMaxActiveHostedRequests),
		requestLimits: normalizedProviderLimits(cfg.ProviderHostedRequestsPerMinute),
		retryLimits:   normalizedProviderLimits(cfg.ProviderHostedRetriesPerMinute),
		active:        make(map[string]int),
		requests:      make(map[string][]hostedRequestStart),
		retries:       make(map[string][]hostedRequestStart),
	}
}

func normalizedProviderLimits(limits map[string]int) map[string]int {
	out := make(map[string]int, len(limits))
	for provider, limit := range limits {
		if provider = strings.TrimSpace(provider); provider != "" && limit > 0 {
			out[provider] = limit
		}
	}
	return out
}

func (g *resourceGovernor) acquireHostedLLM(ctx context.Context, ref string, retry bool) (*llmRequestReservation, error) {
	return g.acquireRequest(ctx, ref, true, retry)
}

func pruneHostedStarts(starts []hostedRequestStart, now time.Time) []hostedRequestStart {
	return slices.DeleteFunc(starts, func(start hostedRequestStart) bool {
		return !start.at.IsZero() && !start.at.Add(time.Minute).After(now)
	})
}

func hostedWindowRetryAt(starts []hostedRequestStart, now time.Time) time.Time {
	for _, start := range starts {
		if !start.at.IsZero() {
			return start.at.Add(time.Minute)
		}
	}
	return now.Add(time.Minute)
}

func (h *hostedAdmission) check(w *llmRequestWaiter, now time.Time) error {
	if !w.hosted {
		return nil
	}
	if limit := h.retryLimits[w.provider]; w.retry && limit > 0 {
		starts := pruneHostedStarts(h.retries[w.provider], now)
		h.retries[w.provider] = starts
		if len(starts) >= limit {
			return &llm.HostedAdmissionError{RetryAt: hostedWindowRetryAt(starts, now), RetryBudget: true}
		}
	}
	if limit := h.requestLimits[w.provider]; limit > 0 {
		starts := pruneHostedStarts(h.requests[w.provider], now)
		h.requests[w.provider] = starts
		if len(starts) >= limit {
			return &llm.HostedAdmissionError{RetryAt: hostedWindowRetryAt(starts, now)}
		}
	}
	return nil
}

func (h *hostedAdmission) start(w *llmRequestWaiter) {
	if !w.hosted {
		return
	}
	h.active[w.provider]++
	start := hostedRequestStart{waiter: w}
	if h.requestLimits[w.provider] > 0 {
		h.requests[w.provider] = append(h.requests[w.provider], start)
	}
	if w.retry && h.retryLimits[w.provider] > 0 {
		h.retries[w.provider] = append(h.retries[w.provider], start)
	}
}

func (h *hostedAdmission) sent(w *llmRequestWaiter, now time.Time) {
	if !w.hosted || w.dispatched {
		return
	}
	w.dispatched = true
	commit := func(starts []hostedRequestStart) []hostedRequestStart {
		starts = slices.DeleteFunc(starts, func(s hostedRequestStart) bool { return s.waiter == w })
		return append(starts, hostedRequestStart{waiter: w, at: now})
	}
	if h.requestLimits[w.provider] > 0 {
		h.requests[w.provider] = commit(h.requests[w.provider])
	}
	if w.retry && h.retryLimits[w.provider] > 0 {
		h.retries[w.provider] = commit(h.retries[w.provider])
	}
}

func (h *hostedAdmission) release(w *llmRequestWaiter, cancelledBeforeDispatch bool) {
	if !w.hosted {
		return
	}
	h.active[w.provider]--
	if cancelledBeforeDispatch && !w.dispatched {
		match := func(s hostedRequestStart) bool { return s.waiter == w }
		h.requests[w.provider] = slices.DeleteFunc(h.requests[w.provider], match)
		h.retries[w.provider] = slices.DeleteFunc(h.retries[w.provider], match)
	} else {
		// An adapter without evidence may already have sent the request.
		h.sent(w, time.Now())
	}
}

func mergeHostedAdmissionConfig(out *config.OrchestrationConfig, override config.OrchestrationConfig) {
	merge := func(base, extra map[string]int) map[string]int {
		result := make(map[string]int, len(base)+len(extra))
		maps.Copy(result, base)
		maps.Copy(result, extra)
		return result
	}
	out.ProviderMaxActiveHostedRequests = merge(out.ProviderMaxActiveHostedRequests, override.ProviderMaxActiveHostedRequests)
	out.ProviderHostedRequestsPerMinute = merge(out.ProviderHostedRequestsPerMinute, override.ProviderHostedRequestsPerMinute)
	out.ProviderHostedRetriesPerMinute = merge(out.ProviderHostedRetriesPerMinute, override.ProviderHostedRetriesPerMinute)
}
