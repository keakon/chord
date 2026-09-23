package agent

import (
	"slices"
	"testing"
	"time"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestPressureCycleIdentityAndStageTransitions(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(8192, 8192, 0, 0.9)
	key := a.currentOverlayWindowKey()

	a.notePressureStage(pressureStageReminded, key)
	first := a.overlayClaims.pressure.id
	if first == 0 {
		t.Fatal("a pressure stage must open a cycle with a runtime-allocated identity")
	}
	if a.overlayClaims.pressure.stage != pressureStageReminded {
		t.Fatalf("stage = %v, want %v", a.overlayClaims.pressure.stage, pressureStageReminded)
	}

	// Climbing the ladder within one window keeps the same cycle identity.
	a.notePressureStage(pressureStageArmed, key)
	a.notePressureStage(pressureStageGrace, key)
	a.notePressureStage(pressureStageCompacting, key)
	if a.overlayClaims.pressure.id != first {
		t.Fatalf("cycle identity changed within the window: %d -> %d", first, a.overlayClaims.pressure.id)
	}
	if a.overlayClaims.pressure.stage != pressureStageCompacting {
		t.Fatalf("stage = %v, want %v", a.overlayClaims.pressure.stage, pressureStageCompacting)
	}

	// A closed cycle is terminal: compacting cannot reopen it on its own.
	a.endPressureCycle(pressureEndWithdrawn)
	if a.overlayClaims.pressure.id != 0 {
		t.Fatal("a closed cycle must not stay open")
	}
	a.notePressureStage(pressureStageCompacting, key)
	if a.overlayClaims.pressure.id != 0 {
		t.Fatal("compacting must never open a pressure cycle by itself")
	}
	// Ending an already closed cycle is idempotent.
	a.endPressureCycle(pressureEndApplied)
	if a.overlayClaims.pressure.id != 0 {
		t.Fatal("closing a closed cycle must be a no-op")
	}

	// A changed window (session switch, model/budget change) closes the old
	// cycle and opens a fresh identity.
	a.notePressureStage(pressureStageReminded, key)
	second := a.overlayClaims.pressure.id
	if second <= first {
		t.Fatalf("cycle identity must advance monotonically, got %d after %d", second, first)
	}
	other := key
	other.windowIndex++
	a.notePressureStage(pressureStageArmed, other)
	if a.overlayClaims.pressure.id == second {
		t.Fatal("a changed window must open a fresh cycle, not inherit the previous identity")
	}
	if a.overlayClaims.pressure.window != other {
		t.Fatalf("cycle window = %+v, want %+v", a.overlayClaims.pressure.window, other)
	}

	// A window change that arrives as compacting only closes the stale cycle:
	// compacting never opens one on its own.
	third := a.overlayClaims.pressure.id
	other.windowIndex++
	a.notePressureStage(pressureStageCompacting, other)
	if a.overlayClaims.pressure.id != 0 {
		t.Fatalf("window-changed compacting opened cycle %d, want no open cycle", a.overlayClaims.pressure.id)
	}
	if third == 0 {
		t.Fatal("expected an open cycle before the window-changed compacting")
	}
}

func TestPressureCycleRecordsOneDurableRowPerCycle(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(8192, 8192, 0, 0.9)
	a.ctxMgr.SetLastTotalContextTokens(5000) // above the reminder line, below the threshold
	enableTestCompactContext(a)

	a.queueContextPressureReminder(a.ctxMgr.AutoCompactDecision())
	reminder := a.pendingContextPressureReminder
	if reminder == "" {
		t.Fatal("an above-line request must queue the reminder")
	}
	a.pendingContextPressureReminder = ""
	a.noteContextPressureReminderAttached()
	a.stashContextNotice(contextNoticePressure, reminder)
	a.markOverlayClaimsDelivered()
	waitForContextNoticeEvent(t, a)
	afterReminder := a.ctxMgr.MessageCount()

	// Escalating inside the same cycle is suppressed: the transcript keeps
	// the one row that already records the cycle.
	a.queueCompactionImminentNotice(minCompactionGracePeriodBatches)
	if a.pendingCompactionImminent == "" {
		t.Fatal("the grace countdown must still be staged for the request")
	}
	a.pendingCompactionImminent = ""
	a.noteCompactionImminentAttached()
	a.stashContextNotice(contextNoticeImminent, compactionImminentText(minCompactionGracePeriodBatches))
	a.markOverlayClaimsDelivered()
	if got := a.ctxMgr.MessageCount(); got != afterReminder {
		t.Fatalf("an escalation appended %d durable rows, want none", got-afterReminder)
	}

	notices := 0
	for _, msg := range a.ctxMgr.Snapshot() {
		if msg.Kind == message.KindContextNotice {
			notices++
		}
	}
	if notices != 1 {
		t.Fatalf("durable notices = %d, want exactly one row per pressure cycle", notices)
	}

	// A new window starts a new cycle and may record again.
	a.clearCompactionGrace()
	a.compactionWindowGeneration++
	a.queueContextPressureReminder(a.ctxMgr.AutoCompactDecision())
	a.pendingContextPressureReminder = ""
	a.noteContextPressureReminderAttached()
	a.stashContextNotice(contextNoticePressure, buildContextPressureReminderText())
	a.markOverlayClaimsDelivered()
	if got := a.ctxMgr.MessageCount(); got != afterReminder+1 {
		t.Fatalf("a fresh cycle appended %d rows, want 1", got-afterReminder)
	}
}

func TestPressureCycleAdoptsRestoredRow(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(8192, 8192, 0, 0.9)
	a.ctxMgr.SetLastTotalContextTokens(5000)
	enableTestCompactContext(a)

	restored := message.Message{
		Role:            message.RoleUser,
		Kind:            message.KindContextNotice,
		Content:         "<system-reminder>\nold pressure\n</system-reminder>",
		NoticeLevel:     contextNoticePressure,
		PressureCycleID: 7,
	}
	a.installContextNoticePresence([]message.Message{restored})
	if !a.contextNoticesPersisted.Load() {
		t.Fatal("a restored notice row must reestablish the durable-notice presence signal")
	}

	// The restored row is the cycle's record: the revived cycle continues past
	// its identity and must not append a duplicate.
	a.queueContextPressureReminder(a.ctxMgr.AutoCompactDecision())
	if a.pendingContextPressureReminder != contextPressureReminderShortText {
		t.Fatalf("restored window reminder = %q, want the short re-attachment", a.pendingContextPressureReminder)
	}
	a.pendingContextPressureReminder = ""
	a.noteContextPressureReminderAttached()
	a.stashContextNotice(contextNoticePressure, contextPressureReminderShortText)
	a.markOverlayClaimsDelivered()
	if got := a.ctxMgr.MessageCount(); got != 0 {
		t.Fatalf("restore adoption appended %d duplicate rows, want none", got)
	}
	if id := a.overlayClaims.pressure.id; id != restored.PressureCycleID {
		t.Fatalf("cycle identity = %d, want the restored row's cycle %d", id, restored.PressureCycleID)
	}

	// Withdrawing the row releases the reservation: a later re-crossing of the
	// same cycle records again.
	a.ctxMgr.Append(restored)
	a.contextNoticesPersisted.Store(true)
	a.contextNoticesStale.Store(true)
	a.maybeClearStaleContextNotices()
	waitForContextNoticeCleared(t, a)
	if hasContextNotice(a.ctxMgr.Snapshot()) {
		t.Fatal("the withdrawal must drop the restored row")
	}
	// Usage climbs back above the line: the same cycle records again now that
	// its withdrawn row gave the reservation back.
	a.ctxMgr.SetLastTotalContextTokens(5000)
	a.queueContextPressureReminder(a.ctxMgr.AutoCompactDecision())
	if a.pendingContextPressureReminder == "" {
		t.Fatalf("re-cross must stage a reminder cycle=%+v claim=%+v", a.overlayClaims.pressure, a.overlayClaims.reminder)
	}
	a.pendingContextPressureReminder = ""
	a.noteContextPressureReminderAttached()
	a.stashContextNotice(contextNoticePressure, buildContextPressureReminderText())
	a.markOverlayClaimsDelivered()
	if got := a.ctxMgr.MessageCount(); got != 1 {
		t.Fatalf("re-crossing after a withdrawal appended %d rows, want 1", got)
	}
	last := a.ctxMgr.Snapshot()[0]
	if last.PressureCycleID != a.overlayClaims.pressure.id {
		t.Fatalf("re-recorded row cycle = %d, want the open cycle %d", last.PressureCycleID, a.overlayClaims.pressure.id)
	}
}

// TestPressureCycleStaleRestoredRowReleasesAdoption pins the ordering the
// restore path can hit: the loaded row is judged stale before any stage
// transition opens the cycle, so the withdrawal has to give the adoption back —
// otherwise the next crossing would revive an identity the transcript no longer
// carries and skip the card that documents it.
func TestPressureCycleStaleRestoredRowReleasesAdoption(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(8192, 8192, 0, 0.9)
	enableTestCompactContext(a)

	restored := message.Message{
		Role:            message.RoleUser,
		Kind:            message.KindContextNotice,
		Content:         "<system-reminder>\nold pressure\n</system-reminder>",
		NoticeLevel:     contextNoticePressure,
		PressureCycleID: 7,
	}
	a.ctxMgr.Append(restored)
	a.installContextNoticePresence([]message.Message{restored})
	if !a.overlayClaims.pressure.adopted || a.overlayClaims.pressure.adoptedID != restored.PressureCycleID {
		t.Fatalf("a restored row must be adopted: %+v", a.overlayClaims.pressure)
	}

	// Usage sits below the reminder line, so the first decision after the
	// restore withdraws the row before anything opens a cycle.
	a.ctxMgr.SetLastTotalContextTokens(1000)
	a.queueContextPressureReminder(a.ctxMgr.AutoCompactDecision())
	a.maybeClearStaleContextNotices()
	waitForContextNoticeCleared(t, a)
	if hasContextNotice(a.ctxMgr.Snapshot()) {
		t.Fatal("a below-line decision must withdraw the restored row")
	}
	if a.overlayClaims.pressure.adopted {
		t.Fatal("a withdrawn row must give its adoption back: the transcript no longer carries a record")
	}

	// Usage climbs back above the line: the new cycle records its own card
	// instead of inheriting an identity with no row behind it.
	a.ctxMgr.SetLastTotalContextTokens(5000)
	a.queueContextPressureReminder(a.ctxMgr.AutoCompactDecision())
	if a.pendingContextPressureReminder == "" {
		t.Fatalf("re-cross must stage a reminder: cycle=%+v", a.overlayClaims.pressure)
	}
	a.pendingContextPressureReminder = ""
	a.noteContextPressureReminderAttached()
	a.stashContextNotice(contextNoticePressure, buildContextPressureReminderText())
	a.markOverlayClaimsDelivered()
	if got := a.ctxMgr.MessageCount(); got != 1 {
		t.Fatalf("re-crossing after the withdrawal appended %d rows, want 1", got)
	}
	last := a.ctxMgr.Snapshot()[0]
	if last.PressureCycleID == restored.PressureCycleID {
		t.Fatalf("the new card reuses the withdrawn row's cycle %d: the adoption was not released", last.PressureCycleID)
	}
	if last.PressureCycleID != a.overlayClaims.pressure.id {
		t.Fatalf("re-recorded row cycle = %d, want the open cycle %d", last.PressureCycleID, a.overlayClaims.pressure.id)
	}
}

func TestDropContextNoticeMessages(t *testing.T) {
	kept := message.Message{Role: message.RoleUser, Content: "hello"}
	notice := message.Message{Role: message.RoleUser, Kind: message.KindContextNotice, Content: "notice"}
	out := dropContextNoticeMessages([]message.Message{notice, kept, notice})
	if len(out) != 1 || out[0].Content != "hello" {
		t.Fatalf("kept messages = %+v, want only the non-notice message", out)
	}
	if got := dropContextNoticeMessages([]message.Message{kept}); len(got) != 1 {
		t.Fatalf("a list without notices must pass through unchanged, got %+v", got)
	}
}

// lastPressureCycleEvent returns the newest pressure-cycle journal event.
func lastPressureCycleEvent(t *testing.T, events []analytics.UsageEvent) analytics.UsageEvent {
	t.Helper()
	for _, event := range slices.Backward(events) {
		if event.Purpose == analytics.UsagePurposeContextPressureCycle {
			return event
		}
	}
	t.Fatalf("no pressure-cycle journal event in %+v", events)
	return analytics.UsageEvent{}
}

// TestPressureCyclePreparationAuditDatesEachAction pins the preparation audit:
// while a cycle is live each observable preparation action is dated once, and
// the terminal journal record carries those instants — absent, never empty,
// when an action did not happen.
func TestPressureCyclePreparationAuditDatesEachAction(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(8192, 8192, 0, 0.9)
	var events []analytics.UsageEvent
	a.SetUsageEventSink(func(event analytics.UsageEvent) { events = append(events, event) })

	key := a.currentOverlayWindowKey()
	a.notePressureStage(pressureStageReminded, key)

	// The real compact_context acceptance path stamps the boolean and its
	// instant together.
	a.markReminderCompactContextCalled()
	cycle := a.overlayClaims.pressure
	if !cycle.responded || cycle.audit.respondedAt.IsZero() {
		t.Fatalf("compact_context acceptance did not stamp the audit: %+v", cycle)
	}
	respondedAt := cycle.audit.respondedAt

	// A hand-back to the user is dated at the ask: the recorded duration is the
	// span the question tool spent waiting for the answer.
	a.notePressurePreparationFromToolResult(&ToolResultPayload{Name: tools.NameQuestion, Duration: 2 * time.Second})
	awaitingAt := a.overlayClaims.pressure.audit.awaitingUserAt
	if awaitingAt.IsZero() {
		t.Fatal("a completed question call must stamp the awaiting-user audit")
	}
	if wait := respondedAt.Sub(awaitingAt); wait < 1500*time.Millisecond || wait > 5*time.Second {
		t.Fatalf("awaiting-user instant should be about 2s before the answer, delta=%v", wait)
	}

	// A committed note write is externalized state; a source edit is not.
	a.notePressurePreparationFromToolResult(&ToolResultPayload{
		Name:      tools.NameEdit,
		FileState: &message.ToolFileState{Changes: []message.ToolFileChange{{Path: "internal/agent/main_handlers_tools.go"}}},
	})
	if !a.overlayClaims.pressure.audit.stateFileWriteAt.IsZero() {
		t.Fatal("a source edit must not count as externalized state")
	}
	a.notePressurePreparationFromToolResult(&ToolResultPayload{
		Name:      tools.NameWrite,
		FileState: &message.ToolFileState{Writes: []message.TrackedFileState{{Path: ".chord/notes/task.md"}}},
	})
	stateFileWriteAt := a.overlayClaims.pressure.audit.stateFileWriteAt
	if stateFileWriteAt.IsZero() {
		t.Fatal("a committed note write must stamp the state-file audit")
	}

	a.endPressureCycle(pressureEndApplied)
	cycleEvent := lastPressureCycleEvent(t, events)
	for _, want := range []struct {
		key  string
		have time.Time
	}{
		{"responded_at", respondedAt},
		{"awaiting_user_at", awaitingAt},
		{"state_file_write_at", stateFileWriteAt},
	} {
		if got := cycleEvent.Diagnostic[want.key]; got != want.have.Format(time.RFC3339Nano) {
			t.Fatalf("%s = %q, want %q", want.key, got, want.have.Format(time.RFC3339Nano))
		}
	}
	if cycleEvent.Diagnostic["responded"] != "true" {
		t.Fatalf("responded = %q, want true", cycleEvent.Diagnostic["responded"])
	}

	// A cycle nobody prepared for omits the instants entirely: absence is the
	// fact a review needs, so an unset action must not appear as an empty value.
	a.notePressureStage(pressureStageReminded, key)
	a.endPressureCycle(pressureEndWithdrawn)
	cycleEvent = lastPressureCycleEvent(t, events)
	for _, key := range []string{"responded_at", "state_file_write_at", "awaiting_user_at"} {
		if _, ok := cycleEvent.Diagnostic[key]; ok {
			t.Fatalf("%s present on a cycle with no preparation: %+v", key, cycleEvent.Diagnostic)
		}
	}
}

// TestPressurePreparationAuditDropsMarksOutsideACycle pins the audit's scope:
// preparation is audited only while a nudge window is live, the first instant
// wins, and only agent-owned notes or plans count as externalized state.
func TestPressurePreparationAuditDropsMarksOutsideACycle(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(8192, 8192, 0, 0.9)
	first := time.Unix(1_700_000_000, 0).UTC()

	a.notePressurePreparation(pressurePreparationStateFileWrite, first)
	if a.overlayClaims.pressure.id != 0 {
		t.Fatal("an audit mark must not open a pressure cycle")
	}

	key := a.currentOverlayWindowKey()
	a.notePressureStage(pressureStageReminded, key)
	if !a.overlayClaims.pressure.audit.stateFileWriteAt.IsZero() {
		t.Fatal("a mark taken before the cycle must not leak into it")
	}
	a.notePressurePreparation(pressurePreparationStateFileWrite, first)
	a.notePressurePreparation(pressurePreparationStateFileWrite, first.Add(time.Hour))
	if got := a.overlayClaims.pressure.audit.stateFileWriteAt; !got.Equal(first) {
		t.Fatalf("state-file instant = %v, want the first %v", got, first)
	}

	a.endPressureCycle(pressureEndWithdrawn)
	a.notePressurePreparation(pressurePreparationAwaitingUser, first.Add(2*time.Hour))
	if a.overlayClaims.pressure.id != 0 || !a.overlayClaims.pressure.audit.awaitingUserAt.IsZero() {
		t.Fatalf("marks after the cycle closed must be dropped: %+v", a.overlayClaims.pressure)
	}
	a.notePressureStage(pressureStageReminded, key)
	if !a.overlayClaims.pressure.audit.awaitingUserAt.IsZero() {
		t.Fatal("a closed cycle's audit instants must not leak into the next cycle")
	}

	if wrotePressureStateFile(&message.ToolFileState{Changes: []message.ToolFileChange{{Path: ".chord/notes/task.md", Deleted: true}}}) {
		t.Fatal("a deleted note is not externalized state")
	}
	if !wrotePressureStateFile(&message.ToolFileState{Changes: []message.ToolFileChange{{Path: "/repo/.chord/plans/20260923-plan.md"}}}) {
		t.Fatal("an absolute plan path inside the project must count as externalized state")
	}
	if wrotePressureStateFile(&message.ToolFileState{Writes: []message.TrackedFileState{{Path: "docs/state-file.md"}}}) {
		t.Fatal("a checkout document must not count as machine state")
	}
}
