package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
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
	claim := a.syncOverlayWindowClaim(&a.overlayClaims.reminder, a.overlayWindowKey(1, 0, 0))
	if !claim.delivered || !claim.ccCalled {
		t.Fatalf("same-key sync must preserve the claim, got %+v", claim)
	}
	claim = a.syncOverlayWindowClaim(&a.overlayClaims.reminder, a.overlayWindowKey(1, 1, 0))
	if claim.delivered || claim.ccCalled {
		t.Fatal("new window index must reset delivered and ccCalled")
	}
	a.overlayClaims.mu.Lock()
	a.overlayClaims.reminder = reminderOverlayClaim{windowEpoch: 1, windowIndex: 1, budgetEpoch: 0, delivered: true, ccCalled: true}
	a.overlayClaims.mu.Unlock()
	claim = a.syncOverlayWindowClaim(&a.overlayClaims.reminder, a.overlayWindowKey(1, 1, 1))
	if claim.delivered || claim.ccCalled {
		t.Fatal("new budget epoch must reset delivered and ccCalled")
	}
	a.overlayClaims.mu.Lock()
	a.overlayClaims.reminder = reminderOverlayClaim{windowEpoch: 1, windowIndex: 1, budgetEpoch: 1, delivered: true, ccCalled: true}
	a.overlayClaims.mu.Unlock()
	claim = a.syncOverlayWindowClaim(&a.overlayClaims.reminder, a.overlayWindowKey(2, 1, 1))
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
	claim = a.syncOverlayWindowClaim(&a.overlayClaims.reminder, a.overlayWindowKey(2, 1, 1))
	if claim.deliveryPending != true || claim.delivered {
		t.Fatalf("attach must mark deliveryPending without consuming the claim, got %+v", claim)
	}
	// A dispatch confirmation marks delivered with a delivered_first stage
	// (first delivery in the window).
	a.markOverlayClaimsDelivered()
	claim = a.syncOverlayWindowClaim(&a.overlayClaims.reminder, a.overlayWindowKey(2, 1, 1))
	if !claim.delivered || claim.deliveryPending {
		t.Fatalf("dispatch must confirm the delivery, got %+v", claim)
	}
	// The imminent claim is tracked separately under the same window key:
	// claiming it never touches the reminder claim and vice versa.
	claim = a.syncOverlayWindowClaim(&a.overlayClaims.imminent, a.overlayWindowKey(2, 1, 1))
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
	a.tools.Register(tools.NewCompactContextTool(tools.CompactContextValidator{ContinuationStateMaxTokens: CompactContinuationStateMaxTokens}))
	a.queueContextPressureReminder(a.ctxMgr.AutoCompactDecision())
	reminder := a.pendingContextPressureReminder
	if reminder == "" {
		t.Fatal("tool-visible session must queue a reminder above the line")
	}
	if !strings.Contains(reminder, "compact_context") || !strings.Contains(reminder, "automatic-compaction threshold") || !strings.Contains(reminder, "structured arguments or permitted state files") {
		t.Fatalf("reminder text = %q, want actionable text naming compact_context and both recovery-state options", reminder)
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

func TestQueueContextPressureReminderKeepsArmWhenReminderDisabled(t *testing.T) {
	// A disabled reminder line means there is no pressure line to withdraw
	// from: the usage-driven arm and grace must keep running so automatic
	// compaction still starts at the threshold.
	a := newTestMainAgent(t, t.TempDir())
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(8192, 8192, 0, 0.9)
	a.ctxMgr.SetLastTotalContextTokens(100)
	enableTestCompactContext(a)
	a.globalConfig = &config.Config{Context: config.ContextConfig{Compaction: config.CompactionConfig{Reminder: config.CompactionReminderDisabled}}}
	a.armUsageDrivenAutoCompactRequest()
	a.pendingCompactionWarning = "queued warning"

	a.queueContextPressureReminder(a.ctxMgr.AutoCompactDecision())

	if !a.autoCompactRequested.Load() {
		t.Fatal("a disabled reminder line must not disarm the usage-driven compaction request")
	}
	if a.pendingContextPressureReminder != "" {
		t.Fatalf("a disabled reminder must not queue an overlay, got %q", a.pendingContextPressureReminder)
	}
	if a.pendingCompactionWarning != "queued warning" {
		t.Fatal("a disabled reminder line must not drop a queued compaction warning")
	}
	if a.contextNoticesStale.Load() {
		t.Fatal("a disabled reminder line must not mark context notices stale")
	}
}

// A disabled reminder line withdraws only the reminder-class rows. The grace
// and externalization rows are measured against the compaction threshold, which
// is still live, so a sweep that removes them would delete a notice the runtime
// just wrote and rewrite it on the next request.
func TestQueueContextPressureReminderDisabledKeepsThresholdNotices(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(8192, 8192, 0, 0.9)
	a.ctxMgr.SetLastTotalContextTokens(7600)
	enableTestCompactContext(a)
	a.globalConfig = &config.Config{Context: config.ContextConfig{Compaction: config.CompactionConfig{Reminder: config.CompactionReminderDisabled}}}
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Kind: message.KindContextNotice, Content: "pressure", NoticeLevel: contextNoticePressure})
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Kind: message.KindContextNotice, Content: "imminent", NoticeLevel: contextNoticeImminent})
	a.contextNoticesPersisted.Store(true)

	a.queueContextPressureReminder(a.ctxMgr.AutoCompactDecision())
	if !a.contextNoticesStale.Load() {
		t.Fatal("a disabled reminder line must withdraw the reminder-class row")
	}

	a.maybeClearStaleContextNotices()
	var levels []string
	for _, msg := range a.ctxMgr.Snapshot() {
		if msg.Kind == message.KindContextNotice {
			levels = append(levels, msg.NoticeLevel)
		}
	}
	if len(levels) != 1 || levels[0] != contextNoticeImminent {
		t.Fatalf("surviving notices = %v, want only the threshold-driven imminent row", levels)
	}
}

