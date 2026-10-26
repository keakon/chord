package agent

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/keakon/golog/log"
)

// interactionBroker owns the requestID→response-channel plumbing for the
// single-modal confirm and question flows. It encapsulates the two maps and
// four mutexes that previously lived directly on MainAgent, exposing a small
// register / await / resolve / clear surface so the lock that guards each map
// lives next to the map itself, and the await select logic (response /
// timeout / shutdown) is hidden behind a single method per flow.
//
// Locking discipline (preserved from the original MainAgent fields):
//   - confirmFlowMu / questionFlowMu serialize a whole flow because the TUI
//     supports a single modal dialog at a time.
//   - confirmMapMu / questionMapMu guard only the maps and are the *only* locks
//     taken by the resolve/clear path, so a resolving TUI goroutine never
//     blocks on a flow lock held by the waiting tool goroutine.
//   - confirmResolveMu serializes claiming a terminal ConfirmResolvedEvent with
//     its emission. It is not taken by resolveConfirm, resolveQuestion or
//     clearPending; only the waiter's terminal report and the cancellation
//     settlement use it, so neither can overtake an event the other already
//     claimed.
type interactionBroker struct {
	questionWaiting func() bool
	// stoppingCh mirrors MainAgent.stoppingCh: closed just before the event
	// loop exits so in-flight awaits unblock with ErrAgentShutdown.
	stoppingCh <-chan struct{}

	confirmFlow      chan struct{}
	confirmResolveMu sync.Mutex
	confirmMapMu     sync.Mutex
	confirmCh        map[string]chan ConfirmResponse
	confirmStart     map[string]time.Time
	// confirmTargets maps requestID -> the walltime owner captured at
	// registration (agent id + the session ledger/generation it belongs to).
	// It lives next to confirmCh/confirmStart under confirmMapMu so a concurrent
	// question flow (guarded by its own questionMapMu) can never race on the
	// same map.
	confirmTargets map[string]*walltimeTarget
	// confirmIdentities records the tool name and interaction owner each
	// registered confirm was announced with, so a turn cancellation can report
	// the terminal resolution with the identity the request was announced by.
	confirmIdentities map[string]confirmIdentity
	// confirmCtx records the waiter's context at registration. A turn cancellation
	// settles only confirms whose waiter context is already cancelled and whose
	// request was delivered: confirms parked by subagents or external workers —
	// whose contexts do not derive from the aborted turn — survive the abort and
	// keep waiting for a real answer. The context (not only Done) is kept so a
	// deadline can settle as timeout instead of being mistaken for cancellation.
	confirmCtx map[string]context.Context
	// confirmDelivered records requestIDs whose ConfirmRequestEvent was queued,
	// so a cancellation settles only confirmations a consumer actually saw; a
	// request whose delivery lost against the same cancellation is dropped
	// without a resolution event instead of reporting one for a dialog that
	// never opened.
	confirmDelivered map[string]bool
	// confirmResolvedClaimed records requestIDs whose terminal
	// ConfirmResolvedEvent emission a goroutine has claimed, so the unblocking
	// waiter and a turn cancellation emit it exactly once between them. Entries
	// are removed by unregisterConfirm; clearPending leaves them so a claimed
	// resolution cannot be claimed a second time after the mappings are dropped.
	confirmResolvedClaimed map[string]struct{}
	// confirmDecisions records the terminal decision the waiter and a turn
	// cancellation settle for requestID: the buffered answer when one arrived,
	// or the timeout/cancelled outcome the waiter selected. Recording it under
	// confirmMapMu makes the answer visible to the cancellation settlement
	// before the waiter wakes, so both sides report the same outcome and a late
	// answer cannot change a decision that was already made.
	confirmDecisions map[string]confirmDecision

	// handoffMapMu guards the handoff wait bookkeeping. Unlike confirm/question
	// there is no waiting goroutine: the handoff tool call completes before the
	// selector opens, so only the wait start and walltime owner are tracked and
	// settled when the user's decision arrives (approve/deny/cancel) or when the
	// wait is abandoned (session switch / shutdown).
	handoffMapMu   sync.Mutex
	handoffStart   map[string]time.Time
	handoffTargets map[string]*walltimeTarget

	// onSettled is invoked once per settled confirm/question wait (resolved,
	// timed out, cancelled, or cleared). target is the interaction's walltime
	// owner captured at registration; d is the wait wall clock.
	onSettled func(target *walltimeTarget, d time.Duration)
}

