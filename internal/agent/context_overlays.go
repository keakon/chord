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
	compactionWarningText = "The context has reached the automatic-compaction threshold and will be compacted at the next safe boundary.\nIf important findings, decisions, or working state are not yet written to files, write them now to a project file your role may write (for example a task-notes file under .chord/notes/, named with a YYYYMMDD date prefix, or a plan document under .chord/plans/) — this may be the last request on the current context.\nThe compaction does not wait for this message."
)

// reminderOverlayClaim is the per-window claim shared by the context-pressure
// reminder and the grace-period "compaction imminent" notice (optimizations
// 2.9/2.10). It binds to (compaction_window_id, budget_epoch): a durable apply
// (model-driven or usage-driven), a session reset/restore, or a usage-baseline
// model/provider/budget switch changes one of the three components and starts
// a fresh claim, so a reset that is followed by a new full reminder is
// intended — each compaction window delivers the full reminder text at most
// once.
//
// The claim never suppresses an attach by itself: the reminder overlay is
// sticky while usage stays above the reminder line (the full text before the
// first dispatch, the short text afterwards) and the imminent notice is
// sticky for the whole grace period. delivered only records whether the full
// text / first notice already dispatched, and ccCalled records that the model
// called compact_context in this window — its attempt (whatever it settles
// to) answered the nudge, so the reminder goes quiet until a fresh window
// clears the mark. Cancellation never consumes a delivery: delivered is
// confirmed only when a request carrying the overlay actually dispatches.
type reminderOverlayClaim struct {
	windowEpoch     uint64
	windowIndex     int    // compaction index (history file count); 0 = no history yet
	budgetEpoch     uint64 // ctxmgr token-budget switch counter (model/provider/budget changes)
	deliveryPending bool   // overlay attached to the current in-flight request
	delivered       bool   // overlay attached to a request that actually dispatched
	ccCalled        bool   // model called compact_context in this window
}

// warningOverlayClaim is the per-generation claim for the usage-driven
// externalization warning. It binds to (auto_compact_request_id,
// main_request_batch). A request whose dispatch was cancelled rolls the batch
// back, so the retried request carries the same batch and may re-claim; once a
// request with that batch dispatches, the batch advances and the same
// generation can never claim again. The warning stays one-shot per generation
// (it is explicitly the "last request" notice), unlike the sticky reminder.
type warningOverlayClaim struct {
	requestID       uint64
	batch           uint64
	deliveryPending bool
	delivered       bool
}

// overlayClaimState owns the overlay claims: the sticky context-pressure
// reminder and grace-period imminent notice (reminder-class, same window key)
// plus the one-shot usage-driven warning. The queue decision runs on the event
// loop (beginMainLLMAfterPreparation), the attach note and the delivered
// confirmation run on the main LLM goroutine (callLLM), so the state is
// mutex-guarded. Claims are best-effort runtime memory: no durable pending
// artifact is introduced.
type overlayClaimState struct {
	mu       sync.Mutex
	reminder reminderOverlayClaim
	warning  warningOverlayClaim
	// imminent is the grace-period "compaction imminent" notice claim; it
	// shares the reminder's (window, budget) key but is tracked separately so
	// the two never suppress each other.
	imminent reminderOverlayClaim
}

// syncOverlayWindowClaim binds a reminder-class claim (the context-pressure
// reminder or the grace-period imminent notice) to the given (window, budget)
// key, resetting the claim — including delivered and ccCalled — when a
// component changed, and returns a snapshot for the queue decision. Call on
// the event loop before queuing overlay text.
func (a *MainAgent) syncOverlayWindowClaim(claim *reminderOverlayClaim, windowEpoch uint64, windowIndex int, budgetEpoch uint64) reminderOverlayClaim {
	a.overlayClaims.mu.Lock()
	defer a.overlayClaims.mu.Unlock()
	if claim.windowEpoch != windowEpoch || claim.windowIndex != windowIndex || claim.budgetEpoch != budgetEpoch {
		*claim = reminderOverlayClaim{windowEpoch: windowEpoch, windowIndex: windowIndex, budgetEpoch: budgetEpoch}
	}
	return *claim
}

