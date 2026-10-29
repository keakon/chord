package agent

import (
	"sync"

	"github.com/keakon/chord/internal/message"
)

// SubAgent context-pressure notices.
//
// The worker mirrors the MainAgent's ladder in miniature: an approaching
// notice below the compaction threshold and a reached notice at it, both
// persisted as KindContextNotice rows after the carrying request actually
// dispatches, so the card is message-backed and a restore rebuilds it.
// Delivery is claimed once per compaction window per level; the window
// advances when any compaction applies.
//
// While compact_context is visible, a threshold crossing grants a short grace
// during which the deterministic compaction is deferred so the model can wrap
// up and request a checkpoint first. The grace ends on the request-budget
// hard ceiling, on a compact_context call that still left the context above
// the threshold, or after minCompactionGracePeriodBatches logical requests.
// A crossing without a delivered approaching notice is abrupt and compacts
// immediately with the threshold warning.

// subContextNoticeState owns the window, delivery marks, grace, and the
// staged request overlay. The LLM request goroutine observes usage, consumes
// staged overlays and confirms delivery; the event loop advances windows and
// records compact_context calls. Both touch the same state, so one mutex
// guards it.
type subContextNoticeState struct {
	mu sync.Mutex
	// delivered marks, per level, for the current window.
	delivered map[string]bool
	// ccCalled records a compact_context request accepted while the grace was
	// active: whatever the attempt settles to, the countdown has been
	// answered, so the grace no longer defers the deterministic compaction. A
	// call below the threshold (a low-gain skip early in the window) is not a
	// shot at the safety net and must not forfeit a grace not yet granted.
	ccCalled bool
	// dispatchCount counts logical requests this worker actually dispatched.
	// The request-start callback runs for every admitted transport retry, so
	// lastDispatchedRequestSeq deduplicates retries of the same request.
	dispatchCount            uint64
	lastDispatchedRequestSeq uint64
	// grace defers the deterministic compaction after a threshold crossing.
	graceActive        bool
	graceStartDispatch uint64
	graceExhausted     bool
	// staged carries the overlay text for the request currently being built.
	// Staged fresh by every observation, consumed at request assembly, and
	// persisted as a durable row at dispatch confirmation.
	stagedLevel string
	stagedText  string
}

// observeSubContextPressure stages this request's pressure overlay and
// decides whether the compaction grace defers the deterministic compaction.
// Called from prepareContextForLLM. Returns true when the request should go
// out without deterministic compaction.
//
// The usage ratio is estimated/budget — applyModelCompactionConfig returns
// the threshold, not a usage. Guards mirror the MainAgent queue: no notices
// without a threshold, a budget, and a visible compact_context; the
// approaching level additionally requires an enabled reminder line.
func (s *SubAgent) observeSubContextPressure(modelRef string, estimated, budget int, threshold float64, requestBudget int) bool {
	st := &s.subNotices
	st.mu.Lock()
	defer st.mu.Unlock()
	preserveWarning := st.stagedLevel == contextNoticeWarning && threshold > 0 && budget > 0 && s.compactContextRegistered
	if !preserveWarning {
		st.stagedLevel, st.stagedText = "", ""
	}
	if threshold <= 0 || budget <= 0 || !s.compactContextRegistered {
		return false
	}
	usage := float64(estimated) / float64(budget)
	// "Whichever line is reached first": a configured reminder line at or
	// above the threshold never injects on its own — such requests already
	// carry the reached notice.
	reminderPct := 0.0
	if s.parent != nil {
		reminderPct = s.parent.effectiveReminderPctForModelRef(modelRef, threshold)
		if reminderPct > threshold {
			reminderPct = threshold
		}
	}
	// approaching: reminderPct <= usage < threshold, once per window.
	if usage < threshold && reminderPct > 0 && usage >= reminderPct &&
		!preserveWarning && !st.delivered[contextNoticePressure] && !st.delivered[contextNoticeWarning] {
		st.stagedLevel = contextNoticePressure
		st.stagedText = buildContextPressureReminderTextFor(contextPressureAudienceWorker)
	}

	overThreshold := usage >= threshold
	overRequestBudget := requestBudget > 0 && estimated >= requestBudget
	if !overThreshold && !overRequestBudget {
		return false
	}
	// reached: a trigger line is met. The grace defers the deterministic
	// compaction for the crossing request plus one more, but only after the
	// model has received an approaching notice. A sudden crossing has no
	// countdown to answer and compacts immediately.
	hardCeiling := requestBudget > 0 && estimated >= int(float64(requestBudget)*compactionGraceHardCeilingRatio)
	deferred := false
	switch {
	case st.graceExhausted || hardCeiling || st.ccCalled:
		st.graceActive = false
		st.graceExhausted = true
	case !st.delivered[contextNoticePressure]:
		st.graceActive = false
		st.graceExhausted = true
		if !st.delivered[contextNoticeWarning] {
			st.stagedLevel = contextNoticeWarning
			st.stagedText = compactionThresholdNoticeTextFor(contextPressureAudienceWorker)
		}
		return false
	case !st.graceActive:
		st.graceActive = true
		st.graceStartDispatch = st.dispatchCount
		deferred = true
	case st.dispatchCount-st.graceStartDispatch >= minCompactionGracePeriodBatches:
		st.graceActive = false
		st.graceExhausted = true
	default:
		deferred = true
	}
	if deferred && !preserveWarning && !st.delivered[contextNoticeImminent] {
		st.stagedLevel = contextNoticeImminent
		st.stagedText = compactionThresholdNoticeTextFor(contextPressureAudienceWorker)
	}
	return deferred
}

