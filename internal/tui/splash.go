package tui

import (
	_ "embed"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	tea "github.com/keakon/bubbletea/v2"
)

// Compact wordmark, mirroring the SVG wordmark (assets/logo/chord-wordmark.svg):
// "chor" in ink, a quarter note standing in for the "d", and a swash beneath
// the whole word. Used when the viewport cannot hold the large art.
const (
	splashWord      = "chor"
	splashNote      = "♩"
	splashSwashEnd  = "╰"
	splashSwashFill = "─"
	splashSwashTip  = "╯"

	// The large wordmark draws its swash with half-blocks instead. Box-drawing
	// rules are hairlines: next to two-pixel-thick block letters they all but
	// vanish, while a half-block is one pixel against the letters' two — close
	// to the 62:108 ratio the swash and the letter stems have in the SVG.
	splashSwashHigh = "▀"
	splashSwashLow  = "▄"

	// Depth of the swash arc, in half-cells below its raised ends. The SVG
	// swash is a very shallow crescent, so one half-cell tracks it closely and
	// keeps the arc on a single row. Raising this deepens the curve and costs
	// (depth+2)/2 rows.
	splashSwashDepth = 1

	// Blank rows between the letters and the swash. The art ends on the
	// letters' baseline, and without a gap the arc's raised ends touch the
	// bottom of the "c". One row is two pixels, close to the 150 units the SVG
	// leaves between the baseline and the top of the swash.
	splashSwashGapRows = 1
)

// Reveal cadence: the wordmark types itself out a glyph at a time, holds for a
// beat, then the swash is drawn under it noticeably faster — the flourish
// should read as one stroke, not as more typing. The whole run is under a
// second and stops for good once finished, so an idle session ticks nothing.
const (
	splashLetterDelay = 90 * time.Millisecond
	splashBeatDelay   = 140 * time.Millisecond
	splashSwashDelay  = 28 * time.Millisecond

	// c, h, o, r and the note, revealed one per step in both variants.
	splashUnitSteps = 5
	// The large swash spans ~50 columns; drawing it a column per step would
	// drag on far longer than the word did, so it fills in chunks instead.
	splashMaxSwashSteps = 8
)

// splashTickMsg advances the startup wordmark by one reveal step.
type splashTickMsg struct{}

// splashLayer marks which of the wordmark's two colours a cell belongs to.
const (
	splashLayerNone   = '.'
	splashLayerInk    = 'i'
	splashLayerAccent = 'a'
)

// splashArt is the large half-block wordmark rendered from the real SVG.
// See assets/logo/build_splash_art.py.
type splashArt struct {
	width int
	glyph []string
	fg    []string
	bg    []string
	// unitEnd holds the inclusive last column of each reveal unit, so the
	// animation can pop whole letters rather than wiping columns.
	unitEnd []int
}

//go:embed splash_art.txt
var splashArtSource string

var (
	splashArtOnce   sync.Once
	splashArtParsed *splashArt
)

// loadSplashArt parses the embedded art. A malformed file degrades to the
// compact wordmark rather than breaking startup.
func loadSplashArt() *splashArt {
	splashArtOnce.Do(func() {
		art := &splashArt{}
		var section *[]string
		for line := range strings.Lines(splashArtSource) {
			line = strings.TrimRight(line, "\r\n")
			switch {
			case strings.HasPrefix(line, "#"):
				continue
			case line == "[glyph]":
				section = &art.glyph
			case line == "[fg]":
				section = &art.fg
			case line == "[bg]":
				section = &art.bg
			case strings.HasPrefix(line, "size "):
				fields := strings.Fields(line)
				if len(fields) == 3 {
					art.width, _ = strconv.Atoi(fields[1])
				}
			case strings.HasPrefix(line, "units "):
				for span := range strings.FieldsSeq(strings.TrimPrefix(line, "units ")) {
					_, end, ok := strings.Cut(span, "-")
					if !ok {
						continue
					}
					n, err := strconv.Atoi(end)
					if err != nil {
						continue
					}
					art.unitEnd = append(art.unitEnd, n)
				}
			case section != nil:
				*section = append(*section, line)
			}
		}
		if art.width <= 0 || len(art.glyph) == 0 ||
			len(art.fg) != len(art.glyph) || len(art.bg) != len(art.glyph) ||
			len(art.unitEnd) != splashUnitSteps {
			return
		}
		splashArtParsed = art
	})
	return splashArtParsed
}

// splashArtFor returns the large art when the viewport can seat it with room
// to breathe, and nil when the compact wordmark should be used instead. The
// height budget covers the art, its swash, the version line and the hint
// block below it.
func splashArtFor(width, height int) *splashArt {
	art := loadSplashArt()
	if art == nil || width < art.width+4 || height < len(art.glyph)+12 {
		return nil
	}
	return art
}

// splashSwashWidth is the width the swash spans, matching the wordmark above
// it so the centred block never shifts.
func splashSwashWidth(art *splashArt) int {
	if art != nil {
		return art.width
	}
	return ansi.StringWidth(splashWord + splashNote)
}

func splashSwashSteps(art *splashArt) int {
	return min(splashSwashWidth(art), splashMaxSwashSteps)
}

// splashStepCount is the number of reveal steps in a full run.
func splashStepCount(width, height int) int {
	return splashUnitSteps + splashSwashSteps(splashArtFor(width, height))
}

// splashStepDelay is the pause before revealing the given 1-based step.
func splashStepDelay(step int) time.Duration {
	switch {
	case step <= splashUnitSteps:
		return splashLetterDelay
	case step == splashUnitSteps+1:
		return splashBeatDelay
	default:
		return splashSwashDelay
	}
}

