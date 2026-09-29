package agent

import (
	"strings"
	"sync"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
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
	// A threshold notice remains true throughout grace and compaction startup.
	compactionThresholdNoticeText = "The context has reached the automatic-compaction threshold. The runtime may compact it after the current safe continuation window.\n" +
		contextCheckpointPressureAction + "\n" +
		"Earlier messages remain recoverable from the checkpoint's archived history files; preserve the working details needed to continue before compaction."
)

// reminderOverlayClaim tracks first dispatch within a compaction window.
// Cancelled requests leave the claim available; durable rows suppress repeats
// across model/window changes until the relevant row is withdrawn.
type reminderOverlayClaim struct {
	windowEpoch     uint64
	windowIndex     int    // compaction window generation; 0 = initial window
	modelRef        string // running/selected provider/model identity
	budgetEpoch     uint64 // ctxmgr token-budget switch counter
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
// while sharing the durable upper-threshold slot with the grace notice.
type warningOverlayClaim struct {
	requestID       uint64
	batch           uint64
	deliveryPending bool
	delivered       bool
}

// overlayClaimState owns the overlay claims: the context-pressure
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
	// pressure is the single authority for the current pressure cycle: the
	// stage reached, the durable notice rows it may write (one per level), and
	// how it ended. It lives under this mutex because the durable-row
	// reservation happens on the main LLM goroutine while the stage transitions
	// happen on the event loop.
	pressure pressureCycle
	// noticeModel is the model whose prepared request last selected the notices.
	noticeModel         string
	noticePrefixSource  []message.Message
	noticePrefixesKnown bool
}

// overlayWindowKey is the (session epoch, compaction window, model, budget
// epoch) identity a reminder-class claim binds to. It exists so the four
// components are assembled in exactly one place: every site that used to spell
// the triple out by hand — and then compare it field by field — now goes
// through overlayWindowKey / bindTo.
type overlayWindowKey struct {
	windowEpoch uint64
	windowIndex int
	modelRef    string
	budgetEpoch uint64
}

// overlayWindowKey builds the key for an explicit window, filling in the
// running model identity.
func (a *MainAgent) overlayWindowKey(windowEpoch uint64, windowIndex int, budgetEpoch uint64) overlayWindowKey {
	return overlayWindowKey{
		windowEpoch: windowEpoch,
		windowIndex: windowIndex,
		modelRef:    a.reminderClaimModelRef(),
		budgetEpoch: budgetEpoch,
	}
}

// currentOverlayWindowKey reads the live window identity. Call on the event
// loop (it reads sessionEpoch / compactionWindowGeneration).
func (a *MainAgent) currentOverlayWindowKey() overlayWindowKey {
	return a.overlayWindowKey(a.sessionEpoch, int(a.compactionWindowGeneration), a.ctxMgr.TokenBudgetsEpoch())
}

// key returns the window identity a claim is currently bound to.
func (c *reminderOverlayClaim) key() overlayWindowKey {
	return overlayWindowKey{windowEpoch: c.windowEpoch, windowIndex: c.windowIndex, modelRef: c.modelRef, budgetEpoch: c.budgetEpoch}
}

// bindTo rebinds the claim to key, resetting it — including delivered and
// ccCalled — when any component changed. Callers must hold overlayClaims.mu.
func (c *reminderOverlayClaim) bindTo(key overlayWindowKey) {
	if c.key() != key {
		*c = reminderOverlayClaim{windowEpoch: key.windowEpoch, windowIndex: key.windowIndex, modelRef: key.modelRef, budgetEpoch: key.budgetEpoch}
	}
}

// confirmDelivery consumes a pending attachment at dispatch and reports the
// delivery stage ("" when nothing was attached). The first delivery in a
// window is distinguished from redundant delivery attempts.
func (c *reminderOverlayClaim) confirmDelivery() string {
	if !c.deliveryPending {
		return ""
	}
	stage := "delivered_first"
	if c.delivered {
		stage = "delivered_repeat"
	}
	c.deliveryPending = false
	c.delivered = true
	return stage
}

