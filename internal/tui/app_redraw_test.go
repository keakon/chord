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
	tests := []string{"stream-flush", "scroll-flush", "scroll-flush-fallback", "post-focus-settle-fallback"}
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

	m.lastForegroundAt = time.Time{}
	if cmd := m.maybePostHostRedrawFallbackCmd("scroll-flush", 1, now); cmd != nil {
		t.Fatal("scroll fallback should not arm without a recent focus")
	}

	m.lastForegroundAt = now.Add(-scrollFlushFallbackAfterFocusWindow - time.Millisecond)
	if cmd := m.maybePostHostRedrawFallbackCmd("scroll-flush", 2, now); cmd != nil {
		t.Fatal("scroll fallback should not arm outside the post-focus window")
	}

	m.lastForegroundAt = now.Add(-time.Second)
	if cmd := m.maybePostHostRedrawFallbackCmd("scroll-flush", 3, now); cmd == nil {
		t.Fatal("scroll fallback should arm inside the post-focus window")
	}
}

func TestPostFocusSettleFallbackSkipsWhenStrongHostRedrawAlreadyRanAfterFocus(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.SetFocusResizeFreezeEnabled(true)
	m.focusResizeGeneration = 7
	now := time.Now()
	m.lastForegroundAt = now
	m.lastHostRedrawAt = now.Add(100 * time.Millisecond)
	m.lastHostRedrawReason = "scroll-flush-fallback"

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

func TestPostFocusSettleFallbackTriggersHostRedrawWithoutWindowSize(t *testing.T) {
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
