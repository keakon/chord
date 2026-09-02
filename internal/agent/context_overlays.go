package agent

import (
	"strings"
	"sync"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/tools"
)

const (
	// contextPressureReminderRatioCap clamps the reminder line so high
	// thresholds get an earlier head start: reminder_pct = min(0.60,
	// threshold*0.90). The lower the threshold, the smaller the head start
	// (threshold <= 0.667 keeps exactly the 10% window). threshold=0 means
	// auto-compact is off and no reminder is ever injected.
	contextPressureReminderRatioCap = 0.60
	// contextPressureReminderThresholdRatio is the reminder head start before
	// the configured auto-compaction threshold.
	contextPressureReminderThresholdRatio = 0.90
	// compactionWarningText is the usage-driven externalization opportunity.
	// It is only injected while model-driven compaction is enabled (the
	// compact_context tool is visible): with the tool off the model has no
	// externalization contract, so automatic compaction is fully owned by the
	// runtime — like Codex's local and remote compaction paths, which never
	// notify the working model — and an unactionable warning would only be
	// read as conversation noise. The text is bare content: the turn-overlay
	// injector wraps it in a <system-reminder> block, the same runtime-message
	// convention used by every other harness injection, so the model can tell
	// it apart from user-written messages. It never asks the model to call
	// compact_context or guarantee a write; it only preserves an
	// externalization opportunity on the request that runs alongside the
	// automatic-compaction start, and says plainly that the compaction does
	// not wait for it.
	compactionWarningText = "The context has reached the automatic-compaction threshold and will be compacted at the next safe boundary.\nIf important findings, decisions, or working state are not yet written to files, write them now — this may be the last request on the current context.\nThe compaction does not wait for this message."
)

// reminderOverlayClaim is the per-window claim for the context-pressure
// reminder. It binds to (compaction_window_id, budget_epoch): a durable apply
// (model-driven or usage-driven), a session reset/restore, or a usage-baseline
// model/provider/budget switch changes one of the three components and starts
// a fresh claim, so a reset that is followed by a new reminder is intended —
// each compaction window gets at most one delivered reminder.
type reminderOverlayClaim struct {
	windowEpoch     uint64
	windowIndex     int    // compaction index (history file count); 0 = no history yet
	budgetEpoch     uint64 // ctxmgr token-budget switch counter (model/provider/budget changes)
	deliveryPending bool   // overlay attached to the current in-flight request
	delivered       bool   // overlay attached to a request that actually dispatched
}

// warningOverlayClaim is the per-generation claim for the usage-driven
// externalization warning. It binds to (auto_compact_request_id,
// main_request_batch). A request whose dispatch was cancelled rolls the batch
// back, so the retried request carries the same batch and may re-claim; once a
// request with that batch dispatches, the batch advances and the same
// generation can never claim again.
type warningOverlayClaim struct {
	requestID       uint64
	batch           uint64
	deliveryPending bool
	delivered       bool
}

// overlayClaimState owns the two one-shot overlay claims. The queue decision
// runs on the event loop (beginMainLLMAfterPreparation), the attach note and
// the delivered confirmation run on the main LLM goroutine (callLLM), so the
// state is mutex-guarded. Claims are best-effort runtime memory (at most once
// per window is best-effort): no durable pending artifact is introduced.
type overlayClaimState struct {
	mu       sync.Mutex
	reminder reminderOverlayClaim
	warning  warningOverlayClaim
}

// tryClaimContextPressureReminder returns true when the reminder overlay may
// be attached for the given (window, budget) epoch, updating the claim
// bookkeeping. Call on the event loop before queuing the overlay text.
func (a *MainAgent) tryClaimContextPressureReminder(windowEpoch uint64, windowIndex int, budgetEpoch uint64) bool {
	a.overlayClaims.mu.Lock()
	defer a.overlayClaims.mu.Unlock()
	c := &a.overlayClaims.reminder
	if c.windowEpoch != windowEpoch || c.windowIndex != windowIndex || c.budgetEpoch != budgetEpoch {
		*c = reminderOverlayClaim{windowEpoch: windowEpoch, windowIndex: windowIndex, budgetEpoch: budgetEpoch}
	}
	return !c.delivered
}

