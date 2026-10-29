package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

// noticeTestSubAgent returns a worker with the worker-variant compact_context
// registered and the compaction lines pinned, so the observe decisions are
// deterministic.
func noticeTestSubAgent(t *testing.T, reminder float64) (*MainAgent, *SubAgent) {
	t.Helper()
	parent, sub := newMixedBatchTestSubAgent(t)
	t.Cleanup(sub.cancel)
	parent.modelDrivenCompactionEnabled.Store(true)
	parent.globalConfig = config.DefaultConfig()
	parent.globalConfig.Context.Compaction.Threshold = 0.5
	parent.globalConfig.Context.Compaction.Reminder = reminder
	sub.compactContextRegistered = true
	return parent, sub
}

func deliverSubPressureReminder(t *testing.T, sub *SubAgent, requestSeq uint64) {
	t.Helper()
	if sub.observeSubContextPressure("", 4500, 10000, 0.5, 8000) {
		t.Fatal("approaching usage must not defer compaction")
	}
	notice, ok := sub.stagedContextNoticeOverlay()
	if !ok || notice.NoticeLevel != contextNoticePressure {
		t.Fatalf("approaching overlay = %+v, want a pressure notice", notice)
	}
	sub.emitStagedContextNotice()
	sub.noteSubRequestDispatched(requestSeq)
}

func TestSubAgentContextPressureApproachingOncePerWindow(t *testing.T) {
	t.Parallel()
	_, sub := noticeTestSubAgent(t, 0.4)
	// usage = 0.45: between the reminder line (0.4) and the threshold (0.5).
	if sub.observeSubContextPressure("", 4500, 10000, 0.5, 6000) {
		t.Fatal("approaching usage must not defer compaction")
	}
	notice, ok := sub.stagedContextNoticeOverlay()
	if !ok || notice.NoticeLevel != contextNoticePressure || !strings.Contains(notice.Content, "approaching the configured automatic-compaction threshold") {
		t.Fatalf("approaching overlay = %+v, want the pressure notice", notice)
	}
	// Dispatch confirmation consumes the slot and claims the level: the next
	// window observation must not re-notice.
	sub.emitStagedContextNotice()
	if _, ok := sub.stagedContextNoticeOverlay(); ok {
		t.Fatal("emit must consume the staged slot")
	}
	if sub.observeSubContextPressure("", 4500, 10000, 0.5, 6000) {
		t.Fatal("approaching usage must not defer compaction")
	}
	if _, ok := sub.stagedContextNoticeOverlay(); ok {
		t.Fatal("the approaching notice must deliver once per window")
	}
}

func TestSubAgentContextPressureGraceDefersThenExpires(t *testing.T) {
	t.Parallel()
	_, sub := noticeTestSubAgent(t, 0.4)
	deliverSubPressureReminder(t, sub, 1)
	// usage = 0.6: past the threshold (0.5), below the hard ceiling of the
	// request budget (0.95 * 8000 = 7600).
	if !sub.observeSubContextPressure("", 6000, 10000, 0.5, 8000) {
		t.Fatal("the first crossing must defer the deterministic compaction")
	}
	notice, ok := sub.stagedContextNoticeOverlay()
	if !ok || notice.NoticeLevel != contextNoticeImminent || !strings.Contains(notice.Content, "reached the automatic-compaction threshold") {
		t.Fatalf("reached overlay = %+v, want the threshold notice", notice)
	}
	sub.emitStagedContextNotice()
	sub.noteSubRequestDispatched(2)
	// The grace covers the crossing request plus one more; the second request
	// replays the durable row instead of staging a fresh overlay.
	if !sub.observeSubContextPressure("", 6000, 10000, 0.5, 8000) {
		t.Fatal("the grace must cover the crossing request plus one more")
	}
	if _, ok := sub.stagedContextNoticeOverlay(); ok {
		t.Fatal("a delivered level must not stage again inside its window")
	}
	// A transport retry of the same logical request must not consume another
	// grace slot.
	sub.noteSubRequestDispatched(2)
	if sub.subNotices.dispatchCount != 2 {
		t.Fatalf("dispatch count after a retry = %d, want 2 logical requests", sub.subNotices.dispatchCount)
	}
	if !sub.observeSubContextPressure("", 6000, 10000, 0.5, 8000) {
		t.Fatal("a transport retry must not expire the two-request grace")
	}
	sub.noteSubRequestDispatched(3)
	if sub.observeSubContextPressure("", 6000, 10000, 0.5, 8000) {
		t.Fatal("the grace must expire after two dispatched requests")
	}
}

