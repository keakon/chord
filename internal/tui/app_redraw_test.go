package tui

import (
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/keakon/bubbletea/v2"
)

func containsCmd(cmds []tea.Cmd, target tea.Cmd) bool {
	for _, cmd := range cmds {
		if reflect.ValueOf(cmd).Pointer() == reflect.ValueOf(target).Pointer() {
			return true
		}
	}
	return false
}

func hasDiagnosticEvent(events []tuiDiagnosticEvent, kind, detailSubstr string) bool {
	for _, evt := range events {
		if evt.Kind != kind {
			continue
		}
		if detailSubstr == "" || strings.Contains(evt.Detail, detailSubstr) {
			return true
		}
	}
	return false
}

func TestHostRedrawPolicyForReason(t *testing.T) {
	tests := []struct {
		reason                    string
		requestWindowSize         bool
		suppressPostFocusFallback bool
		suppressPeriodicViewer    bool
		postHostFallbackReason    string
		postHostFallbackDelay     time.Duration
	}{
		{reason: "focus-restore", requestWindowSize: true, suppressPostFocusFallback: true},
		{reason: "post-focus-settle-redraw", requestWindowSize: true, suppressPostFocusFallback: false},
		{reason: "post-focus-settle-fallback", requestWindowSize: true, suppressPostFocusFallback: true},
		{reason: "stream-flush", suppressPostFocusFallback: false, suppressPeriodicViewer: true},
		{reason: "scroll-flush", suppressPostFocusFallback: false, suppressPeriodicViewer: true, postHostFallbackReason: "scroll-flush-fallback", postHostFallbackDelay: scrollFlushFallbackRedrawDelay},
		{reason: "content-boundary", suppressPostFocusFallback: false, postHostFallbackReason: "content-boundary-fallback", postHostFallbackDelay: contentBoundaryFallbackRedrawDelay},
		{reason: "live-append", suppressPostFocusFallback: false, postHostFallbackReason: "content-boundary-fallback", postHostFallbackDelay: contentBoundaryFallbackRedrawDelay},
		{reason: "toast-boundary", suppressPostFocusFallback: false, postHostFallbackReason: "content-boundary-fallback", postHostFallbackDelay: contentBoundaryFallbackRedrawDelay},
		{reason: "background-dirty-focus", suppressPostFocusFallback: false, postHostFallbackReason: "background-dirty-focus-fallback", postHostFallbackDelay: contentBoundaryFallbackRedrawDelay},
		{reason: "content-boundary-fallback", suppressPostFocusFallback: true},
		{reason: "debug-dump", suppressPostFocusFallback: true},
		{reason: "unknown", suppressPostFocusFallback: true},
	}

	for _, tc := range tests {
		t.Run(tc.reason, func(t *testing.T) {
			got := hostRedrawPolicyForReason(tc.reason)
			if got.requestWindowSize != tc.requestWindowSize {
				t.Fatalf("requestWindowSize = %t, want %t", got.requestWindowSize, tc.requestWindowSize)
			}
			if got.suppressPostFocusFallback != tc.suppressPostFocusFallback {
				t.Fatalf("suppressPostFocusFallback = %t, want %t", got.suppressPostFocusFallback, tc.suppressPostFocusFallback)
			}
			if got.suppressPeriodicViewer != tc.suppressPeriodicViewer {
				t.Fatalf("suppressPeriodicViewer = %t, want %t", got.suppressPeriodicViewer, tc.suppressPeriodicViewer)
			}
			if got.postHostFallbackReason != tc.postHostFallbackReason {
				t.Fatalf("postHostFallbackReason = %q, want %q", got.postHostFallbackReason, tc.postHostFallbackReason)
			}
			if got.postHostFallbackDelay != tc.postHostFallbackDelay {
				t.Fatalf("postHostFallbackDelay = %s, want %s", got.postHostFallbackDelay, tc.postHostFallbackDelay)
			}
		})
	}
}

