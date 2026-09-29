package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// manualIntentAgent returns an idle agent with compact_context visible and a
// long-enough history for a busy /compact scenario.
func manualIntentAgent(t *testing.T) *MainAgent {
	t.Helper()
	a := newTestMainAgent(t, t.TempDir())
	for _, content := range []string{"one", "two", "three", "four"} {
		a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: content})
	}
	enableTestCompactContext(a)
	return a
}

func manualNoticeRow(cycleID uint64) message.Message {
	return message.Message{
		Role:            message.RoleUser,
		Kind:            message.KindContextNotice,
		NoticeLevel:     contextNoticeManual,
		PressureCycleID: cycleID,
		Content:         "<system-reminder>\n" + manualCompactionNoticeText + "\n</system-reminder>",
	}
}

func TestHandleCompactCommandBusyArmsManualIntent(t *testing.T) {
	a := manualIntentAgent(t)
	a.newTurn()

	a.handleCompactCommand()
	if !a.IsCompactionRunning() {
		t.Fatal("busy /compact must start the summarize worker immediately")
	}
	if !a.manualNoticeActive() {
		t.Fatal("busy /compact with a visible compact_context must arm the manual intent")
	}
	if got := a.manualCompactArmedPlanID.Load(); got != a.compactionState.planID {
		t.Fatalf("armed plan id = %d, want the started worker's plan id %d", got, a.compactionState.planID)
	}
	if a.compactionState.trigger != compactionTriggerManual {
		t.Fatalf("trigger = %q, want manual", a.compactionState.trigger)
	}
}

func TestHandleCompactCommandIdleDoesNotArm(t *testing.T) {
	a := manualIntentAgent(t)

	a.handleCompactCommand()
	if !a.IsCompactionRunning() {
		t.Fatal("idle /compact must start the summarize worker")
	}
	if a.manualNoticeActive() {
		t.Fatal("idle /compact must not arm the manual intent")
	}
}

func TestHandleCompactCommandInvisibleToolDoesNotArm(t *testing.T) {
	a := manualIntentAgent(t)
	a.modelDrivenCompactionEnabled.Store(false)
	a.newTurn()

	a.handleCompactCommand()
	if !a.IsCompactionRunning() {
		t.Fatal("busy /compact without the model-driven tool must still start the worker")
	}
	if a.manualNoticeActive() {
		t.Fatal("an invisible compact_context must not arm the manual intent")
	}
}

func TestHandleCompactCommandWhileWorkerRunningKeepsIntent(t *testing.T) {
	a := manualIntentAgent(t)
	a.newTurn()
	a.handleCompactCommand()
	if !a.manualNoticeActive() {
		t.Fatal("premise: the first command armed the intent")
	}
	planID := a.compactionState.planID

	a.handleCompactCommand()
	if a.compactionState.planID != planID {
		t.Fatal("a repeated /compact while the worker runs must not start a second worker")
	}
	if got := a.manualCompactArmedPlanID.Load(); got != planID {
		t.Fatalf("armed plan id = %d, want the original intent's %d", got, planID)
	}
}

func TestHandleCompactCommandArmedWithoutWorkerReschedules(t *testing.T) {
	a := manualIntentAgent(t)
	a.newTurn()
	a.handleCompactCommand()
	if !a.manualNoticeActive() {
		t.Fatal("premise: the first command armed the intent")
	}
	// The model's checkpoint displaced the worker and settled without apply:
	// armed but no worker running.
	a.discardCompactionForModelOverride()
	a.handleCompactCommand()
	if !a.IsCompactionRunning() {
		t.Fatal("a repeated /compact with armed intent and no worker must reschedule")
	}
}

