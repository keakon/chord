package main

import (
	"os"
	"runtime/debug"
	"testing"
)

// readGCPercent reports the process-wide GC target. debug.SetGCPercent returns
// the previous value, so setting a known baseline both resets and reads it.
func readGCPercent(t *testing.T, baseline int) int {
	t.Helper()
	return debug.SetGCPercent(baseline)
}

// TestDefaultGCPercentTightensTheGoDefault pins the product decision behind
// defaultGCPercent: the point of the constant is to run tighter than Go's
// default of 100, so a change back to 100 (or above) has to be deliberate.
func TestDefaultGCPercentTightensTheGoDefault(t *testing.T) {
	if defaultGCPercent >= 100 {
		t.Fatalf("defaultGCPercent = %d, want a target tighter than the Go default of 100", defaultGCPercent)
	}
}

func TestApplyDefaultGCPercentAppliesWhenGOGCUnset(t *testing.T) {
	prev := debug.SetGCPercent(100)
	t.Cleanup(func() { debug.SetGCPercent(prev) })

	// t.Setenv registers the automatic restore; Unsetenv then removes the
	// variable entirely, which is what a normal launch sees. os.Getenv cannot
	// distinguish unset from empty, so one branch covers both.
	t.Setenv("GOGC", "")
	os.Unsetenv("GOGC")

	applyDefaultGCPercent()

	if got := readGCPercent(t, 100); got != defaultGCPercent {
		t.Fatalf("GC percent = %d, want %d", got, defaultGCPercent)
	}
}

func TestApplyDefaultGCPercentAppliesWhenGOGCBlank(t *testing.T) {
	prev := debug.SetGCPercent(100)
	t.Cleanup(func() { debug.SetGCPercent(prev) })

	t.Setenv("GOGC", "  ")

	applyDefaultGCPercent()

	if got := readGCPercent(t, 100); got != defaultGCPercent {
		t.Fatalf("GC percent = %d, want %d", got, defaultGCPercent)
	}
}

func TestApplyDefaultGCPercentRespectsExplicitGOGC(t *testing.T) {
	prev := debug.SetGCPercent(100)
	t.Cleanup(func() { debug.SetGCPercent(prev) })

	t.Setenv("GOGC", "80")

	applyDefaultGCPercent()

	if got := readGCPercent(t, 100); got != 100 {
		t.Fatalf("GC percent = %d, want the operator-supplied GOGC to stay in effect", got)
	}
}

func TestApplyDefaultGCPercentLeavesMemoryLimitAlone(t *testing.T) {
	prevGC := debug.SetGCPercent(100)
	t.Cleanup(func() { debug.SetGCPercent(prevGC) })
	const limit = 1 << 30
	prevLimit := debug.SetMemoryLimit(limit)
	t.Cleanup(func() { debug.SetMemoryLimit(prevLimit) })

	t.Setenv("GOGC", "")

	applyDefaultGCPercent()

	if got := debug.SetMemoryLimit(-1); got != limit {
		t.Fatalf("memory limit = %d, want %d", got, limit)
	}
}