func TestToastBoundaryHostRedrawOnToastAppearAndDisappear(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	m.SetFocusResizeFreezeEnabled(true)
	oldHeight := m.viewport.height

	cmd := m.enqueueToast("diagnostics bundle exported", "info")
	if cmd == nil {
		t.Fatal("enqueueToast should schedule toast timer and boundary redraw")
	}
	if got, want := m.viewport.height, oldHeight-1; got != want {
		t.Fatalf("viewport height after toast appear = %d, want %d", got, want)
	}
	if m.lastHostRedrawReason != "toast-boundary" {
		t.Fatalf("lastHostRedrawReason after toast appear = %q, want toast-boundary", m.lastHostRedrawReason)
	}
	if !hasDiagnosticEvent(m.snapshotTUIDiagnosticEvents(), "toast-boundary", "active=true") {
		t.Fatalf("recent events missing toast-boundary active=true marker: %#v", m.snapshotTUIDiagnosticEvents())
	}

	m.lastHostRedrawAt = time.Time{}
	cmd = m.handleToastTick()
	if cmd == nil {
		t.Fatal("toast expiry should schedule boundary redraw")
	}
	if m.activeToast != nil {
		t.Fatalf("activeToast = %#v, want nil", m.activeToast)
	}
	if got, want := m.viewport.height, oldHeight; got != want {
		t.Fatalf("viewport height after toast disappear = %d, want %d", got, want)
	}
	if m.lastHostRedrawReason != "toast-boundary" {
		t.Fatalf("lastHostRedrawReason after toast disappear = %q, want toast-boundary", m.lastHostRedrawReason)
	}
	if !hasDiagnosticEvent(m.snapshotTUIDiagnosticEvents(), "toast-boundary", "active=false") {
		t.Fatalf("recent events missing toast-boundary active=false marker: %#v", m.snapshotTUIDiagnosticEvents())
	}
}

func TestHostRedrawSequenceSkipsWindowSizeForPureRepairReasons(t *testing.T) {
	tests := []string{
		"stream-flush",
		"scroll-flush",
		"scroll-flush-fallback",
		"debug-dump",
		"content-boundary",
		"content-boundary-fallback",
		"live-append",
		"toast-boundary",
		"background-dirty-focus",
		"background-dirty-focus-fallback",
	}
	for _, reason := range tests {
		t.Run(reason, func(t *testing.T) {
			m := NewModelWithSize(nil, 80, 24)
			cmds := m.hostRedrawSequence(reason)
			if len(cmds) != 2 {
				t.Fatalf("%s redraw cmd count = %d, want 2", reason, len(cmds))
			}
			if !containsCmd(cmds, tea.ClearScreen) {
				t.Fatalf("%s redraw should include ClearScreen", reason)
			}
			if containsCmd(cmds, tea.RequestWindowSize) {
				t.Fatalf("%s redraw should not include RequestWindowSize", reason)
			}
		})
	}
}

func TestContentBoundaryHostRedrawMinInterval(t *testing.T) {
	t.Run("default terminal", func(t *testing.T) {
		m := NewModelWithSize(nil, 80, 24)
		m.SetFocusResizeFreezeEnabled(false)
		if got := m.contentBoundaryHostRedrawMinInterval(); got != defaultContentBoundaryHostRedrawMinInterval {
			t.Fatalf("contentBoundaryHostRedrawMinInterval = %s, want %s", got, defaultContentBoundaryHostRedrawMinInterval)
		}
	})

	t.Run("ghostty/cmux workaround enabled", func(t *testing.T) {
		m := NewModelWithSize(nil, 80, 24)
		m.SetFocusResizeFreezeEnabled(true)
		if got := m.contentBoundaryHostRedrawMinInterval(); got != riskyHostContentBoundaryHostRedrawMinInterval {
			t.Fatalf("contentBoundaryHostRedrawMinInterval = %s, want %s", got, riskyHostContentBoundaryHostRedrawMinInterval)
		}
	})
}

func TestMaybePostHostRedrawFallbackCmdOnlyArmsNearFocusRestore(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	now := time.Now()

	for _, reason := range []string{"scroll-flush", "content-boundary", "live-append", "toast-boundary", "background-dirty-focus"} {
		t.Run(reason, func(t *testing.T) {
			m.lastForegroundAt = time.Time{}
			if cmd := m.maybePostHostRedrawFallbackCmd(reason, 1, now); cmd != nil {
				t.Fatalf("%s fallback should not arm without a recent focus", reason)
			}

			m.lastForegroundAt = now.Add(-scrollFlushFallbackAfterFocusWindow - time.Millisecond)
			if cmd := m.maybePostHostRedrawFallbackCmd(reason, 2, now); cmd != nil {
				t.Fatalf("%s fallback should not arm outside the post-focus window", reason)
			}

			m.lastForegroundAt = now.Add(-time.Second)
			if cmd := m.maybePostHostRedrawFallbackCmd(reason, 3, now); cmd == nil {
				t.Fatalf("%s fallback should arm inside the post-focus window", reason)
			}
		})
	}
}