func TestManualGateInjectsNoticeOnce(t *testing.T) {
	a := manualIntentAgent(t)
	a.newTurn()
	a.handleCompactCommand()
	planID := a.compactionState.planID

	// The worker is still running at the next gate: the notice stages for this
	// request, and lower-priority pending texts yield to it.
	a.setPendingContextNoticeText(contextNoticeWarning, compactionThresholdNoticeText)
	a.setPendingContextNoticeText(contextNoticePressure, buildContextPressureReminderText())
	a.handleManualCompactionIntentAtGate()
	if a.pendingCompactionManual == "" {
		t.Fatal("the gate must stage the manual imperative for the armed worker")
	}
	if a.pendingCompactionWarning != "" || a.pendingContextPressureReminder != "" {
		t.Fatalf("the manual notice must clear undelivered lower pending texts, got warning %q reminder %q", a.pendingCompactionWarning, a.pendingContextPressureReminder)
	}

	// Dispatch confirms the claim and writes the durable row with the cycle
	// that the injection opened.
	a.buildTurnOverlayMessages()
	a.markOverlayClaimsDelivered()
	a.flushPersist()
	if !containsContextNoticeLevel(a.ctxMgr.Snapshot(), contextNoticeManual) {
		t.Fatal("the delivered manual notice must persist a durable row")
	}
	if a.overlayClaims.pressure.id == 0 {
		t.Fatal("the manual row must be reserved inside an open pressure cycle")
	}
	evt := waitForContextNoticeEvent(t, a)
	if evt.Level != contextNoticeManual || evt.Message != manualCompactionNoticeText {
		t.Fatalf("card event = %+v, want the manual level and bare text", evt)
	}

	// Later gates replay the durable row instead of re-staging: the claim is
	// spent and the durable row suppresses a fresh delivery.
	a.handleManualCompactionIntentAtGate()
	if a.pendingCompactionManual != "" {
		t.Fatalf("a delivered manual intent must not re-stage, got %q", a.pendingCompactionManual)
	}
	if got := a.manualCompactArmedPlanID.Load(); got != planID {
		t.Fatalf("armed plan id = %d, want %d", got, planID)
	}
}

func TestManualGateRestartsWorkerWithoutInjecting(t *testing.T) {
	a := manualIntentAgent(t)
	a.newTurn()
	a.handleCompactCommand()
	armedPlanID := a.compactionState.planID
	// The model's checkpoint displaced the worker and settled without apply.
	a.discardCompactionForModelOverride()

	a.handleManualCompactionIntentAtGate()
	if !a.IsCompactionRunning() {
		t.Fatal("an armed intent without a worker must restart the manual worker at the gate")
	}
	if a.compactionState.planID == armedPlanID {
		t.Fatal("the restarted worker must carry a fresh plan id")
	}
	if a.manualCompactArmedPlanID.Load() != armedPlanID {
		t.Fatal("the armed record keeps the original plan id")
	}
	if a.pendingCompactionManual != "" {
		t.Fatalf("the restart must not inject the notice again, got %q", a.pendingCompactionManual)
	}
}

func TestManualArmedSuppressesAutoNoticesAndKeepsWarningClaim(t *testing.T) {
	a := manualIntentAgent(t)
	a.newTurn()
	a.handleCompactCommand()
	a.requestBatches.reserve(a.sessionEpoch, 0)

	// The threshold warning's claim stays unconsumed while the manual intent
	// suppresses every usage-driven notice.
	a.armUsageDrivenAutoCompactRequest()
	generation := a.autoCompactRequestGeneration.Load()
	a.queueCompactionWarning()
	if a.pendingCompactionWarning != "" {
		t.Fatalf("the armed manual intent must suppress the warning, got %q", a.pendingCompactionWarning)
	}
	if a.overlayClaims.warning.requestID != 0 || a.overlayClaims.warning.delivered {
		t.Fatal("the suppressed warning must not touch its per-generation claim")
	}
	_ = generation
	a.queueContextPressureReminderForNextRequest()
	if a.pendingContextPressureReminder != "" {
		t.Fatalf("the armed manual intent must suppress the reminder, got %q", a.pendingContextPressureReminder)
	}
	a.queueCompactionImminentNotice()
	if a.pendingCompactionImminent != "" {
		t.Fatalf("the armed manual intent must suppress the imminent countdown, got %q", a.pendingCompactionImminent)
	}
}

