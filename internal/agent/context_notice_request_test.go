package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
)

func noticeRequestAgent(t *testing.T) *MainAgent {
	t.Helper()
	a := newTestMainAgent(t, t.TempDir())
	a.globalConfig = &config.Config{Context: config.ContextConfig{Compaction: config.CompactionConfig{Threshold: 0.9, Reminder: 0.6}}}
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(100000, 100000, 0, 0.9)
	enableTestCompactContext(a)
	return a
}

func TestPressureNoticeModelSwitchUsesPreviousUsage(t *testing.T) {
	a := noticeRequestAgent(t)
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 70000})
	a.ctxMgr.InvalidateSizeObservation()
	// A tiny request surface must not replace the last model's usage estimate.
	messages := []message.Message{{Role: message.RoleUser, Content: "continue"}}
	budget := 100000 + a.effectiveCompactionReservedInput()
	pressure, upper := a.pressureNoticeValidity(messages, "provider/target", budget, true)
	if !pressure || upper {
		t.Fatalf("slots = (%v, %v), want lower only", pressure, upper)
	}
	pressure, upper = a.pressureNoticeValidity(messages, "provider/target", budget*3, true)
	if pressure || upper {
		t.Fatalf("larger model should need no notices: %v %v", pressure, upper)
	}
	pressure, upper = a.pressureNoticeValidity(messages, "provider/target", 50000+a.effectiveCompactionReservedInput(), true)
	if !pressure || !upper {
		t.Fatalf("smaller model should cross both lines: %v %v", pressure, upper)
	}
	if a.ctxMgr.AutoCompactDecision().ShouldCompact {
		t.Fatal("notification estimates must not arm compaction")
	}
}

func TestPressureNoticeReductionUsesBytesInsteadOfUsage(t *testing.T) {
	a := noticeRequestAgent(t)
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 99000})
	budget := 100000 + a.effectiveCompactionReservedInput()
	messages := []message.Message{{Role: message.RoleUser, Content: "short request"}}
	pressure, upper := a.pressureNoticeValidity(messages, "provider/model", budget, false)
	if pressure || upper {
		t.Fatal("reduced request must use its byte estimate")
	}
	a.ctxMgr.NoteMissingUsage()
	messages[0].Content = strings.Repeat("a", 400000)
	pressure, upper = a.pressureNoticeValidity(messages, "provider/model", budget, true)
	if !pressure || !upper {
		t.Fatal("without usage, model switches must fall back to byte estimation")
	}
}

func TestPressureNoticeReductionOnlyInvalidatesFollowingNotices(t *testing.T) {
	for _, editIndex := range []int{0, 2, 4} {
		t.Run(string(rune('0'+editIndex)), func(t *testing.T) {
			a := noticeRequestAgent(t)
			messages := []message.Message{
				{Role: message.RoleUser, Content: "prefix"},
				{Role: message.RoleUser, Kind: message.KindContextNotice, NoticeLevel: contextNoticePressure, PressureCycleID: 1, Content: "lower"},
				{Role: message.RoleUser, Content: "middle"},
				{Role: message.RoleUser, Kind: message.KindContextNotice, NoticeLevel: contextNoticeWarning, PressureCycleID: 1, Content: "upper"},
				{Role: message.RoleUser, Content: "tail"},
			}
			a.ctxMgr.RestoreMessages(messages)
			a.installContextNoticePresence(messages)
			a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 99000})
			budget := 100000 + a.effectiveCompactionReservedInput()
			a.reconcilePressureNoticesForModel(messages, messages, "provider/model", budget)
			a.rememberPressureNoticeRequest(messages, "provider/model")
			changed := append([]message.Message(nil), messages...)
			changed[editIndex].Content = "reduced"
			got := a.reconcilePressureNoticesForModel(changed, changed, "provider/model", budget)
			var lower, upper bool
			for _, msg := range got {
				if msg.Kind != message.KindContextNotice {
					continue
				}
				lower = lower || msg.NoticeLevel == contextNoticePressure
				upper = upper || msg.NoticeLevel == contextNoticeWarning
			}
			if lower != (editIndex > 1) || upper != (editIndex > 3) {
				t.Fatalf("edit at %d retained (%v,%v)", editIndex, lower, upper)
			}
			if !containsContextNotice(a.ctxMgr.Snapshot()) {
				t.Fatal("in-flight filtering must not rewrite durable history")
			}
		})
	}
}

func TestFallbackFiltersInvalidNoticesUsingPreviousUsage(t *testing.T) {
	a := noticeRequestAgent(t)
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 70000})
	a.overlayClaims.noticeModel = "provider/source"
	lower := message.Message{Role: message.RoleUser, Kind: message.KindContextNotice, NoticeLevel: contextNoticePressure, Content: "lower"}
	upper := message.Message{Role: message.RoleUser, Kind: message.KindContextNotice, NoticeLevel: contextNoticeWarning, Content: "upper"}
	a.ctxMgr.RestoreMessages([]message.Message{lower, upper})
	a.contextNoticesPersisted.Store(true)
	messages := []message.Message{lower, upper, {Role: message.RoleUser, Kind: message.KindTurnOverlay, NoticeLevel: contextNoticeWarning, Content: "<system-reminder>\n" + compactionThresholdNoticeText + "\n</system-reminder>"}, {Role: message.RoleUser, Kind: message.KindTurnOverlay, Content: "continue"}}
	got := a.reconcileFallbackPressureNotices(messages, "provider/target", 100000+a.effectiveCompactionReservedInput(), true)
	if len(got) != 2 || got[0].NoticeLevel != contextNoticePressure || got[1].Content != "continue" {
		t.Fatalf("filtered request=%+v", got)
	}
	if trailingTurnOverlayCount(messages)-trailingTurnOverlayCount(got) != 1 {
		t.Fatal("tail accounting must exclude only the removed overlay")
	}
	if len(a.ctxMgr.Snapshot()) != 2 {
		t.Fatal("fallback must not rewrite compaction's durable source")
	}
}

func TestCancelledNoticePreparationKeepsDispatchBaseline(t *testing.T) {
	a := noticeRequestAgent(t)
	original := []message.Message{{Role: message.RoleUser, Content: "original"}, {Role: message.RoleUser, Kind: message.KindContextNotice, NoticeLevel: contextNoticePressure, Content: "notice"}}
	a.rememberPressureNoticeRequest(original, "provider/model")
	prepared := append([]message.Message(nil), original...)
	prepared[0].Content = "reduced"
	a.reconcilePressureNoticesForModel(prepared, prepared, "provider/model", 100000)
	if a.overlayClaims.noticePrefixSource[0].Content != "original" {
		t.Fatal("preparation must not commit a new dispatched prefix")
	}
}

func TestFallbackAddsOnlyMissingThresholdAndReusesDurableRows(t *testing.T) {
	a := noticeRequestAgent(t)
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 99000})
	messages := []message.Message{{Role: message.RoleUser, Content: "continue"}}
	got := a.reconcileFallbackPressureNotices(messages, "provider/target", 100000, true)
	if len(got) != 3 || a.ctxMgr.MessageCount() != 2 {
		t.Fatalf("request=%d durable=%d, want two notices", len(got), a.ctxMgr.MessageCount())
	}
	// An intervening large-budget model removed both notices from its request;
	// the next smaller model reuses the durable rows without appending cards.
	got = a.reconcileFallbackPressureNotices(messages, "provider/other", 100000, true)
	if len(got) != 3 || a.ctxMgr.MessageCount() != 2 {
		t.Fatalf("reused request=%d durable=%d", len(got), a.ctxMgr.MessageCount())
	}
}