func TestHostRedrawCmdDoesNotArmSuccessfulScrollFallbackOutsideFocusWindow(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.SetFocusResizeFreezeEnabled(true)
	now := time.Now()
	m.lastForegroundAt = now.Add(-scrollFlushFallbackAfterFocusWindow - time.Second)
	m.lastHostRedrawAt = now.Add(-hostRedrawMinInterval - time.Second)
	m.hostRedrawGeneration = 7

	cmd := m.hostRedrawCmd("scroll-flush")
	if cmd == nil {
		t.Fatal("successful scroll-flush should still schedule the primary host redraw")
	}
	if m.hostRedrawGeneration != 8 {
		t.Fatalf("hostRedrawGeneration = %d, want 8", m.hostRedrawGeneration)
	}
	if hasDiagnosticEvent(m.snapshotTUIDiagnosticEvents(), "post-host-redraw-fallback-arm", "reason=scroll-flush") {
		t.Fatal("successful scroll-flush outside the focus window should not arm a fallback")
	}
}

func TestHostRedrawCmdArmsFallbackForThrottledScrollBurstOutsideFocusWindow(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.SetFocusResizeFreezeEnabled(true)
	now := time.Now()
	m.lastForegroundAt = now.Add(-scrollFlushFallbackAfterFocusWindow - time.Second)
	m.lastHostRedrawAt = now.Add(-hostRedrawMinInterval / 2)
	m.lastHostRedrawReason = "scroll-flush"
	m.hostRedrawGeneration = 7

	cmd := m.hostRedrawCmd("scroll-flush")
	if cmd == nil {
		t.Fatal("throttled scroll-flush should arm a delayed fallback on risky hosts")
	}
	if m.hostRedrawGeneration != 7 {
		t.Fatalf("hostRedrawGeneration = %d, want unchanged 7", m.hostRedrawGeneration)
	}
	if m.lastHostRedrawReason != "scroll-flush" {
		t.Fatalf("lastHostRedrawReason = %q, want unchanged scroll-flush", m.lastHostRedrawReason)
	}
	if !hasDiagnosticEvent(m.snapshotTUIDiagnosticEvents(), "post-host-redraw-fallback-arm", "throttled=true") {
		t.Fatalf("recent events missing throttled fallback arm marker: %#v", m.snapshotTUIDiagnosticEvents())
	}
}

func TestHostRedrawCmdDoesNotArmFallbackForThrottledNonScroll(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.SetFocusResizeFreezeEnabled(true)
	now := time.Now()
	m.lastForegroundAt = now.Add(-scrollFlushFallbackAfterFocusWindow - time.Second)
	m.lastHostRedrawAt = now.Add(-hostRedrawMinInterval / 2)
	m.lastHostRedrawReason = "stream-flush"
	m.hostRedrawGeneration = 7

	cmd := m.hostRedrawCmd("stream-flush")
	if cmd != nil {
		t.Fatalf("throttled non-scroll redraw should not arm a fallback, got %#v", cmd)
	}
	if hasDiagnosticEvent(m.snapshotTUIDiagnosticEvents(), "post-host-redraw-fallback-arm", "") {
		t.Fatalf("throttled non-scroll redraw should not arm fallback: %#v", m.snapshotTUIDiagnosticEvents())
	}
}