func TestContextPressureBelowReminderLineDisabledLineIsNotBelow(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(8192, 8192, 0, 0.9)
	a.ctxMgr.SetLastTotalContextTokens(100)
	a.globalConfig = &config.Config{Context: config.ContextConfig{Compaction: config.CompactionConfig{Reminder: config.CompactionReminderDisabled}}}
	if a.contextPressureBelowReminderLine(a.ctxMgr.AutoCompactDecision(), -1) {
		t.Fatal("a disabled reminder line must not report below-line pressure")
	}
	a.globalConfig = &config.Config{}
	if !a.contextPressureBelowReminderLine(a.ctxMgr.AutoCompactDecision(), -1) {
		t.Fatal("usage far below the derived reminder line must report below-line pressure")
	}
}

func TestQueueContextPressureReminderStickyAcrossRequests(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(8192, 8192, 0, 0.9)
	a.ctxMgr.SetLastTotalContextTokens(5000) // ≈0.61: above the 0.60 reminder line, below threshold 0.9
	a.modelDrivenCompactionEnabled.Store(true)
	a.tools.Register(tools.NewCompactContextTool(tools.CompactContextValidator{ContinuationStateMaxTokens: CompactContinuationStateMaxTokens}))
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
	a.tools.Register(tools.NewCompactContextTool(tools.CompactContextValidator{ContinuationStateMaxTokens: CompactContinuationStateMaxTokens}))

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
	a.tools.Register(tools.NewCompactContextTool(tools.CompactContextValidator{ContinuationStateMaxTokens: CompactContinuationStateMaxTokens}))
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
	a.tools.Register(tools.NewCompactContextTool(tools.CompactContextValidator{ContinuationStateMaxTokens: CompactContinuationStateMaxTokens}))
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

// TestPressureNoticeStagingKeepsHighestSeverityOnly pins the single-notice
// contract: the reminder, the grace countdown, and the externalization warning
// report the same pressure fact at increasing severity, so staging any one of
// them drops whatever lower-severity notice is still pending — regardless of
// the order the callers queue them. Without it a model switch that both
// re-attaches the reminder and arms the compaction in the same cycle injects
// two notices into one request.
func TestPressureNoticeStagingKeepsHighestSeverityOnly(t *testing.T) {
	// Reminder first (queued on the request that observes the switch), then the
	// warning for the compaction the same cycle starts.
	a := newTestMainAgent(t, t.TempDir())
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(8192, 8192, 0, 0.9)
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 5000}) // above the reminder line (0.54), below the threshold
	enableTestCompactContext(a)
	a.queueContextPressureReminder(a.ctxMgr.AutoCompactDecision())
	if a.pendingContextPressureReminder == "" {
		t.Fatal("above-line usage must stage the reminder")
	}
	a.requestBatches.reserve(a.sessionEpoch, 0)
	a.armUsageDrivenAutoCompactRequest()
	a.queueCompactionWarning()
	if a.pendingContextPressureReminder != "" {
		t.Fatalf("the externalization warning must supersede the reminder, got reminder %q", a.pendingContextPressureReminder)
	}
	if a.pendingCompactionWarning == "" {
		t.Fatal("armed auto-compact request with a visible tool must stage the warning")
	}

	// Warning first, then the reminder on the next request: the reminder must
	// not stage on top of it.
	b := newTestMainAgent(t, t.TempDir())
	b.ctxMgr = ctxmgr.NewManagerWithInputBudget(8192, 8192, 0, 0.9)
	b.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 5000})
	enableTestCompactContext(b)
	b.requestBatches.reserve(b.sessionEpoch, 0)
	b.armUsageDrivenAutoCompactRequest()
	b.queueCompactionWarning()
	b.queueContextPressureReminder(b.ctxMgr.AutoCompactDecision())
	if b.pendingContextPressureReminder != "" {
		t.Fatalf("a request that already carries the warning must not also carry the reminder, got %q", b.pendingContextPressureReminder)
	}
	if b.pendingCompactionWarning == "" {
		t.Fatal("the warning must survive the lower-severity reminder staging")
	}

	// The warning also supersedes a countdown left armed by a request whose
	// dispatch never confirmed.
	c := newTestMainAgent(t, t.TempDir())
	c.ctxMgr = ctxmgr.NewManagerWithInputBudget(8192, 8192, 0, 0.9)
	c.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 5000})
	enableTestCompactContext(c)
	c.queueCompactionImminentNotice(minCompactionGracePeriodBatches)
	if c.pendingCompactionImminent == "" {
		t.Fatal("grace must stage the countdown")
	}
	c.requestBatches.reserve(c.sessionEpoch, 0)
	c.armUsageDrivenAutoCompactRequest()
	c.queueCompactionWarning()
	if c.pendingCompactionImminent != "" {
		t.Fatalf("the warning must supersede a stale countdown, got %q", c.pendingCompactionImminent)
	}
	if c.pendingCompactionWarning == "" {
		t.Fatal("the warning must be staged")
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

// TestCompactContextPermissionActionHonoursArgumentRules pins that any rule
// naming compact_context applies. The tool has no permission-matching
// argument, so a rule written with an argument pattern must not be silently
// treated as "no rule at all" (matching it against a literal "*" argument used
// to drop it), while a wildcard-only rule still never blocks the tool.
func TestCompactContextPermissionActionHonoursArgumentRules(t *testing.T) {
	cases := []struct {
		name    string
		ruleset permission.Ruleset
		want    permission.Action
	}{
		{"no rules", nil, permission.ActionAllow},
		{"wildcard deny does not apply", permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionDeny}}, permission.ActionAllow},
		{"exact deny", permission.Ruleset{{Permission: tools.NameCompactContext, Pattern: "*", Action: permission.ActionDeny}}, permission.ActionDeny},
		{"narrow glob deny", permission.Ruleset{{Permission: "compact_*", Pattern: "*", Action: permission.ActionDeny}}, permission.ActionDeny},
		{"argument-pattern deny", permission.Ruleset{{Permission: tools.NameCompactContext, Pattern: "anything", Action: permission.ActionDeny}}, permission.ActionDeny},
		{"argument-pattern ask", permission.Ruleset{{Permission: tools.NameCompactContext, Pattern: "some-arg", Action: permission.ActionAsk}}, permission.ActionAsk},
		{"last matching rule wins", permission.Ruleset{
			{Permission: tools.NameCompactContext, Pattern: "anything", Action: permission.ActionDeny},
			{Permission: tools.NameCompactContext, Pattern: "*", Action: permission.ActionAllow},
		}, permission.ActionAllow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := compactContextPermissionAction(tc.ruleset); got != tc.want {
				t.Fatalf("compactContextPermissionAction = %v, want %v", got, tc.want)
			}
			decision := evaluateToolPermissionInDir(tc.ruleset, tools.NameCompactContext, json.RawMessage(`{}`), permission.PathScope{})
			if decision.Action != tc.want {
				t.Fatalf("evaluateToolPermission action = %v, want %v", decision.Action, tc.want)
			}
		})
	}
}

