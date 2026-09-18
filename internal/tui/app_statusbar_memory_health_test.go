package tui

import (
	"strings"
	"testing"
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
