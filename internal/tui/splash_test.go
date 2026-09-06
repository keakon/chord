package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// Viewports that select each wordmark variant.
const (
	splashWideW, splashWideH     = 120, 40
	splashNarrowW, splashNarrowH = 30, 20
)

// splashPlain strips styling from a block of rendered rows.
func splashPlain(rows []string) []string {
	out := make([]string, len(rows))
	for i, row := range rows {
		out[i] = stripANSI(row)
	}
	return out
}

// splashLitColumns reports, per column, how many rows have ink there.
func splashLitColumns(rows []string) []int {
	width := 0
	for _, row := range splashPlain(rows) {
		width = max(width, len([]rune(row)))
	}
	lit := make([]int, width)
	for _, row := range splashPlain(rows) {
		for x, r := range []rune(row) {
			if r != ' ' {
				lit[x]++
			}
		}
	}
	return lit
}

func splashInk(rows []string) int {
	total := 0
	for _, n := range splashLitColumns(rows) {
		total += n
	}
	return total
}

func TestSplashArtLoads(t *testing.T) {
	art := loadSplashArt()
	if art == nil {
		t.Fatal("embedded splash_art.txt failed to parse; regenerate with assets/logo/build_splash_art.py")
	}
	if len(art.unitEnd) != splashUnitSteps {
		t.Fatalf("art has %d reveal units, want %d", len(art.unitEnd), splashUnitSteps)
	}
	for row := range art.glyph {
		for name, rows := range map[string][]string{"fg": art.fg, "bg": art.bg} {
			if got := len([]rune(rows[row])); got != art.width {
				t.Fatalf("%s row %d has %d cells, want %d", name, row, got, art.width)
			}
		}
	}
	// Reveal units must be ordered and reach the far edge, or the last letters
	// would never appear.
	for i, end := range art.unitEnd {
		if i > 0 && end <= art.unitEnd[i-1] {
			t.Fatalf("reveal unit %d ends at %d, not after %d", i, end, art.unitEnd[i-1])
		}
	}
	if art.unitEnd[len(art.unitEnd)-1] != art.width-1 {
		t.Fatalf("last reveal unit ends at %d, want %d", art.unitEnd[len(art.unitEnd)-1], art.width-1)
	}
}

func TestSplashPicksVariantByViewport(t *testing.T) {
	if splashArtFor(splashWideW, splashWideH) == nil {
		t.Fatal("a 120x40 viewport should use the large wordmark")
	}
	if splashArtFor(splashNarrowW, splashNarrowH) != nil {
		t.Fatal("a 30x20 viewport is too narrow for the large wordmark")
	}
	// Short-but-wide must fall back too, or the art crowds out the hints.
	if splashArtFor(splashWideW, 12) != nil {
		t.Fatal("a 12-row viewport is too short for the large wordmark")
	}
}

// Every frame must keep the block's width and line count, or the centred
// wordmark visibly jitters while it types itself out.
func TestSplashFrameGeometryIsStable(t *testing.T) {
	for _, tc := range []struct {
		name string
		w, h int
	}{
		{"large", splashWideW, splashWideH},
		{"compact", splashNarrowW, splashNarrowH},
	} {
		t.Run(tc.name, func(t *testing.T) {
			total := splashStepCount(tc.w, tc.h)
			wantLines, wantSwash := splashFrame(tc.w, tc.h, total)
			for step := 0; step <= total; step++ {
				lines, swash := splashFrame(tc.w, tc.h, step)
				if len(lines) != len(wantLines) {
					t.Fatalf("step %d: %d wordmark lines, want %d", step, len(lines), len(wantLines))
				}
				for i, line := range lines {
					if got, want := ansi.StringWidth(stripANSI(line)), ansi.StringWidth(stripANSI(wantLines[i])); got != want {
						t.Fatalf("step %d line %d: width %d, want %d", step, i, got, want)
					}
				}
				if len(swash) != len(wantSwash) {
					t.Fatalf("step %d: %d swash rows, want %d", step, len(swash), len(wantSwash))
				}
				for i, row := range swash {
					if got, want := ansi.StringWidth(stripANSI(row)), ansi.StringWidth(stripANSI(wantSwash[i])); got != want {
						t.Fatalf("step %d swash row %d: width %d, want %d", step, i, got, want)
					}
				}
			}
		})
	}
}

