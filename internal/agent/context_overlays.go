package agent

import (
	"strings"
	"sync"

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
// reminder and the grace-period "compaction imminent" notice. It binds to
// (session_epoch, compaction_window_id, model_ref, budget_epoch): a durable
// apply (model-driven or usage-driven), a session reset/restore, or a
// model/provider/budget switch changes one component and starts a fresh claim,
// so a reset that is followed by a new full reminder is intended — each
// compaction window delivers the full reminder text at most once.
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
// window is distinguished from the sticky repeats.
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
// reminder nudge has been answered and the sticky reminder must go quiet until
// a fresh window resets the claim. It binds the mark to the current (window,
// budget) key first, because the reminder claim is only synced lazily by the
// queue — a call that arrived after a background apply advanced the window
// must not stamp ccCalled onto the stale pre-apply claim.
func (a *MainAgent) markReminderCompactContextCalled() {
	key := a.currentOverlayWindowKey()
	a.overlayClaims.mu.Lock()
	defer a.overlayClaims.mu.Unlock()
	c := &a.overlayClaims.reminder
	c.bindTo(key)
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

// contextNotice is the text of one context overlay, staged so the dispatch
// confirmation point can surface it to the user as a card.
type contextNotice struct {
	level string
	text  string
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
// instead of losing a live-only notice. Repeat deliveries are suppressed: the
// reminder and the grace notice re-attach on every request in the window, and
// re-showing the card each time would bury the transcript in duplicates of the
// same warning.
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
		msg := message.Message{
			Role: message.RoleUser,
			Kind: message.KindContextNotice,
			// The durable row carries the same <system-reminder> wrapper every
			// other harness injection uses, so the model can tell it apart from
			// a user-written message when a later request replays the notice.
			// The live card is built from the bare event text below.
			Content:     "<system-reminder>\n" + text + "\n</system-reminder>",
			NoticeLevel: level,
		}
		messageIndex := a.ctxMgr.MessageCount()
		a.ctxMgr.Append(msg)
		a.persistAsyncAfter(identity.MainAgentID, msg, func(err error) {
			if err != nil {
				a.notePersistenceFailure(err)
				return
			}
			a.emitToTUI(ContextNoticeEvent{Level: level, Message: text, MessageIndex: messageIndex})
		})
	}
}

// maybeClearStaleContextNotices drops durable context-pressure notices after a
// model switch changed the effective compaction threshold or reminder line. A
// notice computed against the previous model's line can claim pressure the new
// model is nowhere near, and since the card is backed by the message both must
// go together. Runs on the event loop after dispatch, and only at an idle
// boundary (no active turn, no in-flight request, no running compaction) so the
// rewrite can never race request assembly, a compaction draft whose headSplit
// was measured against the current transcript, or a provider call that still
// holds the old transcript.
func (a *MainAgent) maybeClearStaleContextNotices() {
	if a == nil || a.ctxMgr == nil || !a.contextNoticesStale.Load() {
		return
	}
	if a.turn != nil || a.mainLLMRequestInFlight.Load() || a.IsCompactionRunning() {
		return
	}
	a.contextNoticesStale.Store(false)
	// Any notice queued for the next request was measured against the old
	// threshold; drop it so the request re-queues against the new model.
	a.pendingContextPressureReminder = ""
	a.pendingCompactionWarning = ""
	a.pendingCompactionImminent = ""
	a.resetContextNotices()
	a.flushPersist()
	messages := a.ctxMgr.Snapshot()
	kept := make([]message.Message, 0, len(messages))
	removed := false
	for _, msg := range messages {
		if msg.Kind == message.KindContextNotice {
			removed = true
			continue
		}
		kept = append(kept, msg)
	}
	if !removed {
		return
	}
	a.ctxMgr.RestoreMessages(kept)
	if manager := a.recoveryManager(); manager != nil {
		if err := manager.RewriteLog(identity.MainAgentID, kept); err != nil {
			a.notePersistenceFailure(err)
		}
	}
	a.emitToTUI(ContextNoticeClearedEvent{})
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
	claim := a.syncOverlayWindowClaim(&a.overlayClaims.reminder, a.currentOverlayWindowKey())
	// The model already called compact_context in this window: whatever
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

// contextPressureReminderShortText is the short re-attachment used on requests
// after the full reminder already dispatched in the same window. It must stay
// self-contained: reminders are request-scoped overlays rebuilt from scratch
// on every request, so a later request — or a fallback or replay of one — is
// never guaranteed to still carry the full notice this text would otherwise
// point back at. The short form therefore restates the action instead of
// referencing the earlier notice.
const contextPressureReminderShortText = "Context pressure is still active and the context may be compacted soon.\n" +
	"If the current atomic operation has ended and the state needed to resume is externalized, call compact_context alone with a provisional checkpoint if work remains; otherwise keep writing the active objective, completed work, next step, and open issues to a project file your role may write."

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
		"Finish the current atomic operation, then choose based on cost: if replacing the current history is cheaper than carrying it forward and the state needed to resume is fully externalized, request a durable context checkpoint by calling compact_context alone. Use a provisional checkpoint when the work remains active; a completed phase is not required.\n" +
		"If the operation is still in flight, or the state is not yet externalized, stop optional exploration and write the active objective, completed work, concrete next step, and open issues to project files your role may write (for example a task-notes file under .chord/notes/, named with a YYYYMMDD date prefix, or a plan document under .chord/plans/) as they settle. Do not interrupt the operation or describe unfinished work as completed."
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
	"Re-read any referenced state_files when needed before acting. " +
	"If the checkpoint conflicts with a newer source, the newer source wins, in this order: the latest user message or Done rejection, then current runtime state (todos, subagents, background tasks), then the files on disk, then tool results still in this conversation, then archived artifacts, and only then the checkpoint's own text."