// markReminderCompactContextCalled records that the model called
// compact_context in the current window: whatever that attempt settles to, the
// reminder nudge has been answered and the sticky reminder must go quiet until
// a fresh window resets the claim. It binds the mark to the current (window,
// budget) key first, because the reminder claim is only synced lazily by the
// queue — a call that arrived after a background apply advanced the window
// must not stamp ccCalled onto the stale pre-apply claim.
func (a *MainAgent) markReminderCompactContextCalled() {
	windowEpoch := a.sessionEpoch
	windowIndex := nextHistoryIndexMinusOne(a.sessionDir)
	budgetEpoch := a.ctxMgr.TokenBudgetsEpoch()
	a.overlayClaims.mu.Lock()
	defer a.overlayClaims.mu.Unlock()
	c := &a.overlayClaims.reminder
	if c.windowEpoch != windowEpoch || c.windowIndex != windowIndex || c.budgetEpoch != budgetEpoch {
		*c = reminderOverlayClaim{windowEpoch: windowEpoch, windowIndex: windowIndex, budgetEpoch: budgetEpoch}
	}
	c.ccCalled = true
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

// noteCompactionImminentAttached records that the grace-period notice was
// attached to the in-flight request. Called from buildTurnOverlayMessages.
func (a *MainAgent) noteCompactionImminentAttached() {
	a.overlayClaims.mu.Lock()
	a.overlayClaims.imminent.deliveryPending = true
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
//
// Delivery stages distinguish the first delivery in the window from the sticky
// repeat deliveries (optimization 2.9): the reminder and the grace imminent
// notice report delivered_first when the claim had no delivery yet and
// delivered_repeat on later ones. The warning stays one-shot per generation
// and keeps a plain delivered stage.
func (a *MainAgent) markOverlayClaimsDelivered() {
	reminderStage := ""
	warningDelivered := false
	imminentStage := ""
	a.overlayClaims.mu.Lock()
	if a.overlayClaims.imminent.deliveryPending {
		imminentStage = "delivered_first"
		if a.overlayClaims.imminent.delivered {
			imminentStage = "delivered_repeat"
		}
		a.overlayClaims.imminent.deliveryPending = false
		a.overlayClaims.imminent.delivered = true
	}
	if a.overlayClaims.reminder.deliveryPending {
		reminderStage = "delivered_first"
		if a.overlayClaims.reminder.delivered {
			reminderStage = "delivered_repeat"
		}
		a.overlayClaims.reminder.deliveryPending = false
		a.overlayClaims.reminder.delivered = true
	}
	if a.overlayClaims.warning.deliveryPending {
		a.overlayClaims.warning.deliveryPending = false
		a.overlayClaims.warning.delivered = true
		warningDelivered = true
	}
	a.overlayClaims.mu.Unlock()
	if reminderStage != "" {
		modelDriven := "0"
		if a.compactContextVisible() {
			modelDriven = "1"
		}
		a.recordContextDiagnosticEvent(analytics.UsagePurposeContextPressureReminder, map[string]string{
			"stage":        reminderStage,
			"model_driven": modelDriven,
		})
	}
	if warningDelivered {
		a.recordContextDiagnosticEvent(analytics.UsagePurposeCompactionWarning, map[string]string{"stage": "delivered"})
	}
	if imminentStage != "" {
		a.recordContextDiagnosticEvent(analytics.UsagePurposeCompactionGrace, map[string]string{"stage": imminentStage})
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

// queueContextPressureReminderForNextRequest queues the context-pressure
// reminder overlay for the next main request. Called from
// beginMainLLMAfterPreparation before the compaction gate decision, using the
// post-response usage baseline of AutoCompactDecision — not the current
// request's prepared/reduced surface. The reminder is sticky (optimization
// 2.9): while usage stays above the reminder line it is re-queued for every
// request — the full text once per window, then a one-line short text — until
// the model calls compact_context in this window, the usage drops back below
// the line, or a durable apply / session switch / model change starts a fresh
// window. The usage-driven externalization warning is queued separately by
// the gate only on the request that actually starts the compaction: during
// the grace period the compaction has not started yet, so a warning that
// claims "the runtime has scheduled automatic compaction" would be misleading
// there.
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
	// "Whichever line is reached first" semantics: a configured reminder below
	// the threshold fires at its own line and is clamped so it never moves the
	// compaction line.
	if reminderPct > threshold {
		reminderPct = threshold
	}
	if reminderPct <= 0 || float64(decision.EffectiveInputTokens)/float64(usable) < reminderPct {
		return
	}
	// Optimization 2.10: when the resolved reminder line sits at or beyond the
	// threshold the reminder would only ever attach to a request that already
	// crossed the line — the crossing and every grace-deferred request carry
	// the "compaction imminent" notice (or the externalization warning once
	// the grace is spent), which contains the same wrap-up and externalize
	// instructions. Injecting the reminder as well would stack two prompts on
	// every such request, so a reminder line at/above the threshold never
	// injects on its own.
	if reminderPct >= threshold {
		return
	}
	windowEpoch := a.sessionEpoch
	windowIndex := nextHistoryIndexMinusOne(a.sessionDir)
	budgetEpoch := a.ctxMgr.TokenBudgetsEpoch()
	claim := a.syncOverlayWindowClaim(&a.overlayClaims.reminder, windowEpoch, windowIndex, budgetEpoch)
	// The model already called compact_context in this window (2.9): whatever
	// that attempt settles to — an apply that advances the window and resets
	// the claim, or a skip/failure surfaced by the continuation notice — the
	// nudge has been answered, so the sticky reminder goes quiet until a fresh
	// window.
	if claim.ccCalled {
		return
	}
	// Sticky re-attach: only the full text is one-shot per window (delivered
	// is confirmed at dispatch, so a cancelled request never consumes it);
	// later above-line requests in the same window carry the short text.
	text := buildContextPressureReminderText()
	if claim.delivered {
		text = contextPressureReminderShortText
	}
	a.pendingContextPressureReminder = text
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

// contextPressureReminderShortText is the one-line re-attachment used on
// requests after the full reminder already dispatched in the same window
// (optimization 2.9). The model saw the full instructions on the first
// delivery, so a single line that it is still above the reminder line and
// points back at the full notice is enough to keep the phase open and
// externalizing without re-quoting the whole contract on every request.
const contextPressureReminderShortText = "Context pressure reminder still active; see the earlier notice."

// buildContextPressureReminderText renders the full reminder text. It does not
// quote the current usage ratio or the remaining budget: the model cannot act
// on that number (compaction is already scheduled), and stating how much space
// is left would invite it to reason about deferring instead of preparing. It
// only ever runs while compact_context is visible, so it names the tool
// directly and splits the instruction by phase state. The text is bare
// content; the turn-overlay injector wraps it in a <system-reminder> block.
func buildContextPressureReminderText() string {
	return "The context is approaching the configured automatic-compaction threshold.\n" +
		"If the current phase is wrapped up and its working state is fully externalized, request a durable context checkpoint now by calling compact_context alone.\n" +
		"If the phase is still open, keep writing important findings and decisions to project files your role may write (for example a task-notes file under .chord/notes/, named with a YYYYMMDD date prefix, or a plan document under .chord/plans/) as they settle, so they survive the upcoming compaction and can be re-read afterwards."
}

// appendContextPressureVerificationGuidance appends the post-apply guidance:
// only a successful checkpoint apply (model-driven, usage-driven, or oversize
// recovery that depended on it) adds it; skip/failure/non-checkpoint
// continuations never do.
func appendContextPressureVerificationGuidance(text string) string {
	return strings.TrimSpace(text) + "\nBefore continuing, confirm that the preserved Current User Request and Next Step still match the actual state. Re-read any referenced state_files when needed before acting."
}
