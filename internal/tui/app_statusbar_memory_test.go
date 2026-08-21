package tui

import (
	"strings"
	"testing"
)

// The MEMORY pill appears in the status bar only while automatic memory
// extraction is enabled, mirroring the LOOP/YOLO pills.
func TestStatusBarMemoryPillAppearsWhenEnabled(t *testing.T) {
	m := NewModelWithSize(&sessionControlAgent{memoryEnabled: true}, 120, 24)
	got := stripANSI(m.renderStatusBar())
	if !strings.Contains(got, "MEMORY") {
		t.Fatalf("status bar missing MEMORY pill, got:\n%s", got)
	}
}

func TestStatusBarMemoryPillHiddenWhenDisabled(t *testing.T) {
	m := NewModelWithSize(&sessionControlAgent{}, 120, 24)
	got := stripANSI(m.renderStatusBar())
	if strings.Contains(got, "MEMORY") {
		t.Fatalf("status bar shows MEMORY pill while disabled, got:\n%s", got)
	}
}