func TestManualWorkerSkipClearsIntentAndWithdrawsNotice(t *testing.T) {
	a := manualIntentAgent(t)
	a.newTurn()
	a.handleCompactCommand()
	a.handleManualCompactionIntentAtGate()
	a.buildTurnOverlayMessages()
	a.markOverlayClaimsDelivered()
	a.flushPersist()
	if !containsContextNoticeLevel(a.ctxMgr.Snapshot(), contextNoticeManual) {
		t.Fatal("premise: the notice row is durable")
	}

	// A manual skip is terminal: the intent clears and the row withdraws.
	draft := &compactionDraft{
		Skip:           true,
		TooFewMessages: true,
		InfoMessage:    "Not enough history to compact.",
		Manual:         true,
		PlanID:         a.compactionState.planID,
		Target:         a.compactionState.target,
	}
	if err := a.applyCompactionDraft(draft); err != nil {
		t.Fatalf("applyCompactionDraft(skip) = %v", err)
	}
	if a.manualNoticeActive() {
		t.Fatal("a manual worker skip must clear the armed intent")
	}
	if got := a.omitStaleContextNoticesFromRequest(a.ctxMgr.Snapshot()); containsContextNoticeLevel(got, contextNoticeManual) {
		t.Fatal("the withdrawn manual row must be omitted from requests before the idle sweep")
	}
	// The worker's skip already settled it above; release the slot so the
	// idle-boundary sweep may run.
	a.resetCompactionState()
	a.contextNoticeWithdrawalScope.Store(contextNoticeWithdrawAll)
	a.contextNoticesStale.Store(true)
	a.turn = nil
	a.maybeClearStaleContextNotices()
	if containsContextNoticeLevel(a.ctxMgr.Snapshot(), contextNoticeManual) {
		t.Fatal("the idle sweep must remove the withdrawn manual row")
	}
}

func TestModelSettleKeepsIntentAndWithdrawsNotice(t *testing.T) {
	a := manualIntentAgent(t)
	a.newTurn()
	a.handleCompactCommand()
	a.handleManualCompactionIntentAtGate()
	a.buildTurnOverlayMessages()
	a.markOverlayClaimsDelivered()
	a.flushPersist()
	if !containsContextNoticeLevel(a.ctxMgr.Snapshot(), contextNoticeManual) {
		t.Fatal("premise: the notice row is durable")
	}

	// The model's checkpoint settled with a skip: the imperative was answered,
	// so the row withdraws while the intent stays armed for the restart.
	a.settleModelDrivenOutcome(CompactionStatusSkipped, "projected savings too small", nil, 0)
	if !a.manualNoticeActive() {
		t.Fatal("a model settle without apply must keep the armed intent")
	}
	if got := a.omitStaleContextNoticesFromRequest(a.ctxMgr.Snapshot()); containsContextNoticeLevel(got, contextNoticeManual) {
		t.Fatal("the answered manual row must be omitted from requests")
	}
}

func TestSettlePointRestartsArmedWorkerWhenTurnDead(t *testing.T) {
	a := manualIntentAgent(t)
	a.newTurn()
	a.handleCompactCommand()
	armedPlanID := a.compactionState.planID
	a.discardCompactionForModelOverride()
	a.turn = nil

	a.restartManualCompactionIfArmedIdle()
	if !a.IsCompactionRunning() {
		t.Fatal("a dead turn with an armed intent must restart the worker at the settle point")
	}
	if a.compactionState.planID == armedPlanID {
		t.Fatal("the restarted worker must carry a fresh plan id")
	}
	if a.pendingCompactionManual != "" {
		t.Fatalf("the settle-point restart must not inject, got %q", a.pendingCompactionManual)
	}
	if a.compactionState.continuation.kind != compactionResumeIdle {
		t.Fatalf("continuation = %q, want the idle continuation", a.compactionState.continuation.kind)
	}
}

func TestSettlePointSkipsRestartWhenTurnAlive(t *testing.T) {
	a := manualIntentAgent(t)
	a.newTurn()
	a.handleCompactCommand()
	a.discardCompactionForModelOverride()

	a.restartManualCompactionIfArmedIdle()
	if a.IsCompactionRunning() {
		t.Fatal("a live turn leaves the restart to the gate, not the settle point")
	}
	if !a.manualNoticeActive() {
		t.Fatal("the intent must stay armed until a gate or settle point restarts the worker")
	}
}

func TestTurnCancellationClearsIntentButKeepsWorker(t *testing.T) {
	a := manualIntentAgent(t)
	a.newTurn()
	a.handleCompactCommand()
	a.handleManualCompactionIntentAtGate()
	a.buildTurnOverlayMessages()
	a.markOverlayClaimsDelivered()
	a.flushPersist()
	planID := a.compactionState.planID

	a.handleTurnCancelled(Event{
		TurnID:  a.turn.ID,
		Payload: &TurnCancelledPayload{},
	})
	if a.manualNoticeActive() {
		t.Fatal("ESC must clear the armed manual intent")
	}
	if got := a.omitStaleContextNoticesFromRequest(a.ctxMgr.Snapshot()); containsContextNoticeLevel(got, contextNoticeManual) {
		t.Fatal("ESC must withdraw the manual notice row from requests")
	}
	// The manual worker itself survives the cancellation (unlike the
	// model-driven checkpoint) and still owns its slot.
	if !a.IsCompactionRunning() || a.compactionState.planID != planID {
		t.Fatal("ESC must not discard the manual worker")
	}
}

