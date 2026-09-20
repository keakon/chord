package agent

import (
	"context"
	"crypto/rand"
	"fmt"
	"sync"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/tools"
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
type interactionBroker struct {
	// stoppingCh mirrors MainAgent.stoppingCh: closed just before the event
	// loop exits so in-flight awaits unblock with ErrAgentShutdown.
	stoppingCh <-chan struct{}

	confirmFlowMu sync.Mutex
	confirmMapMu  sync.Mutex
	confirmCh     map[string]chan ConfirmResponse
	confirmStart  map[string]time.Time
	// confirmTargets maps requestID -> the walltime owner captured at
	// registration (agent id + the session ledger/generation it belongs to).
	// It lives next to confirmCh/confirmStart under confirmMapMu so a concurrent
	// question flow (guarded by its own questionMapMu) can never race on the
	// same map.
	confirmTargets map[string]*walltimeTarget

	// questionAdmit is a one-slot semaphore serializing whole question batches.
	// Acquiring means receiving from it; releasing means sending it back.
	// Unlike a plain Mutex this is cancellable, so a waiter can abandon the
	// queue when its context is cancelled or shutdown begins.
	questionAdmit chan struct{}
	questionMapMu sync.Mutex
	// questionPending holds the in-flight question requests. Each entry carries
	// its own terminal state so the winning resolver is decided atomically by
	// terminateQuestion instead of by a select branch.
	questionPending map[string]*questionEntry

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
		stoppingCh:      stoppingCh,
		confirmCh:       make(map[string]chan ConfirmResponse),
		confirmStart:    make(map[string]time.Time),
		confirmTargets:  make(map[string]*walltimeTarget),
		questionAdmit:   make(chan struct{}, 1),
		questionPending: make(map[string]*questionEntry),
		handoffStart:    make(map[string]time.Time),
		handoffTargets:  make(map[string]*walltimeTarget),
	}
	b.questionAdmit <- struct{}{}
	return b
}

// setSettledHook installs the per-wait settlement callback (walltime recorder).
func (b *interactionBroker) setSettledHook(fn func(target *walltimeTarget, d time.Duration)) {
	b.confirmMapMu.Lock()
	b.questionMapMu.Lock()
	b.onSettled = fn
	b.questionMapMu.Unlock()
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
func (b *interactionBroker) beginConfirmFlow() { b.confirmFlowMu.Lock() }
func (b *interactionBroker) endConfirmFlow()   { b.confirmFlowMu.Unlock() }

// registerConfirm creates and registers a buffered response channel for the
// given requestID, recording the wait start. The caller must
// unregisterConfirm when the flow ends.
func (b *interactionBroker) registerConfirm(requestID string, target *walltimeTarget) chan ConfirmResponse {
	ch := make(chan ConfirmResponse, 1)
	now := time.Now()
	b.confirmMapMu.Lock()
	b.confirmCh[requestID] = ch
	b.confirmStart[requestID] = now
	b.confirmTargets[requestID] = target
	b.confirmMapMu.Unlock()
	return ch
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
	b.confirmMapMu.Unlock()
	if ok {
		b.settleWait(target, start)
	}
}

// awaitConfirm blocks until a response arrives on ch, the timeout fires
// (timeout <= 0 means wait indefinitely), ctx is cancelled, or shutdown
// begins. A timeout auto-denies; toolName is used only for the warning log.
func (b *interactionBroker) awaitConfirm(ctx context.Context, ch <-chan ConfirmResponse, timeout time.Duration, toolName string) (ConfirmResponse, error) {
	if timeout <= 0 {
		select {
		case resp := <-ch:
			return resp, nil
		case <-ctx.Done():
			return ConfirmResponse{}, ctx.Err()
		case <-b.stoppingCh:
			return ConfirmResponse{}, ErrAgentShutdown
		}
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case resp := <-ch:
		return resp, nil
	case <-timer.C:
		log.Warnf("tool confirmation timed out, auto-denying tool=%v timeout=%v", toolName, timeout)
		return ConfirmResponse{Approved: false}, nil
	case <-ctx.Done():
		return ConfirmResponse{}, ctx.Err()
	case <-b.stoppingCh:
		return ConfirmResponse{}, ErrAgentShutdown
	}
}

// resolveConfirm delivers resp to the waiter registered under requestID. It is
// a no-op if no waiter is registered (already resolved, cleared, or unknown),
// and never blocks: the per-request channel is buffered and the send is
// best-effort.
func (b *interactionBroker) resolveConfirm(requestID string, resp ConfirmResponse) {
	b.confirmMapMu.Lock()
	ch, ok := b.confirmCh[requestID]
	b.confirmMapMu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- resp:
	default:
	}
}