// syncOverlayWindowClaim binds a reminder-class claim (the context-pressure
// reminder or the grace-period imminent notice) to a window key, resetting the
// claim — including delivered and ccCalled — when a component changed, and
// returns a snapshot for the queue decision. Call on the event loop before
// queuing overlay text.
func (a *MainAgent) syncOverlayWindowClaim(claim *reminderOverlayClaim, key overlayWindowKey) reminderOverlayClaim {
	a.overlayClaims.mu.Lock()
	defer a.overlayClaims.mu.Unlock()
	claim.bindTo(key)
	return *claim
}

func (a *MainAgent) reminderClaimModelRef() string {
	if a == nil {
		return ""
	}
	a.llmMu.RLock()
	defer a.llmMu.RUnlock()
	if ref := strings.TrimSpace(a.runningModelRef); ref != "" {
		return ref
	}
	return strings.TrimSpace(a.providerModelRef)
}

// markReminderCompactContextCalled records that the model called
// compact_context in the current window: whatever that attempt settles to, the
// reminder nudge has been answered and the reminder must go quiet until
// a fresh window resets the claim. It binds the mark to the current (window,
// budget) key first, because the reminder claim is only synced lazily by the
// queue — a call that arrived after a background apply advanced the window
// must not stamp ccCalled onto the stale pre-apply claim.
func (a *MainAgent) markReminderCompactContextCalled() {
	key := a.currentOverlayWindowKey()
	a.overlayClaims.mu.Lock()
	c := &a.overlayClaims.reminder
	c.bindTo(key)
	c.ccCalled = true
	a.overlayClaims.mu.Unlock()
	// The pressure cycle records the answer separately: the claim is the
	// per-window delivery gate, the cycle's preparation audit is the fact.
	a.notePressurePreparation(pressurePreparationCompactContext, time.Now())
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

// contextNotice is the text of one context overlay, staged so the dispatch
// confirmation point can surface it to the user as a card.
type contextNotice struct {
	level string
	text  string
}

// Pending-notice text hand-off: the three pending fields are written by the
// event-loop queue paths and cleared or restaged by the request assembly on
// the main LLM goroutine (reconcilePressureNoticesForModel,
// buildTurnOverlayMessages). A turn replaced or cancelled mid-assembly runs
// both sides concurrently, so the fields share overlayClaims.mu — the same
// guard as the claims the queue decisions feed.

// setPendingContextNoticeText stages one notice level's overlay text for the
// next request assembly; an empty text clears the level.
func (a *MainAgent) setPendingContextNoticeText(level, text string) {
	a.overlayClaims.mu.Lock()
	defer a.overlayClaims.mu.Unlock()
	switch level {
	case contextNoticePressure:
		a.pendingContextPressureReminder = text
	case contextNoticeImminent:
		a.pendingCompactionImminent = text
	case contextNoticeWarning:
		a.pendingCompactionWarning = text
	}
}

// clearPendingContextNotices drops every queued overlay text.
func (a *MainAgent) clearPendingContextNotices() {
	a.overlayClaims.mu.Lock()
	a.pendingContextPressureReminder = ""
	a.pendingCompactionImminent = ""
	a.pendingCompactionWarning = ""
	a.overlayClaims.mu.Unlock()
}

// takePendingContextNoticeText consumes one level's queued overlay text.
func (a *MainAgent) takePendingContextNoticeText(level string) string {
	a.overlayClaims.mu.Lock()
	defer a.overlayClaims.mu.Unlock()
	var text string
	switch level {
	case contextNoticePressure:
		text, a.pendingContextPressureReminder = a.pendingContextPressureReminder, ""
	case contextNoticeImminent:
		text, a.pendingCompactionImminent = a.pendingCompactionImminent, ""
	case contextNoticeWarning:
		text, a.pendingCompactionWarning = a.pendingCompactionWarning, ""
	}
	return text
}

// stageContextNotice retains one pending message for each threshold. Grace
// and compaction startup share the upper slot; startup supersedes grace.
func (a *MainAgent) stageContextNotice(level, text string) {
	if a == nil || strings.TrimSpace(text) == "" {
		return
	}
	rank := pressureNoticeSlot(level)
	if rank < 0 {
		return
	}
	if a.hasDurablePressureNotice(level) {
		return
	}
	a.overlayClaims.mu.Lock()
	defer a.overlayClaims.mu.Unlock()
	switch level {
	case contextNoticePressure:
		a.pendingContextPressureReminder = text
	case contextNoticeImminent:
		// The warning owns the shared upper slot once staged; the empty-text
		// guard above means the staged text is never blank.
		if a.pendingCompactionWarning != "" {
			return
		}
		a.pendingCompactionImminent = text
	case contextNoticeWarning:
		a.pendingCompactionImminent = ""
		a.pendingCompactionWarning = text
	}
}

// stashContextNotice stages the text of a context overlay that was just
// attached to the request being assembled (buildTurnOverlayMessages).
func (a *MainAgent) stashContextNotice(level, text string) {
	if a == nil || strings.TrimSpace(text) == "" {
		return
	}
	a.pendingContextNoticesMu.Lock()
	a.pendingContextNotices = append(a.pendingContextNotices, contextNotice{level: level, text: text})
	a.pendingContextNoticesMu.Unlock()
}

// resetContextNotices drops notices staged for a request that never reached
// dispatch, so a cancelled request's overlay text cannot surface later.
func (a *MainAgent) resetContextNotices() {
	if a == nil {
		return
	}
	a.pendingContextNoticesMu.Lock()
	a.pendingContextNotices = nil
	a.pendingContextNoticesMu.Unlock()
}

func (a *MainAgent) takeContextNotices() []contextNotice {
	if a == nil {
		return nil
	}
	a.pendingContextNoticesMu.Lock()
	out := a.pendingContextNotices
	a.pendingContextNotices = nil
	a.pendingContextNoticesMu.Unlock()
	return out
}

// emitStagedContextNotices turns the overlays confirmed by this dispatch into
// durable transcript messages and the cards that mirror them. The message is
// the only source of the card: it is appended to ctxmgr and persisted before
// the event goes out, and ContextNoticeEvent carries its transcript index so a
// restored session rebuilds the same card from message.KindContextNotice
// instead of losing a live-only notice. Each notice level records its first
// delivery once, so the user sees a card for every message the model actually
// received. Later requests replay the durable rows without new overlays.
func (a *MainAgent) emitStagedContextNotices(reminderStage, imminentStage string, warningDelivered bool) {
	if a == nil || a.ctxMgr == nil {
		return
	}
	for _, notice := range a.takeContextNotices() {
		switch notice.level {
		case contextNoticePressure:
			if reminderStage != "delivered_first" {
				continue
			}
		case contextNoticeImminent:
			if imminentStage != "delivered_first" {
				continue
			}
		case contextNoticeWarning:
			if !warningDelivered {
				continue
			}
		}
		level := notice.level
		text := notice.text
		cycleID, skipReason, reserved := a.reservePressureCycleRecord(level)
		if !reserved {
			// The existing durable threshold row owns its card. No live-only
			// event is emitted for a suppressed duplicate.
			log.Debugf("durable context notice row skipped level=%v reason=%v", level, skipReason)
			continue
		}
		msg := message.Message{
			Role: message.RoleUser,
			Kind: message.KindContextNotice,
			// The durable row carries the same <system-reminder> wrapper every
			// other harness injection uses, so the model can tell it apart from
			// a user-written message when a later request replays the notice.
			// The live card is built from the bare event text below.
			Content:         "<system-reminder>\n" + text + "\n</system-reminder>",
			NoticeLevel:     level,
			PressureCycleID: cycleID,
		}
		messageIndex := a.ctxMgr.MessageCount()
		a.ctxMgr.Append(msg)
		a.contextNoticesPersisted.Store(true)
		a.persistAsyncAfter(identity.MainAgentID, msg, func(err error) {
			if err != nil {
				a.notePersistenceFailure(err)
				return
			}
			a.emitToTUI(ContextNoticeEvent{Level: level, Message: text, MessageIndex: messageIndex})
		})
	}
}

// removedContextNotice names one durable notice row withdrawn from the
// transcript: its cycle and level are what the reservation release needs to
// give the level back to the live cycle.
type removedContextNotice struct {
	cycleID uint64
	level   string
}

// maybeClearStaleContextNotices removes withdrawn rows and their cards at an
// idle boundary. Active requests and compaction retain their original indices.
// Surviving rows keep their delivery claims and cards in their original positions.
func (a *MainAgent) maybeClearStaleContextNotices() {
	if a == nil || a.ctxMgr == nil || !a.contextNoticesStale.Load() {
		return
	}
	if a.turn != nil || a.mainLLMRequestInFlight.Load() || a.IsCompactionRunning() {
		return
	}
	scope := a.contextNoticeWithdrawalScope.Load()
	a.contextNoticesStale.Store(false)
	a.contextNoticeWithdrawalScope.Store(contextNoticeWithdrawAll)
	// Any overlay queued for the next request was measured against the stale
	// decision; drop it so the next request re-queues against live usage.
	a.clearPendingContextNotices()
	a.resetContextNotices()
	a.flushPersist()
	messages := a.ctxMgr.Snapshot()
	kept := make([]message.Message, 0, len(messages))
	removed := false
	var removedRows []removedContextNotice
	var removedIndices []int
	for index, msg := range messages {
		if msg.Kind == message.KindContextNotice && contextNoticeStale(msg, scope) {
			removed = true
			removedIndices = append(removedIndices, index)
			removedRows = append(removedRows, removedContextNotice{cycleID: msg.PressureCycleID, level: msg.NoticeLevel})
			continue
		}
		kept = append(kept, msg)
	}
	// The scan is the authoritative answer on whether any notice row remains:
	// rows after the removal point (and every other message) survive.
	a.contextNoticesPersisted.Store(containsContextNotice(kept))
	if !removed {
		// Consume a leftover delivered flag so a later below-line queue does
		// not keep re-arming idle cleanup when there is nothing to rewrite.
		a.resetOverlayDeliveryAfterNoticeClear(scope)
		return
	}
	if manager := a.recoveryManager(); manager != nil {
		if err := manager.RewriteLog(identity.MainAgentID, kept); err != nil {
			a.notePersistenceFailure(err)
			a.contextNoticesPersisted.Store(containsContextNotice(messages))
			a.contextNoticeWithdrawalScope.Store(scope)
			a.contextNoticesStale.Store(true)
			return
		}
	}
	a.ctxMgr.RestoreMessages(kept)
	a.resetOverlayDeliveryAfterNoticeClear(scope)
	// The withdrawn rows gave up their cycle's reservations: a later re-crossing
	// of the same cycle may record a level again, while a withdrawn row from an
	// older cycle cannot reopen the current one's reservation.
	for _, row := range removedRows {
		a.releasePressureCycleRecord(row.cycleID, row.level)
	}
	// The rewrite is the new truth about which rows document pressure, so the
	// adoption follows it. A row withdrawn before the next cycle opens would
	// otherwise leave the runtime reviving an identity the transcript no longer
	// carries — and that cycle would skip the card the row used to justify.
	a.derivePressureCycleAdoption(kept)
	a.emitToTUI(ContextNoticeClearedEvent{MessageIndices: removedIndices})
}

// contextNoticeStale reports whether an armed cleanup withdraws this row. A
// pressure-only withdrawal leaves the threshold-measured classes in place, and
// a row that does not name the reminder class is never proven to be one, so it
// survives: keeping a stale row costs a visible card, while withdrawing a live
// threshold notice loses the externalization instruction the runtime just gave.
const (
	contextNoticeWithdrawAll uint32 = iota
	contextNoticeWithdrawPressure
	contextNoticeWithdrawCompaction
)

func contextNoticeStale(msg message.Message, scope uint32) bool {
	return pressureSlotWithdrawn(pressureNoticeSlot(msg.NoticeLevel), scope)
}

// pressureSlotWithdrawn reports whether an armed cleanup withdraws every row
// of a threshold slot. A pressure-only withdrawal leaves the
// threshold-measured classes in place, and a slot that does not name the
// reminder class is never proven to be one, so it survives: keeping a stale
// row costs a visible card, while withdrawing a live threshold notice loses
// the externalization instruction the runtime just gave.
func pressureSlotWithdrawn(slot int, scope uint32) bool {
	switch scope {
	case contextNoticeWithdrawPressure:
		return slot == 0
	case contextNoticeWithdrawCompaction:
		return slot == 1
	default:
		return true
	}
}

// resetOverlayDeliveryAfterNoticeClear releases only the withdrawn threshold's
// delivery claims. A compact_context call still keeps its answered reminder
// quiet until the next window.
func (a *MainAgent) resetOverlayDeliveryAfterNoticeClear(scope uint32) {
	a.overlayClaims.mu.Lock()
	if scope != contextNoticeWithdrawPressure {
		a.overlayClaims.imminent.deliveryPending = false
		a.overlayClaims.imminent.delivered = false
		a.overlayClaims.warning.deliveryPending = false
		a.overlayClaims.warning.delivered = false
	}
	if scope != contextNoticeWithdrawCompaction {
		a.overlayClaims.reminder.deliveryPending = false
		a.overlayClaims.reminder.delivered = false
	}
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
// repeat deliveries: the reminder and the grace imminent
// notice report delivered_first when the claim had no delivery yet and
// delivered_repeat on later ones. The warning stays one-shot per generation
// and keeps a plain delivered stage.
func (a *MainAgent) markOverlayClaimsDelivered() {
	warningDelivered := false
	a.overlayClaims.mu.Lock()
	imminentStage := a.overlayClaims.imminent.confirmDelivery()
	reminderStage := a.overlayClaims.reminder.confirmDelivery()
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
	a.emitStagedContextNotices(reminderStage, imminentStage, warningDelivered)
}

// compactContextVisible reports whether the compact_context tool is present in
// the effective surface: the model-driven feature is enabled, the tool is
// registered, and no non-global permission rule whose tool pattern matches
// compact_context denies it. Wildcard-only rules (such as an allowlist's
// `"*": deny`) never hide the tool — registration is gated by the
// model_driven feature flag, which is the user's authorization
// (see compactContextPermissionAction). Only then may a reminder name the
// tool; a denied or invisible tool must never be pushed onto the model as an
// option.
func (a *MainAgent) compactContextVisible() bool {
	if a == nil || !a.modelDrivenCompactionEnabled.Load() || a.tools == nil {
		return false
	}
	if _, ok := a.tools.Get(tools.NameCompactContext); !ok {
		return false
	}
	return compactContextPermissionAction(a.effectiveRuleset()) != permission.ActionDeny
}

// queueContextPressureReminderForNextRequest evaluates the lower threshold
// from observed usage. A delivered notification stays in the durable history;
// only the request reconciler may withdraw it after a relevant prefix change.
// The gate separately stages the shared upper-threshold notice.
func (a *MainAgent) queueContextPressureReminderForNextRequest() {
	if a == nil || a.ctxMgr == nil {
		return
	}
	a.queueContextPressureReminder(a.ctxMgr.AutoCompactDecision())
}

func (a *MainAgent) queueContextPressureReminder(decision ctxmgr.AutoCompactDecision) {
	a.setPendingContextNoticeText(contextNoticePressure, "")
	threshold := decision.Threshold
	usable := decision.UsableInputBudget
	// threshold<=0 means auto-compact is off: no reminder, even when
	// model-driven is enabled, because without a usage-driven safety net the
	// reminder would only induce premature resets.
	if threshold <= 0 || usable <= 0 {
		// Automatic compaction is off: a durable pressure notice can no longer
		// be justified by the live decision.
		a.endPressureCycle(pressureEndDisabled)
		a.armContextNoticeCleanup()
		return
	}
	// Without the compact_context tool the reminder is unactionable: the model
	// has no externalization contract, and quoting usage numbers would only
	// invite it to reason about how much space is left instead of preparing
	// for the compaction. Automatic compaction is fully runtime-owned in that
	// mode, so no request-side overlay is injected — and a durable row written
	// by a configuration that did inject one is stale.
	if !a.compactContextVisible() {
		a.endPressureCycle(pressureEndDisabled)
		a.armContextNoticeCleanup()
		return
	}
	reminderPct := a.effectiveReminderPct(threshold)
	// "Whichever line is reached first" semantics: a configured reminder below
	// the threshold fires at its own line and is clamped so it never moves the
	// compaction line.
	if reminderPct > threshold {
		reminderPct = threshold
	}
	if reminderPct <= 0 {
		// The reminder is explicitly disabled for this model: there is no line
		// for usage to fall below, and the usage-driven arm and grace must keep
		// running so automatic compaction still starts at the threshold. Only
		// the reminder-class rows are withdrawn — a notice left by a
		// configuration that injected one — while the rows measured against
		// the live threshold (the grace notice and the externalization
		// warning) must survive: sweeping them would delete a notice the
		// runtime just wrote and rewrite it on the next request.
		a.armContextPressureNoticeCleanup()
		return
	}
	if a.contextPressureBelowReminderLine(decision, reminderPct) {
		// Usage withdrew below the reminder line: the crossing that armed the
		// request is no longer justified, so the next gate must not start
		// compaction from it, and any queued or durable notice measured
		// against the higher line is stale.
		a.endPressureCycle(pressureEndWithdrawn)
		a.clearUsageDrivenAutoCompactRequest()
		a.clearCompactionGrace()
		a.setPendingContextNoticeText(contextNoticeWarning, "")
		return
	}
	// When the resolved reminder line sits at or beyond the
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
	// The stage is noted before the claim sync: after a restore it opens the
	// cycle against the adopted row, and the claim it seeds (delivered) is then
	// what suppresses another delivery of an existing threshold.
	a.notePressureStage(pressureStageReminded, a.currentOverlayWindowKey())
	claim := a.syncOverlayWindowClaim(&a.overlayClaims.reminder, a.currentOverlayWindowKey())
	// The model already called compact_context in this window: whatever
	// that attempt settles to — an apply that advances the window and resets
	// the claim, or a skip/failure surfaced by the continuation notice — the
	// nudge has been answered, so the reminder goes quiet until a fresh
	// window.
	if claim.ccCalled {
		return
	}
	if claim.delivered || a.hasDurablePressureNotice(contextNoticePressure) {
		return
	}
	a.stageContextNotice(contextNoticePressure, buildContextPressureReminderText())
}

// contextPressureBelowReminderLine reports whether the decision's effective
// usage sits below the resolved reminder line. A negative reminderPct means
// resolve it from the decision's threshold. A disabled or absent line reports
// false: there is nothing for usage to fall below, and callers must not treat
// "no reminder" as "below the line" (that would disarm a justified
// usage-driven compaction request).
func (a *MainAgent) contextPressureBelowReminderLine(decision ctxmgr.AutoCompactDecision, reminderPct float64) bool {
	threshold := decision.Threshold
	usable := decision.UsableInputBudget
	if threshold <= 0 || usable <= 0 {
		return false
	}
	if reminderPct < 0 {
		reminderPct = a.effectiveReminderPct(threshold)
	}
	if reminderPct > threshold {
		reminderPct = threshold
	}
	if reminderPct <= 0 {
		return false
	}
	return float64(decision.EffectiveInputTokens)/float64(usable) < reminderPct
}

// containsContextNotice reports whether a message list carries a durable
// context-pressure notice row.
func containsContextNotice(messages []message.Message) bool {
	for i := range messages {
		if messages[i].Kind == message.KindContextNotice {
			return true
		}
	}
	return false
}

// installContextNoticePresence rebuilds the durable-notice presence signal from
// a freshly loaded transcript and drops any cleanup armed for the session being
// replaced. Overlay delivery claims are runtime memory that never survives a
// restore or a session switch, so the load path is the only place presence can
// be reestablished; the live decision at the next request boundary then decides
// whether the loaded rows are still justified.
func (a *MainAgent) installContextNoticePresence(messages []message.Message) {
	if a == nil {
		return
	}
	a.contextNoticeWithdrawalScope.Store(contextNoticeWithdrawAll)
	a.contextNoticesStale.Store(false)
	a.overlayClaims.mu.Lock()
	a.overlayClaims.noticeModel = ""
	a.overlayClaims.noticePrefixSource = nil
	a.overlayClaims.noticePrefixesKnown = false
	a.overlayClaims.mu.Unlock()
	a.contextNoticesPersisted.Store(containsContextNotice(messages))
	a.adoptPressureCyclesFromTranscript(messages)
}

// armContextNoticeCleanup withdraws all notices when their contract is disabled.
// Model and request-prefix changes use the scoped validity selection instead.
func (a *MainAgent) armContextNoticeCleanup() {
	if a == nil || !a.contextNoticesPersisted.Load() {
		return
	}
	// Scope before the mark: a reader that observes the mark must also observe
	// which classes it withdraws.
	a.contextNoticeWithdrawalScope.Store(contextNoticeWithdrawAll)
	a.contextNoticesStale.Store(true)
}

// armContextPressureNoticeCleanup arms idle cleanup of the reminder-class rows
// only, for a decision whose reminder line is gone while the compaction
// threshold still justifies the threshold-measured classes.
func (a *MainAgent) armContextPressureNoticeCleanup() {
	if a == nil || !a.contextNoticesPersisted.Load() {
		return
	}
	a.contextNoticeWithdrawalScope.Store(contextNoticeWithdrawPressure)
	a.contextNoticesStale.Store(true)
}

// disarmContextNoticeCleanup retains both notices after re-evaluation confirms
// that both thresholds still apply.
func (a *MainAgent) disarmContextNoticeCleanup() {
	if a == nil {
		return
	}
	a.contextNoticesStale.Store(false)
	a.contextNoticeWithdrawalScope.Store(contextNoticeWithdrawAll)
}

// omitStaleContextNoticesFromRequest drops the withdrawn durable context
// notices from a request-facing copy while idle cleanup has not yet rewritten
// main.jsonl. A scoped withdrawal removes only its own class. It does not change
// ctxmgr.
func (a *MainAgent) omitStaleContextNoticesFromRequest(messages []message.Message) []message.Message {
	if a == nil || !a.contextNoticesStale.Load() {
		return messages
	}
	return omitContextNoticeMessages(messages, a.contextNoticeWithdrawalScope.Load())
}

// dropContextNoticeMessages removes every durable context-pressure notice row
// from a rewritten message list. A successful compaction ends the cycle that
// wrote them, so the compacted context must not keep replaying a pre-apply
// pressure notice as if it still described current usage.
func dropContextNoticeMessages(messages []message.Message) []message.Message {
	n := 0
	for i := range messages {
		if messages[i].Kind == message.KindContextNotice {
			n++
		}
	}
	if n == 0 {
		return messages
	}
	out := make([]message.Message, 0, len(messages)-n)
	for i := range messages {
		if messages[i].Kind == message.KindContextNotice {
			continue
		}
		out = append(out, messages[i])
	}
	return out
}

func omitContextNoticeMessages(messages []message.Message, scope uint32) []message.Message {
	n := 0
	for i := range messages {
		if messages[i].Kind == message.KindContextNotice && contextNoticeStale(messages[i], scope) {
			n++
		}
	}
	if n == 0 {
		return messages
	}
	out := make([]message.Message, 0, len(messages)-n)
	for i := range messages {
		if messages[i].Kind == message.KindContextNotice && contextNoticeStale(messages[i], scope) {
			continue
		}
		out = append(out, messages[i])
	}
	return out
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
	a.notePressureStage(pressureStageArmed, a.currentOverlayWindowKey())
	a.stageContextNotice(contextNoticeWarning, compactionThresholdNoticeText)
}

// contextStateFileTargetHint gives both thresholds the same file target and
// naming guidance whenever checkpoint preparation calls for a file write.
const contextStateFileTargetHint = "a task-notes file under .chord/notes/ or a plan document under .chord/plans/, named with a YYYYMMDD date prefix such as 20260915-auth-token-refresh.md"

const contextCheckpointPressureAction = "If only the final response remains, deliver it without a checkpoint; if user input is required, use the normal question or waiting mechanism. Otherwise finish the current atomic operation, stop optional exploration, and update the reusable working details in " + contextStateFileTargetHint + " when permitted; register the saved file in state_files and keep structured arguments a concise handoff with the next action and relevant notes section. If the recovery state is small or file writing is unavailable, preserve it in structured arguments. Request a provisional checkpoint with compact_context alone when its preparation requirements are met; do not claim unfinished work is complete."

// buildContextPressureReminderText renders the full reminder text. It does not
// quote the current usage ratio or the remaining budget: the model cannot act
// on that number (compaction is already scheduled), and stating how much space
// is left would invite it to reason about deferring instead of preparing. It
// only ever runs while compact_context is visible, so it names the tool
// directly and distinguishes a safe stop from a completed phase. The text is
// bare content; the turn-overlay injector wraps it in a <system-reminder>
// block.
func buildContextPressureReminderText() string {
	return "The context is approaching the configured automatic-compaction threshold.\n" +
		contextCheckpointPressureAction
}

// appendContextPressureVerificationGuidance appends the post-apply guidance:
// only a successful checkpoint apply (model-driven, usage-driven, or oversize
// recovery that depended on it) adds it; skip/failure/non-checkpoint
// continuations never do.
func appendContextPressureVerificationGuidance(text string) string {
	return strings.TrimSpace(text) + "\n" + checkpointVerificationGuidance
}

// checkpointVerificationGuidance is the post-checkpoint reading rule, stated
// once and used by every continuation path (overlay and auto-continue prompt).
//
// The precedence clause exists because a checkpoint is the one place where
// model-authored text sits next to runtime facts in the same shape. Without an
// explicit order, a summary written before the last user message reads exactly
// like the user's current instruction, and a preserved Next Step outranks a
// todo list that has moved on. The order below is the authority model: newest
// user intent, then live runtime state, then the file system, then this
// conversation's tool results, then archived payloads, and only then the
// checkpoint's own prose.
const checkpointVerificationGuidance = "Before continuing, confirm that the preserved Current User Request and Next Step still match the actual state. " +
	"Use registered notes as the detail source: read the sections needed for the next action unless already injected, before repeating exploration. Reuse verified results while their code and conditions remain unchanged. " +
	"If the checkpoint conflicts with a newer source, the newer source wins, in this order: the latest user message or Done rejection, then current runtime state (todos, subagents, background tasks), then current source and configuration files, then tool results still in this conversation, then archived artifacts, then task notes, and only then the checkpoint's own text."