func TestSubAgentContextPressureAbruptCrossingBypassesGraceWithWarning(t *testing.T) {
	t.Parallel()
	_, sub := noticeTestSubAgent(t, 0.4)
	if sub.observeSubContextPressure("", 6000, 10000, 0.5, 8000) {
		t.Fatal("a crossing without a delivered reminder must compact immediately")
	}
	notice, ok := sub.stagedContextNoticeOverlay()
	if !ok || notice.NoticeLevel != contextNoticeWarning ||
		!strings.Contains(notice.Content, "reached the automatic-compaction threshold") {
		t.Fatalf("abrupt crossing overlay = %+v, want one reached-threshold warning", notice)
	}
	if !sub.subNotices.graceExhausted || sub.subNotices.graceActive {
		t.Fatalf("abrupt crossing grace state: active=%v exhausted=%v, want exhausted and inactive",
			sub.subNotices.graceActive, sub.subNotices.graceExhausted)
	}
}

func TestSubAgentPrepareContextAbruptCrossingCompactsAndKeepsWarning(t *testing.T) {
	t.Parallel()
	_, sub := noticeTestSubAgent(t, 0.4)
	sub.llmClient.Close()
	sub.llmClient = nil
	messages := workerCompactionHistory()
	sub.ctxMgr.RestoreMessages(messages)
	estimated := estimateMessagesTokens(sub.ctxMgr, messages)
	sub.ctxMgr.SetTokenBudgets(estimated*2, estimated*2, estimated*10/6, 0)

	prepared := sub.prepareContextForLLM(llmRequestInput{Messages: messages}).Messages
	if len(prepared) >= len(messages) {
		t.Fatalf("abrupt crossing must compact immediately: before=%d after=%d", len(messages), len(prepared))
	}
	// A second preparation in the same logical request must not discard the
	// warning when post-compaction usage falls below the reminder line.
	_ = sub.prepareContextForLLM(llmRequestInput{Messages: prepared}).Messages
	notice, ok := sub.stagedContextNoticeOverlay()
	if !ok || notice.NoticeLevel != contextNoticeWarning {
		t.Fatalf("post-compaction notice = %+v, want the pending abrupt-crossing warning", notice)
	}
	before := sub.ctxMgr.MessageCount()
	sub.emitStagedContextNotice()
	row := sub.ctxMgr.Snapshot()[before]
	if row.Kind != message.KindContextNotice || row.NoticeLevel != contextNoticeWarning {
		t.Fatalf("durable warning = kind:%q level:%q, want a context notice", row.Kind, row.NoticeLevel)
	}
}

func TestSubAgentContextPressureHardCeilingBypassesGrace(t *testing.T) {
	t.Parallel()
	_, sub := noticeTestSubAgent(t, 0.4)
	deliverSubPressureReminder(t, sub, 1)
	// estimated 9600 >= 0.95 * 10000: the safety net cannot be deferred.
	if sub.observeSubContextPressure("", 9600, 10000, 0.5, 10000) {
		t.Fatal("the request-budget hard ceiling must end the grace immediately")
	}
	if _, ok := sub.stagedContextNoticeOverlay(); ok {
		t.Fatal("a request about to be compacted must not carry a grace notice")
	}
	// The bypass exhausts the window: a later crossing compacts immediately.
	if sub.observeSubContextPressure("", 6000, 10000, 0.5, 10000) {
		t.Fatal("a bypassed window must not grant the grace again")
	}
}

func TestSubAgentContextPressureCompactContextEndsGrace(t *testing.T) {
	t.Parallel()
	_, sub := noticeTestSubAgent(t, 0.4)
	deliverSubPressureReminder(t, sub, 1)
	if !sub.observeSubContextPressure("", 6000, 10000, 0.5, 8000) {
		t.Fatal("the first crossing must defer")
	}
	sub.noteSubCompactContextCalled()
	if sub.observeSubContextPressure("", 6000, 10000, 0.5, 8000) {
		t.Fatal("an answered compact_context call must end the grace")
	}
}