// The word finishes before the swash starts, and both only ever grow.
func TestSplashRevealOrder(t *testing.T) {
	for _, tc := range []struct {
		name string
		w, h int
	}{
		{"large", splashWideW, splashWideH},
		{"compact", splashNarrowW, splashNarrowH},
	} {
		t.Run(tc.name, func(t *testing.T) {
			art := splashArtFor(tc.w, tc.h)
			total := splashStepCount(tc.w, tc.h)

			if _, swash := splashFrame(tc.w, tc.h, splashUnitSteps); splashInk(swash) != 0 {
				t.Fatalf("swash started before the word finished: %q", splashPlain(swash))
			}
			if got, want := splashWordCols(art, splashUnitSteps), splashSwashWidth(art); got != want {
				t.Fatalf("word covers %d columns after its last unit, want %d", got, want)
			}

			prevWord, prevSwash := -1, -1
			for step := 0; step <= total; step++ {
				lines, swash := splashFrame(tc.w, tc.h, step)
				word := len(strings.TrimRight(stripANSI(lines[len(lines)-1]), " "))
				inked := splashInk(swash)
				if word < prevWord || inked < prevSwash {
					t.Fatalf("step %d reveal went backwards (word %d->%d, swash %d->%d)", step, prevWord, word, prevSwash, inked)
				}
				prevWord, prevSwash = word, inked
			}
			// The finished swash is a continuous curve: every column carries
			// exactly one lit pixel, so the arc can never break apart as it
			// steps between rows.
			_, finalSwash := splashFrame(tc.w, tc.h, total)
			for x, n := range splashLitColumns(finalSwash) {
				if n != 1 {
					t.Fatalf("final swash column %d has %d lit rows, want 1: %q", x, n, splashPlain(finalSwash))
				}
			}
		})
	}
}

func TestSplashCompactMatchesLogoText(t *testing.T) {
	lines, swash := splashFrame(splashNarrowW, splashNarrowH, splashStepCount(splashNarrowW, splashNarrowH))
	if len(lines) != 1 || stripANSI(lines[0]) != splashWord+splashNote {
		t.Fatalf("compact wordmark = %q, want %q", stripANSI(lines[0]), splashWord+splashNote)
	}
	if len(swash) != 1 {
		t.Fatalf("compact swash should be one row, got %d", len(swash))
	}
	plain := stripANSI(swash[0])
	if !strings.HasPrefix(plain, splashSwashEnd) || !strings.HasSuffix(plain, splashSwashTip) {
		t.Fatalf("swash %q should be capped by %q and %q", plain, splashSwashEnd, splashSwashTip)
	}
}

// The swash is drawn faster than the word is typed — that contrast is the
// whole point of the flourish.
func TestSplashSwashOutrunsTheWord(t *testing.T) {
	if splashStepDelay(splashUnitSteps+2) >= splashStepDelay(1) {
		t.Fatal("swash steps must be quicker than letter steps")
	}
	if splashStepDelay(splashUnitSteps+1) <= splashStepDelay(splashUnitSteps+2) {
		t.Fatal("the beat between word and swash should be the longest pause")
	}
}

// lipgloss centres these lines with ansi.StringWidth, which — unlike
// go-runewidth — ignores the East Asian Ambiguous width of the note and the
// box-drawing runes. The rendered wordmark must not shift with the locale.
func TestSplashIgnoresEastAsianAmbiguousWidth(t *testing.T) {
	original := runewidth.DefaultCondition.EastAsianWidth
	t.Cleanup(func() { runewidth.DefaultCondition.EastAsianWidth = original })

	runewidth.DefaultCondition.EastAsianWidth = false
	narrowLines, narrowSwash := splashFrame(splashNarrowW, splashNarrowH, splashStepCount(splashNarrowW, splashNarrowH))

	runewidth.DefaultCondition.EastAsianWidth = true
	wideLines, wideSwash := splashFrame(splashNarrowW, splashNarrowH, splashStepCount(splashNarrowW, splashNarrowH))

	if strings.Join(narrowLines, "\n") != strings.Join(wideLines, "\n") ||
		strings.Join(narrowSwash, "\n") != strings.Join(wideSwash, "\n") {
		t.Fatalf("wordmark changed with EastAsianWidth: %q/%q vs %q/%q",
			splashPlain(narrowLines), splashPlain(narrowSwash), splashPlain(wideLines), splashPlain(wideSwash))
	}
}

func TestSplashRevealDrivesToCompletionThenStops(t *testing.T) {
	m := NewModelWithSize(nil, splashWideW, splashWideH)
	m.layout = m.generateLayout(m.width, m.height)

	if cmd := m.startSplashReveal(); cmd == nil {
		t.Fatal("startSplashReveal should schedule the first tick on an empty session")
	}
	if !m.splashAnimating || m.splashStep != 0 {
		t.Fatalf("reveal should start hidden, got animating=%v step=%d", m.splashAnimating, m.splashStep)
	}

	total := splashStepCount(m.viewport.width, m.viewport.height)
	for i := 1; i < total; i++ {
		if cmd := m.handleSplashTick(); cmd == nil {
			t.Fatalf("tick %d ended the reveal early at step %d of %d", i, m.splashStep, total)
		}
	}
	if cmd := m.handleSplashTick(); cmd != nil {
		t.Fatal("the final tick must not schedule another one")
	}
	if m.splashAnimating {
		t.Fatal("reveal should be finished")
	}
	// Once stopped, the view falls back to the finished wordmark.
	if cmd := m.handleSplashTick(); cmd != nil {
		t.Fatal("ticks after completion must stay inert")
	}
}