// stagedContextNoticeOverlay returns the staged overlay for the request being
// assembled. The overlay is a transient request tail; the slot is NOT
// consumed here — the dispatch confirmation point (emitStagedContextNotice)
// consumes it, so a request that never dispatches leaves the slot to the next
// observation instead of orphaning the notice.
func (s *SubAgent) stagedContextNoticeOverlay() (message.Message, bool) {
	st := &s.subNotices
	st.mu.Lock()
	level, text := st.stagedLevel, st.stagedText
	st.mu.Unlock()
	if text == "" {
		return message.Message{}, false
	}
	return message.Message{
		Role:        message.RoleUser,
		Kind:        message.KindTurnOverlay,
		Content:     message.SystemReminderOverlayText(text),
		NoticeLevel: level,
	}, true
}

// emitStagedContextNotice consumes the staged notice, persists it as a
// durable KindContextNotice row, and surfaces it as a card backed by that
// message, mirroring the SubAgent LSP follow-up: a persistence failure defers
// the live card to the restore path. Called from the request goroutine at the
// dispatch confirmation point — the request carried the overlay, so the
// model has already received the text.
func (s *SubAgent) emitStagedContextNotice() {
	if s == nil || s.ctxMgr == nil {
		return
	}
	st := &s.subNotices
	st.mu.Lock()
	level, text := st.stagedLevel, st.stagedText
	st.stagedLevel, st.stagedText = "", ""
	st.mu.Unlock()
	if text == "" {
		return
	}
	st.mu.Lock()
	if st.delivered == nil {
		st.delivered = make(map[string]bool)
	}
	st.delivered[level] = true
	st.mu.Unlock()
	msg := message.Message{
		Role:        message.RoleUser,
		Kind:        message.KindContextNotice,
		Content:     message.SystemReminderOverlayText(text),
		NoticeLevel: level,
	}
	messageIndex := s.ctxMgr.MessageCount()
	s.ctxMgr.Append(msg)
	s.persistMessageAsync(msg, "context notice", func() {
		if s.parent != nil {
			s.parent.emitToTUI(ContextNoticeEvent{Level: level, Message: text, MessageIndex: messageIndex, AgentID: s.instanceID})
		}
	})
}

// noteSubRequestDispatched counts one logical request. The same requestSeq is
// reported again when the LLM client retries a transport submission.
func (s *SubAgent) noteSubRequestDispatched(requestSeq uint64) {
	st := &s.subNotices
	st.mu.Lock()
	if requestSeq <= st.lastDispatchedRequestSeq {
		st.mu.Unlock()
		return
	}
	st.lastDispatchedRequestSeq = requestSeq
	st.dispatchCount++
	st.mu.Unlock()
}

// noteSubCompactContextCalled records that the model called compact_context
// during the grace: the grace's purpose — let the model checkpoint first —
// has been exercised, so it no longer defers the deterministic compaction.
// Calls outside the grace leave it untouched.
func (s *SubAgent) noteSubCompactContextCalled() {
	st := &s.subNotices
	st.mu.Lock()
	if st.graceActive {
		st.ccCalled = true
	}
	st.mu.Unlock()
}

// advanceSubNoticeWindow ends the current compaction window: delivered marks,
// the compact_context mark and the grace reset, so a fresh window can claim
// both levels again. Called after any successful compaction apply.
func (s *SubAgent) advanceSubNoticeWindow() {
	st := &s.subNotices
	st.mu.Lock()
	pendingWarning, warningText := "", ""
	if st.stagedLevel == contextNoticeWarning {
		pendingWarning, warningText = st.stagedLevel, st.stagedText
	}
	st.delivered = make(map[string]bool)
	st.ccCalled = false
	st.graceActive = false
	st.graceStartDispatch = 0
	st.graceExhausted = false
	st.stagedLevel, st.stagedText = pendingWarning, warningText
	st.mu.Unlock()
}

// restoreContextNoticeState rebuilds the delivery marks from a restored
// transcript: durable KindContextNotice rows after the newest compaction
// summary mark their levels delivered in the fresh window, so a restored
// worker does not re-notice what its transcript already shows. Rows before
// the summary belonged to windows that already ended.
func (s *SubAgent) restoreContextNoticeState(messages []message.Message) {
	st := &s.subNotices
	st.mu.Lock()
	defer st.mu.Unlock()
	st.delivered = make(map[string]bool)
	for i := len(messages) - 1; i >= 0; i-- {
		msg := &messages[i]
		if msg.IsCompactionSummary {
			break
		}
		if msg.Kind == message.KindContextNotice {
			st.delivered[msg.NoticeLevel] = true
		}
	}
}
