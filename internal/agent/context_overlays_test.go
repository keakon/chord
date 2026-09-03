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
	// Delivered and ccCalled reset whenever any window component changes.
	// Same key keeps the claim; a new compaction window index, budget epoch,
	// or session epoch all start a fresh claim.
	a.overlayClaims.mu.Lock()
	a.overlayClaims.reminder = reminderOverlayClaim{windowEpoch: 1, windowIndex: 0, budgetEpoch: 0, delivered: true, ccCalled: true}
	a.overlayClaims.mu.Unlock()
	claim := a.syncOverlayWindowClaim(&a.overlayClaims.reminder, 1, 0, 0)
	if !claim.delivered || !claim.ccCalled {
		t.Fatalf("same-key sync must preserve the claim, got %+v", claim)
	}
	claim = a.syncOverlayWindowClaim(&a.overlayClaims.reminder, 1, 1, 0)
	if claim.delivered || claim.ccCalled {
		t.Fatal("new window index must reset delivered and ccCalled")
	}
	a.overlayClaims.mu.Lock()
	a.overlayClaims.reminder = reminderOverlayClaim{windowEpoch: 1, windowIndex: 1, budgetEpoch: 0, delivered: true, ccCalled: true}
	a.overlayClaims.mu.Unlock()
	claim = a.syncOverlayWindowClaim(&a.overlayClaims.reminder, 1, 1, 1)
	if claim.delivered || claim.ccCalled {
		t.Fatal("new budget epoch must reset delivered and ccCalled")
	}
	a.overlayClaims.mu.Lock()
	a.overlayClaims.reminder = reminderOverlayClaim{windowEpoch: 1, windowIndex: 1, budgetEpoch: 1, delivered: true, ccCalled: true}
	a.overlayClaims.mu.Unlock()
	claim = a.syncOverlayWindowClaim(&a.overlayClaims.reminder, 2, 1, 1)
	if claim.delivered || claim.ccCalled {
		t.Fatal("new session epoch must reset delivered and ccCalled")
	}
	// Attached but never dispatched (pre-dispatch cancellation): the claim
	// stays undelivered so the retried request may still deliver the full
	// text — delivery is confirmed only by the dispatch.
	a.overlayClaims.mu.Lock()
	a.overlayClaims.reminder = reminderOverlayClaim{windowEpoch: 2, windowIndex: 1, budgetEpoch: 1}
	a.overlayClaims.mu.Unlock()
	a.noteContextPressureReminderAttached()
	claim = a.syncOverlayWindowClaim(&a.overlayClaims.reminder, 2, 1, 1)
	if claim.deliveryPending != true || claim.delivered {
		t.Fatalf("attach must mark deliveryPending without consuming the claim, got %+v", claim)
	}
	// A dispatch confirmation marks delivered with a delivered_first stage
	// (first delivery in the window).
	a.markOverlayClaimsDelivered()
	claim = a.syncOverlayWindowClaim(&a.overlayClaims.reminder, 2, 1, 1)
	if !claim.delivered || claim.deliveryPending {
		t.Fatalf("dispatch must confirm the delivery, got %+v", claim)
	}
	// The imminent claim is tracked separately under the same window key:
	// claiming it never touches the reminder claim and vice versa.
	claim = a.syncOverlayWindowClaim(&a.overlayClaims.imminent, 2, 1, 1)
	if claim.delivered || claim.ccCalled {
		t.Fatal("reminder state must not leak into the imminent claim")
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

	// A delivered claim does not silence the reminder while usage stays above
	// the line: the next request re-queues the one-line
	// short text, not the full reminder. Only the full text is one-shot per
	// window.
	a.pendingContextPressureReminder = ""
	a.noteContextPressureReminderAttached()
	a.markOverlayClaimsDelivered()
	a.queueContextPressureReminder(a.ctxMgr.AutoCompactDecision())
	if a.pendingContextPressureReminder != contextPressureReminderShortText {
		t.Fatalf("sticky reminder after delivery must re-queue the short text, got %q", a.pendingContextPressureReminder)
	}
}