func TestSessionSwitchClearsManualIntent(t *testing.T) {
	a := manualIntentAgent(t)
	a.newTurn()
	a.handleCompactCommand()
	if !a.manualNoticeActive() {
		t.Fatal("premise: the command armed the intent")
	}

	a.clearManualCompactionIntent()
	if a.manualNoticeActive() {
		t.Fatal("a session switch must clear the armed intent")
	}
}

func TestRestoreWithdrawsOrphanManualRow(t *testing.T) {
	a := manualIntentAgent(t)
	restored := manualNoticeRow(7)
	messages := []message.Message{{Role: message.RoleUser, Content: "hello"}, restored}
	a.ctxMgr.RestoreMessages(messages)
	a.installContextNoticePresence(messages)

	if a.manualNoticeActive() {
		t.Fatal("the armed intent is runtime memory and must not survive a restore")
	}
	if got := a.omitStaleContextNoticesFromRequest(a.ctxMgr.Snapshot()); containsContextNoticeLevel(got, contextNoticeManual) {
		t.Fatal("a restored manual row without an armed intent must be omitted from requests")
	}
	a.maybeClearStaleContextNotices()
	if containsContextNoticeLevel(a.ctxMgr.Snapshot(), contextNoticeManual) {
		t.Fatal("the idle boundary must remove the orphan manual row")
	}
}

func TestReconcileKeepsManualRowWhileArmed(t *testing.T) {
	a := manualIntentAgent(t)
	row := manualNoticeRow(1)
	messages := []message.Message{{Role: message.RoleUser, Content: "hello"}, row}
	a.ctxMgr.RestoreMessages(messages)
	a.installContextNoticePresence(messages)
	a.manualCompactArmedPlanID.Store(3)
	a.manualCompactArmed.Store(true)

	// A prefix reduction before the row triggers the re-evaluation; the armed
	// intent keeps the row justified (reused, not withdrawn or re-carded).
	budget := 100000 + a.effectiveCompactionReservedInput()
	got := a.reconcilePressureNoticesForModel(messages, messages, "provider/other-model", budget)
	if !containsContextNoticeLevel(got, contextNoticeManual) {
		t.Fatal("an armed intent must keep the manual row on the request")
	}
	if a.pendingCompactionManual != "" {
		t.Fatalf("the reconcile must not re-card the manual notice, got %q", a.pendingCompactionManual)
	}

	// Without the armed intent the row is no longer justified.
	a.manualCompactArmed.Store(false)
	got = a.reconcilePressureNoticesForModel(messages, messages, "provider/other-model", budget)
	if containsContextNoticeLevel(got, contextNoticeManual) {
		t.Fatal("a manual row without an armed intent must be omitted from requests")
	}
}

func TestAbruptCrossingBypassesGraceAndStacksNoReminder(t *testing.T) {
	a := manualIntentAgent(t)
	const budget = 1000000
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(budget, budget, 0, 0.8)
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: int(budget * 0.85)})
	a.requestBatches.reserve(a.sessionEpoch, 0)
	snapshot := a.ctxMgr.Snapshot()

	// The first observation of the window is already past the threshold and no
	// reminder was ever delivered: the grace has no one to serve.
	if a.usageDrivenCompactionGraceDefers(snapshot) {
		t.Fatal("an abrupt crossing must bypass the grace")
	}
	if !a.compactionGraceExhausted {
		t.Fatal("the abrupt bypass must spend the window's grace")
	}
	// The crossing request carries a single warning and no reminder.
	a.requestBatches.reserve(a.sessionEpoch, 1)
	a.armUsageDrivenAutoCompactRequest()
	a.queueCompactionWarning()
	a.queueContextPressureReminderForNextRequest()
	if a.pendingCompactionWarning == "" {
		t.Fatal("the abrupt crossing must stage the externalization warning")
	}
	if a.pendingContextPressureReminder != "" {
		t.Fatalf("the abrupt crossing must not stack the reminder, got %q", a.pendingContextPressureReminder)
	}
}

