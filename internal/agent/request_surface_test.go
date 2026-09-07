package agent

import "testing"

// TestFallbackSurfaceDecisionsAreCounted covers the telemetry half of the
// fallback boundary: a rebuild that fires on every request and one that never
// fires are indistinguishable without these counters.
func TestFallbackSurfaceDecisionsAreCounted(t *testing.T) {
	a := &MainAgent{}
	a.noteFallbackSurfaceDecision(true)
	a.noteFallbackSurfaceDecision(false)
	a.noteFallbackSurfaceDecision(false)
	rebuilt, reused := a.FallbackSurfaceCounts()
	if rebuilt != 1 || reused != 2 {
		t.Fatalf("FallbackSurfaceCounts() = (%d, %d), want (1, 2)", rebuilt, reused)
	}
}
