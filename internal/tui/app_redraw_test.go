package tui

import (
	"reflect"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func containsCmd(cmds []tea.Cmd, target tea.Cmd) bool {
	for _, cmd := range cmds {
		if reflect.ValueOf(cmd).Pointer() == reflect.ValueOf(target).Pointer() {
			return true
		}
	}
	return false
}

func TestHostRedrawSequenceSkipsWindowSizeForFlushReasons(t *testing.T) {
	tests := []string{"stream-flush", "scroll-flush", "scroll-flush-fallback"}
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

func TestMaybePostHostRedrawFallbackCmdOnlyArmsNearFocusRestore(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	now := time.Now()

	for _, reason := range []string{"scroll-flush", "content-boundary", "live-append"} {
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

func TestPostFocusSettleFallbackSkipsWhenStrongHostRedrawAlreadyRanAfterFocus(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.SetFocusResizeFreezeEnabled(true)
	m.focusResizeGeneration = 7
	now := time.Now()
	m.lastForegroundAt = now
	m.lastHostRedrawAt = now.Add(100 * time.Millisecond)
	m.lastHostRedrawReason = "post-focus-settle-redraw"

	if cmd := m.handlePostFocusSettleRedraw(postFocusSettleRedrawMsg{generation: 7, fallback: true}); cmd != nil {
		t.Fatalf("fallback redraw should skip when strong host redraw already ran after focus, got %#v", cmd)
	}
}

func TestPostFocusSettleFallbackIgnoresWeakHostRedrawAfterFocus(t *testing.T) {
	tests := []string{"content-boundary", "live-append", "scroll-flush", "stream-flush"}
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

func TestPostFocusSettleRedrawSuppressesLaterFallback(t *testing.T) {
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
	if fallback := m.handlePostFocusSettleRedraw(postFocusSettleRedrawMsg{generation: 9, fallback: true}); fallback != nil {
		t.Fatalf("fallback should skip after strong post-focus settle redraw, got %#v", fallback)
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
	m := NewModelWithSize(nil, 120, 40)
	m.SetFocusResizeFreezeEnabled(true)
	m.hostRedrawGeneration = 4
	m.lastHostRedrawAt = time.Now().Add(-time.Second)

	cmd := m.handlePostHostRedrawFallback(postHostRedrawFallbackMsg{generation: 4, reason: "content-boundary"})
	if cmd == nil {
		t.Fatal("matching content-boundary fallback should schedule a host redraw")
	}
	if m.lastHostRedrawReason != "content-boundary-fallback" {
		t.Fatalf("lastHostRedrawReason = %q, want content-boundary-fallback", m.lastHostRedrawReason)
	}
}

func TestHostRedrawForContentBoundaryCmdReturnsFallbackWhenThrottledNearFocusRestore(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.SetFocusResizeFreezeEnabled(true)
	m.displayState = stateForeground
	now := time.Now()
	m.lastForegroundAt = now.Add(-time.Second)
	m.lastHostRedrawAt = now.Add(-time.Second)
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
	if m.lastHostRedrawReason != "background-dirty-focus" {
		t.Fatalf("lastHostRedrawReason = %q, want background-dirty-focus", m.lastHostRedrawReason)
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

func TestHostRedrawSequenceKeepsWindowSizeForNonFlushReasons(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	cmds := m.hostRedrawSequence("focus-restore")
	if len(cmds) != 3 {
		t.Fatalf("focus-restore redraw cmd count = %d, want 3", len(cmds))
	}
	if !containsCmd(cmds, tea.ClearScreen) {
		t.Fatal("focus-restore redraw should include ClearScreen")
	}
	if !containsCmd(cmds, tea.RequestWindowSize) {
		t.Fatal("focus-restore redraw should include RequestWindowSize")
	}
}
