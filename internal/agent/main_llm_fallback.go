package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

// toastCategoryFallback groups fallback-attempt notifications so the TUI merges
// repeats of the same transition instead of queueing one toast per retry round.
const toastCategoryFallback = "llm_fallback"

// fallbackAttemptToastMessage describes a fallback attempt that just started:
// the selected model failed and the retry loop is moving to another model. The
// toast is emitted when the attempt starts, so it says "trying" instead of
// claiming the switch already happened.
func fallbackAttemptToastMessage(reason, modelRef string) string {
	switch reason = strings.TrimSpace(reason); reason {
	case "context_length_exceeded":
		return fmt.Sprintf("Current model context exceeded; trying fallback model: %s", modelRef)
	case "":
		return fmt.Sprintf("Model error; trying fallback model: %s", modelRef)
	default:
		return fmt.Sprintf("Model error (%s); trying fallback model: %s", reason, modelRef)
	}
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

// applyFallbackModelDownshift commits a fallback that re-evaluates the request
// against a smaller window than the one it was admitted on. The commit moves
// the sidebar identity, the budgets the compaction line is evaluated against,
// and the RunningModelChanged event together, or the displayed model keeps the
// previous window until some later request happens to move it. Under usage-only
// triggering the stale size observation is invalidated (only the calibration
// ratio survives), so nothing arms until fresh usage on the new window crosses
// its own line; the fallback request itself is never held back, and only a hard
// context-length rejection suspends a round.
func (a *MainAgent) applyFallbackModelDownshift(payload *llmFallbackBoundaryPayload) {
	if a == nil || payload == nil || a.ctxMgr == nil ||
		payload.fallbackModelRef == "" || payload.fallbackContextLimit <= 0 {
		return
	}
	// The auto-compaction line tracks the effective input budget, so a
	// downshift must be detected against both windows: a fallback whose input
	// budget is smaller (even when its total context window is unchanged)
	// re-evaluates the same context against a lower line and can cross it.
	if !fallbackNarrowsRequestBudget(payload.fallbackContextLimit, payload.fallbackInputLimit,
		a.ctxMgr.GetMaxTokens(), a.ctxMgr.GetInputBudget()) {
		// A non-narrowing fallback commits nothing: the request was admitted
		// against the current budgets, and the sidebar keeps the last confirmed
		// identity until the fallback emits its first token or the round
		// realigns at cursor head. The compaction state the narrowing branch
		// moves (threshold, armed request) is re-evaluated against the
		// realigned model at the next pre-request gate or the idle compaction
		// check, where an armed request the wider window no longer justifies is
		// cleared instead of force-compacting it.
		return
	}

	client, _ := a.mainLLMAndRef()
	a.applyRunningModelRefIfCurrent(client, payload.fallbackModelRef, payload.fallbackContextLimit, payload.fallbackInputLimit)
	a.applyModelCompactionConfig()
}

type llmFallbackBoundaryPayload struct {
	turnID               uint64
	messages             []message.Message
	tailOverlayCount     int
	primaryContextLimit  int
	primaryInputLimit    int
	primaryModelRef      string
	fallbackModelRef     string
	fallbackContextLimit int
	fallbackInputLimit   int
	reply                chan llmFallbackBoundaryResult
}

type llmFallbackBoundaryResult struct {
	messages []message.Message
	// rebuild asks the requesting goroutine to re-run request preparation on
	// the returned messages. The decision needs event-loop state (the pending
	// user queue and the fallback's narrower budgets); the work it implies does
	// not.
	rebuild bool
	err     error
}

// updateMainLLMRequestBeforeFallback pauses the retry worker at the boundary
// before a fallback provider request. The event loop owns pendingUserMessages,
// so it must decide which queued inputs have arrived and append them to both
// the durable context and the fallback request snapshot.
func (a *MainAgent) updateMainLLMRequestBeforeFallback(ctx context.Context, turnID uint64, messages []message.Message, tailOverlayCount int, fallback llm.FallbackModel) ([]message.Message, error) {
	if a == nil {
		return messages, nil
	}
	if !a.started.Load() {
		// The pending queue is event-loop owned. A direct call without a running
		// event loop cannot safely consume it, so leave it for the normal drain.
		return messages, nil
	}

	payload := &llmFallbackBoundaryPayload{
		turnID:               turnID,
		messages:             messages,
		tailOverlayCount:     tailOverlayCount,
		primaryContextLimit:  a.ctxMgr.GetMaxTokens(),
		primaryInputLimit:    a.ctxMgr.GetInputBudget(),
		primaryModelRef:      a.ProviderModelRef(),
		fallbackModelRef:     fallbackModelDisplayRef(fallback),
		fallbackContextLimit: fallback.ContextLimit,
		fallbackInputLimit:   fallback.InputLimit,
		reply:                make(chan llmFallbackBoundaryResult, 1),
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
	messages = a.injectPendingMailboxMessagesForRequest(messages, payload.tailOverlayCount)
	a.applyFallbackModelDownshift(payload)
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