// tryClaimCompactionWarning returns true when the usage-driven externalization
// warning may be attached for the armed request generation. Call on the event
// loop before queuing the overlay text.
func (a *MainAgent) tryClaimCompactionWarning(requestID, batch uint64) bool {
	a.overlayClaims.mu.Lock()
	defer a.overlayClaims.mu.Unlock()
	c := &a.overlayClaims.warning
	if c.requestID != requestID {
		*c = warningOverlayClaim{requestID: requestID, batch: batch}
		return true
	}
	return c.batch == batch && !c.delivered
}

// noteContextPressureReminderAttached records that the reminder overlay was
// attached to the in-flight request. Called from buildTurnOverlayMessages on
// the LLM goroutine; the delivered flag is only confirmed at dispatch.
func (a *MainAgent) noteContextPressureReminderAttached() {
	a.overlayClaims.mu.Lock()
	a.overlayClaims.reminder.deliveryPending = true
	a.overlayClaims.mu.Unlock()
}

// noteCompactionWarningAttached records that the externalization warning was
// attached to the in-flight request. Called from buildTurnOverlayMessages.
func (a *MainAgent) noteCompactionWarningAttached() {
	a.overlayClaims.mu.Lock()
	a.overlayClaims.warning.deliveryPending = true
	a.overlayClaims.mu.Unlock()
}

// markOverlayClaimsDelivered confirms delivery for every overlay that was
// attached to the request being dispatched. Called on the main LLM goroutine
// at the dispatch confirmation point (after the hook and governor gates, right
// before the provider request starts), so a request cancelled before dispatch
// — by a hook block, an oversize rejection, a session switch, or a turn
// replacement — never consumes its claim and the next request may re-claim.
func (a *MainAgent) markOverlayClaimsDelivered() {
	reminderDelivered := false
	warningDelivered := false
	a.overlayClaims.mu.Lock()
	if a.overlayClaims.reminder.deliveryPending {
		a.overlayClaims.reminder.deliveryPending = false
		a.overlayClaims.reminder.delivered = true
		reminderDelivered = true
	}
	if a.overlayClaims.warning.deliveryPending {
		a.overlayClaims.warning.deliveryPending = false
		a.overlayClaims.warning.delivered = true
		warningDelivered = true
	}
	a.overlayClaims.mu.Unlock()
	if reminderDelivered {
		modelDriven := "0"
		if a.compactContextVisible() {
			modelDriven = "1"
		}
		a.recordContextDiagnosticEvent(analytics.UsagePurposeContextPressureReminder, map[string]string{
			"stage":        "delivered",
			"model_driven": modelDriven,
		})
	}
	if warningDelivered {
		a.recordContextDiagnosticEvent(analytics.UsagePurposeCompactionWarning, map[string]string{"stage": "delivered"})
	}
}

// compactContextVisible reports whether the compact_context tool is present in
// the effective surface: the model-driven feature is enabled, the tool is
// registered, and permission rules do not disable it. Only then may a reminder
// name the tool; a denied or invisible tool must never be pushed onto the
// model as an option.
func (a *MainAgent) compactContextVisible() bool {
	if a == nil || !a.modelDrivenCompactionEnabled.Load() || a.tools == nil {
		return false
	}
	if _, ok := a.tools.Get(tools.NameCompactContext); !ok {
		return false
	}
	ruleset := a.effectiveRuleset()
	if len(ruleset) == 0 {
		return true
	}
	return !ruleset.IsDisabled(tools.NameCompactContext)
}

// queueContextPressureReminderForNextRequest arms the one-shot context-pressure
// reminder for the next main request. Called from beginMainLLMAfterPreparation
// before the compaction gate decision, using the post-response usage baseline
// of AutoCompactDecision — not the current request's prepared/reduced surface.
// The usage-driven externalization warning is queued separately by the gate
// only on the request that actually starts the compaction: during the grace
// period the compaction has not started yet, so a warning that claims "the
// runtime has scheduled automatic compaction" would be misleading there.
func (a *MainAgent) queueContextPressureReminderForNextRequest() {
	if a == nil || a.ctxMgr == nil {
		return
	}
	a.queueContextPressureReminder(a.ctxMgr.AutoCompactDecision())
}

