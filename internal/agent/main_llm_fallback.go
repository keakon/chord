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
	if !fallbackNarrowsRequestBudget(payload.fallbackContextLimit, payload.fallbackInputLimit,
		a.ctxMgr.GetMaxTokens(), a.ctxMgr.GetInputBudget()) {
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
	if !a.IsCompactionRunning() {
		a.startDownshiftCompactionWithContinuation(a.ctxMgr.Snapshot(), payload.turnID, "")
	}
	// When a compaction is already running the round is folded onto it rather
	// than starting a second worker. That fold is armed by
	// handleCompactionDownshiftSuspend, which the pending error below routes
	// to: arming it here as well would make the continuation plan have two
	// sources that must be kept in step, and only the handler also hands off
	// the activity slot and applies a draft already parked at the barrier.
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
	primaryContextLimit     int
	primaryInputLimit       int
	primaryModelRef         string
	fallbackModelRef        string
	fallbackContextLimit    int
	fallbackInputLimit      int
	fallbackDownshiftBypass bool
	reply                   chan llmFallbackBoundaryResult
}

type llmFallbackBoundaryResult struct {
	messages []message.Message
	// rebuild asks the requesting goroutine to re-run request preparation on
	// the returned messages. The decision needs event-loop state (the pending
	// user queue and the downshifted budgets); the work it implies does not.
	rebuild bool
	err     error
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
		primaryContextLimit:     a.ctxMgr.GetMaxTokens(),
		primaryInputLimit:       a.ctxMgr.GetInputBudget(),
		primaryModelRef:         a.ProviderModelRef(),
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
		if result.err != nil {
			return result.messages, result.err
		}
		messages = result.messages
		rebuilt := result.rebuild
		primarySurface := newRequestSurfaceFingerprint(requestSurfacePrimary, payload.primaryModelRef, messages,
			estimateMessagesTokens(a.ctxMgr, messages), payload.primaryInputLimit)
		if rebuilt {
			messages = a.prepareMessagesForLLMWithOptions(messages, false)
		}
		targetSurface := newRequestSurfaceFingerprint(requestSurfaceFallback, payload.fallbackModelRef, messages,
			estimateMessagesTokens(a.ctxMgr, messages), payload.fallbackInputLimit)
		a.noteFallbackSurfaceDecision(rebuilt)
		log.Debugf("LLM fallback %s", describeSurfaceDecision(primarySurface, targetSurface, rebuilt))
		return messages, nil
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
	// A prepared surface is only reusable when the fallback admits the request
	// against the same effective budget; a narrower target window can require
	// additional reduction even though the primary request already passed. The
	// rebuild itself runs on the requesting goroutine (see
	// updateMainLLMRequestBeforeFallback): it re-runs the whole reduction pass,
	// including the file-evidence rebuild and one archive stat per pending
	// message, which on a long session is far too much work to do inside an
	// event-loop handler.
	payload.reply <- llmFallbackBoundaryResult{
		messages: messages,
		rebuild:  fallbackRequiresFreshAdmission(payload),
	}
}

// fallbackRequiresFreshAdmission reports whether the fallback target has to
// re-run admission on its own budget. Chord's token estimate is model-agnostic
// — a usage-calibrated character estimate shared by every provider, not a
// per-model tokenizer — so reduction is driven purely by the budget: an
// unchanged budget always rebuilds the same bytes and only a narrower window
// can change the surface.
func fallbackRequiresFreshAdmission(payload *llmFallbackBoundaryPayload) bool {
	if payload == nil {
		return false
	}
	return fallbackNarrowsRequestBudget(payload.fallbackContextLimit, payload.fallbackInputLimit,
		payload.primaryContextLimit, payload.primaryInputLimit)
}

// fallbackNarrowsRequestBudget reports whether a fallback model re-evaluates
// the request against a smaller window than the one it was admitted on. It is
// the single definition of "downshift": the auto-compaction line and the
// request surface must agree on it, or one of them acts on a budget the other
// never applied. An unknown fallback window is not a downshift — no budget
// update follows it, and a window that is actually too small surfaces as a
// provider overflow error instead.
func fallbackNarrowsRequestBudget(fallbackContextLimit, fallbackInputLimit, primaryContextLimit, primaryInputLimit int) bool {
	if fallbackContextLimit <= 0 {
		return false
	}
	if fallbackContextLimit < primaryContextLimit {
		return true
	}
	// An unset fallback input limit means the whole window is available to the
	// prompt, which is how the token budgets are applied downstream.
	fallbackInput := fallbackInputLimit
	if fallbackInput <= 0 {
		fallbackInput = fallbackContextLimit
	}
	return primaryInputLimit > 0 && fallbackInput < primaryInputLimit
}
