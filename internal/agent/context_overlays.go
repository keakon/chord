package agent

import (
	"fmt"
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
	// It never asks the model to delay compaction, call compact_context, or
	// guarantee a write; it only preserves an externalization opportunity
	// while an auto-compact request is armed.
	compactionWarningText = "<compaction-warning>\nThe runtime has scheduled automatic context compaction for the current context.\nIf critical findings or state are not yet externalized, write them when appropriate.\nCompaction may continue independently of this message.\n</compaction-warning>"
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
// state is mutex-guarded. Claims are best-effort runtime memory (§11.3): no
// durable pending artifact is introduced.
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

// queueContextPressureOverlays arms the one-shot context-pressure reminder and
// the usage-driven externalization warning for the next main request. Called
// from beginMainLLMAfterPreparation before the compaction gate decision, using
// the post-response usage baseline of AutoCompactDecision — not the current
// request's prepared/reduced surface. Both overlays may be queued on the same
// request: they have independent claims and neither suppresses the other.
func (a *MainAgent) queueContextPressureOverlays() {
	if a == nil || a.ctxMgr == nil {
		return
	}
	decision := a.ctxMgr.AutoCompactDecision()
	a.queueContextPressureReminder(decision)
	a.queueCompactionWarning()
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
	reminderPct := contextPressureReminderRatioCap
	if head := threshold * contextPressureReminderThresholdRatio; head < reminderPct {
		reminderPct = head
	}
	if float64(decision.EffectiveInputTokens)/float64(usable) < reminderPct {
		return
	}
	windowEpoch := a.sessionEpoch
	windowIndex := nextHistoryIndexMinusOne(a.sessionDir)
	budgetEpoch := a.ctxMgr.TokenBudgetsEpoch()
	if !a.tryClaimContextPressureReminder(windowEpoch, windowIndex, budgetEpoch) {
		return
	}
	a.pendingContextPressureReminder = buildContextPressureReminderText(decision, reminderPct, a.compactContextVisible())
}

func (a *MainAgent) queueCompactionWarning() {
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

// buildContextPressureReminderText renders the one-shot reminder. The usage
// percentage comes from the trusted provider-input baseline
// (AutoCompactDecision), so no "estimate" qualifier is needed for the ratio
// itself; the remaining tokens are derived from the same baseline and rounded.
// The compact_context tool is only named when it is visible and executable.
func buildContextPressureReminderText(decision ctxmgr.AutoCompactDecision, reminderPct float64, toolVisible bool) string {
	remaining := decision.UsableInputBudget - decision.EffectiveInputTokens
	if remaining < 0 {
		remaining = 0
	}
	pct := int(reminderPct * 100)
	if ratio := float64(decision.EffectiveInputTokens) / float64(decision.UsableInputBudget); ratio > 0 {
		pct = int(ratio*100 + 0.5)
	}
	text := fmt.Sprintf("<context-pressure>\nContext usage is approximately %d%% of the usable input budget, with about %d tokens remaining.", pct, remaining)
	if toolVisible {
		text += "\nIf the current phase is wrapped up and its working state is externalized, you may consider requesting a context checkpoint with compact_context before automatic compaction."
	} else {
		text += "\nIf important findings are not externalized, preserve them when appropriate; separable, summarizable work may be isolated in a SubAgent."
	}
	return text + "\n</context-pressure>"
}

// appendContextPressureVerificationGuidance appends the post-apply guidance:
// only a successful checkpoint apply (model-driven, usage-driven, or oversize
// recovery that depended on it) adds it; skip/failure/non-checkpoint
// continuations never do.
func appendContextPressureVerificationGuidance(text string) string {
	return strings.TrimSpace(text) + "\nBefore continuing, confirm that the preserved Current User Request and Next Step still match the actual state. Re-read any referenced state_files when needed before acting."
}
