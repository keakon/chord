package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
)

func TestNextEscHint(t *testing.T) {
	t.Run("insert mode", func(t *testing.T) {
		m := NewModelWithSize(nil, 120, 24)
		m.mode = ModeInsert
		m.search.State.Active = true
		m.activities["main"] = agent.AgentActivityEvent{AgentID: "main", Type: agent.ActivityConnecting}
		if got := m.nextEscHint(); got != "normal mode" {
			t.Fatalf("nextEscHint() = %q, want %q", got, "normal mode")
		}
	})

	t.Run("search mode", func(t *testing.T) {
		m := NewModelWithSize(nil, 120, 24)
		m.mode = ModeSearch
		m.search.State.Active = true
		m.activities["main"] = agent.AgentActivityEvent{AgentID: "main", Type: agent.ActivityConnecting}
		if got := m.nextEscHint(); got != "cancel search" {
			t.Fatalf("nextEscHint() = %q, want %q", got, "cancel search")
		}
	})

	nonNormalModes := []struct {
		name string
		mode Mode
	}{
		{"confirm", ModeConfirm},
		{"question", ModeQuestion},
		{"help", ModeHelp},
		{"image viewer", ModeImageViewer},
		{"model select", ModeModelSelect},
		{"session select", ModeSessionSelect},
	}
	for _, tc := range nonNormalModes {
		t.Run("non normal mode: "+tc.name, func(t *testing.T) {
			m := NewModelWithSize(nil, 120, 24)
			m.mode = tc.mode
			// even with conditions that would match in Normal mode, these modes
			// have their own nearby hint or fixed esc semantics, so the status-bar
			// esc hint must stay empty.
			m.search.State.Active = true
			m.activities["main"] = agent.AgentActivityEvent{AgentID: "main", Type: agent.ActivityConnecting}
			if got := m.nextEscHint(); got != "" {
				t.Fatalf("nextEscHint() for %s mode = %q, want empty", tc.name, got)
			}
		})
	}

	t.Run("search wins over busy", func(t *testing.T) {
		m := NewModelWithSize(nil, 120, 24)
		m.mode = ModeNormal
		m.search.State.Active = true
		m.search.State.Query = "grep"
		m.search.State.Matches = []MatchPosition{{BlockIndex: 0}}
		m.search.State.Current = 0
		m.activities["main"] = agent.AgentActivityEvent{AgentID: "main", Type: agent.ActivityConnecting}
		if got := m.nextEscHint(); got != "clear search" {
			t.Fatalf("nextEscHint() = %q, want %q", got, "clear search")
		}
	})

	t.Run("chord uses existing left hint", func(t *testing.T) {
		m := NewModelWithSize(nil, 120, 24)
		m.mode = ModeNormal
		m.chord = chordState{count: 3, op: chordG}
		if got := m.nextEscHint(); got != "" {
			t.Fatalf("nextEscHint() = %q, want empty for chord-pending state", got)
		}
	})

	t.Run("loop", func(t *testing.T) {
		m := NewModelWithSize(&sessionControlAgent{loopState: agent.LoopStateExecuting}, 120, 24)
		m.mode = ModeNormal
		if got := m.nextEscHint(); got != "" {
			t.Fatalf("nextEscHint() = %q, want empty when LOOP pill is visible", got)
		}
	})

	t.Run("busy", func(t *testing.T) {
		m := NewModelWithSize(nil, 120, 24)
		m.mode = ModeNormal
		m.activities["main"] = agent.AgentActivityEvent{AgentID: "main", Type: agent.ActivityConnecting}
		if got := m.nextEscHint(); got != "cancel turn" {
			t.Fatalf("nextEscHint() = %q, want %q", got, "cancel turn")
		}
	})

	t.Run("idle", func(t *testing.T) {
		m := NewModelWithSize(nil, 120, 24)
		m.mode = ModeNormal
		if got := m.nextEscHint(); got != "" {
			t.Fatalf("nextEscHint() = %q, want empty", got)
		}
	})
}

func TestStatusBarFingerprintChangesForEscHintInputs(t *testing.T) {
	m := NewModelWithSize(nil, 120, 24)
	m.mode = ModeNormal
	now := time.Unix(1, 0)
	before := m.statusBarFingerprint(now)
	m.search.State.Active = true
	m.search.State.Query = "grep"
	m.search.State.Matches = []MatchPosition{{BlockIndex: 0}}
	m.search.State.Current = 0
	afterSearch := m.statusBarFingerprint(now)
	if before == afterSearch {
		t.Fatal("statusBarFingerprint did not change after enabling search esc hint input")
	}

	m2 := NewModelWithSize(&sessionControlAgent{loopState: agent.LoopStateExecuting}, 120, 24)
	m2.mode = ModeNormal
	loopOn := m2.statusBarFingerprint(now)
	m2.agent = &sessionControlAgent{}
	loopOff := m2.statusBarFingerprint(now)
	if loopOn == loopOff {
		t.Fatal("statusBarFingerprint did not change after loop state changed")
	}
}

func TestRenderStatusBarShowsBusyEscHint(t *testing.T) {
	m := NewModelWithSize(nil, 160, 24)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{AgentID: "main", Type: agent.ActivityConnecting}
	rendered := stripANSI(m.renderStatusBar())
	if !strings.Contains(rendered, "esc ⇢ cancel turn") {
		t.Fatalf("status bar = %q, want busy esc hint", rendered)
	}
}

func TestRenderStatusBarShowsSearchModeEscHint(t *testing.T) {
	m := NewModelWithSize(nil, 160, 24)
	m.mode = ModeSearch
	rendered := stripANSI(m.renderStatusBar())
	if !strings.Contains(rendered, "esc ⇢ cancel search") {
		t.Fatalf("status bar = %q, want search-mode esc hint", rendered)
	}
}

func TestRenderStatusBarChordDoesNotDuplicateEscHint(t *testing.T) {
	m := NewModelWithSize(nil, 160, 24)
	m.mode = ModeNormal
	m.chord = chordState{count: 3, op: chordG}
	rendered := stripANSI(m.renderStatusBar())
	if !strings.Contains(rendered, "3g") {
		t.Fatalf("status bar = %q, want chord buffer", rendered)
	}
	if strings.Contains(rendered, "esc ⇢ clear") {
		t.Fatalf("status bar = %q, did not want duplicated chord esc hint", rendered)
	}
}
