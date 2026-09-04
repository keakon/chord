package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

type fallbackModelDownshiftCompactionPendingError struct {
	planID           uint64
	selectedModelRef string
	runningModelRef  string
}

func (e *fallbackModelDownshiftCompactionPendingError) Error() string {
	if e == nil {
		return "fallback model downshift compaction pending"
	}
	return fmt.Sprintf("fallback model downshift compaction pending: model=%s plan_id=%d", e.runningModelRef, e.planID)
}

func isFallbackModelDownshiftCompactionPending(err error) bool {
	_, ok := errors.AsType[*fallbackModelDownshiftCompactionPendingError](err)
	return ok
}

type fallbackDownshiftBypassContextKey struct{}

func withFallbackDownshiftCompactionBypass(ctx context.Context) context.Context {
	return context.WithValue(ctx, fallbackDownshiftBypassContextKey{}, true)
}

func fallbackDownshiftCompactionBypass(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	enabled, _ := ctx.Value(fallbackDownshiftBypassContextKey{}).(bool)
	return enabled
}

func fallbackModelDisplayRef(fallback llm.FallbackModel) string {
	ref := ""
	if fallback.ProviderConfig != nil {
		ref = fallback.ProviderConfig.Name()
	}
	if ref != "" && fallback.ModelID != "" {
		ref += "/" + fallback.ModelID
	} else if fallback.ModelID != "" {
		ref = fallback.ModelID
	}
	if fallback.Variant != "" && ref != "" {
		ref += "@" + fallback.Variant
	}
	return ref
}

func (a *MainAgent) deferFallbackModelDownshift(payload *llmFallbackBoundaryPayload) error {
	if a == nil || payload == nil || a.ctxMgr == nil ||
		payload.fallbackModelRef == "" || payload.fallbackContextLimit <= 0 {
		return nil
	}
	// The auto-compaction line tracks the effective input budget, so a
	// downshift must be detected against both windows: a fallback whose input
	// budget is smaller (even when its total context window is unchanged)
	// re-evaluates the same context against a lower line and can cross it.
	fallbackInput := payload.fallbackInputLimit
	if fallbackInput <= 0 {
		fallbackInput = payload.fallbackContextLimit
	}
	if payload.fallbackContextLimit >= a.ctxMgr.GetMaxTokens() &&
		fallbackInput >= a.ctxMgr.GetInputBudget() {
		return nil
	}

	a.llmMu.Lock()
	a.runningModelRef = payload.fallbackModelRef
	a.llmMu.Unlock()
	a.ctxMgr.SetTokenBudgets(
		payload.fallbackContextLimit,
		payload.fallbackInputLimit,
		a.effectiveCompactionReservedInput(),
	)
	a.applyModelCompactionConfig()
	// Crossing must be evaluated without modelDownshiftCrossing's "not already
	// running" gate: when a compaction is already in flight (e.g. the
	// usage-driven compaction this round started in parallel at the gate), the
	// else branch below still has to fold this round onto it and return the
	// pending error — returning nil here would let the smaller-window fallback
	// request go out over the line.
	if !a.modelDownshiftLineCrossed() {
		return nil
	}
	if a.IsCompactionRunning() {
		// Do not start a second worker for the same switch: fold the deferred
		// round into the running compaction so its apply resumes this request
		// (compactionResumeMainLLM) on the compacted context.
		a.compactionState.continuation = continuationPlan{
			kind:      compactionResumeMainLLM,
			turnID:    payload.turnID,
			turnEpoch: a.currentTurnEpoch(),
		}
		a.compactionState.downshiftSuspended = true
	} else {
		a.startDownshiftCompactionWithContinuation(a.ctxMgr.Snapshot(), payload.turnID, "")
	}
	return &fallbackModelDownshiftCompactionPendingError{
		planID:           a.compactionState.planID,
		selectedModelRef: a.ProviderModelRef(),
		runningModelRef:  payload.fallbackModelRef,
	}
}

type llmFallbackBoundaryPayload struct {
	turnID                  uint64
	messages                []message.Message
	tailOverlayCount        int
	fallbackModelRef        string
	fallbackContextLimit    int
	fallbackInputLimit      int
	fallbackDownshiftBypass bool
	reply                   chan llmFallbackBoundaryResult
}

type llmFallbackBoundaryResult struct {
	messages []message.Message
	err      error
}

// updateMainLLMRequestBeforeFallback pauses the retry worker at the boundary
// before a fallback provider request. The event loop owns pendingUserMessages,
// so it must decide which queued inputs have arrived and append them to both
// the durable context and the fallback request snapshot.
func (a *MainAgent) updateMainLLMRequestBeforeFallback(ctx context.Context, turnID uint64, messages []message.Message, tailOverlayCount int, fallback llm.FallbackModel, bypass bool) ([]message.Message, error) {
	if a == nil {
		return messages, nil
	}
	if !a.started.Load() {
		// The pending queue is event-loop owned. A direct call without a running
		// event loop cannot safely consume it, so leave it for the normal drain.
		return messages, nil
	}

	payload := &llmFallbackBoundaryPayload{
		turnID:                  turnID,
		messages:                messages,
		tailOverlayCount:        tailOverlayCount,
		fallbackModelRef:        fallbackModelDisplayRef(fallback),
		fallbackContextLimit:    fallback.ContextLimit,
		fallbackInputLimit:      fallback.InputLimit,
		fallbackDownshiftBypass: bypass,
		reply:                   make(chan llmFallbackBoundaryResult, 1),
	}
	a.sendEvent(Event{
		Type:    EventLLMFallbackBoundary,
		TurnID:  turnID,
		Payload: payload,
	})
	select {
	case result := <-payload.reply:
		return result.messages, result.err
	case <-ctx.Done():
		return nil, fmt.Errorf("fallback request update cancelled: %w", ctx.Err())
	case <-a.parentCtx.Done():
		return nil, fmt.Errorf("fallback request update cancelled: %w", a.parentCtx.Err())
	case <-a.stoppingCh:
		return nil, context.Canceled
	}
}

func (a *MainAgent) handleLLMFallbackBoundary(evt Event) {
	payload, ok := evt.Payload.(*llmFallbackBoundaryPayload)
	if !ok || payload == nil || payload.reply == nil {
		log.Errorf("handleLLMFallbackBoundary: invalid payload type=%T", evt.Payload)
		return
	}
	if a.turn == nil || payload.turnID == 0 || a.turn.ID != payload.turnID || evt.TurnID != payload.turnID {
		payload.reply <- llmFallbackBoundaryResult{err: context.Canceled}
		return
	}
	messages := a.consumePendingUserMessagesForRequest(payload.messages, payload.tailOverlayCount)
	if !payload.fallbackDownshiftBypass {
		if err := a.deferFallbackModelDownshift(payload); err != nil {
			payload.reply <- llmFallbackBoundaryResult{err: err}
			return
		}
	}
	payload.reply <- llmFallbackBoundaryResult{messages: messages}
}