func (a *MainAgent) queueContextPressureReminder(decision ctxmgr.AutoCompactDecision) {
	threshold := decision.Threshold
	usable := decision.UsableInputBudget
	// threshold<=0 means auto-compact is off: no reminder, even when
	// model-driven is enabled, because without a usage-driven safety net the
	// reminder would only induce premature resets.
	if threshold <= 0 || usable <= 0 {
		return
	}
	// Without the compact_context tool the reminder is unactionable: the model
	// has no externalization contract, and quoting usage numbers would only
	// invite it to reason about how much space is left instead of preparing
	// for the compaction. Automatic compaction is fully runtime-owned in that
	// mode, so no request-side overlay is injected.
	if !a.compactContextVisible() {
		return
	}
	reminderPct := a.effectiveReminderPct(threshold)
	// "Whichever line is reached first" semantics: when the configured
	// reminder is at or above the threshold, the threshold crossing itself
	// triggers the reminder (compaction starts on the crossing itself, so the
	// reminder and the start share the request).
	if reminderPct > threshold {
		reminderPct = threshold
	}
	if reminderPct <= 0 || float64(decision.EffectiveInputTokens)/float64(usable) < reminderPct {
		return
	}
	windowEpoch := a.sessionEpoch
	windowIndex := nextHistoryIndexMinusOne(a.sessionDir)
	budgetEpoch := a.ctxMgr.TokenBudgetsEpoch()
	if !a.tryClaimContextPressureReminder(windowEpoch, windowIndex, budgetEpoch) {
		return
	}
	a.pendingContextPressureReminder = buildContextPressureReminderText()
}

func (a *MainAgent) queueCompactionWarning() {
	// The warning rides on the request that actually starts the usage-driven
	// compaction. It is only actionable while model-driven compaction is
	// enabled (the compact_context tool is visible and the system prompt's
	// long-session guidance gives the model an externalization contract);
	// otherwise compaction is runtime-owned and the model is never notified.
	if !a.compactContextVisible() {
		return
	}
	if !a.autoCompactRequested.Load() || a.isUsageDrivenAutoCompactSuppressed() {
		return
	}
	// The warning rides on the next main request that actually goes out;
	// beginMainLLMAfterPreparation only runs when a request will be prepared,
	// so the armed flag alone is the binding condition (the gate triggers on
	// the armed request even when the instantaneous decision does not re-arm).
	requestID := a.autoCompactRequestGeneration.Load()
	batch := a.currentRequestBatch(a.ctxMgr.Snapshot())
	if !a.tryClaimCompactionWarning(requestID, batch) {
		return
	}
	a.pendingCompactionWarning = compactionWarningText
}

// buildContextPressureReminderText renders the one-shot reminder. It does not
// quote the current usage ratio or the remaining budget: the model cannot act
// on that number (compaction is already scheduled), and stating how much space
// is left would invite it to reason about deferring instead of preparing. It
// only ever runs while compact_context is visible, so it names the tool
// directly and splits the instruction by phase state. The text is bare
// content; the turn-overlay injector wraps it in a <system-reminder> block.
func buildContextPressureReminderText() string {
	return "The context is approaching the configured automatic-compaction threshold.\n" +
		"If the current phase is wrapped up and its working state is fully externalized, request a durable context checkpoint now by calling compact_context alone.\n" +
		"If the phase is still open, keep writing important findings and decisions to project files as they settle, so they survive the upcoming compaction and can be re-read afterwards."
}

// appendContextPressureVerificationGuidance appends the post-apply guidance:
// only a successful checkpoint apply (model-driven, usage-driven, or oversize
// recovery that depended on it) adds it; skip/failure/non-checkpoint
// continuations never do.
func appendContextPressureVerificationGuidance(text string) string {
	return strings.TrimSpace(text) + "\nBefore continuing, confirm that the preserved Current User Request and Next Step still match the actual state. Re-read any referenced state_files when needed before acting."
}