func TestHostRedrawForContentBoundaryCmdArmsFallbackForThrottledContentBoundaryClassOutsideFocusWindow(t *testing.T) {
	tests := []struct {
		name       string
		reason     string
		lastReason string
	}{
		{name: "content-boundary-after-content-boundary", reason: "content-boundary", lastReason: "content-boundary"},
		{name: "live-append-after-live-append", reason: "live-append", lastReason: "live-append"},
		{name: "content-boundary-after-live-append", reason: "content-boundary", lastReason: "live-append"},
		{name: "live-append-after-content-boundary", reason: "live-append", lastReason: "content-boundary"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewModelWithSize(nil, 120, 40)
			m.SetFocusResizeFreezeEnabled(true)
			m.displayState = stateForeground
			now := time.Now()
			m.lastForegroundAt = now.Add(-scrollFlushFallbackAfterFocusWindow - time.Second)
			m.lastHostRedrawAt = now.Add(-m.contentBoundaryHostRedrawMinInterval() / 2)
			m.lastHostRedrawReason = tt.lastReason
			m.hostRedrawGeneration = 7

			cmd := m.hostRedrawForContentBoundaryCmd(tt.reason)
			if cmd == nil {
				t.Fatalf("throttled content-boundary-class %s after %s should arm a delayed fallback on risky hosts", tt.reason, tt.lastReason)
			}
			if m.hostRedrawGeneration != 7 {
				t.Fatalf("hostRedrawGeneration = %d, want unchanged 7", m.hostRedrawGeneration)
			}
			if m.lastHostRedrawReason != tt.lastReason {
				t.Fatalf("lastHostRedrawReason = %q, want unchanged %s", m.lastHostRedrawReason, tt.lastReason)
			}
			if !hasDiagnosticEvent(m.snapshotTUIDiagnosticEvents(), "post-host-redraw-fallback-arm", "content_boundary_throttled=true") {
				t.Fatalf("recent events missing content-boundary fallback arm marker: %#v", m.snapshotTUIDiagnosticEvents())
			}
		})
	}
}

func TestHostRedrawForContentBoundaryCmdDoesNotArmSteadyFallbackAfterScrollRedraw(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.SetFocusResizeFreezeEnabled(true)
	m.displayState = stateForeground
	now := time.Now()
	m.lastForegroundAt = now.Add(-scrollFlushFallbackAfterFocusWindow - time.Second)
	m.lastHostRedrawAt = now.Add(-m.contentBoundaryHostRedrawMinInterval() / 2)
	m.lastHostRedrawReason = "scroll-flush"
	m.hostRedrawGeneration = 7

	cmd := m.hostRedrawForContentBoundaryCmd("content-boundary")
	if cmd != nil {
		t.Fatalf("content-boundary after recent scroll redraw should keep using the scroll fallback path, got %#v", cmd)
	}
	if hasDiagnosticEvent(m.snapshotTUIDiagnosticEvents(), "post-host-redraw-fallback-arm", "content_boundary_throttled=true") {
		t.Fatalf("non-content-boundary-class throttle should not arm a separate fallback: %#v", m.snapshotTUIDiagnosticEvents())
	}
}

func TestHostRedrawForContentBoundaryCmdDoesNotArmSteadyFallbackOnNonRiskyHost(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.SetFocusResizeFreezeEnabled(false)
	m.displayState = stateForeground
	now := time.Now()
	m.lastForegroundAt = now.Add(-scrollFlushFallbackAfterFocusWindow - time.Second)
	m.lastHostRedrawAt = now.Add(-m.contentBoundaryHostRedrawMinInterval() / 2)
	m.lastHostRedrawReason = "content-boundary"
	m.hostRedrawGeneration = 7

	cmd := m.hostRedrawForContentBoundaryCmd("content-boundary")
	if cmd != nil {
		t.Fatalf("non-risky host should keep throttled content-boundary single-pass, got %#v", cmd)
	}
	if hasDiagnosticEvent(m.snapshotTUIDiagnosticEvents(), "post-host-redraw-fallback-arm", "content_boundary_throttled=true") {
		t.Fatalf("non-risky host should not arm content-boundary fallback: %#v", m.snapshotTUIDiagnosticEvents())
	}
}

func TestPostFocusSettleFallbackSkipsWhenStrongHostRedrawAlreadyRanAfterFocus(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.SetFocusResizeFreezeEnabled(true)
	m.focusResizeGeneration = 7
	now := time.Now()
	m.lastForegroundAt = now
	m.lastHostRedrawAt = now.Add(100 * time.Millisecond)
	m.lastHostRedrawReason = "focus-restore"

	if cmd := m.handlePostFocusSettleRedraw(postFocusSettleRedrawMsg{generation: 7, fallback: true}); cmd != nil {
		t.Fatalf("fallback redraw should skip when a later strong host redraw already ran after focus, got %#v", cmd)
	}
}