// ---------------------------------------------------------------------------
// Question flow
// ---------------------------------------------------------------------------

// questionEntry is the broker-side state of one in-flight question request.
// reason and answers are written once under questionMapMu before done closes;
// a waiter reads them only after done closes, which orders the writes.
type questionEntry struct {
	done     chan struct{}
	deadline time.Time
	reason   string
	answers  []string
	target   *walltimeTarget
	start    time.Time
}

// acquireQuestionFlow takes the one-slot question batch lock. Unlike the
// confirm flow lock it is cancellable: a waiter can give up when ctx is
// cancelled or shutdown begins instead of blocking behind a question batch that
// may wait forever. The returned release must be called exactly once.
func (b *interactionBroker) acquireQuestionFlow(ctx context.Context) (func(), error) {
	select {
	case <-b.questionAdmit:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-b.stoppingCh:
		return nil, ErrAgentShutdown
	}
	// A cancellation can be ready in the same select tick as the slot, so
	// re-check before the caller registers a request that is already dead.
	if err := ctx.Err(); err != nil {
		b.releaseQuestionFlow()
		return nil, err
	}
	select {
	case <-b.stoppingCh:
		b.releaseQuestionFlow()
		return nil, ErrAgentShutdown
	default:
	}
	return b.releaseQuestionFlow, nil
}

func (b *interactionBroker) releaseQuestionFlow() {
	b.questionAdmit <- struct{}{}
}

// registerQuestion creates the pending state for one question. deadline is the
// absolute time the request must resolve by, or the zero value to wait
// indefinitely. It is fixed at registration so it covers the request send and
// any client queueing, not just the answer wait.
func (b *interactionBroker) registerQuestion(requestID string, deadline time.Time, target *walltimeTarget) *questionEntry {
	entry := &questionEntry{
		done:     make(chan struct{}),
		deadline: deadline,
		target:   target,
		start:    time.Now(),
	}
	b.questionMapMu.Lock()
	b.questionPending[requestID] = entry
	b.questionMapMu.Unlock()
	return entry
}

// abortQuestion drops a request whose send failed before the client ever saw
// it, settling its wait once. No awaitQuestion waiter exists on this path, so
// settling here is what keeps the wait from leaking; every other close leaves
// settlement to that waiter. No terminal state or resolved event is produced
// because no request reached the client.
func (b *interactionBroker) abortQuestion(requestID string) {
	b.questionMapMu.Lock()
	entry, ok := b.questionPending[requestID]
	if ok {
		delete(b.questionPending, requestID)
	}
	b.questionMapMu.Unlock()
	if ok {
		b.settleWait(entry.target, entry.start)
	}
}