func waitForContextNoticeEvent(t *testing.T, a *MainAgent) ContextNoticeEvent {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case evt := <-a.outputCh:
			if notice, ok := evt.(ContextNoticeEvent); ok {
				return notice
			}
		case <-deadline:
			t.Fatal("timed out waiting for ContextNoticeEvent")
		}
	}
}

func waitForContextNoticeCleared(t *testing.T, a *MainAgent) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case evt := <-a.outputCh:
			if _, ok := evt.(ContextNoticeClearedEvent); ok {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for ContextNoticeClearedEvent")
		}
	}
}

func TestContextNoticeFirstDeliveryPersistsDurableMessage(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	// A durable row belongs to an open pressure cycle: the cycle identity is
	// stamped on the row so a restore can adopt it instead of duplicating it.
	a.notePressureStage(pressureStageReminded, a.currentOverlayWindowKey())
	before := a.ctxMgr.MessageCount()
	text := "Context is nearing the automatic-compaction threshold."
	a.stashContextNotice(contextNoticePressure, text)
	a.noteContextPressureReminderAttached()
	a.markOverlayClaimsDelivered()

	messages := a.ctxMgr.Snapshot()
	if len(messages) != before+1 {
		t.Fatalf("message count = %d, want %d", len(messages), before+1)
	}
	last := messages[len(messages)-1]
	wrapped := "<system-reminder>\n" + text + "\n</system-reminder>"
	if last.Role != message.RoleUser || last.Kind != message.KindContextNotice || last.NoticeLevel != contextNoticePressure || last.Content != wrapped {
		t.Fatalf("persisted notice = %+v, want a user-role KindContextNotice at level %s carrying the wrapped text", last, contextNoticePressure)
	}
	if last.PressureCycleID == 0 {
		t.Fatal("a durable notice row must carry the pressure cycle that wrote it")
	}
	if message.IsUserAuthored(last) {
		t.Fatal("a context notice must not count as user-authored")
	}
	a.flushPersist()
	evt := waitForContextNoticeEvent(t, a)
	if evt.MessageIndex != before || evt.Level != contextNoticePressure || evt.Message != text {
		t.Fatalf("event = %+v, want index %d level %s and the bare text %q", evt, before, contextNoticePressure, text)
	}
}