func newInteractionBroker(stoppingCh <-chan struct{}) *interactionBroker {
	b := &interactionBroker{
		confirmFlow:            make(chan struct{}, 1),
		stoppingCh:             stoppingCh,
		confirmCh:              make(map[string]chan ConfirmResponse),
		confirmStart:           make(map[string]time.Time),
		confirmTargets:         make(map[string]*walltimeTarget),
		confirmIdentities:      make(map[string]confirmIdentity),
		confirmCtx:             make(map[string]context.Context),
		confirmDelivered:       make(map[string]bool),
		confirmResolvedClaimed: make(map[string]struct{}),
		confirmDecisions:       make(map[string]confirmDecision),
		handoffStart:           make(map[string]time.Time),
		handoffTargets:         make(map[string]*walltimeTarget),
	}
	return b
}

// confirmIdentity names a registered confirm the way consumers saw it: the
// ConfirmRequestEvent and the terminal ConfirmResolvedEvent carry these fields.
type confirmIdentity struct {
	ToolName string
	AgentID  string
}

// confirmDecision is the terminal decision for a confirm request. resp is the
// answer the waiter returns; outcome names how the wait ended.
type confirmDecision struct {
	resp    ConfirmResponse
	outcome string
}

// setSettledHook installs the per-wait settlement callback (walltime recorder).
func (b *interactionBroker) setSettledHook(fn func(target *walltimeTarget, d time.Duration)) {
	b.confirmMapMu.Lock()
	b.onSettled = fn
	b.confirmMapMu.Unlock()
}

// settleWait closes a pending wait opened at start for the pinned target and
// invokes the settlement hook once after the wait actually started.
func (b *interactionBroker) settleWait(target *walltimeTarget, start time.Time) {
	if start.IsZero() {
		return
	}
	if b.onSettled != nil {
		b.onSettled(target, time.Since(start))
	}
}

// ---------------------------------------------------------------------------
// Handoff wait
// ---------------------------------------------------------------------------

// openHandoff records the wall-clock start of a user-visible handoff wait.
// The wait is settled by settleHandoff when the user decides, or by clearPending
// when the wait is abandoned (session switch / shutdown).
func (b *interactionBroker) openHandoff(requestID string, target *walltimeTarget) {
	if requestID == "" {
		return
	}
	b.handoffMapMu.Lock()
	b.handoffStart[requestID] = time.Now()
	b.handoffTargets[requestID] = target
	b.handoffMapMu.Unlock()
}

// settleHandoff closes a handoff wait once the user's decision arrives.
func (b *interactionBroker) settleHandoff(requestID string) {
	b.handoffMapMu.Lock()
	start, ok := b.handoffStart[requestID]
	delete(b.handoffStart, requestID)
	target := b.handoffTargets[requestID]
	delete(b.handoffTargets, requestID)
	b.handoffMapMu.Unlock()
	if ok {
		b.settleWait(target, start)
	}
}

// ---------------------------------------------------------------------------
// Confirm flow
// ---------------------------------------------------------------------------

// beginConfirmFlow serializes confirm flows; the caller must pair it with
// endConfirmFlow (typically via defer).
func (b *interactionBroker) beginConfirmFlow(ctx context.Context) error {
	select {
	case b.confirmFlow <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-b.confirmFlow
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-b.stoppingCh:
		return ErrAgentShutdown
	}
}
func (b *interactionBroker) endConfirmFlow() { <-b.confirmFlow }