func splashTickCmd(step int) tea.Cmd {
	return tea.Tick(splashStepDelay(step), func(time.Time) tea.Msg {
		return splashTickMsg{}
	})
}

// splashSwashRows is how many rows the swash block occupies, the baseline gap
// included.
func splashSwashRows(art *splashArt) int {
	if art == nil {
		return 1
	}
	return splashSwashGapRows + (splashSwashDepth+2)/2
}

// splashSwashDepthAt is the arc's depth at column x, in half-cells below the
// ends. A parabola gives exactly one lit pixel per column, so the curve never
// breaks — which is what rasterising the SVG crescent could not guarantee.
func splashSwashDepthAt(x, width int) int {
	if width < 2 {
		return 0
	}
	t := float64(x) / float64(width-1)
	return int(math.Round(float64(splashSwashDepth) * 4 * t * (1 - t)))
}

// splashSwashLines builds the swash and reveals its first cols columns. Hidden
// columns become spaces so the lines keep their final width from the first
// frame and the centred block never shifts.
func splashSwashLines(art *splashArt, cols int) []string {
	width := splashSwashWidth(art)
	shown := min(max(cols, 0), width)

	if art == nil {
		// The compact wordmark keeps the box-drawing swash: its letters are
		// ordinary font glyphs, so a hairline rule is the matching weight.
		var body string
		if shown > 0 {
			body = splashSwashEnd + strings.Repeat(splashSwashFill, min(shown-1, max(width-2, 0)))
			if shown == width {
				body += splashSwashTip
			}
		}
		return []string{SplashAccentStyle.Render(body) + strings.Repeat(" ", width-shown)}
	}

	rows := make([]string, splashSwashRows(art))
	for row := range rows {
		if row < splashSwashGapRows {
			rows[row] = strings.Repeat(" ", width)
			continue
		}
		var b strings.Builder
		for x := range width {
			depth := splashSwashDepthAt(x, width)
			switch {
			case x >= shown || depth/2 != row-splashSwashGapRows:
				b.WriteString(" ")
			case depth%2 == 0:
				b.WriteString(splashSwashHigh)
			default:
				b.WriteString(splashSwashLow)
			}
		}
		rows[row] = SplashAccentStyle.Render(b.String())
	}
	return rows
}

// splashCompactLine renders "chor♩" with the first shown glyphs visible.
func splashCompactLine(shown int) string {
	var b strings.Builder
	for i, r := range splashWord + splashNote {
		switch {
		case i >= shown:
			b.WriteString(" ")
		case r == []rune(splashNote)[0]:
			b.WriteString(SplashAccentStyle.Render(string(r)))
		default:
			b.WriteString(SplashStyle.Render(string(r)))
		}
	}
	return b.String()
}

// splashArtLine renders one row of the large art up to cols, grouping runs of
// equal styling so a row costs a handful of escape sequences rather than one
// per cell.
func splashArtLine(art *splashArt, row, cols int) string {
	glyph := []rune(art.glyph[row])
	fg := []rune(art.fg[row])
	bg := []rune(art.bg[row])

	var b strings.Builder
	var run strings.Builder
	var runFg, runBg rune
	flush := func() {
		if run.Len() == 0 {
			return
		}
		b.WriteString(splashCellStyle(runFg, runBg).Render(run.String()))
		run.Reset()
	}
	for x := range art.width {
		cellFg, cellBg, cell := splashLayerNone, splashLayerNone, ' '
		if x < cols && x < len(glyph) {
			cell, cellFg, cellBg = glyph[x], fg[x], bg[x]
		}
		if run.Len() > 0 && (cellFg != runFg || cellBg != runBg) {
			flush()
		}
		runFg, runBg = cellFg, cellBg
		run.WriteRune(cell)
	}
	flush()
	return b.String()
}

func splashCellStyle(fg, bg rune) lipgloss.Style {
	style := lipgloss.NewStyle().Bold(true)
	if colour := splashLayerColour(fg); colour != "" {
		style = style.Foreground(lipgloss.Color(colour))
	}
	if colour := splashLayerColour(bg); colour != "" {
		style = style.Background(lipgloss.Color(colour))
	}
	return style
}

func splashLayerColour(layer rune) string {
	switch layer {
	case splashLayerInk:
		return currentTheme.SplashFg
	case splashLayerAccent:
		return currentTheme.SplashAccentFg
	default:
		return ""
	}
}

// splashWordCols maps completed reveal units to visible wordmark columns.
func splashWordCols(art *splashArt, units int) int {
	units = min(max(units, 0), splashUnitSteps)
	if art == nil {
		return units
	}
	if units == 0 {
		return 0
	}
	return art.unitEnd[units-1] + 1
}

// splashFrame renders the wordmark for the given viewport with the first step
// reveal steps applied. A step at or past splashStepCount renders it whole.
func splashFrame(width, height, step int) (lines, swash []string) {
	art := splashArtFor(width, height)
	cols := splashWordCols(art, step)

	if art == nil {
		lines = []string{splashCompactLine(cols)}
	} else {
		lines = make([]string, len(art.glyph))
		for row := range art.glyph {
			lines[row] = splashArtLine(art, row, cols)
		}
	}

	steps := splashSwashSteps(art)
	swashCols := 0
	if done := step - splashUnitSteps; done > 0 && steps > 0 {
		swashCols = splashSwashWidth(art) * min(done, steps) / steps
	}
	return lines, splashSwashLines(art, swashCols)
}