func TestContextNoticeRepeatDeliveryDoesNotPersistAgain(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.notePressureStage(pressureStageReminded, a.currentOverlayWindowKey())
	a.stashContextNotice(contextNoticePressure, "first full reminder")
	a.noteContextPressureReminderAttached()
	a.markOverlayClaimsDelivered()
	after := a.ctxMgr.MessageCount()

	// A repeat delivery re-attaches the sticky short text in the same window;
	// it must neither persist a second message nor rebuild the card.
	a.stashContextNotice(contextNoticePressure, contextPressureReminderShortText)
	a.noteContextPressureReminderAttached()
	a.markOverlayClaimsDelivered()
	if got := a.ctxMgr.MessageCount(); got != after {
		t.Fatalf("repeat delivery appended %d messages, want no new message", got-after)
	}
	notices := 0
	for _, msg := range a.ctxMgr.Snapshot() {
		if msg.Kind == message.KindContextNotice {
			notices++
		}
	}
	if notices != 1 {
		t.Fatalf("durable notices = %d, want exactly 1 for the window", notices)
	}
}

func TestMaybeClearStaleContextNoticesRemovesHistoryAndRewritesLog(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.installSessionTarget(t.TempDir())
	manager := a.recoveryManager()
	if manager == nil {
		t.Fatal("test requires an installed recovery manager")
	}

	notice := message.Message{Role: message.RoleUser, Kind: message.KindContextNotice, Content: "stale pressure", NoticeLevel: contextNoticePressure}
	kept := message.Message{Role: message.RoleUser, Content: "hello"}
	a.ctxMgr.Append(notice)
	a.ctxMgr.Append(kept)
	a.persistAsync(identity.MainAgentID, notice)
	a.persistAsync(identity.MainAgentID, kept)
	a.flushPersist()

	a.pendingContextPressureReminder = "stale reminder"
	a.pendingCompactionWarning = "stale warning"
	a.pendingCompactionImminent = "stale imminent"
	a.contextNoticesStale.Store(true)
	a.maybeClearStaleContextNotices()

	for _, msg := range a.ctxMgr.Snapshot() {
		if msg.Kind == message.KindContextNotice {
			t.Fatalf("context notice survived cleanup in memory: %+v", msg)
		}
	}
	persisted, err := manager.LoadMessages(identity.MainAgentID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(persisted) != 1 || persisted[0].Content != "hello" {
		t.Fatalf("persisted messages = %+v, want only the kept user message", persisted)
	}
	if a.pendingContextPressureReminder != "" || a.pendingCompactionWarning != "" || a.pendingCompactionImminent != "" {
		t.Fatal("pending context notices must be cleared with the history")
	}
	if a.contextNoticesStale.Load() {
		t.Fatal("the stale marker must be consumed exactly once")
	}
	waitForContextNoticeCleared(t, a)
}

func TestMaybeClearStaleContextNoticesWaitsForIdle(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Kind: message.KindContextNotice, Content: "stale pressure", NoticeLevel: contextNoticeWarning})
	a.contextNoticesStale.Store(true)

	a.newTurn()
	a.maybeClearStaleContextNotices()
	if !a.contextNoticesStale.Load() {
		t.Fatal("cleanup must keep the stale marker while a turn is active")
	}
	if !hasContextNotice(a.ctxMgr.Snapshot()) {
		t.Fatal("a busy agent must not lose the context notice")
	}

	a.turn = nil
	a.maybeClearStaleContextNotices()
	if hasContextNotice(a.ctxMgr.Snapshot()) {
		t.Fatal("idle cleanup must remove the context notice")
	}
}