func TestPostFocusSettleFallbackIgnoresWeakHostRedrawAfterFocus(t *testing.T) {
	tests := []string{"content-boundary", "live-append", "scroll-flush", "stream-flush", "background-dirty-focus", "post-focus-settle-redraw"}
	for _, reason := range tests {
		t.Run(reason, func(t *testing.T) {
			m := NewModelWithSize(nil, 120, 40)
			m.SetFocusResizeFreezeEnabled(true)
			m.focusResizeGeneration = 7
			now := time.Now()
			m.lastForegroundAt = now.Add(-2 * time.Second)
			m.lastHostRedrawAt = now.Add(-hostRedrawMinInterval / 2)
			m.lastHostRedrawReason = reason

			cmd := m.handlePostFocusSettleRedraw(postFocusSettleRedrawMsg{generation: 7, fallback: true})
			if cmd == nil {
				t.Fatal("fallback redraw should still run after weak post-focus redraw")
			}
			if m.lastHostRedrawReason != "post-focus-settle-fallback" {
				t.Fatalf("lastHostRedrawReason = %q, want post-focus-settle-fallback", m.lastHostRedrawReason)
			}
		})
	}
}

func TestFocusResizeSettleArmsPostFocusFallback(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.SetFocusResizeFreezeEnabled(true)
	m.focusResizeFrozen = true
	m.focusResizeGeneration = 11

	cmd := m.handleFocusResizeSettle(focusResizeSettleMsg{generation: 11})
	if cmd == nil {
		t.Fatal("focus-settle should schedule redraw commands")
	}
	if m.focusResizeFrozen {
		t.Fatal("focus-settle should unfreeze resize handling")
	}
	if !m.streamFlushScheduled {
		t.Fatal("focus-settle should schedule a near-immediate stream flush for proactive full-frame replay")
	}

	events := m.snapshotTUIDiagnosticEvents()
	found := false
	for _, evt := range events {
		if evt.Kind == "post-focus-settle-fallback-arm" && strings.Contains(evt.Detail, "generation=11") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("recent events missing post-focus-settle-fallback-arm: %#v", events)
	}
}

func TestPostFocusSettleRedrawDoesNotSuppressLaterFallback(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.SetFocusResizeFreezeEnabled(true)
	m.focusResizeGeneration = 9
	now := time.Now()
	m.lastForegroundAt = now
	m.lastHostRedrawAt = now.Add(-time.Second)

	cmd := m.handlePostFocusSettleRedraw(postFocusSettleRedrawMsg{generation: 9})
	if cmd == nil {
		t.Fatal("post-focus settle redraw should schedule a strong host redraw")
	}
	if m.lastHostRedrawReason != "post-focus-settle-redraw" {
		t.Fatalf("lastHostRedrawReason = %q, want post-focus-settle-redraw", m.lastHostRedrawReason)
	}

	m.lastForegroundAt = now
	fallback := m.handlePostFocusSettleRedraw(postFocusSettleRedrawMsg{generation: 9, fallback: true})
	if fallback == nil {
		t.Fatal("fallback should still run after post-focus-settle-redraw when no later strong redraw replaced it")
	}
	if m.lastHostRedrawReason != "post-focus-settle-fallback" {
		t.Fatalf("lastHostRedrawReason = %q, want post-focus-settle-fallback", m.lastHostRedrawReason)
	}
}

func TestPostFocusSettleFallbackTriggersStrongHostRedraw(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.SetFocusResizeFreezeEnabled(true)
	m.focusResizeGeneration = 7
	now := time.Now()
	m.lastForegroundAt = now
	m.lastHostRedrawAt = now.Add(-time.Second)

	cmd := m.handlePostFocusSettleRedraw(postFocusSettleRedrawMsg{generation: 7, fallback: true})
	if cmd == nil {
		t.Fatal("fallback redraw should schedule a host redraw")
	}
	if m.lastHostRedrawReason != "post-focus-settle-fallback" {
		t.Fatalf("lastHostRedrawReason = %q, want post-focus-settle-fallback", m.lastHostRedrawReason)
	}

	cmds := m.hostRedrawSequence("post-focus-settle-fallback")
	if len(cmds) != 3 {
		t.Fatalf("post-focus-settle-fallback redraw cmd count = %d, want 3", len(cmds))
	}
	if !containsCmd(cmds, tea.ClearScreen) {
		t.Fatal("post-focus-settle-fallback redraw should include ClearScreen")
	}
	if !containsCmd(cmds, tea.RequestWindowSize) {
		t.Fatal("post-focus-settle-fallback redraw should include RequestWindowSize")
	}
}