// terminateQuestion atomically decides the terminal state for requestID. The
// first caller wins; later callers are no-ops. A client response that lands at
// or after the deadline never decides the outcome, whether it carried a
// selection or a refusal: it is closed as no_response instead, so a late
// response cannot beat the deadline just because the timer has not fired yet.
// It returns the winning reason, answers, and whether this call decided the
// state.
//
// The terminal state is written before done is closed; a waiter that observes
// the closed channel reads those fields without the lock. Settlement stays with
// that waiter (see awaitQuestion), so a resolver on the TUI or headless command
// path never waits on the usage ledger.
func (b *interactionBroker) terminateQuestion(requestID, reason string, answers []string) (string, []string, bool) {
	b.questionMapMu.Lock()
	entry, ok := b.questionPending[requestID]
	if !ok {
		b.questionMapMu.Unlock()
		return "", nil, false
	}
	switch reason {
	case tools.QuestionOutcomeAnswered, tools.QuestionOutcomeDeclined:
		if !entry.deadline.IsZero() && !time.Now().Before(entry.deadline) {
			reason = tools.QuestionOutcomeNoResponse
			answers = nil
		}
	}
	entry.reason = reason
	entry.answers = answers
	delete(b.questionPending, requestID)
	close(entry.done)
	b.questionMapMu.Unlock()
	return reason, answers, true
}

// awaitQuestion waits for the request's terminal state and settles the wait
// once it ends. The deadline timer and the ctx/shutdown paths only decide that
// state atomically; the reason and answers are always read back from the entry,
// so a resolving client and the timer can never both decide the result. A
// system-close reason is returned with a matching error so callers never
// mistake it for a normal answer.
func (b *interactionBroker) awaitQuestion(ctx context.Context, entry *questionEntry, requestID string) (string, []string, error) {
	// Settlement can block on the usage ledger's persistence pump, so it runs
	// here on the waiting tool goroutine rather than on whichever TUI or
	// headless command path decided the state.
	defer b.settleWait(entry.target, entry.start)

	var timerC <-chan time.Time
	if !entry.deadline.IsZero() {
		timer := time.NewTimer(time.Until(entry.deadline))
		defer timer.Stop()
		timerC = timer.C
	}
	select {
	case <-entry.done:
		return entry.reason, entry.answers, questionCloseError(entry.reason, nil)
	case <-timerC:
		b.terminateQuestion(requestID, tools.QuestionOutcomeNoResponse, nil)
		return entry.reason, entry.answers, questionCloseError(entry.reason, nil)
	case <-ctx.Done():
		b.terminateQuestion(requestID, QuestionResolvedReasonCancelled, nil)
		return entry.reason, entry.answers, questionCloseError(entry.reason, ctx.Err())
	case <-b.stoppingCh:
		b.terminateQuestion(requestID, QuestionResolvedReasonError, nil)
		return entry.reason, entry.answers, questionCloseError(entry.reason, ErrAgentShutdown)
	}
}

// questionCloseError maps a terminal reason to the error the question flow
// reports. The four normal outcomes return nil; a system close returns the
// cause when there is one, or a synthetic error when the wait was settled by
// clearPending during a session switch or shutdown.
func questionCloseError(reason string, cause error) error {
	switch reason {
	case tools.QuestionOutcomeAnswered, tools.QuestionOutcomeDeclined,
		tools.QuestionOutcomeNoResponse, tools.QuestionOutcomeSuperseded:
		return nil
	}
	if cause != nil {
		return cause
	}
	if reason == QuestionResolvedReasonCancelled {
		return fmt.Errorf("question cancelled")
	}
	return fmt.Errorf("question flow closed: %w", ErrAgentShutdown)
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
	b.confirmMapMu.Unlock()

	b.questionMapMu.Lock()
	for requestID, entry := range b.questionPending {
		// Wake the waiter with a definite reason so it can emit the matching
		// resolved event, rather than depending on a select branch that might
		// not observe the dropped map. Settling the wait is that waiter's job,
		// so the segment is not counted twice.
		entry.reason = QuestionResolvedReasonCancelled
		entry.answers = nil
		delete(b.questionPending, requestID)
		close(entry.done)
	}
	b.questionMapMu.Unlock()

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
	b.confirmMapMu.Lock()
	pending := len(b.confirmStart) > 0
	b.confirmMapMu.Unlock()
	if pending {
		return true
	}
	b.questionMapMu.Lock()
	pending = len(b.questionPending) > 0
	b.questionMapMu.Unlock()
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