func TestMaybeClearStaleContextNoticesWaitsForCompaction(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Kind: message.KindContextNotice, Content: "stale pressure", NoticeLevel: contextNoticeWarning})
	a.contextNoticesStale.Store(true)

	// A compaction draft's headSplit is measured against the current
	// transcript; removing a notice now would shift the ordinals and abort the
	// apply at the provenance check.
	a.compactionSlotActive.Store(true)
	a.maybeClearStaleContextNotices()
	if !a.contextNoticesStale.Load() {
		t.Fatal("cleanup must keep the stale marker while a compaction is running")
	}
	if !hasContextNotice(a.ctxMgr.Snapshot()) {
		t.Fatal("a running compaction must not lose the context notice")
	}

	a.compactionSlotActive.Store(false)
	a.maybeClearStaleContextNotices()
	if hasContextNotice(a.ctxMgr.Snapshot()) {
		t.Fatal("cleanup after the compaction must remove the context notice")
	}
}

func TestMaybeClearStaleContextNoticesNoNoticeKeepsHistory(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "hello"})
	a.contextNoticesStale.Store(true)
	a.maybeClearStaleContextNotices()
	if hasContextNotice(a.ctxMgr.Snapshot()) {
		t.Fatal("cleanup must not invent a notice")
	}
	messages := a.ctxMgr.Snapshot()
	if len(messages) != 1 || messages[0].Content != "hello" {
		t.Fatalf("cleanup with no notice must keep history, got %+v", messages)
	}
	if a.contextNoticesStale.Load() {
		t.Fatal("the stale marker must be consumed even when nothing matched")
	}
	select {
	case evt := <-a.outputCh:
		if _, ok := evt.(ContextNoticeClearedEvent); ok {
			t.Fatal("no match must not emit a cleared event")
		}
	default:
	}
}

func hasContextNotice(messages []message.Message) bool {
	for _, msg := range messages {
		if msg.Kind == message.KindContextNotice {
			return true
		}
	}
	return false
}

func enableTestCompactContext(a *MainAgent) {
	a.modelDrivenCompactionEnabled.Store(true)
	a.tools.Register(tools.NewCompactContextTool(tools.CompactContextValidator{ContinuationStateMaxTokens: CompactContinuationStateMaxTokens}))
}

func TestQueueContextPressureReminderMarksNoticesStaleWhenUsageDrops(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(8192, 8192, 0, 0.9)
	a.ctxMgr.SetLastTotalContextTokens(5000)
	enableTestCompactContext(a)
	a.armUsageDrivenAutoCompactRequest()
	a.queueContextPressureReminder(a.ctxMgr.AutoCompactDecision())
	a.stashContextNotice(contextNoticePressure, a.pendingContextPressureReminder)
	a.noteContextPressureReminderAttached()
	a.markOverlayClaimsDelivered()

	a.ctxMgr.SetLastTotalContextTokens(4000)
	a.queueContextPressureReminder(a.ctxMgr.AutoCompactDecision())
	if a.pendingContextPressureReminder != "" {
		t.Fatalf("usage below the line must stop the reminder, got %q", a.pendingContextPressureReminder)
	}
	if !a.contextNoticesStale.Load() {
		t.Fatal("a delivered notice must be marked stale once usage drops below the reminder line")
	}
	if a.autoCompactRequested.Load() {
		t.Fatal("a below-line queue must clear a stale usage-driven auto-compact request")
	}
}