func TestAbruptCrossingRequiresUndeliveredReminder(t *testing.T) {
	a := manualIntentAgent(t)
	const budget = 1000000
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(budget, budget, 0, 0.8)
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: int(budget * 0.85)})
	a.requestBatches.reserve(a.sessionEpoch, 0)
	// The gradual climb delivered the sticky reminder earlier in the window.
	a.notePressureStage(pressureStageReminded, a.currentOverlayWindowKey())
	a.syncOverlayWindowClaim(&a.overlayClaims.reminder, a.currentOverlayWindowKey())
	a.overlayClaims.mu.Lock()
	a.overlayClaims.reminder.delivered = true
	a.overlayClaims.mu.Unlock()

	if !a.usageDrivenCompactionGraceDefers(a.ctxMgr.Snapshot()) {
		t.Fatal("a gradual climb must keep the grace deferral")
	}
}

func TestManualNoticeJoinsPressureCycleRecord(t *testing.T) {
	a := manualIntentAgent(t)
	a.notePressureStage(pressureStageArmed, a.currentOverlayWindowKey())
	a.stashContextNotice(contextNoticeManual, manualCompactionNoticeText)
	a.noteCompactionManualAttached()
	a.markOverlayClaimsDelivered()
	a.flushPersist()

	row := manualNoticeRow(0)
	for _, msg := range a.ctxMgr.Snapshot() {
		if msg.Kind == message.KindContextNotice && msg.NoticeLevel == contextNoticeManual {
			row = msg
		}
	}
	if row.PressureCycleID == 0 {
		t.Fatal("the manual row must carry the open cycle's identity")
	}
	if a.overlayClaims.pressure.id != row.PressureCycleID {
		t.Fatalf("cycle id = %d, want the row's %d", a.overlayClaims.pressure.id, row.PressureCycleID)
	}
	if !a.overlayClaims.pressure.recorded[pressureNoticeSlot(contextNoticeManual)] {
		t.Fatal("the manual level must be recorded in the cycle")
	}

	// A repeat delivery in the same cycle is suppressed as already recorded.
	a.stashContextNotice(contextNoticeManual, manualCompactionNoticeText)
	a.noteCompactionManualAttached()
	count := a.ctxMgr.MessageCount()
	a.markOverlayClaimsDelivered()
	if a.ctxMgr.MessageCount() != count {
		t.Fatal("a cycle must record the manual level at most once")
	}
}

func TestWithdrawalScopeBitmaskCoversManualSlot(t *testing.T) {
	if pressureNoticeSlot(contextNoticeManual) != 2 {
		t.Fatalf("manual slot = %d, want 2", pressureNoticeSlot(contextNoticeManual))
	}
	if !pressureSlotWithdrawn(2, contextNoticeWithdrawManual) || pressureSlotWithdrawn(0, contextNoticeWithdrawManual) {
		t.Fatal("the manual scope must withdraw only the manual slot")
	}
	if !pressureSlotWithdrawn(2, contextNoticeWithdrawAll) || !pressureSlotWithdrawn(0, contextNoticeWithdrawAll) {
		t.Fatal("the full scope must withdraw every slot")
	}
	if pressureSlotWithdrawn(2, contextNoticeWithdrawAll&^contextNoticeWithdrawManual) {
		t.Fatal("the full scope minus the manual bit must keep manual rows")
	}
}

func TestManualCompactionNoticeTextIsSelfContained(t *testing.T) {
	if !strings.Contains(manualCompactionNoticeText, "compact_context") {
		t.Fatal("the imperative must name the tool")
	}
	if !strings.Contains(manualCompactionNoticeText, "only tool call") {
		t.Fatal("the imperative must state the sole-call constraint")
	}
	if strings.Contains(manualCompactionNoticeText, "<") {
		t.Fatal("the text must stay bare content without angle brackets")
	}
}