func TestPostHostRedrawFallbackDedupesSameReasonAndGeneration(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.SetFocusResizeFreezeEnabled(true)
	now := time.Now()
	m.lastForegroundAt = now.Add(-time.Second)

	first := m.maybePostHostRedrawFallbackCmd("scroll-flush", 7, now)
	if first == nil {
		t.Fatal("first fallback should be armed")
	}
	if second := m.maybePostHostRedrawFallbackCmd("scroll-flush", 7, now); second != nil {
		t.Fatalf("duplicate fallback should be suppressed, got %#v", second)
	}
	if different := m.maybePostHostRedrawFallbackCmd("content-boundary", 7, now); different == nil {
		t.Fatal("different fallback reason should still be armed")
	}
	if nextGeneration := m.maybePostHostRedrawFallbackCmd("scroll-flush", 8, now); nextGeneration == nil {
		t.Fatal("new fallback generation should be armed")
	}
}

func TestPostHostRedrawFallbackSkipsWhenNewerRedrawExists(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.SetFocusResizeFreezeEnabled(true)
	m.hostRedrawGeneration = 4

	if cmd := m.handlePostHostRedrawFallback(postHostRedrawFallbackMsg{generation: 3, reason: "scroll-flush"}); cmd != nil {
		t.Fatalf("stale fallback should skip when a newer redraw exists, got %#v", cmd)
	}
}

func TestPostHostRedrawFallbackTriggersLateScrollRedraw(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.SetFocusResizeFreezeEnabled(true)
	m.hostRedrawGeneration = 4
	m.lastHostRedrawAt = time.Now().Add(-time.Second)

	cmd := m.handlePostHostRedrawFallback(postHostRedrawFallbackMsg{generation: 4, reason: "scroll-flush"})
	if cmd == nil {
		t.Fatal("matching scroll fallback should schedule a host redraw")
	}
	if m.lastHostRedrawReason != "scroll-flush-fallback" {
		t.Fatalf("lastHostRedrawReason = %q, want scroll-flush-fallback", m.lastHostRedrawReason)
	}
}

func TestPostHostRedrawFallbackTriggersLateContentBoundaryRedraw(t *testing.T) {
	for _, reason := range []string{"content-boundary", "live-append"} {
		t.Run(reason, func(t *testing.T) {
			m := NewModelWithSize(nil, 120, 40)
			m.SetFocusResizeFreezeEnabled(true)
			m.hostRedrawGeneration = 4
			m.lastHostRedrawAt = time.Now().Add(-time.Second)

			cmd := m.handlePostHostRedrawFallback(postHostRedrawFallbackMsg{generation: 4, reason: reason})
			if cmd == nil {
				t.Fatalf("matching %s fallback should schedule a host redraw", reason)
			}
			if m.lastHostRedrawReason != "content-boundary-fallback" {
				t.Fatalf("lastHostRedrawReason = %q, want content-boundary-fallback", m.lastHostRedrawReason)
			}
		})
	}
}

func TestHostRedrawForContentBoundaryCmdReturnsFallbackWhenThrottledNearFocusRestore(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.SetFocusResizeFreezeEnabled(true)
	m.displayState = stateForeground
	now := time.Now()
	m.lastForegroundAt = now.Add(-time.Second)
	m.lastHostRedrawAt = now.Add(-m.contentBoundaryHostRedrawMinInterval() / 2)
	m.lastHostRedrawReason = "focus-restore"
	m.hostRedrawGeneration = 7

	cmd := m.hostRedrawForContentBoundaryCmd("content-boundary")
	if cmd == nil {
		t.Fatal("throttled content-boundary should arm a fallback near focus restore")
	}
	if m.lastHostRedrawReason != "focus-restore" {
		t.Fatalf("lastHostRedrawReason = %q, want unchanged focus-restore", m.lastHostRedrawReason)
	}
	if m.hostRedrawGeneration != 7 {
		t.Fatalf("hostRedrawGeneration = %d, want unchanged 7", m.hostRedrawGeneration)
	}
}