func TestQueueContextPressureReminderReCrossBeforeCleanupKeepsShortText(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(8192, 8192, 0, 0.9)
	a.ctxMgr.SetLastTotalContextTokens(5000)
	enableTestCompactContext(a)
	a.queueContextPressureReminder(a.ctxMgr.AutoCompactDecision())
	a.stashContextNotice(contextNoticePressure, a.pendingContextPressureReminder)
	a.noteContextPressureReminderAttached()
	a.markOverlayClaimsDelivered()

	a.ctxMgr.SetLastTotalContextTokens(4000)
	a.queueContextPressureReminder(a.ctxMgr.AutoCompactDecision())
	if !a.contextNoticesStale.Load() {
		t.Fatal("usage drop must mark the delivered notice stale")
	}

	a.pendingContextPressureReminder = ""
	a.ctxMgr.SetLastTotalContextTokens(5000)
	a.queueContextPressureReminder(a.ctxMgr.AutoCompactDecision())
	if a.pendingContextPressureReminder != contextPressureReminderShortText {
		t.Fatalf("re-crossing before idle cleanup must keep the short text, got %q", a.pendingContextPressureReminder)
	}
	if a.contextNoticesStale.Load() {
		t.Fatal("a same-window re-cross must cancel idle cleanup of an accurate notice")
	}
}

func TestMaybeClearStaleContextNoticesResetsReminderDeliveryForRecross(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.installSessionTarget(t.TempDir())
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(8192, 8192, 0, 0.9)
	a.ctxMgr.SetLastTotalContextTokens(5000)
	enableTestCompactContext(a)

	a.queueContextPressureReminder(a.ctxMgr.AutoCompactDecision())
	a.stashContextNotice(contextNoticePressure, a.pendingContextPressureReminder)
	a.noteContextPressureReminderAttached()
	a.markOverlayClaimsDelivered()
	if !hasContextNotice(a.ctxMgr.Snapshot()) {
		t.Fatal("first delivery must persist a context notice")
	}

	a.ctxMgr.SetLastTotalContextTokens(4000)
	a.queueContextPressureReminder(a.ctxMgr.AutoCompactDecision())
	a.maybeClearStaleContextNotices()
	if hasContextNotice(a.ctxMgr.Snapshot()) {
		t.Fatal("idle cleanup must drop the leftover pressure notice")
	}
	waitForContextNoticeCleared(t, a)

	a.overlayClaims.mu.Lock()
	delivered := a.overlayClaims.reminder.delivered
	ccCalled := a.overlayClaims.reminder.ccCalled
	a.overlayClaims.mu.Unlock()
	if delivered {
		t.Fatal("idle cleanup must reset reminder delivery so a later re-cross can persist a new card")
	}
	if ccCalled {
		t.Fatal("cleanup must not invent a compact_context call")
	}

	a.ctxMgr.SetLastTotalContextTokens(5000)
	a.queueContextPressureReminder(a.ctxMgr.AutoCompactDecision())
	if got := a.pendingContextPressureReminder; got == "" || got == contextPressureReminderShortText {
		t.Fatalf("re-crossing after cleanup must queue the full reminder, got %q", got)
	}
}

func TestMaybeClearStaleContextNoticesKeepsCcCalledQuiet(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(8192, 8192, 0, 0.9)
	a.ctxMgr.SetLastTotalContextTokens(5000)
	enableTestCompactContext(a)
	a.queueContextPressureReminder(a.ctxMgr.AutoCompactDecision())
	a.stashContextNotice(contextNoticePressure, "pressure")
	a.noteContextPressureReminderAttached()
	a.markOverlayClaimsDelivered()
	a.markReminderCompactContextCalled()

	a.ctxMgr.SetLastTotalContextTokens(4000)
	a.queueContextPressureReminder(a.ctxMgr.AutoCompactDecision())
	a.maybeClearStaleContextNotices()

	a.ctxMgr.SetLastTotalContextTokens(5000)
	a.pendingContextPressureReminder = ""
	a.queueContextPressureReminder(a.ctxMgr.AutoCompactDecision())
	if a.pendingContextPressureReminder != "" {
		t.Fatalf("a compact_context call in the window must stay quiet after cleanup, got %q", a.pendingContextPressureReminder)
	}
}

