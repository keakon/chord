package agent

import "time"

// A reservation holds capacity and rate quota while a provider builds its
// request. Only positive not-sent evidence refunds the rate quota.
type llmRequestReservation struct {
	governor *resourceGovernor
	waiter   *llmRequestWaiter
}

func (r *llmRequestReservation) markSent() {
	if r == nil || r.governor == nil {
		return
	}
	r.governor.llmMu.Lock()
	defer r.governor.llmMu.Unlock()
	if !r.waiter.released.Load() {
		r.governor.hosted.sent(r.waiter, time.Now())
	}
}

func (r *llmRequestReservation) release(notSent bool) {
	if r != nil && r.governor != nil && r.waiter.released.CompareAndSwap(false, true) {
		r.governor.releaseLLM(r.waiter, notSent)
	}
}
