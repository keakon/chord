package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestContextPressureReminderClaimWindowBinding(t *testing.T) {
	a := &MainAgent{}
	// Fresh window: claimable.
	if !a.tryClaimContextPressureReminder(1, 0, 0) {
		t.Fatal("first claim in a fresh window must be granted")
	}
	// Same window before any delivery: still claimable (the queue is a
	// per-request attempt until the request actually dispatches).
	if !a.tryClaimContextPressureReminder(1, 0, 0) {
		t.Fatal("same-window claim before delivery must be granted")
	}
	// Delivered: same window is now spent.
	a.noteContextPressureReminderAttached()
	a.markOverlayClaimsDelivered()
	if a.tryClaimContextPressureReminder(1, 0, 0) {
		t.Fatal("same-window claim after delivery must be rejected")
	}
	// A new compaction window (compaction index advanced) resets the claim.
	if !a.tryClaimContextPressureReminder(1, 1, 0) {
		t.Fatal("new window index must reset the reminder claim")
	}
	// A new budget epoch (model/provider/budget switch) also resets it.
	if !a.tryClaimContextPressureReminder(1, 1, 1) {
		t.Fatal("new budget epoch must reset the reminder claim")
	}
	// A session switch (new session epoch) resets it too.
	if !a.tryClaimContextPressureReminder(2, 0, 0) {
		t.Fatal("new session epoch must reset the reminder claim")
	}
	// Attached but never dispatched (pre-dispatch cancellation): the claim
	// stays reusable for the retried request.
	a.noteContextPressureReminderAttached()
	if !a.tryClaimContextPressureReminder(2, 0, 0) {
		t.Fatal("attached-but-not-delivered claim must remain reusable after a cancelled dispatch")
	}
}

func TestCompactionWarningClaimGenerationBinding(t *testing.T) {
	a := &MainAgent{}
	// First arm (generation 1), request batch 3.
	if !a.tryClaimCompactionWarning(1, 3) {
		t.Fatal("first warning claim for a fresh generation must be granted")
	}
	// Same generation, same batch, not yet dispatched: the retried request may
	// re-claim (rollback semantics).
	if !a.tryClaimCompactionWarning(1, 3) {
		t.Fatal("same-generation same-batch claim before dispatch must be granted")
	}
	// Dispatch confirms delivery; the batch then advances.
	a.noteCompactionWarningAttached()
	a.markOverlayClaimsDelivered()
	if a.tryClaimCompactionWarning(1, 3) {
		t.Fatal("delivered claim must not be re-granted for the same batch")
	}
	// Next request carries the next batch: the same generation can never claim
	// again (the per-generation budget is spent).
	if a.tryClaimCompactionWarning(1, 4) {
		t.Fatal("same generation with an advanced batch must not re-claim")
	}
	// A re-armed request (new generation after apply) starts a fresh claim.
	if !a.tryClaimCompactionWarning(2, 4) {
		t.Fatal("a new generation must reset the warning claim")
	}
}

func TestQueueContextPressureReminderGates(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(8192, 8192, 0, 0.9)

	// Auto-compact off (threshold 0): never a reminder, even with model-driven
	// enabled — there is no usage-driven safety net to justify the nudge.
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(8192, 8192, 0, 0)
	a.ctxMgr.SetLastTotalContextTokens(7000)
	a.queueContextPressureReminder(a.ctxMgr.AutoCompactDecision())
	if a.pendingContextPressureReminder != "" {
		t.Fatalf("threshold=0 must not queue a reminder, got %q", a.pendingContextPressureReminder)
	}

	// Below the reminder line (0.6 for threshold 0.9): no reminder.
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(8192, 8192, 0, 0.9)
	a.ctxMgr.SetLastTotalContextTokens(4000) // 4000/8192 ≈ 0.49 < 0.60
	a.queueContextPressureReminder(a.ctxMgr.AutoCompactDecision())
	if a.pendingContextPressureReminder != "" {
		t.Fatalf("below the reminder line must not queue a reminder, got %q", a.pendingContextPressureReminder)
	}

	// Above the reminder line while model-driven compaction is off (the
	// compact_context tool is not visible): no reminder. Without an
	// externalization contract the model cannot act on the overlay, and quoting
	// usage numbers would only invite it to reason about how much space is left
	// instead of preparing — automatic compaction is runtime-owned in this
	// mode, like Codex's local and remote paths that never notify the working
	// model.
	a.ctxMgr.SetLastTotalContextTokens(5000) // 5000/8192 ≈ 0.61 >= 0.60
	a.queueContextPressureReminder(a.ctxMgr.AutoCompactDecision())
	if a.pendingContextPressureReminder != "" {
		t.Fatalf("model-driven off must not queue a reminder, got %q", a.pendingContextPressureReminder)
	}

	// Model-driven on with the tool registered: the reminder is queued above
	// the line, tells the model to prepare for the compaction instead of
	// quoting how much space is left, and names compact_context.
	a.modelDrivenCompactionEnabled.Store(true)
	a.tools.Register(tools.NewCompactContextTool(tools.CompactContextValidator{ContinuationStateMaxTokens: 2048}))
	a.queueContextPressureReminder(a.ctxMgr.AutoCompactDecision())
	reminder := a.pendingContextPressureReminder
	if reminder == "" {
		t.Fatal("tool-visible session must queue a reminder above the line")
	}
	if !strings.Contains(reminder, "compact_context") || !strings.Contains(reminder, "automatic-compaction threshold") || !strings.Contains(reminder, ".chord/notes/") {
		t.Fatalf("reminder text = %q, want actionable text naming compact_context and a concrete write target", reminder)
	}
	if strings.Contains(reminder, "<context-pressure>") || strings.Contains(reminder, "<system-reminder>") {
		t.Fatalf("queued reminder must be bare text; the injector wraps it in <system-reminder>, got %q", reminder)
	}
	if strings.Contains(reminder, "approximately") || strings.Contains(reminder, "tokens remaining") {
		t.Fatalf("reminder must not quote usage numbers, got %q", reminder)
	}
	a.pendingContextPressureReminder = ""

	// A delivered claim suppresses the reminder for the same window even when
	// the ratio stays above the line.
	a.pendingContextPressureReminder = ""
	a.noteContextPressureReminderAttached()
	a.markOverlayClaimsDelivered()
	a.queueContextPressureReminder(a.ctxMgr.AutoCompactDecision())
	if a.pendingContextPressureReminder != "" {
		t.Fatalf("same-window reminder must be delivered at most once, got %q", a.pendingContextPressureReminder)
	}
}