func TestSubAgentContextPressureCallBelowThresholdKeepsGrace(t *testing.T) {
	t.Parallel()
	_, sub := noticeTestSubAgent(t, 0.4)
	deliverSubPressureReminder(t, sub, 1)
	// A call answered while usage is still below the threshold (for example a
	// low-gain skip after the approaching notice) is not a reply to the
	// countdown: the later crossing must still grant the grace.
	if sub.observeSubContextPressure("", 4500, 10000, 0.5, 8000) {
		t.Fatal("approaching usage must not defer compaction")
	}
	sub.noteSubCompactContextCalled()
	if !sub.observeSubContextPressure("", 6000, 10000, 0.5, 8000) {
		t.Fatal("a call made before the crossing must not forfeit the grace")
	}
}

func TestSubAgentContextPressureInvisibleToolKeepsCurrentBehavior(t *testing.T) {
	t.Parallel()
	_, sub := noticeTestSubAgent(t, 0.4)
	sub.compactContextRegistered = false
	if sub.observeSubContextPressure("", 6000, 10000, 0.5, 8000) {
		t.Fatal("no deferral without a visible compact_context")
	}
	if _, ok := sub.stagedContextNoticeOverlay(); ok {
		t.Fatal("no notice without a visible compact_context")
	}
}

func TestSubAgentContextPressureDisabledReminderMakesCrossingAbrupt(t *testing.T) {
	t.Parallel()
	_, sub := noticeTestSubAgent(t, config.CompactionReminderDisabled)
	// Below the threshold with the reminder line disabled: no notice at all.
	if sub.observeSubContextPressure("", 4500, 10000, 0.5, 6000) {
		t.Fatal("no deferral below the threshold")
	}
	if _, ok := sub.stagedContextNoticeOverlay(); ok {
		t.Fatal("a disabled reminder line must silence the approaching notice")
	}
	// Without an early reminder, the crossing is abrupt and compacts at once.
	if sub.observeSubContextPressure("", 6000, 10000, 0.5, 8000) {
		t.Fatal("the reminder-disabled crossing must not grant grace")
	}
	if notice, ok := sub.stagedContextNoticeOverlay(); !ok || notice.NoticeLevel != contextNoticeWarning {
		t.Fatalf("reached overlay = %+v, want the abrupt-crossing warning", notice)
	}
}

func TestSubAgentContextNoticeWindowAdvanceResetsClaims(t *testing.T) {
	t.Parallel()
	_, sub := noticeTestSubAgent(t, 0.4)
	deliverSubPressureReminder(t, sub, 1)
	if !sub.observeSubContextPressure("", 6000, 10000, 0.5, 8000) {
		t.Fatal("the first crossing must defer")
	}
	sub.emitStagedContextNotice()
	sub.noteSubCompactContextCalled()
	sub.advanceSubNoticeWindow()
	deliverSubPressureReminder(t, sub, 2)
	if !sub.observeSubContextPressure("", 6000, 10000, 0.5, 8000) {
		t.Fatal("a fresh window grants the grace again")
	}
	notice, ok := sub.stagedContextNoticeOverlay()
	if !ok || notice.NoticeLevel != contextNoticeImminent {
		t.Fatalf("fresh-window overlay = %+v, want the threshold notice again", notice)
	}
}