func TestOmitStaleContextNoticesFromRequestDropsCopyOnly(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	notice := message.Message{Role: message.RoleUser, Kind: message.KindContextNotice, Content: "stale pressure", NoticeLevel: contextNoticePressure}
	kept := message.Message{Role: message.RoleUser, Content: "hello"}
	a.ctxMgr.Append(notice)
	a.ctxMgr.Append(kept)
	snapshot := a.ctxMgr.Snapshot()

	filtered := a.omitStaleContextNoticesFromRequest(snapshot)
	if !hasContextNotice(filtered) {
		t.Fatal("fresh notices must stay on the request until they are marked stale")
	}

	a.contextNoticesStale.Store(true)
	filtered = a.omitStaleContextNoticesFromRequest(snapshot)
	if hasContextNotice(filtered) {
		t.Fatal("a stale marker must omit context notices from the request copy")
	}
	if !hasContextNotice(a.ctxMgr.Snapshot()) {
		t.Fatal("omitting stale notices from the request must not rewrite durable history")
	}
}

func TestMaybeClearStaleContextNoticesNoNoticeResetsDelivered(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.overlayClaims.mu.Lock()
	a.overlayClaims.reminder.delivered = true
	a.overlayClaims.mu.Unlock()
	a.contextNoticesStale.Store(true)
	a.maybeClearStaleContextNotices()
	a.overlayClaims.mu.Lock()
	delivered := a.overlayClaims.reminder.delivered
	a.overlayClaims.mu.Unlock()
	if delivered {
		t.Fatal("cleanup with no matching notice must still consume the leftover delivered flag")
	}
}

func TestInstallContextNoticePresenceRebuildsFromTranscript(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.contextNoticesStale.Store(true)
	notice := message.Message{Role: message.RoleUser, Kind: message.KindContextNotice, Content: "pressure", NoticeLevel: contextNoticePressure}
	a.installContextNoticePresence([]message.Message{notice})
	if !a.contextNoticesPersisted.Load() {
		t.Fatal("a loaded transcript carrying a notice row must rebuild presence")
	}
	if a.contextNoticesStale.Load() {
		t.Fatal("a session load must drop the replaced session's armed cleanup")
	}
	a.installContextNoticePresence([]message.Message{{Role: message.RoleUser, Content: "hello"}})
	if a.contextNoticesPersisted.Load() {
		t.Fatal("a loaded transcript without a notice row must clear presence")
	}
	if a.contextNoticesStale.Load() {
		t.Fatal("a session load must not leave a stale marker behind")
	}
}

func TestContextNoticeCleanupDisarmsOnFirstDeliveryOnly(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.contextNoticesPersisted.Store(true)
	a.armContextNoticeCleanup()
	if !a.contextNoticesStale.Load() {
		t.Fatal("presence with a withdrawn line must arm the cleanup")
	}
	// A fresh first delivery supersedes the pending withdrawal: the row it
	// persists measures against the live line, so the sweep must be cancelled.
	a.noteContextPressureReminderAttached()
	a.markOverlayClaimsDelivered()
	if a.contextNoticesStale.Load() {
		t.Fatal("the first delivery must cancel the pending withdrawal")
	}
	// A sticky repeat re-attaches the same durable row: it must not cancel an
	// armed withdrawal.
	a.armContextNoticeCleanup()
	a.noteContextPressureReminderAttached()
	a.markOverlayClaimsDelivered()
	if !a.contextNoticesStale.Load() {
		t.Fatal("a repeat delivery must not cancel an armed withdrawal")
	}
	// The one-shot externalization warning persists its own row, so its
	// delivery supersedes the withdrawal the same way.
	a.disarmContextNoticeCleanup()
	a.contextNoticesStale.Store(true)
	a.noteCompactionWarningAttached()
	a.markOverlayClaimsDelivered()
	if a.contextNoticesStale.Load() {
		t.Fatal("the externalization warning delivery must cancel the pending withdrawal")
	}
}