// registerConfirm creates and registers a buffered response channel for the
// given requestID, recording the wait start, the identity the request was
// announced with, and the waiter's context. The caller must unregisterConfirm
// when the flow ends. A nil context means the wait has no cancellation source.
func (b *interactionBroker) registerConfirm(requestID string, target *walltimeTarget, identity confirmIdentity, ctx context.Context) chan ConfirmResponse {
	if ctx == nil {
		ctx = context.Background()
	}
	ch := make(chan ConfirmResponse, 1)
	now := time.Now()
	b.confirmMapMu.Lock()
	b.confirmCh[requestID] = ch
	b.confirmStart[requestID] = now
	b.confirmTargets[requestID] = target
	b.confirmIdentities[requestID] = identity
	b.confirmCtx[requestID] = ctx
	b.confirmMapMu.Unlock()
	return ch
}

// deliverConfirm queues the confirm request and records it as delivered under
// confirmResolveMu. A turn cancellation takes the same lock, so it either sees
// a delivered request or runs before the delivery started: it can never settle
// a request the consumer is still about to receive, nor miss one that went out
// before the cancellation. emit queues the request event.
func (b *interactionBroker) deliverConfirm(ctx context.Context, requestID string, emit func() error) error {
	b.confirmResolveMu.Lock()
	defer b.confirmResolveMu.Unlock()
	// A context cancelled before the send would make the delivery select race
	// its own ctx.Done case; aborting here keeps a request from going out after
	// the cancellation settlement already passed it by.
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := emit(); err != nil {
		return err
	}
	// The mark is part of the locked delivery: a cancellation settlement's scan
	// cannot land between the queued request and its delivered record.
	b.confirmMapMu.Lock()
	b.confirmDelivered[requestID] = true
	b.confirmMapMu.Unlock()
	return nil
}

// unregisterConfirm removes the requestID mapping and settles any still-open
// wait (timeout, cancellation, or shutdown path).
func (b *interactionBroker) unregisterConfirm(requestID string) {
	b.confirmMapMu.Lock()
	start, ok := b.confirmStart[requestID]
	delete(b.confirmCh, requestID)
	delete(b.confirmStart, requestID)
	target := b.confirmTargets[requestID]
	delete(b.confirmTargets, requestID)
	delete(b.confirmIdentities, requestID)
	delete(b.confirmCtx, requestID)
	delete(b.confirmDelivered, requestID)
	delete(b.confirmResolvedClaimed, requestID)
	delete(b.confirmDecisions, requestID)
	b.confirmMapMu.Unlock()
	if ok {
		b.settleWait(target, start)
	}
}

