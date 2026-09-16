package tui

import (
	"strings"
	"testing"
)

// BenchmarkViewportDropOffScreenCachesLargeTranscript guards the background
// idle sweep's contract: reclaiming an off-screen block must only drop cache
// references and read the cached span. The viewport position cache is
// materialized before the timer starts, so a sweep that measures or renders a
// block instead of reading the cached span shows up here as a jump from
// microseconds to tens of milliseconds on a transcript this size — and keeps
// paying it on every iteration, because the sweep drops the very cache it would
// have just built.
func BenchmarkViewportDropOffScreenCachesLargeTranscript(b *testing.B) {
	const blockCount = 400
	v := NewViewport(120, 40)
	for i := range blockCount {
		v.AppendBlock(&Block{ID: i, Type: BlockAssistant, Content: strings.Repeat("body line with some text\n", 6)})
	}
	// Appending sticks the viewport to the newest block; sweep from the top so
	// the retained window is the small visible one.
	v.sticky = false
	v.offset = 0
	v.clampOffset()
	_ = v.blockStarts()

	b.ReportAllocs()
	for b.Loop() {
		v.DropOffScreenCaches()
	}
}