func TestQueueContextPressureReminderStickyAcrossRequests(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(8192, 8192, 0, 0.9)
	a.ctxMgr.SetLastTotalContextTokens(5000) // ≈0.61: above the 0.60 reminder line, below threshold 0.9
	a.modelDrivenCompactionEnabled.Store(true)
	a.tools.Register(tools.NewCompactContextTool(tools.CompactContextValidator{ContinuationStateMaxTokens: 2048}))
	queue := func() string {
		// Runtime order: buildTurnOverlayMessages consumes the pending text at
		// attach, then the next request's queue call refills it from scratch.
		a.pendingContextPressureReminder = ""
		a.queueContextPressureReminder(a.ctxMgr.AutoCompactDecision())
		return a.pendingContextPressureReminder
	}
	deliver := func() {
		a.pendingContextPressureReminder = ""
		a.noteContextPressureReminderAttached()
		a.markOverlayClaimsDelivered()
	}

	// First above-line request carries the full text; after a dispatch the
	// next request re-attaches only the short text.
	if got := queue(); got == "" || got == contextPressureReminderShortText {
		t.Fatalf("first above-line request must queue the full reminder, got %q", got)
	}
	deliver()
	if got := queue(); got != contextPressureReminderShortText {
		t.Fatalf("after the first dispatch the reminder must re-attach as short text, got %q", got)
	}

	// Usage drops back below the line: the reminder stops re-attaching.
	a.ctxMgr.SetLastTotalContextTokens(4000) // ≈0.49 < 0.60
	if got := queue(); got != "" {
		t.Fatalf("usage below the line must stop the reminder, got %q", got)
	}
	// A later rise in the same window resumes with the short text (the full
	// text already dispatched in this window).
	a.ctxMgr.SetLastTotalContextTokens(5000)
	if got := queue(); got != contextPressureReminderShortText {
		t.Fatalf("re-crossing the reminder line in the same window must keep the short text, got %q", got)
	}

	// The model calls compact_context in this window: whatever the attempt
	// settles to, the reminder goes quiet even while usage stays above the
	// line...
	a.markReminderCompactContextCalled()
	if got := queue(); got != "" {
		t.Fatalf("a compact_context call in the window must stop the reminder, got %q", got)
	}

	// ...until a fresh window (a session switch here) resets the claim and the
	// full text becomes available again.
	a.sessionEpoch++
	if got := queue(); got == "" || got == contextPressureReminderShortText {
		t.Fatalf("a fresh window must re-queue the full reminder, got %q", got)
	}
}

func TestCompactContextCallArmsAndStopsStickyReminder(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	a.ctxMgr.SetThreshold(0.9)
	a.ctxMgr.SetLastTotalContextTokens(5000) // above the 0.60 reminder line, below threshold
	a.modelDrivenCompactionEnabled.Store(true)
	a.tools.Register(tools.NewCompactContextTool(tools.CompactContextValidator{ContinuationStateMaxTokens: 2048}))

	a.queueContextPressureReminder(a.ctxMgr.AutoCompactDecision())
	if a.pendingContextPressureReminder == "" {
		t.Fatal("above-line usage must queue the reminder before the call")
	}
	ccID, args := testCompactContextCall()
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{testToolCall(ccID, tools.NameCompactContext)}})
	if _, err := a.tryArmModelDrivenCheckpoint(ccID, args); err != nil {
		t.Fatalf("tryArmModelDrivenCheckpoint: %v", err)
	}
	// The armed call marks the window ccCalled: later
	// requests in the same window stop re-attaching the reminder, whatever the
	// attempt settles to.
	a.pendingContextPressureReminder = ""
	a.queueContextPressureReminder(a.ctxMgr.AutoCompactDecision())
	if a.pendingContextPressureReminder != "" {
		t.Fatalf("a compact_context call in the window must stop the sticky reminder, got %q", a.pendingContextPressureReminder)
	}
}

func TestQueueContextPressureReminderSkipsWhenReminderAtThreshold(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(8192, 8192, 0, 0.5)
	a.ctxMgr.SetLastTotalContextTokens(4500) // ≈0.55: above the reminder line
	a.modelDrivenCompactionEnabled.Store(true)
	a.tools.Register(tools.NewCompactContextTool(tools.CompactContextValidator{ContinuationStateMaxTokens: 2048}))
	// An explicit reminder line at the threshold: the reminder only ever
	// fires on requests that already crossed the line, and
	// those carry the grace imminent notice or the externalization warning —
	// injecting both would stack two prompts on the same request.
	a.globalConfig.Context.Compaction.Reminder = 0.5
	a.queueContextPressureReminder(a.ctxMgr.AutoCompactDecision())
	if a.pendingContextPressureReminder != "" {
		t.Fatalf("a reminder line at the threshold must not inject separately, got %q", a.pendingContextPressureReminder)
	}
	// A reminder line below the threshold still injects once usage is above
	// its own line (control group).
	a.globalConfig.Context.Compaction.Reminder = 0.45
	a.queueContextPressureReminder(a.ctxMgr.AutoCompactDecision())
	if a.pendingContextPressureReminder == "" {
		t.Fatal("a reminder line below the threshold must keep injecting above its own line")
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