// TestManualGateAfterReadyApplyWritesNoNotice pins the worker-first race: when
// the summarize worker's draft is ready before the next gate, the apply
// consumes the intent and the gate writes no notice row.
func TestManualGateAfterReadyApplyWritesNoNotice(t *testing.T) {
	a := manualIntentAgent(t)
	a.newTurn()
	a.handleCompactCommand()
	a.armManualCompactionIntent(a.compactionState.planID)

	// Simulate the worker reaching the barrier-ready state and the gate
	// applying it before any manual handling.
	draft := &compactionDraft{
		PlanID:                a.compactionState.planID,
		Target:                a.compactionState.target,
		NewMessages:           []message.Message{{Role: message.RoleUser, Content: "[Context Summary]\nsummary", IsCompactionSummary: true}},
		HeadSplit:             3,
		AbsHistoryPath:        "",
		SummaryMode:           message.CompactionSummaryModeTruncateOnly,
		Manual:                true,
		ArchivedCount:         3,
		TransactionSessionDir: a.sessionDir,
	}
	a.compactionState.readyDraft = draft
	a.compactionState.headSplit = 3
	if _, handled := a.applyReadyDraft(); handled {
		t.Fatal("the idle barrier must not take over the gate flow")
	}
	if a.manualNoticeActive() {
		t.Fatal("the apply must clear the armed intent")
	}
	a.handleManualCompactionIntentAtGate()
	if a.pendingCompactionManual != "" {
		t.Fatalf("a satisfied intent must not stage the notice, got %q", a.pendingCompactionManual)
	}
	if containsContextNoticeLevel(a.ctxMgr.Snapshot(), contextNoticeManual) {
		t.Fatal("no manual row may be written when the worker won the race")
	}
}

// TestManualRestartWorkerAppliesWithoutNotice drives the full restart loop:
// the gate restarts the displaced worker, the restarted worker (fresh plan id)
// never injects, and its apply clears the armed intent.
func TestManualRestartWorkerAppliesWithoutNotice(t *testing.T) {
	a := manualIntentAgent(t)
	a.newTurn()
	a.handleCompactCommand()
	armedPlanID := a.compactionState.planID
	a.discardCompactionForModelOverride()

	a.handleManualCompactionIntentAtGate()
	if !a.IsCompactionRunning() {
		t.Fatal("premise: the gate restarted the worker")
	}
	draft := &compactionDraft{
		PlanID:                a.compactionState.planID,
		Target:                a.compactionState.target,
		NewMessages:           []message.Message{{Role: message.RoleUser, Content: "[Context Summary]\nsummary", IsCompactionSummary: true}},
		HeadSplit:             3,
		SummaryMode:           message.CompactionSummaryModeTruncateOnly,
		Manual:                true,
		ArchivedCount:         3,
		TransactionSessionDir: a.sessionDir,
	}
	a.compactionState.readyDraft = draft
	a.compactionState.headSplit = 3
	if _, handled := a.applyReadyDraft(); handled {
		t.Fatal("the idle barrier must not take over the gate flow")
	}
	if a.manualNoticeActive() {
		t.Fatal("the restarted worker's apply must clear the armed intent")
	}
	if a.manualCompactArmedPlanID.Load() != armedPlanID {
		t.Fatal("the armed record never changed during the restart loop")
	}
	if containsContextNoticeLevel(a.ctxMgr.Snapshot(), contextNoticeManual) {
		t.Fatal("the restart loop must not write a manual row")
	}
}

