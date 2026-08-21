package agent

import (
	"context"
	"crypto/rand"
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

	questionFlowMu sync.Mutex
	questionMapMu  sync.Mutex
	questionCh     map[string]chan QuestionResponse
	questionStart  map[string]time.Time
	// questionTargets is the question-flow counterpart of confirmTargets,
	// guarded by questionMapMu only.
	questionTargets map[string]*walltimeTarget

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
	return &interactionBroker{
		stoppingCh:      stoppingCh,
		confirmCh:       make(map[string]chan ConfirmResponse),
		confirmStart:    make(map[string]time.Time),
		confirmTargets:  make(map[string]*walltimeTarget),
		questionCh:      make(map[string]chan QuestionResponse),
		questionStart:   make(map[string]time.Time),
		questionTargets: make(map[string]*walltimeTarget),
		handoffStart:    make(map[string]time.Time),
		handoffTargets:  make(map[string]*walltimeTarget),
	}
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

// beginQuestionFlow serializes question flows; the caller must pair it with
// endQuestionFlow (typically via defer).
func (b *interactionBroker) beginQuestionFlow() { b.questionFlowMu.Lock() }
func (b *interactionBroker) endQuestionFlow()   { b.questionFlowMu.Unlock() }

func (b *interactionBroker) registerQuestion(requestID string, target *walltimeTarget) chan QuestionResponse {
	ch := make(chan QuestionResponse, 1)
	now := time.Now()
	b.questionMapMu.Lock()
	b.questionCh[requestID] = ch
	b.questionStart[requestID] = now
	b.questionTargets[requestID] = target
	b.questionMapMu.Unlock()
	return ch
}

func (b *interactionBroker) unregisterQuestion(requestID string) {
	b.questionMapMu.Lock()
	start, ok := b.questionStart[requestID]
	delete(b.questionCh, requestID)
	delete(b.questionStart, requestID)
	target := b.questionTargets[requestID]
	delete(b.questionTargets, requestID)
	b.questionMapMu.Unlock()
	if ok {
		b.settleWait(target, start)
	}
}

// awaitQuestion blocks until a response arrives on ch, the timeout fires
// (timeout <= 0 means wait indefinitely), ctx is cancelled, or shutdown
// begins. Unlike confirm, a timeout is an error rather than an auto-answer.
func (b *interactionBroker) awaitQuestion(ctx context.Context, ch <-chan QuestionResponse, timeout time.Duration) (QuestionResponse, error) {
	if timeout <= 0 {
		select {
		case resp := <-ch:
			return resp, nil
		case <-ctx.Done():
			return QuestionResponse{}, ctx.Err()
		case <-b.stoppingCh:
			return QuestionResponse{}, ErrAgentShutdown
		}
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case resp := <-ch:
		return resp, nil
	case <-timer.C:
		return QuestionResponse{}, fmt.Errorf("question timed out after %s", timeout)
	case <-ctx.Done():
		return QuestionResponse{}, ctx.Err()
	case <-b.stoppingCh:
		return QuestionResponse{}, ErrAgentShutdown
	}
}

func (b *interactionBroker) resolveQuestion(requestID string, resp QuestionResponse) {
	b.questionMapMu.Lock()
	ch, ok := b.questionCh[requestID]
	b.questionMapMu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- resp:
	default:
	}
}

// ---------------------------------------------------------------------------
// Shared
// ---------------------------------------------------------------------------

// clearPending removes all in-flight confirm/question/handoff request mappings. It does
// not close the per-request channels; waiters exit via ctx cancellation or
// stoppingCh during shutdown. Open waits are settled (counted as user wait)
// before the mappings are dropped so a shutdown/session-switch never leaks an
// open segment. Settlement appends to the usage ledger, which can block on the
// persistence pump or write to disk, so it runs after the map locks are
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
	for requestID, start := range b.questionStart {
		pending = append(pending, pendingWait{target: b.questionTargets[requestID], start: start})
	}
	clear(b.questionCh)
	clear(b.questionStart)
	clear(b.questionTargets)
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

func makeRequestID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		log.Warnf("request ID generation failed, using fallback err=%v", err)
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", buf[:])
}