func TestSubAgentContextNoticeRestoreRebuildsDeliveryMarks(t *testing.T) {
	t.Parallel()
	_, sub := noticeTestSubAgent(t, 0.4)
	messages := []message.Message{
		{Role: message.RoleUser, Content: "task"},
		{Role: message.RoleUser, Kind: message.KindContextNotice, NoticeLevel: contextNoticePressure, Content: "old"},
		{Role: message.RoleUser, IsCompactionSummary: true, Content: "[system] SubAgent context checkpoint"},
		{Role: message.RoleUser, Kind: message.KindContextNotice, NoticeLevel: contextNoticePressure, Content: "current pressure"},
		{Role: message.RoleUser, Kind: message.KindContextNotice, NoticeLevel: contextNoticeImminent, Content: "current"},
	}
	sub.restoreContextNoticeState(messages)
	// The fresh window grants grace because its transcript contains the
	// approaching notice, and neither delivered level is repeated.
	if !sub.observeSubContextPressure("", 6000, 10000, 0.5, 8000) {
		t.Fatal("the restored worker's first crossing must defer")
	}
	if _, ok := sub.stagedContextNoticeOverlay(); ok {
		t.Fatal("delivered levels must not re-notice after a restore")
	}
	if !sub.subNotices.delivered[contextNoticePressure] {
		t.Fatal("the post-summary pressure row must mark the level delivered")
	}
	// A pre-summary row belongs to an ended window and must not suppress the
	// fresh window's pressure level.
	sub.restoreContextNoticeState([]message.Message{
		{Role: message.RoleUser, Content: "task"},
		{Role: message.RoleUser, Kind: message.KindContextNotice, NoticeLevel: contextNoticePressure, Content: "old"},
		{Role: message.RoleUser, IsCompactionSummary: true, Content: "[system] SubAgent context checkpoint"},
	})
	if sub.observeSubContextPressure("", 4500, 10000, 0.5, 6000) {
		t.Fatal("approaching usage must not defer")
	}
	if _, ok := sub.stagedContextNoticeOverlay(); !ok {
		t.Fatal("the fresh window's approaching level must deliver")
	}
	if sub.subNotices.delivered[contextNoticePressure] {
		t.Fatal("a pre-summary row must not mark the fresh window delivered")
	}
}

func TestSubAgentContextNoticeEmitPersistsRowAndEmitsEvent(t *testing.T) {
	t.Parallel()
	parent, sub := noticeTestSubAgent(t, 0.4)
	deliverSubPressureReminder(t, sub, 1)
	if !sub.observeSubContextPressure("", 6000, 10000, 0.5, 8000) {
		t.Fatal("the first crossing must defer")
	}
	before := sub.ctxMgr.MessageCount()
	sub.emitStagedContextNotice()
	row := sub.ctxMgr.Snapshot()[before]
	if row.Kind != message.KindContextNotice || row.NoticeLevel != contextNoticeImminent {
		t.Fatalf("durable row = kind:%q level:%q, want a context notice", row.Kind, row.NoticeLevel)
	}
	if !strings.HasPrefix(row.Content, "<system-reminder>") {
		t.Fatalf("durable row content = %q, want the system-reminder wrapper", row.Content)
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case evt := <-parent.Events():
			if notice, ok := evt.(ContextNoticeEvent); ok {
				if notice.Level != contextNoticeImminent {
					continue
				}
				if notice.AgentID != sub.instanceID {
					t.Fatalf("event agent = %q, want %q", notice.AgentID, sub.instanceID)
				}
				if notice.MessageIndex != before {
					t.Fatalf("event = level:%q index:%d, want imminent at %d", notice.Level, notice.MessageIndex, before)
				}
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for the context notice event")
		}
	}
}

func TestSubAgentPrepareContextDefersCompactionDuringGrace(t *testing.T) {
	t.Parallel()
	_, sub := noticeTestSubAgent(t, 0.4)
	sub.llmClient.Close()
	sub.llmClient = nil // This test supplies its own token budgets.
	messages := workerCompactionHistory()
	sub.ctxMgr.RestoreMessages(messages)
	deliverSubPressureReminder(t, sub, 1)
	messages = sub.ctxMgr.Snapshot()
	estimated := estimateMessagesTokens(sub.ctxMgr, messages)
	// usage ≈ 0.6 of the compaction budget (threshold 0.5), safely below the
	// 0.95 hard ceiling of the request budget.
	sub.ctxMgr.SetTokenBudgets(estimated*2, estimated*2, estimated*10/6, 0)

	prepared := sub.prepareContextForLLM(llmRequestInput{Messages: messages}).Messages
	if len(prepared) != len(messages) {
		t.Fatalf("grace-deferred request must go out uncompacted: before=%d after=%d", len(messages), len(prepared))
	}
	if _, ok := sub.stagedContextNoticeOverlay(); !ok {
		t.Fatal("the deferred request must carry the reached notice")
	}
	sub.emitStagedContextNotice()
	sub.noteSubRequestDispatched(2)
	sub.noteSubRequestDispatched(3)
	prepared = sub.prepareContextForLLM(llmRequestInput{Messages: messages}).Messages
	if len(prepared) >= len(messages) {
		t.Fatalf("expired grace must compact: before=%d after=%d", len(messages), len(prepared))
	}
}