// awaitConfirm blocks until a response arrives on ch, the timeout fires
// (timeout <= 0 means wait indefinitely), ctx is cancelled, or shutdown
// begins. A timeout auto-denies. A context deadline is the same terminal state
// as the timer: callers that bound the wait both ways (the external permission
// queue) must report one deterministic timeout outcome instead of racing the
// deadline against the timer and sometimes reporting a user cancellation. The
// returned outcome names how the wait ended (ConfirmOutcomeApproved / Denied /
// Timeout / Cancelled) so the caller can report the terminal state; toolName is
// used only for the warning log.
//
// Before recording a timeout, cancellation, or shutdown outcome, a buffered
// answer is consumed first: resolveConfirm records the answer and sends it
// under confirmMapMu, so it may land between the terminal select and this
// point. The recorded decision wins over a later branch, keeping the returned
// response and the terminal ConfirmResolvedEvent consistent even when the
// user answers as the turn is cancelled. requestID names the registration the
// decision is recorded under.
func (b *interactionBroker) awaitConfirm(ctx context.Context, requestID string, ch <-chan ConfirmResponse, timeout time.Duration, toolName string) (ConfirmResponse, string, error) {
	finish := func(resp ConfirmResponse, outcome string, waitErr error) (ConfirmResponse, string, error) {
		decision := b.recordConfirmDecision(requestID, confirmDecision{resp: resp, outcome: outcome})
		if decision.outcome != ConfirmOutcomeCancelled {
			return decision.resp, decision.outcome, nil
		}
		if waitErr != nil {
			return decision.resp, decision.outcome, waitErr
		}
		return decision.resp, decision.outcome, context.Canceled
	}
	finishBuffered := func() (ConfirmResponse, string, error, bool) {
		resp, ok := receiveBufferedConfirm(ch)
		if !ok {
			return ConfirmResponse{}, "", nil, false
		}
		resp, outcome, err := finish(resp, confirmOutcomeFor(resp), nil)
		return resp, outcome, err, true
	}
	finishTimeout := func() (ConfirmResponse, string, error) {
		if resp, outcome, err, ok := finishBuffered(); ok {
			return resp, outcome, err
		}
		log.Warnf("tool confirmation timed out, auto-denying tool=%v timeout=%v", toolName, timeout)
		return finish(ConfirmResponse{Approved: false}, ConfirmOutcomeTimeout, nil)
	}
	finishCtxDone := func() (ConfirmResponse, string, error) {
		if resp, outcome, err, ok := finishBuffered(); ok {
			return resp, outcome, err
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			log.Warnf("tool confirmation deadline reached, auto-denying tool=%v", toolName)
			return finish(ConfirmResponse{Approved: false}, ConfirmOutcomeTimeout, nil)
		}
		return finish(ConfirmResponse{}, ConfirmOutcomeCancelled, ctx.Err())
	}
	finishStopping := func() (ConfirmResponse, string, error) {
		if resp, outcome, err, ok := finishBuffered(); ok {
			return resp, outcome, err
		}
		return finish(ConfirmResponse{}, ConfirmOutcomeCancelled, ErrAgentShutdown)
	}

	if timeout <= 0 {
		select {
		case resp := <-ch:
			return finish(resp, confirmOutcomeFor(resp), nil)
		case <-ctx.Done():
			return finishCtxDone()
		case <-b.stoppingCh:
			return finishStopping()
		}
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case resp := <-ch:
		return finish(resp, confirmOutcomeFor(resp), nil)
	case <-timer.C:
		if ctx.Err() != nil {
			return finishCtxDone()
		}
		return finishTimeout()
	case <-ctx.Done():
		return finishCtxDone()
	case <-b.stoppingCh:
		return finishStopping()
	}
}

// receiveBufferedConfirm consumes a response that is already queued without
// blocking. It lets a terminal select branch honor an answer that was sent but
// not chosen by the select before reporting timeout, cancellation, or shutdown.
func receiveBufferedConfirm(ch <-chan ConfirmResponse) (ConfirmResponse, bool) {
	select {
	case resp := <-ch:
		return resp, true
	default:
		return ConfirmResponse{}, false
	}
}

// recordConfirmDecision stores the first terminal decision for requestID and
// returns the effective one. A decision already recorded by the waiter or the
// cancellation settlement wins, so both sides report one outcome. The record is
// skipped once the request is unregistered.
func (b *interactionBroker) recordConfirmDecision(requestID string, decision confirmDecision) confirmDecision {
	b.confirmMapMu.Lock()
	defer b.confirmMapMu.Unlock()
	if _, ok := b.confirmCh[requestID]; !ok {
		return decision
	}
	if existing, ok := b.confirmDecisions[requestID]; ok {
		return existing
	}
	b.confirmDecisions[requestID] = decision
	return decision
}

func confirmOutcomeFor(resp ConfirmResponse) string {
	if resp.Approved {
		return ConfirmOutcomeApproved
	}
	return ConfirmOutcomeDenied
}