func TestQueueContextPressureReminderMarksNonReminderNoticesStale(t *testing.T) {
	// The one durable row a cycle may write can be the grace imminent notice or
	// the externalization warning rather than the sticky reminder (an explicit
	// reminder line at or above the threshold never injects on its own, and a
	// compact_context call silences the reminder for the window). A below-line
	// drop must still withdraw that row.
	cases := []struct {
		name string
		mark func(a *MainAgent)
	}{
		{name: "imminent", mark: func(a *MainAgent) {
			a.syncOverlayWindowClaim(&a.overlayClaims.imminent, a.currentOverlayWindowKey())
			a.queueCompactionImminentNotice(minCompactionGracePeriodBatches)
			a.stashContextNotice(contextNoticeImminent, a.pendingCompactionImminent)
			a.pendingCompactionImminent = ""
			a.noteCompactionImminentAttached()
		}},
		{name: "warning", mark: func(a *MainAgent) {
			a.notePressureStage(pressureStageArmed, a.currentOverlayWindowKey())
			a.stashContextNotice(contextNoticeWarning, compactionWarningText)
			a.noteCompactionWarningAttached()
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestMainAgent(t, t.TempDir())
			a.installSessionTarget(t.TempDir())
			a.ctxMgr = ctxmgr.NewManagerWithInputBudget(8192, 8192, 0, 0.9)
			a.ctxMgr.SetLastTotalContextTokens(5000)
			enableTestCompactContext(a)
			tc.mark(a)
			a.markOverlayClaimsDelivered()
			if !hasContextNotice(a.ctxMgr.Snapshot()) {
				t.Fatal("the delivered overlay must persist a durable notice row")
			}

			a.ctxMgr.SetLastTotalContextTokens(4000)
			a.queueContextPressureReminder(a.ctxMgr.AutoCompactDecision())
			if !a.contextNoticesStale.Load() {
				t.Fatal("a durable notice of any class must be marked stale once usage drops below the reminder line")
			}
			a.maybeClearStaleContextNotices()
			if hasContextNotice(a.ctxMgr.Snapshot()) {
				t.Fatal("idle cleanup must drop the durable notice")
			}
			a.queueContextPressureReminder(a.ctxMgr.AutoCompactDecision())
			if a.contextNoticesStale.Load() {
				t.Fatal("a withdrawn notice must not re-arm idle cleanup on a later below-line request")
			}
		})
	}
}

func TestMaybeClearStaleContextNoticesResetsImminentDeliveryForRecross(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.installSessionTarget(t.TempDir())
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(8192, 8192, 0, 0.9)
	a.ctxMgr.SetLastTotalContextTokens(5000)
	enableTestCompactContext(a)

	// Deliver only the grace imminent notice: its durable row exists while the
	// sticky reminder was never attached.
	a.syncOverlayWindowClaim(&a.overlayClaims.imminent, a.currentOverlayWindowKey())
	a.queueCompactionImminentNotice(minCompactionGracePeriodBatches)
	a.stashContextNotice(contextNoticeImminent, a.pendingCompactionImminent)
	a.pendingCompactionImminent = ""
	a.noteCompactionImminentAttached()
	a.markOverlayClaimsDelivered()
	if !hasContextNotice(a.ctxMgr.Snapshot()) {
		t.Fatal("imminent delivery must persist a context notice")
	}

	a.ctxMgr.SetLastTotalContextTokens(4000)
	a.queueContextPressureReminder(a.ctxMgr.AutoCompactDecision())
	a.maybeClearStaleContextNotices()
	if hasContextNotice(a.ctxMgr.Snapshot()) {
		t.Fatal("idle cleanup must drop the leftover imminent notice")
	}
	a.overlayClaims.mu.Lock()
	delivered := a.overlayClaims.imminent.delivered
	a.overlayClaims.mu.Unlock()
	if delivered {
		t.Fatal("idle cleanup must reset imminent delivery so a later re-cross can persist a new card")
	}
}

// TestStageContextNoticeNeverDowngrades pins the staging rank: a lower-pressure
// notice staged after a higher-pressure one is dropped, so an aborted request's
// residual warning cannot be replaced by a stale countdown or reminder.
func TestStageContextNoticeNeverDowngrades(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.stageContextNotice(contextNoticeWarning, "warning text")
	a.stageContextNotice(contextNoticeImminent, "imminent text")
	if a.pendingCompactionWarning != "warning text" {
		t.Fatalf("staging imminent must not clear the pending warning, got warning %q imminent %q", a.pendingCompactionWarning, a.pendingCompactionImminent)
	}
	if a.pendingCompactionImminent != "" {
		t.Fatalf("a lower-pressure imminent must be dropped while warning is pending, got %q", a.pendingCompactionImminent)
	}
	a.stageContextNotice(contextNoticePressure, "reminder text")
	if a.pendingCompactionWarning != "warning text" || a.pendingContextPressureReminder != "" {
		t.Fatalf("staging pressure must not downgrade the pending warning, got warning %q reminder %q", a.pendingCompactionWarning, a.pendingContextPressureReminder)
	}
}