func TestBackgroundDirtyFocusRedrawDefersUntilFocusSettleWhenFrozen(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.SetFocusResizeFreezeEnabled(true)
	m.displayState = stateBackground
	m.focusResizeFrozen = true
	m.lastBackgroundAt = time.Now().Add(-time.Second)
	m.markBackgroundDirty("agent-event")
	if !m.backgroundDirty {
		t.Fatal("background dirty should be recorded while backgrounded")
	}

	_ = m.handleFocusMsg()
	if !m.backgroundDirty {
		t.Fatal("background dirty should stay pending while focus resize is frozen")
	}
	if m.lastHostRedrawReason == "background-dirty-focus" {
		t.Fatal("background dirty redraw should not run before focus-settle unfreezes")
	}

	m.focusResizeGeneration = 3
	settle := m.handleFocusResizeSettle(focusResizeSettleMsg{generation: 3})
	if settle == nil {
		t.Fatal("focus-settle should schedule redraw commands")
	}
	if m.backgroundDirty {
		t.Fatal("background dirty should be consumed on focus-settle")
	}
	if m.lastHostRedrawReason == "background-dirty-focus" {
		t.Fatalf("focus-settle should fold background-dirty recovery into its own redraw, got %q", m.lastHostRedrawReason)
	}

	events := m.snapshotTUIDiagnosticEvents()
	found := false
	for _, evt := range events {
		if evt.Kind == "background-dirty-focus-redraw" && strings.Contains(evt.Detail, "stage=focus-settle") && strings.Contains(evt.Detail, "issue_host_redraw=false") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("recent events missing folded background-dirty focus-settle redraw marker: %#v", events)
	}
}

func TestBackgroundDirtyFocusRedrawConsumesWithoutHostRedrawWhenRequested(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.SetFocusResizeFreezeEnabled(true)
	m.displayState = stateForeground
	m.markBackgroundDirty("agent-event")

	cmd := m.consumeBackgroundDirtyFocusRedrawWithOptions("focus-settle", time.Now(), false)
	if cmd != nil {
		t.Fatalf("expected no host redraw command when issueHostRedraw=false, got %#v", cmd)
	}
	if m.backgroundDirty {
		t.Fatal("background dirty should be cleared even when host redraw is folded into another recovery path")
	}
	if m.lastHostRedrawReason != "" {
		t.Fatalf("lastHostRedrawReason = %q, want empty", m.lastHostRedrawReason)
	}
}

func TestBackgroundDirtyFocusRedrawConsumesDirtyImmediatelyWithoutFreeze(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.SetFocusResizeFreezeEnabled(false)
	m.displayState = stateBackground
	m.lastBackgroundAt = time.Now().Add(-time.Second)
	m.markBackgroundDirty("agent-event")
	if !m.backgroundDirty {
		t.Fatal("background dirty should be recorded while backgrounded")
	}

	_ = m.handleFocusMsg()
	if m.backgroundDirty {
		t.Fatal("background dirty should be consumed on focus")
	}
	if m.lastHostRedrawReason == "background-dirty-focus" {
		t.Fatalf("host redraw should be skipped when focus resize mitigation is disabled, got %q", m.lastHostRedrawReason)
	}
}

func TestPostHostRedrawFallbackTriggersBackgroundDirtyFocusFallback(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.SetFocusResizeFreezeEnabled(true)
	m.hostRedrawGeneration = 4
	m.lastHostRedrawAt = time.Now().Add(-time.Second)

	cmd := m.handlePostHostRedrawFallback(postHostRedrawFallbackMsg{generation: 4, reason: "background-dirty-focus"})
	if cmd == nil {
		t.Fatal("matching background-dirty-focus fallback should schedule a host redraw")
	}
	if m.lastHostRedrawReason != "background-dirty-focus-fallback" {
		t.Fatalf("lastHostRedrawReason = %q, want background-dirty-focus-fallback", m.lastHostRedrawReason)
	}
}

func TestHostRedrawSequenceRequestsWindowSizeOnlyForStrongFocusRecovery(t *testing.T) {
	tests := []string{"focus-restore", "post-focus-settle-redraw", "post-focus-settle-fallback"}
	for _, reason := range tests {
		t.Run(reason, func(t *testing.T) {
			m := NewModelWithSize(nil, 80, 24)
			cmds := m.hostRedrawSequence(reason)
			if len(cmds) != 3 {
				t.Fatalf("%s redraw cmd count = %d, want 3", reason, len(cmds))
			}
			if !containsCmd(cmds, tea.ClearScreen) {
				t.Fatalf("%s redraw should include ClearScreen", reason)
			}
			if !containsCmd(cmds, tea.RequestWindowSize) {
				t.Fatalf("%s redraw should include RequestWindowSize", reason)
			}
		})
	}
}
