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
	a.ctxMgr = ctxmgr.NewManagerWithTokenBudgets(100000, 100000, 100000, 0, 0.9)
	enableTestCompactContext(a)
	return a
}

func TestPressureNoticeModelSwitchUsesPreviousUsage(t *testing.T) {
	t.Parallel()
	a := noticeRequestAgent(t)
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 70000})
	a.ctxMgr.InvalidateSizeObservation()
	// A tiny request surface must not replace the last model's usage estimate.
	budget := 100000 + a.effectiveCompactionReservedInput()
	pressure, upper := a.pressureNoticeValidity("provider/target", budget)
	if !pressure || upper {
		t.Fatalf("slots = (%v, %v), want lower only", pressure, upper)
	}
	pressure, upper = a.pressureNoticeValidity("provider/target", budget*3)
	if pressure || upper {
		t.Fatalf("larger model should need no notices: %v %v", pressure, upper)
	}
	pressure, upper = a.pressureNoticeValidity("provider/target", 50000+a.effectiveCompactionReservedInput())
	if !pressure || !upper {
		t.Fatalf("smaller model should cross both lines: %v %v", pressure, upper)
	}
	if a.ctxMgr.AutoCompactDecision().ShouldCompact {
		t.Fatal("notification estimates must not arm compaction")
	}
}

// The context size behind a pressure notice comes only from usage readings:
// the observed baseline, the frozen estimate when the latest response missed
// usage, or a retained reading reused as an estimate. A byte-based
// request-surface estimate never stands in for a missing sample — without a
// usable reading the size reads 0 and crosses no line.
func TestPressureNoticeNeverFallsBackToByteEstimate(t *testing.T) {
	t.Parallel()
	a := noticeRequestAgent(t)
	budget := 100000 + a.effectiveCompactionReservedInput()
	// No usage sample at all: even a huge request surface (the shape of a
	// pasted first message) reads as context size 0 and justifies no notice.
	if pressure, upper := a.pressureNoticeValidity("provider/model", budget); pressure || upper {
		t.Fatal("without a usage sample the byte estimate must not justify a notice")
	}
	// A response that missed usage without any calibration sample keeps the
	// size unknown (frozen estimate 0): still no notice.
	a.ctxMgr.NoteMissingUsage()
	if pressure, upper := a.pressureNoticeValidity("provider/model", budget); pressure || upper {
		t.Fatal("an unknown usage state must not justify a notice")
	}
	// The reading, not the request surface, decides: a crossing usage sample
	// fires on a tiny surface, and a below-line reading stays quiet on a huge
	// one.
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 99000})
	if pressure, upper := a.pressureNoticeValidity("provider/model", budget); !pressure || !upper {
		t.Fatal("a crossing usage reading must justify notices on a tiny surface")
	}
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 1000})
	if pressure, upper := a.pressureNoticeValidity("provider/model", budget); pressure || upper {
		t.Fatal("a below-line usage reading must not justify notices on a huge surface")
	}
}

// Notice retention follows the usage reading, not the request surface: a
// changed prefix before a notice row no longer re-derives validity from a
// byte estimate, so a crossing reading keeps both rows wherever the edit
// lands. The durable history is never rewritten by in-flight filtering.
func TestPressureNoticeRetentionFollowsUsageReadingNotPrefixEdits(t *testing.T) {
	t.Parallel()
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
			if !lower || !upper {
				t.Fatalf("edit at %d retained (%v,%v), want both from the crossing reading", editIndex, lower, upper)
			}
			if !containsContextNotice(a.ctxMgr.Snapshot()) {
				t.Fatal("in-flight filtering must not rewrite durable history")
			}
		})
	}
}

func TestFallbackFiltersInvalidNoticesUsingPreviousUsage(t *testing.T) {
	t.Parallel()
	a := noticeRequestAgent(t)
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 70000})
	a.overlayClaims.noticeModel = "provider/source"
	lower := message.Message{Role: message.RoleUser, Kind: message.KindContextNotice, NoticeLevel: contextNoticePressure, Content: "lower"}
	upper := message.Message{Role: message.RoleUser, Kind: message.KindContextNotice, NoticeLevel: contextNoticeWarning, Content: "upper"}
	a.ctxMgr.RestoreMessages([]message.Message{lower, upper})
	a.contextNoticesPersisted.Store(true)
	messages := []message.Message{lower, upper, {Role: message.RoleUser, Kind: message.KindTurnOverlay, NoticeLevel: contextNoticeWarning, Content: "<system-reminder>\n" + compactionThresholdNoticeText + "\n</system-reminder>"}, {Role: message.RoleUser, Kind: message.KindTurnOverlay, Content: "continue"}}
	got := a.reconcileFallbackPressureNotices(messages, "provider/target", 100000+a.effectiveCompactionReservedInput())
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
	t.Parallel()
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

// Regression (2026-10-16, session 20261016170358037): a fresh session whose
// first request timed out reached the fallback boundary with zero observed
// usage. The boundary must not stage a pressure notice from the byte estimate
// of the pasted first message — with no usable reading the context size is 0
// and no line is crossed.
func TestFallbackWithoutUsageSampleStagesNoNotice(t *testing.T) {
	t.Parallel()
	a := noticeRequestAgent(t)
	a.overlayClaims.noticeModel = "provider/source"
	messages := []message.Message{{Role: message.RoleUser, Content: strings.Repeat("pasted transcript line\n", 20000)}}
	got := a.reconcileFallbackPressureNotices(messages, "provider/target", 100000)
	if len(got) != len(messages) {
		t.Fatalf("fallback appended %d messages, want none", len(got)-len(messages))
	}
	if pending := a.takePendingContextNoticeText(contextNoticePressure); pending != "" {
		t.Fatal("no usage sample must not stage a pressure reminder")
	}
	if containsContextNotice(a.ctxMgr.Snapshot()) {
		t.Fatal("no usage sample must not write a durable notice row")
	}
	if a.overlayClaims.reminder.deliveryPending {
		t.Fatal("no usage sample must not mark a reminder delivery")
	}
}

func TestFallbackAddsOnlyMissingThresholdAndReusesDurableRows(t *testing.T) {
	t.Parallel()
	a := noticeRequestAgent(t)
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 99000})
	messages := []message.Message{{Role: message.RoleUser, Content: "continue"}}
	// Both lines are crossed at once: the ladder appends only the higher
	// notice, whose row the dispatch persists.
	got := a.reconcileFallbackPressureNotices(messages, "provider/target", 100000)
	if len(got) != 2 || a.ctxMgr.MessageCount() != 1 {
		t.Fatalf("request=%d durable=%d, want only the upper threshold", len(got), a.ctxMgr.MessageCount())
	}
	// An intervening large-budget model removed the notice from its request;
	// the next smaller model reuses the durable row without appending cards,
	// and the retained warning row keeps the lower reminder suppressed.
	got = a.reconcileFallbackPressureNotices(messages, "provider/other", 100000)
	if len(got) != 2 || a.ctxMgr.MessageCount() != 1 {
		t.Fatalf("reused request=%d durable=%d", len(got), a.ctxMgr.MessageCount())
	}
}
