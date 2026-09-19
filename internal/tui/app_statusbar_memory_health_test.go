package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
)

func TestMemoryPillSurfacesDegradedHealth(t *testing.T) {
	m := NewModel(nil)

	if pills := m.appendStatusBarMemoryPill(nil, statusBarInputs{}); len(pills) != 0 {
		t.Fatalf("disabled memory rendered %#v, want no pill", pills)
	}

	pills := m.appendStatusBarMemoryPill(nil, statusBarInputs{MemoryEnabled: true})
	if len(pills) != 1 || !strings.Contains(pills[0], "MEMORY") || strings.Contains(pills[0], "FAIL") {
		t.Fatalf("enabled memory pills = %#v, want a plain MEMORY pill", pills)
	}

	// Degraded memory shows even without the enabled pill: setup can fail while
	// the config still says enabled, and that is exactly the silent stop the
	// pill must surface.
	pills = m.appendStatusBarMemoryPill(nil, statusBarInputs{MemoryDegraded: true})
	if len(pills) != 1 || !strings.Contains(pills[0], "MEMORY-FAIL") {
		t.Fatalf("degraded memory pills = %#v, want a MEMORY-FAIL pill", pills)
	}
}

// The pill is rendered from live agent health but sits behind the status bar
// caches, so both the fingerprint and the event-driven invalidation must react
// to a health flip.
func TestStatusBarFingerprintTracksMemoryHealth(t *testing.T) {
	backend := &sessionControlAgent{memoryEnabled: true}
	m := NewModelWithSize(backend, 180, 24)
	now := time.Unix(123, 0)

	before := m.statusBarFingerprint(now)
	backend.memoryDegraded = true
	if after := m.statusBarFingerprint(now); after == before {
		t.Fatal("statusBarFingerprint did not change after memory health degraded")
	}
}

func TestMemoryHealthEventRepaintsDegradedPill(t *testing.T) {
	backend := &sessionControlAgent{memoryEnabled: true}
	m := NewModelWithSize(backend, 180, 24)
	m.mode = ModeNormal
	m.rightPanelVisible = false

	if plain := stripANSI(m.renderStatusBar()); strings.Contains(plain, "MEMORY-FAIL") {
		t.Fatalf("healthy status bar = %q, should not show MEMORY-FAIL", plain)
	}

	backend.memoryDegraded = true
	m.renderCacheState.statusBarAgentSnapshotDirty = false
	m.handleAgentEvent(agentEventMsg{event: agent.MemoryHealthEvent{Degraded: true}})
	if !m.renderCacheState.statusBarAgentSnapshotDirty {
		t.Fatal("memory health event did not invalidate the draw caches")
	}
	if plain := stripANSI(m.renderStatusBar()); !strings.Contains(plain, "MEMORY-FAIL") {
		t.Fatalf("degraded status bar = %q, want MEMORY-FAIL", plain)
	}
}