// resolveConfirm delivers resp to the waiter registered under requestID and
// records it as the terminal decision while holding confirmMapMu. It is a
// no-op if no waiter is registered (already resolved, cleared, or unknown),
// if a decision was already recorded (timeout or cancellation), or if the
// terminal resolution was already claimed by the waiter or a turn
// cancellation. The send is best-effort: the per-request channel is buffered.
// Recording the answer here, before the waiter wakes, lets a concurrent turn
// cancellation settlement see it and report the same outcome instead of
// emitting a cancellation the waiter would contradict.
func (b *interactionBroker) resolveConfirm(requestID string, resp ConfirmResponse) {
	b.confirmMapMu.Lock()
	defer b.confirmMapMu.Unlock()
	ch, ok := b.confirmCh[requestID]
	if !ok {
		return
	}
	if _, claimed := b.confirmResolvedClaimed[requestID]; claimed {
		return
	}
	if _, decided := b.confirmDecisions[requestID]; decided {
		return
	}
	select {
	case ch <- resp:
		b.confirmDecisions[requestID] = confirmDecision{resp: resp, outcome: confirmOutcomeFor(resp)}
	default:
	}
}

// emitConfirmResolvedOnce claims the exactly-once emission of the terminal
// ConfirmResolvedEvent for requestID and runs emit while holding
// confirmResolveMu. The loser between the unblocking waiter and a turn
// cancellation — in either direction — must skip its own copy. Holding the lock
// across emit means a cancellation that has to close the turn out waits for an
// emission the waiter already claimed before it can queue the cancellation
// event and the idle that follows.
func (b *interactionBroker) emitConfirmResolvedOnce(requestID string, emit func()) {
	b.confirmResolveMu.Lock()
	defer b.confirmResolveMu.Unlock()
	if !b.claimConfirmResolvedLocked(requestID) {
		return
	}
	emit()
}

// claimConfirmResolvedLocked records requestID as claimed. The caller holds
// confirmResolveMu, which keeps the claim and its emission ordered.
func (b *interactionBroker) claimConfirmResolvedLocked(requestID string) bool {
	b.confirmMapMu.Lock()
	defer b.confirmMapMu.Unlock()
	if _, claimed := b.confirmResolvedClaimed[requestID]; claimed {
		return false
	}
	b.confirmResolvedClaimed[requestID] = struct{}{}
	return true
}

// emitCancelledConfirmResolutions claims and emits the terminal resolution for
// every pending confirm that a cancellation must report: the waiter's context
// is already cancelled (so the confirm cannot outlive the abort it is being
// settled for) and the request was delivered (so a resolution does not describe
// a dialog that never opened). A confirm the user answered just before the
// cancellation reports its approved/denied decision; an unanswered one reports
// cancellation. Confirms whose waiter context is still live —
// typically parked by a subagent or external worker whose context does not
// derive from the aborted turn — stay pending and keep waiting for a real
// answer. A turn cancellation calls this before its cancellation event is
// queued, so the emitted ConfirmResolvedEvents precede the idle that follows
// the turn; a waiter unblocked by the same cancellation either completes its
// already-claimed emission first or loses the claim and reports nothing.
func (b *interactionBroker) emitCancelledConfirmResolutions(emit func(requestID string, identity confirmIdentity, outcome string)) {
	b.confirmResolveMu.Lock()
	defer b.confirmResolveMu.Unlock()
	for requestID, resolution := range b.claimCancelledConfirmsLocked() {
		emit(requestID, resolution.identity, resolution.decision.outcome)
	}
}

// confirmResolution is the captured identity and terminal decision a turn
// cancellation reports for one confirm.
type confirmResolution struct {
	identity confirmIdentity
	decision confirmDecision
}