func TestQueueCompactionWarningLifecycle(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)

	// Not armed: no warning.
	a.queueCompactionWarning()
	if a.pendingCompactionWarning != "" {
		t.Fatalf("no armed request must not queue a warning, got %q", a.pendingCompactionWarning)
	}

	// Armed while model-driven compaction is off (the compact_context tool is
	// not visible): no warning either. The model has no externalization
	// contract in that mode, and automatic compaction is runtime-owned — like
	// Codex's local and remote paths, which never notify the working model —
	// so an unactionable warning would only be read as conversation noise.
	a.requestBatches.reserve(a.sessionEpoch, 0) // batch 1 = last completed request
	a.armUsageDrivenAutoCompactRequest()        // generation 1
	a.queueCompactionWarning()
	if a.pendingCompactionWarning != "" {
		t.Fatalf("model-driven off must not queue a warning, got %q", a.pendingCompactionWarning)
	}

	// With model-driven enabled and compact_context visible, the armed request
	// queues the externalization warning for the request about to be prepared.
	// The claim batch is the last completed request batch (the queue runs
	// before the next callLLM reserve).
	a.modelDrivenCompactionEnabled.Store(true)
	a.tools.Register(tools.NewCompactContextTool(tools.CompactContextValidator{ContinuationStateMaxTokens: 2048}))
	a.queueCompactionWarning()
	if a.pendingCompactionWarning == "" {
		t.Fatal("armed auto-compact request with a visible tool must queue the externalization warning")
	}
	if !strings.Contains(a.pendingCompactionWarning, "next safe boundary") || !strings.Contains(a.pendingCompactionWarning, "last request on the current context") || !strings.Contains(a.pendingCompactionWarning, ".chord/notes/") || strings.Contains(a.pendingCompactionWarning, "<") {
		t.Fatalf("warning text = %q, want bare actionable externalization content (the injector wraps <system-reminder>)", a.pendingCompactionWarning)
	}
	a.pendingCompactionWarning = ""

	// Pre-dispatch cancellation: the warning was attached to the in-flight
	// request (batch 2) but it never dispatched; the batch rolls back, so the
	// retried request carries the same claim batch again and re-claims.
	a.noteCompactionWarningAttached()
	a.requestBatches.reserve(a.sessionEpoch, 1) // batch 2 = the cancelled request
	a.requestBatches.rollback(a.sessionEpoch, 2)
	a.queueCompactionWarning()
	if a.pendingCompactionWarning == "" {
		t.Fatal("attached-but-not-delivered warning must be re-queued on the retried request")
	}
	a.pendingCompactionWarning = ""

	// Dispatch confirmation for the batch-1 request; the batch then advances,
	// so the same generation can never claim again.
	a.noteCompactionWarningAttached()
	a.markOverlayClaimsDelivered()
	a.requestBatches.reserve(a.sessionEpoch, 1) // batch 2
	a.queueCompactionWarning()
	if a.pendingCompactionWarning != "" {
		t.Fatalf("same-generation warning must be delivered at most once, got %q", a.pendingCompactionWarning)
	}

	// A re-armed request after a durable apply starts a new generation and a
	// fresh claim.
	a.clearUsageDrivenAutoCompactRequest()
	a.armUsageDrivenAutoCompactRequest()
	a.queueCompactionWarning()
	if a.pendingCompactionWarning == "" {
		t.Fatal("a new auto-compact generation must re-queue the warning")
	}
}