func TestManualApplyFailureSettlesIntent(t *testing.T) {
	for _, path := range []string{"barrier", "idle", "model_checkpoint"} {
		t.Run(path, func(t *testing.T) {
			a := manualIntentAgent(t)
			a.ctxMgr.Append(manualNoticeRow(1))
			a.contextNoticesPersisted.Store(true)
			a.armManualCompactionIntent(1)
			target := compactionTarget{sessionEpoch: a.sessionEpoch}
			a.startCompactionState(1, target, compactionTriggerManual, continuationPlan{kind: compactionResumeIdle})
			count := a.ctxMgr.MessageCount()
			draft := &compactionDraft{
				PlanID:      1,
				Target:      target,
				Manual:      path != "model_checkpoint",
				HeadSplit:   count + 1,
				SourceRefs:  []checkpointSourceRef{{}},
				NewMessages: []message.Message{{Role: message.RoleUser, Content: "summary", IsCompactionSummary: true}},
			}
			if !draft.Manual {
				a.startCompactionState(2, target, compactionTriggerModelDriven, continuationPlan{kind: compactionResumeModelDriven})
				draft.PlanID = 2
				draft.SummaryMode = compactionSummaryModeModelDriven
			}
			// The captured prefix exceeds the live history, so apply fails before
			// rewriting the transcript or relying on filesystem permissions.
			switch path {
			case "barrier":
				a.newTurn()
				a.compactionState.readyDraft = draft
				if applied, _ := a.applyReadyDraft(); applied {
					t.Fatal("an invalid source boundary must not apply")
				}
			case "idle":
				a.handleCompactionReady(Event{Type: EventCompactionReady, Payload: draft})
			case "model_checkpoint":
				if err := a.applyCompactionDraft(draft); err == nil {
					t.Fatal("an invalid source boundary must fail")
				}
				if !a.manualNoticeActive() {
					t.Fatal("a failed model checkpoint must preserve the displaced manual intent")
				}
				return
			}
			if a.manualNoticeActive() || a.IsCompactionRunning() {
				t.Fatal("a failed manual apply must settle the intent and release the slot")
			}
			if got := a.omitStaleContextNoticesFromRequest(a.ctxMgr.Snapshot()); containsContextNoticeLevel(got, contextNoticeManual) {
				t.Fatal("a failed manual apply must withdraw the imperative notice")
			}
			if a.ctxMgr.MessageCount() != count {
				t.Fatal("a failed apply must leave the transcript unchanged")
			}
			if a.autoCompactFailureState.ConsecutiveFailures != 0 || a.autoCompactRequested.Load() {
				t.Fatal("a manual apply failure must leave the usage-driven breaker untouched")
			}
			a.handleManualCompactionIntentAtGate()
			a.turn = nil
			a.restartManualCompactionIfArmedIdle()
			if a.IsCompactionRunning() {
				t.Fatal("a failed manual apply must not restart at the gate or idle settle")
			}
		})
	}
}

// TestManualWorkerFailureKeepsBreakerUntouched pins that a manual worker
// failure is terminal without touching the usage-driven failure breaker.
func TestManualWorkerFailureKeepsBreakerUntouched(t *testing.T) {
	a := manualIntentAgent(t)
	a.newTurn()
	a.handleCompactCommand()
	a.handleCompactionFailed(Event{Payload: &compactionFailure{
		planID: a.compactionState.planID,
		target: a.compactionState.target,
		err:    errCompactionNoModelAvailable,
	}})
	if a.manualNoticeActive() {
		t.Fatal("a manual worker failure must clear the armed intent")
	}
	if a.IsCompactionRunning() {
		t.Fatal("the failure must release the compaction slot")
	}
	if a.autoCompactFailureState.ConsecutiveFailures != 0 {
		t.Fatalf("manual failures must not count into the usage-driven breaker, got %d", a.autoCompactFailureState.ConsecutiveFailures)
	}
	if a.autoCompactRequested.Load() {
		t.Fatal("a manual failure must not touch the usage-driven request")
	}
}

// TestManualGateVisibleCheckKeepsWorkerWithoutInjection covers the visibility
// edge: an armed intent whose tool surface lost compact_context still restarts
// nothing new while the worker runs, and injects nothing.
func TestManualIntentRequiresVisibleToolForInjection(t *testing.T) {
	a := manualIntentAgent(t)
	a.newTurn()
	a.handleCompactCommand()
	if !a.manualNoticeActive() {
		t.Fatal("premise: the command armed the intent")
	}
	a.tools = tools.NewRegistry()

	a.handleManualCompactionIntentAtGate()
	if !a.IsCompactionRunning() {
		t.Fatal("the armed worker keeps running regardless of visibility")
	}
	if a.pendingCompactionManual != "" {
		t.Fatal("an invisible compact_context must not receive the imperative")
	}
}

// TestManualArmedSurvivesUsageResets pins the lifecycle rule: usage-driven
// resets (usage falling back, the failure breaker, a model switch) never clear
// the manual intent — only applies, worker terminals, cancellation, and
// session boundaries do.
func TestManualArmedSurvivesUsageResets(t *testing.T) {
	a := manualIntentAgent(t)
	a.newTurn()
	a.handleCompactCommand()
	a.manualCompactArmedPlanID.Store(a.compactionState.planID)

	a.clearUsageDrivenAutoCompactRequest()
	a.resetAutoCompactionFailureState()
	a.appliedCompactionModelRef = "provider/previous-model"
	a.applyModelCompactionConfig()
	if !a.manualNoticeActive() {
		t.Fatal("usage-driven resets and model switches must not clear the armed intent")
	}
}