// claimCancelledConfirmsLocked claims every pending confirm that has no
// terminal resolution yet, whose waiter context is already cancelled, and whose
// request was delivered. A confirm whose answer was already buffered keeps that
// answer as its decision; one with no decision is settled as cancelled. A
// context deadline is settled as timeout with the same auto-deny response the
// waiter would produce, rather than recording a cancellation the client cannot
// reconcile with its own deadline. The caller holds confirmResolveMu.
func (b *interactionBroker) claimCancelledConfirmsLocked() map[string]confirmResolution {
	b.confirmMapMu.Lock()
	defer b.confirmMapMu.Unlock()
	claimed := make(map[string]confirmResolution)
	for requestID, identity := range b.confirmIdentities {
		if _, done := b.confirmResolvedClaimed[requestID]; done {
			continue
		}
		ctx := b.confirmCtx[requestID]
		select {
		case <-ctx.Done():
		default:
			// The waiter's context is still live: the confirm outlives this
			// abort and keeps waiting for a real answer.
			continue
		}
		if !b.confirmDelivered[requestID] {
			// The request never reached a consumer, so there is nothing to
			// resolve; the waiter drops it through its own cancelled context.
			continue
		}
		decision, ok := b.confirmDecisions[requestID]
		if !ok {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				decision = confirmDecision{resp: ConfirmResponse{Approved: false}, outcome: ConfirmOutcomeTimeout}
			} else {
				decision = confirmDecision{outcome: ConfirmOutcomeCancelled}
			}
			b.confirmDecisions[requestID] = decision
		}
		b.confirmResolvedClaimed[requestID] = struct{}{}
		claimed[requestID] = confirmResolution{identity: identity, decision: decision}
	}
	return claimed
}

// ---------------------------------------------------------------------------
// Shared
// ---------------------------------------------------------------------------

// clearPending removes all in-flight confirm/question/handoff request mappings.
// It does not close the per-request channels; waiters exit via ctx cancellation
// or stoppingCh during shutdown. The confirm and handoff waits are settled
// (counted as user wait) before the mappings are dropped so a shutdown or
// session switch never leaks an open segment; a question wait is settled by its
// own waiter, which clearPending wakes with a closed entry rather than counting
// the segment here. Settlement appends to the usage ledger, which can block on
// the persistence pump or write to disk, so it runs after the map locks are
// released: resolveConfirm and registerConfirm contend for the same locks.
func (b *interactionBroker) clearPending() {
	type pendingWait struct {
		target *walltimeTarget
		start  time.Time
	}
	var pending []pendingWait

	b.confirmMapMu.Lock()
	for requestID, start := range b.confirmStart {
		pending = append(pending, pendingWait{target: b.confirmTargets[requestID], start: start})
	}
	clear(b.confirmCh)
	clear(b.confirmStart)
	clear(b.confirmTargets)
	clear(b.confirmIdentities)
	clear(b.confirmCtx)
	clear(b.confirmDelivered)
	clear(b.confirmDecisions)
	// confirmResolvedClaimed is left alone: a resolution already claimed by the
	// turn-cancellation path must stay claimed until the waiter's unregister,
	// or the waiter would emit a second copy after these mappings are dropped.
	b.confirmMapMu.Unlock()

	b.handoffMapMu.Lock()
	for requestID, start := range b.handoffStart {
		pending = append(pending, pendingWait{target: b.handoffTargets[requestID], start: start})
	}
	clear(b.handoffStart)
	clear(b.handoffTargets)
	b.handoffMapMu.Unlock()

	for _, wait := range pending {
		b.settleWait(wait.target, wait.start)
	}
}

// hasPendingUserInteraction reports whether the main agent is currently
// waiting on a user-facing modal interaction (confirm, question, or handoff).
// While such a dialog is open the loop dispatches no new input, so delegated
// workers legitimately stay silent; the coordination stall sweep consults this
// to avoid mislabelling the wait as a worker stall.
func (b *interactionBroker) hasPendingUserInteraction() bool {
	if b == nil {
		return false
	}
	if b.questionWaiting != nil && b.questionWaiting() {
		return true
	}
	b.confirmMapMu.Lock()
	pending := len(b.confirmStart) > 0
	b.confirmMapMu.Unlock()
	if pending {
		return true
	}

	b.handoffMapMu.Lock()
	defer b.handoffMapMu.Unlock()
	return len(b.handoffStart) > 0
}

func makeRequestID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		log.Warnf("request ID generation failed, using fallback err=%v", err)
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", buf[:])
}