func TestBuildTurnOverlayMessagesAttachesPressureOverlays(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.pendingContextPressureReminder = "test reminder text"
	a.pendingCompactionWarning = "test warning text"

	overlays := a.buildTurnOverlayMessages()
	if len(overlays) != 2 {
		t.Fatalf("overlays = %d, want 2 (reminder + warning); got %#v", len(overlays), overlays)
	}
	foundReminder := false
	foundWarning := false
	for _, o := range overlays {
		if o.Kind != message.KindTurnOverlay {
			t.Fatalf("pressure overlay kind = %q, want turn overlay", o.Kind)
		}
		// The injector wraps every runtime notice in the same
		// <system-reminder> block so the model can tell it apart from
		// user-written content (the convention used by all harness
		// injections); the two overlays remain distinguishable by content.
		if !strings.HasPrefix(o.Content, "<system-reminder>\n") || !strings.HasSuffix(o.Content, "\n</system-reminder>") {
			t.Fatalf("pressure overlay must be wrapped in <system-reminder>, got %q", o.Content)
		}
		if strings.Contains(o.Content, "test reminder text") {
			foundReminder = true
		}
		if strings.Contains(o.Content, "test warning text") {
			foundWarning = true
		}
	}
	if !foundReminder || !foundWarning {
		t.Fatalf("overlays must carry both blocks, reminder=%v warning=%v", foundReminder, foundWarning)
	}
	// The pending fields are consumed and the claims are marked deliveryPending
	// (delivered still requires the dispatch confirmation).
	if a.pendingContextPressureReminder != "" || a.pendingCompactionWarning != "" {
		t.Fatal("pressure overlay texts must be consumed by buildTurnOverlayMessages")
	}
	a.overlayClaims.mu.Lock()
	reminderPending := a.overlayClaims.reminder.deliveryPending
	warningPending := a.overlayClaims.warning.deliveryPending
	reminderDelivered := a.overlayClaims.reminder.delivered
	warningDelivered := a.overlayClaims.warning.delivered
	a.overlayClaims.mu.Unlock()
	if !reminderPending || !warningPending {
		t.Fatal("attached overlays must mark deliveryPending")
	}
	if reminderDelivered || warningDelivered {
		t.Fatal("attach alone must not mark delivered; dispatch confirmation does")
	}
}

func TestArmUsageDrivenAutoCompactRequestGeneration(t *testing.T) {
	a := &MainAgent{}
	a.armUsageDrivenAutoCompactRequest()
	if !a.autoCompactRequested.Load() {
		t.Fatal("arming must set the request flag")
	}
	if gen := a.autoCompactRequestGeneration.Load(); gen != 1 {
		t.Fatalf("generation after first arm = %d, want 1", gen)
	}
	// Re-arming while already armed is idempotent.
	a.armUsageDrivenAutoCompactRequest()
	if gen := a.autoCompactRequestGeneration.Load(); gen != 1 {
		t.Fatalf("generation after redundant arm = %d, want 1", gen)
	}
	// Clear + re-arm assigns a fresh generation (new request instance).
	a.clearUsageDrivenAutoCompactRequest()
	a.armUsageDrivenAutoCompactRequest()
	if gen := a.autoCompactRequestGeneration.Load(); gen != 2 {
		t.Fatalf("generation after re-arm = %d, want 2", gen)
	}
}

func TestCompactionStatusEventCarriesPlanID(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	a.beginCompactionState(42, compactionTarget{turnID: 1, turnEpoch: 1, sessionEpoch: a.sessionEpoch}, compactionTriggerModelDriven, continuationPlan{kind: compactionResumeModelDriven, turnID: 1}, 2, nil)
	evt := a.compactionStatusEvent(CompactionStatusSucceeded, "")
	if evt.PlanID != "42" {
		t.Fatalf("PlanID = %q, want \"42\"", evt.PlanID)
	}
	if evt.Trigger != "model_driven" {
		t.Fatalf("Trigger = %q, want model_driven", evt.Trigger)
	}
}

func TestAutoContinuePromptVerificationGuidance(t *testing.T) {
	prompt := autoContinuePrompt()
	if !strings.Contains(prompt, "Before continuing, confirm that the preserved Current User Request and Next Step still match the actual state.") {
		t.Fatalf("auto-continue prompt must carry the post-apply verification guidance: %q", prompt)
	}
	if !strings.Contains(prompt, "Re-read any referenced state_files when needed before acting.") {
		t.Fatalf("auto-continue prompt must reference state_files re-read: %q", prompt)
	}
}

func TestAppendContextPressureVerificationGuidance(t *testing.T) {
	base := "A model-driven context checkpoint was applied; continue."
	got := appendContextPressureVerificationGuidance(base)
	if !strings.Contains(got, base) || !strings.Contains(got, "state_files") {
		t.Fatalf("guidance append = %q, want base + verification guidance", got)
	}
}
