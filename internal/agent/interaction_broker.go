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

	questionFlowMu sync.Mutex
	questionMapMu  sync.Mutex
	questionCh     map[string]chan QuestionResponse
}

func newInteractionBroker(stoppingCh <-chan struct{}) *interactionBroker {
	return &interactionBroker{
		stoppingCh: stoppingCh,
		confirmCh:  make(map[string]chan ConfirmResponse),
		questionCh: make(map[string]chan QuestionResponse),
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
// given requestID. The caller must unregisterConfirm when the flow ends.
func (b *interactionBroker) registerConfirm(requestID string) chan ConfirmResponse {
	ch := make(chan ConfirmResponse, 1)
	b.confirmMapMu.Lock()
	b.confirmCh[requestID] = ch
	b.confirmMapMu.Unlock()
	return ch
}

func (b *interactionBroker) unregisterConfirm(requestID string) {
	b.confirmMapMu.Lock()
	delete(b.confirmCh, requestID)
	b.confirmMapMu.Unlock()
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

func (b *interactionBroker) registerQuestion(requestID string) chan QuestionResponse {
	ch := make(chan QuestionResponse, 1)
	b.questionMapMu.Lock()
	b.questionCh[requestID] = ch
	b.questionMapMu.Unlock()
	return ch
}

func (b *interactionBroker) unregisterQuestion(requestID string) {
	b.questionMapMu.Lock()
	delete(b.questionCh, requestID)
	b.questionMapMu.Unlock()
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

// clearPending removes all in-flight confirm/question request mappings. It does
// not close the per-request channels; waiters exit via ctx cancellation or
// stoppingCh during shutdown.
func (b *interactionBroker) clearPending() {
	b.confirmMapMu.Lock()
	clear(b.confirmCh)
	b.confirmMapMu.Unlock()

	b.questionMapMu.Lock()
	clear(b.questionCh)
	b.questionMapMu.Unlock()
}

func makeRequestID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		log.Warnf("request ID generation failed, using fallback err=%v", err)
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", buf[:])
}
